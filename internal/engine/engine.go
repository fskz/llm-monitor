// Package engine schedules and runs availability probes for every configured
// provider (docs/REQUIREMENTS.md §4.2, §7.1).
//
// Each enabled provider with a positive interval gets one scheduler goroutine
// that probes immediately and then once per interval. Scheduled and manual
// probes share a single in-flight slot per provider: when the slot is taken,
// a due scheduled tick is skipped — nothing queues and no sample is produced.
// Ticks missed while a probe is running, or while the process is down, are
// never replayed.
//
// Every probe runs under a per-provider lifecycle context; editing,
// disabling, removing or shutting down cancels it, and the engine rewrites
// the outcome to status "cancelled" (excluded from statistics) before
// persisting it, so operator actions never count as target failures.
package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"llm-monitor/internal/probe"
	"llm-monitor/internal/store"
)

// ErrInFlight is returned by ProbeNow when the provider already has a probe
// running: scheduled and manual probes share one in-flight slot (§7.1).
var ErrInFlight = errors.New("probe already in flight")

// windDownWait bounds how long Update/Remove wait for a canceled scheduler
// goroutine (and its in-flight probe) to finish persisting its final record.
const windDownWait = 10 * time.Second

// providerRunner is the engine's live state for one provider: a config
// snapshot, the lifecycle context governing scheduling and in-flight probes,
// the shared in-flight slot, and the scheduler-exit signal.
type providerRunner struct {
	id       int
	provider store.Provider // snapshot used for scheduling and probing
	ctx      context.Context
	cancel   context.CancelFunc
	inFlight atomic.Bool
	// hasScheduler reports whether a scheduler goroutine owns done; runners
	// for manual-only/disabled providers pre-close done so stopRunner never
	// waits on a goroutine that does not exist.
	hasScheduler bool
	done         chan struct{}
}

// Engine schedules probes for the providers registered with the store and
// persists every completed outcome through it. The zero value is not usable;
// call New.
type Engine struct {
	store  *store.Store
	client *http.Client

	// opsMu serializes Start/Add/Update/Remove so concurrent mutations can
	// never leave two scheduler goroutines for one provider.
	opsMu sync.Mutex

	mu       sync.Mutex
	started  bool
	stopped  bool
	startCtx context.Context // set by Start; parent of runner lifecycles
	baseCtx  context.Context // fallback parent for runners added before Start
	baseCxl  context.CancelFunc
	runners  map[int]*providerRunner

	// wg tracks scheduler goroutines and manual probes, so Shutdown can wait
	// for every in-flight outcome to be persisted before returning.
	wg sync.WaitGroup

	storageMu  sync.Mutex
	storageErr string
}

// New returns an engine probing through client and persisting through st.
// client must not set Timeout — probe deadlines are context-driven; nil
// selects a default client.
func New(st *store.Store, client *http.Client) *Engine {
	if client == nil {
		client = &http.Client{}
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	return &Engine{
		store:   st,
		client:  client,
		baseCtx: baseCtx,
		baseCxl: cancel,
		runners: make(map[int]*providerRunner),
	}
}

// Start registers every stored provider and begins scheduling for those
// enabled with a positive interval: one immediate probe, then one per
// interval (§7.1). ctx becomes the parent of all runner lifecycles, so
// canceling it stops scheduling and cancels in-flight probes. Start is
// idempotent and is expected to be called once, before Add/Update/ProbeNow.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	if e.started || e.stopped {
		e.mu.Unlock()
		return
	}
	e.started = true
	if ctx != nil {
		e.startCtx = ctx
	}
	e.mu.Unlock()

	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	for _, p := range e.store.GetProviders() {
		e.startRunner(p)
	}
}

// Add registers a new provider, starting its schedule when it is enabled
// with a positive interval (§7.1). The provider is expected to be persisted
// already; the engine only owns runtime state.
func (e *Engine) Add(p store.Provider) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	e.startRunner(p)
}

// Update applies any configuration change (including enable/disable): the
// previous runner is canceled — its in-flight probe, if any, is rewritten to
// "cancelled" and persisted — then scheduling restarts under the new
// configuration (§4.1). Update returns only after the old scheduler wound
// down, so no request issued under the old config is still alive.
func (e *Engine) Update(p store.Provider) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	e.stopRunner(p.ID)
	e.startRunner(p)
}

// Remove cancels the provider's in-flight probe (persisted as "cancelled")
// and stops its scheduling. Deleting the stored configuration and history is
// the server's job.
func (e *Engine) Remove(id int) {
	e.opsMu.Lock()
	defer e.opsMu.Unlock()
	e.stopRunner(id)
}

// startRunner registers p and, when it must be auto-probed, spawns its
// scheduler goroutine. Callers hold opsMu; an existing runner for the same
// ID is stopped first, so there are never two live schedulers per provider.
func (e *Engine) startRunner(p store.Provider) {
	e.stopRunner(p.ID)

	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	parent := e.baseCtx
	if e.startCtx != nil {
		parent = e.startCtx
	}
	ctx, cancel := context.WithCancel(parent)
	r := &providerRunner{
		id:       p.ID,
		provider: p,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	if p.Enabled && p.IntervalSec > 0 {
		r.hasScheduler = true
		e.runners[p.ID] = r
		e.wg.Add(1)
		e.mu.Unlock()
		go e.schedule(r)
		return
	}
	// Manual-only or disabled: the runner exists so ProbeNow still finds the
	// provider (§4.6); done is pre-closed because no goroutine owns it.
	close(r.done)
	e.runners[p.ID] = r
	e.mu.Unlock()
}

// stopRunner cancels the runner for id and, when it has a scheduler
// goroutine, waits (bounded) until that goroutine exited — which happens
// only after any in-flight probe returned and its record was persisted.
// Callers hold opsMu.
func (e *Engine) stopRunner(id int) {
	e.mu.Lock()
	r := e.runners[id]
	delete(e.runners, id)
	e.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	if !r.hasScheduler {
		return
	}
	select {
	case <-r.done:
	case <-time.After(windDownWait):
		fmt.Fprintf(os.Stderr, "engine: provider %d scheduler did not stop within %s\n", id, windDownWait)
	}
}

// schedule is the per-provider scheduler goroutine: probe immediately, then
// once per interval. A tick landing while a probe is in flight is skipped —
// no queueing, no sample, no backfill (§7.1). A panic here is recovered and
// logged so one provider can never take down the process (§5.5).
func (e *Engine) schedule(r *providerRunner) {
	defer e.wg.Done()
	defer close(r.done)
	defer func() {
		if v := recover(); v != nil {
			fmt.Fprintf(os.Stderr, "engine: provider %d scheduler panic: %v\n%s", r.id, v, debug.Stack())
		}
	}()

	// Same guard as the tick case below: a runner whose lifecycle was already
	// canceled (Start racing Shutdown/Remove) must not launch a probe that
	// never sends a request yet persists a phantom cancelled record.
	if r.ctx.Err() != nil {
		return
	}
	e.scheduledProbe(r)
	if r.ctx.Err() != nil {
		return
	}
	ticker := time.NewTicker(time.Duration(r.provider.IntervalSec) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			// Both cases can be ready at once (shutdown racing a tick) and
			// select picks randomly; guard so a tick observed after cancel
			// exits instead of launching a probe that never sends a request
			// yet persists a phantom cancelled record.
			if r.ctx.Err() != nil {
				return
			}
			e.scheduledProbe(r)
		}
	}
}

// scheduledProbe runs one scheduled probe unless the shared in-flight slot
// is taken: a failed CAS means the tick is skipped, producing neither a
// request nor a sample (§7.1).
func (e *Engine) scheduledProbe(r *providerRunner) {
	if !r.inFlight.CompareAndSwap(false, true) {
		return
	}
	defer r.inFlight.Store(false)
	res := e.runProbe(r.ctx, r.provider, store.SourceScheduled)
	e.persist(&res)
}

// ProbeNow runs one manual probe synchronously in the caller's goroutine —
// its wall time is therefore bounded by the provider's total timeout — and
// returns the persisted result (§4.6). Manual and scheduled probes share the
// in-flight slot: ErrInFlight is returned when one is already running.
// Disabled and manual-only providers may still be probed manually. When
// persisting fails, both the (unpersisted) result and the storage error are
// returned so callers can surface the storage problem explicitly.
func (e *Engine) ProbeNow(id int) (*store.Result, error) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil, fmt.Errorf("engine: shutting down")
	}
	r := e.runners[id]
	if r == nil {
		e.mu.Unlock()
		return nil, fmt.Errorf("engine: unknown provider %d", id)
	}
	if !r.inFlight.CompareAndSwap(false, true) {
		e.mu.Unlock()
		return nil, ErrInFlight
	}
	// wg.Add while holding mu: Shutdown can never sit between the stopped
	// check and wg.Wait.
	e.wg.Add(1)
	e.mu.Unlock()
	defer e.wg.Done()
	defer r.inFlight.Store(false)

	res := e.runProbe(r.ctx, r.provider, store.SourceManual)
	if err := e.persist(&res); err != nil {
		return &res, fmt.Errorf("engine: persist manual result: %w", err)
	}
	return &res, nil
}

// Probing reports whether the provider currently has a probe in flight
// (scheduled or manual).
func (e *Engine) Probing(id int) bool {
	e.mu.Lock()
	r := e.runners[id]
	e.mu.Unlock()
	return r != nil && r.inFlight.Load()
}

// runProbe executes one probe against the provider snapshot and fills the
// persisted record. started_at/finished_at are UTC Unix milliseconds at the
// probe's start/end; durations come from the probe's monotonic clock (§9.2).
func (e *Engine) runProbe(ctx context.Context, p store.Provider, source string) store.Result {
	started := time.Now()
	out := probe.Do(ctx, e.client, probe.Target{
		BaseURL:        p.BaseURL,
		APIKey:         p.APIKey,
		Model:          p.Model,
		Prompt:         p.Prompt,
		MaxTokens:      p.MaxTokens,
		TTFTTimeoutMs:  p.TTFTTimeoutMs,
		TotalTimeoutMs: p.TimeoutSec * 1000,
		IncludeUsage:   p.IncludeUsage,
	})
	res := store.Result{
		ProviderID:       p.ID,
		Revision:         p.Revision,
		BaseURL:          p.BaseURL,
		Model:            p.Model,
		Source:           source,
		StartedAt:        started.UnixMilli(),
		FinishedAt:       time.Now().UnixMilli(),
		Success:          out.Status == probe.StatusOK,
		TTFTMs:           out.TTFTMs,
		TotalMs:          out.TotalMs,
		Status:           out.Status,
		HTTPStatus:       out.HTTPStatus,
		Error:            out.ErrDetail,
		OutputPreview:    out.OutputPreview,
		PromptTokens:     out.PromptTokens,
		CompletionTokens: out.CompletionTokens,
	}
	if source == store.SourceScheduled {
		res.OutputPreview = "" // §9.2: scheduled probes store no preview
	}
	if ctx.Err() != nil && out.Status == probe.StatusConnError {
		// The lifecycle context was canceled (edit, disable, remove,
		// shutdown): an operator action, never a target failure (§3.3, §5.5).
		res.Status = probe.StatusCancelled
		res.Success = false
	}
	return res
}

// persist appends the result and stores the persisted form (with its assigned
// Seq) back into *res, surfacing storage failures instead of masking them as
// probe failures (§5.5). The recorded message names the provider for the
// panel and never contains the API key.
func (e *Engine) persist(res *store.Result) error {
	stored, err := e.store.AppendResult(*res)
	if err != nil {
		label := fmt.Sprintf("id %d", res.ProviderID)
		if p, ok := e.store.GetProvider(res.ProviderID); ok && p.Name != "" {
			label = fmt.Sprintf("%q (id %d)", p.Name, res.ProviderID)
		}
		msg := fmt.Sprintf("provider %s: persist probe result: %v", label, err)
		e.storageMu.Lock()
		e.storageErr = msg
		e.storageMu.Unlock()
		fmt.Fprintln(os.Stderr, "engine:", msg)
		return err
	}
	*res = stored
	return nil
}

// StorageError returns a summary of the most recent AppendResult failure, or
// "" when every persist succeeded.
func (e *Engine) StorageError() string {
	e.storageMu.Lock()
	defer e.storageMu.Unlock()
	return e.storageErr
}

// Shutdown stops all scheduling, cancels every in-flight probe (each is
// persisted as "cancelled" before its goroutine reports done) and waits up
// to timeout for that to finish (§5.5). Shutdown is idempotent.
func (e *Engine) Shutdown(timeout time.Duration) {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return
	}
	e.stopped = true
	runners := make([]*providerRunner, 0, len(e.runners))
	for _, r := range e.runners {
		runners = append(runners, r)
	}
	e.runners = make(map[int]*providerRunner)
	e.mu.Unlock()

	e.baseCxl()
	for _, r := range runners {
		r.cancel()
	}

	done := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		fmt.Fprintf(os.Stderr, "engine: shutdown still waiting after %s\n", timeout)
	}
}
