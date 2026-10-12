package store

import (
	"sort"
	"time"
)

// MetricsPack bundles every statistic the hot paths need for one
// (provider, revision, window, source) query: the availability Stats, the
// ok-sample averages, the metrics-pack aggregates (percentiles, flip rate,
// error breakdown), the result streaks and the revision list. PERF1
// (10-12-review-fixpack): the per-metric methods below each snapshot the
// provider's full record set (≤20k records) and re-filter the same
// collection, so the 5s panel/TUI polling paths multiplied that copy per
// provider — MetricsPack takes ONE snapshot and runs ONE filter pass, and
// every output is byte-for-byte what the individual methods would have
// returned (this is an equivalence refactor, not a semantics change).
type MetricsPack struct {
	Stats Stats
	// Streaks is the run of identical results ending at the newest
	// non-cancelled sample under the same filter (ComputeStreaks).
	Streaks Streaks
	// Averages of the ok samples (AvgOnOK / AvgThroughput); nil when the
	// respective evidence set is empty.
	AvgTTFTMs     *int64
	AvgTotalMs    *int64
	AvgDecodeTPS  *float64
	AvgPrefillTPS *float64
	// Percentiles / FlipRate / ErrorsByKind mirror ComputePercentiles /
	// ComputeFlipRate / ComputeErrorBreakdown.
	Percentiles  Percentiles
	FlipRate     *float64
	ErrorsByKind []ErrorBreakdownEntry
	// Revisions is the distinct revisions appearing in the provider's
	// history union the provider's current configured revision (Revisions);
	// it ignores the revision filter like Revisions does.
	Revisions []int
}

// MetricsPack computes the whole aggregate of one provider under one
// snapshot. The filter family is the same as the individual methods':
// revision (RevisionAll for every revision), source ("scheduled"/"manual"/
// "all") and window by started_at; cancelled never counts.
func (s *Store) MetricsPack(providerID, revision int, window time.Duration, source string) MetricsPack {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return MetricsPack{}
	}
	records := pr.snapshot()
	minStartedAt := time.Now().Add(-window).UnixMilli()

	var pack MetricsPack
	revSeen := map[int]bool{}
	if current, ok := s.GetProvider(providerID); ok {
		revSeen[current.Revision] = true
	}

	// Single forward pass over the oldest-first records: collect the
	// revision set (unfiltered) and every in-window aggregate (filtered).
	var ttftSum, totalSum int64
	ttftCount, okCount := 0, 0
	var decodeSum, prefillSum float64
	decodeCount, prefillCount := 0, 0
	var ttfts, totals []int64
	errCounts := make(map[string]int)
	// statuses is the filtered sequence oldest-first; the flip rate walks
	// adjacent pairs and the streaks walk it from the newest end.
	var statuses []string

	for _, r := range records {
		revSeen[r.Revision] = true
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if !inWindow(r, minStartedAt, source) {
			continue
		}
		statuses = append(statuses, r.Status)
		pack.Stats.classify(r)
		if r.Status == statusOK {
			okCount++
			totalSum += r.TotalMs
			totals = append(totals, r.TotalMs)
			if r.TTFTMs != nil {
				ttftSum += *r.TTFTMs
				ttftCount++
				ttfts = append(ttfts, *r.TTFTMs)
			}
			if d, ok := decodeTPS(r); ok {
				decodeSum += d
				decodeCount++
			}
			if p, ok := prefillTPS(r); ok {
				prefillSum += p
				prefillCount++
			}
		} else {
			errCounts[r.Status]++
		}
	}

	if pack.Stats.Samples > 0 {
		pack.Stats.OKPct = pct(pack.Stats.OK, pack.Stats.Samples)
		pack.Stats.TimeoutPct = pct(pack.Stats.TimeoutTTFT+pack.Stats.TimeoutTotal, pack.Stats.Samples)
		pack.Stats.ErrorPct = pct(pack.Stats.ErrorCount, pack.Stats.Samples)
	}
	if okCount > 0 {
		avg := totalSum / int64(okCount)
		pack.AvgTotalMs = &avg
		if ttftCount > 0 {
			t := ttftSum / int64(ttftCount)
			pack.AvgTTFTMs = &t
		}
	}
	if decodeCount > 0 {
		v := decodeSum / float64(decodeCount)
		pack.AvgDecodeTPS = &v
	}
	if prefillCount > 0 {
		v := prefillSum / float64(prefillCount)
		pack.AvgPrefillTPS = &v
	}

	pack.Percentiles.TTFTCount = len(ttfts)
	if len(totals) > 0 {
		pack.Percentiles.TotalP50, pack.Percentiles.TotalP95 = rankPair(totals)
	}
	if len(ttfts) > 0 {
		pack.Percentiles.TTFTP50, pack.Percentiles.TTFTP95 = rankPair(ttfts)
	}

	if len(statuses) >= 2 {
		flips := 0
		for i := 1; i < len(statuses); i++ {
			if (statuses[i] == statusOK) != (statuses[i-1] == statusOK) {
				flips++
			}
		}
		rate := float64(flips) / float64(len(statuses)-1)
		pack.FlipRate = &rate
	}

	pack.ErrorsByKind = make([]ErrorBreakdownEntry, 0, len(errCounts))
	for st, n := range errCounts {
		pack.ErrorsByKind = append(pack.ErrorsByKind, ErrorBreakdownEntry{Status: st, Count: n})
	}
	sort.Slice(pack.ErrorsByKind, func(i, j int) bool {
		if pack.ErrorsByKind[i].Count != pack.ErrorsByKind[j].Count {
			return pack.ErrorsByKind[i].Count > pack.ErrorsByKind[j].Count
		}
		return pack.ErrorsByKind[i].Status < pack.ErrorsByKind[j].Status
	})

	// Streaks: the newest-first walk of the same filtered sequence
	// (ComputeStreaks' skip conditions are exactly !inWindow).
	for i := len(statuses) - 1; i >= 0; i-- {
		if pack.Streaks.Base == "" {
			pack.Streaks.Base = statuses[i]
		}
		if statuses[i] == statusOK {
			if pack.Streaks.Base != statusOK {
				break // run ended
			}
			pack.Streaks.OK++
		} else {
			if pack.Streaks.Base == statusOK {
				break
			}
			pack.Streaks.Fail++
		}
	}

	pack.Revisions = make([]int, 0, len(revSeen))
	for rev := range revSeen {
		pack.Revisions = append(pack.Revisions, rev)
	}
	sort.Ints(pack.Revisions)
	return pack
}
