package store

import (
	"sort"
	"time"
)

// Aggregation-layer metrics beyond the ratio/average set (task
// 10-10-metrics-pack): percentiles, result streaks, the error-status
// breakdown and the flip ("jitter") rate. All four derive purely from the
// in-memory record index under the same (revision, window, source) filter
// family as Stats/AvgOnOK — cancelled never counts, and every value is nil
// (never 0) when its evidence set is empty.

// Percentiles holds the nearest-rank P50/P95 of the ok samples.
type Percentiles struct {
	TTFTP50   *int64
	TTFTP95   *int64
	TotalP50  *int64
	TotalP95  *int64
	TTFTCount int // ok samples carrying a TTFT (nil TTFTs only drop out of the TTFT set)
}

// Streaks is the run of identical results ending at the newest sample.
type Streaks struct {
	Fail int // consecutive failures at the head of history (0 when the newest run is ok)
	OK   int // consecutive successes at the head (0 when the newest run failed)
	// Base is the status of the newest non-cancelled sample ("ok" or a
	// failure status); empty when there is no sample at all.
	Base string
}

// ErrorBreakdownEntry counts one non-ok final status.
type ErrorBreakdownEntry struct {
	Status string
	Count  int
}

// ComputePercentiles returns the nearest-rank P50/P95 of TTFT and total
// duration over the window's ok samples (REQUIREMENTS.md §3.1, PRD A1).
// Nearest-rank with the lower median on even counts: rank = ceil(p·n) on a
// 1-based ascending sort — no interpolation, small samples stay honest.
func (s *Store) ComputePercentiles(providerID int, revision int, window time.Duration, source string) Percentiles {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return Percentiles{}
	}
	records := pr.snapshot()
	minStartedAt := time.Now().Add(-window).UnixMilli()

	var ttfts, totals []int64
	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if !inWindow(r, minStartedAt, source) || r.Status != statusOK {
			continue
		}
		totals = append(totals, r.TotalMs)
		if r.TTFTMs != nil {
			ttfts = append(ttfts, *r.TTFTMs)
		}
	}

	p := Percentiles{TTFTCount: len(ttfts)}
	if len(totals) > 0 {
		p.TotalP50, p.TotalP95 = rankPair(totals)
	}
	if len(ttfts) > 0 {
		p.TTFTP50, p.TTFTP95 = rankPair(ttfts)
	}
	return p
}

// rankPair sorts v ascending and returns the nearest-rank P50 (lower
// median) and P95.
func rankPair(v []int64) (p50, p95 *int64) {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	rank := func(p float64) int64 {
		// ceil(p·n) clamped to n, 1-based rank → 0-based index.
		n := float64(len(v))
		idx := int(p*n + 0.999999)
		if idx > len(v) {
			idx = len(v)
		}
		return v[idx-1]
	}
	p50, p95 = new(int64), new(int64)
	*p50, *p95 = rank(0.50), rank(0.95)
	return p50, p95
}

// ComputeStreaks counts the run of identical results ending at the newest
// non-cancelled sample, under the source filter (PRD A2). Cancelled rows
// are skipped — a cancellation is not an outcome.
func (s *Store) ComputeStreaks(providerID int, revision int, window time.Duration, source string) Streaks {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return Streaks{}
	}
	records := pr.snapshot()
	minStartedAt := time.Now().Add(-window).UnixMilli()

	// records are oldest-first; walk backwards from the newest.
	st := Streaks{}
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if r.Status == statusCancelled || r.StartedAt < minStartedAt {
			continue
		}
		if srcFiltered(r, source) {
			continue
		}
		if st.Base == "" {
			st.Base = r.Status
		}
		if r.Status == statusOK {
			if st.Base != statusOK {
				break // run ended
			}
			st.OK++
		} else {
			if st.Base == statusOK {
				break
			}
			st.Fail++
		}
	}
	return st
}

// ComputeErrorBreakdown counts the non-ok, non-cancelled samples of the
// window per final status, most frequent first (PRD A3).
func (s *Store) ComputeErrorBreakdown(providerID int, revision int, window time.Duration, source string) []ErrorBreakdownEntry {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil
	}
	records := pr.snapshot()
	minStartedAt := time.Now().Add(-window).UnixMilli()

	counts := make(map[string]int)
	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if !inWindow(r, minStartedAt, source) || r.Status == statusOK {
			continue
		}
		counts[r.Status]++
	}
	out := make([]ErrorBreakdownEntry, 0, len(counts))
	for st, n := range counts {
		out = append(out, ErrorBreakdownEntry{Status: st, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Status < out[j].Status
	})
	return out
}

// ComputeFlipRate returns the fraction of adjacent comparable sample pairs
// whose result flipped between ok and non-ok, over the window and source
// (PRD A4). Nil when fewer than two comparable samples — one sample has no
// adjacency, and "0% jitter" would overclaim stability.
func (s *Store) ComputeFlipRate(providerID int, revision int, window time.Duration, source string) *float64 {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil
	}
	records := pr.snapshot()
	minStartedAt := time.Now().Add(-window).UnixMilli()

	// records are oldest-first; keep the in-window comparable tail.
	var seq []bool // true = ok
	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if r.Status == statusCancelled || r.StartedAt < minStartedAt {
			continue
		}
		if srcFiltered(r, source) {
			continue
		}
		seq = append(seq, r.Status == statusOK)
	}
	if len(seq) < 2 {
		return nil
	}
	flips := 0
	for i := 1; i < len(seq); i++ {
		if seq[i] != seq[i-1] {
			flips++
		}
	}
	rate := float64(flips) / float64(len(seq)-1)
	return &rate
}

// srcFiltered reports whether r is outside the requested source view
// (inWindow's source half, split out for the walk-from-newest loops).
func srcFiltered(r Result, source string) bool {
	return source != SourceAll && r.Source != source
}
