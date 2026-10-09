package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"llm-monitor/internal/store"
)

// defaults for new providers (REQUIREMENTS.md §4.1). The panel pre-fills
// its form with these; the server validates strictly instead of defaulting,
// so a client that omits a field gets an explicit 400 rather than a silent
// configuration it never asked for.
const defaultPrompt = "请回复一个词：pong（这是一条连通性探测消息，请简短回复）"

// providerPayload is the request body of POST/PUT. APIKey is *string so the
// three PUT semantics can be told apart: absent (nil) = keep the stored
// key, explicit "" or null = clear it, non-empty = replace
// (REQUIREMENTS.md §5.4). POST ignores nil and "" (no key configured).
type providerPayload struct {
	Name          string  `json:"name"`
	BaseURL       string  `json:"base_url"`
	APIKey        *string `json:"api_key"`
	Model         string  `json:"model"`
	Prompt        string  `json:"prompt"`
	MaxTokens     int     `json:"max_tokens"`
	IntervalSec   int     `json:"interval_sec"`
	TimeoutSec    int     `json:"timeout_sec"`
	TTFTTimeoutMs int     `json:"ttft_timeout_ms"`
	TTFTSlowMs    int     `json:"ttft_slow_ms"`
	Enabled       bool    `json:"enabled"`
	IncludeUsage  bool    `json:"include_usage"`
}

// validate mirrors the panel-side checks (web/app.js validateProvider):
// the server re-validates so a broken or malicious client cannot store an
// unsound configuration (defense in depth).
func (c *providerPayload) validate() string {
	switch {
	case strings.TrimSpace(c.Name) == "":
		return "名称不能为空"
	case !strings.HasPrefix(c.BaseURL, "http://") && !strings.HasPrefix(c.BaseURL, "https://"):
		return "Base URL 必须以 http:// 或 https:// 开头（API 前缀，如 https://example.com/v1）"
	case strings.TrimSpace(c.Model) == "":
		return "模型不能为空"
	case c.MaxTokens <= 0:
		return "max_tokens 必须大于 0"
	case c.TimeoutSec <= 0:
		return "总超时必须大于 0"
	case c.IntervalSec != 0 && c.IntervalSec < 60:
		return "探测间隔为 0（仅手动）或至少 1 分钟"
	case !(c.TTFTSlowMs > 0 && c.TTFTSlowMs < c.TTFTTimeoutMs):
		return "需要 0 < 慢阈值 < 首内容超时"
	case c.TTFTTimeoutMs > c.TimeoutSec*1000:
		return "首内容超时不能大于总超时"
	}
	return ""
}

// applyPromptDefault fills the probe prompt when the client submitted none.
// Numeric fields are never defaulted: the panel sends complete forms and the
// server validates them strictly (REQUIREMENTS.md §4.1), so an omitted
// numeric field is reported as invalid instead of guessed.
func (c *providerPayload) applyPromptDefault() {
	if strings.TrimSpace(c.Prompt) == "" {
		c.Prompt = defaultPrompt
	}
}

// toProvider materializes the payload as a store.Provider. key carries the
// resolved API key after PUT semantics (keep/clear/replace).
func (c *providerPayload) toProvider(key string) store.Provider {
	return store.Provider{
		Name:          strings.TrimSpace(c.Name),
		BaseURL:       strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"),
		APIKey:        key,
		Model:         strings.TrimSpace(c.Model),
		Prompt:        c.Prompt,
		MaxTokens:     c.MaxTokens,
		IntervalSec:   c.IntervalSec,
		TimeoutSec:    c.TimeoutSec,
		TTFTTimeoutMs: c.TTFTTimeoutMs,
		TTFTSlowMs:    c.TTFTSlowMs,
		Enabled:       c.Enabled,
		IncludeUsage:  c.IncludeUsage,
	}
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	providers := s.st.GetProviders()
	out := make([]providerView, 0, len(providers))
	for _, p := range providers {
		out = append(out, s.viewOf(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddProvider(w http.ResponseWriter, r *http.Request) {
	var c providerPayload
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.applyPromptDefault()
	if msg := c.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	key := ""
	if c.APIKey != nil {
		key = strings.TrimSpace(*c.APIKey)
	}
	created, err := s.st.AddProvider(c.toProvider(key))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	if s.mut != nil {
		s.mut.Add(created)
	}
	writeJSON(w, http.StatusCreated, s.viewOf(created))
}

func (s *Server) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	old, exists := s.st.GetProvider(id)
	if !exists {
		writeError(w, http.StatusNotFound, "监测对象不存在")
		return
	}
	var c providerPayload
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.applyPromptDefault()
	if msg := c.validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// API key semantics: absent = keep, explicit ""/null = clear,
	// non-empty = replace (REQUIREMENTS.md §5.4).
	key := old.APIKey
	if c.APIKey != nil {
		key = strings.TrimSpace(*c.APIKey)
	}
	updated := c.toProvider(key)
	updated.ID = old.ID
	updated.CreatedAt = old.CreatedAt
	updated.Revision = old.Revision
	// Target change bumps the revision; other edits keep it
	// (REQUIREMENTS.md §4.1). Base URLs are compared normalized.
	if updated.BaseURL != old.BaseURL || updated.Model != old.Model {
		updated.Revision = old.Revision + 1
	}

	if err := s.st.UpdateProvider(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	if s.mut != nil {
		s.mut.Update(updated)
	}
	writeJSON(w, http.StatusOK, s.viewOf(updated))
}

func (s *Server) handleDeleteProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if _, exists := s.st.GetProvider(id); !exists {
		writeError(w, http.StatusNotFound, "监测对象不存在")
		return
	}
	// The engine cancels the in-flight probe first (its outcome is recorded
	// as cancelled and does not count as an API failure, §4.1).
	if s.mut != nil {
		s.mut.Remove(id)
	}
	if err := s.st.DeleteProvider(id); err != nil {
		writeError(w, http.StatusInternalServerError, "删除失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleProbe runs one manual probe synchronously (REQUIREMENTS.md §4.6,
// §10). The handler blocks until the probe finishes, bounded by the
// provider's total timeout. 409 when a probe is already in flight.
func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	if s.eng == nil {
		writeError(w, http.StatusServiceUnavailable, "探测引擎未就绪")
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	p, exists := s.st.GetProvider(id)
	if !exists {
		writeError(w, http.StatusNotFound, "监测对象不存在")
		return
	}
	res, err := s.eng.ProbeNow(id)
	if err != nil {
		if s.eng.IsInFlight(err) {
			writeError(w, http.StatusConflict, "正在探测：该对象已有在途请求")
			return
		}
		writeError(w, http.StatusInternalServerError, "探测失败："+err.Error())
		return
	}
	// slow mirrors the panel badge for manual probes (web/app.js
	// manualProbe reads r.slow); throughput uses the same derivation as the
	// stored-sample statistics.
	pv := probeView{Result: res, Slow: slowTTFT(p, res)}
	if res.Status == "ok" {
		pv.DecodeTPS = store.ResultTPS(*res, true)
		pv.PrefillTPS = store.ResultTPS(*res, false)
	}
	writeJSON(w, http.StatusOK, pv)
}

// pathID extracts and parses the {id} path parameter.
func pathID(w http.ResponseWriter, r *http.Request, param string) (int, bool) {
	id, err := strconv.Atoi(r.PathValue(param))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "无效的对象 ID")
		return 0, false
	}
	return id, true
}

// slowTTFT reports whether a successful probe exceeded the slow threshold;
// it is a hint on top of "ok", never a separate status (REQUIREMENTS.md
// §4.4).
func slowTTFT(p store.Provider, res *store.Result) bool {
	return res.Status == "ok" && res.TTFTMs != nil && *res.TTFTMs > int64(p.TTFTSlowMs)
}

// staleAfter is the freshness limit of the last scheduled probe:
// 2×interval + timeout, in milliseconds (REQUIREMENTS.md §7.2-4).
func staleAfter(p store.Provider) int64 {
	return (int64(2*p.IntervalSec) + int64(p.TimeoutSec)) * 1000
}
