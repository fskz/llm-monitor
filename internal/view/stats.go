package view

import (
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
