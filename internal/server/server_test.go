package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"llm-monitor/internal/probe"
	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// errInFlight mirrors engine.ErrInFlight for the fake engine below.
var errInFlight = errors.New("probe already in flight")

// fakeEngine is a test double of EngineAPI. When targetURL is set it runs a
// real probe.Do against a mocked OpenAI-compatible endpoint, so the probe
// endpoint is exercised end to end through the server.
type fakeEngine struct {
	mu       sync.Mutex
	st       *store.Store
	probing  map[int]bool
	storeErr string
	// probe behavior knobs for the next ProbeNow call
	inFlight bool
	result   *store.Result
}

func (f *fakeEngine) Probing(id int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probing[id]
}

func (f *fakeEngine) StorageError() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.storeErr
}

func (f *fakeEngine) IsInFlight(err error) bool { return errors.Is(err, errInFlight) }

func (f *fakeEngine) ProbeNow(id int) (*store.Result, error) {
	f.mu.Lock()
	inFlight, preseeded := f.inFlight, f.result
	f.inFlight, f.result = false, nil
	f.mu.Unlock()

	if inFlight {
		return nil, errInFlight
	}
	if preseeded != nil {
		res := *preseeded
		res.ProviderID = id
		if _, err := f.st.AppendResult(res); err != nil {
			return nil, err
		}
		return &res, nil
	}

	// Real probe against a mocked endpoint: exercised by TestProbeEndpoint.
	p, ok := f.st.GetProvider(id)
	if !ok {
		return nil, fmt.Errorf("unknown provider %d", id)
	}
	o := probe.Do(context.Background(), &http.Client{}, probe.Target{
		BaseURL:        p.BaseURL,
		APIKey:         p.APIKey,
		Model:          p.Model,
		Prompt:         p.Prompt,
		MaxTokens:      p.MaxTokens,
		TTFTTimeoutMs:  p.TTFTTimeoutMs,
		TotalTimeoutMs: p.TimeoutSec * 1000,
	})
	now := time.Now().UnixMilli()
	res := store.Result{
		ProviderID:    id,
		Revision:      p.Revision,
		BaseURL:       p.BaseURL,
		Model:         p.Model,
		Source:        store.SourceManual,
		StartedAt:     now - o.TotalMs,
		FinishedAt:    now,
		Success:       o.Status == probe.StatusOK,
		TTFTMs:        o.TTFTMs,
		TotalMs:       o.TotalMs,
		Status:        o.Status,
		HTTPStatus:    o.HTTPStatus,
		Error:         o.ErrDetail,
		OutputPreview: o.OutputPreview,
	}
	if _, err := f.st.AppendResult(res); err != nil {
		return nil, err
	}
	return &res, nil
}

// fakeMutator records engine-side mutations.
type fakeMutator struct {
	mu      sync.Mutex
	added   []int
	updated []int
	removed []int
}

func (m *fakeMutator) Add(p store.Provider) {
	m.mu.Lock()
	m.added = append(m.added, p.ID)
	m.mu.Unlock()
}
func (m *fakeMutator) Update(p store.Provider) {
	m.mu.Lock()
	m.updated = append(m.updated, p.ID)
	m.mu.Unlock()
}
func (m *fakeMutator) Remove(id int) { m.mu.Lock(); m.removed = append(m.removed, id); m.mu.Unlock() }

const testPort = 10110

// newTestServer builds a server on a real temp store plus a fake engine.
func newTestServer(t *testing.T) (*Server, *fakeEngine, *fakeMutator) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	eng := &fakeEngine{st: st, probing: map[int]bool{}}
	mut := &fakeMutator{}
	webFS := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>ok")}}
	return New(st, eng, mut, webFS, testPort), eng, mut
}

// serve runs one request against the server handler and returns the response.
func serve(t *testing.T, s *Server, method, target, body string, header map[string]string) (*http.Response, string) {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	res := rec.Result()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return res, string(b)
}

func mustPost(t *testing.T, s *Server, body string, header map[string]string) (int, string) {
	t.Helper()
	res, body2 := serve(t, s, "POST", "/api/providers", body, header)
	return res.StatusCode, body2
}

// addProviderDirect seeds a store provider without going through the API.
func addProviderDirect(t *testing.T, st *store.Store, mutate func(*store.Provider)) store.Provider {
	t.Helper()
	p, err := st.AddProvider(store.Provider{
		Name: "p", BaseURL: "http://127.0.0.1:9/v1", Model: "m",
		Prompt: "ping", MaxTokens: 128,
		TimeoutSec: 60, TTFTTimeoutMs: 10000, TTFTSlowMs: 2000,
		IntervalSec: 300, Enabled: true,
	})
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	if mutate != nil {
		mutate(&p)
		if err := st.UpdateProvider(p); err != nil {
			t.Fatalf("UpdateProvider: %v", err)
		}
	}
	return p
}

// appendDirect seeds one result line.
func appendDirect(t *testing.T, st *store.Store, r store.Result) {
	t.Helper()
	if _, err := st.AppendResult(r); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
}

func scheduledResult(p store.Provider, status string, startedAt int64, ttft *int64) store.Result {
	return store.Result{
		ProviderID: p.ID, Revision: p.Revision,
		BaseURL: p.BaseURL, Model: p.Model,
		Source:    store.SourceScheduled,
		StartedAt: startedAt, FinishedAt: startedAt + 1500,
		Success: status == "ok", TTFTMs: ttft, TotalMs: 1500, Status: status,
	}
}

// overview fetches and decodes GET /api/providers.
func overview(t *testing.T, s *Server) []view.ProviderView {
	t.Helper()
	res, body := serve(t, s, "GET", "/api/providers", "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("GET /api/providers = %d: %s", res.StatusCode, body)
	}
	var views []view.ProviderView
	if err := json.Unmarshal([]byte(body), &views); err != nil {
		t.Fatalf("decode overview: %v (%s)", err, body)
	}
	return views
}

func i64(v int64) *int64 { return &v }

func base64Raw(s string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(s))
}

/* ---------- 1. monitor status matrix (§7.2) ---------- */

func TestStatusMatrix(t *testing.T) {
	s, _, _ := newTestServer(t)
	st := s.st
	now := time.Now().UnixMilli()

	// One provider per status case; the scheduled result each case seeds is
	// built with the real provider ID/revision, so seeding happens after
	// creation (switch below).
	cases := []struct {
		name       string
		mutate     func(*store.Provider)
		wantStatus string
		wantSlow   bool
	}{
		{"disabled", func(p *store.Provider) { p.Enabled = false }, "disabled", false},
		{"manual_only", func(p *store.Provider) { p.IntervalSec = 0 }, "manual_only", false},
		{"unknown", nil, "unknown", false},
		// staleness limit: 2×300s + 60s + margin.
		{"stale", nil, "stale", false},
		{"ok", nil, "ok", false},
		{"ok_slow", nil, "ok", true}, // ttft 5000 > slow threshold 2000
		{"fail", nil, "fail", false},
	}

	views := map[string]view.ProviderView{}
	for _, c := range cases {
		p := addProviderDirect(t, st, func(pp *store.Provider) {
			pp.Name = c.name
			if c.mutate != nil {
				c.mutate(pp)
			}
		})
		switch c.name {
		case "stale":
			appendDirect(t, st, scheduledResult(p, "ok", now-(2*300+60)*1000-5000, i64(500)))
		case "ok":
			appendDirect(t, st, scheduledResult(p, "ok", now-30_000, i64(800)))
		case "ok_slow":
			appendDirect(t, st, scheduledResult(p, "ok", now-30_000, i64(5000)))
		case "fail":
			r := scheduledResult(p, "http_error", now-30_000, nil)
			r.HTTPStatus = new(int)
			*r.HTTPStatus = 401
			r.Error = "401 Unauthorized"
			appendDirect(t, st, r)
		}
	}

	for _, v := range overview(t, s) {
		views[v.Name] = v
	}
	for _, c := range cases {
		v, ok := views[c.name]
		if !ok {
			t.Fatalf("provider %q missing from overview", c.name)
		}
		if v.Status != c.wantStatus {
			t.Errorf("%s: status = %q, want %q", c.name, v.Status, c.wantStatus)
		}
		if c.wantSlow && !v.SlowTTFT {
			t.Errorf("%s: slow_ttft = false, want true", c.name)
		}
	}

	// Fresh ok with recent result must not be stale; last_probe present.
	if v := views["ok"]; v.LastProbe == nil || v.LastProbe.Status != "ok" {
		t.Errorf("ok: last_probe = %+v, want ok result", v.LastProbe)
	}
	// unknown must have last_probe == null.
	if v := views["unknown"]; v.LastProbe != nil {
		t.Errorf("unknown: last_probe = %+v, want nil", v.LastProbe)
	}
	// stale keeps its (old) last_probe but stays stale.
	if v := views["stale"]; v.LastProbe == nil || v.Status != "stale" {
		t.Errorf("stale: last_probe = %+v status = %q", v.LastProbe, v.Status)
	}
}

/* ---------- 2. API key masking (§5.4) ---------- */

func TestAPIKeyMasking(t *testing.T) {
	s, _, _ := newTestServer(t)
	code, _ := mustPost(t, s, `{
		"name":"masked","base_url":"https://api.example.com/v1","model":"m",
		"max_tokens":128,"interval_sec":300,"timeout_sec":60,
		"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true,
		"api_key":"sk-1234567890abcd"
	}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d", code)
	}

	views := overview(t, s)
	if len(views) != 1 {
		t.Fatalf("overview len = %d", len(views))
	}
	v := views[0]
	if !v.APIKeySet {
		t.Error("api_key_set = false, want true")
	}
	if v.APIKeyMask != "sk-1****abcd" {
		t.Errorf("mask = %q, want %q", v.APIKeyMask, "sk-1****abcd")
	}
	// Raw key must never appear in any response body.
	res, body := serve(t, s, "GET", "/api/providers", "", nil)
	_ = res
	if strings.Contains(body, "sk-1234567890abcd") {
		t.Error("full API key leaked in /api/providers body")
	}

	// Short key: fully masked.
	p := addProviderDirect(t, s.st, func(pp *store.Provider) { pp.APIKey = "abc12345"; pp.Name = "short" })
	_ = p
	for _, v := range overview(t, s) {
		if v.Name == "short" && v.APIKeyMask != "********" {
			t.Errorf("short key mask = %q, want ********", v.APIKeyMask)
		}
	}
}

/* ---------- 3. PUT api_key semantics ---------- */

func TestPutAPIKeySemantics(t *testing.T) {
	s, _, _ := newTestServer(t)
	code, _ := mustPost(t, s, `{
		"name":"k","base_url":"https://a.example.com/v1","model":"m",
		"max_tokens":128,"interval_sec":300,"timeout_sec":60,
		"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true,
		"api_key":"sk-original-key-000"
	}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d", code)
	}
	id := overview(t, s)[0].ID
	base := func(key string) string {
		return fmt.Sprintf(`{"name":"k","base_url":"https://a.example.com/v1","model":"m",
			"max_tokens":128,"interval_sec":300,"timeout_sec":60,
			"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true%s}`, key)
	}

	// absent field → keep stored key.
	res, body := serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", id), base(""), nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT keep = %d: %s", res.StatusCode, body)
	}
	if got, _ := s.st.GetProvider(id); got.APIKey != "sk-original-key-000" {
		t.Errorf("absent api_key: stored = %q, want kept", got.APIKey)
	}
	if v := overview(t, s)[0]; !v.APIKeySet {
		t.Error("absent api_key: api_key_set = false, want true")
	}

	// explicit "" → clear.
	res, body = serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", id), base(`,"api_key":""`), nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT clear = %d: %s", res.StatusCode, body)
	}
	if got, _ := s.st.GetProvider(id); got.APIKey != "" {
		t.Errorf("empty api_key: stored = %q, want cleared", got.APIKey)
	}
	if v := overview(t, s)[0]; v.APIKeySet {
		t.Error("cleared api_key: api_key_set = true, want false")
	}

	// new value → replace.
	res, body = serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", id), base(`,"api_key":"sk-new-key-111"`), nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT replace = %d: %s", res.StatusCode, body)
	}
	if got, _ := s.st.GetProvider(id); got.APIKey != "sk-new-key-111" {
		t.Errorf("new api_key: stored = %q, want replaced", got.APIKey)
	}
}

/* ---------- 4. revision bump on target change ---------- */

func TestRevisionBump(t *testing.T) {
	s, _, _ := newTestServer(t)
	code, _ := mustPost(t, s, `{
		"name":"rev","base_url":"https://a.example.com/v1","model":"m1",
		"max_tokens":128,"interval_sec":300,"timeout_sec":60,
		"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true
	}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d", code)
	}
	v := overview(t, s)[0]
	if v.Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", v.Revision)
	}

	// prompt-only change: revision unchanged.
	res, body := serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", v.ID),
		`{"name":"rev","base_url":"https://a.example.com/v1","model":"m1","prompt":"hi",
		  "max_tokens":128,"interval_sec":300,"timeout_sec":60,
		  "ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`, nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT prompt = %d: %s", res.StatusCode, body)
	}
	if got := overview(t, s)[0]; got.Revision != 1 {
		t.Errorf("prompt change: revision = %d, want 1", got.Revision)
	}

	// base_url change: revision bumps. Also trailing slash must normalize.
	res, body = serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", v.ID),
		`{"name":"rev","base_url":"https://b.example.com/v1/","model":"m1",
		  "max_tokens":128,"interval_sec":300,"timeout_sec":60,
		  "ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`, nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT base_url = %d: %s", res.StatusCode, body)
	}
	got := overview(t, s)[0]
	if got.Revision != 2 {
		t.Errorf("base_url change: revision = %d, want 2", got.Revision)
	}
	if got.BaseURL != "https://b.example.com/v1" {
		t.Errorf("base_url = %q, want trailing slash trimmed", got.BaseURL)
	}

	// model change bumps again.
	res, body = serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", v.ID),
		`{"name":"rev","base_url":"https://b.example.com/v1","model":"m2",
		  "max_tokens":128,"interval_sec":300,"timeout_sec":60,
		  "ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`, nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT model = %d: %s", res.StatusCode, body)
	}
	if got := overview(t, s)[0]; got.Revision != 3 {
		t.Errorf("model change: revision = %d, want 3", got.Revision)
	}
}

/* ---------- 5. validation (400) ---------- */

func TestValidation(t *testing.T) {
	s, _, _ := newTestServer(t)
	cases := []struct {
		name string
		body string
	}{
		{"interval_sec=30", `{"name":"v","base_url":"https://a.example.com/v1","model":"m",
			"max_tokens":128,"interval_sec":30,"timeout_sec":60,
			"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`},
		{"slow >= ttft_timeout", `{"name":"v","base_url":"https://a.example.com/v1","model":"m",
			"max_tokens":128,"interval_sec":300,"timeout_sec":60,
			"ttft_timeout_ms":2000,"ttft_slow_ms":2000,"enabled":true}`},
		{"ttft_timeout > total", `{"name":"v","base_url":"https://a.example.com/v1","model":"m",
			"max_tokens":128,"interval_sec":300,"timeout_sec":5,
			"ttft_timeout_ms":10000,"ttft_slow_ms":1000,"enabled":true}`},
		{"empty name", `{"name":"","base_url":"https://a.example.com/v1","model":"m",
			"max_tokens":128,"interval_sec":300,"timeout_sec":60,
			"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`},
		{"bad base_url", `{"name":"v","base_url":"ftp://a.example.com/v1","model":"m",
			"max_tokens":128,"interval_sec":300,"timeout_sec":60,
			"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`},
		{"max_tokens=0 explicit", `{"name":"v","base_url":"https://a.example.com/v1","model":"m",
			"max_tokens":0,"interval_sec":300,"timeout_sec":60,
			"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`},
	}
	for _, c := range cases {
		code, body := mustPost(t, s, c.body, nil)
		if code != http.StatusBadRequest {
			t.Errorf("%s: POST = %d (%s), want 400", c.name, code, body)
		}
	}
	// manual-only interval 0 must be accepted on PUT.
	code, _ := mustPost(t, s, `{"name":"v","base_url":"https://a.example.com/v1","model":"m",
		"max_tokens":128,"interval_sec":300,"timeout_sec":60,
		"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("POST valid = %d", code)
	}
	id := overview(t, s)[0].ID
	res, body := serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", id),
		`{"name":"v","base_url":"https://a.example.com/v1","model":"m",
		  "max_tokens":128,"interval_sec":0,"timeout_sec":60,
		  "ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`, nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT interval=0 = %d: %s", res.StatusCode, body)
	}
	if got := overview(t, s)[0]; got.Status != "manual_only" {
		t.Errorf("interval=0: status = %q, want manual_only", got.Status)
	}
}

/* ---------- 6. manual probe endpoint ---------- */

// mockLLM starts an OpenAI-compatible streaming endpoint for end-to-end
// probes through the fake engine.
func mockLLM(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProbeEndpoint(t *testing.T) {
	s, eng, _ := newTestServer(t)
	llm := mockLLM(t)

	code, _ := mustPost(t, s, fmt.Sprintf(`{
		"name":"probe","base_url":%q,"model":"m",
		"max_tokens":128,"interval_sec":300,"timeout_sec":60,
		"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`, llm.URL), nil)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d", code)
	}
	id := overview(t, s)[0].ID

	// In-flight conflict → 409.
	eng.mu.Lock()
	eng.inFlight = true
	eng.mu.Unlock()
	res, body := serve(t, s, "POST", fmt.Sprintf("/api/providers/%d/probe", id), "", nil)
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("probe in-flight = %d (%s), want 409", res.StatusCode, body)
	}
	if !strings.Contains(body, "正在探测") {
		t.Errorf("409 body = %s, want in-flight message", body)
	}

	// Normal probe through the real probe.Do → ok + manual result stored.
	res, body = serve(t, s, "POST", fmt.Sprintf("/api/providers/%d/probe", id), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("probe = %d (%s), want 200", res.StatusCode, body)
	}
	var pv view.ProbeView
	if err := json.Unmarshal([]byte(body), &pv); err != nil {
		t.Fatalf("decode probe view: %v", err)
	}
	if pv.Result == nil || pv.Result.Status != "ok" {
		t.Fatalf("probe result = %+v, want ok", pv.Result)
	}
	if pv.Result.Source != store.SourceManual {
		t.Errorf("probe source = %q, want manual", pv.Result.Source)
	}
	// Local mock streams instantly: ttft stays under the 2000ms slow
	// threshold, so the slow hint must be false.
	if pv.Slow {
		t.Error("probe slow = true, want false (fast local stream)")
	}
	if pv.Result.OutputPreview != "pong" {
		t.Errorf("probe preview = %q, want pong", pv.Result.OutputPreview)
	}
	// Manual result must NOT enter the default (scheduled) stats.
	res, body = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d", id), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("stats = %d (%s)", res.StatusCode, body)
	}
	if !strings.Contains(body, `"samples":0`) {
		t.Errorf("stats after manual probe = %s, want samples 0", body)
	}
	// But it is queryable with source=manual.
	res, body = serve(t, s, "GET",
		fmt.Sprintf("/api/results?provider=%d&source=manual", id), "", nil)
	if res.StatusCode != 200 || !strings.Contains(body, `"status":"ok"`) {
		t.Errorf("manual results = %d (%s)", res.StatusCode, body)
	}
}

/* ---------- 7. same-origin protection (§5.4) ---------- */

func TestOriginProtection(t *testing.T) {
	s, _, _ := newTestServer(t)
	body := `{"name":"o","base_url":"https://a.example.com/v1","model":"m",
		"max_tokens":128,"interval_sec":300,"timeout_sec":60,
		"ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`

	// Cross-origin write → 403, nothing stored.
	res, respBody := serve(t, s, "POST", "/api/providers", body,
		map[string]string{"Origin": "http://evil.com"})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("evil origin POST = %d (%s), want 403", res.StatusCode, respBody)
	}
	if got := overview(t, s); len(got) != 0 {
		t.Fatalf("cross-origin POST created %d providers", len(got))
	}

	// Same-origin (127.0.0.1:<port>) → allowed.
	res, respBody = serve(t, s, "POST", "/api/providers", body,
		map[string]string{"Origin": fmt.Sprintf("http://127.0.0.1:%d", testPort)})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("local origin POST = %d (%s), want 201", res.StatusCode, respBody)
	}
	// localhost variant also allowed.
	id := overview(t, s)[0].ID
	res, respBody = serve(t, s, "DELETE", fmt.Sprintf("/api/providers/%d", id), "",
		map[string]string{"Origin": fmt.Sprintf("http://localhost:%d", testPort)})
	if res.StatusCode != 200 {
		t.Fatalf("localhost origin DELETE = %d (%s), want 200", res.StatusCode, respBody)
	}

	// No Origin (curl) → allowed.
	res, respBody = serve(t, s, "POST", "/api/providers", body, nil)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("no-origin POST = %d (%s), want 201", res.StatusCode, respBody)
	}

	// GET is never origin-checked.
	res, _ = serve(t, s, "GET", "/api/providers", "",
		map[string]string{"Origin": "http://evil.com"})
	if res.StatusCode != 200 {
		t.Fatalf("evil origin GET = %d, want 200", res.StatusCode)
	}

	// No CORS headers on responses.
	res, _ = serve(t, s, "GET", "/api/providers", "", nil)
	if h := res.Header.Get("Access-Control-Allow-Origin"); h != "" {
		t.Errorf("Access-Control-Allow-Origin = %q, want empty", h)
	}
}

/* ---------- 8. results pagination ---------- */

func TestResultsPagination(t *testing.T) {
	s, _, _ := newTestServer(t)
	p := addProviderDirect(t, s.st, nil)
	now := time.Now().UnixMilli()
	for i := 0; i < 15; i++ {
		appendDirect(t, s.st, scheduledResult(p, "ok", now-int64(15-i)*1000, i64(900)))
	}

	seen := map[string]bool{}
	cursor := ""
	pages := []int{}
	for {
		target := fmt.Sprintf("/api/results?provider=%d&limit=6", p.ID)
		if cursor != "" {
			target += "&before=" + cursor
		}
		res, body := serve(t, s, "GET", target, "", nil)
		if res.StatusCode != 200 {
			t.Fatalf("results page = %d: %s", res.StatusCode, body)
		}
		var page struct {
			Items      []store.Result `json:"items"`
			NextCursor *string        `json:"next_cursor"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatalf("decode page: %v", err)
		}
		pages = append(pages, len(page.Items))
		for _, it := range page.Items {
			key := fmt.Sprintf("%d:%d", it.StartedAt, it.Seq)
			if seen[key] {
				t.Fatalf("duplicate record %s across pages", key)
			}
			seen[key] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(pages) != 3 || pages[0] != 6 || pages[1] != 6 || pages[2] != 3 {
		t.Fatalf("page sizes = %v, want [6 6 3]", pages)
	}
	if len(seen) != 15 {
		t.Fatalf("total records = %d, want 15", len(seen))
	}

	// Descending order check on the first page.
	res, body := serve(t, s, "GET", fmt.Sprintf("/api/results?provider=%d&limit=5", p.ID), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("results = %d", res.StatusCode)
	}
	var page struct {
		Items []store.Result `json:"items"`
	}
	_ = json.Unmarshal([]byte(body), &page)
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i-1].StartedAt < page.Items[i].StartedAt {
			t.Fatalf("results not descending: [%d]=%d < [%d]=%d",
				i-1, page.Items[i-1].StartedAt, i, page.Items[i].StartedAt)
		}
	}

	// limit clamp: 501 records, limit=9999 → exactly 500 + a cursor.
	p2 := addProviderDirect(t, s.st, func(pp *store.Provider) { pp.Name = "clamp" })
	for i := 0; i < 501; i++ {
		appendDirect(t, s.st, scheduledResult(p2, "ok", now-int64(1000-i), i64(900)))
	}
	res, body = serve(t, s, "GET",
		fmt.Sprintf("/api/results?provider=%d&limit=9999", p2.ID), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("clamped results = %d: %s", res.StatusCode, body)
	}
	var big struct {
		Items      []store.Result `json:"items"`
		NextCursor *string        `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(body), &big); err != nil {
		t.Fatalf("decode big page: %v", err)
	}
	if len(big.Items) != 500 {
		t.Errorf("clamped page size = %d, want 500", len(big.Items))
	}
	if big.NextCursor == nil {
		t.Error("clamped page next_cursor = nil, want a cursor (501 records)")
	}

	// Invalid cursor → 400 (decodable base64 but malformed payload).
	res, _ = serve(t, s, "GET",
		fmt.Sprintf("/api/results?provider=%d&before=%s", p2.ID,
			base64Raw("not-a-cursor")), "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid cursor = %d, want 400", res.StatusCode)
	}
}

/* ---------- 9. stats end-to-end (acceptance A1.6) ---------- */

func TestStatsAcceptanceRatios(t *testing.T) {
	s, _, _ := newTestServer(t)
	p := addProviderDirect(t, s.st, nil)
	now := time.Now().UnixMilli()

	for i := 0; i < 3; i++ {
		appendDirect(t, s.st, scheduledResult(p, "ok", now-int64(i+1)*60_000, i64(900)))
	}
	appendDirect(t, s.st, scheduledResult(p, "timeout_ttft", now-4*60_000, nil))
	appendDirect(t, s.st, scheduledResult(p, "timeout_total", now-5*60_000, i64(5000)))
	r := scheduledResult(p, "http_error", now-6*60_000, nil)
	r.HTTPStatus = new(int)
	*r.HTTPStatus = 500
	r.Error = "500 Internal Server Error"
	appendDirect(t, s.st, r)
	appendDirect(t, s.st, scheduledResult(p, "cancelled", now-7*60_000, nil))
	m := scheduledResult(p, "ok", now-8*60_000, i64(700))
	m.Source = store.SourceManual
	appendDirect(t, s.st, m)

	res, body := serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d", p.ID), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("stats = %d: %s", res.StatusCode, body)
	}
	var sv view.StatsView
	if err := json.Unmarshal([]byte(body), &sv); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if sv.Samples != 6 || sv.OK != 3 || sv.Timeout != 2 || sv.ErrorCount != 1 {
		t.Fatalf("counts = %+v, want samples 6 ok 3 timeout 2 error 1", sv)
	}
	assertPct(t, "ok", sv.OKPct, 50)
	assertPct(t, "timeout", sv.TimeoutPct, 33.33)
	assertPct(t, "error", sv.ErrorPct, 16.67)

	// avg over ok samples only: (900+900+900)/3.
	if sv.AvgTTFTMs == nil || *sv.AvgTTFTMs != 900 {
		t.Errorf("avg_ttft_ms = %v, want 900", sv.AvgTTFTMs)
	}
	if sv.AvgTotalMs == nil || *sv.AvgTotalMs != 1500 {
		t.Errorf("avg_total_ms = %v, want 1500", sv.AvgTotalMs)
	}

	// Overview embeds the same 24h stats.
	v := overview(t, s)[0]
	if v.Stats.Samples != 6 || v.Stats.OK != 3 {
		t.Errorf("overview stats = %+v", v.Stats)
	}

	// No samples → null percentages, never 0 or 100.
	fresh := addProviderDirect(t, s.st, func(pp *store.Provider) { pp.Name = "fresh" })
	_, body = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d", fresh.ID), "", nil)
	if strings.Contains(body, `"ok_pct":0`) || strings.Contains(body, `"ok_pct":100`) {
		t.Errorf("fresh stats = %s, want null pct", body)
	}
	if !strings.Contains(body, `"ok_pct":null`) {
		t.Errorf("fresh stats = %s, want ok_pct null", body)
	}
}

func assertPct(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s pct = nil, want %v", name, want)
	}
	if d := *got - want; d > 0.01 || d < -0.01 {
		t.Errorf("%s pct = %.4f, want %.2f±0.01", name, *got, want)
	}
}

/* ---------- 10. revision scoping (§4.1, A1.8) ---------- */

func TestRevisionScoping(t *testing.T) {
	s, _, _ := newTestServer(t)
	p := addProviderDirect(t, s.st, nil)
	now := time.Now().UnixMilli()

	// Two results on revision 1.
	appendDirect(t, s.st, scheduledResult(p, "ok", now-60_000, i64(800)))
	appendDirect(t, s.st, scheduledResult(p, "http_error", now-30_000, nil))

	// Target change → revision 2 (via PUT, the real path).
	res, body := serve(t, s, "PUT", fmt.Sprintf("/api/providers/%d", p.ID),
		fmt.Sprintf(`{"name":"p","base_url":"http://127.0.0.1:9/v2","model":"m",
		  "max_tokens":128,"interval_sec":300,"timeout_sec":60,
		  "ttft_timeout_ms":10000,"ttft_slow_ms":2000,"enabled":true}`), nil)
	if res.StatusCode != 200 {
		t.Fatalf("PUT = %d: %s", res.StatusCode, body)
	}
	p2, _ := s.st.GetProvider(p.ID)
	if p2.Revision != 2 {
		t.Fatalf("revision = %d, want 2", p2.Revision)
	}
	// One result on revision 2.
	appendDirect(t, s.st, scheduledResult(p2, "ok", now-10_000, i64(300)))

	// Default (current revision): 1 sample.
	_, body = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d", p.ID), "", nil)
	if !strings.Contains(body, `"samples":1`) || !strings.Contains(body, `"ok":1`) {
		t.Errorf("current-revision stats = %s, want 1 ok sample", body)
	}

	// revision=all: 3 samples, 2 ok.
	_, body = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d&revision=all", p.ID), "", nil)
	if !strings.Contains(body, `"samples":3`) || !strings.Contains(body, `"ok":2`) {
		t.Errorf("all-revision stats = %s, want 3 samples / 2 ok", body)
	}

	// Explicit old revision: 2 samples on v1.
	_, body = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d&revision=1", p.ID), "", nil)
	if !strings.Contains(body, `"samples":2`) {
		t.Errorf("v1 stats = %s, want 2 samples", body)
	}

	// Overview: revision list exposes both, last_probe is the current-revision one.
	v := overview(t, s)[0]
	if len(v.Revisions) != 2 || v.Revisions[0] != 1 || v.Revisions[1] != 2 {
		t.Errorf("revisions = %v, want [1 2]", v.Revisions)
	}
	if v.LastProbe == nil || v.LastProbe.TTFTMs == nil || *v.LastProbe.TTFTMs != 300 {
		t.Errorf("last_probe = %+v, want the revision-2 result (ttft 300)", v.LastProbe)
	}

	// results honor revision=all too.
	_, body = serve(t, s, "GET",
		fmt.Sprintf("/api/results?provider=%d&revision=all&limit=50", p.ID), "", nil)
	if !strings.Contains(body, `"revision":1`) || !strings.Contains(body, `"revision":2`) {
		t.Errorf("all-revision results missing a revision: %s", body)
	}

	// Bad revision value → 400.
	res, _ = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d&revision=xyz", p.ID), "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("revision=xyz = %d, want 400", res.StatusCode)
	}
	// Bad window → 400.
	res, _ = serve(t, s, "GET", fmt.Sprintf("/api/stats?provider=%d&window=2h", p.ID), "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("window=2h = %d, want 400", res.StatusCode)
	}
	// Missing provider → 400; unknown provider → 404.
	res, _ = serve(t, s, "GET", "/api/stats", "", nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("no provider = %d, want 400", res.StatusCode)
	}
	res, _ = serve(t, s, "GET", "/api/stats?provider=999", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown provider = %d, want 404", res.StatusCode)
	}
}

/* ---------- extras: series shape, static files, delete ---------- */

func TestSeriesAndStaticAndDelete(t *testing.T) {
	s, _, mut := newTestServer(t)
	p := addProviderDirect(t, s.st, nil)
	now := time.Now().UnixMilli()
	appendDirect(t, s.st, scheduledResult(p, "ok", now-30_000, i64(700)))

	// series: buckets array with 24h grid (24 buckets), last bucket has the sample.
	res, body := serve(t, s, "GET",
		fmt.Sprintf("/api/series?provider=%d&window=24h", p.ID), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("series = %d: %s", res.StatusCode, body)
	}
	var series struct {
		Buckets []view.BucketView `json:"buckets"`
	}
	if err := json.Unmarshal([]byte(body), &series); err != nil {
		t.Fatalf("decode series: %v", err)
	}
	if len(series.Buckets) != 24 {
		t.Fatalf("buckets = %d, want 24", len(series.Buckets))
	}
	if !jsonHasKeys(body, []string{`"start_ms"`, `"samples"`, `"ok_pct"`, `"avg_ttft_ms"`, `"avg_total_ms"`}) {
		t.Errorf("bucket JSON missing snake_case keys: %s", body)
	}

	// static panel served from the embedded FS.
	res, body = serve(t, s, "GET", "/", "", nil)
	if res.StatusCode != 200 || !strings.Contains(body, "ok") {
		t.Errorf("GET / = %d", res.StatusCode)
	}
	res, _ = serve(t, s, "GET", "/nope.js", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope.js = %d, want 404", res.StatusCode)
	}

	// delete: config and history gone, engine notified.
	res, body = serve(t, s, "DELETE", fmt.Sprintf("/api/providers/%d", p.ID), "", nil)
	if res.StatusCode != 200 {
		t.Fatalf("DELETE = %d: %s", res.StatusCode, body)
	}
	if len(overview(t, s)) != 0 {
		t.Error("provider still listed after delete")
	}
	if s.st.ResultCount(p.ID) != 0 {
		t.Error("history still present after delete")
	}
	mut.mu.Lock()
	defer mut.mu.Unlock()
	// This provider was seeded directly into the store, so only the DELETE
	// notification is expected here (Add notifications are covered by the
	// POST path in TestOriginProtection).
	if len(mut.removed) != 1 || mut.removed[0] != p.ID {
		t.Errorf("mutator removed = %v, want [%d]", mut.removed, p.ID)
	}
	// second delete → 404.
	res, _ = serve(t, s, "DELETE", fmt.Sprintf("/api/providers/%d", p.ID), "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("re-DELETE = %d, want 404", res.StatusCode)
	}
}

func jsonHasKeys(body string, keys []string) bool {
	for _, k := range keys {
		if !strings.Contains(body, k) {
			return false
		}
	}
	return true
}

/* ---------- probing hint + storage error surfacing ---------- */

func TestProbingHintAndStorageError(t *testing.T) {
	s, eng, _ := newTestServer(t)
	p := addProviderDirect(t, s.st, nil)
	now := time.Now().UnixMilli()
	appendDirect(t, s.st, scheduledResult(p, "ok", now-30_000, i64(700)))

	eng.mu.Lock()
	eng.probing[p.ID] = true
	eng.storeErr = "append failed: disk full"
	eng.mu.Unlock()

	v := overview(t, s)[0]
	if !v.Probing {
		t.Error("probing = false, want true")
	}
	// Probing is a hint: status must stay "ok".
	if v.Status != "ok" {
		t.Errorf("status = %q, want ok (probing must not override)", v.Status)
	}
	if v.StorageError == "" {
		t.Error("storage_error empty, want engine error surfaced")
	}
}

// IncludeUsage round-trips through the API and the new throughput fields
// surface in stats (task 10-09 A5). The manual-probe response path is
// covered by TestProbeEndpoint's fake engine plus store.ResultTPS unit
// behavior in the store package.
func TestIncludeUsageRoundTripAndThroughputFields(t *testing.T) {
	s, _, _ := newTestServer(t)
	body := `{"name":"u","base_url":"http://127.0.0.1:1/v1","model":"m","prompt":"p",` +
		`"max_tokens":16,"interval_sec":0,"timeout_sec":30,"ttft_timeout_ms":10000,` +
		`"ttft_slow_ms":2000,"enabled":true,"include_usage":true}`
	code, respBody := mustPost(t, s, body, nil)
	if code != http.StatusCreated {
		t.Fatalf("create = %d: %s", code, respBody)
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal([]byte(respBody), &created); err != nil {
		t.Fatal(err)
	}

	_, raw := serve(t, s, "GET", "/api/providers", "", nil)
	if !strings.Contains(raw, `"include_usage":true`) {
		t.Fatalf("overview missing include_usage=true: %s", raw)
	}

	// A stored ok sample with usage feeds avg_decode_tps / avg_prefill_tps.
	prompt, completion := 64, 32
	ttft, total := int64(200), int64(1200)
	now := time.Now().UnixMilli()
	appendDirect(t, s.st, store.Result{
		ProviderID: created.ID, Revision: 1, BaseURL: "http://x/v1", Model: "m",
		Source: store.SourceScheduled, StartedAt: now - 1000, FinishedAt: now - 1000 + total,
		Success: true, TTFTMs: &ttft, TotalMs: total, Status: "ok",
		PromptTokens: &prompt, CompletionTokens: &completion,
	})

	_, rawStats := serve(t, s, "GET", "/api/stats?provider="+strconv.Itoa(created.ID)+"&window=1h", "", nil)
	for _, want := range []string{`"avg_decode_tps":31`, `"avg_prefill_tps":3`} {
		if !strings.Contains(rawStats, want) {
			t.Fatalf("stats missing %s: %s", want, rawStats)
		}
	}

	// The series buckets carry the same two fields for the sampled bucket.
	_, rawSeries := serve(t, s, "GET", "/api/series?provider="+strconv.Itoa(created.ID)+"&window=1h", "", nil)
	if !strings.Contains(rawSeries, `"avg_decode_tps":31`) || !strings.Contains(rawSeries, `"avg_prefill_tps":3`) {
		t.Fatalf("series missing throughput fields: %s", rawSeries)
	}

	// PUT without the field resets it to false (full-replace semantics,
	// consistent with the other boolean fields).
	res, _ := serve(t, s, "PUT", "/api/providers/"+strconv.Itoa(created.ID),
		`{"name":"u","base_url":"http://127.0.0.1:1/v1","model":"m","prompt":"p",`+
			`"max_tokens":16,"interval_sec":0,"timeout_sec":30,"ttft_timeout_ms":10000,`+
			`"ttft_slow_ms":2000,"enabled":true}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("PUT = %d", res.StatusCode)
	}
	_, raw2 := serve(t, s, "GET", "/api/providers", "", nil)
	if strings.Contains(raw2, `"include_usage":true`) {
		t.Fatalf("include_usage must reset to false on full-replace PUT: %s", raw2)
	}
}

// HTML report export — HTTP semantics only (task 10-09 阶段 2): headers,
// the download filename contract and parameter errors. Content assertions
// (escaping, key non-leakage, truncation, null-dash rendering) live in
// internal/view/report_test.go against the shared RenderReport pipeline.
func TestReportExport(t *testing.T) {
	s, _, _ := newTestServer(t)
	p := addProviderDirect(t, s.st, func(pp *store.Provider) { pp.Name = "report-target" })

	res, _ := serve(t, s, "GET", "/api/report?provider="+strconv.Itoa(p.ID)+"&window=24h", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("report = %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	cd := res.Header.Get("Content-Disposition")
	wantPrefix := "attachment; filename=\"llm-monitor-report-target-24h-"
	if !strings.HasPrefix(cd, wantPrefix) || !strings.HasSuffix(cd, ".html\"") {
		t.Fatalf("Content-Disposition = %q, want prefix %q", cd, wantPrefix)
	}
	if len(cd) != len(wantPrefix)+len("20060102-150405.html\"") {
		t.Fatalf("filename timestamp not yyyymmdd-hhmmss: %q", cd)
	}

	// Parameter errors behave like /api/stats.
	res2, _ := serve(t, s, "GET", "/api/report?provider=&window=24h", "", nil)
	if res2.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing provider = %d, want 400", res2.StatusCode)
	}
	res3, _ := serve(t, s, "GET", "/api/report?provider=99999", "", nil)
	if res3.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider = %d, want 404", res3.StatusCode)
	}
	res4, _ := serve(t, s, "GET", "/api/report?provider="+strconv.Itoa(p.ID)+"&source=bogus", "", nil)
	if res4.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad source = %d, want 400", res4.StatusCode)
	}
}

// CJK-only names fall back to provider-<id> in the download filename.
func TestReportFilenameCJKFallback(t *testing.T) {
	s, _, _ := newTestServer(t)
	p := addProviderDirect(t, s.st, func(pp *store.Provider) { pp.Name = "生产接口监控" })

	res, _ := serve(t, s, "GET", "/api/report?provider="+strconv.Itoa(p.ID)+"&window=1h", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("report = %d", res.StatusCode)
	}
	cd := res.Header.Get("Content-Disposition")
	wantPrefix := fmt.Sprintf("attachment; filename=\"llm-monitor-provider-%d-1h-", p.ID)
	if !strings.HasPrefix(cd, wantPrefix) || !strings.HasSuffix(cd, ".html\"") {
		t.Fatalf("Content-Disposition = %q, want prefix %q", cd, wantPrefix)
	}
}
