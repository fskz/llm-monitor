package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// providerRequest is the POST/PUT wire body. It embeds the shared form
// (view.ProviderForm, validated identically for the panel and the TUI) and
// adds APIKey, which stays at the HTTP layer: *string so the three PUT
// semantics can be told apart — absent (nil) = keep the stored key,
// explicit "" or null = clear it, non-empty = replace (REQUIREMENTS.md
// §5.4). POST ignores nil and "" (no key configured).
type providerRequest struct {
	view.ProviderForm
	APIKey *string `json:"api_key"`
}

// form normalizes the decoded body and applies the prompt default before
// validation (view.ProviderForm.Prepare).
func (c *providerRequest) form() view.ProviderForm {
	f := c.ProviderForm
	f.Prepare()
	return f
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	providers := s.st.GetProviders()
	out := make([]view.ProviderView, 0, len(providers))
	for _, p := range providers {
		out = append(out, view.ViewOf(s.st, s.eng, p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddProvider(w http.ResponseWriter, r *http.Request) {
	var c providerRequest
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	f := c.form()
	if msg := f.Validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	key := ""
	if c.APIKey != nil {
		key = strings.TrimSpace(*c.APIKey)
	}
	created, err := s.st.AddProvider(f.ToProvider(key))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "保存配置失败："+err.Error())
		return
	}
	if s.mut != nil {
		s.mut.Add(created)
	}
	writeJSON(w, http.StatusCreated, view.ViewOf(s.st, s.eng, created))
}

// handleCloneProvider duplicates a provider as a NEW object (task:
// clone-for-model-variants). The copy carries every field including the
// API key — the key is read from the source server-side and never enters
// a request or response body, preserving the "read APIs never return the
// full key" contract (§5.4). The name gets a 副本 suffix; everything else
// (base_url, model, thresholds…) is the starting point the user edits
// afterwards — typically just the model name for the same channel.
func (s *Server) handleCloneProvider(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	src, exists := s.st.GetProvider(id)
	if !exists {
		writeError(w, http.StatusNotFound, "监测对象不存在")
		return
	}
	f := view.ProviderForm{
		Name:          src.Name + "（副本）",
		BaseURL:       src.BaseURL,
		Model:         src.Model,
		Prompt:        src.Prompt,
		MaxTokens:     src.MaxTokens,
		IntervalSec:   src.IntervalSec,
		TimeoutSec:    src.TimeoutSec,
		TTFTTimeoutMs: src.TTFTTimeoutMs,
		TTFTSlowMs:    src.TTFTSlowMs,
		Enabled:       src.Enabled,
		IncludeUsage:  src.IncludeUsage,
	}
	if msg := f.Validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	created, err := s.st.AddProvider(f.ToProvider(src.APIKey))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "克隆失败："+err.Error())
		return
	}
	if s.mut != nil {
		s.mut.Add(created)
	}
	writeJSON(w, http.StatusCreated, view.ViewOf(s.st, s.eng, created))
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
	var c providerRequest
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	f := c.form()
	if msg := f.Validate(); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	// API key semantics: absent = keep, explicit ""/null = clear,
	// non-empty = replace (REQUIREMENTS.md §5.4).
	key := old.APIKey
	if c.APIKey != nil {
		key = strings.TrimSpace(*c.APIKey)
	}
	updated := f.ToProvider(key)
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
	writeJSON(w, http.StatusOK, view.ViewOf(s.st, s.eng, updated))
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
	pv := view.ProbeView{Result: res, Slow: view.SlowTTFT(p, res)}
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
