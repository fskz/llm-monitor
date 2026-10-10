package store

import (
	"os"
	"path/filepath"
	"testing"
)

// A1: a config.json written before the settings section exists loads with
// pure defaults and no error.
func TestSettingsLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got := st.GetSettings()
	def := DefaultSettings()
	if got.StreakAlert != def.StreakAlert || got.Defaults.MaxTokens != def.Defaults.MaxTokens || got.ReportDir != "" {
		t.Fatalf("legacy settings = %+v, want defaults", got)
	}
}

// A2: SaveSettings round-trips atomically and survives a reopen; invalid
// values read back as defaults (never zero-poisoned).
func TestSettingsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, _ := New(dir)
	want := Settings{
		Defaults:    SettingsDefaults{MaxTokens: 256, IntervalSec: 120, TimeoutSec: 90, TTFTTimeoutMs: 20000, TTFTSlowMs: 4000, Enabled: true, IncludeUsage: true},
		StreakAlert: 5,
		ReportDir:   filepath.Join(dir, "myreports"),
	}
	if err := st.SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// Persisted on disk: reopen and read.
	st2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got := st2.GetSettings()
	if got.StreakAlert != 5 || got.ReportDir != want.ReportDir || got.Defaults.MaxTokens != 256 || !got.Defaults.IncludeUsage {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}

	// Corrupt values fall back per-field without blocking startup.
	bad := want
	bad.StreakAlert = 0 // <2 → default 2
	bad.Defaults.MaxTokens = -3
	if err := st2.SaveSettings(bad); err != nil {
		t.Fatalf("SaveSettings bad: %v", err)
	}
	fixed := st2.GetSettings()
	def := DefaultSettings()
	if fixed.StreakAlert != def.StreakAlert || fixed.Defaults.MaxTokens != def.Defaults.MaxTokens {
		t.Fatalf("fallback = %+v, want defaults for bad fields", fixed)
	}
	// Good fields survive the fallback.
	if fixed.Defaults.TimeoutSec != 90 || fixed.ReportDir != want.ReportDir {
		t.Fatalf("fallback kept wrong fields: %+v", fixed)
	}
	_ = os.RemoveAll(dir)
}
