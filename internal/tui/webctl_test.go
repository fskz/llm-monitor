package tui

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"llm-monitor/internal/store"
)

// fakeEngine is a minimal view.EngineAPI for the action tests: ProbeNow
// returns a canned result; IsInFlight recognizes the sentinel error.
type fakeEngine struct {
	probeResult *store.Result
	probeErr    error
}

func (f *fakeEngine) Probing(id int) bool  { return false }
func (f *fakeEngine) StorageError() string { return "" }
func (f *fakeEngine) ProbeNow(id int) (*store.Result, error) {
	return f.probeResult, f.probeErr
}
func (f *fakeEngine) IsInFlight(err error) bool { return err != nil && err.Error() == "in-flight" }

// The on-demand web panel lifecycle (A2): start binds and reports the URL;
// start twice re-reports without rebinding; stop releases the port; stop
// again is a no-op.
func TestWebPanelLifecycle(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	deps := Deps{
		Store:     st,
		PortStart: 20110,
		ServerHook: func(boundPort int) http.Handler {
			return http.NotFoundHandler()
		},
	}
	w := newWebPanel(deps)

	url1, err := w.start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if url1 == "" {
		t.Fatal("start returned empty URL")
	}
	port := portOf(t, url1)

	// While running: the exact port is taken by the panel listener…
	if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
		ln.Close()
		t.Fatalf("port %d must be occupied while the panel runs", port)
	}
	// …and a second start is an idempotent no-op re-reporting the same URL.
	url2, err := w.start()
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if url2 != url1 {
		t.Fatalf("second start URL = %q, want %q", url2, url1)
	}

	if !w.stop() {
		t.Fatal("stop must report true when running")
	}
	// Port released: findListener now binds the same port again.
	ln, again, err := findListener(port)
	if err != nil {
		t.Fatalf("port %d not released after stop: %v", port, err)
	}
	ln.Close()
	if again != port {
		t.Fatalf("rebind port = %d, want %d", again, port)
	}
	if w.stop() {
		t.Fatal("second stop must be a no-op")
	}
	if w.running() {
		t.Fatal("running() after stop")
	}
}

// Without a ServerHook the panel fails with a clear error instead of
// panicking (cmd always passes one; tests may not).
func TestWebPanelNoHook(t *testing.T) {
	w := newWebPanel(Deps{PortStart: 20110})
	if _, err := w.start(); err == nil {
		t.Fatal("start without ServerHook must fail")
	}
}

// findListener must reject invalid ranges (mirrors the moved gate tests).
func TestFindListenerInvalid(t *testing.T) {
	if _, _, err := findListener(0); err == nil {
		t.Fatal("findListener(0) must fail")
	}
	if _, _, err := findListener(65536); err == nil {
		t.Fatal("findListener(65536) must fail")
	}
}

// The report export writes a self-contained HTML file under <data>/reports
// with the shared rendering pipeline (A5).
func TestExportReport(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 1)
	seedResults(t, st, ids[0], 5)
	p, _ := st.GetProvider(ids[0])

	u := &ui{deps: Deps{Store: st}}
	path, err := u.exportReport(p)
	if err != nil {
		t.Fatalf("exportReport: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(st.Dir(), "reports") {
		t.Fatalf("report dir = %s, want %s", filepath.Dir(path), filepath.Join(st.Dir(), "reports"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	s := string(data)
	for _, want := range []string{"<html", "LLM", "统计", "成功"} {
		if !strings.Contains(s, want) {
			t.Errorf("report missing %q", want)
		}
	}
	if strings.Contains(s, "http://127.0.0.1") {
		// no external resource references in a self-contained report
		t.Error("report must not reference the panel URL")
	}
}

// The status line surfaces the in-flight conflict message when ProbeNow
// reports the engine's ErrInFlight equivalent (§4.6 / 409 semantics).
func TestProbeNowConflictMessage(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])

	app := newTestApp(t)
	u := &ui{app: app, deps: Deps{Store: st, Engine: &fakeEngine{probeErr: errSentinel("in-flight")}}}
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	u.probeNow(p)
	if !waitForStatus(u, "已有在途请求") {
		t.Fatalf("status = %q, want in-flight conflict message", u.statusBar.GetText(true))
	}
}

// A successful probe paints the result line.
func TestProbeNowSuccessMessage(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])

	total := int64(1200)
	res := &store.Result{Status: "ok", Success: true, TotalMs: total}
	app := newTestApp(t)
	u := &ui{app: app, deps: Deps{Store: st, Engine: &fakeEngine{probeResult: res}}}
	u.statusBar = tview.NewTextView().SetDynamicColors(true)

	u.probeNow(p)
	if !waitForStatus(u, "成功") {
		t.Fatalf("status = %q, want result line", u.statusBar.GetText(true))
	}
	if !strings.Contains(u.statusBar.GetText(true), "1.20 s") {
		t.Fatalf("status = %q, want formatted total 1.20 s", u.statusBar.GetText(true))
	}
}

type errSentinel string

func (e errSentinel) Error() string { return string(e) }

// --- small test helpers ---------------------------------------------------

func portOf(t *testing.T, url string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimSuffix(url, "/"), "http://"))
	if err != nil {
		t.Fatalf("parse url %q: %v", url, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return port
}

// newTestApp runs a real event loop on a simulation screen so
// QueueUpdateDraw closures actually execute (an unstarted app would block
// them forever — QueueUpdate waits for the loop's acknowledgment).
func newTestApp(t *testing.T) *tview.Application {
	t.Helper()
	app := tview.NewApplication()
	app.SetRoot(tview.NewBox(), true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = app.SetScreen(tcell.NewSimulationScreen("UTF-8")).Run()
	}()
	t.Cleanup(func() {
		app.Stop()
		<-done
	})
	return app
}

// waitForStatus polls the status line until it contains want. The read
// itself runs through QueueUpdate: TextView state belongs to the app
// goroutine, and reading GetText from the test goroutine races the event
// loop (caught by -race).
func waitForStatus(u *ui, want string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := make(chan string, 1)
		u.app.QueueUpdate(func() { got <- u.statusBar.GetText(true) })
		select {
		case s := <-got:
			if strings.Contains(s, want) {
				return true
			}
		case <-time.After(500 * time.Millisecond):
		}
	}
	return false
}
