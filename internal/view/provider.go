package view

import (
	"strings"

	"llm-monitor/internal/store"
)

// DefaultPrompt is the probe prompt pre-filled for new providers
// (REQUIREMENTS.md §4.1). The panel pre-fills its form with it; validation
// is strict instead of defaulting numeric fields, so a client that omits a
// numeric field gets an explicit error rather than a silent configuration
// it never asked for. The prompt is the only defaulted field.
const DefaultPrompt = "请回复一个词：pong（这是一条连通性探测消息，请简短回复）"

// ProviderForm is the editable configuration of one provider as submitted
// by the panel forms, the TUI forms and the report pipeline alike. JSON
// tags are the POST/PUT /api/providers wire contract (web/app.js); APIKey
// is deliberately NOT part of the form — the HTTP layer keeps it separate
// as *string so the three PUT semantics (absent = keep, ""/null = clear,
// non-empty = replace, REQUIREMENTS.md §5.4) can be told apart.
type ProviderForm struct {
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	Prompt        string `json:"prompt"`
	MaxTokens     int    `json:"max_tokens"`
	IntervalSec   int    `json:"interval_sec"`
	TimeoutSec    int    `json:"timeout_sec"`
	TTFTTimeoutMs int    `json:"ttft_timeout_ms"`
	TTFTSlowMs    int    `json:"ttft_slow_ms"`
	Enabled       bool   `json:"enabled"`
	IncludeUsage  bool   `json:"include_usage"`
}

// Prepare applies the pre-validation normalization: the base URL is
// whitespace-trimmed (a padded " https://…" must not fail the prefix check)
// and an empty prompt falls back to DefaultPrompt.
func (f *ProviderForm) Prepare() {
	f.BaseURL = strings.TrimSpace(f.BaseURL)
	if strings.TrimSpace(f.Prompt) == "" {
		f.Prompt = DefaultPrompt
	}
}

// Validate mirrors the panel-side checks (web/app.js validateProvider) and
// returns the first violated rule's Chinese message, or "" when the form is
// sound. The server maps a non-empty message to HTTP 400; the TUI form
// shows it inline — same rules, same words, one owner.
func (f ProviderForm) Validate() string {
	switch {
	case strings.TrimSpace(f.Name) == "":
		return "名称不能为空"
	case !strings.HasPrefix(f.BaseURL, "http://") && !strings.HasPrefix(f.BaseURL, "https://"):
		return "Base URL 必须以 http:// 或 https:// 开头（API 前缀，如 https://example.com/v1）"
	case strings.TrimSpace(f.Model) == "":
		return "模型不能为空"
	case f.MaxTokens <= 0:
		return "max_tokens 必须大于 0"
	case f.TimeoutSec <= 0:
		return "总超时必须大于 0"
	case f.IntervalSec != 0 && f.IntervalSec < 60:
		return "探测间隔为 0（仅手动）或至少 1 分钟"
	case !(f.TTFTSlowMs > 0 && f.TTFTSlowMs < f.TTFTTimeoutMs):
		return "需要 0 < 慢阈值 < 首内容超时"
	case f.TTFTTimeoutMs > f.TimeoutSec*1000:
		return "首内容超时不能大于总超时"
	}
	return ""
}

// ToProvider materializes the form as a store.Provider. key carries the
// resolved API key after the PUT semantics (keep/clear/replace); the base
// URL is stored normalized (no trailing slash, go-conventions.md).
func (f ProviderForm) ToProvider(key string) store.Provider {
	return store.Provider{
		Name:          strings.TrimSpace(f.Name),
		BaseURL:       strings.TrimRight(strings.TrimSpace(f.BaseURL), "/"),
		APIKey:        key,
		Model:         strings.TrimSpace(f.Model),
		Prompt:        f.Prompt,
		MaxTokens:     f.MaxTokens,
		IntervalSec:   f.IntervalSec,
		TimeoutSec:    f.TimeoutSec,
		TTFTTimeoutMs: f.TTFTTimeoutMs,
		TTFTSlowMs:    f.TTFTSlowMs,
		Enabled:       f.Enabled,
		IncludeUsage:  f.IncludeUsage,
	}
}
