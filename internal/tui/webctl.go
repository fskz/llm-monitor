package tui

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// portTries is how many consecutive ports the on-demand web panel scans
// upward from PortStart (design.md §3).
const portTries = 100

// webPanel is the on-demand browser panel (R2): one listener + http server
// bound to 127.0.0.1 only while the user wants it, closed explicitly from
// the TUI (no idle auto-shutdown — user decision, grill-me 2026-10-09).
//
// The http.Handler comes from the Deps.ServerHook indirection: tui must not
// import internal/server (dependency direction, design.md §1), so cmd
// passes server.New's handler in. webPanel owns only the listener lifecycle.
type webPanel struct {
	deps Deps

	mu     sync.Mutex
	ln     net.Listener
	url    string
	closed chan struct{} // closed once Serve returned
}

func newWebPanel(deps Deps) *webPanel { return &webPanel{deps: deps} }

// start binds the panel (scan from PortStart), serves in the background and
// opens the browser. Returns the URL; a second call while running is a
// no-op that re-reports the same URL (R2: 已运行提示).
func (w *webPanel) start() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ln != nil {
		return w.url, nil
	}
	if w.deps.ServerHook == nil {
		return "", errors.New("web 面板不可用：处理器未装配")
	}
	ln, port, err := findListener(w.deps.PortStart)
	if err != nil {
		return "", err
	}
	handler := w.deps.ServerHook(port)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		// Close makes Serve return with the closed-listener error; anything
		// else is a real failure worth a stderr line.
		if err := http.Serve(ln, handler); err != nil && !isClosedConnErr(err) {
			fmt.Fprintf(os.Stderr, "web 面板服务异常退出：%v\n", err)
		}
	}()
	w.ln, w.url, w.closed = ln, fmt.Sprintf("http://127.0.0.1:%d/", port), closed
	openBrowser(w.url)
	return w.url, nil
}

// stop closes the listener and waits for Serve to return, so the port is
// fully released when stop returns (A2 gate: 关闭后端口释放).
func (w *webPanel) stop() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ln == nil {
		return false
	}
	_ = w.ln.Close()
	<-w.closed
	w.ln, w.url = nil, ""
	return true
}

// running reports whether the panel is up (status line).
func (w *webPanel) running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ln != nil
}

// currentURL returns the bound URL or "" when stopped.
func (w *webPanel) currentURL() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.url
}

// isClosedConnErr matches the "use of closed network connection" family that
// http.Serve returns after listener Close on various platforms.
func isClosedConnErr(err error) bool {
	if errors.Is(err, http.ErrServerClosed) {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection")
}

// findListener probes 127.0.0.1 ports from start upward (max portTries) and
// returns the first free listener. The --port flag overrides the start port
// (§5.2, task 10-09: the scan now runs only when the on-demand web panel is
// pulled up from the TUI, never at startup — the process starts with zero
// listening ports).
func findListener(start int) (net.Listener, int, error) {
	if start < 1 || start > 65535 {
		return nil, 0, fmt.Errorf("invalid port %d", start)
	}
	for p := start; p <= 65535 && p < start+portTries; p++ {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			return ln, p, nil
		}
	}
	return nil, 0, fmt.Errorf("no free port on 127.0.0.1 in range %d-%d", start, start+portTries-1)
}

// openBrowser launches the system browser for url; failure is non-fatal and
// the URL is always printed so it can be opened manually (§5.2).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Printf("自动打开浏览器失败（%v）；请手动访问 %s\n", err, url)
	}
}
