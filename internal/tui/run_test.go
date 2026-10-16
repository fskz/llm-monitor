package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"llm-monitor/internal/store"
)

// readyScreen closes ready once the underlying screen finished Init. tcell's
// simscreen Init writes the event channels that InjectKey reads, and Init
// runs on the app goroutine — without this signal there is no happens-before
// edge between Init and a test-side InjectKey, so the race detector fires
// even with sleeps.
type readyScreen struct {
	tcell.Screen
	once  sync.Once
	ready chan struct{}
}

func (s *readyScreen) Init() error {
	err := s.Screen.Init()
	s.once.Do(func() { close(s.ready) })
	return err
}

// InjectKey forwards to the underlying simscreen (tcell.Screen interface
// does not expose the injection helpers).
func (s *readyScreen) InjectKey(key tcell.Key, ch rune, mod tcell.ModMask) {
	s.Screen.(interface {
		InjectKey(tcell.Key, rune, tcell.ModMask)
	}).InjectKey(key, ch, mod)
}

// A1 gate (headless environment, task 10-09 阶段 3): Run must return promptly
// when ctx is cancelled — the SIGINT/SIGTERM graceful-exit path — instead of
// blocking on the terminal. A real screen cannot init under a test pipe, so
// the tcell simulation screen stands in; the interactive smoke test is done
// by a human on a real terminal.
func TestRunReturnsOnContextCancel(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	deps := Deps{Store: st, PortStart: 10110}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, deps, tcell.NewSimulationScreen("UTF-8"))
	}()

	time.Sleep(200 * time.Millisecond) // let the app loop start
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("Run returned %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of ctx cancellation")
	}
}

// Run must also return cleanly on a user quit — the /quit command typed
// through the command bar (task 10-16: the startup focus sits on the command
// input, so the old 'q' rune would just type into the field). Typing the
// full command exercises the real chain: rune insertion → changed callback
// → prefix filter → Enter capture → execute → registry dispatch → app.Stop.
func TestRunReturnsOnQuitKey(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	deps := Deps{Store: st, PortStart: 10110}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	screen := &readyScreen{Screen: tcell.NewSimulationScreen("UTF-8"), ready: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, deps, screen)
	}()

	// Wait for the screen Init inside run() to finish (happens-before edge),
	// then give the app event loop a moment to start before typing.
	select {
	case <-screen.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("screen was not initialized within 5s")
	}
	time.Sleep(200 * time.Millisecond)
	for _, r := range "/quit" {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil (user quit)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the /quit command")
	}
}

// The web panel start-port scan (task 10-09 阶段 3 gate): findListener must
// return a bound listener on 127.0.0.1 and skip ports that are taken.
func TestFindListenerSkipsOccupiedPorts(t *testing.T) {
	first, port, err := findListener(10110)
	if err != nil {
		t.Fatalf("findListener: %v", err)
	}
	defer first.Close()
	if port != 10110 {
		t.Fatalf("port = %d, want 10110", port)
	}

	second, port2, err := findListener(10110)
	if err != nil {
		t.Fatalf("findListener with occupied port: %v", err)
	}
	defer second.Close()
	if port2 != 10111 {
		t.Fatalf("port2 = %d, want 10111 (scan moves up)", port2)
	}

	if _, _, err := findListener(0); err == nil {
		t.Fatal("findListener(0) must fail")
	}
	if _, _, err := findListener(65536); err == nil {
		t.Fatal("findListener(65536) must fail")
	}
}

// review-fixpack-2 F13: a status flash survives focus rotation (rotateFocus →
// renderStatus) for flashTTL; after expiry the station hint returns.
func TestFlashSurvivesFocusRotation(t *testing.T) {
	u := newTestUI(t, 1)
	u.setStatus("[green]报告已导出：/tmp/x.html[-]")
	if got := statusOf(u); !strings.Contains(got, "报告已导出") {
		t.Fatalf("status = %q, want the flash", got)
	}

	// What Tab does: focus change + renderStatus.
	u.rotateFocus()
	if got := statusOf(u); !strings.Contains(got, "报告已导出") {
		t.Fatalf("status after rotation = %q, the flash must survive (was erased by Tab before)", got)
	}

	// After the TTL the hint takes the line back.
	u.flashAt = time.Now().Add(-2 * flashTTL)
	u.renderStatus()
	if got := statusOf(u); strings.Contains(got, "报告已导出") {
		t.Fatalf("expired flash must give way to the hint, got %q", got)
	}
}

// review-fixpack-2 F10: when the selected provider vanishes and the overview
// falls back to another provider, the detail pagination resets with it —
// the cursors belong to one provider's record sequence only.
func TestRedrawResetsPagingOnSelectionFallback(t *testing.T) {
	u := newTestUI(t, 2)
	all := u.deps.Store.GetProviders()
	if len(all) < 2 {
		t.Fatalf("need 2 providers, got %d", len(all))
	}
	second := all[1]

	// Select the second provider and page into its records.
	u.overview.selected = second.ID
	u.detail.pageCursor = &pageCursor{startedAt: 1, seq: 1}
	u.detailRenderedID = second.ID

	// The second provider is deleted from another end (store-direct, like
	// the web panel would); a redraw falls back to the first provider.
	if err := u.deps.Store.DeleteProvider(second.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	u.redrawLocked()

	if u.detail.pageCursor != nil {
		t.Fatal("pagination cursor must reset when the selection falls back")
	}
}
