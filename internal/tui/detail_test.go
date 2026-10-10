package tui

import (
	"fmt"
	"testing"
	"time"

	"llm-monitor/internal/store"
)

func ptrF(v float64) *float64 { return &v }
func ptrI64(v int64) *int64   { return &v }

func TestSparklineSuccessRate(t *testing.T) {
	// 4 buckets: 0%, 50%, 100%, gap — max-normalized against 100%.
	buckets := []store.SeriesBucket{
		{StartMs: 0, Samples: 1, OKPct: ptrF(0)},
		{StartMs: 1, Samples: 1, OKPct: ptrF(50)},
		{StartMs: 2, Samples: 1, OKPct: ptrF(100)},
		{StartMs: 3, Samples: 0, OKPct: nil},
	}
	got := sparkline(buckets, false)
	want := " ▄█·"
	if got != want {
		t.Fatalf("sparkline = %q, want %q", got, want)
	}
}

func TestSparklineLatencyMaxNormalized(t *testing.T) {
	buckets := []store.SeriesBucket{
		{StartMs: 0, Samples: 1, AvgTotalMs: ptrI64(250)},
		{StartMs: 1, Samples: 1, AvgTotalMs: ptrI64(500)},
		{StartMs: 2, Samples: 1, AvgTotalMs: ptrI64(1000)},
	}
	got := sparkline(buckets, true)
	want := "▂▄█" // 250/1000→▂, 500/1000→▄, 1000/1000→█
	if got != want {
		t.Fatalf("latency sparkline = %q, want %q", got, want)
	}
}

func TestSparklineNoData(t *testing.T) {
	got := sparkline([]store.SeriesBucket{{StartMs: 0, Samples: 0}}, false)
	if got != "暂无样本" {
		t.Fatalf("empty sparkline = %q, want 暂无样本", got)
	}
	if got := sparkline(nil, true); got != "暂无样本" {
		t.Fatalf("nil sparkline = %q, want 暂无样本", got)
	}
}

func TestSparklineAllZeroSamples(t *testing.T) {
	// Samples exist but all values are zero: flat baseline, not div-by-zero.
	buckets := []store.SeriesBucket{
		{StartMs: 0, Samples: 1, OKPct: ptrF(0)},
		{StartMs: 1, Samples: 1, OKPct: ptrF(0)},
	}
	if got := sparkline(buckets, false); got != "  " {
		t.Fatalf("all-zero sparkline = %q, want two blanks", got)
	}
}

// seedProviders adds n providers with distinct names and returns their ids.
func seedProviders(t *testing.T, st *store.Store, n int) []int {
	t.Helper()
	ids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		p, err := st.AddProvider(store.Provider{
			Name: fmt.Sprintf("p%d", i), BaseURL: "https://api.example.com",
			Model: "m", Prompt: "hi", MaxTokens: 16, TimeoutSec: 10,
			TTFTTimeoutMs: 5000, TTFTSlowMs: 1000, IntervalSec: 60, Enabled: true,
		})
		if err != nil {
			t.Fatalf("AddProvider: %v", err)
		}
		ids = append(ids, p.ID)
	}
	return ids
}

// seedResults appends results for a provider covering multiple pages.
// Append order matches the real engine invariant: later appends carry both
// a larger Seq and a later StartedAt (oldest first).
func seedResults(t *testing.T, st *store.Store, id int, n int) {
	t.Helper()
	base := time.Now().Add(-time.Duration(n) * time.Second).UnixMilli()
	for i := 0; i < n; i++ {
		started := base + int64(i)*1000
		res := store.Result{
			ProviderID: id, Revision: 1, BaseURL: "https://api.example.com", Model: "m",
			Source: store.SourceScheduled, StartedAt: started,
			FinishedAt: started + 500, Success: true, Status: "ok",
			TotalMs: 500,
		}
		if _, err := st.AppendResult(res); err != nil {
			t.Fatalf("AppendResult: %v", err)
		}
	}
}

// Selection must survive a refresh by provider id (R3: 刷新不丢选中行).
func TestOverviewRefreshPreservesSelection(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 3)

	o := newOverviewPane(st, nil, nil)
	o.refresh()
	o.selected = ids[2] // select the third provider

	// Simulate the 5s tick: full rebuild.
	o.refresh()

	if o.selectedID() != ids[2] {
		t.Fatalf("selection after refresh = %d, want %d", o.selectedID(), ids[2])
	}
	if o.list.GetCurrentItem() != 2 {
		t.Fatalf("list cursor after refresh = %d, want 2", o.list.GetCurrentItem())
	}

	// Deleting the selected provider falls back to the first entry.
	if err := st.DeleteProvider(ids[2]); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	o.refresh()
	if o.selectedID() != ids[0] {
		t.Fatalf("selection after delete = %d, want %d (first)", o.selectedID(), ids[0])
	}
}

// Pagination walks strictly older via (started_at, seq) and PgUp returns
// through the cursor stack (web/app.js prevCursors pattern).
func TestDetailPagination(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 1)
	p, _ := st.GetProvider(ids[0])
	seedResults(t, st, ids[0], pageSize*2+3) // 43 rows → 3 pages

	d := newDetailPane(st, nil)

	// Page 1: newest pageSize rows, nextCursor points at row 20 (index from
	// newest), pageCursor still nil (we are on the newest page).
	d.renderText(p)
	if d.pageCursor != nil {
		t.Fatal("first render must not advance the page cursor")
	}
	if d.nextCursor == nil {
		t.Fatal("43 rows must produce a next cursor on page 1")
	}

	// PgDn → page 2 (a real keypress always redraws before the next press).
	if !d.pageNext() {
		t.Fatal("pageNext must advance with a next cursor present")
	}
	page2 := d.st.QueryResults(p.ID, p.Revision, d.filters.source, d.filters.window,
		pageSize+1, d.cursorSeq(), d.cursorStartedAt())
	if len(page2) != pageSize+1 { // 43 - 20 = 23 → full page + 1 lookahead
		t.Fatalf("page2 rows = %d, want %d", len(page2), pageSize+1)
	}
	d.renderText(p) // the redraw recomputes the page-2 next cursor

	// PgDn → page 3 (23 rows left; 20 shown, 3 remain → hasMore=false).
	if !d.pageNext() {
		t.Fatal("pageNext page 2→3 must advance")
	}
	d.renderText(p) // recompute nextCursor from page 3
	if d.nextCursor != nil {
		t.Fatalf("page 3 is the last page, nextCursor = %+v, want nil", d.nextCursor)
	}
	if d.pageNext() {
		t.Fatal("pageNext on the last page must be a no-op")
	}

	// PgUp ×2 → back to page 1 (cursor nil).
	if !d.pagePrev() {
		t.Fatal("pagePrev page 3→2 must move")
	}
	if !d.pagePrev() {
		t.Fatal("pagePrev page 2→1 must move")
	}
	if d.pageCursor != nil {
		t.Fatalf("after walking back to page 1 cursor = %+v, want nil", d.pageCursor)
	}
	if d.pagePrev() {
		t.Fatal("pagePrev on page 1 must be a no-op")
	}
}

// Filter cycles walk the same value sets as the panel selects.
func TestDetailFilterCycles(t *testing.T) {
	st, _ := store.New(t.TempDir())
	d := newDetailPane(st, nil)

	// window: 24h default → 7d → 1h → 24h
	if d.filters.window != 24*time.Hour {
		t.Fatalf("default window = %v, want 24h", d.filters.window)
	}
	d.cycleWindow()
	if d.filters.window != 7*24*time.Hour {
		t.Fatalf("cycleWindow 1 = %v, want 7d", d.filters.window)
	}
	d.cycleWindow()
	if d.filters.window != time.Hour {
		t.Fatalf("cycleWindow 2 = %v, want 1h", d.filters.window)
	}

	// revision: "" → all → ""
	d.cycleRevision()
	if d.filters.revision != "all" {
		t.Fatalf("cycleRevision 1 = %q, want all", d.filters.revision)
	}

	// source: scheduled → manual → all → scheduled
	d.cycleSource()
	if d.filters.source != store.SourceManual {
		t.Fatalf("cycleSource 1 = %q, want manual", d.filters.source)
	}
	d.cycleSource()
	if d.filters.source != store.SourceAll {
		t.Fatalf("cycleSource 2 = %q, want all", d.filters.source)
	}
	d.cycleSource()
	if d.filters.source != store.SourceScheduled {
		t.Fatalf("cycleSource 3 = %q, want scheduled", d.filters.source)
	}
}

// revisionOf resolves the filter to store arguments.
func TestDetailRevisionOf(t *testing.T) {
	st, _ := store.New(t.TempDir())
	d := newDetailPane(st, nil)
	p := store.Provider{ID: 1, Revision: 3}

	if got := d.filters.revisionOf(p); got != 3 {
		t.Fatalf("revisionOf current = %d, want 3", got)
	}
	d.filters.revision = "all"
	if got := d.filters.revisionOf(p); got != store.RevisionAll {
		t.Fatalf("revisionOf all = %d, want %d", got, store.RevisionAll)
	}
}

// The detail text carries the monitor badge, stats and the results header —
// smoke-level assurance the assembly path does not panic on a fresh store.
func TestDetailRenderTextSmoke(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 1)
	seedResults(t, st, ids[0], 3)
	p, _ := st.GetProvider(ids[0])

	d := newDetailPane(st, nil)
	txt := d.renderText(p)
	// The seeded scheduled rows give a fresh last_probe → status "可用";
	// the stats block and results table are real.
	for _, want := range []string{"p0", "可用", "统计", "探测记录", "成功"} {
		if !containsPlain(txt, want) {
			t.Errorf("detail text missing %q", want)
		}
	}
}

func containsPlain(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TUI detail stats follow the source filter (task 10-10 A5, unit half):
// with a mixed history the manual filter's stats block counts only the
// manual samples and renders them.
func TestDetailStatsFollowSource(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 1)
	seedResults(t, st, ids[0], 3) // scheduled ok rows
	// one manual row
	now := time.Now().UnixMilli()
	if _, err := st.AppendResult(store.Result{
		ProviderID: ids[0], Revision: 1, BaseURL: "https://api.example.com", Model: "m",
		Source: store.SourceManual, StartedAt: now - 1000, FinishedAt: now - 500,
		Success: true, Status: "ok", TotalMs: 500,
	}); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
	p, _ := st.GetProvider(ids[0])

	d := newDetailPane(st, nil)
	d.filters.source = store.SourceManual
	txt := d.renderText(p)
	if !containsPlain(txt, "样本 1") {
		t.Fatalf("manual-filtered stats = missing 样本 1:\n%s", txt)
	}
	d.filters.source = store.SourceAll
	txt = d.renderText(p)
	if !containsPlain(txt, "样本 4") {
		t.Fatalf("all-filtered stats = missing 样本 4:\n%s", txt)
	}
}
