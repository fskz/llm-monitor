package view

import (
	"strings"
	"testing"
)

// validForm is the baseline every case below starts from.
func validForm() ProviderForm {
	return ProviderForm{
		Name:          "p",
		BaseURL:       "https://api.example.com/v1",
		Model:         "m",
		MaxTokens:     128,
		IntervalSec:   300,
		TimeoutSec:    60,
		TTFTTimeoutMs: 10000,
		TTFTSlowMs:    2000,
		Enabled:       true,
	}
}

// TestValidateMessages asserts the exact Chinese 400 messages rule by rule —
// the HTTP layer maps a non-empty result to 400 verbatim, so these strings
// are the wire contract (web/app.js surfaces them to the user).
func TestValidateMessages(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*ProviderForm)
		want string
	}{
		{"empty name", func(f *ProviderForm) { f.Name = "  " }, "名称不能为空"},
		{"ftp base_url", func(f *ProviderForm) { f.BaseURL = "ftp://a.example.com/v1" },
			"Base URL 必须以 http:// 或 https:// 开头（API 前缀，如 https://example.com/v1）"},
		{"whitespace name only", func(f *ProviderForm) { f.Name = "" }, "名称不能为空"},
		{"empty model", func(f *ProviderForm) { f.Model = "" }, "模型不能为空"},
		{"max_tokens 0", func(f *ProviderForm) { f.MaxTokens = 0 }, "max_tokens 必须大于 0"},
		{"timeout 0", func(f *ProviderForm) { f.TimeoutSec = 0 }, "总超时必须大于 0"},
		{"interval 30", func(f *ProviderForm) { f.IntervalSec = 30 }, "探测间隔为 0（仅手动）或至少 1 分钟"},
		{"slow equals ttft_timeout", func(f *ProviderForm) { f.TTFTSlowMs = f.TTFTTimeoutMs },
			"需要 0 < 慢阈值 < 首内容超时"},
		{"slow 0", func(f *ProviderForm) { f.TTFTSlowMs = 0 }, "需要 0 < 慢阈值 < 首内容超时"},
		{"ttft_timeout over total", func(f *ProviderForm) { f.TTFTTimeoutMs = 61000 },
			"首内容超时不能大于总超时"},
	}
	for _, c := range cases {
		f := validForm()
		c.mut(&f)
		if got := f.Validate(); got != c.want {
			t.Errorf("%s: Validate = %q, want %q", c.name, got, c.want)
		}
	}

	// The baseline itself and manual-only interval 0 must pass.
	if got := validForm().Validate(); got != "" {
		t.Errorf("baseline form invalid: %q", got)
	}
	f := validForm()
	f.IntervalSec = 0
	if got := f.Validate(); got != "" {
		t.Errorf("manual-only form invalid: %q", got)
	}
}

// TestPrepareNormalizes covers the pre-validation normalization: trimmed
// base URL and the prompt default.
func TestPrepareNormalizes(t *testing.T) {
	f := validForm()
	f.BaseURL = "  https://api.example.com/v1  "
	f.Prompt = "   "
	f.Prepare()
	if f.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url = %q, want trimmed", f.BaseURL)
	}
	if f.Prompt != DefaultPrompt {
		t.Errorf("prompt = %q, want the default", f.Prompt)
	}
	// An explicit prompt survives (whitespace-only counts as absent).
	f2 := validForm()
	f2.Prompt = " hi "
	f2.Prepare()
	if f2.Prompt != " hi " {
		t.Errorf("prompt = %q, want untouched", f2.Prompt)
	}
}

// TestToProvider checks the stored-shape normalization.
func TestToProvider(t *testing.T) {
	f := validForm()
	f.Name = "  p  "
	f.BaseURL = "https://api.example.com/v1/"
	p := f.ToProvider("sk-key")
	if p.Name != "p" {
		t.Errorf("name = %q, want trimmed", p.Name)
	}
	if p.BaseURL != "https://api.example.com/v1" {
		t.Errorf("base_url = %q, want trailing slash trimmed", p.BaseURL)
	}
	if p.APIKey != "sk-key" {
		t.Errorf("api key = %q, want passed through", p.APIKey)
	}
	if p.Prompt != f.Prompt {
		t.Errorf("prompt = %q, want %q", p.Prompt, f.Prompt)
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abc12345", "********"},              // <=8 chars: fully masked
		{"sk-1234567890abcd", "sk-1****abcd"}, // first4+****+last4
		{"123456789", "1234****6789"},         // exactly 9 chars
	}
	for _, c := range cases {
		if got := maskKey(c.in); got != c.want {
			t.Errorf("maskKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The mask must never contain the raw key.
	if got := maskKey("sk-1234567890abcd"); strings.Contains(got, "1234567890") {
		t.Errorf("mask %q leaks key middle", got)
	}
	// Note: maskKey slices bytes, not runes (pre-existing behavior, moved
	// verbatim from the server), so a multi-byte key can split mid-rune.
	// Keys in practice are ASCII.
}
