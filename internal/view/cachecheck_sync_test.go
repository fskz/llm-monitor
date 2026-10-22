package view

import (
	"regexp"
	"testing"

	"llm-monitor/internal/cachecheck"
)

/* ---------- web/app.js cache-check sync (task 10-22-cache-check) ----------
 *
 * The cache-check twin of the STATUS_TEXT lock in status_test.go: the JS
 * CACHE_VERDICT_TEXT table and the CACHE_VERDICT_NONE empty-state constant
 * in web/app.js are copies of the Go owners (CacheCheckVerdictText /
 * CacheCheckVerdictNone). A verdict added or relabeled on either side
 * without the other fails here.
 */

// cacheCheckVerdicts is the owner package's own enumeration: a verdict added
// there without a view label — or without the JS copy — fails this test.
var cacheCheckVerdicts = cachecheck.AllVerdicts()

// TestCacheCheckVerdictTextMatchesWebPanel checks every entry of web/app.js
// CACHE_VERDICT_TEXT against view.CacheCheckVerdictText and that the table
// covers exactly the four verdicts.
func TestCacheCheckVerdictTextMatchesWebPanel(t *testing.T) {
	block := jsConstBlock(appJS(t), "CACHE_VERDICT_TEXT")
	if block == "" {
		t.Fatal("CACHE_VERDICT_TEXT table not found in web/app.js")
	}
	re := regexp.MustCompile(`([a-z_]+):\s*"([^"]*)"`)
	js := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(block, -1) {
		js[m[1]] = m[2]
	}
	if len(js) != len(cacheCheckVerdicts) {
		t.Errorf("app.js CACHE_VERDICT_TEXT has %d entries, want %d (a verdict was added on one side only)", len(js), len(cacheCheckVerdicts))
	}
	for _, s := range cacheCheckVerdicts {
		want, ok := js[s]
		if !ok {
			t.Errorf("app.js CACHE_VERDICT_TEXT missing verdict %q", s)
			continue
		}
		if got := CacheCheckVerdictText(s); got != want {
			t.Errorf("CacheCheckVerdictText(%q) = %q, app.js = %q — tables drifted", s, got, want)
		}
	}
}

// TestCacheCheckVerdictNoneMatchesWebPanel pins the 未检测 empty-state
// constant: the unchecked card must render the same placeholder as the Go
// view layer (CacheCheckViewOf's VerdictLabel).
func TestCacheCheckVerdictNoneMatchesWebPanel(t *testing.T) {
	src := appJS(t)
	m := regexp.MustCompile(`CACHE_VERDICT_NONE\s*=\s*"([^"]*)"`).FindStringSubmatch(src)
	if m == nil {
		t.Fatal("CACHE_VERDICT_NONE constant not found in web/app.js")
	}
	if m[1] != CacheCheckVerdictNone {
		t.Errorf("app.js CACHE_VERDICT_NONE = %q, want view.CacheCheckVerdictNone %q", m[1], CacheCheckVerdictNone)
	}
}
