package server

import (
	"strings"
	"time"

	"llm-monitor/internal/store"
)

// providerView is one element of GET /api/providers. Field names and shapes
// match web/app.js exactly: snake_case JSON keys, nullable percentages and
// durations, last_probe null when the current revision has no valid
// scheduled result yet.
type providerView struct {
	ID            int            `json:"id"`
	Name          string         `json:"name"`
	BaseURL       string         `json:"base_url"`
	Model         string         `json:"model"`
	Revision      int            `json:"revision"`
	Revisions     []int          `json:"revisions"`
	Prompt        string         `json:"prompt"`
	MaxTokens     int            `json:"max_tokens"`
	IntervalSec   int            `json:"interval_sec"`
	TimeoutSec    int            `json:"timeout_sec"`
	TTFTTimeoutMs int            `json:"ttft_timeout_ms"`
	TTFTSlowMs    int            `json:"ttft_slow_ms"`
	Enabled       bool           `json:"enabled"`
	IncludeUsage  bool           `json:"include_usage"`
	APIKeySet     bool           `json:"api_key_set"`
	APIKeyMask    string         `json:"api_key_mask"`
	Status        string         `json:"status"`
	SlowTTFT      bool           `json:"slow_ttft"`
	Probing       bool           `json:"probing"`
	LastProbe     *lastProbeView `json:"last_probe"`
	Stats         statsView      `json:"stats"`
	StorageError  string         `json:"storage_error"`
}

// lastProbeView is the most recent valid scheduled probe of the current
// revision (web/app.js renderOverview reads status/finished_at).
type lastProbeView struct {
	Status      string `json:"status"`
	Success     bool   `json:"success"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	TTFTMs      *int64 `json:"ttft_ms"`
	TotalMs     int64  `json:"total_ms"`
	HTTPStatus  *int   `json:"http_status"`
	ErrorDetail string `json:"error"`
}

// probeView is the response of POST /api/providers/{id}/probe: the manual
// probe result plus the slow-TTFT hint and the derived per-probe throughput
// (nil when usage evidence is missing; web/app.js manualProbe).
type probeView struct {
	*store.Result
	Slow       bool     `json:"slow"`
	DecodeTPS  *float64 `json:"decode_tps"`
	PrefillTPS *float64 `json:"prefill_tps"`
}

// viewOf assembles the overview element of one provider: monitor status
// (REQUIREMENTS.md §7.2 priority order), masked key, last scheduled probe
// and the default-window statistics.
func (s *Server) viewOf(p store.Provider) providerView {
	v := providerView{
		ID:            p.ID,
		Name:          p.Name,
		BaseURL:       p.BaseURL,
		Model:         p.Model,
		Revision:      p.Revision,
		Revisions:     s.st.Revisions(p.ID),
		Prompt:        p.Prompt,
		MaxTokens:     p.MaxTokens,
		IntervalSec:   p.IntervalSec,
		TimeoutSec:    p.TimeoutSec,
		TTFTTimeoutMs: p.TTFTTimeoutMs,
		TTFTSlowMs:    p.TTFTSlowMs,
		Enabled:       p.Enabled,
		IncludeUsage:  p.IncludeUsage,
		APIKeySet:     p.APIKey != "",
		APIKeyMask:    maskKey(p.APIKey),
		LastProbe:     nil,
	}
	if s.eng != nil {
		v.Probing = s.eng.Probing(p.ID)
		v.StorageError = s.eng.StorageError()
	}

	// last_probe only counts when it belongs to the current revision:
	// after a target change the new revision starts from "unknown"
	// (REQUIREMENTS.md §4.1, §7.2-3).
	if last := s.st.LatestValidScheduled(p.ID); last != nil && last.Revision == p.Revision {
		v.LastProbe = &lastProbeView{
			Status:      last.Status,
			Success:     last.Success,
			StartedAt:   last.StartedAt,
			FinishedAt:  last.FinishedAt,
			TTFTMs:      last.TTFTMs,
			TotalMs:     last.TotalMs,
			HTTPStatus:  last.HTTPStatus,
			ErrorDetail: last.Error,
		}
	}

	v.Status, v.SlowTTFT = monitorStatus(p, v.LastProbe)
	// Default overview window: 24h, current revision only (design.md §5).
	st := s.st.Stats(p.ID, p.Revision, 24*time.Hour)
	v.Stats = statsViewFrom(st)
	return v
}

// monitorStatus implements the priority ladder of REQUIREMENTS.md §7.2.
// slow is only meaningful for status "ok".
func monitorStatus(p store.Provider, last *lastProbeView) (status string, slow bool) {
	if !p.Enabled {
		return "disabled", false
	}
	if p.IntervalSec == 0 {
		return "manual_only", false
	}
	if last == nil {
		return "unknown", false
	}
	if time.Since(time.UnixMilli(last.FinishedAt)).Milliseconds() > staleAfter(p) {
		return "stale", false
	}
	if last.Status == "ok" {
		slow = last.TTFTMs != nil && *last.TTFTMs > int64(p.TTFTSlowMs)
		return "ok", slow
	}
	return "fail", false
}

// maskKey renders the stored key as first4+"****"+last4, or all stars when
// the key is too short to expose anything (REQUIREMENTS.md §5.4: the API
// never returns the full key).
func maskKey(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:4] + "****" + key[len(key)-4:]
}
