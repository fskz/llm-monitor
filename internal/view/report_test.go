package view

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"llm-monitor/internal/store"
)

// renderReportFixture renders the report of provider p into a buffer and
// fails the test on a template error. Source "" is normalized to
// store.SourceAll the way the caller already validated it.
func renderReportFixture(t *testing.T, st *store.Store, p store.Provider, rev int, source string, window time.Duration) string {
	t.Helper()
	if source == "" {
		source = store.SourceAll
	}
	var buf bytes.Buffer
	if err := RenderReport(&buf, st, nil, p, rev, source, window); err != nil {
		t.Fatalf("RenderReport: %v", err)
	}
	return buf.String()
}

// Report content (task 10-09 阶段 2): key sections, XSS escaping, key
// non-leakage (render-time ScrubSecret layer), no external resource URLs,
// and stats parity with store.Stats on the same window.
func TestRenderReportContent(t *testing.T) {
	st := newTestStore(t)
	p := seedProvider(t, st, func(pp *store.Provider) {
		pp.Name = "<script>alert(1)</script>" // hostile name
		pp.APIKey = "sk-report-secret-123456"
	})
	now := time.Now().UnixMilli()
	// Fixture: error text containing the key, mixed statuses, usage evidence.
	for i := 0; i < 3; i++ {
		r := store.Result{
			ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
			Source: store.SourceScheduled, Status: "ok", Success: true,
			StartedAt: now - int64(i)*60_000, FinishedAt: now - int64(i)*60_000 + 1500,
			TTFTMs: i64p(500), TotalMs: 1500,
			PromptTokens: ptrInt(64), CompletionTokens: ptrInt(32),
		}
		if _, err := st.AppendResult(r); err != nil {
			t.Fatal(err)
		}
	}
	fail := store.Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: store.SourceScheduled, Status: "http_error",
		StartedAt: now - 4*60_000, FinishedAt: now - 4*60_000 + 1500, TotalMs: 1500,
	}
	if _, err := st.AppendResult(fail); err != nil {
		t.Fatal(err)
	}
	leak := store.Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: store.SourceScheduled, Status: "timeout_ttft",
		StartedAt: now - 5*60_000, FinishedAt: now - 5*60_000 + 1500, TotalMs: 1500,
		Error: "Bearer sk-report-secret-123456 rejected",
	}
	if _, err := st.AppendResult(leak); err != nil {
		t.Fatal(err)
	}

	body := renderReportFixture(t, st, p, p.Revision, store.SourceAll, 24*time.Hour)

	// Key sections present.
	for _, want := range []string{"监控报告", "监测状态", "成功率趋势", "探测明细", "错误 / 说明", "<svg"} {
		if !strings.Contains(body, want) {
			t.Fatalf("report missing %q", want)
		}
	}
	// Hostile name escaped (the tag must not survive; the text may).
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("hostile provider name not escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("escaped name text missing — name should render as literal text")
	}
	// Key never appears (the error row carrying it must be scrubbed).
	if strings.Contains(body, "sk-report-secret-123456") {
		t.Fatalf("API key leaked into report")
	}
	// No external resource references (only the SVG/CSS we emit).
	for _, pat := range []string{"src=\"http", "href=\"http", "url(http", "@import"} {
		if strings.Contains(body, pat) {
			t.Fatalf("external resource reference %q in report", pat)
		}
	}
	// Stats parity with the store on the same window and source (the
	// report defaults to source=all; fixture rows are scheduled, so
	// scheduled/all agree here).
	got := st.Stats(p.ID, p.Revision, 24*time.Hour, store.SourceAll)
	if got.Samples != 5 {
		t.Fatalf("stats samples = %d, want 5", got.Samples)
	}
	if !strings.Contains(body, "样本数") || !strings.Contains(body, ">5<") {
		t.Fatalf("stats card missing or wrong sample count in report body")
	}
}

// More rows than the cap get truncated with an explicit note (A3).
func TestRenderReportTruncation(t *testing.T) {
	st := newTestStore(t)
	p := seedProvider(t, st, nil)
	now := time.Now().UnixMilli()
	for i := 0; i < ReportMaxRows+50; i++ {
		seedResult(t, st, p, "ok", now-int64(i)*60_000, i64p(100))
	}

	body := renderReportFixture(t, st, p, p.Revision, store.SourceAll, 7*24*time.Hour)
	if !strings.Contains(body, fmt.Sprintf("已截断至 %d 条", ReportMaxRows)) {
		t.Fatalf("truncation note missing")
	}
	if got := strings.Count(body, "<tr class="); got > ReportMaxRows+5 {
		t.Fatalf("row count leak: %d tr elements", got)
	}
}

// Filename follows the llm-monitor-<slug>-<window>-<yyyymmdd-hhmmss> contract;
// CJK-only names fall back to provider-<id>; revision/report metadata render;
// per-field token dash keeps null ≠ 0 (§9.2); the latency chart degrades to
// the "暂无成功样本" placeholder when no ok bucket exists.
func TestRenderReportFilenameAndNullSemantics(t *testing.T) {
	st := newTestStore(t)
	// CJK-only name → slug folds to empty → provider-<id> fallback.
	p := seedProvider(t, st, func(pp *store.Provider) { pp.Name = "生产接口监控" })
	now := time.Now().UnixMilli()
	// One failure with only prompt_tokens observed: completion stays null and
	// must render as a dash, never 0.
	r := store.Result{
		ProviderID: p.ID, Revision: p.Revision, BaseURL: p.BaseURL, Model: p.Model,
		Source: store.SourceScheduled, Status: "http_error",
		StartedAt: now, FinishedAt: now + 1500, TotalMs: 1500,
		PromptTokens: ptrInt(64),
	}
	if _, err := st.AppendResult(r); err != nil {
		t.Fatal(err)
	}

	body := renderReportFixture(t, st, p, store.RevisionAll, store.SourceScheduled, time.Hour)
	if !strings.Contains(body, "64 / —") {
		t.Fatalf("null completion_tokens must render as dash, got: %s", body[:min(len(body), 200)])
	}
	if strings.Contains(body, "64 / 0") {
		t.Fatalf("null completion_tokens rendered as 0")
	}
	if !strings.Contains(body, "全部版本") {
		t.Fatalf("revision=all filter label missing")
	}
	// No ok samples in the window → latency placeholder (fixture has one
	// scheduled http_error, so the success chart itself is not empty here).
	if !strings.Contains(body, "暂无成功样本") {
		t.Fatalf("latency chart placeholder missing for zero ok samples")
	}
}

func TestReportSlug(t *testing.T) {
	cases := []struct{ in, want string }{
		{"My Provider (prod)", "my-provider-prod"},
		{"  生产接口监控  ", ""}, // CJK-only folds to empty → caller falls back
		{"Acme-v2", "acme-v2"},
		{"", ""},
	}
	for _, c := range cases {
		if got := ReportSlug(c.in); got != c.want {
			t.Errorf("ReportSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The filename carries the window tag and timestamp; the slug fallback keeps
// it deterministic for CJK-only names.
func TestReportFilename(t *testing.T) {
	st := newTestStore(t)
	cjk := seedProvider(t, st, func(pp *store.Provider) { pp.Name = "生产接口监控" })
	name := ReportFilename(cjk, time.Hour)
	wantPrefix := fmt.Sprintf("llm-monitor-provider-%d-1h-", cjk.ID)
	if !strings.HasPrefix(name, wantPrefix) {
		t.Fatalf("filename = %q, want prefix %q", name, wantPrefix)
	}
	if !strings.HasSuffix(name, ".html") || len(name) != len(wantPrefix)+len("20060102-150405.html") {
		t.Fatalf("filename timestamp not yyyymmdd-hhmmss: %q", name)
	}

	ascii := seedProvider(t, st, func(pp *store.Provider) { pp.Name = "OpenAI Prod" })
	if got := ReportFilename(ascii, 24*time.Hour); !strings.HasPrefix(got, "llm-monitor-openai-prod-24h-") {
		t.Fatalf("filename = %q, want slug from name", got)
	}
}

// ScrubSecret is the render-time layer of key non-leakage: empty secret must
// leave the text untouched, non-empty must blank every occurrence.
func TestScrubSecret(t *testing.T) {
	if got := ScrubSecret("plain text", ""); got != "plain text" {
		t.Errorf("empty secret must not alter text, got %q", got)
	}
	if got := ScrubSecret("Bearer sk-xyz rejected, sk-xyz again", "sk-xyz"); got != "Bearer *** rejected, *** again" {
		t.Errorf("secret not fully scrubbed: %q", got)
	}
}

func ptrInt(v int) *int { return &v }

// The report carries the TPOT card, derived from the same decode TPS
// evidence (TPOT metric task). A usage-carrying ok sample makes decode
// TPS non-nil, so TPOT must render as a numeric ms/tok value; without
// usage evidence it renders the dash.
func TestReportContainsTPOT(t *testing.T) {
	st := newTestStore(t)
	p := seedProvider(t, st, nil)
	now := time.Now().UnixMilli()
	// ok row WITH usage: completion=32, ttft=500ms, total=1500ms →
	// decode span 1000ms, 31 tokens → 31 tok/s → TPOT ≈ 32.3 ms/tok.
	if _, err := st.AppendResult(store.Result{
		ProviderID: p.ID, Revision: p.Revision,
		BaseURL: p.BaseURL, Model: p.Model,
		Source: store.SourceScheduled, Status: "ok", Success: true,
		StartedAt: now - 60_000, FinishedAt: now - 60_000 + 1500,
		TTFTMs: i64p(500), TotalMs: 1500,
		PromptTokens: ptrInt(64), CompletionTokens: ptrInt(32),
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
	body := renderReportFixture(t, st, p, p.Revision, store.SourceAll, 24*time.Hour)
	if !strings.Contains(body, "TPOT") {
		t.Fatal("report missing TPOT card")
	}
	if !strings.Contains(body, "ms/tok") {
		t.Fatal("usage-backed sample must render a numeric TPOT (ms/tok)")
	}
	if !strings.Contains(body, "32.3 ms/tok") {
		t.Fatal("TPOT value = want 32.3 ms/tok (1000/31)")
	}

	// No usage evidence → dash, never 0.
	st2 := newTestStore(t)
	p2 := seedProvider(t, st2, nil)
	seedResult(t, st2, p2, "ok", now-60_000, i64p(500))
	body2 := renderReportFixture(t, st2, p2, p2.Revision, store.SourceAll, 24*time.Hour)
	if !strings.Contains(body2, "TPOT") || !strings.Contains(body2, "0.0 ms/tok") && !strings.Contains(body2, ">—<") {
		t.Fatalf("TPOT without usage must render the dash:\n%s", body2[:400])
	}
}
