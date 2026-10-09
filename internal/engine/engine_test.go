package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-monitor/internal/probe"
	"llm-monitor/internal/store"
)

// probeServer is a controllable OpenAI-compatible SSE endpoint. It delays
// every response by delay and records each request body plus the maximum
// number of requests it ever served concurrently — the observable evidence
// for the single in-flight rule (§7.1).
type probeServer struct {
	srv   *httptest.Server
	delay time.Duration

	mu        sync.Mutex
	bodies    []string
	active    int
	maxActive int
}

func newProbeServer(t *testing.T, delay time.Duration) *probeServer {
	t.Helper()
	ps := &probeServer{delay: delay}
	ps.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ps.mu.Lock()
		ps.bodies = append(ps.bodies, string(body))
		ps.active++
		if ps.active > ps.maxActive {
			ps.maxActive = ps.active
		}
		ps.mu.Unlock()

		time.Sleep(ps.delay)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{\"content\":\"Hello world\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
				"data: [DONE]\n\n")
		ps.mu.Lock()
		ps.active--
		ps.mu.Unlock()
	}))
	t.Cleanup(ps.srv.Close)
	return ps
}

func (ps *probeServer) count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return len(ps.bodies)
}

func (ps *probeServer) maxConcurrent() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.maxActive
}

func (ps *probeServer) body(i int) string {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.bodies[i]
}

// promptOf extracts the user prompt from a captured chat/completions body.
func promptOf(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("parse request body %q: %v", body, err)
	}
	if len(req.Messages) == 0 {
		t.Fatalf("no messages in body %q", body)
	}
	return req.Messages[0].Content
}

func newTestEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return New(st, &http.Client{}), st // no Timeout: deadlines are ctx-driven
}

// addProvider persists a provider pointing at baseURL with the schedule
// given by interval/enabled and returns the stored form.
func addProvider(t *testing.T, st *store.Store, name, baseURL string, interval int, enabled bool) store.Provider {
	t.Helper()
	p, err := st.AddProvider(store.Provider{
		Name:          name,
		BaseURL:       baseURL,
		Prompt:        "old prompt",
		Model:         "test-model",
		MaxTokens:     32,
		TimeoutSec:    10,
		TTFTTimeoutMs: 5000,
		TTFTSlowMs:    500,
		IntervalSec:   interval,
		Enabled:       enabled,
	})
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	return p
}

func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

// results returns all stored results of the provider, newest first.
func results(t *testing.T, st *store.Store, id int) []store.Result {
	t.Helper()
	rs := st.QueryResults(id, store.RevisionAll, store.SourceAll, 24*time.Hour, 100, nil, nil)
	if len(rs) == 0 && st.ResultCount(id) != 0 {
		t.Fatalf("QueryResults returned nothing but ResultCount=%d", st.ResultCount(id))
	}
	return rs
}

// 1. In-flight skip: with interval shorter than the endpoint latency, ticks
// firing while a probe runs are skipped — never two concurrent requests,
// never a record without a request.
func TestScheduledSkipWhileInFlight(t *testing.T) {
	ps := newProbeServer(t, 1200*time.Millisecond) // slower than the 1s interval
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "skip", ps.srv.URL, 1, true)

	eng.Start(context.Background())
	time.Sleep(3300 * time.Millisecond) // several ticks, some must land mid-probe
	eng.Shutdown(5 * time.Second)       // waits until every record is persisted

	if got := ps.count(); got < 2 {
		t.Fatalf("endpoint saw %d requests, want >= 2 (ticks must keep firing)", got)
	}
	if got := ps.maxConcurrent(); got != 1 {
		t.Fatalf("max concurrent requests = %d, want 1 (in-flight limit)", got)
	}
	if got := st.ResultCount(p.ID); got != ps.count() {
		t.Fatalf("records = %d, requests = %d: every request must yield exactly one record, skips none", got, ps.count())
	}
	cancelled := 0
	for _, r := range results(t, st, p.ID) {
		if r.Source != store.SourceScheduled {
			t.Fatalf("record source = %q, want scheduled", r.Source)
		}
		switch r.Status {
		case probe.StatusOK:
		case probe.StatusCancelled:
			cancelled++ // at most the single probe cut short by Shutdown
		default:
			t.Fatalf("unexpected status %q (detail %q)", r.Status, r.Error)
		}
	}
	if cancelled > 1 {
		t.Fatalf("%d cancelled records, want <= 1 (only the probe in flight at shutdown)", cancelled)
	}
}

// 2. Update cancels the in-flight probe (persisted as cancelled, not as a
// target failure) and reschedules under the new configuration.
func TestUpdateCancelsInFlightAndAppliesNewConfig(t *testing.T) {
	ps := newProbeServer(t, 1500*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "edit", ps.srv.URL, 60, true)

	eng.Start(context.Background())
	waitFor(t, 3*time.Second, "first scheduled probe to start", func() bool { return ps.count() >= 1 })

	started := time.Now()
	p.Prompt = "brand new prompt"
	eng.Update(p)
	// Update must return via cancellation, not by waiting out the 1.5s response.
	if el := time.Since(started); el > 1000*time.Millisecond {
		t.Fatalf("Update took %s, want fast cancellation of the in-flight probe", el)
	}

	waitFor(t, 8*time.Second, "probe under the new config to finish", func() bool {
		return st.ResultCount(p.ID) >= 2
	})

	rs := results(t, st, p.ID) // newest first
	if len(rs) != 2 {
		t.Fatalf("got %d records, want 2", len(rs))
	}
	if rs[0].Status != probe.StatusOK {
		t.Fatalf("newest status = %q, want ok (detail %q)", rs[0].Status, rs[0].Error)
	}
	if rs[1].Status != probe.StatusCancelled || rs[1].Success {
		t.Fatalf("oldest status = %q success=%v, want cancelled/false (operator action)", rs[1].Status, rs[1].Success)
	}
	if got := promptOf(t, ps.body(0)); got != "old prompt" {
		t.Fatalf("first request prompt = %q, want old config", got)
	}
	if got := promptOf(t, ps.body(1)); got != "brand new prompt" {
		t.Fatalf("second request prompt = %q, want new config", got)
	}
	eng.Shutdown(2 * time.Second)
}

// 3. ProbeNow conflict: while a probe is in flight the shared slot rejects a
// second one with ErrInFlight, and Probing reports the activity.
func TestProbeNowConflict(t *testing.T) {
	ps := newProbeServer(t, 800*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "conflict", ps.srv.URL, 0, true) // manual-only: no scheduled interference
	eng.Start(context.Background())

	type outcome struct {
		res *store.Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := eng.ProbeNow(p.ID)
		done <- outcome{res, err}
	}()
	waitFor(t, 3*time.Second, "manual probe to start", func() bool { return ps.count() >= 1 })

	if !eng.Probing(p.ID) {
		t.Fatal("Probing = false while a manual probe is in flight")
	}
	if _, err := eng.ProbeNow(p.ID); !errors.Is(err, ErrInFlight) {
		t.Fatalf("second ProbeNow error = %v, want ErrInFlight", err)
	}

	select {
	case oc := <-done:
		if oc.err != nil {
			t.Fatalf("first ProbeNow: %v", oc.err)
		}
		if oc.res.Status != probe.StatusOK {
			t.Fatalf("manual result status = %q, want ok", oc.res.Status)
		}
		if oc.res.Source != store.SourceManual {
			t.Fatalf("manual result source = %q, want manual", oc.res.Source)
		}
		if oc.res.OutputPreview != "Hello world" {
			t.Fatalf("OutputPreview = %q, want the streamed text", oc.res.OutputPreview)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual probe did not finish")
	}
	if eng.Probing(p.ID) {
		t.Fatal("Probing = true after the probe finished")
	}
	eng.Shutdown(2 * time.Second)
}

// 4. ProbeNow on a manual-only provider returns synchronously with a
// persisted manual record carrying the output preview.
func TestProbeNowReturnsManualResult(t *testing.T) {
	ps := newProbeServer(t, 300*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "manual", ps.srv.URL, 0, true)
	eng.Start(context.Background())

	res, err := eng.ProbeNow(p.ID)
	if err != nil {
		t.Fatalf("ProbeNow: %v", err)
	}
	if res.Status != probe.StatusOK || !res.Success {
		t.Fatalf("status = %q success = %v, want ok/true (error %q)", res.Status, res.Success, res.Error)
	}
	if res.Source != store.SourceManual {
		t.Fatalf("source = %q, want manual", res.Source)
	}
	if res.OutputPreview == "" {
		t.Fatal("manual result must carry an output preview")
	}
	// The returned record must be the persisted one: same Seq as on disk.
	if res.Seq != 1 {
		t.Fatalf("returned Seq = %d, want 1 (assigned by AppendResult)", res.Seq)
	}
	if st.ResultCount(p.ID) != 1 {
		t.Fatalf("ResultCount = %d, want 1", st.ResultCount(p.ID))
	}
	eng.Shutdown(2 * time.Second)
}

// 5. Remove cancels the in-flight probe (cancelled record persisted) and
// stops scheduling: no further requests reach the endpoint.
func TestRemoveCancelsAndStopsScheduling(t *testing.T) {
	ps := newProbeServer(t, 1500*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "gone", ps.srv.URL, 60, true)

	eng.Start(context.Background())
	waitFor(t, 3*time.Second, "scheduled probe to start", func() bool { return ps.count() >= 1 })

	eng.Remove(p.ID)

	rs := results(t, st, p.ID)
	if len(rs) != 1 || rs[0].Status != probe.StatusCancelled || rs[0].Source != store.SourceScheduled {
		t.Fatalf("records after Remove = %+v, want one cancelled scheduled record", rs)
	}
	time.Sleep(400 * time.Millisecond)
	if got := ps.count(); got != 1 {
		t.Fatalf("endpoint saw %d requests after Remove, want 1 (no further scheduling)", got)
	}
	if got := st.ResultCount(p.ID); got != 1 {
		t.Fatalf("ResultCount = %d after Remove, want 1", got)
	}
	if _, err := eng.ProbeNow(p.ID); err == nil {
		t.Fatal("ProbeNow on a removed provider must fail")
	}
	eng.Shutdown(2 * time.Second)
}

// 6. Shutdown cancels in-flight probes and returns only after their
// cancelled records are persisted — for scheduled and for manual probes.
func TestShutdownCancelsInFlightScheduled(t *testing.T) {
	ps := newProbeServer(t, 2000*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "sd", ps.srv.URL, 60, true)

	eng.Start(context.Background())
	waitFor(t, 3*time.Second, "scheduled probe to start", func() bool { return ps.count() >= 1 })

	started := time.Now()
	eng.Shutdown(8 * time.Second)
	if el := time.Since(started); el > 1000*time.Millisecond {
		t.Fatalf("Shutdown took %s, want fast cancellation rather than waiting out the 2s response", el)
	}
	rs := results(t, st, p.ID)
	if len(rs) != 1 || rs[0].Status != probe.StatusCancelled {
		t.Fatalf("records right after Shutdown = %+v, want one cancelled record persisted before return", rs)
	}
}

func TestShutdownCancelsInFlightManual(t *testing.T) {
	ps := newProbeServer(t, 2000*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "sdman", ps.srv.URL, 0, true)
	eng.Start(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := eng.ProbeNow(p.ID); err != nil {
			t.Errorf("ProbeNow during shutdown: %v", err)
		}
	}()
	waitFor(t, 3*time.Second, "manual probe to start", func() bool { return ps.count() >= 1 })

	eng.Shutdown(8 * time.Second)
	rs := results(t, st, p.ID)
	if len(rs) != 1 || rs[0].Status != probe.StatusCancelled || rs[0].Source != store.SourceManual {
		t.Fatalf("records right after Shutdown = %+v, want one cancelled manual record persisted before return", rs)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("manual ProbeNow goroutine still running after Shutdown returned")
	}
}

// 7. Disabled and manual-only providers are never auto-probed, yet remain
// manually testable (§4.6).
func TestNoAutoProbeWhenDisabledOrManualOnly(t *testing.T) {
	psManual := newProbeServer(t, 100*time.Millisecond)
	psDisabled := newProbeServer(t, 100*time.Millisecond)
	eng, st := newTestEngine(t)
	pManual := addProvider(t, st, "manual-only", psManual.srv.URL, 0, true)   // enabled, interval 0
	pDisabled := addProvider(t, st, "disabled", psDisabled.srv.URL, 1, false) // would tick every 1s

	eng.Start(context.Background())
	// 1300ms covers both failure modes: an (incorrect) immediate probe and
	// the first tick of a 1s interval if Enabled were ignored.
	time.Sleep(1300 * time.Millisecond)
	if got := psManual.count() + psDisabled.count(); got != 0 {
		t.Fatalf("auto-probes fired %d times, want 0 (manual-only and disabled must not schedule)", got)
	}

	for _, p := range []store.Provider{pManual, pDisabled} {
		res, err := eng.ProbeNow(p.ID)
		if err != nil {
			t.Fatalf("ProbeNow(%q): %v", p.Name, err)
		}
		if res.Status != probe.StatusOK || res.Source != store.SourceManual {
			t.Fatalf("ProbeNow(%q) = %q/%q, want ok/manual", p.Name, res.Status, res.Source)
		}
	}
	eng.Shutdown(2 * time.Second)
}

// 8. A persist failure is surfaced through StorageError (naming the
// provider, never the API key) instead of being masked as a probe failure.
// The failure is injected by removing the provider from the store directly,
// which makes AppendResult fail on the next completion.
func TestStorageErrorSurfaced(t *testing.T) {
	ps := newProbeServer(t, 200*time.Millisecond)
	eng, st := newTestEngine(t)
	p := addProvider(t, st, "broken", ps.srv.URL, 60, true)

	eng.Start(context.Background())
	waitFor(t, 3*time.Second, "scheduled probe to start", func() bool { return ps.count() >= 1 })
	if err := st.DeleteProvider(p.ID); err != nil { // store-level failure injection
		t.Fatalf("DeleteProvider: %v", err)
	}

	waitFor(t, 3*time.Second, "engine to hit the persist failure", func() bool {
		return eng.StorageError() != ""
	})
	msg := eng.StorageError()
	if !strings.Contains(msg, "persist") || !strings.Contains(msg, "id") {
		t.Fatalf("StorageError = %q, want a persist failure naming the provider", msg)
	}
	if st.ResultCount(p.ID) != 0 {
		t.Fatalf("ResultCount = %d, want 0 (nothing could be persisted)", st.ResultCount(p.ID))
	}
	eng.Shutdown(2 * time.Second)
}

// 9. Capacity (§5.3): 20 providers on a shared engine with short intervals
// schedule independently — every provider gets probed, none is starved, and
// one failing provider (dead endpoint) does not block the others.
func TestTwentyProvidersScheduleIndependently(t *testing.T) {
	ps := newProbeServer(t, 100*time.Millisecond)
	eng, st := newTestEngine(t)
	const n = 20
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		url := ps.srv.URL
		if i == n-1 { // one provider points at a dead port
			url = "http://127.0.0.1:1"
		}
		p := addProvider(t, st, fmt.Sprintf("p%d", i), url, 1, true)
		ids = append(ids, p.ID)
	}

	eng.Start(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st.ResultCount(ids[0]) >= 2 && st.ResultCount(ids[n-2]) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	eng.Shutdown(5 * time.Second)

	healthy := ids[:n-1]
	for _, id := range healthy {
		if c := st.ResultCount(id); c < 2 {
			t.Fatalf("provider %d got %d records, want >= 2 (starved?)", id, c)
		}
	}
	// The dead provider must fail fast with conn_error, never block others.
	rs := results(t, st, ids[n-1])
	if len(rs) == 0 || rs[0].Status != probe.StatusConnError {
		t.Fatalf("dead provider status = %v, want conn_error", rs)
	}
	if ps.maxConcurrent() > n {
		t.Fatalf("max concurrent = %d, want <= %d", ps.maxConcurrent(), n)
	}
}

// IncludeUsage flows from the provider config into the probe request and the
// captured usage lands in the persisted Result (task 10-09 S3).
func TestIncludeUsageFlowsToRequestAndResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"include_usage":true`) {
			t.Errorf("request missing stream_options.include_usage: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w,
			"data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n"+
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5}}\n\n"+
				"data: [DONE]\n\n")
	}))
	defer srv.Close()
	eng, st := newTestEngine(t)
	// interval 0 = manual-only: no scheduler racing ProbeNow for the shared
	// in-flight slot (same pattern as TestProbeNowConflict).
	p := addProvider(t, st, "usage", srv.URL, 0, true)
	p.IncludeUsage = true
	if err := st.UpdateProvider(p); err != nil {
		t.Fatal(err)
	}
	eng.Add(p) // register the runner so ProbeNow finds it

	res, err := eng.ProbeNow(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.PromptTokens == nil || *res.PromptTokens != 12 {
		t.Fatalf("PromptTokens = %v, want 12", res.PromptTokens)
	}
	if res.CompletionTokens == nil || *res.CompletionTokens != 5 {
		t.Fatalf("CompletionTokens = %v, want 5", res.CompletionTokens)
	}
}
