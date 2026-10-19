package tui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/view"
)

// Command bar (task 10-16-tui-command-bar): the bottom /xxx input plus its
// prefix-filtered menu replaces the scattered rune shortcuts as the single
// action entry (prd R1/R2). Ported from mos-runner tui/command.go (same
// tview/tcell stack) with the argument-candidate extension for the three
// enum commands; the handlers reuse the existing ui methods (design.md §4).
//
// Red lines (design.md §6, mos-runner field-proven):
//   - SetChangedFunc only does a synchronous SetText on the menu view —
//     QueueUpdateDraw would redraw before the character lands in the field
//     and eat the input.
//   - The menu is a permanently-present TextView, never a Pages overlay.
//   - Tab is ALWAYS intercepted at the control level, but only ever
//     completes (menu open) — InputField treats Tab as a finished key and
//     would jump focus on its own. Focus rotation lives on Esc
//     (2026-10-19): Esc clears a non-empty input, and on an empty input
//     enters pane selection (版块轮转 命令栏→列表→详情→命令栏).
//   - The app-level capture runs first, so globalKeys passes Tab and Esc
//     through while the command bar holds focus (the bar's capture wins).

// command is one registry entry. desc is the ONLY user-visible text that
// travels with the tui package (prd R5); every status/flash wording comes
// from internal/view.
type command struct {
	name string // with the / prefix, e.g. "/window"
	args []string
	desc string
	// needSel: the command acts on the currently selected provider and must
	// not run with nothing selected (prd R3 / design.md §3 step 7).
	needSel bool
	// run executes the command; arg == "" means the bare form (cycling for
	// the enum commands, Q5).
	run func(u *ui, arg string)
}

// commands is the 15-entry registry (prd R1 + review-fixpack-2 F10: /page).
// Assembled in init() like mos-runner: /help closes over the table itself,
// which a var initializer could not reference without a cycle. No command
// name may be a prefix of another — Enter's unique-prefix execution and the
// ambiguous-entry guard below both lean on that invariant.
var commands []command

func init() {
	commands = []command{
		{name: "/exit", desc: "退出（Ctrl+C 也可）", run: func(u *ui, arg string) { u.app.Stop() }},
		{name: "/probe", desc: "立即探测当前对象", needSel: true, run: func(u *ui, arg string) {
			if p, ok := u.deps.Store.GetProvider(u.overview.selectedID()); ok {
				u.probeNow(p)
			}
		}},
		{name: "/report", desc: "导出当前对象 HTML 报告", run: func(u *ui, arg string) { u.exportCurrent() }},
		{name: "/web", desc: "拉起 / 关闭 Web 面板", run: func(u *ui, arg string) { u.toggleWeb() }},
		{name: "/settings", desc: "打开全局设置", run: func(u *ui, arg string) { u.openSettingsModal() }},
		{name: "/toggle", desc: "启用 / 停用当前对象", needSel: true, run: func(u *ui, arg string) { u.toggleCurrent() }},
		{name: "/new", desc: "新建监测对象", run: func(u *ui, arg string) { u.openFormModal(0) }},
		{name: "/edit", desc: "编辑当前对象", needSel: true, run: func(u *ui, arg string) { u.openFormModal(u.overview.selectedID()) }},
		{name: "/delete", desc: "删除当前对象（需确认）", needSel: true, run: func(u *ui, arg string) { u.openDeleteModal(u.overview.selectedID()) }},
		// window/revision/source follow the detail filter state machine; the
		// bare form cycles, an argument selects directly (design.md §4).
		{name: "/window", args: []string{"1h", "24h", "7d"}, desc: "时间窗（不带参数=循环切换）", needSel: true,
			run: func(u *ui, arg string) {
				if arg == "" {
					u.detail.cycleWindow()
				} else {
					u.detail.setWindow(arg)
				}
				u.redrawFromDetail()
			}},
		{name: "/revision", args: []string{"current", "all"}, desc: "版本过滤（不带参数=循环切换）", needSel: true,
			run: func(u *ui, arg string) {
				if arg == "" {
					u.detail.cycleRevision()
				} else {
					u.detail.setRevision(arg)
				}
				u.redrawFromDetail()
			}},
		{name: "/source", args: []string{"scheduled", "manual", "all"}, desc: "来源过滤（不带参数=循环切换）", needSel: true,
			run: func(u *ui, arg string) {
				if arg == "" {
					u.detail.cycleSource()
				} else {
					u.detail.setSource(arg)
				}
				u.redrawFromDetail()
			}},
		// /page pages the detail records (review-fixpack-2 F10): the table
		// cursor machinery predates the command bar but was unreachable once
		// the rune keys retired. Bare form = next page (the common direction),
		// unlike the filter commands' cycling.
		{name: "/page", args: []string{"next", "prev"}, desc: "明细记录翻页（不带参数=下一页）", needSel: true,
			run: func(u *ui, arg string) {
				if arg == "prev" {
					if !u.detail.pagePrev() {
						u.setStatus("[gray]" + view.CmdMsgPageNoPrev + "[-]")
						return
					}
				} else {
					if !u.detail.pageNext() {
						u.setStatus("[gray]" + view.CmdMsgPageNoMore + "[-]")
						return
					}
				}
				u.redrawFromDetail()
			}},
		{name: "/refresh", desc: "立即刷新全部数据", run: func(u *ui, arg string) { u.refreshNow() }},
		{name: "/help", desc: "查看全部命令", run: func(u *ui, arg string) { u.helpCmd() }},
	}
}

// menuEntry is one highlighted candidate in the menu. Command phase: arg is
// empty and cmd is the candidate. Argument phase: arg is the candidate value
// of cmd (design.md §3 filter state 3).
type menuEntry struct {
	cmd *command
	arg string
}

// label is what the menu line shows (the argument when present).
func (m menuEntry) label() string {
	if m.arg != "" {
		return m.arg
	}
	return m.cmd.name
}

// commandBar is the bottom input field plus its menu state.
type commandBar struct {
	u     *ui
	input *tview.InputField
	menu  *tview.TextView

	menuItems []menuEntry // filtered candidates (commands or arguments)
	menuSel   int         // highlighted index, -1 = none
}

// newCommandBar builds the input field and the permanently-present menu
// view. Wiring happens against the ui; no draw call here — the changed
// callback keeps the menu in sync synchronously (red line §6-1).
func newCommandBar(u *ui) *commandBar {
	cb := &commandBar{u: u, menuSel: -1}
	cb.input = tview.NewInputField().
		SetLabel(" > ").
		SetPlaceholder(view.CmdPlaceholderText()).
		SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.ColorGray)).
		SetFieldWidth(0) // fill the width
	cb.menu = tview.NewTextView().SetDynamicColors(true)
	// Text changed → refilter and repaint the menu synchronously. The app
	// draws once more after the key event completes; a QueueUpdateDraw here
	// would redraw before the rune is inserted and swallow the input.
	cb.input.SetChangedFunc(func(string) {
		cb.filter()
		cb.drawMenu()
	})
	cb.input.SetBorder(true).SetTitle(" 命令 ")
	cb.input.SetInputCapture(cb.handleKey)
	return cb
}

// filter recomputes the candidates from the input text. Three states
// (design.md §3): no "/" prefix → menu closed; "/name" → prefix-filtered
// commands; "/name arg…" (space present) → the located command's argument
// candidates, prefix-filtered on the argument part. The text is used RAW —
// trimming would eat the trailing space that switches to the argument
// candidates (prd R2); only the argument part is trimmed.
func (cb *commandBar) filter() {
	cb.menuItems = nil
	cb.menuSel = -1

	text := cb.input.GetText()
	if !strings.HasPrefix(text, "/") {
		return // plain text never opens the menu (anti-mistouch)
	}
	if space := strings.IndexByte(text, ' '); space >= 0 {
		// Argument phase: the command must be an exact match and take args.
		name, argPart := text[:space], strings.TrimSpace(text[space+1:])
		cmd := lookupCommand(name)
		if cmd == nil || len(cmd.args) == 0 {
			return
		}
		for _, a := range cmd.args {
			if argPart == "" || strings.HasPrefix(a, argPart) {
				cb.menuItems = append(cb.menuItems, menuEntry{cmd: cmd, arg: a})
			}
		}
	} else {
		for i := range commands {
			if strings.HasPrefix(commands[i].name, text) {
				cb.menuItems = append(cb.menuItems, menuEntry{cmd: &commands[i]})
			}
		}
	}
	if len(cb.menuItems) > 0 {
		cb.menuSel = 0
	}
}

// menuOpen reports whether the menu currently shows candidates.
func (cb *commandBar) menuOpen() bool { return len(cb.menuItems) > 0 }

// drawMenu republishes the menu text. Synchronous SetText only (red line
// §6-1/2): the view is permanently in the layout, so this never touches
// Pages or the layout geometry. The viewport follows the highlight — the
// fixed 3-row menu cannot be scrolled by hand (it never gets focus), so
// without this the highlighted entry walks out of sight (review-fixpack-2
// F11).
func (cb *commandBar) drawMenu() {
	if cb.menu != nil {
		cb.menu.SetText(cb.menuText())
		if cb.menuSel > 0 {
			cb.menu.ScrollTo(cb.menuSel-1, 0) // highlight on the 2nd row, next row visible
		} else {
			cb.menu.ScrollTo(0, 0)
		}
	}
}

// menuText renders the candidate window: "▸ " marks the highlighted entry,
// command names (or argument values) are left-aligned at 14 columns followed
// by the description.
func (cb *commandBar) menuText() string {
	if !cb.menuOpen() {
		return ""
	}
	var b strings.Builder
	for i, e := range cb.menuItems {
		marker, style := "  ", "[white]"
		if i == cb.menuSel {
			marker, style = "▸ ", "[yellow::b]"
		}
		fmt.Fprintf(&b, "%s%s%-14s[-] %s\n", marker, style, e.label(), e.cmd.desc)
	}
	return b.String()
}

// handleKey is the command field's input capture: ↑/↓ move the menu
// selection (cyclically), Tab completes it (menu open only), Enter
// executes, Esc clears a non-empty input or — on an empty input — enters
// pane selection (rotateFocus). A modal always wins (prd R3/A8: the
// command bar must not steal input from an open form).
func (cb *commandBar) handleKey(ev *tcell.EventKey) *tcell.EventKey {
	if cb.u.modalOpen() {
		return ev
	}
	switch ev.Key() {
	case tcell.KeyUp, tcell.KeyDown:
		if !cb.menuOpen() {
			return ev // passthrough: single-line field has no ↑/↓ behavior
		}
		if ev.Key() == tcell.KeyDown {
			cb.menuSel = (cb.menuSel + 1) % len(cb.menuItems)
		} else {
			cb.menuSel = (cb.menuSel - 1 + len(cb.menuItems)) % len(cb.menuItems)
		}
		cb.drawMenu()
		return nil
	case tcell.KeyTab:
		// Completion only (red line §6-3): Tab stays intercepted even with
		// the menu closed — InputField would treat it as a finished key and
		// jump focus on its own. Rotation moved to Esc (2026-10-19).
		if cb.menuOpen() {
			cb.completeSelection()
		}
		return nil
	case tcell.KeyEnter:
		cb.execute()
		return nil
	case tcell.KeyEscape:
		// First Esc dismisses typed text (SetText triggers the changed
		// callback → filter + menu repaint); on an empty input it enters
		// pane selection — focus walks 命令栏 → 列表 → 详情 → 命令栏.
		if cb.input.GetText() != "" {
			cb.input.SetText("")
		} else {
			cb.u.rotateFocus()
		}
		return nil
	}
	return ev
}

// completeSelection copies the highlighted candidate into the input: the
// command name in the command phase, "name arg" in the argument phase. The
// changed callback refilters from there.
func (cb *commandBar) completeSelection() {
	if !cb.menuOpen() {
		return
	}
	e := cb.menuItems[cb.menuSel]
	if e.arg != "" {
		cb.input.SetText(e.cmd.name + " " + e.arg)
	} else {
		cb.input.SetText(e.cmd.name)
	}
}

// execute dispatches Enter. With the menu open the highlighted candidate
// runs (command or argument direct-select, prd R2); otherwise the typed text
// is parsed. Validation order (design.md §3): unknown command, argument on
// a zero-arg command, illegal enum argument, missing selection — every
// failure flashes the view-owned message and keeps the input for editing;
// only a passing command clears the field first, then runs (先清后跑).
func (cb *commandBar) execute() {
	text := strings.TrimSpace(cb.input.GetText())
	if !strings.HasPrefix(text, "/") {
		return // plain text is swallowed, never executed (anti-mistouch)
	}

	var cmd *command
	var arg string
	if cb.menuOpen() {
		e := cb.menuItems[cb.menuSel]
		// Command phase with several candidates must NOT fire the highlighted
		// entry: a bare "/" highlights /exit (registry order), so "/" + Enter
		// would quit the app (review-fixpack-2 F12). Argument-phase candidates
		// all share one command — direct-select stays allowed. No command name
		// prefixes another, so several command candidates always mean the
		// typed text is a strict ambiguous prefix, never an exact name.
		if e.arg == "" && len(cb.menuItems) > 1 {
			cb.flash(view.CmdMsgAmbiguousCommand)
			return
		}
		cmd, arg = e.cmd, e.arg
	} else {
		name := text
		if space := strings.IndexByte(text, ' '); space >= 0 {
			name, arg = text[:space], strings.TrimSpace(text[space+1:])
		}
		cmd = lookupCommand(name)
	}
	if cmd == nil {
		cb.flash(view.CmdMsgUnknownCommand)
		return
	}
	if arg != "" && len(cmd.args) == 0 {
		cb.flash(view.CmdMsgNoArg(cmd.name))
		return
	}
	if arg != "" && !cmd.hasArg(arg) {
		cb.flash(view.CmdMsgInvalidArg(cmd.name, cmd.args))
		return
	}
	if cmd.needSel && cb.u.overview.selectedID() <= 0 {
		cb.flash(view.CmdMsgNeedSelection)
		return
	}
	cb.input.SetText("") // changed callback closes the menu
	cmd.run(cb.u, arg)
}

// hasArg reports whether arg is one of the command's enum values.
func (c *command) hasArg(arg string) bool {
	for _, a := range c.args {
		if a == arg {
			return true
		}
	}
	return false
}

// flash shows a command feedback message on the status line (the same
// channel probe/export use, design.md §7).
func (cb *commandBar) flash(msg string) {
	cb.u.setStatus("[gray]" + msg + "[-]")
}

// lookupCommand finds a registry entry by exact name.
func lookupCommand(name string) *command {
	for i := range commands {
		if commands[i].name == name {
			return &commands[i]
		}
	}
	return nil
}

// helpCmd renders the whole registry into the detail view — the command
// table is the single source, there is no second doc (design.md §4). The
// listing is temporary: the next redraw (selection change or the 5s
// refresh) restores the provider detail.
func (u *ui) helpCmd() {
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]全部命令[-] [gray]（%s）[white]\n", view.CmdHelpGuideText())
	for _, c := range commands {
		args := ""
		if len(c.args) > 0 {
			args = " " + strings.Join(c.args, " / ")
		}
		fmt.Fprintf(&b, "[yellow]%-14s[-]%s  [gray]%s[white]\n", c.name, args, c.desc)
	}
	u.detailView.SetText(b.String())
	u.setStatus("[gray]" + view.CmdMsgHelpListed + "[-]")
}
