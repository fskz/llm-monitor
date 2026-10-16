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

// ui is the assembled application: panes, the command bar, shared selection
// state and the redraw pipeline. All tview mutations happen on the app
// goroutine (direct during setup, via QueueUpdateDraw afterwards).
type ui struct {
	app      *tview.Application
	deps     Deps
	overview *overviewPane
	detail   *detailPane
	web      *webPanel
	cmd      *commandBar

	detailView *tview.TextView // right pane target (rebuilt content)
	statusBar  *tview.TextView
	pages      *tview.Pages // main layout + modal overlays (forms, confirm)

	// quit is closed once the event loop is down (Run returned); background
	// goroutines consult it before QueueUpdateDraw, which would otherwise
	// block forever on a dead loop (review fixpack #5).
	quit chan struct{}

	// Flash messages carry a timestamp: focus rotation and redraws rewrite
	// the status line with the station hint, but a fresh task flash (report
	// path, probe result) survives them for flashTTL (review-fixpack-2 F13 —
	// Tab used to erase an export path instantly).
	flashMsg string
	flashAt  time.Time
	// detailRenderedID is the provider the detail pane last rendered; when the
	// selection falls back to another provider (the selected one vanished),
	// the pagination cursors must reset with it (review-fixpack-2 F10).
	detailRenderedID int
}

// flashTTL is how long a status flash survives focus rotation/redraws before
// the station hint takes the line back.
const flashTTL = 5 * time.Second

// Run blocks the main goroutine on the TUI event loop and returns when the
// user quits (/quit through the bottom command bar, or Ctrl+C which is
// delivered as a key in raw mode and stops the app via tview itself) or when
// ctx is cancelled (external SIGINT/SIGTERM). Either way the caller proceeds
// down the graceful-shutdown path; the returned error is ctx.Err() for a
// context exit and nil for a user quit.
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
	u := newUI(app, deps)
	return u.eventLoop(ctx)
}

// newUI assembles the whole interface and returns it ready for the event
// loop (task 10-16: split out of run so tests can drive a fully wired ui on
// a dormant application without starting Run).
//
// Layout (design.md §1, Q10): the two existing columns stay unchanged in a
// main row; below it sit three full-width bands — the permanently-present
// 3-line command menu (empty when closed), the bordered command input and
// the status line migrated out of the right column's bottom.
func newUI(app *tview.Application, deps Deps) *ui {
	u := &ui{app: app, deps: deps, quit: make(chan struct{})}
	u.detail = newDetailPane(deps.Store, engineOrNil(deps.Engine))
	u.overview = newOverviewPane(deps.Store, engineOrNil(deps.Engine), u.selectProvider)
	u.web = newWebPanel(deps)
	u.cmd = newCommandBar(u)

	u.detailView = tview.NewTextView().SetDynamicColors(true).SetScrollable(true)
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	listHeader := tview.NewTextView().SetDynamicColors(true).
		SetText("[::b]监测对象（↑↓ 切换）[-]")
	listPane := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(listHeader, 1, 0, false).
		AddItem(u.overview.list, 0, 1, true)

	// Exactly one Focus=true child per Flex level (red line §6-4): focus
	// moves with app.SetFocus directly onto the control, never through Flex
	// delegation, so the flags only seed the initial tree resolution.
	main := tview.NewFlex().
		AddItem(listPane, 28, 0, true).
		AddItem(u.detailView, 0, 4, false)
	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(main, 0, 1, true).
		AddItem(u.cmd.menu, 3, 0, false).
		AddItem(u.cmd.input, 3, 0, false).
		AddItem(u.statusBar, 1, 0, false)

	// Pages carry the main layout plus modal overlays (forms, delete
	// confirm); overlays are added on demand and hidden again on close.
	u.pages = tview.NewPages().AddPage("main", root, true, true)

	u.overview.wireSelection()
	app.SetInputCapture(u.globalKeys)
	app.SetRoot(u.pages, true).EnableMouse(false)
	// After SetRoot (which resets focus onto the root): the command bar owns
	// the startup focus (Q2).
	app.SetFocus(u.cmd.input)

	// Initial draw before the loop: selection defaults to the first provider.
	u.redrawLocked()
	return u
}

// eventLoop drives app.Run plus the 5s refresh ticker and owns the shutdown
// handshake (quit channel, ctx cancellation).
func (u *ui) eventLoop(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- u.app.Run() }()

	refresh := time.NewTicker(refreshInterval)
	defer refresh.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-u.quit:
				return // the event loop is down; a queued update would hang
			case <-refresh.C:
				// 5s re-read of the store (R3): selection and scroll survive
				// because both panes restore by id/offset inside redraw.
				u.queueDraw(func() {
					u.refreshNow()
				})
			}
		}
	}()

	select {
	case <-ctx.Done():
		// Stop may race with screen creation inside Run (a Stop before the
		// screen exists is a no-op), so retry until the app actually exits.
		for {
			u.app.Stop()
			select {
			case <-done:
				close(u.quit) // the event loop is down (review fixpack #5)
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	case err := <-done:
		// The loop is down for good either way: user quit (nil) or the
		// headless fallback (screen-init error). Closing u.quit releases the
		// refresh loop and any pending manual-probe update goroutine — a
		// QueueUpdateDraw on the dead loop would block forever.
		close(u.quit)
		if err != nil {
			log.Printf("tui: 无法初始化终端界面（%v）；以无界面模式继续运行，等待退出信号", err)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil // user quit through the TUI
	}
}

// refreshNow is the single refresh pipeline shared by the 5s ticker and the
// /refresh command (design.md §4): re-read the store and redraw both panes,
// keeping selection by id and the detail scroll offset.
func (u *ui) refreshNow() { u.redrawLocked() }

// queueDraw runs f on the app goroutine unless the event loop is already
// down. QueueUpdateDraw blocks on the loop's acknowledgment; after Run has
// returned there is nobody to acknowledge, so the caller would hang forever
// (review fixpack #5). A nil quit channel (bare ui in tests) never reads as
// closed and the update goes through normally.
func (u *ui) queueDraw(f func()) {
	select {
	case <-u.quit:
		return
	default:
	}
	u.app.QueueUpdateDraw(f)
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

// redrawFromDetail is the redraw callback the /window /revision /source
// command handlers use after changing the detail filters (task 10-16 step 2:
// the rune capture that used to own this path retired).
func (u *ui) redrawFromDetail() { u.redrawLocked() }

// redrawLocked refreshes both panes and the status bar. Caller must be on
// the app goroutine.
func (u *ui) redrawLocked() {
	// Remember scroll position of the detail view across the redraw. The
	// offset is (row, column) — the previous `_, offsetY` binding read the
	// COLUMN (always 0) and silently reset the detail scroll to the top on
	// every 5s redraw (review-fixpack-2 F16, found while testing F11).
	row, _ := u.detailView.GetScrollOffset()
	u.overview.refresh()
	if id := u.overview.selectedID(); id > 0 {
		// A selection change that bypassed selectProvider (the overview's
		// deleted-provider fallback) still resets the detail pagination — the
		// cursors belong to one provider's record sequence only.
		if id != u.detailRenderedID {
			u.detail.resetPaging()
			u.detailRenderedID = id
		}
		if p, ok := u.deps.Store.GetProvider(id); ok {
			u.detailView.SetText(u.detail.renderText(p))
		} else {
			u.detailView.SetText("[gray]所选对象已不存在[white]")
		}
	} else {
		u.detailView.SetText("[gray]暂无监测对象[white]")
	}
	u.detailView.ScrollTo(row, 0)
	u.renderStatus()
}

func (u *ui) renderStatus() {
	// A fresh flash outranks the station hint (focus rotation used to erase
	// an export path instantly, review-fixpack-2 F13); after flashTTL the
	// view-owned guide line returns.
	if msg := u.currentFlash(); msg != "" {
		u.statusBar.SetText(msg)
		return
	}
	// The guide line is view-owned (prd R5/A6); task flashes reuse the same
	// status area. The line tracks the FOCUS station: from the list/detail,
	// typing does nothing (commands live in the command bar), so the hint
	// says what does work there — a silent keyboard reads as a hang
	// (10-16 freeze report).
	u.statusBar.SetText("[gray]" + u.focusHintText() + "[white]")
}

// currentFlash returns the not-yet-expired flash message, or "".
func (u *ui) currentFlash() string {
	if u.flashMsg != "" && time.Since(u.flashAt) < flashTTL {
		return u.flashMsg
	}
	return ""
}

// focusHintText picks the view-owned guide line for whichever station holds
// focus. Safe on a dormant app (GetFocus returns the seeded focus).
func (u *ui) focusHintText() string {
	switch u.app.GetFocus() {
	case u.overview.list:
		return view.ListHintText()
	case u.detailView:
		return view.DetailHintText()
	default:
		return view.CommandHintText()
	}
}

// globalKeys is the application-level capture. Task 10-16 step 2: the legacy
// rune actions retired with the command bar (A5), leaving ONE duty — the Tab
// three-station rotation (cmdInput → list → detailView → cmdInput, Q9). The
// app-level capture runs BEFORE the focused primitive, so:
//   - while a modal (form / delete confirm) is up every key must pass
//     through — otherwise typing into an input field would trigger actions
//     instead of inserting the character;
//   - while the command bar holds focus Tab passes through — the bar's own
//     capture decides (completion wins, red line §6-5).
func (u *ui) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if u.modalOpen() {
		return ev
	}
	if u.app.GetFocus() == u.cmd.input {
		return ev // the command bar owns the keyboard
	}
	if ev.Key() == tcell.KeyTab {
		u.rotateFocus()
		return nil
	}
	return ev
}

// rotateFocus walks the three focus stations (Q9): command bar → provider
// list → detail view → command bar. Focus lands with app.SetFocus directly
// on the control, never through Flex delegation (red line §6-4). The status
// hint follows the station — tview draws right after the consumed key event,
// so the new line is visible immediately.
func (u *ui) rotateFocus() {
	switch u.app.GetFocus() {
	case u.overview.list:
		u.app.SetFocus(u.detailView)
	case u.detailView:
		u.app.SetFocus(u.cmd.input)
	default: // command bar (or anything else): start from the list
		u.app.SetFocus(u.overview.list)
	}
	u.renderStatus()
}

// modalOpen reports whether an overlay page is visible.
func (u *ui) modalOpen() bool {
	name, _ := u.pages.GetFrontPage()
	return name != "main"
}

// openFormModal mounts the add (id==0) or edit form as a centered overlay.
// An edit whose target vanished (deleted from the web panel while the
// command was in flight) never opens: silently degrading into a "new" form
// would create an unrelated object the user believes they just edited
// (review-fixpack-2 F12).
func (u *ui) openFormModal(id int) {
	d := openForm(u.app, u.deps, id, u.afterMutation)
	if d.gone {
		u.setStatus("[gray]监测对象已不存在[-]")
		return
	}
	formDismissHook = u.dismissModal
	u.pages.AddAndSwitchToPage("modal", d.primitive(), true)
}

// openDeleteModal mounts the delete confirmation over the UI. onFail closes
// the modal and flashes the store error in red: a failed delete must not
// leave a stuck modal with the engine/store divergence invisible
// (review-fixpack-2 F12).
func (u *ui) openDeleteModal(id int) {
	modal := confirmDelete(u.app, u.deps, id,
		func() { // confirmed delete
			u.dismissModal()
			u.afterMutation()
		},
		u.dismissModal, // cancel (取消 button / Esc) — just close, store untouched
		func(msg string) { // store delete failed
			u.dismissModal()
			u.setStatus("[red]" + tviewEscape(msg) + "[-]")
		},
	)
	formDismissHook = u.dismissModal // Esc/route-out reuses the same dismiss
	u.pages.AddAndSwitchToPage("modal", modal, true)
}

// dismissModal tears the overlay down and returns focus to the command bar —
// the command system's home focus (mos-runner pattern: tree resolution would
// otherwise drop focus onto the list).
func (u *ui) dismissModal() {
	u.pages.RemovePage("modal")
	u.pages.SwitchToPage("main")
	formDismissHook = nil
	if u.cmd != nil {
		u.app.SetFocus(u.cmd.input)
		u.renderStatus() // the hint follows focus back to the command bar
	}
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
		u.setStatus("[gray]" + view.CmdMsgNeedSelection + "[-]")
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
	u.setStatus("[green]报告已导出：[-]" + tviewEscape(path) + " [gray]（/web 打开 Web 面板查看）[-]")
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
	u.setStatus("[green]Web 面板：[-]" + url + " [gray]（/web 关闭）[-]")
}

// setStatus writes the one-line message area as a flash with a timestamp
// (see flashTTL).
func (u *ui) setStatus(msg string) {
	u.flashMsg = msg
	u.flashAt = time.Now()
	u.statusBar.SetText(msg)
}
