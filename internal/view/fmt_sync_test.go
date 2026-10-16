package view

import (
	"regexp"
	"strings"
	"testing"
)

/* ---------- web/app.js formatting sync (review-fixpack-2 F14/F15) ----------
 *
 * Go owns the formatting semantics (report.go pctOrDash/FmtTPS/FmtTPOT/
 * FmtFlip/FmtMs, provider.go DefaultPrompt); web/app.js keeps JS copies.
 * There is no JS runtime in the Go test harness, so the lock has two halves:
 *
 *  1. Go-side golden values pin the OWNER's semantics (a semantic change to
 *     the Go formatters fails here first).
 *  2. The JS function SOURCES are pinned verbatim (whitespace-normalized).
 *     Editing a JS copy without mirroring the Go owner — or vice versa —
 *     fails the source pin and forces a conscious, mirrored update.
 *
 * Same discipline as the STATUS_TEXT dual-table lock in status_test.go; the
 * fmt family had already drifted once (fmtTPOT lacked the <=0 guard).
 */

// collapse reduces a source snippet to a whitespace-insensitive form so pure
// reformatting (line breaks, indentation) does not break the pin, while any
// token-level edit still does.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// jsFuncSource extracts the `function <name>(…) { … }` source from app.js
// (first match). One-liner helpers end with "; }" on their own line,
// multi-line ones with "}" at line start — the pattern stops at whichever
// terminator comes first; none of the pinned helpers nests a function.
func jsFuncSource(t *testing.T, src, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?s)function ` + name + `\(.*?(\n\}|; \})`)
	m := re.FindString(src)
	if m == "" {
		t.Fatalf("function %s not found in web/app.js", name)
	}
	return m
}

// goGolden pins the Go owners' outputs. Every row doubles as the spec of the
// JS copies: the pinned JS sources below were written to produce exactly
// these strings for these inputs.
func TestFmtGoGoldenValues(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	i := func(v int64) *int64 { return &v }
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"pct nil", pctOrDash(nil), "—"},
		{"pct 12.3456", pctOrDash(f(12.3456)), "12.35%"},
		{"tps nil", FmtTPS(nil), "—"},
		{"tps 57.549", FmtTPS(f(57.549)), "57.5 tok/s"},
		{"tpot nil", FmtTPOT(nil), "—"},
		{"tpot zero", FmtTPOT(f(0)), "—"},
		{"tpot negative", FmtTPOT(f(-3)), "—"},
		{"tpot 57.5", FmtTPOT(f(57.5)), "17.4 ms/tok"},
		{"flip nil", FmtFlip(nil), "—"},
		{"flip 0.049", FmtFlip(f(0.049)), "4.9%（稳定）"},
		{"flip 0.05 boundary", FmtFlip(f(0.05)), "5.0%（波动）"},
		{"flip 0.2 boundary", FmtFlip(f(0.2)), "20.0%（波动）"},
		{"flip 0.2001", FmtFlip(f(0.2001)), "20.0%（频繁翻转）"},
		{"ms nil", FmtMs(nil), "—"},
		{"ms 999", FmtMs(i(999)), "999 ms"},
		{"ms 1000", FmtMs(i(1000)), "1.00 s"},
		{"ms 1050", FmtMs(i(1050)), "1.05 s"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestFmtJsSourcesPinned pins the JS copies verbatim (whitespace-normalized).
// The pinned sources are the implementations that match the golden table
// above; any divergence — including the historical fmtTPOT missing guard —
// fails here.
func TestFmtJsSourcesPinned(t *testing.T) {
	src := appJS(t)
	for name, golden := range map[string]string{
		"fmtPct":  `function fmtPct(v) { return v == null ? "—" : v.toFixed(2) + "%"; }`,
		"fmtTPS":  `function fmtTPS(v) { return v == null ? "—" : v.toFixed(1) + " tok/s"; }`,
		"fmtTPOT": `function fmtTPOT(decodeTps) { return decodeTps == null || decodeTps <= 0 ? "—" : (1000 / decodeTps).toFixed(1) + " ms/tok"; }`,
		"fmtMs": `function fmtMs(v) {
		  if (v == null) return "—";
		  if (v < 1000) return v + " ms";
		  return (v / 1000).toFixed(2) + " s";
		}`,
		"fmtFlip": "function fmtFlip(r) {\n" +
			"  if (r == null) return \"—\";\n" +
			"  const pct = (r * 100).toFixed(1) + \"%\";\n" +
			"  const band = r < 0.05 ? \"稳定\" : r <= 0.2 ? \"波动\" : \"频繁翻转\";\n" +
			"  return `${pct}（${band}）`;\n" +
			"}",
	} {
		js := collapse(jsFuncSource(t, src, name))
		want := collapse(golden)
		if js != want {
			t.Errorf("web/app.js %s drifted from the pinned source:\n got: %s\nwant: %s\nMirror the Go owner (internal/view/report.go) or update BOTH sides consciously.", name, js, want)
		}
	}
}

// TestDefaultPromptMatchesWebPanel pins F15: the panel's defaultPrompt must
// equal view.DefaultPrompt — web-created and TUI/API-created objects must
// probe with the same request body.
func TestDefaultPromptMatchesWebPanel(t *testing.T) {
	src := appJS(t)
	re := regexp.MustCompile(`(?s)function defaultPrompt\(\).*?\n`)
	m := re.FindString(src)
	if m == "" {
		t.Fatal("defaultPrompt not found in web/app.js")
	}
	q := regexp.MustCompile(`"([^"]+)"`).FindStringSubmatch(m)
	if q == nil || q[1] != DefaultPrompt {
		t.Errorf("web/app.js defaultPrompt = %q, want view.DefaultPrompt %q", q, DefaultPrompt)
	}
}
