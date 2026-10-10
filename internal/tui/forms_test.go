package tui

import (
	"testing"

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

// While a modal is open, the app-level capture must pass every key through
// so typing p/r/w/q inside a form field inserts the character instead of
// triggering probe/export/web/quit (regression: p was swallowed by
// globalKeys before the fix).
func TestGlobalKeysPassThroughWhenModalOpen(t *testing.T) {
	u := &ui{pages: tview.NewPages()}
	u.pages.AddPage("main", tview.NewBox(), true, true)
	u.pages.AddAndSwitchToPage("modal", tview.NewBox(), true)

	if !u.modalOpen() {
		t.Fatal("modal must be reported open")
	}
	for _, r := range []rune{'p', 'r', 'w', 'q', 'Q', 'x'} {
		ev := u.globalKeys(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if ev == nil {
			t.Fatalf("key %q must pass through while a modal is open", r)
		}
	}

	// And with no modal, the action keys are consumed again (p/r hit the
	// "no provider selected" guard; w starts the panel lifecycle and fails
	// on the invalid port — all without touching a nil dependency).
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	u.app = tview.NewApplication()
	u.deps = Deps{Store: st}
	u.overview = newOverviewPane(st, nil, nil)
	u.statusBar = tview.NewTextView().SetDynamicColors(true)
	u.web = newWebPanel(Deps{})
	u.pages.RemovePage("modal")
	u.pages.SwitchToPage("main")
	for _, r := range []rune{'p', 'r', 'w'} {
		ev := u.globalKeys(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
		if ev != nil {
			t.Fatalf("key %q must be consumed with no modal open (got %v)", r, ev)
		}
	}
}
