package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// Action handlers for the /probe and /report commands. They run on the app
// goroutine, mutate state via Deps, and report through the status line — the
// TUI equivalent of the panel's inline messages.

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
	// Report dir: settings override, else <data>/reports (R5).
	dir, err := u.reportDir()
	if err != nil {
		return "", err
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

// reportDir resolves and creates the export directory shared by every
// export action: the settings override, else <data>/reports (R5).
func (u *ui) reportDir() (string, error) {
	dir := u.deps.Store.GetSettings().ReportDir
	if dir == "" {
		dir = u.deps.Store.Dir() + string(filepath.Separator) + "reports"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建报告目录失败：%w", err)
	}
	return dir, nil
}

// exportCacheSnapshot writes the all-providers JSON snapshot of the latest
// cache-check verdicts (/exportcache). No selection is needed — the
// snapshot is per-tool, not per-provider; unchecked providers are included
// (未检测) so the file is a complete picture. Same file the panel downloads
// via GET /api/cache-check/export.
func (u *ui) exportCacheSnapshot() {
	dir, err := u.reportDir()
	if err != nil {
		u.setStatus("[red]" + tviewEscape(view.CmdMsgCacheExportFailed(err.Error())) + "[-]")
		return
	}
	snap := view.CacheCheckSnapshotOf(u.deps.Store.GetProviders(), u.deps.Store.LoadCacheCheck)
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		u.setStatus("[red]" + tviewEscape(view.CmdMsgCacheExportFailed(err.Error())) + "[-]")
		return
	}
	path := filepath.Join(dir, "cache-check-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		u.setStatus("[red]" + tviewEscape(view.CmdMsgCacheExportFailed(err.Error())) + "[-]")
		return
	}
	u.setStatus("[green]" + tviewEscape(view.CmdMsgCacheExportDone(path)) + "[-]")
}

// probeNow runs one manual probe for the selected provider on a background
// goroutine and surfaces the outcome on the status line: the result line on
// success, the in-flight conflict message on 409-equivalent errors (§4.6).
// toggleCurrent flips the selected provider's enabled flag (/toggle): a
// regular Update — the engine cancels the in-flight probe (persisted
// cancelled) and stops/resumes the scheduler; manual probes remain
// available while disabled. Revision is untouched (enabled is not a
// target change, §4.1).
func (u *ui) toggleCurrent() {
	id := u.overview.selectedID()
	if id <= 0 {
		u.setStatus("[gray]" + view.CmdMsgNeedSelection + "[-]")
		return
	}
	p, ok := u.deps.Store.GetProvider(id)
	if !ok {
		u.setStatus("[gray]监测对象已不存在[-]")
		return
	}
	p.Enabled = !p.Enabled
	if err := u.deps.Store.UpdateProvider(p); err != nil {
		u.setStatus("[red]操作失败：" + tviewEscape(err.Error()) + "[-]")
		return
	}
	if u.deps.Mutator != nil {
		u.deps.Mutator.Update(p)
	}
	u.afterMutation()
	if p.Enabled {
		u.setStatus("[green]已启用 " + tviewEscape(p.Name) + "：恢复自动定时探测[-]")
	} else {
		u.setStatus("[orange]已停用 " + tviewEscape(p.Name) + "：已停止自动定时探测（手动测试仍可用）[-]")
	}
}

func (u *ui) probeNow(p store.Provider) {
	if u.deps.Engine == nil {
		u.setStatus("[red]" + view.CmdMsgEngineNotReady + "[-]")
		return
	}
	id := p.ID
	u.setStatus("[yellow]正在探测 " + tviewEscape(p.Name) + " ……[-]")
	go func() {
		res, err := u.deps.Engine.ProbeNow(id)
		u.queueDraw(func() {
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
				if res.Status == store.StatusOK {
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

// cacheCheckNow runs one two-hop prefix-cache detection for the selected
// provider (/cache, task 10-22-cache-check): flash on dispatch, the engine
// call on a background goroutine, the outcome back on the app goroutine via
// queueDraw — the probeNow pattern. Fully independent of the probe machinery
// (prd R5): the conflict flash covers the engine's own per-provider
// cache-check mutex, and on success the redraw picks up the freshly persisted
// record for the detail block (prd R4).
func (u *ui) cacheCheckNow(p store.Provider) {
	if u.deps.Engine == nil {
		u.setStatus("[red]" + view.CmdMsgEngineNotReady + "[-]")
		return
	}
	id := p.ID
	u.setStatus("[yellow]" + view.CmdMsgCacheCheckRunning + "[-]")
	go func() {
		rec, err := u.deps.Engine.CacheCheck(id)
		u.queueDraw(func() {
			switch {
			case err != nil && u.deps.Engine.IsCacheCheckInFlight(err):
				u.setStatus("[orange]" + view.CmdMsgCacheCheckInFlight + "[-]")
			case err != nil:
				u.setStatus("[red]" + tviewEscape(view.CmdMsgCacheCheckFailed(err.Error())) + "[-]")
			default:
				v := view.CacheCheckViewOf(p, rec, true)
				at := time.UnixMilli(v.CheckedAt).Format("01-02 15:04:05")
				u.setStatus("[" + cacheVerdictColor(v.Verdict) + "]" +
					view.CmdMsgCacheCheckDone(v.VerdictLabel, at) + "[-]")
				u.redrawLocked() // the persisted record lands in the detail block now
			}
		})
	}()
}
