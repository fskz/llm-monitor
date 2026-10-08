package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"llm-monitor/internal/store"
)

// API parameter limits (design.md §5).
const (
	defaultResultLimit = 50
	maxResultLimit     = 500
)

// statsView is the JSON shape of GET /api/stats and the embedded "stats"
// field of GET /api/providers (web/app.js renderStats). Percentages are nil
// when there is no sample — the panel shows "暂无样本" instead of 0%/100%
// (REQUIREMENTS.md §3.4).
type statsView struct {
	Samples    int      `json:"samples"`
	OK         int      `json:"ok"`
	Timeout    int      `json:"timeout"`
	ErrorCount int      `json:"error"`
	OKPct      *float64 `json:"ok_pct"`
	TimeoutPct *float64 `json:"timeout_pct"`
	ErrorPct   *float64 `json:"error_pct"`
	AvgTTFTMs  *int64   `json:"avg_ttft_ms"`
	AvgTotalMs *int64   `json:"avg_total_ms"`
}

// statsViewFrom maps a store.Stats plus the ok-sample averages onto the API
// shape. Timeout merges the two timeout sub-categories; the panel keeps the
// split visible through the results table.
func statsViewFrom(st store.Stats) statsView {
	return statsView{
		Samples:    st.Samples,
		OK:         st.OK,
		Timeout:    st.TimeoutTTFT + st.TimeoutTotal,
		ErrorCount: st.ErrorCount,
		OKPct:      st.OKPct,
		TimeoutPct: st.TimeoutPct,
		ErrorPct:   st.ErrorPct,
	}
}

// bucketView maps store.SeriesBucket onto the API shape (web/app.js
// renderCharts reads ok_pct / avg_ttft_ms / avg_total_ms / samples /
// start_ms).
type bucketView struct {
	StartMs    int64    `json:"start_ms"`
	Samples    int      `json:"samples"`
	OKPct      *float64 `json:"ok_pct"`
	AvgTTFTMs  *int64   `json:"avg_ttft_ms"`
	AvgTotalMs *int64   `json:"avg_total_ms"`
}

// parseWindow validates the window parameter: 1h / 24h / 7d, default 24h
// (REQUIREMENTS.md §3.4).
func parseWindow(v string) (time.Duration, bool) {
	switch v {
	case "", "24h":
		return 24 * time.Hour, true
	case "1h":
		return time.Hour, true
	case "7d":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// resolveRevision validates the revision parameter against the provider:
// "" = current revision, "all" = every revision, a number selects that
// exact revision (REQUIREMENTS.md §10).
func (s *Server) resolveRevision(p store.Provider, v string) (int, bool) {
	switch v {
	case "", "current":
		return p.Revision, true
	case "all":
		return store.RevisionAll, true
	}
	rev, err := strconv.Atoi(v)
	if err != nil || rev <= 0 {
		return 0, false
	}
	return rev, true
}

// queryProvider parses provider + revision + window, the shared prefix of
// /api/stats, /api/series and /api/results.
func (s *Server) queryProvider(w http.ResponseWriter, r *http.Request) (store.Provider, int, time.Duration, bool) {
	idStr := r.URL.Query().Get("provider")
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "缺少或无效的 provider 参数")
		return store.Provider{}, 0, 0, false
	}
	p, exists := s.st.GetProvider(id)
	if !exists {
		writeError(w, http.StatusNotFound, "监测对象不存在")
		return store.Provider{}, 0, 0, false
	}
	revStr := r.URL.Query().Get("revision")
	rev, ok := s.resolveRevision(p, revStr)
	if !ok {
		writeError(w, http.StatusBadRequest, "无效的 revision 参数")
		return store.Provider{}, 0, 0, false
	}
	window, ok := parseWindow(r.URL.Query().Get("window"))
	if !ok {
		writeError(w, http.StatusBadRequest, "window 仅支持 1h / 24h / 7d")
		return store.Provider{}, 0, 0, false
	}
	return p, rev, window, true
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}
	view := statsViewFrom(s.st.Stats(p.ID, rev, window))
	view.AvgTTFTMs, view.AvgTotalMs = s.st.AvgOnOK(p.ID, rev, window)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}
	buckets := s.st.Series(p.ID, rev, window)
	out := make([]bucketView, len(buckets))
	for i, b := range buckets {
		out[i] = bucketView{
			StartMs:    b.StartMs,
			Samples:    b.Samples,
			OKPct:      b.OKPct,
			AvgTTFTMs:  b.AvgTTFTMs,
			AvgTotalMs: b.AvgTotalMs,
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": out})
}

// handleResults serves the paginated detail list: newest first, composite
// (started_at, seq) cursor, server-side limit cap (REQUIREMENTS.md §10).
func (s *Server) handleResults(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}

	source := r.URL.Query().Get("source")
	if source == "" {
		source = store.SourceScheduled
	}
	switch source {
	case store.SourceScheduled, store.SourceManual, store.SourceAll:
	default:
		writeError(w, http.StatusBadRequest, "source 仅支持 scheduled / manual / all")
		return
	}

	limit := defaultResultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "无效的 limit 参数")
			return
		}
		limit = min(max(n, 1), maxResultLimit)
	}

	var beforeStartedAt, beforeSeq *int64
	if v := r.URL.Query().Get("before"); v != "" {
		startedAt, seq, err := decodeCursor(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "无效的 before 游标")
			return
		}
		beforeStartedAt, beforeSeq = &startedAt, &seq
	}

	items := s.st.QueryResults(p.ID, rev, source, window, limit+1, beforeSeq, beforeStartedAt)
	var next string
	if len(items) > limit {
		last := items[limit-1] // items[limit] exists → there is a next page
		next = encodeCursor(last.StartedAt, last.Seq)
		items = items[:limit]
	}
	resp := map[string]any{"items": items}
	if next != "" {
		resp["next_cursor"] = next
	} else {
		resp["next_cursor"] = nil
	}
	writeJSON(w, http.StatusOK, resp)
}

// encodeCursor packs (started_at, seq) into the opaque pagination cursor.
func encodeCursor(startedAt, seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(
		strconv.FormatInt(startedAt, 10) + ":" + strconv.FormatInt(seq, 10)))
}

// decodeCursor is the inverse of encodeCursor.
func decodeCursor(s string) (startedAt, seq int64, err error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, 0, err
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return 0, 0, errInvalidCursor
	}
	if startedAt, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
		return 0, 0, err
	}
	if seq, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return 0, 0, err
	}
	return startedAt, seq, nil
}

var errInvalidCursor = errors.New("invalid cursor")
