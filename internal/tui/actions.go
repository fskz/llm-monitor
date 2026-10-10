package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// Action handlers for the 阶段 4b keys: manual probe (p), report export (r).
// They run on the app goroutine, mutate state via Deps, and report through
// the status line — the TUI equivalent of the panel's inline messages.

// exportReport writes the single-file HTML report of one provider with the
// current detail filters to the user's download-ish location and returns
// the full path for the status line (design.md §4 report.go).
//
// Location: the process working directory is unstable for a double-clicked
// binary; the OS user-download dir is not in the stdlib. The store's parent
// config dir (…/llm-monitor/) is writable by construction and stable, so
// reports land next to the data as <dir>/reports/<filename>.
func (u *ui) exportReport(p store.Provider) (string, error) {
	filters := filterState{window: 24 * time.Hour, source: store.SourceAll}
	if u.detail != nil { // full UI carries the live filters; bare ui (tests) defaults
		filters = u.detail.filters
	}
	dir := u.deps.Store.Dir() + string(filepath.Separator) + "reports"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建报告目录失败：%w", err)
	}
	path := filepath.Join(dir, view.ReportFilename(p, filters.window))
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("创建报告文件失败：%w", err)
	}
	defer f.Close()

	eng := engineOrNil(u.deps.Engine)
	if err := view.RenderReport(f, u.deps.Store, eng, p, filters.revisionOf(p),
		filters.source, filters.window); err != nil {
		return "", fmt.Errorf("渲染报告失败：%w", err)
	}
	return path, nil
}

// probeNow runs one manual probe for the selected provider on a background
// goroutine and surfaces the outcome on the status line: the result line on
// success, the in-flight conflict message on 409-equivalent errors (§4.6).
func (u *ui) probeNow(p store.Provider) {
	if u.deps.Engine == nil {
		u.setStatus("[red]探测引擎未就绪[-]")
		return
	}
	id := p.ID
	u.setStatus("[yellow]正在探测 " + tviewEscape(p.Name) + " ……[-]")
	go func() {
		res, err := u.deps.Engine.ProbeNow(id)
		u.app.QueueUpdateDraw(func() {
			switch {
			case err != nil && u.deps.Engine.IsInFlight(err):
				u.setStatus("[orange]正在探测：该对象已有在途请求[-]")
			case err != nil:
				u.setStatus("[red]探测失败：" + tviewEscape(err.Error()) + "[-]")
			case res == nil:
				u.setStatus("[red]探测失败：无结果[-]")
			default:
				suffix := ""
				if view.SlowTTFT(p, res) {
					suffix = " [orange]（首内容偏慢）[-]"
				}
				tpot := ""
				if res.Status == "ok" {
					if d := store.ResultTPS(*res, true); d != nil {
						tpot = " · TPOT " + fmtTPOT(d)
					}
				}
				u.setStatus(fmt.Sprintf("[green]%s[-]%s · TTFT %s · 总耗时 %s%s",
					view.StatusText(res.Status), suffix,
					fmtMs(res.TTFTMs), fmtMs(&res.TotalMs), tpot))
			}
		})
	}()
}
