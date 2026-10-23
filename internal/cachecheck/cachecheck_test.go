package cachecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-monitor/internal/probe"
)

func testClient() *http.Client {
	return &http.Client{} // no Timeout: deadlines are owned by the hops
}

// sseWrite writes SSE lines followed by a newline each and flushes, so the
// client observes events incrementally (same pattern as probe_test.go).
func sseWrite(w http.ResponseWriter, lines ...string) {
	for _, l := range lines {
		fmt.Fprintf(w, "%s\n", l)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// hitEvents builds a minimal ok stream whose usage event carries the given
// cached_tokens value; cached == "" strips prompt_tokens_details entirely.
func hitEvents(cached string) []string {
	usage := `"usage":{"prompt_tokens":2048,"completion_tokens":8`
	if cached != "" {
		usage += `,"prompt_tokens_details":{"cached_tokens":` + cached + `}`
	}
	usage += `}`
	return []string{
		`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"stop"}],` + usage + `}`,
		"",
		"data: [DONE]",
		"",
	}
}

// hopScript describes what the mock endpoint serves for one request: either
// a non-2xx status or a full SSE event list.
type hopScript struct {
	httpStatus int
	events     []string
}

func okHop(cached string) hopScript { return hopScript{events: hitEvents(cached)} }

// emptyEvents reproduces the zero-content stream observed on a real endpoint
// (2026-10-23): role delta, a usage-bearing finish_reason=length, [DONE] —
// the completion budget burns entirely on invisible output, so probe
// classifies the hop as empty while the prefill-side evidence stays intact.
func emptyEvents(cached string) []string {
	usage := `"usage":{"prompt_tokens":2048,"completion_tokens":8`
	if cached != "" {
		usage += `,"prompt_tokens_details":{"cached_tokens":` + cached + `}`
	}
	usage += `}`
	return []string{
		`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}`,
		"",
		`data: {"choices":[{"delta":{},"finish_reason":"length"}],` + usage + `}`,
		"",
		"data: [DONE]",
		"",
	}
}

func emptyHop(cached string) hopScript { return hopScript{events: emptyEvents(cached)} }

func failHop(status int) hopScript { return hopScript{httpStatus: status} }

// scriptServer answers requests in order from scripts (further requests
// repeat the last script) and records each request body.
func scriptServer(t *testing.T, scripts []hopScript) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		i := n
		if i >= len(scripts) {
			i = len(scripts) - 1
		}
		n++
		bodies = append(bodies, string(body))
		sc := scripts[i]
		mu.Unlock()

		if sc.httpStatus != 0 {
			http.Error(w, "upstream failure", sc.httpStatus)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		sseWrite(w, sc.events...)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(bodies))
		copy(out, bodies)
		return out
	}
}

func intPtr(v int) *int       { return &v }
func int64Ptr(v int64) *int64 { return &v }

// The verdict matrix as a pure function (prd R1): each row builds a Result
// by hand and checks judge's derivation, including the first-failed-hop rule
// and the 0-vs-nil boundary between unsupported and unknown.
func TestJudgeMatrix(t *testing.T) {
	cases := []struct {
		name        string
		hops        [2]Hop
		wantVerdict string
		wantFailed  int
	}{
		{
			name:        "both hops hit",
			hops:        [2]Hop{{Status: probe.StatusOK, CachedTokens: int64Ptr(1024)}, {Status: probe.StatusOK, CachedTokens: int64Ptr(2048)}},
			wantVerdict: VerdictSupported,
		},
		{
			name:        "only warm hop hits",
			hops:        [2]Hop{{Status: probe.StatusOK, CachedTokens: int64Ptr(0)}, {Status: probe.StatusOK, CachedTokens: int64Ptr(2048)}},
			wantVerdict: VerdictSupported,
		},
		{
			name:        "reported zero on both hops",
			hops:        [2]Hop{{Status: probe.StatusOK, CachedTokens: int64Ptr(0)}, {Status: probe.StatusOK, CachedTokens: int64Ptr(0)}},
			wantVerdict: VerdictUnsupported,
		},
		{
			name:        "stripped on both hops",
			hops:        [2]Hop{{Status: probe.StatusOK}, {Status: probe.StatusOK}},
			wantVerdict: VerdictUnknown,
		},
		{
			// prd R1: one hop reporting > 0 is conclusive even when the other
			// hop's field was stripped by a relay.
			name:        "stripped on first hop, second hop positive",
			hops:        [2]Hop{{Status: probe.StatusOK}, {Status: probe.StatusOK, CachedTokens: int64Ptr(2048)}},
			wantVerdict: VerdictSupported,
		},
		{
			name:        "stripped on second hop only",
			hops:        [2]Hop{{Status: probe.StatusOK, CachedTokens: int64Ptr(0)}, {Status: probe.StatusOK}},
			wantVerdict: VerdictUnknown,
		},
		{
			// empty (200 + clean DONE + usage, zero content) is a usable
			// observation, not a failure — the live incident of 2026-10-23.
			name:        "empty hops with zero cached",
			hops:        [2]Hop{{Status: probe.StatusEmpty, CachedTokens: int64Ptr(0)}, {Status: probe.StatusEmpty, CachedTokens: int64Ptr(0)}},
			wantVerdict: VerdictUnsupported,
		},
		{
			name:        "empty hop with positive cached",
			hops:        [2]Hop{{Status: probe.StatusEmpty, CachedTokens: int64Ptr(0)}, {Status: probe.StatusEmpty, CachedTokens: int64Ptr(2048)}},
			wantVerdict: VerdictSupported,
		},
		{
			name:        "ok hop plus empty hop with stripped field",
			hops:        [2]Hop{{Status: probe.StatusOK, CachedTokens: int64Ptr(0)}, {Status: probe.StatusEmpty}},
			wantVerdict: VerdictUnknown,
		},
		{
			name:        "first hop failed wins over later evidence",
			hops:        [2]Hop{{Status: probe.StatusHTTPError, HTTPStatus: intPtr(500), ErrDetail: "HTTP 500: boom"}, {Status: probe.StatusOK, CachedTokens: int64Ptr(2048)}},
			wantVerdict: VerdictFailed,
			wantFailed:  1,
		},
		{
			name:        "second hop failed",
			hops:        [2]Hop{{Status: probe.StatusOK, CachedTokens: int64Ptr(0)}, {Status: probe.StatusTimeoutTotal, ErrDetail: "timed out before the stream completed"}},
			wantVerdict: VerdictFailed,
			wantFailed:  2,
		},
		{
			name:        "both hops failed reports the first",
			hops:        [2]Hop{{Status: probe.StatusConnError, ErrDetail: "dial refused"}, {Status: probe.StatusConnError, ErrDetail: "dial refused"}},
			wantVerdict: VerdictFailed,
			wantFailed:  1,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			res := judge(Result{Hops: tc.hops})
			if res.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q (evidence %q)", res.Verdict, tc.wantVerdict, res.Evidence)
			}
			if res.FailedHop != tc.wantFailed {
				t.Fatalf("FailedHop = %d, want %d", res.FailedHop, tc.wantFailed)
			}
		})
	}
}

// End-to-end Run against scripted SSE mocks (prd A1–A4): both hops always
// execute (no short-circuit), the request shape carries the fixed detection
// parameters (include_usage forced on, max_tokens=8, the deterministic
// prompt on both hops), and evidence names the deciding numbers.
func TestRunVerdicts(t *testing.T) {
	cases := []struct {
		name           string
		scripts        []hopScript
		wantVerdict    string
		wantFailedHop  int
		evidenceHas    []string
		wantCachedHops [2]*int64
	}{
		{
			name:           "hit on both hops",
			scripts:        []hopScript{okHop("1024"), okHop("2048")},
			wantVerdict:    VerdictSupported,
			evidenceHas:    []string{"hop1=1024", "hop2=2048"},
			wantCachedHops: [2]*int64{int64Ptr(1024), int64Ptr(2048)},
		},
		{
			name:           "hit on second hop only",
			scripts:        []hopScript{okHop("0"), okHop("2048")},
			wantVerdict:    VerdictSupported,
			evidenceHas:    []string{"hop1=0", "hop2=2048"},
			wantCachedHops: [2]*int64{int64Ptr(0), int64Ptr(2048)},
		},
		{
			name:           "present but always zero",
			scripts:        []hopScript{okHop("0"), okHop("0")},
			wantVerdict:    VerdictUnsupported,
			evidenceHas:    []string{"hop1=0", "hop2=0"},
			wantCachedHops: [2]*int64{int64Ptr(0), int64Ptr(0)},
		},
		{
			name:           "prompt_tokens_details stripped",
			scripts:        []hopScript{okHop(""), okHop("")},
			wantVerdict:    VerdictUnknown,
			evidenceHas:    []string{"TTFT", "ratio", "auxiliary only"},
			wantCachedHops: [2]*int64{nil, nil},
		},
		{
			// The 2026-10-23 incident, replayed end-to-end: both hops return
			// 200 + usage with cached_tokens 0 but no content delta. The
			// check must judge from the usage evidence, not report failed.
			name:           "zero-content streams with cached 0",
			scripts:        []hopScript{emptyHop("0"), emptyHop("0")},
			wantVerdict:    VerdictUnsupported,
			wantCachedHops: [2]*int64{int64Ptr(0), int64Ptr(0)},
		},
		{
			name:           "first hop fails",
			scripts:        []hopScript{failHop(500), okHop("2048")},
			wantVerdict:    VerdictFailed,
			wantFailedHop:  1,
			evidenceHas:    []string{"hop 1", probe.StatusHTTPError, "HTTP 500"},
			wantCachedHops: [2]*int64{nil, int64Ptr(2048)},
		},
		{
			name:           "second hop fails",
			scripts:        []hopScript{okHop("0"), failHop(502)},
			wantVerdict:    VerdictFailed,
			wantFailedHop:  2,
			evidenceHas:    []string{"hop 2", probe.StatusHTTPError},
			wantCachedHops: [2]*int64{int64Ptr(0), nil},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv, bodies := scriptServer(t, tc.scripts)
			res := Run(context.Background(), testClient(), Target{BaseURL: srv.URL, Model: "test-model"})
			if res.Verdict != tc.wantVerdict {
				t.Fatalf("verdict = %q, want %q (evidence %q)", res.Verdict, tc.wantVerdict, res.Evidence)
			}
			if res.FailedHop != tc.wantFailedHop {
				t.Fatalf("FailedHop = %d, want %d", res.FailedHop, tc.wantFailedHop)
			}
			for _, frag := range tc.evidenceHas {
				if !strings.Contains(res.Evidence, frag) {
					t.Fatalf("evidence %q must mention %q", res.Evidence, frag)
				}
			}
			// Both hops always ran, with per-hop evidence populated.
			got := bodies()
			if len(got) != 2 {
				t.Fatalf("endpoint saw %d requests, want 2 (no short-circuit, prd R2)", len(got))
			}
			for i := range res.Hops {
				h := &res.Hops[i]
				if h.Status == "" {
					t.Fatalf("hop %d has no status", i+1)
				}
				if want := tc.wantCachedHops[i]; want == nil && h.CachedTokens != nil {
					t.Fatalf("hop %d CachedTokens = %d, want nil", i+1, *h.CachedTokens)
				} else if want != nil {
					if h.CachedTokens == nil {
						t.Fatalf("hop %d CachedTokens = nil, want %d", i+1, *want)
					}
					if *h.CachedTokens != *want {
						t.Fatalf("hop %d CachedTokens = %d, want %d", i+1, *h.CachedTokens, *want)
					}
				}
			}
			// Request shape: fixed detection parameters, identical on both hops.
			for i, body := range got {
				var req struct {
					MaxTokens     int            `json:"max_tokens"`
					Stream        bool           `json:"stream"`
					StreamOptions map[string]any `json:"stream_options"`
					Messages      []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.Unmarshal([]byte(body), &req); err != nil {
					t.Fatalf("parse request %d: %v", i+1, err)
				}
				if req.MaxTokens != 8 || !req.Stream {
					t.Fatalf("request %d max_tokens=%d stream=%v, want 8/true", i+1, req.MaxTokens, req.Stream)
				}
				if v, ok := req.StreamOptions["include_usage"].(bool); !ok || !v {
					t.Fatalf("request %d stream_options = %v, want include_usage forced on", i+1, req.StreamOptions)
				}
				if len(req.Messages) != 1 || req.Messages[0].Content != detectionPrompt {
					t.Fatalf("request %d must carry the fixed detection prompt", i+1)
				}
			}
		})
	}
}

// Timeout: a hop that never completes makes the whole check failed (prd A4).
// The production budget is the fixed 60s; runHops shrinks it here so the
// test exercises the genuine deadline classification quickly. With the TTFT
// deadline equal to the total deadline and no content ever arriving, the
// hop classifies as timeout_ttft.
func TestRunTimeoutFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
				sseWrite(w, ": ping")
			}
		}
	}))
	defer srv.Close()

	res := runHops(context.Background(), testClient(), Target{BaseURL: srv.URL, Model: "test-model"}, 300)
	if res.Verdict != VerdictFailed {
		t.Fatalf("verdict = %q, want failed (evidence %q)", res.Verdict, res.Evidence)
	}
	if res.FailedHop != 1 {
		t.Fatalf("FailedHop = %d, want 1", res.FailedHop)
	}
	if res.Hops[0].Status != probe.StatusTimeoutTTFT {
		t.Fatalf("hop 1 status = %q, want timeout_ttft", res.Hops[0].Status)
	}
	if !strings.Contains(res.Evidence, "hop 1") || !strings.Contains(res.Evidence, probe.StatusTimeoutTTFT) {
		t.Fatalf("evidence %q must name the failed hop and its status", res.Evidence)
	}
}

// The deterministic prompt must stay in the designed 12–18KB window: ×64
// repetitions were counted as only 1748 prompt tokens by a real endpoint —
// below the 2048-token activation threshold some providers apply — so ×96
// (~2620 counted tokens) clears it with margin. Both hops share the prompt
// byte-for-byte by construction.
func TestDetectionPromptShape(t *testing.T) {
	if len(detectionPrompt) < 12*1024 {
		t.Fatalf("detection prompt is %d bytes, want >= 12288", len(detectionPrompt))
	}
	if len(detectionPrompt) > 18*1024 {
		t.Fatalf("detection prompt is %d bytes, want <= 18432", len(detectionPrompt))
	}
	// Conservative English heuristic: ~4 bytes per token (whitespace
	// included) must bound ~2600 tokens from below even if a tokenizer
	// counts as loosely as the observed 1748-per-9.5KB one.
	if len(detectionPrompt)/4 < 2600 {
		t.Fatalf("detection prompt est. %d tokens, want >= 2600", len(detectionPrompt)/4)
	}
	if !strings.HasSuffix(detectionPrompt, " ") || strings.Contains(detectionPrompt[:len(detectionPrompt)-1], "  ") {
		t.Fatal("detection prompt must be a clean repetition with single separators")
	}
}
