# LLM Monitor Go Conventions

> Code-spec extracted from the MVP implementation (task 10-08-mvp-llm-monitor).
> Authority for judgment rules is `docs/REQUIREMENTS.md` §3; this file records
> the executable contracts the code now relies on.

---

## Package layout & dependency direction

```
cmd/llm-monitor  →  internal/server  →  internal/store
                           ↓                (server also → engine via interface)
                    internal/engine  →  internal/probe
                    web (embed only, read by server)
```

- `internal/probe` imports nothing from the project (pure function, no globals).
- `internal/store` does NOT import `internal/probe` (private status string
  copies live in store; engine passes probe's strings when building Result).
- `internal/server` does NOT import `internal/engine` — it defines narrow
  interfaces (`EngineAPI`, `ProviderMutator`); `cmd/llm-monitor` adapts with
  `engineAdapter`. Compile-time proof: `internal/server/zz_engine_assert_test.go`.
- Zero third-party dependencies (`go.mod` has no require). Do not add one for
  something the standard library does.

## Status string contract (cross-layer, CRITICAL)

The 10 final statuses flow probe → engine → store → server → web:

`ok | timeout_ttft | timeout_total | http_error | conn_error | stream_error |
protocol_error | empty | aborted | cancelled`

- Constants: `internal/probe` exports `StatusOK` … `StatusCancelled`.
  `internal/store` keeps private mirrors (`statusOK`, …). Web displays Chinese
  labels via `STATUS_TEXT` in `web/app.js`. **Adding/renaming a status requires
  touching all four places** (probe const, store mirror, server view, web map).
- `cancelled` is never produced by `probe.Do`; engine overwrites
  `conn_error/"context canceled"` to `cancelled` when the parent ctx was
  cancelled (§5.5). Store's stats exclude `cancelled` from the denominator.
- `success == (status == "ok")` — enforced at Result construction in engine.

## Probe judgment rules (where they live)

- Dual deadline: `time.AfterFunc` per deadline cancels one child ctx;
  classification is **after the read loop, timestamp-based only** (callback
  ordering must not affect the outcome).
- First-content deadline is disarmed once valid text arrives
  (`ttftMissedBy` in `internal/probe/probe.go` — includes the "late but
  arrived" case). Regression tests: `probe_test.go` TestAbortAfterTTFTExpired /
  TestParentCancelAfterTTFTExpired.
- Mid-stream total expiry with text already seen → `timeout_total` (NOT
  `aborted`); aborted is reserved for peer-side breaks.
- Normal end = `[DONE]` seen, OR non-empty `finish_reason` then clean EOF;
  otherwise EOF → `aborted` even with partial text.
- Error/redaction: API key replaced with `***`, ErrDetail ≤512 runes,
  OutputPreview ≤200 runes, HTTP error body ≤4KB.
- **Usage capture is observational, never judgmental**: `streamEvent.Usage`
  is recorded into `Outcome.PromptTokens/CompletionTokens` before any
  classification branch and the classification state machine never reads it.
  The read loop stops at `[DONE]` — usage arriving after `[DONE]` is not
  consumed (classification semantics outrank evidence gathering). Capture is
  switch-independent: an endpoint volunteering usage is recorded even with
  `include_usage=false` (the switch only controls what we ASK for).
- Throughput derivation lives in ONE place (`internal/store`):
  `decodeTPS`/`prefillTPS` (+ public `ResultTPS`) define the formula and
  guards — decode excludes `completion<2` and zero decode span, prefill
  requires `prompt>0 && ttft>0`, each metric has an independent denominator.
  The manual-probe response and the stored-sample averages both go through
  it; never re-implement the formula elsewhere.

## JSONL store discipline

- `config.json` (0600, temp+rename) holds `next_id` (never reused),
  `max_results_per_provider` (default 20000), providers.
- `results/<id>.jsonl`: append-only under a per-provider mutex; trim rewrites
  the whole file (temp+rename) **inside the same lock** so no new record is
  lost. Corrupt lines are skipped + counted on load, never rewritten away.
- `Seq` is assigned inside `AppendResult`, which returns the stored record —
  callers wanting the seq must use the return value (pass-by-value otherwise
  yields `seq:0`; this was a real bug).
- Pagination cursor = composite `(started_at, seq)`, strictly "before", so
  same-timestamp records are separable. Server encodes it base64.
- Stats: denominator `source=scheduled && status!=cancelled`, window filters
  on `started_at`; zero samples → all pct fields JSON `null` (never 0%).

## Scheduling rules

- Per provider: one goroutine, immediate probe then `time.Ticker(interval)`;
  in-flight slot is a per-provider `atomic.Bool` shared by scheduled+manual
  probes; a tick that lands mid-probe is silently skipped (no queue, no
  sample, no backfill).
- **Gotcha**: in the scheduler `select { ctx.Done(); ticker.C }` both cases
  can be ready at shutdown; Go picks randomly. Guard with `r.ctx.Err() != nil`
  before running the tick's probe, else a phantom `cancelled` record is
  persisted for a request that never launched.
- `Update`/`Remove`/disable cancel the in-flight probe (persisted
  `cancelled`) and wait ≤10s for the old scheduler to exit before rescheduling
  — guarantees no old-config request survives an Update return.
- 20+ providers × 1min: independent goroutines; capacity test
  `TestTwentyProvidersScheduleIndependently`.

## Server / security contracts

- Listens on `127.0.0.1` only. Write endpoints (POST/PUT/DELETE/probe) reject
  requests whose `Origin` header is present and not `127.0.0.1|localhost|::1`
  + bound port (403). No CORS headers anywhere.
- API key handling: GET responses carry `api_key_set` + `api_key_mask`
  (>8 chars: first4+"****"+last4). PUT treats absent JSON field as keep,
  `""`/null as clear, non-empty as replace (`*string`).
- `base_url` normalized with `strings.TrimRight "/"` before compare/save;
  change of base_url or model ⇒ `revision++` (other fields do not).
- Validation (400, Chinese messages): name/model non-empty; base_url
  `http(s)://`; `max_tokens>0`; `interval_sec==0||>=60`;
  `0<ttft_slow_ms<ttft_timeout_ms<=timeout_sec*1000`; `timeout_sec>0`.
- Monitor status priority (§7.2): disabled > manual_only > unknown > stale
  (`>2*interval+timeout`) > ok (+`slow_ttft` when `ttft_ms>ttft_slow_ms`) >
  fail. `probing` is a hint bit, never overrides a status.
- `last_probe` must match the provider's current `revision`, else it is
  treated as absent (status becomes `unknown`, §12.1-8).

## Frontend (web/) conventions

- Plain HTML/JS/CSS, no CDN/external URLs, no framework; embedded via
  `//go:embed` in `web/embed.go` (`web.FS()`).
- Poll `/api/providers` every 5s (overview) — read-only, never triggers probes.
- All server-derived strings are HTML-escaped before injection (`esc()`);
  empty-sample states render "暂无样本", never 0%.
- API JSON shapes are the contract with `internal/server/views.go` — change
  both sides together.

## Wrong vs Correct (selections from real bugs)

### Wrong
```go
select {
case <-r.ctx.Done():
    return
case <-ticker.C:
    e.scheduledProbe(r) // may run with ctx already cancelled → phantom record
}
```
### Correct
```go
case <-ticker.C:
    if r.ctx.Err() != nil { return } // both-ready select picks randomly
    e.scheduledProbe(r)
```

### Wrong
```go
st.AppendResult(r) // r passed by value; r.Seq stays 0 in the response
```
### Correct
```go
stored, err := st.AppendResult(r) // use returned record for seq
```

### Wrong
```go
log.Fatalf("http: %v", err) // inside server goroutine: os.Exit skips Shutdown
```
### Correct
```go
log.Printf("http: %v", err); stop() // signal main flow → graceful shutdown
```
