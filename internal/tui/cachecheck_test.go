package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// /cache command tests (task 10-22-cache-check): the needSel guard on a
// dormant ui, the conflict/failure flashes on a bare ui with a running event
// loop (probeNow test pattern), the full typed-command flow on a simulation
// screen, and the persistent detail block's three states.

// The needSel guard fires before the handler: nothing selected → flash, no
// engine call, input kept for editing.
func TestCacheCheckNeedSelectionGuard(t *testing.T) {
	u := newTestUI(t, 0)
	cb := u.cmd
	typeIn(cb, "/cache")
	press(cb, tcell.KeyEnter, 0)
	if got := statusOf(u); !strings.Contains(got, view.CmdMsgNeedSelection) {
		t.Fatalf("status = %q, want the need-selection flash", got)
	}
	if got := cb.input.GetText(); got != "/cache" {
		t.Fatalf("input = %q, must be kept for editing", got)
	}
}

// The engine's cache-check mutex conflict flashes the 409-equivalent message
// (the /probe in-flight flash's wording family).
func TestCacheCheckConflictMessage(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])

	app := newTestApp(t)
	u := &ui{app: app, deps: Deps{Store: st, Engine: &fakeEngine{cacheErr: errSentinel("cache-in-flight")}}}
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	u.cacheCheckNow(p)
	if !waitForStatus(u, view.CmdMsgCacheCheckInFlight) {
		t.Fatalf("status = %q, want %q", u.statusBar.GetText(true), view.CmdMsgCacheCheckInFlight)
	}
}

// Any other engine error flashes the failure message with the error text.
func TestCacheCheckFailureMessage(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])

	app := newTestApp(t)
	u := &ui{app: app, deps: Deps{Store: st, Engine: &fakeEngine{cacheErr: errors.New("存储异常")}}}
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	u.cacheCheckNow(p)
	if !waitForStatus(u, view.CmdMsgCacheCheckFailed("存储异常")) {
		t.Fatalf("status = %q, want %q", u.statusBar.GetText(true), view.CmdMsgCacheCheckFailed("存储异常"))
	}
}

// The full typed flow: "/cache" + Enter on a live event loop dispatches the
// handler, the fake engine answers, and the queueDraw closure both flashes
// the verdict and redraws the detail block from the (fake-persisted) record.
func TestCacheCheckCommandFullFlow(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])
	cached := int64(1234)
	rec := store.CacheCheckRecord{
		Verdict:   "supported",
		Evidence:  "hop2 cached>0",
		CheckedAt: time.Now().UnixMilli(),
		Revision:  p.Revision,
		Hops: []store.CacheCheckHop{
			{Status: "ok", TotalMs: 600, CachedTokens: ptrI64(0)},
			{Status: "ok", TotalMs: 200, CachedTokens: &cached},
		},
	}

	app := tview.NewApplication()
	screen := &readyScreen{Screen: tcell.NewSimulationScreen("UTF-8"), ready: make(chan struct{})}
	app.SetScreen(screen)
	eng := &fakeEngine{cacheRec: rec}
	u := newUI(app, Deps{Store: st, Engine: eng, PortStart: 10110})

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

	for _, r := range "/cache" {
		screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	if !waitForStatus(u, "前缀缓存：已开启") {
		t.Fatalf("status = %q, want the supported-verdict flash", u.statusBar.GetText(true))
	}
	var detail string
	readState(t, app, func() { detail = u.detailView.GetText(true) })
	for _, want := range []string{"前缀缓存", "已开启", "cached_tokens", "1234"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail block missing %q after the check:\n%s", want, detail)
		}
	}
	if strings.Contains(detail, "配置已变更") {
		t.Error("a current-revision record must not carry the stale hint")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("event loop did not exit after ctx cancel")
	}
}

// The persistent block renders its three states: unchecked (未检测), a fresh
// current-revision record, and the stale hint after a revision change. With
// no engine attached the block is skipped entirely (test-only wiring).
func TestDetailCacheCheckBlock(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])

	cached := int64(64)
	rec := store.CacheCheckRecord{
		Verdict:   "unsupported",
		CheckedAt: time.Now().UnixMilli(),
		Revision:  p.Revision,
		Hops: []store.CacheCheckHop{
			{Status: "ok", TotalMs: 500, CachedTokens: ptrI64(0)},
			{Status: "ok", TotalMs: 300, CachedTokens: &cached},
		},
	}

	// No engine → no block at all.
	d := newDetailPane(st, nil)
	if got := d.renderText(p); strings.Contains(got, "前缀缓存") {
		t.Fatal("engine-less detail must not render the cache block")
	}

	// Engine attached, never checked → the 未检测 placeholder.
	d = newDetailPane(st, &fakeEngine{})
	if got := d.renderText(p); !containsPlain(got, view.CmdMsgCacheCheckNone) {
		t.Fatalf("unchecked block = missing %q placeholder:\n%s", view.CmdMsgCacheCheckNone, got)
	}

	// Fresh record of the current revision → verdict + evidence, no stale hint.
	d = newDetailPane(st, &fakeEngine{latest: rec, latestFound: true})
	got := d.renderText(p)
	for _, want := range []string{"未开启", "cached_tokens", "64"} {
		if !containsPlain(got, want) {
			t.Errorf("fresh record block missing %q:\n%s", want, got)
		}
	}
	if containsPlain(got, "配置已变更") {
		t.Error("current-revision record must not render the stale hint")
	}

	// Old revision → the stale hint rides along.
	rec.Revision = p.Revision - 1
	d = newDetailPane(st, &fakeEngine{latest: rec, latestFound: true})
	if got := d.renderText(p); !containsPlain(got, "配置已变更，建议重新检测") {
		t.Fatalf("stale record block missing the stale hint:\n%s", got)
	}
}

// /exportcache writes the all-providers JSON snapshot into the reports dir
// and flashes the path. It needs no selection — the snapshot is per-tool —
// and unchecked providers ride along as 未检测 entries.
func TestExportCacheSnapshot(t *testing.T) {
	u := newTestUI(t, 2)
	u.exportCacheSnapshot()

	status := statusOf(u)
	if !strings.Contains(status, view.CmdMsgCacheExportDone("")[:12]) { // "缓存判定快照已导出："
		t.Fatalf("status = %q, want the export-done flash with the path", status)
	}
	dir := filepath.Join(u.deps.Store.Dir(), "reports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read reports dir: %v", err)
	}
	var path string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "cache-check-") && strings.HasSuffix(e.Name(), ".json") {
			path = filepath.Join(dir, e.Name())
		}
	}
	if path == "" {
		t.Fatalf("no cache-check-*.json in %s", dir)
	}
	if !strings.Contains(status, tviewEscape(path)) {
		t.Fatalf("status %q must carry the written path %q", status, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap struct {
		GeneratedAt int64 `json:"generated_at"`
		Checks      []struct {
			ProviderName string `json:"provider_name"`
			Verdict      string `json:"verdict"`
			VerdictLabel string `json:"verdict_label"`
			Hops         []any  `json:"hops"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.GeneratedAt == 0 {
		t.Fatal("generated_at = 0, want a timestamp")
	}
	if len(snap.Checks) != 2 {
		t.Fatalf("checks = %d, want 2 (every provider, unchecked included)", len(snap.Checks))
	}
	for _, c := range snap.Checks {
		if c.ProviderName == "" {
			t.Errorf("entry %+v must carry the provider name", c)
		}
		if c.Verdict != "" || c.VerdictLabel != view.CacheCheckVerdictNone || len(c.Hops) != 0 {
			t.Errorf("unchecked entry = %+v, want the 未检测 empty state with hops []", c)
		}
	}
}
