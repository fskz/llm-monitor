package store

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, dir
}

func mustAddProvider(t *testing.T, s *Store, name string) Provider {
	t.Helper()
	p, err := s.AddProvider(Provider{Name: name, BaseURL: "http://localhost:1/v1", Model: "m"})
	if err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	return p
}

// mkResult builds a scheduled probe result at time start with the given
// status; convenience wrappers set source/revision/ttft below.
func mkResult(p Provider, status string, startedAt int64) Result {
	return Result{
		ProviderID: p.ID,
		Revision:   p.Revision,
		BaseURL:    p.BaseURL,
		Model:      p.Model,
		Source:     SourceScheduled,
		StartedAt:  startedAt,
		FinishedAt: startedAt + 1000,
		Success:    status == statusOK,
		TotalMs:    1000,
		Status:     status,
	}
}

func appendResult(t *testing.T, s *Store, r Result) {
	t.Helper()
	if _, err := s.AppendResult(r); err != nil {
		t.Fatalf("AppendResult: %v", err)
	}
}

func TestRestartConsistency(t *testing.T) {
	s, dir := newTestStore(t)
	p := mustAddProvider(t, s, "a")
	if p.ID != 1 || p.Revision != 1 || p.CreatedAt == 0 {
		t.Fatalf("unexpected provider %+v", p)
	}
	nowMs := time.Now().UnixMilli()
	for i := 0; i < 3; i++ {
		r := mkResult(p, statusOK, nowMs-int64(i)*1000)
		r.TTFTMs = ptrInt64(int64(100 + i))
		appendResult(t, s, r)
	}
	if s.ResultCount(p.ID) != 3 {
		t.Fatalf("count = %d, want 3", s.ResultCount(p.ID))
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok := s2.GetProvider(p.ID)
	if !ok || got.Name != "a" || got.ID != p.ID {
		t.Fatalf("provider after restart: %+v ok=%v", got, ok)
	}
	if s2.ResultCount(p.ID) != 3 {
		t.Fatalf("count after restart = %d, want 3", s2.ResultCount(p.ID))
	}
	res := s2.QueryResults(p.ID, RevisionAll, SourceAll, 24*time.Hour, 10, nil, nil)
	if len(res) != 3 {
		t.Fatalf("query after restart = %d records, want 3", len(res))
	}
	// Seq continues monotonically after restart.
	appendResult(t, s2, mkResult(p, statusOK, time.Now().UnixMilli()))
	newest := s2.QueryResults(p.ID, RevisionAll, SourceAll, 24*time.Hour, 1, nil, nil)
	if len(newest) != 1 || newest[0].Seq != 4 {
		t.Fatalf("seq after restart = %+v, want seq 4", newest)
	}
}

func TestIDNotReused(t *testing.T) {
	s, _ := newTestStore(t)
	p1 := mustAddProvider(t, s, "first")
	p2 := mustAddProvider(t, s, "second")
	if p2.ID != p1.ID+1 {
		t.Fatalf("ids %d,%d not consecutive", p1.ID, p2.ID)
	}
	if err := s.DeleteProvider(p1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	p3 := mustAddProvider(t, s, "third")
	if p3.ID <= p2.ID {
		t.Fatalf("new id %d reused (<= %d)", p3.ID, p2.ID)
	}
	if _, ok := s.GetProvider(p1.ID); ok {
		t.Fatal("deleted provider still retrievable")
	}
}

// writeRawConfig writes a config.json with a custom retention cap so New
// loads it; used by the trim test.
func writeRawConfig(t *testing.T, dir string, maxKeep int) {
	t.Helper()
	cfg := configFile{Version: 1, NextID: 1, MaxResultsPerProvider: maxKeep, Providers: []Provider{}}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, filePerm); err != nil {
		t.Fatal(err)
	}
}

func TestTrimKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	writeRawConfig(t, dir, 5)
	s, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p := mustAddProvider(t, s, "trim")
	nowMs := time.Now().UnixMilli()
	for i := 1; i <= 8; i++ {
		r := mkResult(p, statusOK, nowMs-int64(8-i)*1000)
		appendResult(t, s, r)
	}
	if s.ResultCount(p.ID) != 5 {
		t.Fatalf("in-memory count = %d, want 5", s.ResultCount(p.ID))
	}
	// Newest 5 kept: the file must contain exactly records with the 5
	// highest Seq values (Seq == append order here).
	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.ResultCount(p.ID) != 5 {
		t.Fatalf("on-disk count after reload = %d, want 5", s2.ResultCount(p.ID))
	}
	res := s2.QueryResults(p.ID, RevisionAll, SourceAll, 24*time.Hour, 10, nil, nil)
	if len(res) != 5 {
		t.Fatalf("reload query = %d, want 5", len(res))
	}
	for i, r := range res {
		if r.Seq != int64(8-i) {
			t.Fatalf("kept seq[%d] = %d, want %d (newest first)", i, r.Seq, 8-i)
		}
	}
	// Seq remains monotonic after trimming: next append gets Seq 9.
	appendResult(t, s2, mkResult(p, statusOK, time.Now().UnixMilli()))
	newest := s2.QueryResults(p.ID, RevisionAll, SourceAll, 24*time.Hour, 1, nil, nil)
	if len(newest) != 1 || newest[0].Seq != 9 {
		t.Fatalf("seq after trim = %+v, want seq 9", newest)
	}
}

func TestCorruptLinesSkippedNotRewritten(t *testing.T) {
	s, dir := newTestStore(t)
	p := mustAddProvider(t, s, "corrupt")
	nowMs := time.Now().UnixMilli()
	for i := 0; i < 3; i++ {
		appendResult(t, s, mkResult(p, statusOK, nowMs-int64(i)*1000))
	}
	// Inject a corrupt line at the end of the history file.
	f, err := os.OpenFile(s.resultsPath(p.ID), os.O_APPEND|os.O_WRONLY, filePerm)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not json{\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s2, err := New(dir)
	if err != nil {
		t.Fatalf("reopen with corrupt line: %v", err)
	}
	if s2.ResultCount(p.ID) != 3 {
		t.Fatalf("count = %d, want 3 intact records", s2.ResultCount(p.ID))
	}
	if got := s2.CorruptLines(p.ID); got != 1 {
		t.Fatalf("CorruptLines = %d, want 1", got)
	}
	if m := s2.GetCorruptCounts(); m[p.ID] != 1 {
		t.Fatalf("GetCorruptCounts = %v, want {1:1}", m)
	}
	// The corrupt line must still be on disk: loading never rewrites.
	data, err := os.ReadFile(s2.resultsPath(p.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !containsLine(string(data), "not json{") {
		t.Fatal("corrupt line was dropped from the file on load")
	}
	// Appending after a corrupt load still works and preserves the line.
	appendResult(t, s2, mkResult(p, statusOK, time.Now().UnixMilli()))
	if s2.ResultCount(p.ID) != 4 {
		t.Fatalf("count after append = %d, want 4", s2.ResultCount(p.ID))
	}
}

func TestStatsRatios(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "stats")
	nowMs := time.Now().UnixMilli()
	// 3 ok + 1 timeout_ttft + 1 timeout_total + 1 http_error (denominator
	// 6) + 1 cancelled + 1 manual ok (excluded).
	for i := 0; i < 3; i++ {
		r := mkResult(p, statusOK, nowMs-int64(i)*60_000)
		r.TTFTMs = ptrInt64(900)
		appendResult(t, s, r)
	}
	appendResult(t, s, mkResult(p, statusTimeoutTTFT, nowMs-4*60_000))
	appendResult(t, s, mkResult(p, statusTimeoutTotal, nowMs-5*60_000))
	httpErr := mkResult(p, statusHTTPError, nowMs-6*60_000)
	httpErr.HTTPStatus = ptrInt(401)
	appendResult(t, s, httpErr)
	cancelled := mkResult(p, statusCancelled, nowMs-7*60_000)
	appendResult(t, s, cancelled)
	manual := mkResult(p, statusOK, nowMs-8*60_000)
	manual.Source = SourceManual
	appendResult(t, s, manual)

	st := s.Stats(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if st.Samples != 6 || st.OK != 3 || st.TimeoutTTFT != 1 || st.TimeoutTotal != 1 || st.ErrorCount != 1 {
		t.Fatalf("counts = %+v", st)
	}
	assertPct(t, "ok", st.OKPct, 50)
	assertPct(t, "timeout", st.TimeoutPct, 33.333333)
	assertPct(t, "error", st.ErrorPct, 16.666667)

	// Source filtering (task 10-10-stats-by-source A1/A2): manual selects
	// only the manual ok sample; all merges both (cancelled never counts).
	if m := s.Stats(p.ID, p.Revision, 24*time.Hour, SourceManual); m.Samples != 1 || m.OK != 1 {
		t.Fatalf("manual counts = %+v, want samples 1 ok 1", m)
	}
	if a := s.Stats(p.ID, p.Revision, 24*time.Hour, SourceAll); a.Samples != 7 || a.OK != 4 {
		t.Fatalf("all counts = %+v, want samples 7 ok 4", a)
	}

	// No samples → nil percentages, never 0 or 100.
	fresh := mustAddProvider(t, s, "fresh")
	empty := s.Stats(fresh.ID, fresh.Revision, 24*time.Hour, SourceScheduled)
	if empty.Samples != 0 || empty.OKPct != nil || empty.TimeoutPct != nil || empty.ErrorPct != nil {
		t.Fatalf("empty stats = %+v, want nil pcts", empty)
	}
	// A manual-only view of a scheduled-only history is equally empty.
	if me := s.Stats(fresh.ID, fresh.Revision, 24*time.Hour, SourceManual); me.Samples != 0 || me.OKPct != nil {
		t.Fatalf("empty manual stats = %+v, want nil pcts", me)
	}
}

func TestStatsWindowFilter(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "window")
	nowMs := time.Now().UnixMilli()
	appendResult(t, s, mkResult(p, statusOK, nowMs-10*60_000))  // 10 min ago
	appendResult(t, s, mkResult(p, statusOK, nowMs-3*3600_000)) // 3 h ago

	st := s.Stats(p.ID, p.Revision, time.Hour, SourceScheduled)
	if st.Samples != 1 || st.OK != 1 {
		t.Fatalf("1h window stats = %+v, want 1 sample", st)
	}
	stAll := s.Stats(p.ID, p.Revision, 24*time.Hour, SourceScheduled)
	if stAll.Samples != 2 {
		t.Fatalf("24h window stats = %+v, want 2 samples", stAll)
	}
}

func TestStatsRevisionFilter(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "rev")
	nowMs := time.Now().UnixMilli()
	r1 := mkResult(p, statusOK, nowMs-60_000)
	r1.Revision = 1
	appendResult(t, s, r1)
	r2 := mkResult(p, statusTimeoutTTFT, nowMs-30_000)
	r2.Revision = 2
	appendResult(t, s, r2)
	r3 := mkResult(p, statusOK, nowMs-15_000)
	r3.Revision = 2
	appendResult(t, s, r3)

	st2 := s.Stats(p.ID, 2, 24*time.Hour, SourceScheduled)
	if st2.Samples != 2 || st2.OK != 1 || st2.TimeoutTTFT != 1 {
		t.Fatalf("revision-2 stats = %+v", st2)
	}
	stAll := s.Stats(p.ID, RevisionAll, 24*time.Hour, SourceScheduled)
	if stAll.Samples != 3 || stAll.OK != 2 {
		t.Fatalf("all-revision stats = %+v", stAll)
	}
}

func TestPaginationCursor(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "page")
	base := time.Now().UnixMilli() - 3600_000
	for i := 0; i < 13; i++ {
		appendResult(t, s, mkResult(p, statusOK, base+int64(i)*60_000))
	}

	var pages [][]Result
	var beforeSeq, beforeStartedAt *int64
	for {
		page := s.QueryResults(p.ID, RevisionAll, SourceAll, 24*time.Hour, 5, beforeSeq, beforeStartedAt)
		if len(page) == 0 {
			break
		}
		pages = append(pages, page)
		last := page[len(page)-1]
		beforeSeq, beforeStartedAt = &last.Seq, &last.StartedAt
	}
	if len(pages) != 3 || len(pages[0]) != 5 || len(pages[1]) != 5 || len(pages[2]) != 3 {
		t.Fatalf("pages = %v lengths", lenIdx(pages))
	}
	// Descending order, no duplicates across pages.
	seen := make(map[int64]bool)
	var prev *Result
	for _, page := range pages {
		for _, r := range page {
			if seen[r.Seq] {
				t.Fatalf("duplicate seq %d", r.Seq)
			}
			seen[r.Seq] = true
			if prev != nil &&
				(prev.StartedAt < r.StartedAt || (prev.StartedAt == r.StartedAt && prev.Seq <= r.Seq)) {
				t.Fatalf("order broken: %+v then %+v", prev, r)
			}
			cur := r
			prev = &cur
		}
	}

	// Same StartedAt records are told apart by the composite cursor.
	q := mustAddProvider(t, s, "ties")
	same := time.Now().UnixMilli()
	for i := 0; i < 3; i++ {
		appendResult(t, s, mkResult(q, statusOK, same))
	}
	first := s.QueryResults(q.ID, RevisionAll, SourceAll, time.Hour, 1, nil, nil)
	if len(first) != 1 || first[0].Seq != 3 {
		t.Fatalf("first of ties = %+v, want seq 3", first)
	}
	rest := s.QueryResults(q.ID, RevisionAll, SourceAll, time.Hour, 10, &first[0].Seq, &first[0].StartedAt)
	if len(rest) != 2 || rest[0].Seq != 2 || rest[1].Seq != 1 {
		t.Fatalf("rest of ties = %+v", rest)
	}
}

func TestSeriesBuckets(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "series")

	// Anchor samples to fixed offsets inside the newest 5-minute bucket of
	// the series grid so the assertions do not depend on the wall-clock
	// phase when the test runs.
	gridStart, width, count := bucketLayout(time.Hour)
	newestBucketEnd := gridStart.Add(time.Duration(count) * width)
	okA := mkResult(p, statusOK, newestBucketEnd.Add(-4*time.Minute).UnixMilli()) // newest bucket
	okA.TTFTMs = ptrInt64(100)
	okA.TotalMs = 300
	appendResult(t, s, okA)
	okB := mkResult(p, statusOK, newestBucketEnd.Add(-30*time.Second).UnixMilli()) // same bucket
	okB.TTFTMs = ptrInt64(300)
	okB.TotalMs = 500
	appendResult(t, s, okB)
	appendResult(t, s, mkResult(p, statusTimeoutTotal, newestBucketEnd.Add(-45*time.Second).UnixMilli())) // fails: excluded from averages

	buckets := s.Series(p.ID, p.Revision, time.Hour, SourceScheduled)
	if len(buckets) != 12 {
		t.Fatalf("buckets = %d, want 12", len(buckets))
	}
	fb := buckets[len(buckets)-1] // newest bucket holds all three samples
	var filled int
	for _, b := range buckets {
		if b.Samples > 0 {
			filled++
		} else if b.OKPct != nil || b.AvgTTFTMs != nil || b.AvgTotalMs != nil {
			t.Fatalf("empty bucket %+v has non-nil values", b)
		}
	}
	if filled != 1 {
		t.Fatalf("filled buckets = %d, want 1", filled)
	}
	if fb.Samples != 3 {
		t.Fatalf("bucket samples = %d, want 3", fb.Samples)
	}
	assertPct(t, "bucket ok", fb.OKPct, 66.666667)
	// Averages cover ok samples only: ttft (100+300)/2=200, total (300+500)/2=400.
	if fb.AvgTTFTMs == nil || *fb.AvgTTFTMs != 200 {
		t.Fatalf("avg ttft = %v, want 200", fb.AvgTTFTMs)
	}
	if fb.AvgTotalMs == nil || *fb.AvgTotalMs != 400 {
		t.Fatalf("avg total = %v, want 400", fb.AvgTotalMs)
	}

	// Series honors revision filtering.
	oldRev := mkResult(p, statusOK, newestBucketEnd.Add(-10*time.Minute).UnixMilli())
	oldRev.Revision = 2
	appendResult(t, s, oldRev)
	rev1 := s.Series(p.ID, 1, time.Hour, SourceScheduled)
	if got := rev1[len(rev1)-2].Samples + rev1[len(rev1)-1].Samples; got != 3 {
		t.Fatalf("revision-1 series samples = %d, want 3", got)
	}
	revAll := s.Series(p.ID, RevisionAll, time.Hour, SourceScheduled)
	total := 0
	for _, b := range revAll {
		total += b.Samples
	}
	if total != 4 {
		t.Fatalf("all-revision series samples = %d, want 4", total)
	}
}

func TestDeleteProviderRemovesHistoryFile(t *testing.T) {
	s, dir := newTestStore(t)
	p := mustAddProvider(t, s, "del")
	appendResult(t, s, mkResult(p, statusOK, time.Now().UnixMilli()))
	path := filepath.Join(dir, "results", "1.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("history file missing before delete: %v", err)
	}
	if err := s.DeleteProvider(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("history file still exists after delete (err=%v)", err)
	}
	if got := s.LatestValidScheduled(p.ID); got != nil {
		t.Fatalf("latest after delete = %+v, want nil", got)
	}
	// Appending to a deleted provider is an explicit error, not a silent
	// resurrection of the history file.
	if _, err := s.AppendResult(mkResult(p, statusOK, time.Now().UnixMilli())); err == nil {
		t.Fatal("append after delete unexpectedly succeeded")
	}
}

func TestLatestValidScheduled(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "latest")
	nowMs := time.Now().UnixMilli()
	if got := s.LatestValidScheduled(p.ID); got != nil {
		t.Fatalf("latest = %+v, want nil", got)
	}
	appendResult(t, s, mkResult(p, statusOK, nowMs-30_000))
	manual := mkResult(p, statusOK, nowMs-20_000)
	manual.Source = SourceManual
	appendResult(t, s, manual)
	cancelled := mkResult(p, statusCancelled, nowMs-10_000)
	appendResult(t, s, cancelled)

	got := s.LatestValidScheduled(p.ID)
	if got == nil || got.Status != statusOK || got.StartedAt != nowMs-30_000 {
		t.Fatalf("latest = %+v, want the scheduled ok", got)
	}
	// Manual and cancelled results never refresh it.
	appendResult(t, s, mkResult(p, statusTimeoutTTFT, nowMs-5_000))
	if got := s.LatestValidScheduled(p.ID); got.Status != statusTimeoutTTFT {
		t.Fatalf("latest = %+v, want timeout_ttft", got)
	}
}

func TestConcurrentAppendAndQuery(t *testing.T) {
	s, _ := newTestStore(t)
	p1 := mustAddProvider(t, s, "c1")
	p2 := mustAddProvider(t, s, "c2")
	nowMs := time.Now().UnixMilli()

	var wg sync.WaitGroup
	for _, p := range []Provider{p1, p2} {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_, _ = s.AppendResult(mkResult(p, statusOK, nowMs-int64(i)*1000))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_ = s.Stats(p1.ID, RevisionAll, 24*time.Hour, SourceScheduled)
			_ = s.QueryResults(p2.ID, RevisionAll, SourceAll, 24*time.Hour, 10, nil, nil)
		}
	}()
	wg.Wait()

	if n := s.ResultCount(p1.ID); n != 50 {
		t.Fatalf("provider1 count = %d, want 50", n)
	}
	if n := s.ResultCount(p2.ID); n != 50 {
		t.Fatalf("provider2 count = %d, want 50", n)
	}
}

func TestConfigCorruptFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{bad"), filePerm); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil {
		t.Fatal("corrupt config.json must be reported as an error")
	}
}

func ptrInt64(v int64) *int64 { return &v }
func ptrInt(v int) *int       { return &v }

func assertPct(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s pct = nil, want %v", name, want)
	}
	if d := *got - want; d < -0.01 || d > 0.01 {
		t.Fatalf("%s pct = %v, want %v (±0.01)", name, *got, want)
	}
}

func lenIdx(pages [][]Result) []int {
	out := make([]int, len(pages))
	for i, p := range pages {
		out[i] = len(p)
	}
	return out
}

func containsLine(s, sub string) bool {
	for _, line := range splitLines(s) {
		if line == sub {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start && s[i-1] == '\r' {
				out = append(out, s[start:i-1])
			} else {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Throughput metrics (task 10-09): decode/prefill derivation with guards,
// independent denominators, series bucket fields, and null serialization.
func okWithUsage(started int64, ttft, total int64, prompt, completion *int) Result {
	return Result{
		ProviderID: 1, Revision: 1, BaseURL: "http://x/v1", Model: "m",
		Source: SourceScheduled, StartedAt: started, FinishedAt: started + total,
		Success: true, TTFTMs: &ttft, TotalMs: total, Status: statusOK,
		PromptTokens: prompt, CompletionTokens: completion,
	}
}

func TestAvgThroughputCalculation(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.AddProvider(Provider{Name: "p", BaseURL: "http://x/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	p64, c32 := 64, 32
	// A4 fixture: ttft=200ms total=1200ms prompt=64 completion=32
	//   decode = (32-1)/(1.0s) = 31.0 ; prefill = 64/0.2 = 320.0
	if _, err := s.AppendResult(okWithUsage(now-1000, 200, 1200, &p64, &c32)); err != nil {
		t.Fatal(err)
	}
	// completion=1: a 1-token response decodes nothing, decode excluded
	// (completion<2 guard), prefill still counts.
	c1 := 1
	if _, err := s.AppendResult(okWithUsage(now-500, 300, 1200, &p64, &c1)); err != nil {
		t.Fatal(err)
	}
	// ttft==total: zero-length decode span excludes decode (span<=0 guard);
	// prefill is independent and still counts.
	if _, err := s.AppendResult(okWithUsage(now-400, 800, 800, &p64, &c32)); err != nil {
		t.Fatal(err)
	}
	// No usage at all: contributes to neither denominator.
	if _, err := s.AppendResult(okWithUsage(now-250, 100, 900, nil, nil)); err != nil {
		t.Fatal(err)
	}

	dec, pre := s.AvgThroughput(1, 1, time.Hour, SourceScheduled)
	if dec == nil || math.Abs(*dec-31.0) > 1.5 {
		t.Fatalf("avg decode = %v, want ~31.0 (only sample 1 qualifies)", dec)
	}
	if pre == nil {
		t.Fatal("avg prefill = nil, want a value")
	}
	// prefill = (320 + 213.33 + 80)/3 ≈ 204.4
	if math.Abs(*pre-204.44) > 2 {
		t.Fatalf("avg prefill = %v, want ~204.4", *pre)
	}
}

func TestAvgThroughputEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.AddProvider(Provider{Name: "p", BaseURL: "http://x/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	dec, pre := s.AvgThroughput(1, 1, time.Hour, SourceScheduled)
	if dec != nil || pre != nil {
		t.Fatalf("want nils without usage evidence, got %v %v", dec, pre)
	}
}

func TestResultTokensSerializeNull(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.AddProvider(Provider{Name: "p", BaseURL: "http://x/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := s.AppendResult(okWithUsage(now, 100, 500, nil, nil)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "results", "1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"prompt_tokens":null`)) {
		t.Fatalf("prompt_tokens must serialize as null, line: %s", data)
	}
}

func TestSeriesBucketThroughput(t *testing.T) {
	s, _ := newTestStore(t)
	if _, err := s.AddProvider(Provider{Name: "p", BaseURL: "http://x/v1", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	p64, c32 := 64, 32
	if _, err := s.AppendResult(okWithUsage(now-1000, 200, 1200, &p64, &c32)); err != nil {
		t.Fatal(err)
	}
	buckets := s.Series(1, 1, time.Hour, SourceScheduled)
	var found bool
	for _, b := range buckets {
		if b.Samples > 0 {
			found = true
			if b.AvgDecodeTPS == nil || math.Abs(*b.AvgDecodeTPS-31.0) > 1.5 {
				t.Fatalf("bucket decode = %v, want ~31.0", b.AvgDecodeTPS)
			}
			if b.AvgPrefillTPS == nil {
				t.Fatal("bucket prefill = nil, want a value")
			}
		}
	}
	if !found {
		t.Fatal("no bucket with samples found")
	}
}

// A6: data written before task 10-09 (config.json and JSONL without the
// include_usage / token fields) loads with the new fields defaulting to
// false/nil, and legacy ok samples simply stay out of the throughput
// denominators.
func TestLegacyDataWithoutUsageFieldsLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "results"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacyConfig := `{
  "version": 1,
  "next_id": 2,
  "max_results_per_provider": 20000,
  "providers": [
    {"id": 1, "name": "old", "base_url": "http://x/v1", "api_key": "", "model": "m",
     "revision": 1, "prompt": "ping", "max_tokens": 128, "timeout_sec": 60,
     "ttft_timeout_ms": 10000, "ttft_slow_ms": 2000, "interval_sec": 300,
     "enabled": true, "created_at": 1700000000000}
  ]
}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	legacyLine, err := json.Marshal(map[string]any{
		"seq": 1, "provider_id": 1, "revision": 1, "base_url": "http://x/v1",
		"model": "m", "source": "scheduled", "started_at": now - 1000,
		"finished_at": now - 1000 + 1200, "success": true,
		"ttft_ms": 200, "total_ms": 1200, "status": "ok", "http_status": 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "results", "1.jsonl"),
		append(legacyLine, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := New(dir)
	if err != nil {
		t.Fatalf("load legacy data: %v", err)
	}
	p, ok := s.GetProvider(1)
	if !ok {
		t.Fatal("legacy provider missing")
	}
	if p.IncludeUsage {
		t.Fatal("include_usage must default to false for legacy config")
	}
	rs := s.QueryResults(1, RevisionAll, SourceAll, 24*time.Hour, 10, nil, nil)
	if len(rs) != 1 {
		t.Fatalf("legacy records = %d, want 1", len(rs))
	}
	if rs[0].PromptTokens != nil || rs[0].CompletionTokens != nil {
		t.Fatalf("legacy tokens = %v/%v, want nil/nil", rs[0].PromptTokens, rs[0].CompletionTokens)
	}
	// Availability stats unaffected; throughput averages stay nil (the legacy
	// ok sample carries no usage evidence, so it enters neither denominator).
	if st := s.Stats(1, 1, time.Hour, SourceScheduled); st.Samples != 1 || st.OK != 1 {
		t.Fatalf("legacy stats = %+v, want 1 ok sample", st)
	}
	dec, pre := s.AvgThroughput(1, 1, time.Hour, SourceScheduled)
	if dec != nil || pre != nil {
		t.Fatalf("legacy throughput = %v/%v, want nil/nil", dec, pre)
	}
}
