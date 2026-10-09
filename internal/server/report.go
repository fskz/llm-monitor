package server

import (
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "embed"

	"llm-monitor/internal/store"
)

//go:embed report.tmpl
var reportTmplSrc string

// reportTmplFuncs are the presentation helpers used by report.tmpl. All
// values they emit are plain text rendered through html/template's
// auto-escaping; the only template.HTML in the report is the SVG markup
// built from strconv-formatted numbers in report_chart.go.
var reportTmplFuncs = template.FuncMap{
	"statusTextOf":  reportStatusText,
	"fmtTimeOf":     func(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04:05") },
	"fmtMsOf":       fmtMsReport,
	"fmtPctOf":      func(p *float64) string { return pctOrDash(p) },
	"fmtTPSOf":      func(p *float64) string { return tpsOrDash(p) },
	"reportMaxRows": func() int { return reportMaxRows },
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

// reportMaxRows caps the detail table of an exported report: 20k rows make a
// single-file report unprintable; the truncation is stated explicitly
// (task 10-09 decision #12).
const reportMaxRows = 200

var reportSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// reportStatusText mirrors web/app.js STATUS_TEXT. Both tables must stay in
// sync when a status is added (go-conventions.md cross-layer rule).
func reportStatusText(status string) string {
	switch status {
	case "ok":
		return "成功"
	case "timeout_ttft":
		return "超时（首内容）"
	case "timeout_total":
		return "超时（总）"
	case "http_error":
		return "错误（HTTP）"
	case "conn_error":
		return "错误（连接）"
	case "stream_error":
		return "错误（流）"
	case "protocol_error":
		return "错误（协议）"
	case "empty":
		return "错误（空回复）"
	case "aborted":
		return "错误（中断）"
	case "cancelled":
		return "已取消"
	}
	return status
}

// reportMonitorText mirrors the panel badge labels (STATUS_META in app.js).
func reportMonitorText(status string) string {
	switch status {
	case "ok":
		return "可用"
	case "fail":
		return "不可用"
	case "stale":
		return "结果过期"
	case "unknown":
		return "未知"
	case "disabled":
		return "已停用"
	case "manual_only":
		return "仅手动"
	}
	return status
}

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
	View         providerView
	Stats        statsView
	OKChart      template.HTML
	LatencyChart template.HTML
	Rows         []reportRow
	Truncated    bool
}

// handleReport renders the single-file self-contained HTML report for one
// provider: overview + status + rates + throughput + SVG trends + recent
// detail rows with failure reasons (task 10-09). Read-only GET, so no
// same-origin guard; the browser downloads it via Content-Disposition.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = store.SourceAll // reports are archival snapshots: full history rows
	}
	if source != store.SourceAll && source != store.SourceScheduled && source != store.SourceManual {
		writeError(w, http.StatusBadRequest, "source 仅支持 scheduled / manual / all")
		return
	}

	rowsRaw := s.st.QueryResults(p.ID, rev, source, window, reportMaxRows+1, nil, nil)
	truncated := len(rowsRaw) > reportMaxRows
	if truncated {
		rowsRaw = rowsRaw[:reportMaxRows]
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
			StatusText: reportStatusText(res.Status),
			RowClass:   rowClassOf(res.Status),
			TTFT:       fmtMsReport(res.TTFTMs),
			Total:      fmtMsReport(&res.TotalMs),
			HTTP:       derefIntStr(res.HTTPStatus),
			Tokens:     tokens,
			// Defense in depth: rows may come from records that bypassed
			// probe-time sanitization; the report is a shareable artifact,
			// so the key is scrubbed again at render time.
			Error: scrubSecret(res.Error, p.APIKey),
		})
	}

	stats := statsViewFrom(s.st.Stats(p.ID, rev, window))
	stats.AvgTTFTMs, stats.AvgTotalMs = s.st.AvgOnOK(p.ID, rev, window)
	stats.AvgDecodeTPS, stats.AvgPrefillTPS = s.st.AvgThroughput(p.ID, rev, window)
	buckets := s.st.Series(p.ID, rev, window)

	data := reportData{
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05"),
		Filters:     fmt.Sprintf("时间窗 %s · 目标版本 %s · 来源 %s", windowLabel(window), revisionLabel(p, rev), sourceLabel(source)),
		Rows:        rows,
		Truncated:   truncated,
	}
	// Reuse the overview view for config summary, masked key slot and the
	// revision-scoped last scheduled probe; its embedded 24h stats are
	// overridden below with the report window.
	v := s.viewOf(p)
	data.View = v
	data.Stats = stats
	data.MonitorText = reportMonitorText(v.Status)
	data.MonitorClass = v.Status
	data.SlowTTFT = v.SlowTTFT
	if len(rowsRaw) > 0 {
		data.TargetSnap = fmt.Sprintf("%s · %s", rowsRaw[0].BaseURL, rowsRaw[0].Model)
	}
	data.OKChart = svgSuccessChart(buckets)
	data.LatencyChart = svgLatencyChart(buckets)

	slug := reportSlug(p.Name)
	if slug == "" {
		slug = fmt.Sprintf("provider-%d", p.ID)
	}
	filename := fmt.Sprintf("llm-monitor-%s-%s-%s.html", slug, windowTag(window), time.Now().Format("20060102-150405"))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if err := s.reportTmpl.Execute(w, data); err != nil {
		// Headers are already sent; nothing sane to rewrite mid-stream.
		http.Error(w, "report rendering failed: "+err.Error(), http.StatusInternalServerError)
	}
}

// scrubSecret blanks the provider's API key if it appears in free text.
func scrubSecret(s, secret string) string {
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

// reportSlug folds a provider name to a filename-safe slug; empty results
// fall back to provider-<id> at the call site (CJK-only names are common).
func reportSlug(name string) string {
	s := reportSlugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	return strings.Trim(s, "-")
}
