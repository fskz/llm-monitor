package view

import (
	"testing"
	"time"

	"llm-monitor/internal/store"
)

// stubEngine is the minimal EngineStatus double.
type stubEngine struct {
	probing  map[int]bool
	storeErr string
}

func (e *stubEngine) Probing(id int) bool  { return e.probing[id] }
func (e *stubEngine) StorageError() string { return e.storeErr }

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return st
}

func seedProvider(t *testing.T, st *store.Store, mutate func(*store.Provider)) store.Provider {
	t.Helper()
	p, err := st.AddProvider(store.Provider{
		Name: "p", BaseURL: "http://127.0.0.1:9/v1", Model: "m",
		Prompt: "ping", MaxTokens: 128,
		TimeoutSec: 60, TTFTTimeoutMs: 10000, TTFTSlowMs: 2000,
		IntervalSec: 300, Enabled: true,
	})
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if mutate != nil {
		mutate(&p)
		if err := st.UpdateProvider(p); err != nil {
			t.Fatalf("UpdateProvider: %v", err)
		}
	}
	return p
}

func seedResult(t *testing.T, st *store.Store, p store.Provider, status string, startedAt int64, ttft *int64) {
	t.Helper()
	if _, err := st.AppendResult(store.Result{
		ProviderID: p.ID, Revision: p.Revision,
		BaseURL: p.BaseURL, Model: p.Model,
		Source: store.SourceScheduled, Status: status, Success: status == "ok",
		StartedAt: startedAt, FinishedAt: startedAt + 1500,
		TTFTMs: ttft, TotalMs: 1500,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
}

// TestViewOfMonitorStatusLadder is the pure-assembly mirror of the §7.2
// priority ladder (the HTTP-level matrix stays in internal/server tests).
func TestViewOfMonitorStatusLadder(t *testing.T) {
	st := newTestStore(t)
	now := time.Now().UnixMilli()

	cases := []struct {
		name       string
		mutate     func(*store.Provider)
		seed       func(p store.Provider)
		wantStatus string
		wantSlow   bool
	}{
		{"disabled", func(p *store.Provider) { p.Enabled = false }, nil, "disabled", false},
		{"manual_only", func(p *store.Provider) { p.IntervalSec = 0 }, nil, "manual_only", false},
		{"unknown", nil, nil, "unknown", false},
		{"stale", nil, func(p store.Provider) {
			seedResult(t, st, p, "ok", now-(2*300+60)*1000-5000, i64p(500))
		}, "stale", false},
		{"ok", nil, func(p store.Provider) {
			seedResult(t, st, p, "ok", now-30_000, i64p(800))
		}, "ok", false},
		{"ok_slow", nil, func(p store.Provider) {
			seedResult(t, st, p, "ok", now-30_000, i64p(5000))
		}, "ok", true},
		{"fail", nil, func(p store.Provider) {
			seedResult(t, st, p, "http_error", now-30_000, nil)
		}, "fail", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := seedProvider(t, st, func(pp *store.Provider) {
				pp.Name = c.name
				if c.mutate != nil {
					c.mutate(pp)
				}
			})
			if c.seed != nil {
				c.seed(p)
			}
			v := ViewOf(st, nil, p)
			if v.Status != c.wantStatus {
				t.Errorf("status = %q, want %q", v.Status, c.wantStatus)
			}
			if c.wantSlow && !v.SlowTTFT {
				t.Errorf("slow_ttft = false, want true")
			}
		})
	}
}

// last_probe only counts for the current revision; a target change (bump)
// resets it to unknown (§12.1-8).
func TestViewOfLastProbeRevisionScope(t *testing.T) {
	st := newTestStore(t)
	p := seedProvider(t, st, nil)
	now := time.Now().UnixMilli()
	seedResult(t, st, p, "ok", now-30_000, i64p(700))
	if v := ViewOf(st, nil, p); v.LastProbe == nil || v.Status != "ok" {
		t.Fatalf("v1: last_probe = %+v status = %q, want ok result", v.LastProbe, v.Status)
	}

	// Target change → revision 2 with no results yet.
	p2 := p
	p2.Revision = p.Revision + 1
	v := ViewOf(st, nil, p2)
	if v.LastProbe != nil {
		t.Errorf("v2: last_probe = %+v, want nil (stale revision)", v.LastProbe)
	}
	if v.Status != "unknown" {
		t.Errorf("v2: status = %q, want unknown", v.Status)
	}
	// Once a revision-2 result exists, the revisions list exposes both.
	seedResult(t, st, p2, "ok", now-10_000, i64p(300))
	v = ViewOf(st, nil, p2)
	if len(v.Revisions) != 2 || v.Revisions[0] != 1 || v.Revisions[1] != 2 {
		t.Errorf("revisions = %v, want [1 2]", v.Revisions)
	}
	if v.LastProbe == nil || v.LastProbe.TTFTMs == nil || *v.LastProbe.TTFTMs != 300 {
		t.Errorf("last_probe = %+v, want the revision-2 result", v.LastProbe)
	}
}

func TestViewOfEngineHints(t *testing.T) {
	st := newTestStore(t)
	p := seedProvider(t, st, nil)
	now := time.Now().UnixMilli()
	seedResult(t, st, p, "ok", now-30_000, i64p(700))

	eng := &stubEngine{probing: map[int]bool{p.ID: true}, storeErr: "append failed"}
	v := ViewOf(st, eng, p)
	if !v.Probing {
		t.Error("probing = false, want true")
	}
	// Probing is a hint and must not override a concrete status.
	if v.Status != "ok" {
		t.Errorf("status = %q, want ok (probing must not override)", v.Status)
	}
	if v.StorageError != "append failed" {
		t.Errorf("storage_error = %q, want engine error surfaced", v.StorageError)
	}
}

// Manual results never feed the overview stats, and a manual-only latest
// record must not be picked as last_probe (scheduled source only).
func TestViewOfIgnoresManualResults(t *testing.T) {
	st := newTestStore(t)
	p := seedProvider(t, st, nil)
	now := time.Now().UnixMilli()
	if _, err := st.AppendResult(store.Result{
		ProviderID: p.ID, Revision: p.Revision, Source: store.SourceManual,
		Status: "ok", Success: true, StartedAt: now - 1000, FinishedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	v := ViewOf(st, nil, p)
	if v.LastProbe != nil {
		t.Errorf("last_probe = %+v, want nil (manual results excluded)", v.LastProbe)
	}
	if v.Stats.Samples != 0 {
		t.Errorf("stats samples = %d, want 0 (manual excluded)", v.Stats.Samples)
	}
}

func TestSlowTTF(t *testing.T) {
	p := store.Provider{TTFTSlowMs: 2000}
	ok := &store.Result{Status: "ok", TTFTMs: i64p(5000)}
	if !SlowTTFT(p, ok) {
		t.Error("ok result with ttft 5000 > 2000: want slow")
	}
	fast := &store.Result{Status: "ok", TTFTMs: i64p(100)}
	if SlowTTFT(p, fast) {
		t.Error("ok result with ttft 100: want not slow")
	}
	noTTFT := &store.Result{Status: "ok"}
	if SlowTTFT(p, noTTFT) {
		t.Error("ok result without ttft: want not slow")
	}
	fail := &store.Result{Status: "http_error", TTFTMs: i64p(5000)}
	if SlowTTFT(p, fail) {
		t.Error("failed result is never slow (slow rides only on ok)")
	}
}

// Timeout merges the two timeout sub-categories (the split stays visible in
// the results table).
func TestStatsViewFrom(t *testing.T) {
	sv := StatsViewFrom(store.Stats{
		Samples: 10, OK: 5, TimeoutTTFT: 2, TimeoutTotal: 1, ErrorCount: 2,
	})
	if sv.Timeout != 3 {
		t.Errorf("timeout = %d, want 2+1=3", sv.Timeout)
	}
	if sv.Samples != 10 || sv.OK != 5 || sv.ErrorCount != 2 {
		t.Errorf("counts = %+v", sv)
	}
}

func TestBucketViewFrom(t *testing.T) {
	pct := 66.67
	ttft, total, dec := int64(800), int64(1500), 12.5
	b := BucketViewFrom(store.SeriesBucket{
		StartMs: 42, Samples: 3, OKPct: &pct,
		AvgTTFTMs: &ttft, AvgTotalMs: &total, AvgDecodeTPS: &dec,
	})
	if b.StartMs != 42 || b.Samples != 3 || b.OKPct != &pct ||
		b.AvgTTFTMs != &ttft || b.AvgTotalMs != &total || b.AvgDecodeTPS != &dec {
		t.Errorf("bucket = %+v", b)
	}
}

func i64p(v int64) *int64 { return &v }
