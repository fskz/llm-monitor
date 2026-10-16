package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// pageSize is the results-table page (server caps /api/results at 500; the
// TUI shows a terminal-friendly slice and paginates with the same
// (started_at, seq) strictly-before cursor).
const pageSize = 20

// filterState holds the detail-pane query knobs. They cycle through the same
// value sets as the panel selects (web/app.js sel-window/revision/source).
type filterState struct {
	window   time.Duration // 1h / 24h / 7d
	revision string        // "" = current revision, "all" (panel semantics)
	source   string        // scheduled / manual / all
}

func (f filterState) windowLabel() string {
	switch f.window {
	case time.Hour:
		return "1小时"
	case 7 * 24 * time.Hour:
		return "7天"
	}
	return "24小时"
}

// revisionOf resolves the revision filter to the store argument: current
// revision number, or store.RevisionAll.
func (f filterState) revisionOf(p store.Provider) int {
	if f.revision == "all" {
		return store.RevisionAll
	}
	return p.Revision
}

func (f filterState) sourceLabel() string {
	switch f.source {
	case store.SourceManual:
		return "手动"
	case store.SourceAll:
		return "全部"
	}
	return "定时"
}

// pageCursor is the raw composite the server base64-encodes
// (stats_api.go encodeCursor); the TUI keeps it unencoded.
type pageCursor struct {
	startedAt int64
	seq       int64
}

// detailPane is the right half: filter header, overview+stats, sparklines,
// paginated results table. Rebuilt from the store on every refresh;
// selection (provider id, table scroll offset, cursor stack) lives in uiState.
type detailPane struct {
	st  *store.Store
	eng view.EngineStatus

	filters filterState

	// Pagination: results are newest-first; pageCursor fetches strictly
	// older rows. prevStack remembers the cursor each page was entered with
	// so PgUp walks back (web/app.js prevCursors pattern).
	pageCursor *pageCursor
	prevStack  []pageCursor
	nextCursor *pageCursor // set when the last fetch had more rows
}

func newDetailPane(st *store.Store, eng view.EngineStatus) *detailPane {
	return &detailPane{
		st:      st,
		eng:     eng,
		filters: filterState{window: 24 * time.Hour, source: store.SourceScheduled},
	}
}

// renderText assembles the whole detail block of one provider as a
// color-tagged string. A single scrollable text view keeps the layout robust
// at any terminal width — tview.Table inside the detail would fight the
// outer focus cycle.
func (d *detailPane) renderText(p store.Provider) string {
	v := d.viewOf(p)
	return d.header(v) + d.summary(v) + d.statsBlock(p, v) + d.charts(p) + d.resultsTable(p, v)
}

func (d *detailPane) viewOf(p store.Provider) view.ProviderView {
	return view.ViewOf(d.st, d.eng, p)
}

func (d *detailPane) header(v view.ProviderView) string {
	badges := ""
	if v.Probing {
		badges += " [yellow::b]正在探测[-]"
	}
	if v.SlowTTFT && v.Status == store.StatusOK {
		badges += " [orange::b]首内容偏慢[-]"
	}
	return fmt.Sprintf("[::b]%s[-]  [%s::b]%s[-]%s\n",
		v.Name, monitorColor(v.Status), view.MonitorText(v.Status), badges) +
		fmt.Sprintf("[gray]%s · %s · v%d · 间隔 %ds · 密钥 %s[white]\n",
			v.BaseURL, v.Model, v.Revision, v.IntervalSec, keySlot(v))
}

// keySlot renders the masked key or an explicit empty marker.
func keySlot(v view.ProviderView) string {
	if !v.APIKeySet {
		return "未设置"
	}
	return v.APIKeyMask
}

func (d *detailPane) summary(v view.ProviderView) string {
	last := "尚未有有效定时探测"
	if v.LastProbe != nil {
		last = fmt.Sprintf("%s · %s · TTFT %s · 总耗时 %s",
			time.UnixMilli(v.LastProbe.FinishedAt).Format("01-02 15:04:05"),
			view.StatusText(v.LastProbe.Status),
			fmtMs(v.LastProbe.TTFTMs), fmtMs(&v.LastProbe.TotalMs))
	}
	storage := ""
	if v.StorageError != "" {
		storage = "\n[red::b]存储异常：" + tview.Escape(v.StorageError) + "[-]"
	}
	return fmt.Sprintf("最近定时探测：%s%s\n", last, storage)
}

func (d *detailPane) statsBlock(p store.Provider, v view.ProviderView) string {
	// Stats follow the source filter (task 10-10): the detail pane shows
	// what the user filtered for; the monitor badge in the header stays on
	// scheduled probes (§7.2) via view.ViewOf.
	src := d.filters.source
	// PERF1 (10-12-review-fixpack): one snapshot, one filter pass — the six
	// per-metric calls previously re-snapshotted the same record set.
	stats := view.StatsViewFromPack(d.st.MetricsPack(p.ID, d.filters.revisionOf(p), d.filters.window, src))

	if stats.Samples == 0 {
		return fmt.Sprintf("[gray]统计（%s）：暂无样本[white]\n", d.filters.windowLabel())
	}
	// Grouped like the web panel: rates / latency / throughput / stability,
	// one line each — the qualifier lives on the group, not every value.
	out := fmt.Sprintf(`统计（%s · %s · %s）
[gray]▸ 样本与比率[white]  样本 %d · 成功 [green]%s[-] · 超时 [orange]%s[-] · 错误 [red]%s[-]
[gray]▸ 延迟（成功样本）[white]  TTFT 平均 %s · P50/P95 %s/%s · 总耗时 平均 %s · P50/P95 %s/%s
[gray]▸ 吞吐（成功样本）[white]  decode %s · TPOT %s · prefill %s（近似）
[gray]▸ 稳定性[white]  抖动 %s
`,
		d.filters.windowLabel(), revisionLabel(d.filters, v), d.filters.sourceLabel(),
		stats.Samples, fmtPct(stats.OKPct), fmtPct(stats.TimeoutPct), fmtPct(stats.ErrorPct),
		fmtMs(stats.AvgTTFTMs), fmtMs(stats.TTFTP50Ms), fmtMs(stats.TTFTP95Ms), fmtMs(stats.AvgTotalMs), fmtMs(stats.TotalP50Ms), fmtMs(stats.TotalP95Ms),
		fmtTPS(stats.AvgDecodeTPS), fmtTPOT(stats.AvgDecodeTPS), fmtTPS(stats.AvgPrefillTPS),
		fmtFlip(stats.FlipRate))
	if len(stats.ErrorsByKind) > 0 {
		parts := make([]string, 0, len(stats.ErrorsByKind))
		for _, e := range stats.ErrorsByKind {
			parts = append(parts, fmt.Sprintf("%s×%d", e.Label, e.Count))
		}
		out += "[gray]  错误分布[white] " + strings.Join(parts, " · ") + "\n"
	}
	return out
}

func revisionLabel(f filterState, v view.ProviderView) string {
	if f.revision == "all" {
		return "全部版本"
	}
	return fmt.Sprintf("v%d", v.Revision)
}

func (d *detailPane) charts(p store.Provider) string {
	buckets := d.st.Series(p.ID, d.filters.revisionOf(p), d.filters.window, d.filters.source)
	ok, lat := sparkline(buckets, false), sparkline(buckets, true)
	return fmt.Sprintf("成功率  [blue]%s[-]\n耗时    [blue]%s[-]\n", ok, lat)
}

// resultsTable renders the paginated rows; v is the same view renderText
// already computed (PERF2: one ViewOf snapshot per render, no duplicate
// store reads and no revision-label tearing within a frame).
func (d *detailPane) resultsTable(p store.Provider, v view.ProviderView) string {
	rows := d.st.QueryResults(p.ID, d.filters.revisionOf(p), d.filters.source,
		d.filters.window, pageSize+1, d.cursorSeq(), d.cursorStartedAt())
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	// Recompute the next-page cursor from whatever page is rendered (the
	// lookahead row proved one more page exists); nil when this is the last.
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		d.nextCursor = &pageCursor{startedAt: last.StartedAt, seq: last.Seq}
	} else {
		d.nextCursor = nil
	}

	var b []byte
	w := func(format string, args ...any) { b = append(b, fmt.Sprintf(format, args...)...) }
	w("探测记录（%s · %s · %s）\n", d.filters.windowLabel(), revisionLabel(d.filters, v), d.filters.sourceLabel())
	if len(rows) == 0 {
		w("[gray]暂无记录[white]\n")
		return string(b)
	}
	w("[::b]%-16s %-4s %-10s %9s %9s %5s %11s  %s[-]\n",
		"时间", "来源", "状态", "TTFT", "总耗时", "HTTP", "token", "错误说明")
	for _, res := range rows {
		w("%s\n", d.resultLine(p, res))
	}
	if hasMore {
		w("[gray]（/page next 下一页 · /page prev 上一页）[white]\n")
	}
	return string(b)
}

// resultLine renders one result row with status coloring; the provider key
// is scrubbed from free text like the report rows (defense in depth).
func (d *detailPane) resultLine(p store.Provider, res store.Result) string {
	src := "定时"
	if res.Source == store.SourceManual {
		src = "手动"
	}
	statusColor := "red"
	if res.Success {
		statusColor = "green"
	}
	tokens := "—"
	if res.PromptTokens != nil || res.CompletionTokens != nil {
		tokens = fmtInt(res.PromptTokens) + "/" + fmtInt(res.CompletionTokens)
	}
	errText := ""
	if res.Error != "" {
		errText = "  " + tview.Escape(view.ScrubSecret(res.Error, p.APIKey))
	}
	return fmt.Sprintf("%s %s [%s]%-10s[-] %9s %9s %5s %11s%s",
		time.UnixMilli(res.StartedAt).Format("01-02 15:04:05"), src, statusColor,
		view.StatusText(res.Status), fmtMs(res.TTFTMs), fmtMs(&res.TotalMs),
		fmtInt(res.HTTPStatus), tokens, errText)
}

// cursorSeq/cursorStartedAt adapt pageCursor to QueryResults' pointer args.
func (d *detailPane) cursorSeq() *int64 {
	if d.pageCursor == nil {
		return nil
	}
	return &d.pageCursor.seq
}

func (d *detailPane) cursorStartedAt() *int64 {
	if d.pageCursor == nil {
		return nil
	}
	return &d.pageCursor.startedAt
}

// pageNext advances one page older when a next page exists. The consumed
// nextCursor is cleared so two PgDn presses without an intervening redraw
// advance one page, not two (the redraw recomputes the next-page cursor).
func (d *detailPane) pageNext() bool {
	if d.nextCursor == nil {
		return false
	}
	if d.pageCursor != nil {
		d.prevStack = append(d.prevStack, *d.pageCursor)
	}
	d.pageCursor = d.nextCursor
	d.nextCursor = nil
	return true
}

// pagePrev walks back through the cursor stack to the first page.
func (d *detailPane) pagePrev() bool {
	if d.pageCursor == nil {
		return false
	}
	if len(d.prevStack) == 0 {
		d.pageCursor = nil // back to the newest page
		return true
	}
	last := d.prevStack[len(d.prevStack)-1]
	d.prevStack = d.prevStack[:len(d.prevStack)-1]
	d.pageCursor = &last
	return true
}

func (d *detailPane) resetPaging() {
	d.pageCursor = nil
	d.prevStack = d.prevStack[:0]
	d.nextCursor = nil
}

// cycleWindow/cycleRevision/cycleSource respond to the filter keys; each
// returns the pane dirty so the caller re-renders.
func (d *detailPane) cycleWindow() {
	switch d.filters.window {
	case time.Hour:
		d.filters.window = 24 * time.Hour
	case 24 * time.Hour:
		d.filters.window = 7 * 24 * time.Hour
	default:
		d.filters.window = time.Hour
	}
	d.resetPaging()
}

func (d *detailPane) cycleRevision() {
	if d.filters.revision == "all" {
		d.filters.revision = ""
	} else {
		d.filters.revision = "all"
	}
	d.resetPaging()
}

func (d *detailPane) cycleSource() {
	switch d.filters.source {
	case store.SourceScheduled:
		d.filters.source = store.SourceManual
	case store.SourceManual:
		d.filters.source = store.SourceAll
	default:
		d.filters.source = store.SourceScheduled
	}
	d.resetPaging()
}

// setWindow/setRevision/setSource apply a /window /revision /source argument
// directly (task 10-16 design §4). They are the single owners of the
// direct-selection path the old 1/2/3 rune keys used to inline here — the
// command table is their only caller since the rune captures retired (step
// 2). Argument validation (the enum membership) lives in the command layer;
// an unknown value falls back to the listed default branch.
func (d *detailPane) setWindow(arg string) {
	switch arg {
	case "1h":
		d.filters.window = time.Hour
	case "7d":
		d.filters.window = 7 * 24 * time.Hour
	default: // "24h"
		d.filters.window = 24 * time.Hour
	}
	d.resetPaging()
}

func (d *detailPane) setRevision(arg string) {
	if arg == "all" {
		d.filters.revision = "all"
	} else { // "current"
		d.filters.revision = ""
	}
	d.resetPaging()
}

func (d *detailPane) setSource(arg string) {
	switch arg {
	case store.SourceManual, store.SourceAll:
		d.filters.source = arg
	default: // store.SourceScheduled
		d.filters.source = store.SourceScheduled
	}
	d.resetPaging()
}
