package tui

import (
	"strings"

	"llm-monitor/internal/store"
)

// sparkBars maps a 0..1 value onto the 8-step vertical block scale. Index 0
// is the blank cell so zero samples render visibly lower than any sample.
var sparkBars = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// sparkline renders one bucket series as a single-line block-char chart
// (design.md §4): success rate is 0-100% absolute; latency is max-normalized
// like the panel charts (web/app.js bucketPath). A nil bucket (no samples)
// breaks the bar with a gap '·' so missing windows stay distinguishable from
// 0%. Zero buckets at all → "暂无样本" (same wording as the panel).
func sparkline(buckets []store.SeriesBucket, latency bool) string {
	const noData = "暂无样本"
	has := false
	for _, b := range buckets {
		if b.Samples > 0 {
			has = true
			break
		}
	}
	if !has {
		return noData
	}

	maxV := 0.0
	for _, b := range buckets {
		if b.Samples == 0 {
			continue
		}
		v := sparkValue(b, latency)
		if v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		maxV = 1 // all-zero samples: flat baseline instead of div-by-zero
	}

	var sb strings.Builder
	for _, b := range buckets {
		switch {
		case b.Samples == 0:
			sb.WriteString("·")
		default:
			v := sparkValue(b, latency) / maxV // 0..1
			idx := int(v * float64(len(sparkBars)-1))
			sb.WriteRune(sparkBars[idx])
		}
	}
	return sb.String()
}

// sparkValue picks the numeric series of a bucket: OK rate as fraction for
// the success chart, average total latency in ms for the latency chart.
func sparkValue(b store.SeriesBucket, latency bool) float64 {
	if latency {
		if b.AvgTotalMs == nil {
			return 0
		}
		return float64(*b.AvgTotalMs)
	}
	if b.OKPct == nil {
		return 0
	}
	return *b.OKPct / 100
}
