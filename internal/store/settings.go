package store

import "encoding/json"

// Settings is the tool-level configuration (task 10-10-settings-pack),
// persisted as the "settings" section of config.json. Absent section or
// fields fall back to the built-in defaults — old config files keep
// loading unchanged. Sensitive fields never live here.
type Settings struct {
	// Defaults prefill the NEW-provider forms (web + TUI). Only the
	// per-object-tedious numeric/bool fields are prefilled; name,
	// base_url and model stay empty — every object differs there.
	Defaults SettingsDefaults `json:"defaults"`
	// StreakAlert is the consecutive-failure count at which the overview
	// badge and card highlight turn on. ≥2 (1 is indistinguishable from a
	// single failure — no "真挂了" signal). Local visual only, no push.
	StreakAlert int `json:"streak_alert"`
	// ReportDir overrides the TUI report export directory; empty = the
	// default <data>/reports/. The web report download is unaffected.
	ReportDir string `json:"report_dir"`
}

// SettingsDefaults mirrors the numeric/bool subset of view.ProviderForm.
// The local struct keeps store free of a view import (dependency
// direction: view → store).
type SettingsDefaults struct {
	MaxTokens     int  `json:"max_tokens"`
	IntervalSec   int  `json:"interval_sec"`
	TimeoutSec    int  `json:"timeout_sec"`
	TTFTTimeoutMs int  `json:"ttft_timeout_ms"`
	TTFTSlowMs    int  `json:"ttft_slow_ms"`
	Enabled       bool `json:"enabled"`
	IncludeUsage  bool `json:"include_usage"`
}

// UnmarshalJSON applies per-field defaults for ABSENT fields only, so a
// partially written settings section behaves like the documented "absent
// fields fall back to the built-in defaults" — while explicitly written
// values survive verbatim, including enabled:false and interval_sec:0
// (manual-only), which must not be defaulted away. Invalid values that ARE
// present (e.g. max_tokens:-3) stay as-is here; settingsLocked sanitizes
// them at read time.
func (d *SettingsDefaults) UnmarshalJSON(b []byte) error {
	var raw struct {
		MaxTokens     *int  `json:"max_tokens"`
		IntervalSec   *int  `json:"interval_sec"`
		TimeoutSec    *int  `json:"timeout_sec"`
		TTFTTimeoutMs *int  `json:"ttft_timeout_ms"`
		TTFTSlowMs    *int  `json:"ttft_slow_ms"`
		Enabled       *bool `json:"enabled"`
		IncludeUsage  *bool `json:"include_usage"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	def := DefaultSettings().Defaults
	d.MaxTokens = orDefault(raw.MaxTokens, def.MaxTokens)
	d.IntervalSec = orDefault(raw.IntervalSec, def.IntervalSec)
	d.TimeoutSec = orDefault(raw.TimeoutSec, def.TimeoutSec)
	d.TTFTTimeoutMs = orDefault(raw.TTFTTimeoutMs, def.TTFTTimeoutMs)
	d.TTFTSlowMs = orDefault(raw.TTFTSlowMs, def.TTFTSlowMs)
	d.Enabled = orDefault(raw.Enabled, def.Enabled)
	d.IncludeUsage = orDefault(raw.IncludeUsage, def.IncludeUsage)
	return nil
}

// orDefault dereferences p when the field was present, else returns def.
func orDefault[T any](p *T, def T) T {
	if p != nil {
		return *p
	}
	return def
}

// DefaultSettings returns the built-in defaults (matching the historical
// hard-coded new-provider form values).
func DefaultSettings() Settings {
	return Settings{
		Defaults: SettingsDefaults{
			MaxTokens:     128,
			IntervalSec:   300,
			TimeoutSec:    60,
			TTFTTimeoutMs: 15000,
			TTFTSlowMs:    3000,
			Enabled:       true,
		},
		StreakAlert: 2,
	}
}

// GetSettings returns the effective settings with invalid values fallen
// back to defaults (a corrupt section must never block startup).
func (s *Store) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsLocked()
}

// settingsLocked is GetSettings without the lock (callers holding mu).
func (s *Store) settingsLocked() Settings {
	st := s.config.Settings
	def := DefaultSettings()
	if st.StreakAlert < 2 {
		st.StreakAlert = def.StreakAlert
	}
	d := &st.Defaults
	if d.MaxTokens <= 0 {
		d.MaxTokens = def.Defaults.MaxTokens
	}
	if d.TimeoutSec <= 0 {
		d.TimeoutSec = def.Defaults.TimeoutSec
	}
	if d.TTFTTimeoutMs <= 0 {
		d.TTFTTimeoutMs = def.Defaults.TTFTTimeoutMs
	}
	if !(d.TTFTSlowMs > 0 && d.TTFTSlowMs < d.TTFTTimeoutMs) {
		d.TTFTSlowMs = def.Defaults.TTFTSlowMs
	}
	// IntervalSec keeps explicit 0 (manual-only is a legitimate default);
	// negative values are corrupt and fall back.
	if d.IntervalSec < 0 {
		d.IntervalSec = def.Defaults.IntervalSec
	}
	return st
}

// SaveSettings persists the settings atomically (same lock, same
// config.json write path as provider mutations).
func (s *Store) SaveSettings(st Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config.Settings = st
	return s.saveConfigLocked()
}
