package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// API parameter limits (design.md §5).
const (
	defaultResultLimit = 50
	maxResultLimit     = 500
)

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

// querySource parses the source filter of /api/stats and /api/series.
// Unlike /api/results (which predates it and defaults to scheduled for
// panel-compat), the stats endpoints default to all: a direct API caller
// asking for statistics without a filter wants the full picture
// (task 10-10-stats-by-source decision).
func querySource(w http.ResponseWriter, r *http.Request) (string, bool) {
	source := r.URL.Query().Get("source")
	if source == "" {
		return store.SourceAll, true
	}
	switch source {
	case store.SourceScheduled, store.SourceManual, store.SourceAll:
		return source, true
	}
	writeError(w, http.StatusBadRequest, "source 仅支持 scheduled / manual / all")
	return "", false
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}
	source, ok := querySource(w, r)
	if !ok {
		return
	}
	sv := view.StatsViewFrom(s.st.Stats(p.ID, rev, window, source))
	sv.AvgTTFTMs, sv.AvgTotalMs = s.st.AvgOnOK(p.ID, rev, window, source)
	sv.AvgDecodeTPS, sv.AvgPrefillTPS = s.st.AvgThroughput(p.ID, rev, window, source)
	sv.FillMetricsPack(s.st, p.ID, rev, window, source)
	writeJSON(w, http.StatusOK, sv)
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}
	source, ok := querySource(w, r)
	if !ok {
		return
	}
	buckets := s.st.Series(p.ID, rev, window, source)
	out := make([]view.BucketView, len(buckets))
	for i, b := range buckets {
		out[i] = view.BucketViewFrom(b)
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
