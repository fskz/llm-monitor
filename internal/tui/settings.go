package tui

import (
	"os"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
)

// Settings modal (10-10-settings-pack, PRD R2/R3/R4/R5). Mirrors the web
// dialog: new-provider defaults (numeric/bool subset only), streak-alert
// threshold, report dir. Saving goes straight to the store — the TUI owns
// the same process, no HTTP detour.

// openSettingsModal mounts the settings form over the UI.
func (u *ui) openSettingsModal() {
	st := u.deps.Store.GetSettings()

	form := tview.NewForm().SetButtonsAlign(tview.AlignCenter)
	form.AddInputField("max_tokens", strconv.Itoa(st.Defaults.MaxTokens), 10, nil, nil)
	form.AddInputField("探测间隔秒（0=仅手动，≥60）", intervalStr(st.Defaults.IntervalSec), 10, nil, nil)
	form.AddInputField("总超时秒", strconv.Itoa(st.Defaults.TimeoutSec), 10, nil, nil)
	form.AddInputField("首内容超时 ms", strconv.Itoa(st.Defaults.TTFTTimeoutMs), 10, nil, nil)
	form.AddInputField("慢阈值 ms", strconv.Itoa(st.Defaults.TTFTSlowMs), 10, nil, nil)
	form.AddCheckbox("默认启用定时探测", st.Defaults.Enabled, nil)
	form.AddCheckbox("默认请求 usage 统计", st.Defaults.IncludeUsage, nil)
	form.AddInputField("连败提醒阈值（≥2）", strconv.Itoa(st.StreakAlert), 10, nil, nil)
	form.AddInputField("报告导出目录（空=默认）", st.ReportDir, 44, nil, nil)

	msg := tview.NewTextView().SetDynamicColors(true)
	get := func(label string) string {
		if item := form.GetFormItemByLabel(label); item != nil {
			if in, ok := item.(*tview.InputField); ok {
				return strings.TrimSpace(in.GetText())
			}
		}
		return ""
	}
	checked := func(label string) bool {
		if cb, ok := form.GetFormItemByLabel(label).(*tview.Checkbox); ok {
			return cb.IsChecked()
		}
		return false
	}

	submit := func() {
		next := store.Settings{
			Defaults: store.SettingsDefaults{
				MaxTokens:     atoiDefault(get("max_tokens"), 0),
				IntervalSec:   atoiDefault(get("探测间隔秒（0=仅手动，≥60）"), 0),
				TimeoutSec:    atoiDefault(get("总超时秒"), 0),
				TTFTTimeoutMs: atoiDefault(get("首内容超时 ms"), 0),
				TTFTSlowMs:    atoiDefault(get("慢阈值 ms"), 0),
				Enabled:       checked("默认启用定时探测"),
				IncludeUsage:  checked("默认请求 usage 统计"),
			},
			StreakAlert: atoiDefault(get("连败提醒阈值（≥2）"), 0),
			ReportDir:   get("报告导出目录（空=默认）"),
		}
		if msg2 := validateSettings(next); msg2 != "" {
			msg.SetText("[red]" + tview.Escape(msg2) + "[-]")
			return
		}
		if err := u.deps.Store.SaveSettings(next); err != nil {
			msg.SetText("[red]保存设置失败：" + tview.Escape(err.Error()) + "[-]")
			return
		}
		u.dismissModal()
		u.redrawLocked() // streak badges re-render at the new threshold
	}

	form.AddButton("保存", submit)
	form.AddButton("取消", func() { u.dismissModal() })
	form.SetCancelFunc(func() { u.dismissModal() })
	form.SetInputCapture(arrowNavCapture(form))

	inner := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tview.NewTextView().SetDynamicColors(true).SetText("[::b]全局设置[-]"), 1, 0, false).
		AddItem(form, 0, 1, true).
		AddItem(msg, 1, 0, false)
	dlg := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).
		AddItem(centerRows(inner), 70, 1, true).
		AddItem(nil, 0, 1, false)
	dlg.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEsc {
			u.dismissModal()
			return nil
		}
		return ev
	})
	formDismissHook = u.dismissModal
	u.pages.AddAndSwitchToPage("modal", dlg, true)
}

// intervalStr renders 0 explicitly (manual-only is a valid default).
func intervalStr(sec int) string { return strconv.Itoa(sec) }

// atoiDefault parses or returns fallback on error/empty.
func atoiDefault(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fallback
	}
	return n
}

// validateSettings mirrors the server-side rules (one wording set).
func validateSettings(s store.Settings) string {
	d := s.Defaults
	switch {
	case s.StreakAlert < 2:
		return "连败提醒阈值需至少为 2"
	case d.MaxTokens <= 0:
		return "max_tokens 必须大于 0"
	case d.TimeoutSec <= 0:
		return "总超时必须大于 0"
	case d.IntervalSec != 0 && d.IntervalSec < 60:
		return "探测间隔为 0（仅手动）或至少 1 分钟"
	case !(d.TTFTSlowMs > 0 && d.TTFTSlowMs < d.TTFTTimeoutMs):
		return "需要 0 < 慢阈值 < 首内容超时"
	case d.TTFTTimeoutMs > d.TimeoutSec*1000:
		return "首内容超时不能大于总超时"
	case s.ReportDir != "" && !dirWritable(s.ReportDir):
		return "报告目录不可写或无法创建"
	}
	return ""
}

// arrowNavCapture reuses the provider-form Up/Down navigation on any form.
func arrowNavCapture(form *tview.Form) func(*tcell.EventKey) *tcell.EventKey {
	return func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() != tcell.KeyUp && ev.Key() != tcell.KeyDown {
			return ev
		}
		item, button := form.GetFocusedItemIndex()
		current := -1
		if item >= 0 {
			current = item
		} else if button >= 0 {
			current = form.GetFormItemCount() + button
		}
		total := form.GetFormItemCount() + form.GetButtonCount()
		if current < 0 || total == 0 {
			return ev
		}
		if ev.Key() == tcell.KeyDown {
			current = (current + 1) % total
		} else {
			current = (current - 1 + total) % total
		}
		form.SetFocus(current)
		return nil
	}
}

// dirWritable reports whether dir exists or can be created (same rule as
// the server-side validation).
func dirWritable(dir string) bool {
	if err := osMkdirAll(dir, 0o755); err != nil {
		return false
	}
	info, err := osStat(dir)
	return err == nil && info.IsDir()
}

var (
	osMkdirAll = os.MkdirAll
	osStat     = os.Stat
)
