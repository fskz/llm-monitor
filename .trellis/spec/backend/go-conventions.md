# LLM Monitor Go Conventions

> Code-spec extracted from the MVP implementation (task 10-08-mvp-llm-monitor),
> updated by task 10-09-tui-default (TUI default mode + on-demand web panel).
> Authority for judgment rules is `docs/REQUIREMENTS.md` §3; this file records
> the executable contracts the code now relies on.

---

## Package layout & dependency direction

```
cmd/llm-monitor  →  { internal/tui, internal/server }  →  internal/view  →  internal/store
        ↓                                                            ↑
        └→  internal/engine  →  internal/probe      (narrow interfaces in view;
                                                       cmd adapts with engineAdapter)
        web (embed only, read by cmd; handed to server via handler factory)
```

- `internal/probe` imports nothing from the project (pure function, no globals).
- `internal/store` does NOT import `internal/probe` (private status string
  copies live in store; engine passes probe's strings when building Result).
- `internal/view` is the shared assembly layer (ProviderView/StatsView shape
  contract, StatusText/MonitorText label tables, ProviderForm validation,
  RenderReport pipeline, and the narrow engine interfaces `EngineAPI` /
  `ProviderMutator` / `EngineStatus`). It imports ONLY `internal/store`.
- `internal/server` does NOT import `internal/engine` or `internal/tui`; it
  delegates assembly to `internal/view`. Compile-time proof:
  `internal/server/zz_engine_assert_test.go`.
- `internal/tui` (default front end, zero listening ports) does NOT import
  `internal/server` or `internal/engine`. The on-demand web handler arrives
  via `tui.Deps.ServerHook func(boundPort int) http.Handler`, which cmd fills
  with `server.New(...).Handler()` — the dependency arrow stays cmd → server.
- Third-party dependencies: `github.com/rivo/tview` (+tcell) is the approved
  exception for the TUI layer. Do not add one for something the standard
  library does.

## Process & UI topology (task 10-09)

- One process, one front end at a time: bare `llm-monitor` runs the TUI with
  ZERO listening ports; the web panel is pulled up from inside the TUI (`w`)
  and closed explicitly (no idle timeout). No `web` subcommand, no headless
  mode (PRD decision: unattended operation unsupported).
- Engine + store live in the same process as the front end; `config.json` /
  `results/*.jsonl` have NO file lock, so two concurrent processes on one
  data dir are forbidden by design — do not add a second entry point without
  solving that first.
- tview interactions: all primitive mutations happen on the app goroutine
  (direct during setup, `QueueUpdateDraw` afterwards). The 5s refresh keeps
  selection by provider **id** (not list index) and restores the detail
  scroll offset — both live in `internal/tui/run.go redrawLocked`.

## Status string contract (cross-layer, CRITICAL)

The 10 final statuses flow probe → engine → store → view → {server, tui, web}:

`ok | timeout_ttft | timeout_total | http_error | conn_error | stream_error |
protocol_error | empty | aborted | cancelled`

- Constants: `internal/probe` exports `StatusOK` … `StatusCancelled`.
  `internal/store` keeps private mirrors (`statusOK`, …).
- Chinese labels: `internal/view.StatusText` is the single Go source; the
  report template funcs, the TUI and any Go consumer delegate to it.
  `web/app.js` keeps its own `STATUS_TEXT`/`STATUS_META` copy —
  `internal/view/status_test.go` parses the embedded app.js and FAILS when
  the two tables drift (contract test, acceptance A6). **Adding/renaming a
  status: probe const, store mirror, view.StatusText, web/app.js — the test
  catches anything missed.**
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

## Server / security contracts (on-demand web panel)

- The listener exists only while the TUI pulled the panel up (`w`); scan
  starts at `--port` (default 10110). Binds `127.0.0.1` only. Write
  endpoints (POST/PUT/DELETE/probe) reject requests whose `Origin` header
  is present and not `127.0.0.1|localhost|::1` + bound port (403). No CORS
  headers anywhere.
- API key handling: GET responses carry `api_key_set` + `api_key_mask`
  (>8 chars: first4+"****"+last4). PUT treats absent JSON field as keep,
  `""`/null as clear, non-empty as replace (`*string`). The TUI edit form
  uses the simplified rule 空着=保持、输入=替换 (no clear-by-empty).
- `base_url` normalized with `strings.TrimRight "/"` before compare/save;
  change of base_url or model ⇒ `revision++` (other fields do not).
- Validation lives in `view.ProviderForm.Validate` — the ONE owner of the
  400 messages; server handlers and the TUI form both call it, so the words
  can never drift apart. Rules: name/model non-empty; base_url `http(s)://`;
  `max_tokens>0`; `interval_sec==0||>=60`;
  `0<ttft_slow_ms<ttft_timeout_ms<=timeout_sec*1000`; `timeout_sec>0`.
- Monitor status priority (§7.2): disabled > manual_only > unknown > stale
  (`>2*interval+timeout`) > ok (+`slow_ttft` when `ttft_ms>ttft_slow_ms`) >
  fail. `probing` is a hint bit, never overrides a status.
- `last_probe` must match the provider's current `revision`, else it is
  treated as absent (status becomes `unknown`, §12.1-8).

## Frontend (web/) conventions

- Plain HTML/JS/CSS, no CDN/external URLs, no framework; embedded via
  `//go:embed` in `web/embed.go` (`web.FS()`).
- The panel is served only by the on-demand web server (TUI `w`); it polls
  `/api/providers` every 5s (overview) — read-only, never triggers probes.
- All server-derived strings are HTML-escaped before injection (`esc()`);
  empty-sample states render "暂无样本", never 0%.
- API JSON shapes are the contract with `internal/view` (ProviderView /
  StatsView / BucketView) — change both sides together.

## TUI conventions (internal/tui)

- tview primitives mutate on the app goroutine only; background work
  (manual probe) lands through `QueueUpdateDraw`.
- Terminal tests use `tcell.NewSimulationScreen` wrapped in `readyScreen`
  (run_test.go) — the wrapper closes a channel on Init to give the test a
  happens-before edge; without it the race detector fires on InjectKey.
- Reading tview state from a test goroutine (e.g. GetText) must go through
  `QueueUpdate` — same ownership rule.
- Status/badge text comes from `view.StatusText`/`view.MonitorText`; never
  hardcode a status label map in the TUI.
- Sparkline: `internal/tui/sparkline.go`, max-normalized like the web chart
  math (bucketPath); nil buckets break the bar with `·`.

## HTML report export (internal/view/report.go, report.tmpl, report_chart.go)

- `view.RenderReport(w io.Writer, …)` is the single rendering pipeline for
  BOTH front ends: the HTTP handler streams it as an attachment, the TUI
  writes it to `<data-dir>/reports/` (actions.go exportReport). An exported
  file is byte-identical regardless of producer.
- The report is rendered via `html/template`; the ONLY
  `template.HTML` values are the chart SVG strings built in
  `report_chart.go` from strconv-formatted numbers and fixed literals —
  never route user/target text through them.
- Key non-leakage has three layers: ProviderView masking, probe-time
  `replaceKey`, and render-time `view.ScrubSecret` in report rows (records
  appended by other paths may bypass probe sanitization). The TUI detail
  rows scrub too (`scrubKey` in detail.go).
- null vs 0 discipline applies to display too: unobserved token counts
  render "—", never "0" (§9.2).

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
