package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testModel = "test-model"

func testTarget(base string, ttftMs, totalMs int) Target {
	return Target{
		BaseURL:        base,
		Model:          testModel,
		Prompt:         "Reply with a short greeting.",
		MaxTokens:      128,
		TTFTTimeoutMs:  ttftMs,
		TotalTimeoutMs: totalMs,
	}
}

func testClient() *http.Client {
	return &http.Client{} // no Timeout: deadlines are owned by probe.Do
}

// sseWrite writes SSE lines followed by a newline each and flushes, so the
// client observes events incrementally.
func sseWrite(w http.ResponseWriter, lines ...string) {
	for _, l := range lines {
		fmt.Fprintf(w, "%s\n", l)
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func assertStatus(t *testing.T, o Outcome, want string) {
	t.Helper()
	if o.Status != want {
		t.Fatalf("status = %q, want %q (detail: %q)", o.Status, want, o.ErrDetail)
	}
}

func assertHTTPStatus(t *testing.T, o Outcome, want int) {
	t.Helper()
	if o.HTTPStatus == nil {
		t.Fatalf("HTTPStatus = nil, want %d", want)
	}
	if *o.HTTPStatus != want {
		t.Fatalf("HTTPStatus = %d, want %d", *o.HTTPStatus, want)
	}
}

// 1. Normal stream: role event -> text deltas -> finish_reason=length -> [DONE] => ok.
func TestNormalStreamOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w,
			`data: {"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			"",
			`data: {"choices":[{"index":0,"delta":{"content":"Hello "}}]}`,
			`data: {"choices":[{"index":0,"delta":{"content":"world"}}]}`,
			`data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`,
			"",
			"data: [DONE]",
			"",
		)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusOK)
	assertHTTPStatus(t, o, 200)
	if o.TTFTMs == nil || *o.TTFTMs <= 0 {
		t.Fatalf("TTFTMs = %v, want > 0", o.TTFTMs)
	}
	if o.ErrDetail != "" {
		t.Fatalf("ErrDetail = %q, want empty on success", o.ErrDetail)
	}
	if o.OutputPreview != "Hello world" {
		t.Fatalf("OutputPreview = %q, want %q", o.OutputPreview, "Hello world")
	}
}

// 2. Role/empty events first, text delayed by 300ms: they do not count as
// first content and TTFT must match the actual text arrival (±150ms).
func TestEmptyEventsDoNotCountAsTTFT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w,
			`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
			`data: {"choices":[{"delta":{}}]}`,
			": keep-alive",
			"",
		)
		time.Sleep(300 * time.Millisecond)
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"late"}}]}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusOK)
	if o.TTFTMs == nil {
		t.Fatal("TTFTMs = nil, want measured value")
	}
	ttft := *o.TTFTMs
	if ttft < 150 || ttft > 450 {
		t.Fatalf("TTFTMs = %d, want 150..450ms (text arrives at ~300ms)", ttft)
	}
}

// 3. Only heartbeat comments, no content: TTFT deadline fires => timeout_ttft.
func TestTTFTTimeoutWithHeartbeatOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 200, 2000))
	assertStatus(t, o, StatusTimeoutTTFT)
	if o.TTFTMs != nil {
		t.Fatalf("TTFTMs = %v, want nil (no valid content)", *o.TTFTMs)
	}
	if o.TotalMs > 1500 {
		t.Fatalf("TotalMs = %d, probe should end near the 200ms deadline", o.TotalMs)
	}
}

// 4. Text arrives, then heartbeats never end: total deadline fires mid-stream
// => timeout_total, and the measured TTFT is preserved.
func TestTotalTimeoutMidStreamKeepsTTFT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, `data: {"choices":[{"delta":{"content":"partial text"}}]}`)
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

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 400, 500))
	assertStatus(t, o, StatusTimeoutTotal)
	// Text was sent immediately on loopback, so the measured TTFT can be
	// 0ms after millisecond truncation; what matters is that it was kept.
	if o.TTFTMs == nil || *o.TTFTMs < 0 {
		t.Fatalf("TTFTMs = %v, want preserved measured value", o.TTFTMs)
	}
	if o.OutputPreview != "partial text" {
		t.Fatalf("OutputPreview = %q, want %q", o.OutputPreview, "partial text")
	}
}

// 5. Both deadlines equal and no content at all: simultaneous expiry is
// classified as timeout_ttft.
func TestSimultaneousDeadlinesNoContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never answer. A handler that neither writes nor reads the body
		// learns about client disconnects only via a server-side timeout,
		// so bail out on our own to keep srv.Close() from hanging.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 200, 200))
	assertStatus(t, o, StatusTimeoutTTFT)
	if o.TTFTMs != nil {
		t.Fatalf("TTFTMs = %v, want nil", *o.TTFTMs)
	}
}

// 6. HTTP non-2xx statuses => http_error with the actual status code.
func TestHTTPErrorStatuses(t *testing.T) {
	for _, code := range []int{401, 429, 500} {
		code := code
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, fmt.Sprintf("upstream failure %d", code), code)
			}))
			defer srv.Close()

			o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
			assertStatus(t, o, StatusHTTPError)
			assertHTTPStatus(t, o, code)
			if !strings.Contains(o.ErrDetail, fmt.Sprintf("HTTP %d", code)) {
				t.Fatalf("ErrDetail = %q, want it to mention HTTP %d", o.ErrDetail, code)
			}
		})
	}
}

// 7. HTTP 200 with an in-stream error event => stream_error (not success).
func TestStreamErrorEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w,
			`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
			`data: {"error":{"message":"insufficient quota","type":"insufficient_quota"}}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusStreamError)
	assertHTTPStatus(t, o, 200)
	if !strings.Contains(o.ErrDetail, "insufficient quota") {
		t.Fatalf("ErrDetail = %q, want it to include the error message", o.ErrDetail)
	}
}

// 8. Unparseable data event => protocol_error.
func TestProtocolErrorUnparseableData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, "data: not-json")
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusProtocolError)
	assertHTTPStatus(t, o, 200)
}

// 9. Partial text then an abnormal disconnect without end evidence => aborted.
func TestAbortedAfterPartialText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close() // abrupt break: no [DONE], no finish_reason
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusAborted)
	if o.TTFTMs == nil {
		t.Fatal("TTFTMs = nil, want preserved measured value")
	}
}

// 10. finish_reason set, no [DONE], then clean EOF => ok (compatible endpoints).
func TestFinishReasonEOFOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// "data:" without the optional space must be accepted too.
		sseWrite(w,
			`data:{"choices":[{"delta":{"content":"hi"}}]}`,
			`data:{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusOK)
	if o.OutputPreview != "hi" {
		t.Fatalf("OutputPreview = %q, want %q", o.OutputPreview, "hi")
	}
}

// 11. Normal [DONE] end but no text at all => empty.
func TestEmptyStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w,
			`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusEmpty)
	if o.TTFTMs != nil {
		t.Fatalf("TTFTMs = %v, want nil (no content)", *o.TTFTMs)
	}
}

// 12. Choice-less usage events are skipped and do not affect judgment.
func TestUsageEventSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"answer"}}]}`,
			`data: {"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2}}`,
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusOK)
}

// 13. Bare JSON 200 response without SSE framing => protocol_error.
func TestBareJSONBodyProtocolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"hi"}}]}`)
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusProtocolError)
	assertHTTPStatus(t, o, 200)
}

// 14. The API key must never appear verbatim in ErrDetail.
func TestKeyMaskedInErrorDetail(t *testing.T) {
	const key = "sk-test-secret-0123456789"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid key sk-test-secret-0123456789 provided"}}`, 401)
	}))
	defer srv.Close()

	tg := testTarget(srv.URL, 2000, 5000)
	tg.APIKey = key
	o := Do(context.Background(), testClient(), tg)
	assertStatus(t, o, StatusHTTPError)
	if strings.Contains(o.ErrDetail, key) {
		t.Fatalf("ErrDetail leaks the API key: %q", o.ErrDetail)
	}
	if !strings.Contains(o.ErrDetail, "***") {
		t.Fatalf("ErrDetail = %q, want masked key", o.ErrDetail)
	}
}

// 15. Base URL with a trailing slash must produce /chat/completions on the
// stripped prefix (no duplicated or guessed "/v1"), and the request must
// carry the model/prompt/stream fields and the Bearer key.
func TestBaseURLTrailingSlashAndRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path = %q, want %q", r.URL.Path, "/v1/chat/completions")
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-abc" {
			t.Errorf("Authorization = %q, want Bearer sk-abc", got)
		}
		var body chatRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		if body.Model != testModel || body.Stream != true || body.MaxTokens != 128 || len(body.Messages) != 1 {
			t.Errorf("unexpected request body: %+v", body)
		}
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"ok"}}]}`,
			"data: [DONE]",
		)
	}))
	defer srv.Close()

	tg := testTarget(srv.URL+"/v1/", 2000, 5000)
	tg.APIKey = "sk-abc"
	o := Do(context.Background(), testClient(), tg)
	assertStatus(t, o, StatusOK)
}

// Parent-context cancellation (engine shutdown / config change) is reported
// as conn_error/"context canceled" for the engine to rewrite as cancelled;
// it must never be attributed to the probed endpoint as a timeout.
func TestParentCancelReportsContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, `data: {"choices":[{"delta":{"content":"slow"}}]}`)
		<-r.Context().Done()
	}))
	defer srv.Close()

	parent, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	o := Do(parent, testClient(), testTarget(srv.URL, 2000, 5000))
	assertStatus(t, o, StatusConnError)
	if o.ErrDetail != "context canceled" {
		t.Fatalf("ErrDetail = %q, want %q", o.ErrDetail, "context canceled")
	}
}

// Regression: content arrives within the first-content deadline, then the
// connection breaks AFTER that deadline has passed but within the total
// limit. The first-content deadline is disarmed once content arrived (§3.3),
// so the mid-stream disconnect is an abort, never a timeout.
func TestAbortAfterTTFTDeadlineWithContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`)
		time.Sleep(600 * time.Millisecond) // > the 300ms ttft deadline, < the 5s total
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijack")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	o := Do(context.Background(), testClient(), testTarget(srv.URL, 300, 5000))
	assertStatus(t, o, StatusAborted)
	if o.TTFTMs == nil {
		t.Fatal("TTFTMs = nil, want the preserved measured value")
	}
}

// Regression: caller cancellation arriving after the first-content deadline
// but with content already received must stay "context canceled" (for the
// engine to rewrite as cancelled), not become a timeout.
func TestParentCancelAfterTTFTDeadlineWithContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sseWrite(w, `data: {"choices":[{"delta":{"content":"slow"}}]}`)
		<-r.Context().Done()
	}))
	defer srv.Close()

	parent, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond) // past the 300ms ttft deadline
		cancel()
	}()
	o := Do(parent, testClient(), testTarget(srv.URL, 300, 5000))
	assertStatus(t, o, StatusConnError)
	if o.ErrDetail != "context canceled" {
		t.Fatalf("ErrDetail = %q, want %q", o.ErrDetail, "context canceled")
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo wörld", 5); got != "héllo" {
		t.Fatalf("truncateRunes = %q, want %q", got, "héllo")
	}
	if got := truncateRunes("abc", 5); got != "abc" {
		t.Fatalf("truncateRunes = %q, want unchanged", got)
	}
	if got := truncateRunes("你好世界", 3); got != "你好世" {
		t.Fatalf("truncateRunes = %q, want %q", got, "你好世")
	}
}

// IncludeUsage: the request must carry stream_options only when the switch is
// on (A1/A2), the usage event is captured (A2), absent usage stays nil (A3),
// usage arriving after [DONE] is not consumed (design boundary), and a
// usage value of 0 is still evidence (0, not nil).
func TestIncludeUsageRequestAndCapture(t *testing.T) {
	var sawBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawBody = map[string]any{}
		_ = json.Unmarshal(body, &sawBody)
		w.Header().Set("Content-Type", "text/event-stream")
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"Hello world"}}]}`,
			"",
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":64,"completion_tokens":32}}`,
			"",
			`data: [DONE]`,
			"")
	}))
	defer srv.Close()

	// A1: switch off => no stream_options key at all. Capture itself is
	// switch-independent: an endpoint that volunteers usage is still
	// recorded (§3.2 allows unsolicited usage; evidence is never fabricated).
	o := Do(context.Background(), testClient(), testTarget(srv.URL, 3000, 5000))
	assertStatus(t, o, StatusOK)
	if _, present := sawBody["stream_options"]; present {
		t.Fatalf("request carried stream_options with switch off: %v", sawBody)
	}
	if o.PromptTokens == nil || *o.PromptTokens != 64 {
		t.Fatalf("volunteered usage must be captured even with switch off, got %v", o.PromptTokens)
	}

	// A2: switch on => stream_options present and usage captured.
	tgt := testTarget(srv.URL, 3000, 5000)
	tgt.IncludeUsage = true
	o = Do(context.Background(), testClient(), tgt)
	assertStatus(t, o, StatusOK)
	opts, ok := sawBody["stream_options"].(map[string]any)
	if !ok || opts["include_usage"] != true {
		t.Fatalf("stream_options = %v, want include_usage=true", sawBody["stream_options"])
	}
	if o.PromptTokens == nil || *o.PromptTokens != 64 {
		t.Fatalf("PromptTokens = %v, want 64", o.PromptTokens)
	}
	if o.CompletionTokens == nil || *o.CompletionTokens != 32 {
		t.Fatalf("CompletionTokens = %v, want 32", o.CompletionTokens)
	}
}

// A3: endpoint never sends usage => nil outcome fields, success unchanged.
func TestIncludeUsageAbsentStaysNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
			"",
			`data: [DONE]`,
			"")
	}))
	defer srv.Close()
	tgt := testTarget(srv.URL, 3000, 5000)
	tgt.IncludeUsage = true
	o := Do(context.Background(), testClient(), tgt)
	assertStatus(t, o, StatusOK)
	if o.PromptTokens != nil || o.CompletionTokens != nil {
		t.Fatalf("tokens should be nil without usage, got %v %v", o.PromptTokens, o.CompletionTokens)
	}
}

// Design boundary: usage after [DONE] is not consumed — the read loop stops
// at [DONE] (classification semantics outrank evidence gathering), so the
// trailing usage event never arrives.
func TestUsageAfterDoneNotConsumed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
			"",
			`data: [DONE]`,
			"",
			`data: {"usage":{"prompt_tokens":9,"completion_tokens":9}}`,
			"")
	}))
	defer srv.Close()
	tgt := testTarget(srv.URL, 3000, 5000)
	tgt.IncludeUsage = true
	o := Do(context.Background(), testClient(), tgt)
	assertStatus(t, o, StatusOK)
	if o.PromptTokens != nil {
		t.Fatalf("usage after [DONE] must not be captured, got %v", *o.PromptTokens)
	}
}

// usage counts of 0 are legitimate observations and must be kept (0, nil
// distinction matters downstream for throughput denominators).
func TestUsageZeroCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		sseWrite(w,
			`data: {"choices":[{"delta":{"content":"Hi"}}]}`,
			"",
			`data: {"choices":[],"usage":{"prompt_tokens":0,"completion_tokens":0}}`,
			"",
			`data: [DONE]`,
			"")
	}))
	defer srv.Close()
	tgt := testTarget(srv.URL, 3000, 5000)
	tgt.IncludeUsage = true
	o := Do(context.Background(), testClient(), tgt)
	assertStatus(t, o, StatusOK)
	if o.PromptTokens == nil || *o.PromptTokens != 0 {
		t.Fatalf("PromptTokens = %v, want pointer to 0", o.PromptTokens)
	}
}
