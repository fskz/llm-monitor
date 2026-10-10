package tui

import (
	"strconv"

	"github.com/rivo/tview"

	"llm-monitor/internal/store"
)

// Display formatting for the detail pane. The wording mirrors the web panel
// helpers (web/app.js fmtPct/fmtMs/fmtTPS) and the report helpers in
// internal/view/report.go: nil percentages/durations render "—" and empty
// sample sets render "暂无样本", never 0%/0 ms (§9.2 null-vs-0 discipline).

func fmtPct(p *float64) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(*p, 'f', 2, 64) + "%"
}

func fmtMs(v *int64) string {
	if v == nil {
		return "—"
	}
	if *v < 1000 {
		return strconv.FormatInt(*v, 10) + " ms"
	}
	return strconv.FormatFloat(float64(*v)/1000, 'f', 2, 64) + " s"
}

func fmtTPS(p *float64) string {
	if p == nil {
		return "—"
	}
	return strconv.FormatFloat(*p, 'f', 1, 64) + " tok/s"
}

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

// monitorStatusOf maps one manual-probe result to the §7.2 monitor ladder
// used by the status line: ok/fail by result, manual_only when scheduled
// probing is off (the probe outcome is real but does not change schedule
// health).
func monitorStatusOf(p store.Provider, res *store.Result) string {
	if !p.Enabled || p.IntervalSec == 0 {
		return "manual_only"
	}
	if res.Status == "ok" {
		return "ok"
	}
	return "fail"
}

// tviewEscape shields user/target text before it lands inside a color-tagged
// status string (brackets would otherwise parse as tags).
func tviewEscape(s string) string {
	return tview.Escape(s)
}
