package tui

import (
	"strconv"

	"github.com/rivo/tview"

	"llm-monitor/internal/view"
)

// Display formatting for the detail pane. The wording mirrors the web panel
// helpers (web/app.js fmtPct/fmtMs/fmtTPS) and delegates to the single Go
// owners in internal/view (review fixpack S8): nil percentages/durations
// render "—" and empty sample sets render "暂无样本", never 0%/0 ms
// (§9.2 null-vs-0 discipline).

func fmtPct(p *float64) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(*p, 'f', 2, 64) + "%"
}

// fmtMs and friends are thin delegates onto view's exported owners — one
// implementation, no local duplicate bodies.
func fmtMs(v *int64) string    { return view.FmtMs(v) }
func fmtTPS(p *float64) string { return view.FmtTPS(p) }

// fmtTPOT renders the reciprocal view of decode TPS (ms per output token)
// over the same evidence — one number, two readings.
func fmtTPOT(decodeTPS *float64) string { return view.FmtTPOT(decodeTPS) }

// fmtFlip renders the flip ("jitter") rate with the same qualitative bands
// as the web panel: <5% 稳定 / 5-20% 波动 / >20% 频繁翻转.
func fmtFlip(r *float64) string { return view.FmtFlip(r) }

func fmtInt(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

// monitorColor maps a monitor status to its tview color name, consistent
// with the web panel badge semantics (web/style.css .ok/.fail/... classes).
func monitorColor(status string) string {
	switch status {
	case "ok":
		return "green"
	case "fail":
		return "red"
	case "stale":
		return "orange"
	case "unknown":
		return "gray"
	case "disabled":
		return "gray"
	case "manual_only":
		return "yellow"
	}
	return "white"
}

// tviewEscape shields user/target text before it lands inside a color-tagged
// status string (brackets would otherwise parse as tags).
func tviewEscape(s string) string {
	return tview.Escape(s)
}
