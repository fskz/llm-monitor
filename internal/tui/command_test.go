package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// Command bar tests (task 10-16 design.md §8): the filter state machine,
// menu rendering, key handling and every execute branch run on a dormant
// application built by newUI — SetText triggers the changed callback
// synchronously, so no event loop is needed for the logic half. The
// screen-driven half (startup focus, live typing, Esc rotation) uses the
// tcell simulation screen at the bottom of this file.

// newTestUI builds a fully wired ui on a dormant app. providers seeds the
// store before the first redraw (0 = nothing selected, needSel guards fire).
func newTestUI(t *testing.T, providers int) *ui {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if providers > 0 {
		seedProviders(t, st, providers)
	}
	return newUI(tview.NewApplication(), Deps{Store: st, PortStart: 10110})
}

// bareBar builds a command bar on a minimal ui — enough for the pure
// filter/menu/key tests that never need panes or a store (modalOpen reads
// the pages, so a bare ui{} would panic).
func bareBar() *commandBar {
	u := &ui{pages: tview.NewPages().AddPage("main", tview.NewBox(), true, true)}
	return newCommandBar(u)
}

// typeIn sets the input text the way typing would (changed callback fires).
func typeIn(cb *commandBar, text string) { cb.input.SetText(text) }

// labels lists the current candidate labels.
func labels(cb *commandBar) []string {
	out := make([]string, 0, len(cb.menuItems))
	for _, e := range cb.menuItems {
		out = append(out, e.label())
	}
	return out
}

// press runs one key through the field's capture.
func press(cb *commandBar, key tcell.Key, ch rune) *tcell.EventKey {
	return cb.handleKey(tcell.NewEventKey(key, ch, tcell.ModNone))
}

func statusOf(u *ui) string { return u.statusBar.GetText(true) }

// --- registry shape (prd R1) ---

func TestCommandRegistryShape(t *testing.T) {
	if len(commands) != 16 {
		t.Fatalf("registry has %d commands, want 16", len(commands))
	}
	seen := map[string]bool{}
	wantArgs := map[string][]string{
		"/window":   {"1h", "24h", "7d"},
		"/revision": {"current", "all"},
		"/source":   {"scheduled", "manual", "all"},
		"/page":     {"next", "prev"},
	}
	wantSel := map[string]bool{
		"/probe": true, "/cache": true, "/toggle": true, "/edit": true,
		"/delete": true, "/window": true, "/revision": true, "/source": true,
		"/page": true,
	}
	for _, c := range commands {
		if !strings.HasPrefix(c.name, "/") {
			t.Errorf("%q: name must carry the / prefix", c.name)
		}
		if seen[c.name] {
			t.Errorf("duplicate command %q", c.name)
		}
		seen[c.name] = true
		if c.run == nil || c.desc == "" {
			t.Errorf("%s: run handler and description are mandatory", c.name)
		}
		if want, ok := wantArgs[c.name]; ok {
			if len(c.args) != len(want) {
				t.Fatalf("%s args = %v, want %v", c.name, c.args, want)
			}
			for i := range want {
				if c.args[i] != want[i] {
					t.Fatalf("%s args = %v, want %v", c.name, c.args, want)
				}
			}
		} else if len(c.args) != 0 {
			t.Errorf("%s must be a zero-arg command, got %v", c.name, c.args)
		}
		if wantSel[c.name] != c.needSel {
			t.Errorf("%s needSel = %v, want %v", c.name, c.needSel, wantSel[c.name])
		}
	}
	for _, name := range []string{"/exit", "/probe", "/cache", "/report", "/web", "/settings",
		"/toggle", "/new", "/edit", "/delete", "/window", "/revision", "/source",
		"/page", "/refresh", "/help"} {
		if !seen[name] {
			t.Errorf("registry is missing %s", name)
		}
	}
	// Enter's ambiguous-command guard leans on no name prefixing another.
	for i := range commands {
		for j := range commands {
			if i != j && strings.HasPrefix(commands[j].name, commands[i].name) {
				t.Errorf("%s is a prefix of %s — breaks the Enter guard", commands[i].name, commands[j].name)
			}
		}
	}
}

// --- filter: the three states (design.md §3) ---

func TestFilterCommandPhase(t *testing.T) {
	cb := bareBar()
	for _, tc := range []struct {
		in   string
		want []string
		open bool
	}{
		{in: ""},
		{in: "hello"},
		{in: "w/"},
		{in: "/", want: []string{"/exit", "/probe", "/cache", "/report", "/web", "/settings", "/toggle",
			"/new", "/edit", "/delete", "/window", "/revision", "/source", "/page", "/refresh", "/help"}, open: true},
		{in: "/c", want: []string{"/cache"}, open: true},
		{in: "/w", want: []string{"/web", "/window"}, open: true},
		{in: "/window", want: []string{"/window"}, open: true},
		{in: "/zzz"},
	} {
		typeIn(cb, tc.in)
		if cb.menuOpen() != tc.open {
			t.Errorf("filter(%q) open = %v, want %v", tc.in, cb.menuOpen(), tc.open)
		}
		got := labels(cb)
		if len(got) != len(tc.want) {
			t.Fatalf("filter(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("filter(%q) = %v, want %v", tc.in, got, tc.want)
			}
		}
		if tc.open && cb.menuSel != 0 {
			t.Errorf("filter(%q) menuSel = %d, want reset to 0", tc.in, cb.menuSel)
		}
	}
}

func TestFilterArgumentPhase(t *testing.T) {
	cb := bareBar()
	for _, tc := range []struct {
		in   string
		want []string
		open bool
	}{
		{in: "/window ", want: []string{"1h", "24h", "7d"}, open: true},
		{in: "/window 2", want: []string{"24h"}, open: true},
		{in: "/window 9h"}, // no matching arg: menu closed, Enter will flash
		{in: "/revision a", want: []string{"all"}, open: true},  //
		{in: "/source m", want: []string{"manual"}, open: true}, //
		{in: "/exit x"}, // zero-arg command: never an arg phase
		{in: "/nope a"}, // unknown command
		{in: "/window  ", want: []string{"1h", "24h", "7d"}, open: true}, // extra spaces trim
	} {
		typeIn(cb, tc.in)
		if cb.menuOpen() != tc.open {
			t.Errorf("filter(%q) open = %v, want %v", tc.in, cb.menuOpen(), tc.open)
		}
		got := strings.Join(labels(cb), ",")
		want := strings.Join(tc.want, ",")
		if got != want {
			t.Errorf("filter(%q) = [%s], want [%s]", tc.in, got, want)
		}
	}
}

// --- menu rendering ---

func TestMenuText(t *testing.T) {
	cb := bareBar()
	if got := cb.menuText(); got != "" {
		t.Fatalf("closed menu text = %q, want empty", got)
	}
	typeIn(cb, "/w")
	got := cb.menuText()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("menu for /w has %d lines, want 2:\n%s", len(lines), got)
	}
	if !strings.HasPrefix(lines[0], "▸ [yellow::b]/web") {
		t.Errorf("highlighted line = %q, want ▸ marker and style on /web", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  [white]/window") {
		t.Errorf("unselected line = %q, want two-space marker on /window", lines[1])
	}
	// The description column follows the padded name.
	if !strings.Contains(lines[0], "/web") || !strings.Contains(got, "拉起") {
		t.Errorf("menu must carry the command descriptions, got:\n%s", got)
	}
	// Argument phase renders the argument values, not the command names.
	typeIn(cb, "/source ")
	for _, line := range strings.Split(strings.TrimRight(cb.menuText(), "\n"), "\n") {
		if !strings.Contains(line, "scheduled") && !strings.Contains(line, "manual") && !strings.Contains(line, "all") {
			t.Errorf("arg menu line %q must show an argument value", line)
		}
	}
}

// --- handleKey ---

func TestHandleKeyMenuNavigationCycles(t *testing.T) {
	cb := bareBar()
	typeIn(cb, "/w") // 2 candidates, sel 0
	if ev := press(cb, tcell.KeyDown, 0); ev != nil {
		t.Fatal("Down with the menu open must be consumed")
	}
	if cb.menuSel != 1 {
		t.Fatalf("menuSel = %d after Down, want 1", cb.menuSel)
	}
	if ev := press(cb, tcell.KeyDown, 0); ev != nil {
		t.Fatal("Down must be consumed")
	}
	if cb.menuSel != 0 {
		t.Fatalf("menuSel = %d after wrapping Down, want 0", cb.menuSel)
	}
	if ev := press(cb, tcell.KeyUp, 0); ev != nil {
		t.Fatal("Up must be consumed")
	}
	if cb.menuSel != 1 {
		t.Fatalf("menuSel = %d after wrapping Up, want 1", cb.menuSel)
	}
	if got := cb.input.GetText(); got != "/w" {
		t.Fatalf("input = %q, navigation must not edit it", got)
	}
	// Closed menu: arrows pass through untouched.
	typeIn(cb, "")
	if ev := press(cb, tcell.KeyDown, 0); ev == nil {
		t.Fatal("Down with the menu closed must pass through")
	}
}

func TestHandleKeyTabCompletion(t *testing.T) {
	cb := bareBar()
	typeIn(cb, "/")
	if ev := press(cb, tcell.KeyTab, 0); ev != nil {
		t.Fatal("Tab with the menu open must be consumed (completion)")
	}
	if got := cb.input.GetText(); got != "/exit" {
		t.Fatalf("Tab completed to %q, want /exit (first candidate)", got)
	}
	// Argument phase completes "name arg".
	typeIn(cb, "/source m")
	if ev := press(cb, tcell.KeyTab, 0); ev != nil {
		t.Fatal("Tab in the arg phase must be consumed")
	}
	if got := cb.input.GetText(); got != "/source manual" {
		t.Fatalf("Tab completed to %q, want /source manual", got)
	}
}

// 2026-10-19: rotation moved from Tab to Esc. Tab with the menu closed must
// still be consumed (InputField would otherwise treat it as field-finished
// and jump focus on its own), but it must do NOTHING.
func TestHandleKeyTabSwallowedWhenMenuClosed(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	typeIn(cb, "") // menu closed
	if ev := press(cb, tcell.KeyTab, 0); ev != nil {
		t.Fatal("Tab with the menu closed must be consumed (no tview default)")
	}
	if got := u.app.GetFocus(); got != u.cmd.input {
		t.Fatalf("focus after Tab = %T, want the command bar (Tab no longer rotates)", got)
	}
}

func TestHandleKeyEscapeClears(t *testing.T) {
	cb := bareBar()
	typeIn(cb, "/window")
	if ev := press(cb, tcell.KeyEscape, 0); ev != nil {
		t.Fatal("Esc must be consumed")
	}
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("input after Esc = %q, want cleared", got)
	}
	if cb.menuOpen() {
		t.Fatal("menu must be closed after Esc")
	}
}

// 2026-10-19: Esc on an empty input enters pane selection (版块选择) — the
// rotation that used to live on Tab.
func TestHandleKeyEscapeRotatesWhenEmpty(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("precondition: input = %q, want empty", got)
	}
	if ev := press(cb, tcell.KeyEscape, 0); ev != nil {
		t.Fatal("Esc must be consumed")
	}
	if got := u.app.GetFocus(); got != u.overview.list {
		t.Fatalf("focus after Esc = %T, want the provider list", got)
	}
	// One Esc only rotates one station — a stray press never bounces the
	// focus all the way home.
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); ev != nil {
		t.Fatal("app-level Esc must be consumed")
	}
	if got := u.app.GetFocus(); got != u.detailView {
		t.Fatalf("focus after the second Esc = %T, want the detail view", got)
	}
}

func TestHandleKeyModalGuard(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	u.openFormModal(0) // a modal owns the keyboard
	defer u.dismissModal()
	for _, key := range []tcell.Key{tcell.KeyTab, tcell.KeyEnter, tcell.KeyEscape, tcell.KeyUp} {
		if ev := press(cb, key, 0); ev == nil {
			t.Errorf("key %v must pass through while a modal is open", key)
		}
	}
}

// --- execute: every branch (design.md §3) ---

func TestExecuteSwallowsPlainText(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	before := statusOf(u) // the guide line renderStatus left
	typeIn(cb, "hello world")
	press(cb, tcell.KeyEnter, 0)
	if got := cb.input.GetText(); got != "hello world" {
		t.Fatalf("input = %q, plain text must be kept", got)
	}
	if got := statusOf(u); got != before {
		t.Fatalf("plain text must not flash, got %q", got)
	}
}

func TestExecuteUnknownCommand(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	typeIn(cb, "/nope")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgUnknownCommand) {
		t.Fatalf("status = %q, want the unknown-command flash", got)
	}
	if got := cb.input.GetText(); got != "/nope" {
		t.Fatalf("input = %q, must be kept for editing", got)
	}
}

func TestExecuteZeroArgCommandWithArgument(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	typeIn(cb, "/exit x")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgNoArg("/exit")) {
		t.Fatalf("status = %q, want %q", got, view.CmdMsgNoArg("/exit"))
	}
	if got := cb.input.GetText(); got != "/exit x" {
		t.Fatalf("input = %q, must be kept for editing", got)
	}
}

func TestExecuteInvalidArgumentFlashesLegalSet(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	typeIn(cb, "/window 9h")
	press(cb, tcell.KeyEnter, 0)
	want := view.CmdMsgInvalidArg("/window", []string{"1h", "24h", "7d"})
	if got := statusOf(u); !strings.Contains(got, want) {
		t.Fatalf("status = %q, want %q", got, want)
	}
	if u.detail.filters.window != 24*time.Hour {
		t.Fatalf("window changed to %v on an invalid argument", u.detail.filters.window)
	}
	if got := cb.input.GetText(); got != "/window 9h" {
		t.Fatalf("input = %q, must be kept for editing", got)
	}
}

func TestExecuteBareEnumCycles(t *testing.T) {
	u := newTestUI(t, 1) // default window 24h
	cb := u.cmd
	typeIn(cb, "/window")
	press(cb, tcell.KeyEnter, 0)
	if u.detail.filters.window != 7*24*time.Hour {
		t.Fatalf("bare /window cycled to %v, want 7d", u.detail.filters.window)
	}
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("input = %q after a successful run, want cleared", got)
	}
	if cb.menuOpen() {
		t.Fatal("menu must close after execution")
	}
}

func TestExecuteArgumentDirectSelect(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	for _, tc := range []struct {
		cmdline string
		check   func(*testing.T, *detailPane)
	}{
		{"/window 1h", func(t *testing.T, d *detailPane) {
			if d.filters.window != time.Hour {
				t.Errorf("window = %v, want 1h", d.filters.window)
			}
		}},
		{"/window 24h", func(t *testing.T, d *detailPane) {
			if d.filters.window != 24*time.Hour {
				t.Errorf("window = %v, want 24h", d.filters.window)
			}
		}},
		{"/window 7d", func(t *testing.T, d *detailPane) {
			if d.filters.window != 7*24*time.Hour {
				t.Errorf("window = %v, want 7d", d.filters.window)
			}
		}},
		{"/revision all", func(t *testing.T, d *detailPane) {
			if d.filters.revision != "all" {
				t.Errorf("revision = %q, want all", d.filters.revision)
			}
		}},
		{"/revision current", func(t *testing.T, d *detailPane) {
			if d.filters.revision != "" {
				t.Errorf("revision = %q, want \"\" (current)", d.filters.revision)
			}
		}},
		{"/source manual", func(t *testing.T, d *detailPane) {
			if d.filters.source != store.SourceManual {
				t.Errorf("source = %q, want manual", d.filters.source)
			}
		}},
		{"/source scheduled", func(t *testing.T, d *detailPane) {
			if d.filters.source != store.SourceScheduled {
				t.Errorf("source = %q, want scheduled", d.filters.source)
			}
		}},
		{"/source all", func(t *testing.T, d *detailPane) {
			if d.filters.source != store.SourceAll {
				t.Errorf("source = %q, want all", d.filters.source)
			}
		}},
	} {
		typeIn(cb, tc.cmdline)
		press(cb, tcell.KeyEnter, 0)
		tc.check(t, u.detail)
		if got := cb.input.GetText(); got != "" {
			t.Fatalf("%s: input = %q after run, want cleared", tc.cmdline, got)
		}
	}
}

func TestExecuteMenuHighlightedArgumentRuns(t *testing.T) {
	// "/source a" leaves [all] highlighted; Enter direct-selects it (prd R2).
	u := newTestUI(t, 1)
	cb := u.cmd
	typeIn(cb, "/source a")
	press(cb, tcell.KeyEnter, 0)
	if u.detail.filters.source != store.SourceAll {
		t.Fatalf("source = %q, want all via the highlighted menu entry", u.detail.filters.source)
	}
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("input = %q, want cleared", got)
	}
}

func TestExecuteMenuHighlightedCommandRuns(t *testing.T) {
	// review-fixpack-2 F12: a UNIQUE prefix executes on Enter; an AMBIGUOUS
	// prefix ("/w" matches /web+/window) must NOT fire the highlighted entry
	// (a bare "/" would otherwise execute /exit, the registry's first entry).
	u := newTestUI(t, 1)
	cb := u.cmd

	// Ambiguous: flash + input kept for completion.
	typeIn(cb, "/w")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgAmbiguousCommand) {
		t.Fatalf("status = %q, want the ambiguous-command flash", got)
	}
	if got := cb.input.GetText(); got != "/w" {
		t.Fatalf("input = %q, must be kept for completion", got)
	}

	// Bare "/": the worst case — highlights /exit; Enter must not quit.
	typeIn(cb, "/")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgAmbiguousCommand) {
		t.Fatalf("status = %q after bare /+Enter, want the ambiguous flash", got)
	}

	// Unique: executes (runs /web: no ServerHook → error flash, input cleared).
	typeIn(cb, "/web")
	press(cb, tcell.KeyEnter, 0)
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("input = %q after executing the unique command, want cleared", got)
	}
	if got := statusOf(u); !strings.Contains(got, "Web") {
		t.Fatalf("status = %q, want the /web feedback", got)
	}
}

func TestExecuteNeedSelectionGuard(t *testing.T) {
	u := newTestUI(t, 0) // nothing selected
	cb := u.cmd
	typeIn(cb, "/edit")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgNeedSelection) {
		t.Fatalf("status = %q, want the need-selection flash", got)
	}
	if u.modalOpen() {
		t.Fatal("/edit must not open the form with nothing selected")
	}
	if got := cb.input.GetText(); got != "/edit" {
		t.Fatalf("input = %q, must be kept for editing", got)
	}
	// The enum commands are selection-scoped too (design.md §3 step 7).
	typeIn(cb, "/window 1h")
	press(cb, tcell.KeyEnter, 0)
	if u.detail.filters.window == time.Hour {
		t.Fatal("/window must not apply with nothing selected")
	}
}

func TestExecuteModalsAndDismission(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	for _, cmdline := range []string{"/new", "/settings", "/edit", "/delete"} {
		typeIn(cb, cmdline)
		press(cb, tcell.KeyEnter, 0)
		if !u.modalOpen() {
			t.Fatalf("%s did not open its modal", cmdline)
		}
		u.dismissModal()
		if u.modalOpen() {
			t.Fatalf("modal still open after dismissing %s", cmdline)
		}
		if got := u.app.GetFocus(); got != u.cmd.input {
			t.Fatalf("focus after dismissing %s = %T, want the command bar", cmdline, got)
		}
	}
}

func TestExecuteQuitStopsTheApp(t *testing.T) {
	// On a dormant app Stop is a no-op (screen not yet set); the real exit
	// path is covered by the simulation-screen test. Here: must not panic
	// and must clear the input.
	u := newTestUI(t, 0)
	cb := u.cmd
	typeIn(cb, "/exit")
	press(cb, tcell.KeyEnter, 0)
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("input = %q after /exit, want cleared", got)
	}
}

func TestHelpCommandListsRegistry(t *testing.T) {
	u := newTestUI(t, 0)
	cb := u.cmd
	typeIn(cb, "/help")
	press(cb, tcell.KeyEnter, 0)
	detail := u.detailView.GetText(true)
	for _, c := range commands {
		if !strings.Contains(detail, c.name) {
			t.Errorf("help listing is missing %s", c.name)
		}
	}
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgHelpListed) {
		t.Fatalf("status = %q, want %q", got, view.CmdMsgHelpListed)
	}
	if got := cb.input.GetText(); got != "" {
		t.Fatalf("input = %q after /help, want cleared", got)
	}
}

func TestRefreshCommandRereadsTheStore(t *testing.T) {
	u := newTestUI(t, 0)
	if strings.Contains(u.detailView.GetText(true), "latecomer") {
		t.Fatal("precondition: provider must not exist yet")
	}
	// A provider added after the last redraw only appears through the
	// refresh pipeline the ticker and /refresh share.
	if _, err := u.deps.Store.AddProvider(store.Provider{
		Name: "latecomer", BaseURL: "https://api.example.com", Model: "m",
		Prompt: "hi", MaxTokens: 16, TimeoutSec: 10, TTFTTimeoutMs: 5000,
		TTFTSlowMs: 1000, IntervalSec: 60, Enabled: true,
	}); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	cb := u.cmd
	typeIn(cb, "/refresh")
	press(cb, tcell.KeyEnter, 0)
	if !strings.Contains(u.detailView.GetText(true), "latecomer") {
		t.Fatal("/refresh did not re-read the store")
	}
}

// --- Esc rotation (Q9; 2026-10-19 moved off Tab) ---

func TestEscRotationThreeStations(t *testing.T) {
	u := newTestUI(t, 1)
	// Station 1 → 2: from the command bar via its own capture (empty input:
	// Esc's clear stage is done, so it rotates).
	if ev := press(u.cmd, tcell.KeyEscape, 0); ev != nil {
		t.Fatal("Esc on the command bar must be consumed")
	}
	if got := u.app.GetFocus(); got != u.overview.list {
		t.Fatalf("station 2 = %T, want the list", got)
	}
	// Station 2 → 3 via the app-level capture.
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); ev != nil {
		t.Fatal("app-level Esc must be consumed")
	}
	if got := u.app.GetFocus(); got != u.detailView {
		t.Fatalf("station 3 = %T, want the detail view", got)
	}
	// Station 3 → 1.
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone)); ev != nil {
		t.Fatal("app-level Esc must be consumed")
	}
	if got := u.app.GetFocus(); got != u.cmd.input {
		t.Fatalf("station 1 = %T, want the command bar", got)
	}
}

func TestGlobalKeysYieldToCommandBar(t *testing.T) {
	// The app-level capture runs first; with focus on the command bar Esc
	// (and Tab) must pass through so the bar's capture owns them — its Esc
	// clears a non-empty input before ever rotating.
	u := newTestUI(t, 1)
	for _, key := range []tcell.Key{tcell.KeyEscape, tcell.KeyTab} {
		if ev := u.globalKeys(tcell.NewEventKey(key, 0, tcell.ModNone)); ev == nil {
			t.Fatalf("app-level %v must yield while the command bar holds focus", key)
		}
	}
}

// Tab no longer rotates; outside the command bar it must be swallowed so
// tview's built-ins cannot act on it (tview List maps Tab to Down — a passed
// Tab would silently move the provider selection).
func TestGlobalKeysSwallowsTabOutsideCommandBar(t *testing.T) {
	u := newTestUI(t, 1)
	u.app.SetFocus(u.overview.list)
	before := u.overview.selectedID()
	if ev := u.globalKeys(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)); ev != nil {
		t.Fatal("app-level Tab must be consumed outside the command bar")
	}
	if got := u.app.GetFocus(); got != u.overview.list {
		t.Fatalf("focus after Tab = %T, want the list (Tab no longer rotates)", got)
	}
	if got := u.overview.selectedID(); got != before {
		t.Fatalf("selection moved %d → %d: Tab must not reach tview List", before, got)
	}
}

// --- simulation-screen tests (design.md §8) ---

// readState runs f on the app goroutine (spec: reading tview state from a
// test goroutine goes through QueueUpdate). QueueUpdate blocks until the
// update has executed, so the call itself is the synchronization edge.
func readState(t *testing.T, app *tview.Application, f func()) {
	t.Helper()
	app.QueueUpdate(f)
}

func TestCommandBarScreenFocusMenuAndRotation(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	seedProviders(t, st, 1)

	app := tview.NewApplication()
	screen := &readyScreen{Screen: tcell.NewSimulationScreen("UTF-8"), ready: make(chan struct{})}
	app.SetScreen(screen)
	u := newUI(app, Deps{Store: st, PortStart: 10110})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- u.eventLoop(ctx) }()

	select {
	case <-screen.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("screen was not initialized within 5s")
	}
	time.Sleep(200 * time.Millisecond) // let the event loop start

	// A1: startup focus sits on the command input.
	var focus tview.Primitive
	readState(t, app, func() { focus = app.GetFocus() })
	if focus != u.cmd.input {
		t.Fatalf("startup focus = %T, want the command input", focus)
	}

	// A1: "/" opens the full 16-command menu (15 + /cache, 10-22-cache-check).
	screen.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
	time.Sleep(150 * time.Millisecond)
	var open bool
	var items int
	var menuText string
	readState(t, app, func() {
		open = u.cmd.menuOpen()
		items = len(u.cmd.menuItems)
		menuText = u.cmd.menu.GetText(true)
	})
	if !open || items != 16 {
		t.Fatalf("menu after /: open=%v items=%d, want the full 16", open, items)
	}
	if !strings.Contains(menuText, "/help") {
		t.Fatalf("menu text %q must list /help", menuText)
	}
	if !strings.Contains(menuText, "/page") {
		t.Fatalf("menu text %q must list /page", menuText)
	}
	if !strings.Contains(menuText, "/cache") {
		t.Fatalf("menu text %q must list /cache", menuText)
	}

	// A1: prefix filtering leaves /web + /window.
	screen.InjectKey(tcell.KeyRune, 'w', tcell.ModNone)
	time.Sleep(150 * time.Millisecond)
	readState(t, app, func() { items = len(u.cmd.menuItems) })
	if items != 2 {
		t.Fatalf("menu after /w: %d items, want 2 (/web, /window)", items)
	}

	// A2: Esc clears the "/w" text (menu closes), then Esc×3 walks
	// cmdInput → list → detail → cmdInput and lands back on the command bar.
	screen.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
	time.Sleep(150 * time.Millisecond)
	for i := 0; i < 3; i++ {
		screen.InjectKey(tcell.KeyEscape, 0, tcell.ModNone)
		time.Sleep(150 * time.Millisecond)
	}
	readState(t, app, func() { focus = app.GetFocus() })
	if focus != u.cmd.input {
		t.Fatalf("focus after three Escs = %T, want the command bar again", focus)
	}

	// Shut the loop down through the ctx path.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop did not exit after ctx cancel")
	}
}

// A5 (prd R4, simulation-screen half): after the step-2 retirement every
// former action rune — global q/p/r/w/,/x, list n/e/d, detail 1/2/3/R/S —
// must be a no-op with focus OUTSIDE the command bar: no modal opens, no
// filter flips, no probe/toggle flash, and the event loop survives q.
// /window /revision /source act through the command bar only.
func TestRetiredRuneKeysDoNothing(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 1)

	app := tview.NewApplication()
	screen := &readyScreen{Screen: tcell.NewSimulationScreen("UTF-8"), ready: make(chan struct{})}
	app.SetScreen(screen)
	u := newUI(app, Deps{Store: st, PortStart: 10110})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- u.eventLoop(ctx) }()

	select {
	case <-screen.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("screen was not initialized within 5s")
	}
	time.Sleep(200 * time.Millisecond) // let the event loop start

	var guide string
	readState(t, app, func() {
		app.SetFocus(u.overview.list) // former globalKeys/overviewKeys scope
		guide = u.statusBar.GetText(true)
	})

	// Former global runes and former list-capture runes, list focused.
	for _, r := range []rune("qQprw,xned") {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	// Former detail-capture runes, detail focused.
	readState(t, app, func() { app.SetFocus(u.detailView) })
	for _, r := range []rune("123RS") {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	time.Sleep(300 * time.Millisecond)

	// q must NOT have stopped the loop (a dead loop would make the readState
	// below hang forever, so assert liveness first).
	select {
	case err := <-done:
		t.Fatalf("event loop exited after the retired runes (err=%v)", err)
	default:
	}

	var modal, enabled bool
	var window time.Duration
	var revision, source, status string
	readState(t, app, func() {
		modal = u.modalOpen()
		window, revision, source = u.detail.filters.window, u.detail.filters.revision, u.detail.filters.source
		status = u.statusBar.GetText(true)
		p, ok := st.GetProvider(ids[0])
		enabled = ok && p.Enabled
	})
	if modal {
		t.Fatal("no retired rune may open a modal (n/e/d/,)")
	}
	if !enabled {
		t.Fatal("no retired rune may toggle the provider (x)")
	}
	if window != 24*time.Hour || revision != "" || source != store.SourceScheduled {
		t.Fatalf("retired filter runes changed state: window=%v revision=%q source=%q, want defaults",
			window, revision, source)
	}
	if status != guide {
		t.Fatalf("status = %q after the retired runes, want the unchanged guide %q (no probe/toggle flash)", status, guide)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop did not exit after ctx cancel")
	}
}

// TestStatusHintFollowsFocus pins the anti-silence contract of the freeze
// report (10-16): from the list/detail the keyboard looks dead (commands live
// in the command bar), so the status line must say what DOES work there —
// and rotateFocus must refresh it on every station change.
func TestStatusHintFollowsFocus(t *testing.T) {
	u := newTestUI(t, 1)

	// Home state: the command-bar guide.
	if got := u.statusBar.GetText(false); got != "[gray]"+view.CommandHintText()+"[white]" {
		t.Fatalf("home hint = %q, want the command-bar guide", got)
	}

	// Station 2: the list guide names its keys and the way back. From the
	// command bar Esc is the bar's own capture (globalKeys yields), so drive
	// it the way the event loop does.
	if ev := press(u.cmd, tcell.KeyEscape, 0); ev != nil {
		t.Fatal("Esc on the command bar must be consumed")
	}
	if got := u.statusBar.GetText(false); got != "[gray]"+view.ListHintText()+"[white]" {
		t.Fatalf("list hint = %q, want %q", got, view.ListHintText())
	}

	// Station 3: the detail guide.
	u.globalKeys(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if got := u.statusBar.GetText(false); got != "[gray]"+view.DetailHintText()+"[white]" {
		t.Fatalf("detail hint = %q, want %q", got, view.DetailHintText())
	}

	// Back home: the command-bar guide again.
	u.globalKeys(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if got := u.statusBar.GetText(false); got != "[gray]"+view.CommandHintText()+"[white]" {
		t.Fatalf("restored hint = %q, want the command-bar guide", got)
	}
}

// review-fixpack-2 F10: /page drives the detail pagination and flashes at
// the boundaries instead of silently doing nothing.
func TestPageCommand(t *testing.T) {
	u := newTestUI(t, 1)
	cb := u.cmd
	p := u.deps.Store.GetProviders()[0]
	seedResults(t, u.deps.Store, p.ID, pageSize+5) // 25 rows → 2 pages
	u.redrawLocked()                               // compute the page-1 next cursor
	if u.detail.nextCursor == nil {
		t.Fatal("25 rows must yield a next cursor on page 1")
	}

	typeIn(cb, "/page n") // arg phase: [next] highlighted → Enter direct-selects
	press(cb, tcell.KeyEnter, 0)
	if u.detail.pageCursor == nil {
		t.Fatal("/page next must advance to page 2")
	}
	u.redrawLocked() // recompute nextCursor from page 2 (5 rows, no more)
	if u.detail.nextCursor != nil {
		t.Fatal("page 2 is the last page")
	}

	// Boundary: /page next on the last page flashes instead of no-op.
	typeIn(cb, "/page next")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgPageNoMore) {
		t.Fatalf("status = %q, want the last-page flash", got)
	}

	// /page prev walks back to page 1, then flashes 已是第一页.
	typeIn(cb, "/page p")
	press(cb, tcell.KeyEnter, 0)
	if u.detail.pageCursor != nil {
		t.Fatal("/page prev must return to the newest page (nil cursor)")
	}
	typeIn(cb, "/page prev")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgPageNoPrev) {
		t.Fatalf("status = %q, want the first-page flash", got)
	}
}

// review-fixpack-2 F11: the fixed 3-row menu viewport follows the highlight,
// so ↓ can never walk the highlight out of sight.
func TestMenuViewportFollowsHighlight(t *testing.T) {
	cb := bareBar()
	typeIn(cb, "/") // full registry: 15 candidates
	for i := 0; i < 5; i++ {
		press(cb, tcell.KeyDown, 0)
	}
	if cb.menuSel != 5 {
		t.Fatalf("menuSel = %d after 5×↓, want 5", cb.menuSel)
	}
	row, _ := cb.menu.GetScrollOffset() // (row, column)
	if row != cb.menuSel-1 {
		t.Fatalf("menu top row = %d, want %d (highlight-1 stays in view)", row, cb.menuSel-1)
	}
}
