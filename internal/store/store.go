// Package store persists provider configurations and probe results for the
// LLM interface availability monitor (docs/REQUIREMENTS.md §5.3, §9) and
// computes the statistics served by the local web panel (§3.4).
//
// Data directory layout:
//
//	config.json          provider configuration; written atomically
//	                     (temp+rename) with 0600 permissions
//	results/<id>.jsonl   one JSON result per line per provider; append-only
//
// All results are loaded into per-provider in-memory indexes at startup.
// Stats, series and detail queries run against the in-memory index, so a
// panel refresh never rescans the JSONL files. Corrupt JSONL lines are
// skipped and counted, never silently dropped from disk; a corrupt
// config.json is reported to the caller instead of being rebuilt.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Result sources (REQUIREMENTS.md §9.2).
const (
	SourceScheduled = "scheduled"
	SourceManual    = "manual"
	// SourceAll selects both sources in QueryResults.
	SourceAll = "all"
)

// Final result statuses referenced by statistics (REQUIREMENTS.md §3.3).
// They mirror the canonical constants of internal/probe and are duplicated
// here so the store keeps zero dependencies on the other packages.
const (
	statusOK           = "ok"
	statusTimeoutTTFT  = "timeout_ttft"
	statusTimeoutTotal = "timeout_total"
	statusHTTPError    = "http_error"
	statusCancelled    = "cancelled"
)

const (
	// defaultMaxResultsPerProvider is the retention cap applied when the
	// config file does not override it (REQUIREMENTS.md §5.3).
	defaultMaxResultsPerProvider = 20000
	filePerm                     = 0o600
	dirPerm                      = 0o700
)

// Provider is one monitored target: an OpenAI-compatible base URL plus a
// model (REQUIREMENTS.md §9.1). APIKey is stored locally in plain text
// (decision record #6); masking on read is the server's responsibility.
type Provider struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	BaseURL       string `json:"base_url"`
	APIKey        string `json:"api_key"`
	Model         string `json:"model"`
	Revision      int    `json:"revision"`
	Prompt        string `json:"prompt"`
	MaxTokens     int    `json:"max_tokens"`
	TimeoutSec    int    `json:"timeout_sec"`
	TTFTTimeoutMs int    `json:"ttft_timeout_ms"`
	TTFTSlowMs    int    `json:"ttft_slow_ms"`
	IntervalSec   int    `json:"interval_sec"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     int64  `json:"created_at"`
}

// Result is one persisted probe outcome (REQUIREMENTS.md §9.2). Seq is
// assigned by AppendResult, monotonically increasing per provider, and
// forms the detail-pagination cursor together with StartedAt. TTFTMs and
// HTTPStatus are nil when unknown — never 0 or -1.
type Result struct {
	Seq           int64  `json:"seq"`
	ProviderID    int    `json:"provider_id"`
	Revision      int    `json:"revision"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	Source        string `json:"source"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at"`
	Success       bool   `json:"success"`
	TTFTMs        *int64 `json:"ttft_ms"`
	TotalMs       int64  `json:"total_ms"`
	Status        string `json:"status"`
	HTTPStatus    *int   `json:"http_status"`
	Error         string `json:"error,omitempty"`
	OutputPreview string `json:"output_preview,omitempty"`
}

// configFile is the on-disk shape of config.json.
type configFile struct {
	Version               int        `json:"version"`
	NextID                int        `json:"next_id"`
	MaxResultsPerProvider int        `json:"max_results_per_provider"`
	Providers             []Provider `json:"providers"`
}

// providerResults is the per-provider in-memory index plus the JSONL file
// it mirrors. All fields are guarded by mu; file appends and retention
// rewrites happen under the write lock so they never interleave, while
// queries only take the read lock and scan the in-memory slice.
type providerResults struct {
	mu   sync.RWMutex
	path string

	// records is in completion order (== append order); elements are never
	// mutated in place, trimming only drops the oldest prefix.
	records []Result
	nextSeq int64
	// latestValidScheduled is the newest record with source=scheduled and
	// status!=cancelled, regardless of revision; callers compare its
	// revision against the provider's current one (REQUIREMENTS.md §7.2).
	latestValidScheduled *Result
	// corrupt counts unparsable lines skipped while loading.
	corrupt int
	// deleted marks an index detached by DeleteProvider; later appends
	// through stale references are refused instead of resurrecting the
	// history file.
	deleted bool
}

// Store is the persistence and statistics layer. Configuration state is
// guarded by mu; per-provider result state by each providerResults.mu.
// The two locks are never held at the same time: DeleteProvider releases
// mu before taking the per-provider lock, and AppendResult reads maxKeep
// under mu, releases it, then locks pr.mu.
type Store struct {
	dir        string
	resultsDir string

	mu      sync.Mutex // guards config, maxKeep and the results map
	config  configFile
	maxKeep int

	results map[int]*providerResults
}

// New opens the store rooted at dir, creating the directory if needed, and
// loads config.json plus every provider history file. A corrupt config.json
// is returned as an error so the caller decides how to surface it; corrupt
// JSONL lines are skipped and counted. New never rewrites existing files.
func New(dir string) (*Store, error) {
	s := &Store{
		dir:        dir,
		resultsDir: filepath.Join(dir, "results"),
		results:    make(map[int]*providerResults),
	}
	if err := os.MkdirAll(s.resultsDir, dirPerm); err != nil {
		return nil, fmt.Errorf("store: create %s: %w", s.resultsDir, err)
	}
	if err := s.loadConfig(); err != nil {
		return nil, err
	}
	for _, p := range s.config.Providers {
		s.results[p.ID] = loadProviderResults(s.resultsPath(p.ID))
	}
	return s, nil
}

func (s *Store) configPath() string {
	return filepath.Join(s.dir, "config.json")
}

func (s *Store) resultsPath(id int) string {
	return filepath.Join(s.resultsDir, fmt.Sprintf("%d.jsonl", id))
}

// resultsFor returns the per-provider index, or nil for unknown providers.
func (s *Store) resultsFor(providerID int) *providerResults {
	s.mu.Lock()
	pr := s.results[providerID]
	s.mu.Unlock()
	return pr
}

// loadConfig reads config.json, applying defaults for missing counters.
func (s *Store) loadConfig() error {
	data, err := os.ReadFile(s.configPath())
	if err != nil {
		if os.IsNotExist(err) {
			s.config = configFile{
				Version:               1,
				NextID:                1,
				MaxResultsPerProvider: defaultMaxResultsPerProvider,
				Providers:             []Provider{},
			}
			s.maxKeep = s.config.MaxResultsPerProvider
			return nil
		}
		return fmt.Errorf("store: read %s: %w", s.configPath(), err)
	}
	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("store: parse %s: %w", s.configPath(), err)
	}
	if cfg.Providers == nil {
		cfg.Providers = []Provider{}
	}
	if cfg.MaxResultsPerProvider <= 0 {
		cfg.MaxResultsPerProvider = defaultMaxResultsPerProvider
	}
	if cfg.NextID <= 0 {
		cfg.NextID = 1
		for _, p := range cfg.Providers {
			if p.ID >= cfg.NextID {
				cfg.NextID = p.ID + 1
			}
		}
	}
	s.config = cfg
	s.maxKeep = cfg.MaxResultsPerProvider
	return nil
}

// saveConfigLocked atomically replaces config.json. Callers hold s.mu.
func (s *Store) saveConfigLocked() error {
	data, err := json.MarshalIndent(s.config, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encode config: %w", err)
	}
	tmp := s.configPath() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), filePerm); err != nil {
		return fmt.Errorf("store: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.configPath()); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("store: replace %s: %w", s.configPath(), err)
	}
	return nil
}

func (s *Store) indexOfProviderLocked(id int) int {
	for i := range s.config.Providers {
		if s.config.Providers[i].ID == id {
			return i
		}
	}
	return -1
}

// AddProvider assigns ID, initial revision and creation time, persists the
// configuration and initializes an empty result index. IDs come from the
// persisted next_id counter and are never reused after deletion.
func (s *Store) AddProvider(p Provider) (Provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	p.ID = s.config.NextID
	p.Revision = 1
	if p.CreatedAt == 0 {
		p.CreatedAt = time.Now().UnixMilli()
	}
	// Bump next_id before saving so the persisted counter always exceeds
	// every assigned ID; a crash after the rename cannot recycle IDs.
	s.config.NextID++
	s.config.Providers = append(s.config.Providers, p)
	if err := s.saveConfigLocked(); err != nil {
		s.config.NextID--
		s.config.Providers = s.config.Providers[:len(s.config.Providers)-1]
		return p, err
	}
	s.results[p.ID] = &providerResults{path: s.resultsPath(p.ID), nextSeq: 1}
	return p, nil
}

// UpdateProvider replaces the stored configuration of p.ID. Revision bumps
// for changed base_url/model are decided by the caller (server layer); the
// store persists whatever it is given.
func (s *Store) UpdateProvider(p Provider) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.indexOfProviderLocked(p.ID)
	if idx < 0 {
		return fmt.Errorf("store: unknown provider %d", p.ID)
	}
	old := s.config.Providers[idx]
	s.config.Providers[idx] = p
	if err := s.saveConfigLocked(); err != nil {
		s.config.Providers[idx] = old
		return err
	}
	return nil
}

// DeleteProvider removes the configuration, the in-memory index and the
// provider's history file. Other providers are unaffected.
func (s *Store) DeleteProvider(id int) error {
	s.mu.Lock()
	idx := s.indexOfProviderLocked(id)
	if idx < 0 {
		s.mu.Unlock()
		return fmt.Errorf("store: unknown provider %d", id)
	}
	old := s.config.Providers
	providers := make([]Provider, 0, len(old)-1)
	providers = append(providers, old[:idx]...)
	providers = append(providers, old[idx+1:]...)
	s.config.Providers = providers
	if err := s.saveConfigLocked(); err != nil {
		s.config.Providers = old
		s.mu.Unlock()
		return err
	}
	pr := s.results[id]
	delete(s.results, id)
	s.mu.Unlock()

	if pr != nil {
		// Wait for any in-flight append, then remove the file so a racing
		// append cannot resurrect it: AppendResult re-checks deleted under
		// pr.mu and refuses stale indexes.
		pr.mu.Lock()
		pr.deleted = true
		if err := os.Remove(pr.path); err != nil && !os.IsNotExist(err) {
			pr.mu.Unlock()
			return fmt.Errorf("store: remove %s: %w", pr.path, err)
		}
		pr.mu.Unlock()
	}
	return nil
}

// GetProviders returns a copy of all stored providers.
func (s *Store) GetProviders() []Provider {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Provider, len(s.config.Providers))
	copy(out, s.config.Providers)
	return out
}

// GetProvider returns one provider by ID.
func (s *Store) GetProvider(id int) (Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx := s.indexOfProviderLocked(id); idx >= 0 {
		return s.config.Providers[idx], true
	}
	return Provider{}, false
}
