// Package view assembles the API response shapes and presentation mappings
// shared by the HTTP panel server (internal/server) and the TUI
// (task 10-09-tui-default, design.md §1, §5).
//
// It owns:
//   - the JSON field contract consumed by web/app.js (ProviderView, StatsView,
//     BucketView, ProbeView — snake_case keys, nullable percentages),
//   - the status label tables (StatusText / MonitorText; web/app.js
//     STATUS_TEXT / STATUS_META must stay in sync — go-conventions.md
//     cross-layer rule),
//   - the provider validation rules with their Chinese messages,
//   - the monitor status ladder of REQUIREMENTS.md §7.2,
//   - the narrow engine interfaces (EngineAPI, ProviderMutator) both
//     front ends consume; cmd adapts the concrete engine to them.
//
// Dependency direction: view → store only. It must not import
// internal/engine, internal/server or internal/tui. internal/server and
// internal/tui keep only their transport semantics (HTTP handlers /
// terminal UI) and delegate assembly here.
package view

import (
	"strings"
	"time"

	"llm-monitor/internal/store"
)

// EngineStatus is the narrow engine slice the overview assembly needs.
// nil means no engine is attached (probing hint and storage error stay off).
type EngineStatus interface {
	// Probing reports whether a probe is currently in flight for the
	// provider ("正在探测" is a hint only, it never overrides the
	// monitor status, REQUIREMENTS.md §7.2).
	Probing(id int) bool
	// StorageError returns a non-empty description of the latest
	// engine-side storage failure, or "" when healthy. The panel surfaces
	// storage failures explicitly instead of blaming the probed API (§5.5).
	StorageError() string
}

// EngineAPI is the engine slice the interactive front ends (panel server,
// TUI) need on top of the read-only status view: triggering one manual probe
// and telling its "already in flight" error apart (mapped to HTTP 409 by the
// server, to a conflict hint by the TUI). It is an interface instead of a
// concrete *engine.Engine so server and tui compile independently of
// internal/engine; cmd adapts the engine to it.
type EngineAPI interface {
	EngineStatus
	// ProbeNow runs one manual probe to completion and returns its
	// persisted result (REQUIREMENTS.md §4.6).
	ProbeNow(id int) (*store.Result, error)
	// IsInFlight reports whether err means "a probe is already running".
	IsInFlight(err error) bool
}

// ProviderMutator forwards configuration changes to the engine so it can
// cancel in-flight probes and reschedule. New accepts nil when no engine is
// wired; cancellation then simply does not happen, which is safe for tests.
type ProviderMutator interface {
	Add(p store.Provider)
	Update(p store.Provider)
	Remove(id int)
}

// ProviderView is one element of GET /api/providers. Field names and shapes
// match web/app.js exactly: snake_case JSON keys, nullable percentages and
// durations, last_probe null when the current revision has no valid
// scheduled result yet.
type ProviderView struct {
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
	LastProbe     *LastProbeView `json:"last_probe"`
	Stats         StatsView      `json:"stats"`
	StorageError  string         `json:"storage_error"`
}

// LastProbeView is the most recent valid scheduled probe of the current
// revision (web/app.js renderOverview reads status/finished_at).
type LastProbeView struct {
	Status      string `json:"status"`
	Success     bool   `json:"success"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	TTFTMs      *int64 `json:"ttft_ms"`
	TotalMs     int64  `json:"total_ms"`
	HTTPStatus  *int   `json:"http_status"`
	ErrorDetail string `json:"error"`
}

// ProbeView is the response of POST /api/providers/{id}/probe: the manual
// probe result plus the slow-TTFT hint and the derived per-probe throughput
// (nil when usage evidence is missing; web/app.js manualProbe).
type ProbeView struct {
	*store.Result
	Slow       bool     `json:"slow"`
	DecodeTPS  *float64 `json:"decode_tps"`
	PrefillTPS *float64 `json:"prefill_tps"`
}

// ViewOf assembles the overview element of one provider: monitor status
// (REQUIREMENTS.md §7.2 priority order), masked key, last scheduled probe
// and the default-window statistics. eng may be nil.
func ViewOf(st *store.Store, eng EngineStatus, p store.Provider) ProviderView {
	v := ProviderView{
		ID:            p.ID,
		Name:          p.Name,
		BaseURL:       p.BaseURL,
		Model:         p.Model,
		Revision:      p.Revision,
		Revisions:     st.Revisions(p.ID),
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
	if eng != nil {
		v.Probing = eng.Probing(p.ID)
		v.StorageError = eng.StorageError()
	}

	// last_probe only counts when it belongs to the current revision:
	// after a target change the new revision starts from "unknown"
	// (REQUIREMENTS.md §4.1, §7.2-3).
	if last := st.LatestValidScheduled(p.ID); last != nil && last.Revision == p.Revision {
		v.LastProbe = &LastProbeView{
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
	// Default overview window: 24h, current revision, scheduled probes —
	// the overview card is schedule health and does NOT follow the detail
	// view's source filter (task 10-10-stats-by-source).
	v.Stats = StatsViewFrom(st.Stats(p.ID, p.Revision, 24*time.Hour, store.SourceScheduled))
	return v
}

// monitorStatus implements the priority ladder of REQUIREMENTS.md §7.2.
// slow is only meaningful for status "ok".
func monitorStatus(p store.Provider, last *LastProbeView) (status string, slow bool) {
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

// SlowTTFT reports whether a successful probe exceeded the slow threshold;
// it is a hint on top of "ok", never a separate status (REQUIREMENTS.md
// §4.4).
func SlowTTFT(p store.Provider, res *store.Result) bool {
	return res.Status == "ok" && res.TTFTMs != nil && *res.TTFTMs > int64(p.TTFTSlowMs)
}

// staleAfter is the freshness limit of the last scheduled probe:
// 2×interval + timeout, in milliseconds (REQUIREMENTS.md §7.2-4).
func staleAfter(p store.Provider) int64 {
	return (int64(2*p.IntervalSec) + int64(p.TimeoutSec)) * 1000
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
