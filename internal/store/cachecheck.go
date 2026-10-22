// Cache-check persistence (task 10-22-cache-check, prd R4): the latest
// prefix-cache detection verdict per provider, as one standalone JSON file
// per provider under <data-dir>/cachecheck/<id>.json. The record is fully
// independent of results/<id>.jsonl and of every statistic; it is a
// convenience artifact, not authoritative state — a missing or corrupt file
// simply means "no record".
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// CacheCheckRecord is the persisted outcome of the latest prefix-cache
// detection for one provider. Revision is the provider revision at check
// time so the UI can flag the record stale after a base_url/model change.
type CacheCheckRecord struct {
	Verdict  string          `json:"verdict"`
	Evidence string          `json:"evidence"`
	Hops     []CacheCheckHop `json:"hops"`
	// FailedHop is the 1-based index of the first failed hop, 0 when none.
	FailedHop int   `json:"failed_hop,omitempty"`
	CheckedAt int64 `json:"checked_at"` // UTC Unix milliseconds
	Revision  int   `json:"revision"`
}

// CacheCheckHop is the per-hop evidence persisted with a record; nil fields
// mean "not observed" (same null semantics as Result).
type CacheCheckHop struct {
	Status           string `json:"status"`
	HTTPStatus       *int   `json:"http_status"`
	TTFTMs           *int64 `json:"ttft_ms"`
	TotalMs          int64  `json:"total_ms"`
	PromptTokens     *int   `json:"prompt_tokens"`
	CompletionTokens *int   `json:"completion_tokens"`
	CachedTokens     *int64 `json:"cached_tokens"`
	ErrDetail        string `json:"err_detail,omitempty"`
}

func (s *Store) cacheCheckPath(id int) string {
	return filepath.Join(s.dir, "cachecheck", fmt.Sprintf("%d.json", id))
}

// SaveCacheCheck atomically overwrites the provider's latest cache-check
// record (temp+rename, 0600 — the same write pattern as config.json). The
// cachecheck/ directory is created lazily on first save.
func (s *Store) SaveCacheCheck(providerID int, rec CacheCheckRecord) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	dir := filepath.Join(s.dir, "cachecheck")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("store: create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode cache check: %w", err)
	}
	path := s.cacheCheckPath(providerID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), filePerm); err != nil {
		return fmt.Errorf("store: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("store: replace %s: %w", path, err)
	}
	return nil
}

// LoadCacheCheck returns the provider's latest cache-check record, or
// (zero, false) when there is none yet or the file is corrupt — never an
// error: an unreadable record is indistinguishable from "never checked"
// for every consumer.
func (s *Store) LoadCacheCheck(providerID int) (CacheCheckRecord, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	data, err := os.ReadFile(s.cacheCheckPath(providerID))
	if err != nil {
		return CacheCheckRecord{}, false
	}
	var rec CacheCheckRecord
	if json.Unmarshal(data, &rec) != nil || rec.Verdict == "" {
		return CacheCheckRecord{}, false
	}
	return rec, true
}
