package view

import (
	"time"

	"llm-monitor/internal/store"
)

// StatsView is the JSON shape of GET /api/stats and the embedded "stats"
// field of GET /api/providers (web/app.js renderStats). Percentages are nil
// when there is no sample — the panel shows "暂无样本" instead of 0%/100%
// (REQUIREMENTS.md §3.4).
type StatsView struct {
	Samples    int      `json:"samples"`
	OK         int      `json:"ok"`
	Timeout    int      `json:"timeout"`
	ErrorCount int      `json:"error"`
	OKPct      *float64 `json:"ok_pct"`
	TimeoutPct *float64 `json:"timeout_pct"`
	ErrorPct   *float64 `json:"error_pct"`
	AvgTTFTMs  *int64   `json:"avg_ttft_ms"`
	AvgTotalMs *int64   `json:"avg_total_ms"`
	// Throughput averages over ok samples carrying usage evidence; nil when
	// none qualify (panel shows "—", never 0).
	AvgDecodeTPS  *float64 `json:"avg_decode_tps"`
	AvgPrefillTPS *float64 `json:"avg_prefill_tps"`
	// Metrics-pack (10-10): nearest-rank percentiles of the ok samples,
	// the flip ("jitter") rate and the per-status error breakdown, all
	// following the same window/revision/source filter as the counts.
	TTFTP50Ms    *int64          `json:"ttft_p50_ms"`
	TTFTP95Ms    *int64          `json:"ttft_p95_ms"`
	TotalP50Ms   *int64          `json:"total_p50_ms"`
	TotalP95Ms   *int64          `json:"total_p95_ms"`
	FlipRate     *float64        `json:"flip_rate"`
	ErrorsByKind []ErrorKindView `json:"errors_by_kind"`
}

// ErrorKindView is one non-ok status with its count (most frequent
// first); empty array when the window has no failing sample.
type ErrorKindView struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
	Label  string `json:"label"`
}

// StatsViewFrom maps a store.Stats onto the API shape. Timeout merges the
// two timeout sub-categories; the panel keeps the split visible through the
// results table.
func StatsViewFrom(st store.Stats) StatsView {
	return StatsView{
		Samples:    st.Samples,
		OK:         st.OK,
		Timeout:    st.TimeoutTTFT + st.TimeoutTotal,
		ErrorCount: st.ErrorCount,
		OKPct:      st.OKPct,
		TimeoutPct: st.TimeoutPct,
		ErrorPct:   st.ErrorPct,
	}
}

// FillMetricsPack attaches the metrics-pack aggregates (percentiles, flip
// rate, error breakdown) computed under the same filter family. Label uses
// the shared Chinese status table.
func (v *StatsView) FillMetricsPack(st *store.Store, providerID, revision int, window time.Duration, source string) {
	p := st.ComputePercentiles(providerID, revision, window, source)
	v.TTFTP50Ms, v.TTFTP95Ms = p.TTFTP50, p.TTFTP95
	v.TotalP50Ms, v.TotalP95Ms = p.TotalP50, p.TotalP95
	v.FlipRate = st.ComputeFlipRate(providerID, revision, window, source)
	for _, e := range st.ComputeErrorBreakdown(providerID, revision, window, source) {
		v.ErrorsByKind = append(v.ErrorsByKind, ErrorKindView{
			Status: e.Status, Count: e.Count, Label: StatusText(e.Status),
		})
	}
}

// BucketView maps store.SeriesBucket onto the API shape (web/app.js
// renderCharts reads ok_pct / avg_ttft_ms / avg_total_ms / samples /
// start_ms).
type BucketView struct {
	StartMs       int64    `json:"start_ms"`
	Samples       int      `json:"samples"`
	OKPct         *float64 `json:"ok_pct"`
	AvgTTFTMs     *int64   `json:"avg_ttft_ms"`
	AvgTotalMs    *int64   `json:"avg_total_ms"`
	AvgDecodeTPS  *float64 `json:"avg_decode_tps"`
	AvgPrefillTPS *float64 `json:"avg_prefill_tps"`
}

// BucketViewFrom maps a store.SeriesBucket onto the API shape.
func BucketViewFrom(b store.SeriesBucket) BucketView {
	return BucketView{
		StartMs:       b.StartMs,
		Samples:       b.Samples,
		OKPct:         b.OKPct,
		AvgTTFTMs:     b.AvgTTFTMs,
		AvgTotalMs:    b.AvgTotalMs,
		AvgDecodeTPS:  b.AvgDecodeTPS,
		AvgPrefillTPS: b.AvgPrefillTPS,
	}
}
