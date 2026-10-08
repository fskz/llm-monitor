package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// loadProviderResults reads one provider's JSONL history into memory.
//
// Corrupt lines are skipped and counted, never repaired or dropped from
// disk (REQUIREMENTS.md §5.5). Records beyond the retention cap are kept
// in memory as loaded; the first append after startup triggers the
// retention rewrite (design.md §4.3).
func loadProviderResults(path string) *providerResults {
	pr := &providerResults{path: path, nextSeq: 1}
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			// Keep the index empty but remember the failure as corrupt
			// metadata; the file itself is left untouched.
			pr.corrupt = -1
		}
		return pr
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Result
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			pr.corrupt++
			continue
		}
		pr.appendMem(r)
	}
	if err := sc.Err(); err != nil {
		// A read failure mid-file: keep what loaded so far.
		pr.corrupt++
	}
	return pr
}

// appendMem updates the in-memory index with a record whose Seq is already
// assigned. Callers hold pr.mu.
func (pr *providerResults) appendMem(r Result) {
	pr.records = append(pr.records, r)
	if r.Seq >= pr.nextSeq {
		pr.nextSeq = r.Seq + 1
	}
	if r.Source == SourceScheduled && r.Status != statusCancelled {
		cp := r
		pr.latestValidScheduled = &cp
	}
}

// AppendResult persists one finished probe outcome and returns the stored
// record with its assigned Seq. Seq is assigned here (monotonic per provider,
// never reset by trimming); the JSONL line is appended with O_APPEND and the
// in-memory index is updated under the same lock, so the file and memory
// never diverge. When the retention cap is exceeded the file is rewritten
// atomically (temp+rename) keeping the newest maxResultsPerProvider records —
// trimming never loses the record being appended. Errors are returned, never
// swallowed: the caller must surface storage failures explicitly
// (REQUIREMENTS.md §5.5).
func (s *Store) AppendResult(r Result) (Result, error) {
	pr := s.resultsFor(r.ProviderID)
	if pr == nil {
		return Result{}, fmt.Errorf("store: unknown provider %d", r.ProviderID)
	}

	s.mu.Lock()
	maxKeep := s.maxKeep
	s.mu.Unlock()

	pr.mu.Lock()
	defer pr.mu.Unlock()

	if pr.deleted {
		return Result{}, fmt.Errorf("store: provider %d deleted", r.ProviderID)
	}
	r.Seq = pr.nextSeq
	pr.nextSeq++

	line, err := json.Marshal(r)
	if err != nil {
		pr.nextSeq--
		return Result{}, fmt.Errorf("store: encode result: %w", err)
	}
	if err := appendLine(pr.path, line); err != nil {
		pr.nextSeq--
		return Result{}, err
	}
	pr.appendMem(r)

	// Retention: rewrite the file when over cap, in the same critical
	// section as the append so concurrent appends cannot be lost.
	if len(pr.records) > maxKeep {
		if err := pr.rewriteKeeping(maxKeep); err != nil {
			return r, fmt.Errorf("store: trim %s: %w", pr.path, err)
		}
	}
	return r, nil
}

// appendLine writes one JSONL line with O_APPEND.
func appendLine(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("store: open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("store: append %s: %w", path, err)
	}
	return nil
}

// rewriteKeeping atomically replaces the history file with its newest keep
// records. Callers hold pr.mu; nextSeq is preserved so Seq stays monotonic.
func (pr *providerResults) rewriteKeeping(keep int) error {
	kept := pr.records[len(pr.records)-keep:]
	tmp := pr.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("store: open %s: %w", tmp, err)
	}
	w := bufio.NewWriter(f)
	for _, r := range kept {
		line, err := json.Marshal(r)
		if err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("store: encode result: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("store: write %s: %w", tmp, err)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("store: flush %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("store: close %s: %w", tmp, err)
	}
	// os.Rename replaces the destination on Windows too (MoveFileEx
	// REPLACE_EXISTING), satisfying the cross-platform trim requirement.
	if err := os.Rename(tmp, pr.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("store: replace %s: %w", pr.path, err)
	}
	pr.records = append(pr.records[:0:0], kept...)
	return nil
}

// LatestValidScheduled returns the newest scheduled, non-cancelled result
// for the provider (by completion order), or nil when there is none. The
// record may belong to an older revision; comparing it against the
// provider's current revision is the caller's job (REQUIREMENTS.md §7.2:
// state freshness is bound to the current revision).
func (s *Store) LatestValidScheduled(providerID int) *Result {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return nil
	}
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	if pr.latestValidScheduled == nil {
		return nil
	}
	cp := *pr.latestValidScheduled
	return &cp
}

// ResultCount returns the number of results currently held for a provider
// (in memory == on disk after every append).
func (s *Store) ResultCount(providerID int) int {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return 0
	}
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return len(pr.records)
}

// CorruptLines returns how many unparsable lines were skipped while
// loading a provider's history file. -1 means the file could not be opened
// at all.
func (s *Store) CorruptLines(providerID int) int {
	pr := s.resultsFor(providerID)
	if pr == nil {
		return 0
	}
	pr.mu.RLock()
	defer pr.mu.RUnlock()
	return pr.corrupt
}

// GetCorruptCounts reports per-provider corrupt-line counts detected at
// startup, so the server can surface storage health (REQUIREMENTS.md §5.5)
// without exposing file internals.
func (s *Store) GetCorruptCounts() map[int]int {
	s.mu.Lock()
	ids := make([]int, 0, len(s.results))
	for id := range s.results {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	out := make(map[int]int, len(ids))
	for _, id := range ids {
		if c := s.CorruptLines(id); c != 0 {
			out[id] = c
		}
	}
	return out
}
