package store

import (
	"sort"
	"time"
)

// Stats is the availability summary of one provider over a time window
// (REQUIREMENTS.md §3.4). Denominator: completed, actually-issued,
// non-cancelled *scheduled* probes — manual probes and skips never enter
// it. The three categories are mutually exclusive and exhaustive:
// OKPct+TimeoutPct+ErrorPct = 100%. When Samples=0 every percentage is nil
// (the panel shows "no samples" instead of 0% or 100%).
type Stats struct {
	Samples      int
	OK           int
	TimeoutTTFT  int
	TimeoutTotal int
	ErrorCount   int
	OKPct        *float64
	TimeoutPct   *float64
	ErrorPct     *float64
}

// SeriesBucket aggregates the scheduled probes of one time bucket. Buckets
// without samples report nil percentages and averages; buckets with samples
// but no successes report nil averages only. Averages cover ok samples
// exclusively — failed-request durations never pollute response-time
// statistics (REQUIREMENTS.md §3.4).
type SeriesBucket struct {
	StartMs       int64
	Samples       int
	OKPct         *float64
	AvgTTFTMs     *int64
	AvgTotalMs    *int64
	AvgDecodeTPS  *float64 `json:"avg_decode_tps"`
	AvgPrefillTPS *float64 `json:"avg_prefill_tps"`
}

// RevisionAll selects every revision in Stats and Series (REQUIREMENTS.md
// §10: history queries may explicitly ask for all versions).
const RevisionAll = -1

// Revisions returns the distinct revisions that ever appear in the
// provider's history, in ascending order. The current revision of the
// configuration is included even when it has no results yet, so the panel
// can always offer "current" plus every historical version. Used by the
// overview API to populate the revision selector (web/app.js renderRevisions).
func (s *Store) Revisions(providerID int) []int {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil
	}
	seen := map[int]bool{}
	if current, ok := s.GetProvider(providerID); ok {
		seen[current.Revision] = true
	}
	for _, r := range pr.snapshot() {
		seen[r.Revision] = true
	}
	out := make([]int, 0, len(seen))
	for rev := range seen {
		out = append(out, rev)
	}
	sort.Ints(out)
	return out
}

// ResultTPS derives one result's throughput with the same formula and
// evidence guards as the stored-sample averages (decodeTPS/prefillTPS), so
// the manual-probe response and the statistics never drift apart. Callers
// restrict it to ok samples (§3.1: throughput is defined for ok samples
// only); decode selects the decode metric, otherwise prefill. Nil when the
// evidence does not qualify.
func ResultTPS(r Result, decode bool) *float64 {
	var v float64
	var ok bool
	if decode {
		v, ok = decodeTPS(r)
	} else {
		v, ok = prefillTPS(r)
	}
	if !ok {
		return nil
	}
	return &v
}

// decodeTPS derives the decode throughput of one ok sample from exact
// server-reported counts: (completion_tokens-1) over the decode span
// (total_ms - ttft_ms). The first token belongs to prefill, hence -1.
// Per approved PRD A4, samples with completion_tokens<2 or a zero-length
// decode span (total==ttft) are excluded from the average — a 1-token
// response decodes nothing, so it carries no decode-rate evidence.
func decodeTPS(r Result) (float64, bool) {
	if r.CompletionTokens == nil || r.TTFTMs == nil || *r.CompletionTokens < 2 {
		return 0, false
	}
	span := float64(r.TotalMs) - float64(*r.TTFTMs)
	if span <= 0 {
		return 0, false
	}
	return float64(*r.CompletionTokens-1) / (span / 1000), true
}

// prefillTPS derives the prompt-side throughput: prompt_tokens over ttft_ms.
// TTFT includes queueing, so this is an approximate lower bound, never an
// exact prefill rate; requires both values positive.
func prefillTPS(r Result) (float64, bool) {
	if r.PromptTokens == nil || r.TTFTMs == nil || *r.PromptTokens < 1 || *r.TTFTMs <= 0 {
		return 0, false
	}
	return float64(*r.PromptTokens) / (float64(*r.TTFTMs) / 1000), true
}

// AvgOnOK returns the average TTFT and total duration of the ok samples in
// the window (scheduled, non-cancelled, revision-filtered) — failed-request
// durations never enter response-time statistics (REQUIREMENTS.md §3.4).
// Both results are nil when there is no ok sample.
func (s *Store) AvgOnOK(providerID int, revision int, window time.Duration, source string) (avgTTFT, avgTotal *int64) {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil, nil
	}
	records := pr.snapshot()

	minStartedAt := time.Now().Add(-window).UnixMilli()
	var ttftSum, totalSum int64
	ttftCount, okCount := 0, 0
	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if !inWindow(r, minStartedAt, source) || r.Status != statusOK {
			continue
		}
		okCount++
		totalSum += r.TotalMs
		if r.TTFTMs != nil {
			ttftSum += *r.TTFTMs
			ttftCount++
		}
	}
	if okCount == 0 {
		return nil, nil
	}
	avg := totalSum / int64(okCount)
	if ttftCount > 0 {
		t := ttftSum / int64(ttftCount)
		avgTTFT = &t
	}
	return avgTTFT, &avg
}

// AvgThroughput returns the average decode/prefill throughput of the ok
// samples in the window that carry usable usage evidence. Each metric has
// its own denominator (a sample can qualify for one and not the other);
// nil when no sample qualifies.
func (s *Store) AvgThroughput(providerID int, revision int, window time.Duration, source string) (avgDecode, avgPrefill *float64) {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil, nil
	}
	records := pr.snapshot()

	minStartedAt := time.Now().Add(-window).UnixMilli()
	var decodeSum, prefillSum float64
	decodeCount, prefillCount := 0, 0
	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if !inWindow(r, minStartedAt, source) || r.Status != statusOK {
			continue
		}
		if d, ok := decodeTPS(r); ok {
			decodeSum += d
			decodeCount++
		}
		if p, ok := prefillTPS(r); ok {
			prefillSum += p
			prefillCount++
		}
	}
	if decodeCount > 0 {
		v := decodeSum / float64(decodeCount)
		avgDecode = &v
	}
	if prefillCount > 0 {
		v := prefillSum / float64(prefillCount)
		avgPrefill = &v
	}
	return avgDecode, avgPrefill
}

// inWindow reports whether a result belongs to the denominator or a
// bucket: matching the requested source, not cancelled, started inside
// the window. source is one of the /api source values
// ("scheduled" / "manual" / "all"); "all" keeps both.
func inWindow(r Result, minStartedAt int64, source string) bool {
	if source != SourceAll && r.Source != source {
		return false
	}
	return r.Status != statusCancelled && r.StartedAt >= minStartedAt
}

// classify counts one sample into the three categories.
func (st *Stats) classify(r Result) {
	st.Samples++
	switch r.Status {
	case statusOK:
		st.OK++
	case statusTimeoutTTFT:
		st.TimeoutTTFT++
	case statusTimeoutTotal:
		st.TimeoutTotal++
	default:
		st.ErrorCount++
	}
}

func pct(part, total int) *float64 {
	if total == 0 {
		return nil
	}
	v := float64(part) * 100 / float64(total)
	return &v
}

// snapshot returns a copy of the provider's records under the read lock.
// Slices up to the 20k cap make this cheap enough for panel refreshes.
func (pr *providerResults) snapshot() []Result {
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	out := make([]Result, len(pr.records))
	copy(out, pr.records)
	return out
}

// Stats computes the availability statistics of a provider over window,
// filtered by started_at and source (scheduled / manual / all;
// cancelled never counts, REQUIREMENTS.md §3.4 — 2026-10-10 revision:
// the detail view's stats follow the source filter, the monitor status
// stays on scheduled probes). revision selects a specific target revision;
// RevisionAll covers every revision. Results are computed purely from the
// in-memory index: a refresh never rescans the JSONL files.
func (s *Store) Stats(providerID int, revision int, window time.Duration, source string) Stats {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return Stats{}
	}
	records := pr.snapshot()

	minStartedAt := time.Now().Add(-window).UnixMilli()
	st := Stats{}
	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if inWindow(r, minStartedAt, source) {
			st.classify(r)
		}
	}
	if st.Samples > 0 {
		st.OKPct = pct(st.OK, st.Samples)
		st.TimeoutPct = pct(st.TimeoutTTFT+st.TimeoutTotal, st.Samples)
		st.ErrorPct = pct(st.ErrorCount, st.Samples)
	}
	return st
}

// bucketLayout maps a query window to the bucket grid (design.md §4.4):
// 1h→12×5min, 24h→24×1h, 7d→28×6h. The grid covers the window ending now:
// the boundary is aligned to the current bucket's end so fresh samples
// always land in the last bucket, and older samples land deterministically
// on the fixed grid. Other windows fall back to 24 equal buckets.
func bucketLayout(window time.Duration) (start time.Time, width time.Duration, count int) {
	now := time.Now()
	switch window {
	case time.Hour:
		width, count = 5*time.Minute, 12
	case 24 * time.Hour:
		width, count = time.Hour, 24
	case 7 * 24 * time.Hour:
		width, count = 6*time.Hour, 28
	default:
		count = 24
		width = window / time.Duration(count)
	}
	// Grid end = end of the bucket containing now; grid start = end-count*width.
	end := now.Truncate(width).Add(width)
	return end.Add(-time.Duration(count) * width), width, count
}

// Series aggregates the probes of a provider into time buckets covering
// the window for the requested source: success ratio, average TTFT and
// total duration of ok samples, plus per-bucket sample counts. Empty
// buckets keep nil values — no interpolation across gaps
// (REQUIREMENTS.md §3.4).
func (s *Store) Series(providerID int, revision int, window time.Duration, source string) []SeriesBucket {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil
	}
	records := pr.snapshot()

	gridStart, width, count := bucketLayout(window)
	startMs := gridStart.UnixMilli()
	widthMs := width.Milliseconds()

	oks := make([]int, count)
	totals := make([]int64, count)
	ttfts := make([]int64, count)
	ttftCount := make([]int, count)
	bucketSamples := make([]int, count)
	decodeSum := make([]float64, count)
	decodeCount := make([]int, count)
	prefillSum := make([]float64, count)
	prefillCount := make([]int, count)

	for _, r := range records {
		if r.Revision != revision && revision != RevisionAll {
			continue
		}
		if !inWindow(r, startMs, source) {
			continue
		}
		idx := int((r.StartedAt - startMs) / widthMs)
		if idx < 0 || idx >= count {
			continue
		}
		bucketSamples[idx]++
		if r.Status == statusOK {
			oks[idx]++
			totals[idx] += r.TotalMs
			if r.TTFTMs != nil {
				ttfts[idx] += *r.TTFTMs
				ttftCount[idx]++
			}
			if d, ok := decodeTPS(r); ok {
				decodeSum[idx] += d
				decodeCount[idx]++
			}
			if p, ok := prefillTPS(r); ok {
				prefillSum[idx] += p
				prefillCount[idx]++
			}
		}
	}

	buckets := make([]SeriesBucket, count)
	for i := 0; i < count; i++ {
		b := SeriesBucket{StartMs: startMs + int64(i)*widthMs, Samples: bucketSamples[i]}
		if bucketSamples[i] > 0 {
			b.OKPct = pct(oks[i], bucketSamples[i])
			if oks[i] > 0 {
				avg := totals[i] / int64(oks[i])
				b.AvgTotalMs = &avg
				if ttftCount[i] > 0 {
					avgT := ttfts[i] / int64(ttftCount[i])
					b.AvgTTFTMs = &avgT
				}
				if decodeCount[i] > 0 {
					v := decodeSum[i] / float64(decodeCount[i])
					b.AvgDecodeTPS = &v
				}
				if prefillCount[i] > 0 {
					v := prefillSum[i] / float64(prefillCount[i])
					b.AvgPrefillTPS = &v
				}
			}
		}
		buckets[i] = b
	}
	return buckets
}

// QueryResults returns provider results in (StartedAt, Seq) descending
// order. Filters: revision (RevisionAll for every revision), source
// ("all", SourceScheduled, SourceManual) and window (by started_at).
// beforeSeq/beforeStartedAt form the composite pagination cursor: only
// records with StartedAt < beforeStartedAt, or equal StartedAt and
// Seq < beforeSeq, are returned — records sharing a timestamp are told
// apart by Seq. A nil cursor returns the newest records. limit is capped
// by the caller (server) to 500.
func (s *Store) QueryResults(providerID int, revision int, source string, window time.Duration, limit int, beforeSeq *int64, beforeStartedAt *int64) []Result {
	pr := s.resultsFor(providerID)
	if pr == nil || limit <= 0 {
		return nil
	}
	records := pr.snapshot()

	// records are stored oldest-first (append order); walk backwards for
	// newest-first output.
	out := make([]Result, 0, min(limit, len(records)))
	minStartedAt := time.Now().Add(-window).UnixMilli()
	for i := len(records) - 1; i >= 0; i-- {
		r := records[i]
		if revision != RevisionAll && r.Revision != revision {
			continue
		}
		if source != SourceAll && r.Source != source {
			continue
		}
		if r.StartedAt < minStartedAt {
			continue
		}
		if beforeStartedAt != nil {
			// The cursor record itself and everything newer is excluded:
			// keep only records strictly before (StartedAt, Seq).
			if r.StartedAt > *beforeStartedAt {
				continue
			}
			if r.StartedAt == *beforeStartedAt {
				if beforeSeq == nil || r.Seq >= *beforeSeq {
					continue
				}
			}
		}
		out = append(out, r)
		if len(out) >= limit {
			break
		}
	}
	return out
}
