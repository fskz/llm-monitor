# LLM Monitor Go Conventions

> Code-spec extracted from the MVP implementation (task 10-08-mvp-llm-monitor),
> updated by task 10-09-tui-default (TUI default mode + on-demand web panel),
> task 10-10 metrics/settings packs, 10-12-review-fixpack (tview callback
> re-entry, MetricsPack aggregation, settings-validation single owner),
> 10-16-tui-command-bar (command bar replaces all rune shortcuts), and
> 10-16-review-fixpack-2 (retention hysteresis, SSE tolerance, /page,
> dual-sync fmt lock).
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
  ZERO listening ports; the web panel is pulled up from inside the TUI
  (`/web` command) and closed explicitly (no idle timeout). No `web`
  subcommand, no headless mode (PRD decision: unattended operation
  unsupported).
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
- SSE tolerance (review-fixpack-2): whitespace-only `data:` payloads
  (≥2 spaces survive the single-space strip) are keep-alives — same
  TrimSpace tolerance as `[DONE]`; unknown `field:value` lines are ignored
  per the SSE spec (only bare JSON / plain text with no field structure is
  `protocol_error`). Locked by TestSSEToleratesKeepAliveAndUnknownFields.
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
  lost. Trimming is hysteresis-batched (review-fixpack-2): the rewrite fires
  only once the in-memory count passes `maxKeep + trimSlack` (maxKeep/10)
  and trims back to `maxKeep` — a provider at the cap must NOT rewrite on
  every append (§5.3 requires O(1) appends; the rewrite holds the write
  lock and blocks polling readers). Locked by TestTrimSlackHysteresis.
  Corrupt lines are skipped + counted on load, never rewritten away.
- `Seq` is assigned inside `AppendResult`, which returns the stored record —
  callers wanting the seq must use the return value (pass-by-value otherwise
  yields `seq:0`; this was a real bug).
- Pagination cursor = composite `(started_at, seq)`, strictly "before", so
  same-timestamp records are separable. Server encodes it base64.
- Stats: denominator = non-cancelled records under the query's source filter
  (`scheduled`/`manual`/`all`; `cancelled` never counts), window filters on
  `started_at`; zero samples → all pct fields JSON `null` (never 0%).
- **Hot-path statistics go through `store.MetricsPack(providerID, revision,
  window, source)`** (10-12-review-fixpack): ONE `snapshot()` copy + ONE
  filter pass fills Stats / Streaks / Percentiles / FlipRate / ErrorsByKind /
  AvgOnOK / AvgThroughput / Revisions. Consumers: `view.ViewOf`, server
  `handleStats`, `view.RenderReport`, TUI `statsBlock`. Every per-metric
  method (`Stats`, `ComputePercentiles`, …) takes its own full-record copy
  (≤20k records ≈3.7MB), so a consumer calling them one-by-one multiplies
  that copy 6-7× per 5s poll — do NOT do that in new code. Equivalence with
  the individual methods is pinned by `TestMetricsPackEquivalence`
  (store/metricspack_test.go); a new aggregate field must extend that test.

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

- The listener exists only while the TUI pulled the panel up (`/web`
  command); scan starts at `--port` (default 10110). Binds `127.0.0.1` only. Write
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
  Rule ORDER is part of the contract too (review-fixpack-2): web
  `validateProvider` checks in the same order so the first-error message
  matches the server's for the same form. Tool-level settings follow the
  same one-owner rule: `view.ValidateSettings(store.Settings)` (+
  `view.DirWritable`) owns the settings messages; both `PUT /api/settings`
  and the TUI settings form call it (10-12-review-fixpack — two verbatim
  copies used to drift-risk). `PUT /api/settings` trims `report_dir` BEFORE
  validating (DirWritable mkdirs the given path — validating the raw value
  created mis-named directories); write endpoints cap bodies with
  `http.MaxBytesReader` via `server.decodeJSON` (413 over 1 MiB).
- Monitor status priority (§7.2): disabled > manual_only > unknown > stale
  (`>2*interval+timeout`) > ok (+`slow_ttft` when `ttft_ms>ttft_slow_ms`) >
  fail. `probing` is a hint bit, never overrides a status.
- `last_probe` must match the provider's current `revision`, else it is
  treated as absent (status becomes `unknown`, §12.1-8).

## Cache check (task 10-22-cache-check, 2026-10-22)

- Prefix-cache detection is an INDEPENDENT module: `internal/cachecheck`
  (imports only `internal/probe`; two sequential `probe.Do` hops with the
  fixed ×96-repetition prompt — counted ~2620 endpoint tokens, clearing the
  2048-token activation threshold some providers apply (×64 was counted as
  only 1748 and risked false negatives) — max_tokens 8, forced
  `include_usage`, TTFT deadline == total deadline so evidence is never cut
  short). Its per-provider in-flight mutex (`ErrCacheCheckInFlight`) shares
  NOTHING with the probe slot; a check and a probe may run concurrently on
  one provider by design.
- A hop with status `empty` is a USABLE observation, never a failure
  (2026-10-23): the check consumes prefill-side evidence only, and real
  endpoints do stream 200 + clean [DONE] + full usage with zero content
  (finish_reason=length burning the whole tiny budget on invisible output,
  observed at max_tokens 8 and 64 alike). Judge's failure set is exactly the
  prd R1 list: HTTP/conn/stream/protocol errors and timeouts.
- Verdict machine values (`supported|unsupported|unknown|failed`) live in
  `cachecheck`; `cached_tokens > 0` on ANY hop is conclusive (supported) even
  when the other hop's field was relay-stripped. Chinese labels have exactly
  one Go owner (`view.CacheCheckVerdictText`); the `CACHE_VERDICT_TEXT` copy
  in web/app.js is locked by `internal/view/cachecheck_sync_test.go` — same
  discipline as STATUS_TEXT. `cachecheck.AllVerdicts()` is the enumeration
  source the sync test walks.
- Check records persist in `cachecheck/<id>.json` (one latest record per
  provider, atomic overwrite) and MUST NOT enter `results/*.jsonl`, stats,
  health status, or overview — `AppendResult` stays probe-only.
- Naming: `prefill` is taken by the `prefill_tps` throughput metric; the
  detection feature is named `cache`/`cache-check`/前缀缓存 everywhere.

## Frontend (web/) conventions

- Plain HTML/JS/CSS, no CDN/external URLs, no framework; embedded via
  `//go:embed` in `web/embed.go` (`web.FS()`).
- The panel is served only by the on-demand web server (TUI `/web` command);
  it polls `/api/providers` every 5s (overview) — read-only, never triggers
  probes.
- All server-derived strings are HTML-escaped before injection (`esc()`);
  empty-sample states render "暂无样本", never 0%.
- Formatting dual-sync (review-fixpack-2): the JS `fmt*` family and
  `defaultPrompt()` in web/app.js are copies of the Go owners
  (`view` report.go Fmt* / provider.go DefaultPrompt). No JS runtime exists
  in the Go test harness, so `internal/view/fmt_sync_test.go` pins BOTH the
  Go golden values and the JS function sources verbatim
  (whitespace-normalized). Changing either side means consciously mirroring
  the other — same discipline as STATUS_TEXT. The JS `fmtTPOT` guard
  (`<=0` → "—") had drifted before this lock existed.
- API JSON shapes are the contract with `internal/view` (ProviderView /
  StatsView / BucketView) — change both sides together. Empty collections
  serialize `[]`, never `null` (`errors_by_kind`, results `items`).

## TUI conventions (internal/tui)

- tview primitives mutate on the app goroutine only; background work
  (manual probe) lands through `QueueUpdateDraw`.
- **tview callbacks are synchronous and re-entrant** (10-12-review-fixpack):
  `List.SetCurrentItem` (and the first `AddItem`) fires `SetChangedFunc`
  synchronously — restoring the cursor inside a redraw re-enters
  `changed → onChange → redraw → refresh` and overflows the stack. Programmatic
  cursor restores must sit behind a re-entry guard (see `overviewPane.refreshing`
  in overview.go); `List.Clear()` never fires the callback. Modal contract:
  `SetDoneFunc` receives `(-1, "")` for Esc and `(i, label)` for button i —
  a confirm modal must handle every idx, not just the confirm button, or it
  has no dismiss path (deleteDoneFunc in forms.go).
- **After `Application.Run` returns, `QueueUpdateDraw` blocks forever** (the
  event loop that acknowledges the update is gone). Background goroutines
  that outlive the loop (5s ticker, in-flight manual probes) route UI updates
  through `ui.queueDraw`, which consults the `ui.quit` channel closed on both
  exit paths (run.go) — headless mode included.
- Terminal tests use `tcell.NewSimulationScreen` wrapped in `readyScreen`
  (run_test.go) — the wrapper closes a channel on Init to give the test a
  happens-before edge; without it the race detector fires on InjectKey.
- Reading tview state from a test goroutine (e.g. GetText) must go through
  `QueueUpdate` — same ownership rule.
- Status/badge text comes from `view.StatusText`/`view.MonitorText`; never
  hardcode a status label map in the TUI.
- Sparkline: `internal/tui/sparkline.go`, max-normalized like the web chart
  math (bucketPath); nil buckets break the bar with `·`.

## Command bar (task 10-16, replaces all rune shortcuts; +10-16-review-fixpack-2)

- **The command registry is the only action entry**: `internal/tui/command.go`
  `commands` table (`name`/`args` enum/`desc`/`needSel`/`run`) — the 17
  commands (14 base + `/page next|prev` for detail pagination, bare = next
  page, + `/cache` prefix-cache check and `/exportcache` snapshot export,
  2026-10-22/23)
  replaced the old rune keys (q/p/r/w/,/x, n/e/d, 1/2/3/R/S);
  `keys.go` is gone. `/help` renders FROM the registry. The registry has two
  static copies that MUST be synced when a command is added/renamed/its args
  change: `README.md` TUI table and `docs/REQUIREMENTS.md` §4.3.
- **No command name may be a prefix of another** (TestCommandRegistryShape
  enforces it): Enter's execution relies on "several candidates ⟹ the typed
  text is a strict ambiguous prefix, never an exact name".
- **Enter in the command phase requires a unique candidate** (or exact
  match) — firing the highlighted entry on several candidates made a bare
  `/` + Enter execute `/exit` (registry order; the quit command was renamed
  from `/quit` on 2026-10-19). Argument-phase candidates
  share one command; highlighted direct-select stays allowed.
- Interaction contract: `/` prefix opens the prefix-filtered menu (never
  fuzzy); a space after the command name switches the menu to that command's
  `args` candidates (filter applies to the arg text; do NOT TrimSpace the
  whole input — the trailing space IS the phase switch); Tab completes the
  highlighted candidate when the menu is open and is swallowed when closed
  (rotation is NOT Tab's job, 2026-10-19); Esc clears a non-empty input and
  on an empty input enters pane selection (版块轮转); Enter prefers the
  highlighted candidate, else parses the text; bare enum
  command (`/window`, no space/arg) = cycle fallback; invalid arg flashes the
  legal set; non-`/` text Enter is swallowed. Commands act on the current
  selection regardless of focus (`needSel` commands flash when nothing is
  selected). Esc rotation: 命令框 → list → detail → 命令框; outside the
  command bar Tab is swallowed at the app-level capture (tview List maps Tab
  to Down — a passed Tab silently moves the provider selection); the app-level
  capture must yield every key while the command bar holds focus.
- Five red lines (tview pitfalls, verified in both mos-runner and this repo —
  violating any of these breaks input handling at runtime, tests do not
  always catch it):
  1. Inside `InputField.SetChangedFunc` only synchronous `SetText` +
     redraw — never `QueueUpdateDraw` (the draw races ahead of rune insertion
     and eats the character).
  2. The menu is a persistent fixed-height TextView updated via `SetText` —
     NEVER a Pages overlay (Add/RemovePage inside the event loop breaks the
     InputHandler delegation chain).
  3. Tab is intercepted at the control-level capture on the InputField
     (tview's default treats Tab as field-finished and jumps focus, closing
     the menu instead of completing).
  4. Exactly one Focus=true child per Flex layer; move focus with
     `app.SetFocus` directly onto the primitive.
  5. The app-level `SetInputCapture` runs BEFORE the focused primitive's —
     a global handler that consumes Tab/runes starves the command bar.
- Menu viewport (review-fixpack-2): the fixed 3-row menu TextView never gets
  focus, so `drawMenu` must `ScrollTo(menuSel-1, 0)` after `SetText` —
  otherwise ↓ walks the highlight out of the visible window with zero
  feedback.
- **`TextView.GetScrollOffset()` returns `(row, column)`** — binding
  `_, off := …` reads the COLUMN (always 0 for vertical scrolling) and
  silently resets the scroll on every redraw (review-fixpack-2 F16, found
  in redrawLocked: the detail pane jumped back to top every 5s tick).
- Status flashes carry a timestamp (`ui.flashAt`, TTL 5s): `renderStatus`
  (focus rotation, redraws) keeps a fresh flash instead of overwriting it
  with the station hint — otherwise one Tab erased an export path.
- Command-bar text ownership extends the status-text rule: guidance
  (`view.CommandHintText`/`CmdPlaceholderText`/`CmdHelpGuideText`) and
  feedback constants (`view.CmdMsg*`) live in `internal/view/command_text.go`;
  the ONLY sanctioned TUI-side user-visible strings are the registry `desc`
  fields (same table as the handlers).

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
  rows scrub through the SAME exported `view.ScrubSecret` — never
  re-implement redaction locally (the former `scrubKey` copy was removed in
  10-12-review-fixpack).
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
