package view

import (
	"strings"
	"testing"
)

// Task 10-16-tui-command-bar step 1: the command-bar guide line and feedback
// messages are view-owned so the TUI hardcodes no status string (A6).

func TestCommandHintText(t *testing.T) {
	got := CommandHintText()
	for _, want := range []string{"/", "/help", "Tab", "Ctrl+C"} {
		if !strings.Contains(got, want) {
			t.Errorf("CommandHintText() = %q, want it to mention %q", got, want)
		}
	}
}

// The input placeholder and the /help header guide are view-owned too (task
// 10-16 step 2: they were the last guide strings hardcoded in internal/tui,
// drifting against CommandHintText).
func TestCmdPlaceholderText(t *testing.T) {
	got := CmdPlaceholderText()
	for _, want := range []string{"/", "/help"} {
		if !strings.Contains(got, want) {
			t.Errorf("CmdPlaceholderText() = %q, want it to mention %q", got, want)
		}
	}
}

func TestCmdHelpGuideText(t *testing.T) {
	got := CmdHelpGuideText()
	for _, want := range []string{"/", "Tab", "Esc"} {
		if !strings.Contains(got, want) {
			t.Errorf("CmdHelpGuideText() = %q, want it to mention %q", got, want)
		}
	}
}

// The away-station guide lines must name the station's real keys AND point
// back to the command bar — a silent keyboard reads as a hang (10-16 freeze
// report: Tab-ed away users typed commands into a list that ignores runes).
func TestFocusHintTexts(t *testing.T) {
	if got := ListHintText(); !strings.Contains(got, "↑↓") || !strings.Contains(got, "Tab") {
		t.Errorf("ListHintText() = %q, want ↑↓ guidance and a Tab pointer", got)
	}
	if got := DetailHintText(); !strings.Contains(got, "PgDn") || !strings.Contains(got, "Tab") {
		t.Errorf("DetailHintText() = %q, want PgDn guidance and a Tab pointer", got)
	}
	if ListHintText() == CommandHintText() || DetailHintText() == CommandHintText() {
		t.Error("focus hints must differ from the command-bar hint")
	}
}

func TestCommandFeedbackMessages(t *testing.T) {
	if got := CmdMsgUnknownCommand; !strings.Contains(got, "未知命令") || !strings.Contains(got, "/") {
		t.Errorf("CmdMsgUnknownCommand = %q, want the 未知命令 wording with a / hint", got)
	}
	if got := CmdMsgNeedSelection; got != "请先选择监测对象" {
		t.Errorf("CmdMsgNeedSelection = %q, want 请先选择监测对象", got)
	}
	if got := CmdMsgHelpListed; !strings.Contains(got, "全部命令") {
		t.Errorf("CmdMsgHelpListed = %q, want it to mention 全部命令", got)
	}
}

func TestCmdMsgNoArg(t *testing.T) {
	got := CmdMsgNoArg("/quit")
	if got != "/quit 不接受参数" {
		t.Fatalf("CmdMsgNoArg(/quit) = %q, want \"/quit 不接受参数\"", got)
	}
}

func TestCmdMsgInvalidArg(t *testing.T) {
	got := CmdMsgInvalidArg("/window", []string{"1h", "24h", "7d"})
	if !strings.Contains(got, "/window") || !strings.Contains(got, "1h / 24h / 7d") {
		t.Fatalf("CmdMsgInvalidArg = %q, want the command name and the full legal set", got)
	}
	// Single-value set must not carry a separator.
	if got := CmdMsgInvalidArg("/x", []string{"a"}); strings.Contains(got, "/") && strings.HasSuffix(got, "/") {
		t.Fatalf("CmdMsgInvalidArg single = %q, unexpected trailing separator", got)
	}
}
