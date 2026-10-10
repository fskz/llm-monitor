// Package tui is the default interactive front end (task 10-09-tui-default):
// the process runs with ZERO listening ports until the user pulls up the web
// panel from inside the TUI (R1/R2).
//
// Dependency direction: tui → view/store only (design.md §1). It must not
// import internal/server or internal/engine; the engine arrives through the
// narrow view interfaces, adapted in cmd.
package tui

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// Deps wires everything the TUI needs (design.md §4). Store owns the data;
// Engine/Mutator are the shared narrow engine slices (view.EngineAPI /
// view.ProviderMutator), both optional for tests; PortStart is the first
// port the on-demand web panel scans from (the --port flag); ServerHook
// builds the on-demand web handler for a bound port — an indirection so tui
// never imports internal/server (cmd passes server.New's handler factory).
type Deps struct {
	Store      *store.Store
	Engine     view.EngineAPI
	Mutator    view.ProviderMutator
	PortStart  int
	ServerHook func(boundPort int) http.Handler
}

// refreshInterval matches the web panel polling cadence (web/app.js: 5s).
const refreshInterval = 5 * time.Second

// ui is the assembled application: panes, shared selection state and the
// redraw pipeline. All tview mutations happen on the app goroutine (direct
// during setup, via QueueUpdateDraw afterwards).
type ui struct {
	app      *tview.Application
	deps     Deps
	overview *overviewPane
	detail   *detailPane
	web      *webPanel

	detailView *tview.TextView // right pane target (rebuilt content)
	statusBar  *tview.TextView
	right      *tview.Flex
	pages      *tview.Pages // main layout + modal overlays (forms, confirm)

	mu sync.Mutex // guards selectedID transfer between panes
}

// Run blocks the main goroutine on the TUI event loop and returns when the
// user quits (q; Ctrl+C is delivered as a key in raw mode and stops the app
// via tview itself) or when ctx is cancelled (external SIGINT/SIGTERM).
// Either way the caller proceeds down the graceful-shutdown path; the
// returned error is ctx.Err() for a context exit and nil for a user quit.
//
// When no terminal is available (headless run, stdin not a tty) tview cannot
// initialize a screen; Run then keeps the process alive in degraded no-UI
// mode until ctx is cancelled. Unattended operation is unsupported by design
// (PRD R1) — the engine keeps working, but no UI or web panel is available,
// and the process ends with the terminal's lifetime.
func Run(ctx context.Context, deps Deps) error {
	return run(ctx, deps, nil)
}

// run is Run with an injectable screen for tests (tcell simulation screen);
// a non-nil screen is initialized by tview on the first Run call.
func run(ctx context.Context, deps Deps, screen tcell.Screen) error {
	app := tview.NewApplication()
	if screen != nil {
		app.SetScreen(screen)
	}

	u := &ui{app: app, deps: deps}
	u.detail = newDetailPane(deps.Store, engineOrNil(deps.Engine))
	u.overview = newOverviewPane(deps.Store, engineOrNil(deps.Engine), u.selectProvider)
	u.web = newWebPanel(deps)

	u.detailView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	u.right = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(u.detailView, 0, 1, true).
		AddItem(u.statusBar, 1, 0, false)

	listHeader := tview.NewTextView().SetDynamicColors(true).
		SetText("[::b]监测对象（↑↓ 切换）[-]")
	listPane := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(listHeader, 1, 0, false).
		AddItem(u.overview.list, 0, 1, true)

	root := tview.NewFlex().
		AddItem(listPane, 28, 0, true).
		AddItem(u.right, 0, 4, false)

	// Pages carry the main layout plus modal overlays (forms, delete
	// confirm); overlays are added on demand and hidden again on close.
	u.pages = tview.NewPages().AddPage("main", root, true, true)

	u.overview.wireSelection()
	u.detailView.SetInputCapture(u.detail.inputCapture(app, u.redrawFromDetail))
	u.overview.list.SetInputCapture(u.overviewKeys)
	app.SetInputCapture(u.globalKeys)
	app.SetRoot(u.pages, true).EnableMouse(false)

	// Initial draw before the loop: selection defaults to the first provider.
	u.redrawLocked()

	done := make(chan error, 1)
	go func() { done <- app.Run() }()

	refresh := time.NewTicker(refreshInterval)
	defer refresh.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-refresh.C:
				// 5s re-read of the store (R3): selection and scroll survive
				// because both panes restore by id/offset inside redraw.
				app.QueueUpdateDraw(func() {
					u.redrawLocked()
				})
			}
		}
	}()

	select {
	case <-ctx.Done():
		// Stop may race with screen creation inside Run (a Stop before the
		// screen exists is a no-op), so retry until the app actually exits.
		for {
			app.Stop()
			select {
			case <-done:
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	case err := <-done:
		if err != nil {
			log.Printf("tui: 无法初始化终端界面（%v）；以无界面模式继续运行，等待退出信号", err)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil // user quit through the TUI
	}
}

// engineOrNil normalizes a typed-nil EngineAPI to untyped nil so view.ViewOf
// skips the probing hint instead of calling into a nil interface method.
func engineOrNil(eng view.EngineAPI) view.EngineStatus {
	if eng == nil {
		return nil
	}
	return eng
}

// selectProvider re-renders the detail pane for a newly selected id. Runs on
// the app goroutine (list changed-callback or QueueUpdateDraw).
func (u *ui) selectProvider(id int) {
	u.detail.resetPaging()
	u.redrawLocked()
}

// redrawFromDetail is the redraw callback the detail-pane filter keys use.
func (u *ui) redrawFromDetail() { u.redrawLocked() }

// redrawLocked refreshes both panes and the status bar. Caller must be on
// the app goroutine.
func (u *ui) redrawLocked() {
	// Remember scroll position of the detail view across the redraw.
	_, offsetY := u.detailView.GetScrollOffset()
	u.overview.refresh()
	if id := u.overview.selectedID(); id > 0 {
		if p, ok := u.deps.Store.GetProvider(id); ok {
			u.detailView.SetText(u.detail.renderText(p))
		} else {
			u.detailView.SetText("[gray]所选对象已不存在[white]")
		}
	} else {
		u.detailView.SetText("[gray]暂无监测对象[white]")
	}
	u.detailView.ScrollTo(offsetY, 0)
	u.renderStatus()
}

func (u *ui) renderStatus() {
	f := u.detail.filters
	u.statusBar.SetText(
		"[gray]1/2/3 窗口(" + f.windowLabel() + ") · R 版本 · S 来源(" + f.sourceLabel() +
			") · PgDn/PgUp 翻页 · Tab 切换栏 · q 退出[white]")
}

// globalKeys handles application-level keys: quit and the actions that must
// work wherever the focus sits when no modal is open. Focus-specific keys
// (list navigation, detail filters) are captured earlier and bubble up here
// when unhandled.
func (u *ui) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() == tcell.KeyRune {
		switch ev.Rune() {
		case keyQuit, 'Q':
			u.app.Stop()
			return nil
		case keyProbe:
			if id := u.overview.selectedID(); id > 0 {
				if p, ok := u.deps.Store.GetProvider(id); ok {
					u.probeNow(p)
				}
			}
			return nil
		case keyExport:
			u.exportCurrent()
			return nil
		case keyWeb:
			u.toggleWeb()
			return nil
		}
	}
	return ev
}

// overviewKeys adds the management keys on the provider list: n = 新建,
// e = 编辑, d = 删除 (design.md §7).
func (u *ui) overviewKeys(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() == tcell.KeyRune {
		switch ev.Rune() {
		case keyNew, keyEdit, keyDelete:
			if u.modalOpen() {
				return ev // a modal owns the keyboard
			}
			id := u.overview.selectedID()
			if (ev.Rune() == keyNew || id > 0) && ev.Rune() == keyNew {
				u.openFormModal(0)
				return nil
			}
			if id <= 0 {
				u.setStatus("[gray]请先选择监测对象[-]")
				return nil
			}
			switch ev.Rune() {
			case keyEdit:
				u.openFormModal(id)
			case keyDelete:
				u.openDeleteModal(id)
			}
			return nil
		}
	}
	return ev
}

// modalOpen reports whether an overlay page is visible.
func (u *ui) modalOpen() bool {
	name, _ := u.pages.GetFrontPage()
	return name != "main"
}

// openFormModal mounts the add (id==0) or edit form as a centered overlay.
func (u *ui) openFormModal(id int) {
	d := openForm(u.app, u.deps, id, u.afterMutation)
	formDismissHook = u.dismissModal
	u.pages.AddAndSwitchToPage("modal", d.primitive(), true)
}

// openDeleteModal mounts the delete confirmation over the UI.
func (u *ui) openDeleteModal(id int) {
	modal := confirmDelete(u.app, u.deps, id, func() {
		u.dismissModal()
		u.afterMutation()
	})
	formDismissHook = u.dismissModal // Esc/route-out reuses the same dismiss
	u.pages.AddAndSwitchToPage("modal", modal, true)
}

// dismissModal tears the overlay down and returns focus to the main layout.
func (u *ui) dismissModal() {
	u.pages.RemovePage("modal")
	u.pages.SwitchToPage("main")
	formDismissHook = nil
}

// afterMutation redraws after add/edit/delete (the store changed).
func (u *ui) afterMutation() {
	u.detail.resetPaging()
	u.redrawLocked()
}

// exportCurrent writes the HTML report of the selected provider with the
// current detail filters and shows the path (R3).
func (u *ui) exportCurrent() {
	id := u.overview.selectedID()
	if id <= 0 {
		u.setStatus("[gray]请先选择监测对象[-]")
		return
	}
	p, ok := u.deps.Store.GetProvider(id)
	if !ok {
		u.setStatus("[gray]监测对象已不存在[-]")
		return
	}
	path, err := u.exportReport(p)
	if err != nil {
		u.setStatus("[red]" + tviewEscape(err.Error()) + "[-]")
		return
	}
	u.setStatus("[green]报告已导出：[-]" + tviewEscape(path) + " [gray]（w 打开 Web 面板查看）[-]")
}

// toggleWeb pulls the on-demand panel up or down (R2). The status line
// always shows the outcome — bound URL, release, or the failure reason.
func (u *ui) toggleWeb() {
	if u.web.running() {
		u.web.stop()
		u.setStatus("[green]Web 面板已关闭，端口已释放[-]")
		return
	}
	url, err := u.web.start()
	if err != nil {
		u.setStatus("[red]Web 面板启动失败：" + tviewEscape(err.Error()) + "[-]")
		return
	}
	u.setStatus("[green]Web 面板：[-]" + url + " [gray]（w 关闭）[-]")
}

// setStatus writes the one-line message area.
func (u *ui) setStatus(msg string) {
	u.statusBar.SetText(msg)
}
