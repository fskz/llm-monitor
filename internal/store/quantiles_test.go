package store

import (
	"testing"
	"time"
)

// appendAt appends one result at a fixed timestamp with explicit fields —
// the quantiles fixtures need precise TTFT/total/status/source control.
func appendAt(t *testing.T, s *Store, p Provider, status string, started int64, ttft *int64, total int64, source string) {
	t.Helper()
	if _, err := s.AppendResult(Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: source, Status: status, Success: status == statusOK,
		StartedAt: started, FinishedAt: started + total,
		TTFTMs: ttft, TotalMs: total,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
}

func i64v(v int64) *int64 { return &v }

// A1: nearest-rank P50 (lower median) and P95; nil-TTFT samples drop out
// of the TTFT set only.
func TestComputePercentiles(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "pct")
	base := time.Now().Add(-time.Minute).UnixMilli()
	// Four ok rows with TTFT/total {100,200,300,10000}ms, oldest first.
	for i, v := range []int64{100, 200, 300, 10000} {
		appendAt(t, s, p, statusOK, base+int64(i)*1000, i64v(v), v, SourceScheduled)
	}
	// A failing row: durations never enter response-time statistics.
	appendAt(t, s, p, statusHTTPError, base+5000, i64v(50), 60, SourceScheduled)

	got := s.ComputePercentiles(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if got.TTFTCount != 4 {
		t.Fatalf("ttft count = %d, want 4", got.TTFTCount)
	}
	if *got.TTFTP50 != 200 {
		t.Fatalf("ttft p50 = %d, want 200 (lower median of 4)", *got.TTFTP50)
	}
	if *got.TTFTP95 != 10000 {
		t.Fatalf("ttft p95 = %d, want 10000", *got.TTFTP95)
	}
	if *got.TotalP50 != 200 || *got.TotalP95 != 10000 {
		t.Fatalf("total p50/p95 = %d/%d, want 200/10000", *got.TotalP50, *got.TotalP95)
	}

	// Nil-TTFT ok row: TTFT percentiles drop it, total keeps it.
	appendAt(t, s, p, statusOK, base+6000, nil, 500, SourceScheduled)
	got = s.ComputePercentiles(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if got.TTFTCount != 4 {
		t.Fatalf("ttft count after nil row = %d, want 4", got.TTFTCount)
	}
	if *got.TotalP50 != 300 { // totals now {100,200,300,500,10000} → lower median 300
		t.Fatalf("total p50 after nil row = %d, want 300", *got.TotalP50)
	}

	// Empty window → all nil (never 0).
	if e := s.ComputePercentiles(p.ID, p.Revision, time.Hour, SourceManual); e.TTFTP50 != nil || e.TotalP95 != nil {
		t.Fatalf("empty percentiles = %+v, want nils", e)
	}
}

// A2: streaks count the run ending at the newest sample; cancelled never
// breaks or joins a run; the source filter selects the sequence.
func TestComputeStreaks(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "streak")
	base := time.Now().Add(-time.Minute).UnixMilli()
	// oldest → newest: fail, fail, ok, fail(=newest)
	appendAt(t, s, p, statusHTTPError, base+1000, nil, 60, SourceScheduled)
	appendAt(t, s, p, "conn_error", base+2000, nil, 60, SourceScheduled)
	appendAt(t, s, p, statusOK, base+3000, i64v(100), 500, SourceScheduled)
	appendAt(t, s, p, statusHTTPError, base+4000, nil, 60, SourceScheduled)

	st := s.ComputeStreaks(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if st.Fail != 1 || st.OK != 0 || st.Base != statusHTTPError {
		t.Fatalf("streaks = %+v, want fail 1 base http_error", st)
	}

	// A cancelled row between failures is skipped, not a flip.
	appendAt(t, s, p, statusCancelled, base+5000, nil, 0, SourceScheduled)
	appendAt(t, s, p, statusHTTPError, base+6000, nil, 60, SourceScheduled)
	st = s.ComputeStreaks(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if st.Fail != 2 {
		t.Fatalf("streaks after cancelled gap = %+v, want fail 2 (cancelled skipped)", st)
	}

	// Manual-source view sees only the manual sequence.
	appendAt(t, s, p, statusOK, base+7000, i64v(100), 500, SourceManual)
	appendAt(t, s, p, statusOK, base+8000, i64v(100), 500, SourceManual)
	if m := s.ComputeStreaks(p.ID, p.Revision, 24*time.Hour, SourceManual); m.OK != 2 || m.Base != statusOK {
		t.Fatalf("manual streaks = %+v, want ok 2", m)
	}
}

// A3: per-status counts of non-ok samples, most frequent first.
func TestComputeErrorBreakdown(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "breakdown")
	base := time.Now().Add(-time.Minute).UnixMilli()
	for i := 0; i < 3; i++ {
		appendAt(t, s, p, statusHTTPError, base+int64(i)*1000, nil, 60, SourceScheduled)
	}
	appendAt(t, s, p, "conn_error", base+4000, nil, 60, SourceScheduled)
	appendAt(t, s, p, statusOK, base+5000, i64v(100), 500, SourceScheduled)
	appendAt(t, s, p, statusCancelled, base+6000, nil, 0, SourceScheduled) // excluded

	got := s.ComputeErrorBreakdown(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if len(got) != 2 {
		t.Fatalf("breakdown = %+v, want 2 entries", got)
	}
	if got[0].Status != statusHTTPError || got[0].Count != 3 {
		t.Fatalf("top entry = %+v, want http_error×3", got[0])
	}
	if got[1].Status != "conn_error" || got[1].Count != 1 {
		t.Fatalf("second entry = %+v, want conn_error×1", got[1])
	}

	// All-ok view → no entries at all.
	q := mustAddProvider(t, s, "allok")
	appendAt(t, s, q, statusOK, base, i64v(50), 200, SourceScheduled)
	if e := s.ComputeErrorBreakdown(q.ID, q.Revision, 24*time.Hour, SourceScheduled); len(e) != 0 {
		t.Fatalf("all-ok breakdown = %+v, want empty", e)
	}
}

// A4: flip rate over adjacent comparable pairs; <2 samples → nil.
func TestComputeFlipRate(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "flip")
	base := time.Now().Add(-time.Minute).UnixMilli()
	// ok, ok, fail, fail, ok → pairs 4, flips 2 → 0.5.
	for i, st := range []string{statusOK, statusOK, statusHTTPError, "conn_error", statusOK} {
		ttft := i64v(100)
		total := int64(500)
		if st != statusOK {
			ttft, total = nil, 60
		}
		appendAt(t, s, p, st, base+int64(i)*1000, ttft, total, SourceScheduled)
	}
	r := s.ComputeFlipRate(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if r == nil || *r != 0.5 {
		t.Fatalf("flip rate = %v, want 0.5", r)
	}

	// Uniform results → 0.
	q := mustAddProvider(t, s, "flat")
	for i := 0; i < 5; i++ {
		appendAt(t, s, q, statusOK, base+int64(i)*1000, i64v(100), 500, SourceScheduled)
	}
	if r := s.ComputeFlipRate(q.ID, q.Revision, 24*time.Hour, SourceScheduled); r == nil || *r != 0 {
		t.Fatalf("flat flip rate = %v, want 0", r)
	}

	// Single sample → nil (no adjacency evidence).
	w := mustAddProvider(t, s, "one")
	appendAt(t, s, w, statusOK, base, i64v(100), 500, SourceScheduled)
	if r := s.ComputeFlipRate(w.ID, w.Revision, 24*time.Hour, SourceScheduled); r != nil {
		t.Fatalf("single flip rate = %v, want nil", r)
	}
}
