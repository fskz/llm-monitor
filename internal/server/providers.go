package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	if !decodeJSON(w, r, &c) {
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
	if !decodeJSON(w, r, &c) {
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
	if res.Status == store.StatusOK {
		pv.DecodeTPS = store.ResultTPS(*res, true)
		pv.PrefillTPS = store.ResultTPS(*res, false)
	}
	writeJSON(w, http.StatusOK, pv)
}

// handleCacheCheck runs one two-hop prefix-cache detection synchronously
// (task 10-22-cache-check, prd R3). Same shape as handleProbe: the handler
// blocks until the check finishes — worst case ~2 minutes at the fixed 60s
// per-hop budget (the panel's http.Serve runs without timeouts, so a long
// request is never cut short) — 404 for an unknown provider, 409 when a
// check for the same provider is already running. No request body.
func (s *Server) handleCacheCheck(w http.ResponseWriter, r *http.Request) {
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
	rec, err := s.eng.CacheCheck(id)
	if err != nil {
		if s.eng.IsCacheCheckInFlight(err) {
			writeError(w, http.StatusConflict, "正在检测：该对象已有在途检测请求")
			return
		}
		writeError(w, http.StatusInternalServerError, "缓存检测失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view.CacheCheckViewOf(p, rec, true))
}

// handleLatestCacheCheck returns the provider's latest persisted cache-check
// record via view.CacheCheckViewOf, empty state included (prd R4: the detail
// page always shows the latest verdict). Read-only like every GET: no
// same-origin guard (only writes are origin-checked, §5.4).
func (s *Server) handleLatestCacheCheck(w http.ResponseWriter, r *http.Request) {
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
	rec, found := s.eng.LatestCacheCheck(id)
	writeJSON(w, http.StatusOK, view.CacheCheckViewOf(p, rec, found))
}

// handleCacheCheckExport serves the all-providers JSON snapshot of the
// latest cache-check verdicts as an attachment download (the web twin of
// the TUI /exportcache command). Store-only — no engine involvement — and
// read-only like every GET: no same-origin guard (§5.4).
func (s *Server) handleCacheCheckExport(w http.ResponseWriter, r *http.Request) {
	snap := view.CacheCheckSnapshotOf(s.st.GetProviders(), s.st.LoadCacheCheck)
	// Render into a buffer first so an encoding failure surfaces as a clean
	// 500 instead of a corrupted mid-stream download (headers cannot be
	// rewritten once written — same discipline as handleReport).
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(snap); err != nil {
		writeError(w, http.StatusInternalServerError, "导出缓存判定快照失败："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q",
		"llm-monitor-cache-check-"+time.Now().Format("20060102-150405")+".json"))
	_, _ = w.Write(buf.Bytes())
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
