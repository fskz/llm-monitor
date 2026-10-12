package view

import (
	"testing"

	"llm-monitor/internal/store"
)

// ValidateSettings is the single owner of the settings rules and their
// Chinese messages (review fixpack S1); the server and TUI both call it,
// so the wording can never drift apart.
func TestValidateSettings(t *testing.T) {
	ok := store.Settings{StreakAlert: 3, Defaults: store.SettingsDefaults{MaxTokens: 128, TimeoutSec: 60, TTFTTimeoutMs: 15000, TTFTSlowMs: 3000}}
	if msg := ValidateSettings(ok); msg != "" {
		t.Fatalf("valid settings rejected: %q", msg)
	}
	if msg := ValidateSettings(store.Settings{StreakAlert: 1, Defaults: ok.Defaults}); msg != "连败提醒阈值需至少为 2" {
		t.Fatalf("streak 1 = %q", msg)
	}
	badSlow := ok
	badSlow.Defaults.TTFTSlowMs = 20000
	if msg := ValidateSettings(badSlow); msg == "" {
		t.Fatal("slow ≥ ttft must fail")
	}
	unwritable := ok
	unwritable.ReportDir = "/proc/nope/deep"
	if msg := ValidateSettings(unwritable); msg == "" {
		t.Fatal("unwritable dir must fail")
	}
}
