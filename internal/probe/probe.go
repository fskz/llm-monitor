// Package probe performs single-request streaming text probes against
// OpenAI-compatible chat/completions endpoints and classifies each outcome
// according to docs/REQUIREMENTS.md §3 (availability-first judgment rules).
//
// A probe is a pure function of (ctx, client, Target): it keeps no global
// state, never retries, and never issues extra requests, so the engine can
// run many probes concurrently.
package probe

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Final result categories (REQUIREMENTS.md §3.3).
const (
	StatusOK            = "ok"
	StatusTimeoutTTFT   = "timeout_ttft"
	StatusTimeoutTotal  = "timeout_total"
	StatusHTTPError     = "http_error"
	StatusConnError     = "conn_error"
	StatusStreamError   = "stream_error"
	StatusProtocolError = "protocol_error"
	StatusEmpty         = "empty"
	StatusAborted       = "aborted"

	// StatusCancelled marks probes aborted on purpose by the caller
	// (process shutdown, provider edit/disable/remove). Do never returns it:
	// when the parent context is canceled, Do reports conn_error with detail
	// "context canceled" and the engine rewrites that outcome to cancelled.
	// The constant lives here so every status name is defined in one place.
	StatusCancelled = "cancelled"
)

const (
	maxErrDetailRunes  = 512  // ErrDetail is rune-truncated to this length
	maxPreviewRunes    = 200  // OutputPreview is rune-truncated to this length
	maxHTTPBodySnippet = 4096 // bytes of an HTTP error body kept for ErrDetail
)

// Target describes one probe request.
type Target struct {
	BaseURL, APIKey, Model, Prompt string
	MaxTokens                      int
	TTFTTimeoutMs, TotalTimeoutMs  int
}

// Outcome is the classified result of one probe.
//
// TTFTMs is nil when no valid first content arrived (never 0 or -1 for
// "unknown"); HTTPStatus is nil when no HTTP response was received.
// Durations use the monotonic clock and cover the full end-to-end request
// (connect + network + server + streaming).
type Outcome struct {
	Status        string
	TTFTMs        *int64
	TotalMs       int64
	HTTPStatus    *int
	ErrDetail     string
	OutputPreview string
}

// Do performs one streaming probe.
//
// Both deadlines (first content and total) are enforced internally by
// canceling the request context. Classification happens after the read loop
// exits and is based on timestamps and flags only, so timer callback
// ordering cannot affect the result. client must not have Timeout set.
func Do(ctx context.Context, client *http.Client, t Target) Outcome {
	r := &run{target: t, parent: ctx}
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.start = time.Now()
	if t.TTFTTimeoutMs > 0 {
		r.ttftDeadline = r.start.Add(time.Duration(t.TTFTTimeoutMs) * time.Millisecond)
		// The first-content deadline only applies BEFORE valid text arrives
		// (§3.3). The flag guard closes the timer-vs-Stop race: if content
		// was already processed when the timer fires, it must not kill the
		// stream — the total deadline alone guards the rest.
		r.ttftTimer = time.AfterFunc(time.Until(r.ttftDeadline), func() {
			if !r.firstContentSeen.Load() {
				r.cancel()
			}
		})
	}
	if t.TotalTimeoutMs > 0 {
		r.totalDeadline = r.start.Add(time.Duration(t.TotalTimeoutMs) * time.Millisecond)
		r.totalTimer = time.AfterFunc(time.Until(r.totalDeadline), r.cancel)
	}
	defer r.cancel()
	return r.execute(client)
}

// run holds the state of a single probe execution. All fields are touched
// only from the goroutine running Do, except cancel/timers which are
// goroutine-safe.
type run struct {
	target           Target
	parent           context.Context
	ctx              context.Context
	cancel           context.CancelFunc
	start            time.Time
	ttftDeadline     time.Time
	totalDeadline    time.Time
	ttftTimer        *time.Timer
	totalTimer       *time.Timer
	firstContentSeen atomic.Bool // set when valid first content is processed
	st               scanState
}

// scanState accumulates SSE evidence while reading the response body.
type scanState struct {
	httpStatus     *int
	firstContentAt time.Time // zero until the first non-empty delta.content
	hasText        bool
	finishReason   bool      // a non-empty finish_reason was received
	doneAt         time.Time // when [DONE] was received (terminal evidence)
	stopAt         time.Time // when an error/protocol violation was observed
	stopStatus     string    // stream_error or protocol_error, set with stopAt
	errDetail      string
	preview        strings.Builder
	previewRunes   int
}

func (r *run) execute(client *http.Client) Outcome {
	req, err := r.buildRequest()
	if err != nil {
		return r.finish(Outcome{Status: StatusConnError, ErrDetail: "invalid request: " + err.Error()})
	}
	resp, err := client.Do(req)
	if err != nil {
		return r.finish(r.classifyTransportError(err))
	}
	defer resp.Body.Close()

	code := resp.StatusCode
	r.st.httpStatus = &code
	if code < 200 || code > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPBodySnippet))
		if r.deadlineCancelled() {
			// The deadline fired before the response arrived: a timeout,
			// never an HTTP error (§3.3).
			return r.finish(r.timeoutOutcome())
		}
		detail := fmt.Sprintf("HTTP %d: %s", code, strings.TrimSpace(string(snippet)))
		return r.finish(Outcome{Status: StatusHTTPError, ErrDetail: detail})
	}
	return r.finish(r.classify(r.readLoop(resp.Body)))
}

type chatRequest struct {
	Model     string        `json:"model"`
	Messages  []chatMessage `json:"messages"`
	MaxTokens int           `json:"max_tokens"`
	Stream    bool          `json:"stream"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (r *run) buildRequest() (*http.Request, error) {
	// BaseURL is an API prefix such as https://example.com/v1: strip a
	// trailing slash, append the fixed path, never guess or duplicate "/v1".
	endpoint := strings.TrimRight(r.target.BaseURL, "/") + "/chat/completions"
	body, err := json.Marshal(chatRequest{
		Model:     r.target.Model,
		Messages:  []chatMessage{{Role: "user", Content: r.target.Prompt}},
		MaxTokens: r.target.MaxTokens,
		Stream:    true,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(r.ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if r.target.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.target.APIKey)
	}
	return req, nil
}

// classifyTransportError classifies failures that happened before an HTTP
// response was received (DNS, TCP, TLS, or cancellation).
func (r *run) classifyTransportError(err error) Outcome {
	if r.deadlinePassed() {
		// The deadline, not the network, killed the request: a timeout is
		// never counted as a connection error (§3.3).
		return r.timeoutOutcome()
	}
	if r.parent.Err() != nil {
		// Canceled by the caller, not by a deadline and not by the network.
		return Outcome{Status: StatusConnError, ErrDetail: "context canceled"}
	}
	return Outcome{Status: StatusConnError, ErrDetail: err.Error()}
}

// exitKind is why the SSE read loop terminated.
type exitKind int

const (
	exitDone        exitKind = iota // "data: [DONE]" received
	exitEOF                         // clean EOF from the server
	exitStreamErr                   // error event inside the stream
	exitProtocolErr                 // unparseable / non-SSE data
	exitCanceled                    // context canceled between reads
	exitReadErr                     // read failed (reset, deadline, cancel)
)

func (r *run) readLoop(body io.Reader) exitKind {
	// Scan by lines so classification is independent of chunk boundaries.
	reader := bufio.NewReaderSize(body, 32*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 && r.processLine(line) {
			switch {
			case r.st.doneAt.After(time.Time{}):
				return exitDone
			case r.st.stopStatus == StatusStreamError:
				return exitStreamErr
			default:
				return exitProtocolErr
			}
		}
		if err != nil {
			if err == io.EOF {
				return exitEOF
			}
			r.st.errDetail = "read error: " + err.Error()
			return exitReadErr
		}
		select {
		case <-r.ctx.Done():
			return exitCanceled
		default:
		}
	}
}

// classify maps the read-loop exit to the final §3.3 status. Priority
// follows design.md §2: normal end first, then deadline expiry (compared by
// timestamp, so late-arriving evidence cannot turn a timeout into success),
// then in-stream errors, then EOF without end-of-stream evidence.
func (r *run) classify(exit exitKind) Outcome {
	switch exit {
	case exitDone:
		return r.normalEnd(r.st.doneAt)
	case exitEOF:
		if r.st.finishReason {
			// Endpoints that skip [DONE]: a non-empty finish_reason followed
			// by a clean EOF counts as a normal end, completed at EOF time.
			return r.normalEnd(time.Now())
		}
		return r.noEvidence()
	case exitStreamErr, exitProtocolErr:
		if r.ttftMissedBy(r.st.stopAt) || r.afterTotalDeadline(r.st.stopAt) {
			// The deadline fired before the offending event was observed.
			return r.timeoutOutcome()
		}
		if exit == exitStreamErr {
			return Outcome{Status: StatusStreamError, ErrDetail: r.st.errDetail}
		}
		return Outcome{Status: StatusProtocolError, ErrDetail: r.st.errDetail}
	default: // exitCanceled, exitReadErr
		return r.noEvidence()
	}
}

// normalEnd classifies a stream that ended normally (via [DONE], or via
// finish_reason + clean EOF) at endAt, unless a deadline beat the evidence.
func (r *run) normalEnd(endAt time.Time) Outcome {
	if r.ttftMissedBy(endAt) {
		// The first-content deadline expired before valid text arrived
		// (no text at all by then, or text that arrived too late).
		return r.timeoutOutcome()
	}
	if r.afterTotalDeadline(endAt) {
		// The stream only completed after the total limit: §3.3 counts the
		// probe as timed out, not as successful.
		return r.timeoutOutcome()
	}
	if r.st.hasText {
		return Outcome{Status: StatusOK}
	}
	return Outcome{Status: StatusEmpty, ErrDetail: "stream ended normally with no content"}
}

// noEvidence classifies loop exits without end-of-stream evidence: deadline
// expiry, caller cancellation, or an abnormal disconnect.
func (r *run) noEvidence() Outcome {
	if r.deadlinePassed() {
		return r.timeoutOutcome()
	}
	if r.ctx.Err() != nil {
		// Parent context canceled (engine shutdown / config change); the
		// engine rewrites this to "cancelled" and keeps it out of stats.
		return Outcome{Status: StatusConnError, ErrDetail: "context canceled"}
	}
	return Outcome{Status: StatusAborted, ErrDetail: r.st.errDetail}
}

// ttftMissedBy reports whether the first-content deadline had expired by
// time at: either no valid text ever arrived by then, or the text timestamp
// itself is later than the deadline (text that arrived too late still misses
// the deadline). Covers the simultaneous-expiry rule: no content by the
// deadline means timeout_ttft (§3.3).
func (r *run) ttftMissedBy(at time.Time) bool {
	if r.ttftDeadline.IsZero() {
		return false
	}
	if !r.st.firstContentAt.IsZero() {
		return r.st.firstContentAt.After(r.ttftDeadline)
	}
	return at.After(r.ttftDeadline)
}

// afterTotalDeadline reports whether t is later than the total deadline.
func (r *run) afterTotalDeadline(t time.Time) bool {
	return !r.totalDeadline.IsZero() && t.After(r.totalDeadline)
}

// timeoutOutcome picks the timeout subtype: a missed (or late) first
// content timestamp means timeout_ttft, anything else is a total timeout
// (§3.3). A measured TTFT is preserved by finish for either subtype.
func (r *run) timeoutOutcome() Outcome {
	if r.ttftMissedBy(time.Now()) {
		return Outcome{Status: StatusTimeoutTTFT, ErrDetail: "timed out waiting for first content"}
	}
	return Outcome{Status: StatusTimeoutTotal, ErrDetail: "timed out before the stream completed"}
}

// deadlineCancelled reports whether the context is canceled and a deadline
// has already been reached, i.e. the probe was killed by its own deadline.
func (r *run) deadlineCancelled() bool {
	return r.ctx.Err() != nil && r.deadlinePassed()
}

// deadlinePassed reports whether a deadline that still applies has been
// reached. The first-content deadline only applies while valid text is
// outstanding (§3.3): once content arrived before it, that deadline is
// disarmed and only the total deadline can classify anymore — otherwise a
// stream simply lasting longer than the first-content limit would turn every
// later abort or cancellation into a bogus timeout. Late content (arrived
// after the first-content deadline) still counts as missed.
func (r *run) deadlinePassed() bool {
	now := time.Now()
	return r.ttftMissedBy(now) || r.afterTotalDeadline(now)
}

// finalize fills the derived Outcome fields and applies sanitization.
func (r *run) finish(o Outcome) Outcome {
	if r.ttftTimer != nil {
		r.ttftTimer.Stop()
	}
	if r.totalTimer != nil {
		r.totalTimer.Stop()
	}
	o.TotalMs = time.Since(r.start).Milliseconds()
	o.HTTPStatus = r.st.httpStatus
	if r.st.hasText {
		// Monotonic TTFT; kept even when the probe ultimately failed.
		ttft := r.st.firstContentAt.Sub(r.start).Milliseconds()
		o.TTFTMs = &ttft
	}
	o.ErrDetail = truncateRunes(replaceKey(o.ErrDetail, r.target.APIKey), maxErrDetailRunes)
	if o.Status == StatusOK {
		o.ErrDetail = ""
	}
	o.OutputPreview = truncateRunes(replaceKey(r.st.preview.String(), r.target.APIKey), maxPreviewRunes)
	return o
}
