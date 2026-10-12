package store

import (
	"testing"
	"time"
)

// seedMixed appends a mixed-status fixture (ok with/without usage evidence,
// failures, a cancelled row, manual rows, an older-revision row, out-of-window
// rows) so every aggregate has a non-trivial evidence set.
func seedMixed(t *testing.T, s *Store, p Provider) {
	t.Helper()
	base := time.Now().Add(-time.Minute).UnixMilli()
	i64 := func(v int64) *int64 { return &v }
	intPtr := func(v int) *int { return &v }

	// ok rows with usage evidence (decode/prefill qualify).
	for i, v := range []int64{100, 200, 300, 10000} {
		if _, err := s.AppendResult(Result{
			ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
			Source: SourceScheduled, Status: statusOK, Success: true,
			StartedAt: base + int64(i)*1000, FinishedAt: base + int64(i)*1000 + v,
			TTFTMs: i64(v), TotalMs: v,
			PromptTokens: intPtr(64), CompletionTokens: intPtr(32),
		}); err != nil {
			t.Fatalf("AppendResult: %v", err)
		}
	}
	// ok row without TTFT/usage (drops out of the TTFT and throughput sets).
	if _, err := s.AppendResult(Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: SourceScheduled, Status: statusOK, Success: true,
		StartedAt: base + 5000, FinishedAt: base + 5500, TotalMs: 500,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
	// Failures, a cancelled row (never counts) and manual rows.
	for i, st := range []string{statusHTTPError, "conn_error", statusOK, statusTimeoutTTFT} {
		src := SourceScheduled
		if i >= 2 {
			src = SourceManual
		}
		if _, err := s.AppendResult(Result{
			ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
			Source: src, Status: st, Success: st == statusOK,
			StartedAt: base + 6000 + int64(i)*1000, FinishedAt: base + 6100 + int64(i)*1000,
			TotalMs: 100,
		}); err != nil {
			t.Fatalf("AppendResult: %v", err)
		}
	}
	if _, err := s.AppendResult(Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: SourceScheduled, Status: statusCancelled,
		StartedAt: base + 10000, FinishedAt: base + 10000, TotalMs: 0,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
	// An older-revision row (filtered out at the current revision, visible
	// at RevisionAll) and a far-out-of-window row.
	if _, err := s.AppendResult(Result{
		ProviderID: p.ID, Revision: p.Revision + 1, BaseURL: p.BaseURL, Model: p.Model,
		Source: SourceScheduled, Status: statusOK, Success: true,
		StartedAt: base + 11000, FinishedAt: base + 11500, TotalMs: 500,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
	if _, err := s.AppendResult(Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: SourceScheduled, Status: statusOK, Success: true,
		StartedAt: base - 48*60*60*1000, FinishedAt: base - 48*60*60*1000 + 500, TotalMs: 500,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
}

// f64Eq / i64Eq compare nullable numerics by value (pointer identity is
// meaningless across two computations).
func f64Eq(a, b *float64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

func i64Eq(a, b *int64) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || *a == *b
}

// PERF1 equivalence: MetricsPack returns exactly what the individual
// per-metric methods return, for every combination of revision filter and
// source filter the hot paths use (A8's equivalence evidence).
func TestMetricsPackEquivalence(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "pack")
	seedMixed(t, s, p)

	for _, rev := range []int{p.Revision, RevisionAll} {
		for _, source := range []string{SourceScheduled, SourceManual, SourceAll} {
			for _, window := range []time.Duration{time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
				mp := s.MetricsPack(p.ID, rev, window, source)

				want := s.Stats(p.ID, rev, window, source)
				if mp.Stats.Samples != want.Samples || mp.Stats.OK != want.OK ||
					mp.Stats.TimeoutTTFT != want.TimeoutTTFT || mp.Stats.TimeoutTotal != want.TimeoutTotal ||
					mp.Stats.ErrorCount != want.ErrorCount ||
					!f64Eq(mp.Stats.OKPct, want.OKPct) || !f64Eq(mp.Stats.TimeoutPct, want.TimeoutPct) ||
					!f64Eq(mp.Stats.ErrorPct, want.ErrorPct) {
					t.Errorf("Stats(rev=%d,src=%s) = %+v, want %+v", rev, source, mp.Stats, want)
				}
				ttft, total := s.AvgOnOK(p.ID, rev, window, source)
				if !i64Eq(mp.AvgTTFTMs, ttft) || !i64Eq(mp.AvgTotalMs, total) {
					t.Errorf("AvgOnOK(rev=%d,src=%s) = %v/%v, want %v/%v", rev, source,
						mp.AvgTTFTMs, mp.AvgTotalMs, ttft, total)
				}
				dec, pre := s.AvgThroughput(p.ID, rev, window, source)
				if !f64Eq(mp.AvgDecodeTPS, dec) || !f64Eq(mp.AvgPrefillTPS, pre) {
					t.Errorf("AvgThroughput(rev=%d,src=%s) = %v/%v, want %v/%v", rev, source,
						mp.AvgDecodeTPS, mp.AvgPrefillTPS, dec, pre)
				}
				wp := s.ComputePercentiles(p.ID, rev, window, source)
				if !i64Eq(mp.Percentiles.TTFTP50, wp.TTFTP50) || !i64Eq(mp.Percentiles.TTFTP95, wp.TTFTP95) ||
					!i64Eq(mp.Percentiles.TotalP50, wp.TotalP50) || !i64Eq(mp.Percentiles.TotalP95, wp.TotalP95) ||
					mp.Percentiles.TTFTCount != wp.TTFTCount {
					t.Errorf("Percentiles(rev=%d,src=%s) = %+v, want %+v", rev, source, mp.Percentiles, wp)
				}
				if got, want := mp.Streaks, s.ComputeStreaks(p.ID, rev, window, source); got != want {
					t.Errorf("Streaks(rev=%d,src=%s) = %+v, want %+v", rev, source, got, want)
				}
				if !f64Eq(mp.FlipRate, s.ComputeFlipRate(p.ID, rev, window, source)) {
					t.Errorf("FlipRate(rev=%d,src=%s) = %v, want %v", rev, source,
						mp.FlipRate, s.ComputeFlipRate(p.ID, rev, window, source))
				}
				gotErr, wantErr := mp.ErrorsByKind, s.ComputeErrorBreakdown(p.ID, rev, window, source)
				if len(gotErr) != len(wantErr) {
					t.Errorf("ErrorsByKind(rev=%d,src=%s) = %+v, want %+v", rev, source, gotErr, wantErr)
				} else {
					for i := range gotErr {
						if gotErr[i] != wantErr[i] {
							t.Errorf("ErrorsByKind[%d](rev=%d,src=%s) = %+v, want %+v", i, rev, source, gotErr[i], wantErr[i])
						}
					}
				}
			}
		}
	}

	// Revisions matches the standalone method (unfiltered by design).
	gotRev, wantRev := s.MetricsPack(p.ID, RevisionAll, 24*time.Hour, SourceAll).Revisions, s.Revisions(p.ID)
	if len(gotRev) != len(wantRev) {
		t.Fatalf("Revisions = %v, want %v", gotRev, wantRev)
	}
	for i := range gotRev {
		if gotRev[i] != wantRev[i] {
			t.Fatalf("Revisions = %v, want %v", gotRev, wantRev)
		}
	}

	// Unknown provider: zero pack, no panic.
	if mp := s.MetricsPack(99999, 1, time.Hour, SourceAll); mp.Stats.Samples != 0 || mp.Streaks.Base != "" || mp.Revisions != nil {
		t.Fatalf("unknown provider pack = %+v, want zero value", mp)
	}
}
