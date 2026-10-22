package view

import (
	"encoding/json"
	"strings"
	"testing"

	"llm-monitor/internal/store"
)

func ccProvider() store.Provider {
	return store.Provider{ID: 7, Name: "p", BaseURL: "http://127.0.0.1:9/v1", Model: "m", Revision: 2}
}

// The label table is the single Go source of the four verdict labels; stray
// machine values pass through unchanged (StatusText pattern).
func TestCacheCheckVerdictText(t *testing.T) {
	cases := map[string]string{
		"supported":   "已开启",
		"unsupported": "未开启",
		"unknown":     "无法判定",
		"failed":      "检测失败",
	}
	for verdict, want := range cases {
		if got := CacheCheckVerdictText(verdict); got != want {
			t.Errorf("CacheCheckVerdictText(%q) = %q, want %q", verdict, got, want)
		}
	}
	if got := CacheCheckVerdictText("weird"); got != "weird" {
		t.Errorf("stray verdict must pass through, got %q", got)
	}
}

// The empty state ("未检测"): no verdict, no record data — and hops still
// serializes as [] rather than null (JSON empty-collection discipline).
func TestCacheCheckViewOfEmpty(t *testing.T) {
	v := CacheCheckViewOf(ccProvider(), store.CacheCheckRecord{}, false)
	if v.ProviderID != 7 || v.Verdict != "" || v.VerdictLabel != CacheCheckVerdictNone {
		t.Fatalf("empty view = %+v", v)
	}
	if v.CheckedAt != 0 || v.Stale || v.StaleHint != "" || v.Evidence != "" || v.EvidenceText != "" {
		t.Fatalf("empty view must carry no record data: %+v", v)
	}
	if v.Hops == nil || len(v.Hops) != 0 {
		t.Fatalf("empty hops = %#v, want non-nil empty slice", v.Hops)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"hops":[]`) {
		t.Errorf("empty state JSON = %s, want \"hops\":[]", b)
	}
}

// A real record assembles verdict/label, machine evidence, both hops with
// their Chinese status labels and the observed numbers.
func TestCacheCheckViewOfRecord(t *testing.T) {
	p := ccProvider()
	zero, big := int64(0), int64(2048)
	prompt := 2048
	rec := store.CacheCheckRecord{
		Verdict:   "supported",
		Evidence:  "cached_tokens hop1=0, hop2=2048",
		CheckedAt: 1700000000000,
		Revision:  p.Revision,
		Hops: []store.CacheCheckHop{
			{Status: "ok", TTFTMs: i64p(812), TotalMs: 900, PromptTokens: &prompt, CachedTokens: &zero},
			{Status: "ok", TTFTMs: i64p(210), TotalMs: 300, PromptTokens: &prompt, CachedTokens: &big},
		},
	}
	v := CacheCheckViewOf(p, rec, true)
	if v.ProviderID != p.ID || v.Verdict != "supported" || v.VerdictLabel != "已开启" {
		t.Fatalf("view head = %d %q/%q", v.ProviderID, v.Verdict, v.VerdictLabel)
	}
	if v.Stale || v.StaleHint != "" {
		t.Errorf("matching revision: stale = %v hint = %q, want false/empty", v.Stale, v.StaleHint)
	}
	if v.CheckedAt != rec.CheckedAt {
		t.Errorf("checked_at = %d, want %d", v.CheckedAt, rec.CheckedAt)
	}
	if v.Evidence != rec.Evidence {
		t.Errorf("evidence = %q, want the machine text passed through", v.Evidence)
	}
	if len(v.Hops) != 2 {
		t.Fatalf("hops = %d, want 2", len(v.Hops))
	}
	h1, h2 := v.Hops[0], v.Hops[1]
	if h1.Status != "ok" || h1.StatusLabel != "成功" {
		t.Errorf("hop1 status = %q/%q, want ok/成功", h1.Status, h1.StatusLabel)
	}
	if h1.CachedTokens == nil || *h1.CachedTokens != 0 {
		t.Errorf("hop1 cached_tokens = %v, want observed 0", h1.CachedTokens)
	}
	if h2.CachedTokens == nil || *h2.CachedTokens != 2048 {
		t.Errorf("hop2 cached_tokens = %v, want observed 2048", h2.CachedTokens)
	}
	if !strings.Contains(v.EvidenceText, "2048") {
		t.Errorf("evidence_text = %q, want the hop numbers", v.EvidenceText)
	}
}

// A record from an older revision is flagged stale with the re-check hint;
// the verdict itself stays readable alongside the flag.
func TestCacheCheckViewOfStale(t *testing.T) {
	rec := store.CacheCheckRecord{
		Verdict: "unsupported", CheckedAt: 1, Revision: 1,
		Hops: []store.CacheCheckHop{{Status: "ok", TotalMs: 100}, {Status: "ok", TotalMs: 90}},
	}
	v := CacheCheckViewOf(ccProvider(), rec, true) // provider revision 2 ≠ 1
	if !v.Stale || v.StaleHint != "配置已变更，建议重新检测" {
		t.Fatalf("stale = %v hint = %q, want stale + hint", v.Stale, v.StaleHint)
	}
	if v.VerdictLabel != "未开启" {
		t.Errorf("verdict_label = %q, want 未开启 (record kept, flagged)", v.VerdictLabel)
	}
}

// Null vs 0 discipline: unobserved numerics serialize JSON null, never 0;
// total_ms (always observed) stays a plain number.
func TestCacheCheckViewNullDiscipline(t *testing.T) {
	rec := store.CacheCheckRecord{
		Verdict: "unknown", CheckedAt: 5, Revision: 1,
		Hops: []store.CacheCheckHop{{Status: "ok", TotalMs: 400}, {Status: "ok", TotalMs: 350}},
	}
	b, err := json.Marshal(CacheCheckViewOf(store.Provider{Revision: 1}, rec, true))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, wantNull := range []string{`"ttft_ms":null`, `"cached_tokens":null`, `"prompt_tokens":null`} {
		if !strings.Contains(s, wantNull) {
			t.Errorf("JSON missing %s: %s", wantNull, s)
		}
	}
	if !strings.Contains(s, `"total_ms":400`) {
		t.Errorf("total_ms (always observed) must serialize as a number: %s", s)
	}
}

// The human evidence summary per verdict: unknown carries the TTFT numbers
// and the reference-only wording; failed names the hop with its Chinese
// status label and detail; unsupported states the zero evidence without the
// reference-only wording.
func TestCacheCheckEvidenceText(t *testing.T) {
	v := CacheCheckViewOf(store.Provider{}, store.CacheCheckRecord{
		Verdict: "unknown",
		Hops: []store.CacheCheckHop{
			{Status: "ok", TTFTMs: i64p(900), TotalMs: 1000},
			{Status: "ok", TTFTMs: i64p(220), TotalMs: 320},
		},
	}, true)
	for _, frag := range []string{"无法判定", "缺失", "900ms", "220ms", "TTFT 仅供参考"} {
		if !strings.Contains(v.EvidenceText, frag) {
			t.Errorf("unknown evidence_text %q must contain %q", v.EvidenceText, frag)
		}
	}

	v = CacheCheckViewOf(store.Provider{}, store.CacheCheckRecord{
		Verdict: "failed", FailedHop: 2,
		Hops: []store.CacheCheckHop{
			{Status: "ok"}, {Status: "timeout_ttft", ErrDetail: "timed out"},
		},
	}, true)
	for _, frag := range []string{"第 2 跳", "超时（首内容）", "timed out"} {
		if !strings.Contains(v.EvidenceText, frag) {
			t.Errorf("failed evidence_text %q must contain %q", v.EvidenceText, frag)
		}
	}

	v = CacheCheckViewOf(store.Provider{}, store.CacheCheckRecord{
		Verdict: "unsupported",
		Hops: []store.CacheCheckHop{
			{Status: "ok", CachedTokens: i64p(0)}, {Status: "ok", CachedTokens: i64p(0)},
		},
	}, true)
	if !strings.Contains(v.EvidenceText, "未检测到前缀缓存") {
		t.Errorf("unsupported evidence_text = %q, want the zero-evidence wording", v.EvidenceText)
	}
	if strings.Contains(v.EvidenceText, "仅供参考") {
		t.Errorf("unsupported evidence_text must not carry the unknown-only wording: %q", v.EvidenceText)
	}
}
