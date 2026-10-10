package tui

import (
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/view"
)

// Provider forms (design.md §4 forms.go). One form type serves add and edit;
// the shared view.ProviderForm carries the fields and validation, the TUI
// adds the two things the HTTP layer keeps separate: the API key entry
// (absent = keep / typed = replace, "" = clear on edit; add starts empty)
// and the revision-bump rule on base_url/model changes (§4.1).

// formFields returns the ordered (label, initial value) pairs rendered into
// the input area. Numeric fields are edited as strings and parsed by
// collectForm — the panel does the same (web/app.js formToJSON).
func formFields(f *view.ProviderForm, apiKey string) []struct {
	label string
	value string
} {
	return []struct {
		label string
		value string
	}{
		{"名称", f.Name},
		{"Base URL", f.BaseURL},
		{"模型", f.Model},
		{"API Key（编辑时空着=保持不变）", apiKey},
		{"提示词 Prompt", f.Prompt},
		{"max_tokens", strconv.Itoa(f.MaxTokens)},
		{"探测间隔秒（0=仅手动，≥60）", strconv.Itoa(f.IntervalSec)},
		{"总超时秒", strconv.Itoa(f.TimeoutSec)},
		{"首内容超时 ms", strconv.Itoa(f.TTFTTimeoutMs)},
		{"慢阈值 ms", strconv.Itoa(f.TTFTSlowMs)},
	}
}

// formDialog is a modal add/edit form. submit applies the shared validation
// and persistence rules; Esc cancels without touching the store.
type formDialog struct {
	app     *tview.Application
	deps    Deps
	editID  int // 0 = add
	form    *tview.Form
	dialog  *tview.Flex
	message string // last validation error, rendered in red
	msgView *tview.TextView
	onDone  func() // redraw after a successful mutation
}

// openForm mounts the modal form over the UI. For add, editID is 0 and the
// fields start at the defaults; for edit they prefill from the provider.
func openForm(app *tview.Application, deps Deps, editID int, onDone func()) *formDialog {
	d := &formDialog{app: app, deps: deps, editID: editID, onDone: onDone}

	f := view.ProviderForm{
		Prompt:        view.DefaultPrompt,
		MaxTokens:     128,
		TimeoutSec:    60,
		IntervalSec:   300,
		TTFTTimeoutMs: 15000,
		TTFTSlowMs:    3000,
		Enabled:       true,
	}
	apiKey := ""
	title := "新建监测对象"
	if editID > 0 {
		if p, ok := deps.Store.GetProvider(editID); ok {
			f = view.ProviderForm{
				Name: p.Name, BaseURL: p.BaseURL, Model: p.Model, Prompt: p.Prompt,
				MaxTokens: p.MaxTokens, IntervalSec: p.IntervalSec, TimeoutSec: p.TimeoutSec,
				TTFTTimeoutMs: p.TTFTTimeoutMs, TTFTSlowMs: p.TTFTSlowMs,
				Enabled: p.Enabled, IncludeUsage: p.IncludeUsage,
			}
			title = "编辑监测对象（" + p.Name + "）"
		}
	}

	d.form = tview.NewForm().SetButtonsAlign(tview.AlignCenter)
	for _, fl := range formFields(&f, apiKey) {
		d.form.AddInputField(fl.label, fl.value, 44, nil, nil)
	}
	d.form.AddCheckbox("启用定时探测", f.Enabled, nil)
	d.form.AddCheckbox("请求 usage 统计", f.IncludeUsage, nil)
	d.form.AddButton("保存", d.submit)
	d.form.AddButton("取消", d.close)
	d.form.SetCancelFunc(d.close)
	d.form.SetButtonsAlign(tview.AlignCenter)

	d.msgView = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)

	inner := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tview.NewTextView().SetText("[::b]"+title+"[-]").SetDynamicColors(true), 1, 0, false).
		AddItem(d.form, 0, 1, true).
		AddItem(d.msgView, 1, 0, false)
	d.dialog = tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(nil, 0, 1, false).
		AddItem(centerRows(inner), 70, 1, true).
		AddItem(nil, 0, 1, false)
	d.dialog.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEsc {
			d.close()
			return nil
		}
		return ev
	})
	// tview Form only moves between fields on Tab/Backtab; Up/Down are
	// swallowed by InputField (no suggestions = no-op there). Wire them to
	// field navigation — the near-universal TUI form convention — while
	// Left/Right stay with the text cursor. Esc cancels as before.
	d.form.SetInputCapture(d.arrowNavigation)
	return d
}

// arrowNavigation maps Up = previous element, Down = next element. The wrap
// matches Tab/Backtab semantics (tview wraps at both ends). focusIndex()
// reports -1 until an element truly holds focus, so fall back to the form's
// focusedElement bookkeeping (SetFocus target).
func (d *formDialog) arrowNavigation(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() != tcell.KeyUp && ev.Key() != tcell.KeyDown {
		return ev
	}
	item, button := d.form.GetFocusedItemIndex()
	current := -1
	if item >= 0 {
		current = item
	} else if button >= 0 {
		current = d.form.GetFormItemCount() + button
	}
	total := d.form.GetFormItemCount() + d.form.GetButtonCount()
	if current < 0 {
		// No element has real focus yet (form not drawn); the tview Form
		// still tracks the intended element in focusedElement. That field
		// is private, so approximate: the element tview will focus on the
		// next Tab — treat the intended one as current by probing
		// SetFocus(0)'s bookkeeping via its observable effect below.
		current = 0
	}
	if total == 0 {
		return ev
	}
	if ev.Key() == tcell.KeyDown {
		current = (current + 1) % total
	} else {
		current = (current - 1 + total) % total
	}
	d.form.SetFocus(current)
	return nil
}

// centerRows centers the inner content vertically.
func centerRows(inner tview.Primitive) tview.Primitive {
	rows := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(inner, 0, 8, true).
		AddItem(nil, 0, 1, false)
	return rows
}

// primitive returns the modal root to SetPages/SetRoot with.
func (d *formDialog) primitive() tview.Primitive { return d.dialog }

// collectForm reads the input fields back into the shared form + key.
func (d *formDialog) collectForm() (view.ProviderForm, string) {
	var f view.ProviderForm
	get := func(label string) string {
		item := d.form.GetFormItemByLabel(label)
		if item == nil {
			return ""
		}
		if in, ok := item.(*tview.InputField); ok {
			return in.GetText()
		}
		return ""
	}
	f.Name = get("名称")
	f.BaseURL = get("Base URL")
	f.Model = get("模型")
	f.Prompt = get("提示词 Prompt")
	apiKey := get("API Key（编辑时空着=保持不变）")
	f.MaxTokens, _ = strconv.Atoi(strings.TrimSpace(get("max_tokens")))
	f.TimeoutSec, _ = strconv.Atoi(strings.TrimSpace(get("总超时秒")))
	f.IntervalSec, _ = strconv.Atoi(strings.TrimSpace(get("探测间隔秒（0=仅手动，≥60）")))
	f.TTFTTimeoutMs, _ = strconv.Atoi(strings.TrimSpace(get("首内容超时 ms")))
	f.TTFTSlowMs, _ = strconv.Atoi(strings.TrimSpace(get("慢阈值 ms")))
	if cb, ok := d.form.GetFormItemByLabel("启用定时探测").(*tview.Checkbox); ok {
		f.Enabled = cb.IsChecked()
	}
	if cb, ok := d.form.GetFormItemByLabel("请求 usage 统计").(*tview.Checkbox); ok {
		f.IncludeUsage = cb.IsChecked()
	}
	f.Prepare()
	return f, apiKey
}

// submit validates and persists; failures show inline instead of closing.
func (d *formDialog) submit() {
	f, apiKey := d.collectForm()
	if msg := f.Validate(); msg != "" {
		d.showError(msg)
		return
	}

	if d.editID == 0 {
		created, err := d.deps.Store.AddProvider(f.ToProvider(strings.TrimSpace(apiKey)))
		if err != nil {
			d.showError("保存配置失败：" + err.Error())
			return
		}
		if d.deps.Mutator != nil {
			d.deps.Mutator.Add(created)
		}
	} else {
		old, ok := d.deps.Store.GetProvider(d.editID)
		if !ok {
			d.showError("监测对象不存在")
			return
		}
		// PUT key semantics (§5.4): empty entry = keep the stored key, typed
		// value = replace. Clearing is not offered as an empty field here —
		// the field placeholder says 空着=保持, matching the panel's edit
		// placeholder semantics for a simpler modal.
		key := old.APIKey
		if strings.TrimSpace(apiKey) != "" {
			key = strings.TrimSpace(apiKey)
		}
		updated := f.ToProvider(key)
		updated.ID = old.ID
		updated.CreatedAt = old.CreatedAt
		updated.Revision = old.Revision
		if updated.BaseURL != old.BaseURL || updated.Model != old.Model {
			updated.Revision = old.Revision + 1
		}
		if err := d.deps.Store.UpdateProvider(updated); err != nil {
			d.showError("保存配置失败：" + err.Error())
			return
		}
		if d.deps.Mutator != nil {
			d.deps.Mutator.Update(updated)
		}
	}
	d.close()
	if d.onDone != nil {
		d.onDone()
	}
}

func (d *formDialog) showError(msg string) {
	d.message = msg
	d.msgView.SetText("[red]" + tview.Escape(msg) + "[-]")
}

// close dismisses the modal. The actual page switch lives in ui.dismissModal.
var formDismissHook func()

func (d *formDialog) close() {
	if formDismissHook != nil {
		formDismissHook()
	}
}

// confirmDelete asks before deleting (design.md §7: d = 编辑/n 新建/d 删除,
// delete gets a confirmation modal).
func confirmDelete(app *tview.Application, deps Deps, id int, onDone func()) *tview.Modal {
	p, ok := deps.Store.GetProvider(id)
	name := strconv.Itoa(id)
	if ok {
		name = p.Name
	}
	modal := tview.NewModal().
		SetText("删除监测对象「" + tview.Escape(name) + "」？历史记录将一并删除。").
		AddButtons([]string{"删除", "取消"})
	modal.SetDoneFunc(func(idx int, label string) {
		if idx == 0 { // 删除
			if deps.Mutator != nil {
				deps.Mutator.Remove(id) // cancels the in-flight probe first
			}
			if err := deps.Store.DeleteProvider(id); err == nil && onDone != nil {
				onDone()
			}
		}
	})
	return modal
}
