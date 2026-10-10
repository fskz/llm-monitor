// HTML report rendering as a pure function over an io.Writer (task
// 10-09-tui-default 阶段 2, design.md §1). The HTTP handler and the TUI
// export share this one pipeline — template, chart SVG builders, data
// assembly and filename slug — so an exported file is byte-identical
// regardless of which front end produced it.
package view

import (
	"fmt"
	"html/template"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"llm-monitor/internal/store"
)

//go:embed report.tmpl
var reportTmplSrc string

// reportTmpl is parsed once at package init; the template is fixed at
// compile time so a parse failure is a programming error worth panicking on.
var reportTmpl = template.Must(template.New("report").Funcs(reportTmplFuncs).Parse(reportTmplSrc))

// reportTmplFuncs are the presentation helpers used by report.tmpl. All
// values they emit are plain text rendered through html/template's
// auto-escaping; the only template.HTML in the report is the SVG markup
// built from strconv-formatted numbers in report_chart.go.
var reportTmplFuncs = template.FuncMap{
	"statusTextOf":  StatusText,
	"fmtTimeOf":     func(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04:05") },
	"fmtMsOf":       fmtMsReport,
	"fmtPctOf":      func(p *float64) string { return pctOrDash(p) },
	"fmtTPSOf":      func(p *float64) string { return tpsOrDash(p) },
	"fmtTPOTOf":     tpotOrDash,
	"fmtFlipOf":     flipOrDash,
	"fmtErrKinds":   errKindsText,
	"reportMaxRows": func() int { return ReportMaxRows },
}

func pctOrDash(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.2f%%", *p)
}

func tpsOrDash(p *float64) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f tok/s", *p)
}

// tpotOrDash renders the reciprocal view of decode TPS (ms per output
// token) — the same evidence, read the other way.
func tpotOrDash(decodeTPS *float64) string {
	if decodeTPS == nil || *decodeTPS <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f ms/tok", 1000 / *decodeTPS)
}

// flipOrDash renders the flip ("jitter") rate with the qualitative bands
// shared with the panel/TUI: <5% 稳定 / 5-20% 波动 / >20% 频繁翻转.
func flipOrDash(r *float64) string {
	if r == nil {
		return "—"
	}
	band := "稳定"
	switch {
	case *r > 0.2:
		band = "频繁翻转"
	case *r >= 0.05:
		band = "波动"
	}
	return fmt.Sprintf("%.1f%%（%s）", *r*100, band)
}

// errKindsText renders the error breakdown as "标签×N · …".
func errKindsText(kinds []ErrorKindView) string {
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s×%d", k.Label, k.Count))
	}
	return strings.Join(parts, " · ")
}

// ReportMaxRows caps the detail table of an exported report: 20k rows make a
// single-file report unprintable; the truncation is stated explicitly
// (task 10-09 decision #12).
const ReportMaxRows = 200

var reportSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

type reportRow struct {
	Time       string
	Source     string
	ResultIcon string
	StatusText string
	RowClass   string // r-ok / r-fail / r-timeout / "" (cancelled)
	TTFT       string
	Total      string
	HTTP       string
	Tokens     string
	Error      string
}

type reportData struct {
	GeneratedAt  string
	Filters      string
	TargetSnap   string
	MonitorText  string
	MonitorClass string
	SlowTTFT     bool
	View         ProviderView
	Stats        StatsView
	OKChart      template.HTML
	LatencyChart template.HTML
	Rows         []reportRow
	Truncated    bool
}

// RenderReport renders the single-file self-contained HTML report for one
// provider into w: overview + status + rates + throughput + SVG trends +
// recent detail rows with failure reasons. eng may be nil (no probing hint).
// The caller owns the writer: the HTTP handler streams to the ResponseWriter,
// the TUI export writes to a file.
func RenderReport(w io.Writer, st *store.Store, eng EngineStatus, p store.Provider, rev int, source string, window time.Duration) error {
	rowsRaw := st.QueryResults(p.ID, rev, source, window, ReportMaxRows+1, nil, nil)
	truncated := len(rowsRaw) > ReportMaxRows
	if truncated {
		rowsRaw = rowsRaw[:ReportMaxRows]
	}

	rows := make([]reportRow, 0, len(rowsRaw))
	for _, res := range rowsRaw {
		src := "定时"
		if res.Source == store.SourceManual {
			src = "手动"
		}
		icon := "✘"
		if res.Success {
			icon = "✔"
		}
		tokens := "—"
		if res.PromptTokens != nil || res.CompletionTokens != nil {
			// Per-field dash for unobserved counts (null ≠ 0, REQUIREMENTS §9.2).
			tokens = fmt.Sprintf("%s / %s", derefIntStr(res.PromptTokens), derefIntStr(res.CompletionTokens))
		}
		rows = append(rows, reportRow{
			Time:       time.UnixMilli(res.StartedAt).Format("2006-01-02 15:04:05"),
			Source:     src,
			ResultIcon: icon,
			StatusText: StatusText(res.Status),
			RowClass:   rowClassOf(res.Status),
			TTFT:       fmtMsReport(res.TTFTMs),
			Total:      fmtMsReport(&res.TotalMs),
			HTTP:       derefIntStr(res.HTTPStatus),
			Tokens:     tokens,
			// Defense in depth: rows may come from records that bypassed
			// probe-time sanitization; the report is a shareable artifact,
			// so the key is scrubbed again at render time.
			Error: ScrubSecret(res.Error, p.APIKey),
		})
	}

	// The report's stats follow the report's own source parameter
	// (2026-10-10: previously always scheduled-only).
	stats := StatsViewFrom(st.Stats(p.ID, rev, window, source))
	stats.FillMetricsPack(st, p.ID, rev, window, source)
	stats.AvgTTFTMs, stats.AvgTotalMs = st.AvgOnOK(p.ID, rev, window, source)
	stats.AvgDecodeTPS, stats.AvgPrefillTPS = st.AvgThroughput(p.ID, rev, window, source)
	buckets := st.Series(p.ID, rev, window, source)

	data := reportData{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		Filters:     fmt.Sprintf("时间窗 %s · 目标版本 %s · 来源 %s", windowLabel(window), revisionLabel(p, rev), sourceLabel(source)),
		Rows:        rows,
		Truncated:   truncated,
	}
	// Reuse the overview view for config summary, masked key slot and the
	// revision-scoped last scheduled probe; its embedded 24h stats are
	// overridden below with the report window.
	v := ViewOf(st, eng, p)
	data.View = v
	data.Stats = stats
	data.MonitorText = MonitorText(v.Status)
	data.MonitorClass = v.Status
	data.SlowTTFT = v.SlowTTFT
	if len(rowsRaw) > 0 {
		data.TargetSnap = fmt.Sprintf("%s · %s", rowsRaw[0].BaseURL, rowsRaw[0].Model)
	}
	data.OKChart = svgSuccessChart(buckets)
	data.LatencyChart = svgLatencyChart(buckets)

	return reportTmpl.Execute(w, data)
}

// ReportFilename builds the download/export filename:
// llm-monitor-<slug>-<windowTag>-<yyyymmdd-hhmmss>.html. CJK-only names fold
// to an empty slug and fall back to provider-<id>.
func ReportFilename(p store.Provider, window time.Duration) string {
	slug := ReportSlug(p.Name)
	if slug == "" {
		slug = fmt.Sprintf("provider-%d", p.ID)
	}
	return fmt.Sprintf("llm-monitor-%s-%s-%s.html", slug, windowTag(window), time.Now().Format("20060102-150405"))
}

// ReportSlug folds a provider name to a filename-safe slug; empty results
// fall back to provider-<id> at the call site (CJK-only names are common).
func ReportSlug(name string) string {
	s := reportSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}

// ScrubSecret blanks a secret if it appears in free text. Third layer of key
// non-leakage (providerView masking and probe-time replaceKey are the other
// two).
func ScrubSecret(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "***")
}

func rowClassOf(status string) string {
	switch {
	case status == "ok":
		return "r-ok"
	case strings.HasPrefix(status, "timeout"):
		return "r-timeout"
	case status == "cancelled":
		return ""
	}
	return "r-fail"
}

func derefIntStr(p *int) string {
	if p == nil {
		return "—"
	}
	return strconv.Itoa(*p)
}

func fmtMsReport(ms *int64) string {
	if ms == nil {
		return "—"
	}
	v := *ms
	if v < 1000 {
		return strconv.FormatInt(v, 10) + " ms"
	}
	return fmt.Sprintf("%.2f s", float64(v)/1000)
}

func windowLabel(window time.Duration) string {
	switch window {
	case time.Hour:
		return "1 小时"
	case 7 * 24 * time.Hour:
		return "7 天"
	}
	return "24 小时"
}

func windowTag(window time.Duration) string {
	switch window {
	case time.Hour:
		return "1h"
	case 7 * 24 * time.Hour:
		return "7d"
	}
	return "24h"
}

func revisionLabel(p store.Provider, rev int) string {
	if rev == store.RevisionAll {
		return "全部版本"
	}
	if rev == p.Revision {
		return fmt.Sprintf("当前（v%d）", rev)
	}
	return fmt.Sprintf("v%d", rev)
}

func sourceLabel(source string) string {
	switch source {
	case store.SourceScheduled:
		return "定时探测"
	case store.SourceManual:
		return "手动测试"
	}
	return "全部"
}
