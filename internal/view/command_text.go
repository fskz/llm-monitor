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

// CommandHintText is the status-bar guide line of the command-bar era. It
// replaces the old shortcut guide once the rune keys retire (prd R4).
func CommandHintText() string {
	return "输入 / 查看命令（/help） · Tab 切换焦点 · PgDn/PgUp 翻页 · Ctrl+C 退出"
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
	return "输入 / 过滤 · Tab 补全 · Esc 清空"
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
