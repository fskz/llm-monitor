package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func sampleCacheRecord(verdict string) CacheCheckRecord {
	ttft := int64(321)
	http := 200
	tokens := 2048
	cached := int64(1536)
	return CacheCheckRecord{
		Verdict:   verdict,
		Evidence:  "cached_tokens hop1=0, hop2=1536",
		Hops:      []CacheCheckHop{{Status: "ok"}, {Status: "ok", HTTPStatus: &http, TTFTMs: &ttft, PromptTokens: &tokens, CachedTokens: &cached}},
		CheckedAt: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC).UnixMilli(),
		Revision:  3,
	}
}

// Save then Load round-trips the record, and the file lands where the
// design says: cachecheck/<id>.json, 0600, next to (not inside) results/.
func TestCacheCheckSaveLoad(t *testing.T) {
	s, dir := newTestStore(t)
	p := mustAddProvider(t, s, "cc")
	rec := sampleCacheRecord("supported")

	if _, ok := s.LoadCacheCheck(p.ID); ok {
		t.Fatal("LoadCacheCheck must report no record before the first save")
	}
	if err := s.SaveCacheCheck(p.ID, rec); err != nil {
		t.Fatalf("SaveCacheCheck: %v", err)
	}

	path := filepath.Join(dir, "cachecheck", "1.json")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("record file missing: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("record file perm = %o, want 0600", perm)
	}

	got, ok := s.LoadCacheCheck(p.ID)
	if !ok {
		t.Fatal("LoadCacheCheck = false after save")
	}
	if got.Verdict != rec.Verdict || got.Evidence != rec.Evidence || got.CheckedAt != rec.CheckedAt || got.Revision != rec.Revision {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if len(got.Hops) != 2 || got.Hops[1].CachedTokens == nil || *got.Hops[1].CachedTokens != 1536 {
		t.Fatalf("hop evidence lost in round-trip: %+v", got.Hops)
	}
	if got.Hops[0].CachedTokens != nil || got.Hops[0].TTFTMs != nil {
		t.Fatalf("nil evidence fields must stay nil: %+v", got.Hops[0])
	}
}

// Each provider keeps exactly one record: a second save overwrites the
// first (no history, prd non-goal "多次留存").
func TestCacheCheckOverwrite(t *testing.T) {
	s, _ := newTestStore(t)
	p := mustAddProvider(t, s, "cc-overwrite")
	if err := s.SaveCacheCheck(p.ID, sampleCacheRecord("unsupported")); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.SaveCacheCheck(p.ID, sampleCacheRecord("unknown")); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, ok := s.LoadCacheCheck(p.ID)
	if !ok || got.Verdict != "unknown" {
		t.Fatalf("after overwrite LoadCacheCheck = %+v ok=%v, want the newest record", got, ok)
	}
}

// A corrupt or unreadable record file reads as "no record" — never an error
// (the record is a convenience artifact, not authoritative state).
func TestCacheCheckCorruptFileTolerated(t *testing.T) {
	s, dir := newTestStore(t)
	p := mustAddProvider(t, s, "cc-corrupt")
	path := filepath.Join(dir, "cachecheck")
	if err := os.MkdirAll(path, dirPerm); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(path, "1.json")

	for name, content := range map[string]string{
		"garbage":       "not json at all",
		"empty object":  "{}",
		"truncated":     `{"verdict":"supported","evidence":"...`,
		"wrong types":   `{"verdict":"supported","checked_at":"soon"}`,
		"empty literal": "",
	} {
		if err := os.WriteFile(full, []byte(content), filePerm); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.LoadCacheCheck(p.ID); ok {
			t.Fatalf("%s: LoadCacheCheck = true, want false for corrupt file", name)
		}
	}
	// A missing directory is equally "no record".
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LoadCacheCheck(p.ID); ok {
		t.Fatal("LoadCacheCheck = true with the directory removed")
	}
}
