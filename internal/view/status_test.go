package view

import (
	"io"
	"regexp"
	"strings"
	"testing"

	"llm-monitor/internal/probe"
	"llm-monitor/web"
)

// finalStatuses is the 10-status contract flowing probe → store → view →
// {web, report} (go-conventions.md). Enumerated from the probe constants so
// this test fails when a status is added without a view label.
var finalStatuses = []string{
	probe.StatusOK, probe.StatusTimeoutTTFT, probe.StatusTimeoutTotal,
	probe.StatusHTTPError, probe.StatusConnError, probe.StatusStreamError,
	probe.StatusProtocolError, probe.StatusEmpty, probe.StatusAborted,
	probe.StatusCancelled,
}

// monitorStatuses is the §7.2 monitor ladder rendered as badge labels.
var monitorStatuses = []string{
	"ok", "fail", "stale", "unknown", "disabled", "manual_only",
}

func TestStatusTextCoversAllFinalStatuses(t *testing.T) {
	want := map[string]string{
		"ok":             "成功",
		"timeout_ttft":   "超时（首内容）",
		"timeout_total":  "超时（总）",
		"http_error":     "错误（HTTP）",
		"conn_error":     "错误（连接）",
		"stream_error":   "错误（流）",
		"protocol_error": "错误（协议）",
		"empty":          "错误（空回复）",
		"aborted":        "错误（中断）",
		"cancelled":      "已取消",
	}
	for _, s := range finalStatuses {
		if got := StatusText(s); got != want[s] {
			t.Errorf("StatusText(%q) = %q, want %q", s, got, want[s])
		}
	}
}

func TestMonitorTextCoversAllMonitorStatuses(t *testing.T) {
	want := map[string]string{
		"ok":          "可用",
		"fail":        "不可用",
		"stale":       "结果过期",
		"unknown":     "未知",
		"disabled":    "已停用",
		"manual_only": "仅手动",
	}
	for _, s := range monitorStatuses {
		if got := MonitorText(s); got != want[s] {
			t.Errorf("MonitorText(%q) = %q, want %q", s, got, want[s])
		}
	}
}

func TestTextUnknownStatusPassthrough(t *testing.T) {
	// Unknown statuses render as themselves (web/app.js falls back the same
	// way: STATUS_TEXT[r.status] || r.status), never as an empty string.
	if got := StatusText("nonsense"); got != "nonsense" {
		t.Errorf("StatusText(unknown) = %q, want passthrough", got)
	}
	if got := MonitorText("nonsense"); got != "nonsense" {
		t.Errorf("MonitorText(unknown) = %q, want passthrough", got)
	}
}

/* ---------- web/app.js sync (acceptance A6) ---------- */

// appJS extracts the embedded panel script so the label tables can be
// compared against the Go mapping without duplicating them here.
func appJS(t *testing.T) string {
	t.Helper()
	f, err := web.FS().Open("app.js")
	if err != nil {
		t.Fatalf("open app.js from embedded FS: %v", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	return string(b)
}

// jsConstBlock returns the body of `const <name> = { ... };` from app.js.
func jsConstBlock(src, name string) string {
	start := strings.Index(src, "const "+name+" = {")
	if start < 0 {
		return ""
	}
	rest := src[start:]
	end := strings.Index(rest, "};")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// TestStatusTextMatchesWebPanel is the A6 single-source check: every entry
// of web/app.js STATUS_TEXT must equal view.StatusText for the same status,
// and the JS table must cover all 10 final statuses. Adding a status on
// either side without the other fails here (go-conventions.md cross-layer
// rule).
func TestStatusTextMatchesWebPanel(t *testing.T) {
	block := jsConstBlock(appJS(t), "STATUS_TEXT")
	if block == "" {
		t.Fatal("STATUS_TEXT table not found in web/app.js")
	}
	re := regexp.MustCompile(`([a-z_]+):\s*"([^"]*)"`)
	js := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		js[m[1]] = m[2]
	}
	if len(js) != len(finalStatuses) {
		t.Errorf("app.js STATUS_TEXT has %d entries, want %d (a status was added on one side only)", len(js), len(finalStatuses))
	}
	for _, s := range finalStatuses {
		want, ok := js[s]
		if !ok {
			t.Errorf("app.js STATUS_TEXT missing status %q", s)
			continue
		}
		if got := StatusText(s); got != want {
			t.Errorf("StatusText(%q) = %q, app.js = %q — tables drifted", s, got, want)
		}
	}
}

// TestMonitorTextMatchesWebPanel is the badge-label half of the A6 check
// against STATUS_META.text in web/app.js.
func TestMonitorTextMatchesWebPanel(t *testing.T) {
	block := jsConstBlock(appJS(t), "STATUS_META")
	if block == "" {
		t.Fatal("STATUS_META table not found in web/app.js")
	}
	re := regexp.MustCompile(`([a-z_]+):\s*\{\s*text:\s*"([^"]*)"`)
	js := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		js[m[1]] = m[2]
	}
	if len(js) != len(monitorStatuses) {
		t.Errorf("app.js STATUS_META has %d entries, want %d (a monitor status was added on one side only)", len(js), len(monitorStatuses))
	}
	for _, s := range monitorStatuses {
		want, ok := js[s]
		if !ok {
			t.Errorf("app.js STATUS_META missing status %q", s)
			continue
		}
		if got := MonitorText(s); got != want {
			t.Errorf("MonitorText(%q) = %q, app.js = %q — tables drifted", s, got, want)
		}
	}
}
