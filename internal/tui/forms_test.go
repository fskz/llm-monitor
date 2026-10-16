package tui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
)

// Up/Down must move between form elements (fields AND buttons), wrapping at
// both ends; Left/Right and other keys pass through untouched.
func TestFormArrowNavigation(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	d := openForm(nil, Deps{Store: st}, 0, nil)

	total := d.form.GetFormItemCount() + d.form.GetButtonCount()
	if total < 3 {
		t.Fatalf("form has %d elements, want fields + 2 buttons", total)
	}

	// Start at the first field (tview focuses element 0 on a fresh form).
	d.form.SetFocus(0)

	// Down ×total wraps all the way around back to 0.
	for i := 1; i <= total; i++ {
		ev := d.arrowNavigation(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
		if ev != nil {
			t.Fatalf("Down at step %d must be consumed, got %v", i, ev)
		}
		item, button := d.form.GetFocusedItemIndex()
		got := item
		if button >= 0 {
			got = d.form.GetFormItemCount() + button
		}
		want := i % total
		if got != want {
			t.Fatalf("after Down %d: focused = %d, want %d", i, got, want)
		}
	}

	// Up from 0 wraps to the last element (取消 button).
	d.form.SetFocus(0)
	if ev := d.arrowNavigation(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)); ev != nil {
		t.Fatal("Up must be consumed")
	}
	item, button := d.form.GetFocusedItemIndex()
	got := item
	if button >= 0 {
		got = d.form.GetFormItemCount() + button
	}
	if got != total-1 {
		t.Fatalf("Up from first = %d, want %d (last)", got, total-1)
	}

	// Pass-through keys are untouched.
	for _, key := range []tcell.Key{tcell.KeyLeft, tcell.KeyRight, tcell.KeyEnter, tcell.KeyRune} {
		ev := d.arrowNavigation(tcell.NewEventKey(key, 'x', tcell.ModNone))
		if ev == nil {
			t.Fatalf("key %v must pass through", key)
		}
	}
}

// Review fixpack S3: formArrowNav is the single Up/Down navigation owner for
// every form (provider form + settings form). The converged no-focus
// behavior: with no element holding focus yet, Up/Down treat element 0 as
// current instead of passing through (previously the settings form returned
// the key untouched in that state).
func TestFormArrowNavSharedHelper(t *testing.T) {
	form := tview.NewForm()
	form.AddInputField("a", "", 10, nil, nil)
	form.AddInputField("b", "", 10, nil, nil)
	form.AddButton("保存", nil)
	form.AddButton("取消", nil)
	nav := formArrowNav(form)

	total := form.GetFormItemCount() + form.GetButtonCount()
	if total != 4 {
		t.Fatalf("total elements = %d, want 4", total)
	}

	// No element focused yet (fresh form, never drawn): Down focuses element
	// 1, Up wraps from element 0 to the last one — not a pass-through.
	if ev := nav(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)); ev != nil {
		t.Fatal("Down with no focus must be consumed")
	}
	if item, _ := form.GetFocusedItemIndex(); item != 1 {
		t.Fatalf("focused item after Down = %d, want 1", item)
	}

	form.SetFocus(0)
	if ev := nav(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)); ev != nil {
		t.Fatal("Up must be consumed")
	}
	if _, button := form.GetFocusedItemIndex(); button != form.GetButtonCount()-1 {
		t.Fatalf("Up from element 0 must wrap to the last button, got button %d", button)
	}
}

// The dialog must not be a nil primitive and the cancel path wires up.
func TestFormDialogShape(t *testing.T) {
	st, _ := store.New(t.TempDir())
	d := openForm(nil, Deps{Store: st}, 0, nil)
	if d.primitive() == nil {
		t.Fatal("form dialog primitive must not be nil")
	}
	if d.form == nil {
		t.Fatal("inner form must not be nil")
	}
	_ = tview.NewApplication() // keep the tview import honest in this file
}

// Review fixpack #2: the delete confirmation must offer a working cancel
// path. tview's Modal contract fires done(0, "删除") for the delete button,
// done(1, "取消") for the cancel button and done(-1, "") for Esc — the two
// cancel paths must dismiss the modal without touching the store.
func TestConfirmDeleteCancelPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		idx  int
	}{
		{"cancel button", 1},
		{"escape key", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, _ := store.New(t.TempDir())
			ids := seedProviders(t, st, 1)

			cancelled := false
			done := deleteDoneFunc(Deps{Store: st}, ids[0],
				func() { t.Error("onDone must not fire on cancel") },
				func() { cancelled = true },
				func(msg string) { t.Errorf("onFail must not fire on cancel, got %q", msg) })

			done(tc.idx, "")

			if !cancelled {
				t.Fatal("cancel callback must fire")
			}
			if _, ok := st.GetProvider(ids[0]); !ok {
				t.Fatal("cancel must not delete the provider")
			}
			if got := len(st.GetProviders()); got != 1 {
				t.Fatalf("store has %d providers after cancel, want 1", got)
			}
		})
	}
}

// While a modal is open, the app-level capture must pass every key through
// so typing p/r/w/q inside a form field inserts the character instead of
// triggering actions (regression: p was swallowed by globalKeys before the
// fix).
//
// Task 10-16 step 2: the rune actions are retired (A5), so globalKeys has
// exactly two remaining behaviors to pin here — the modalOpen passthrough
// guard and the Tab rotation, which yields while the command bar holds
// focus (completion wins, red line §6-5). Every former action rune must
// now pass through at ANY non-command-bar focus: the command bar is the
// single action entry.
func TestGlobalKeysPassThroughWhenModalOpen(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	u := newUI(tview.NewApplication(), Deps{Store: st})

	// Modal up: every key passes through, Tab included (the rotation must
	// not fight the form's own navigation).
	u.pages.AddAndSwitchToPage("modal", tview.NewBox(), true)
	if !u.modalOpen() {
		t.Fatal("modal must be reported open")
	}
	for _, r := range []rune{'p', 'r', 'w', 'q', 'Q', 'x', ',', 'n', 'e', 'd', '1', '2', '3', 'R', 'S'} {
		ev := u.globalKeys(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if ev == nil {
			t.Fatalf("key %q must pass through while a modal is open", r)
		}
	}
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev == nil {
		t.Fatal("Tab must pass through while a modal is open")
	}
	u.dismissModal() // restores the command-bar focus

	// Command bar focused: everything passes through — Tab is decided by the
	// bar's own capture (menu completion).
	for _, r := range []rune{'p', 'r', 'w', 'q'} {
		ev := u.globalKeys(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if ev == nil {
			t.Fatalf("key %q must pass through while the command bar is focused", r)
		}
	}
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev == nil {
		t.Fatal("app-level Tab must yield while the command bar holds focus")
	}

	// List focused (A5, handler half): all retired action runes pass through
	// with no side effects; only Tab is consumed and still rotates.
	u.app.SetFocus(u.overview.list)
	for _, r := range []rune{'p', 'r', 'w', 'q', 'Q', 'x', ',', 'n', 'e', 'd', '1', '2', '3', 'R', 'S'} {
		ev := u.globalKeys(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if ev == nil {
			t.Fatalf("rune %q must pass through — the command bar is the only action entry (A5)", r)
		}
	}
	if u.modalOpen() {
		t.Fatal("no rune may open a modal")
	}
	if u.detail.filters.window != 24*time.Hour || u.detail.filters.revision != "" ||
		u.detail.filters.source != store.SourceScheduled {
		t.Fatalf("no rune may change the detail filters, got window=%v revision=%q source=%q",
			u.detail.filters.window, u.detail.filters.revision, u.detail.filters.source)
	}
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev != nil {
		t.Fatal("Tab with focus on the list must be consumed by the rotation")
	}
	if got := u.app.GetFocus(); got != u.detailView {
		t.Fatalf("focus after Tab = %T, want the detail view", got)
	}

	// Detail focused (former detail-capture scope): runes pass through there
	// too — the capture itself is retired (its behavioral half runs on the
	// simulation screen, TestRetiredRuneKeysDoNothing).
	for _, r := range []rune{'1', '2', '3', 'R', 'S'} {
		ev := u.globalKeys(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if ev == nil {
			t.Fatalf("rune %q must pass through with focus on the detail view (A5)", r)
		}
	}
	if u.detail.filters.window != 24*time.Hour {
		t.Fatalf("detail-focused runes changed the window to %v", u.detail.filters.window)
	}
}

// review-fixpack-2 F12: when the STORE delete fails (e.g. the object was
// already deleted from the web panel), the modal must still close (onFail,
// never stuck) and onDone must NOT fire; a successful delete still runs the
// normal done path.
func TestConfirmDeleteStoreFailureClosesModal(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)

	var failMsg string
	done := deleteDoneFunc(Deps{Store: st}, ids[0],
		func() { t.Error("onDone must not fire when the store delete failed") },
		func() { t.Error("onCancel must not fire for the delete button") },
		func(msg string) { failMsg = msg })

	// Delete the provider behind the modal's back first: DeleteProvider now
	// fails with "unknown provider".
	if _, err := st.GetProvider(ids[0]); !err {
		t.Fatal("seed provider missing")
	}
	if derr := st.DeleteProvider(ids[0]); derr != nil {
		t.Fatalf("pre-delete: %v", derr)
	}
	done(0, "")

	if failMsg == "" {
		t.Fatal("onFail must fire with the store error")
	}
}

// review-fixpack-2 F12: /edit on a provider that vanished (deleted from the
// web panel) must mark the dialog gone instead of silently mounting a
// "new" form.
func TestOpenEditFormTargetGone(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	if err := st.DeleteProvider(ids[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	d := openForm(nil, Deps{Store: st}, ids[0], nil)
	if !d.gone {
		t.Fatal("edit dialog for a vanished provider must be marked gone")
	}
}
