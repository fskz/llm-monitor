// Package cachecheck detects whether an OpenAI-compatible endpoint caches
// prompt prefixes (task 10-22-cache-check, prd R1/R2): two identical
// streaming requests are sent in sequence (cold, then warm) and the verdict
// is derived from the endpoint-reported usage.prompt_tokens_details.
// cached_tokens, with the two TTFT observations as auxiliary evidence only.
//
// A cache check keeps the same discipline as probe.Do: a pure function of
// (ctx, client, Target) with no global state, no retries, and no requests
// beyond the two hops it exists to send. Verdicts are machine value strings;
// Chinese labels are owned by internal/view, never this package.
package cachecheck

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"llm-monitor/internal/probe"
)

// Verdict values (machine strings; the human labels live in internal/view).
const (
	// VerdictSupported: cached_tokens reported and > 0 on at least one hop.
	VerdictSupported = "supported"
	// VerdictUnsupported: cached_tokens reported as 0 on both hops.
	VerdictUnsupported = "unsupported"
	// VerdictUnknown: cached_tokens missing (stripped) on at least one hop
	// and no hop reported a value > 0.
	VerdictUnknown = "unknown"
	// VerdictFailed: a detection request itself failed (prd R1 keeps this
	// strictly separate from "cannot determine").
	VerdictFailed = "failed"
)

// AllVerdicts is the single enumeration of the verdict constants: the
// cross-layer label sync tests (view ↔ web) walk it, so a verdict added here
// without a view label or a JS copy fails those tests instead of drifting.
func AllVerdicts() []string {
	return []string{VerdictSupported, VerdictUnsupported, VerdictUnknown, VerdictFailed}
}

// Fixed detection parameters (design §5): the deterministic prompt clears
// the common cache-activation thresholds (≥2048 tokens), max_tokens keeps
// the completion cheap, and the first-content deadline equals the total
// deadline so a hop can never be cut short early — TTFT is recorded as
// evidence, never used to abort.
const (
	detectionMaxTokens  = 8
	detectionTimeoutSec = 60
)

// promptSentence is the deterministic filler repeated into the detection
// prompt. 96 repetitions are counted as ~2620 prompt tokens by the endpoints
// observed so far (×64 was counted as 1748 — below the 2048-token activation
// threshold some providers apply, which would false-negative the verdict), so
// ×96 clears the threshold with margin.
const promptSentence = "The prefix-cache probe sends this deterministic paragraph so that both requests share a byte-identical prompt of well over two thousand tokens. "

// detectionPrompt is the fixed prompt sent on both hops. Built once; the
// requests must stay byte-identical or a cache could never hit.
var detectionPrompt = strings.Repeat(promptSentence, 96)

// Target identifies the endpoint under test. The engine constructs it from
// a provider snapshot; the detection parameters themselves (prompt,
// max_tokens, timeouts, include_usage) are fixed in this package and
// deliberately not configurable (prd non-goals: no adjustable prompt).
type Target struct {
	BaseURL string
	APIKey  string
	Model   string
}

// Hop is the evidence recorded for one of the two identical requests. Field
// semantics mirror probe.Outcome; CachedTokens is nil when the endpoint
// omits prompt_tokens_details — the distinction the unknown verdict hinges on.
type Hop struct {
	Status           string
	HTTPStatus       *int
	TTFTMs           *int64
	TotalMs          int64
	PromptTokens     *int
	CompletionTokens *int
	CachedTokens     *int64
	ErrDetail        string
}

// Result is the outcome of one two-hop detection run.
type Result struct {
	Verdict string
	// Hops always holds both hops in order (cold, warm): there is no early
	// short-circuit, even when the first hop fails (prd R2).
	Hops [2]Hop
	// FailedHop is the 1-based index of the first failed hop, 0 when none.
	FailedHop int
	// Evidence is a short machine-readable summary of why the verdict holds;
	// human rendering is owned by the view layer.
	Evidence string
}

// Run sends the two identical detection requests sequentially and derives
// the verdict. It runs synchronously in the caller's goroutine; each hop is
// bounded by the fixed 60s budget enforced through probe.Do's deadlines.
// client must not have Timeout set (same contract as probe.Do).
func Run(ctx context.Context, client *http.Client, t Target) Result {
	return runHops(ctx, client, t, detectionTimeoutSec*1000)
}

// runHops is Run with an injectable per-hop budget in milliseconds: the
// production path always passes the fixed constant, while tests shrink it
// to exercise the genuine timeout classification quickly.
func runHops(ctx context.Context, client *http.Client, t Target, timeoutMs int) Result {
	pt := probe.Target{
		BaseURL: t.BaseURL,
		APIKey:  t.APIKey,
		Model:   t.Model,
		Prompt:  detectionPrompt,
		// Forced on regardless of any provider configuration (prd R1): the
		// endpoint is explicitly asked to report usage.
		IncludeUsage: true,
		MaxTokens:    detectionMaxTokens,
		// TTFT deadline == total deadline: no early first-content abort;
		// TTFT lands in the evidence either way (design §5).
		TTFTTimeoutMs:  timeoutMs,
		TotalTimeoutMs: timeoutMs,
	}
	var res Result
	for i := range res.Hops {
		out := probe.Do(ctx, client, pt)
		res.Hops[i] = Hop{
			Status:           out.Status,
			HTTPStatus:       out.HTTPStatus,
			TTFTMs:           out.TTFTMs,
			TotalMs:          out.TotalMs,
			PromptTokens:     out.PromptTokens,
			CompletionTokens: out.CompletionTokens,
			CachedTokens:     out.CachedTokens,
			ErrDetail:        out.ErrDetail,
		}
	}
	return judge(res)
}

// usableHop reports whether a hop's observation can feed the verdict. ok is
// the normal case; empty (HTTP 200, clean [DONE], usage delivered, zero
// content) is equally usable because this check consumes prefill-side
// evidence only — content is never part of the judgment. Real endpoints do
// return such streams: observed 2026-10-23, a model burned the entire
// completion budget on invisible output (finish_reason=length, no content
// delta, at max_tokens 8 and 64 alike), and discarding those hops threw away
// valid cached_tokens evidence and reported a bogus "failed".
func usableHop(h *Hop) bool {
	return h.Status == probe.StatusOK || h.Status == probe.StatusEmpty
}

// judge derives the verdict from the two hops (prd R1). Pure function,
// table-tested:
//  1. any hop unusable (a status other than ok/empty — HTTP/conn/stream/
//     protocol errors, timeouts, aborted) → failed (first failing hop
//     number + reason; prd R1's failure set never included empty)
//  2. any hop reports the field
//     with a value > 0          → supported (a positive report is conclusive
//     even when the other hop's field was stripped)
//  3. both hops report 0        → unsupported
//  4. otherwise (a field went
//     missing and none is > 0)  → unknown, TTFT numbers attached as
//     auxiliary evidence whose wording marks it reference-only.
func judge(r Result) Result {
	for i := range r.Hops {
		h := &r.Hops[i]
		if !usableHop(h) {
			r.Verdict = VerdictFailed
			r.FailedHop = i + 1
			detail := h.ErrDetail
			if detail == "" {
				detail = "no detail"
			}
			r.Evidence = fmt.Sprintf("hop %d failed with status %s: %s", i+1, h.Status, detail)
			return r
		}
	}
	cold, warm := r.Hops[0].CachedTokens, r.Hops[1].CachedTokens
	if (cold != nil && *cold > 0) || (warm != nil && *warm > 0) {
		r.Verdict = VerdictSupported
		r.Evidence = fmt.Sprintf("cached_tokens hop1=%s, hop2=%s", tokenText(cold), tokenText(warm))
		return r
	}
	if cold != nil && warm != nil {
		r.Verdict = VerdictUnsupported
		r.Evidence = fmt.Sprintf("cached_tokens hop1=%d, hop2=%d", *cold, *warm)
		return r
	}
	r.Verdict = VerdictUnknown
	r.Evidence = fmt.Sprintf(
		"cached_tokens missing (hop1=%s, hop2=%s); TTFT hop1=%s, hop2=%s, cold/hot ratio=%s (auxiliary only, not part of the verdict)",
		tokenText(cold), tokenText(warm),
		ttftText(r.Hops[0].TTFTMs), ttftText(r.Hops[1].TTFTMs),
		ttftRatioText(r.Hops[0].TTFTMs, r.Hops[1].TTFTMs),
	)
	return r
}

// tokenText renders a captured token count; "absent" when the field was
// stripped — never a fabricated 0.
func tokenText(v *int64) string {
	if v == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *v)
}

// ttftText renders one hop's TTFT; "n/a" when it was not measured.
func ttftText(v *int64) string {
	if v == nil {
		return "n/a"
	}
	return fmt.Sprintf("%dms", *v)
}

// ttftRatioText renders the cold/hot TTFT ratio; "n/a" when either side is
// missing or the warm hop measured 0ms (division impossible).
func ttftRatioText(cold, warm *int64) string {
	if cold == nil || warm == nil || *warm <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.2fx", float64(*cold)/float64(*warm))
}
