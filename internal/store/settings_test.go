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

// review-fixpack-2 F6: a settings section that is absent entirely, or whose
// defaults object omits fields, loads with the built-in defaults — never
// enabled=false/interval 0 — while explicitly written false/0 survive
// verbatim (manual-only default stays legitimate). F3: a corrupt negative
// interval falls back at read time.
func TestSettingsAbsentSectionAndPartialDefaults(t *testing.T) {
	dir := t.TempDir()
	// Legacy file: providers present, no settings key at all.
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"version":1,"next_id":2,"providers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New legacy: %v", err)
	}
	def := DefaultSettings()
	got := st.GetSettings()
	if !got.Defaults.Enabled || got.Defaults.IntervalSec != def.Defaults.IntervalSec {
		t.Fatalf("absent section defaults = %+v, want enabled=true interval=%d",
			got.Defaults, def.Defaults.IntervalSec)
	}

	// Partial section: explicit interval 0 / enabled false must survive;
	// every other absent field falls back to the default.
	if err := os.WriteFile(filepath.Join(dir, "config.json"),
		[]byte(`{"version":1,"next_id":2,"providers":[],"settings":{"defaults":{"interval_sec":0,"enabled":false}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st2, err := New(dir)
	if err != nil {
		t.Fatalf("New partial: %v", err)
	}
	got2 := st2.GetSettings()
	if got2.Defaults.IntervalSec != 0 || got2.Defaults.Enabled {
		t.Fatalf("explicit interval 0 / enabled false not preserved: %+v", got2.Defaults)
	}
	if got2.Defaults.MaxTokens != def.Defaults.MaxTokens || got2.Defaults.TimeoutSec != def.Defaults.TimeoutSec ||
		got2.Defaults.TTFTTimeoutMs != def.Defaults.TTFTTimeoutMs || got2.Defaults.TTFTSlowMs != def.Defaults.TTFTSlowMs {
		t.Fatalf("absent fields must fall back to defaults: %+v", got2.Defaults)
	}

	// Corrupt negative interval: sanitized at read time to the default.
	bad := DefaultSettings()
	bad.Defaults.IntervalSec = -5
	if err := st2.SaveSettings(bad); err != nil {
		t.Fatalf("SaveSettings bad: %v", err)
	}
	if got := st2.GetSettings().Defaults.IntervalSec; got != def.Defaults.IntervalSec {
		t.Fatalf("negative interval = %d, want fallback %d", got, def.Defaults.IntervalSec)
	}
}
