package view

import "strings"

// Command-bar texts (task 10-16-tui-command-bar, design.md §7). The
// status-bar guide line and every command feedback message live here so the
// TUI hardcodes no user-visible status string (go-conventions.md status
// contract). The only command texts that travel with internal/tui are the
// per-command descriptions in the tui command table itself — they share the
// table with the handlers and cannot drift from it (prd R5).
//
// Messages are plain text; the TUI adds its color tags around them and must
// not inject user-controlled text into them.

// CommandHintText is the status-bar guide line while the command bar holds
// focus — the app's home state. It replaces the old shortcut guide (prd R4).
// Record paging is a command (/page), not a key: from the command bar PgDn
// does nothing, so the guide must not promise it. Esc is two-stage: it
// clears typed text first and enters pane selection on an empty input.
func CommandHintText() string {
	return "输入 / 查看命令（/help） · Esc 清空/切换版块 · Ctrl+C 退出"
}

// ListHintText is the status-bar guide line while the provider list holds
// focus: typing goes nowhere from here (commands live in the command bar), so
// the line must say what DOES work instead of going silent — otherwise Esc-ed
// away users read the silent keyboard as a hang (10-16 freeze report).
func ListHintText() string {
	return "↑↓ 切换监测对象 · 命令请回命令栏（Esc 切换版块）"
}

// DetailHintText is the status-bar guide line while the detail view holds
// focus (scrolling only; same anti-silence rationale as ListHintText).
func DetailHintText() string {
	return "PgDn/PgUp 滚动详情 · 命令请回命令栏（Esc 切换版块）"
}

// CmdPlaceholderText is the grayed hint shown inside the empty command input.
// It lives here — not in internal/tui — so it cannot drift from
// CommandHintText (same wording family, single owner).
func CmdPlaceholderText() string {
	return "输入 / 执行命令（/help 查看全部）"
}

// CmdHelpGuideText is the one-line menu-interaction guide at the top of the
// /help listing (plain text; the TUI adds its color tags).
func CmdHelpGuideText() string {
	return "输入 / 过滤 · Tab 补全 · Esc 清空/切换版块"
}

// Command feedback messages, flashed on the status line by the TUI command
// bar (plain text — the TUI wraps them with color tags).
const (
	// CmdMsgUnknownCommand: Enter hit a "/name" that is not in the registry.
	CmdMsgUnknownCommand = "未知命令，输入 / 查看全部"
	// CmdMsgNeedSelection: a selection-scoped command ran with nothing
	// selected (prd R3: 命令作用于当前选中项,未选中不执行).
	CmdMsgNeedSelection = "请先选择监测对象"
	// CmdMsgHelpListed: /help rendered the full command list into the detail
	// pane; the listing itself is temporary until the next redraw.
	CmdMsgHelpListed = "全部命令已列出（输入 / 可随时唤出菜单）"
	// CmdMsgAmbiguousCommand: Enter in the command phase with several
	// candidates. Firing the highlighted entry would make a bare "/" execute
	// /exit, the registry's first entry (review-fixpack-2 F12) — keep typing
	// or Tab-complete instead.
	CmdMsgAmbiguousCommand = "匹配多条命令：继续输入或 Tab 补全"
	// CmdMsgPageNoMore: /page next on the newest-and-oldest boundary.
	CmdMsgPageNoMore = "已是最后一页"
	// CmdMsgPageNoPrev: /page prev on the first page.
	CmdMsgPageNoPrev = "已是第一页"
)

// CmdMsgNoArg formats the feedback for a zero-argument command that received
// an argument (design.md §3 execute step 4).
func CmdMsgNoArg(name string) string {
	return name + " 不接受参数"
}

// CmdMsgInvalidArg formats the illegal-argument feedback with the command's
// legal value set (prd R2: 参数非法不执行, flash 合法值集).
func CmdMsgInvalidArg(name string, args []string) string {
	return name + " 参数非法，合法值：" + strings.Join(args, " / ")
}
