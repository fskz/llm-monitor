package probe

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

// streamEvent is the subset of an OpenAI streaming chat chunk that probe
// judgment depends on. Fields we do not act on (usage, reasoning_content,
// tool_calls, id, created, ...) are intentionally absent: they must not
// affect first-content detection or classification.
type streamEvent struct {
	Error   json.RawMessage `json:"error"`
	Choices []streamChoice  `json:"choices"`
}

type streamChoice struct {
	Delta struct {
		// Content is the streamed text. Role-only or empty deltas leave it
		// as ""; reasoning/tool-call deltas never set it, so they cannot
		// be mistaken for first content.
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason string `json:"finish_reason"`
}

// processLine handles one SSE line, scanning strictly by line so behavior is
// independent of HTTP chunk boundaries. It returns true when the read loop
// must stop immediately ([DONE] marker, in-stream error, or unusable data),
// having set st.doneAt or st.stopStatus/stopAt accordingly.
func (r *run) processLine(line []byte) bool {
	s := strings.TrimRight(string(line), "\r\n")
	switch {
	case s == "":
		return false // blank line between events
	case strings.HasPrefix(s, ":"):
		return false // comment / heartbeat
	}
	if data, ok := cutField(s, "data:"); ok {
		return r.handleData(data)
	}
	// Known SSE fields whose payload carries no judgment signal.
	if _, ok := cutField(s, "event:"); ok {
		return false
	}
	if _, ok := cutField(s, "id:"); ok {
		return false
	}
	if _, ok := cutField(s, "retry:"); ok {
		return false
	}
	return r.handleUnknownLine(s)
}

// cutField strips "name" and at most one optional following space (both
// "data:x" and "data: x" are accepted, per SSE).
func cutField(line, name string) (string, bool) {
	if !strings.HasPrefix(line, name) {
		return "", false
	}
	return strings.TrimPrefix(line[len(name):], " "), true
}

// handleData judges one "data:" payload.
func (r *run) handleData(data string) bool {
	if strings.TrimSpace(data) == "[DONE]" {
		r.st.doneAt = time.Now()
		return true
	}
	if data == "" {
		return false // keep-alive empty data line
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		r.st.stopStatus = StatusProtocolError
		r.st.stopAt = time.Now()
		r.st.errDetail = "unparseable data event: " + dataSnippet(data)
		return true
	}
	if msg := errorText(ev.Error); msg != "" {
		r.st.stopStatus = StatusStreamError
		r.st.stopAt = time.Now()
		r.st.errDetail = "error event in stream: " + msg
		return true
	}
	if len(ev.Choices) == 0 {
		// Usage-only or otherwise choice-less events are skipped, never
		// errors (we do not request include_usage, but endpoints may send it).
		return false
	}
	c := ev.Choices[0]
	if c.Delta.Content != "" {
		if r.st.firstContentAt.IsZero() {
			r.st.firstContentAt = time.Now()
			// The first-content deadline no longer applies once content
			// arrived; the total timeout still guards the rest of the stream.
			r.firstContentSeen.Store(true)
			if r.ttftTimer != nil {
				r.ttftTimer.Stop()
			}
		}
		r.st.hasText = true
		r.appendPreview(c.Delta.Content)
	}
	if c.FinishReason != "" {
		r.st.finishReason = true
	}
	return false
}

// handleUnknownLine judges a line that carries no SSE field prefix at all
// (for example a bare JSON body from a non-streaming endpoint): an embedded
// error object still reports stream_error, anything else is protocol_error.
func (r *run) handleUnknownLine(s string) bool {
	if json.Valid([]byte(s)) {
		var ev streamEvent
		if err := json.Unmarshal([]byte(s), &ev); err == nil && errorText(ev.Error) != "" {
			r.st.stopStatus = StatusStreamError
			r.st.stopAt = time.Now()
			r.st.errDetail = "error event in stream: " + errorText(ev.Error)
			return true
		}
		// Valid JSON but no error object and no SSE framing: still not a
		// stream this probe can judge.
		r.st.stopStatus = StatusProtocolError
		r.st.stopAt = time.Now()
		r.st.errDetail = "unexpected non-SSE line: " + dataSnippet(s)
		return true
	}
	r.st.stopStatus = StatusProtocolError
	r.st.stopAt = time.Now()
	r.st.errDetail = "unrecognized line: " + dataSnippet(s)
	return true
}

// appendPreview accumulates streamed content for the manual-test echo,
// capped at the preview length so long streams cannot grow memory.
func (r *run) appendPreview(content string) {
	if r.st.previewRunes >= maxPreviewRunes {
		return
	}
	runes := []rune(content)
	if take := maxPreviewRunes - r.st.previewRunes; take < len(runes) {
		runes = runes[:take]
	}
	r.st.preview.WriteString(string(runes))
	r.st.previewRunes += len(runes)
}

// errorText returns a printable message for a top-level "error" value, or ""
// when the value is absent/empty and therefore not an in-stream error.
func errorText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", `""`, "{}":
		return ""
	}
	return s
}

func dataSnippet(s string) string {
	return truncateRunes(s, 120)
}

// replaceKey masks the API key anywhere it appears so it can never leak into
// ErrDetail or OutputPreview (logs, panels, error bodies included).
func replaceKey(s, key string) string {
	if key == "" || !strings.Contains(s, key) {
		return s
	}
	return strings.ReplaceAll(s, key, "***")
}

// truncateRunes cuts s to at most n runes, keeping multi-byte characters intact.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
