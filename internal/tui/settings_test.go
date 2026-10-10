package tui

import (
	"github.com/rivo/tview"

	"path/filepath"
	"testing"

	"llm-monitor/internal/store"
)

// Settings validation mirrors the server wording and rules (R2).
func TestValidateSettings(t *testing.T) {
	ok := store.Settings{StreakAlert: 3, Defaults: store.SettingsDefaults{MaxTokens: 128, TimeoutSec: 60, TTFTTimeoutMs: 15000, TTFTSlowMs: 3000}}
	if msg := validateSettings(ok); msg != "" {
		t.Fatalf("valid settings rejected: %q", msg)
	}
	if msg := validateSettings(store.Settings{StreakAlert: 1, Defaults: ok.Defaults}); msg != "连败提醒阈值需至少为 2" {
		t.Fatalf("streak 1 = %q", msg)
	}
	badSlow := ok
	badSlow.Defaults.TTFTSlowMs = 20000
	if msg := validateSettings(badSlow); msg == "" {
		t.Fatal("slow ≥ ttft must fail")
	}
	unwritable := ok
	unwritable.ReportDir = "/proc/nope/deep"
	if msg := validateSettings(unwritable); msg == "" {
		t.Fatal("unwritable dir must fail")
	}
}

// Report export honors settings.report_dir; empty falls back to <data>/reports (R5/A6).
func TestExportReportDirSetting(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ids := seedProviders(t, st, 1)
	seedResults(t, st, ids[0], 2)
	p, _ := st.GetProvider(ids[0])

	// Default: <data>/reports.
	u := &ui{deps: Deps{Store: st}}
	path, err := u.exportReport(p)
	if err != nil {
		t.Fatalf("export default: %v", err)
	}
	if filepath.Dir(path) != filepath.Join(st.Dir(), "reports") {
		t.Fatalf("default dir = %s", filepath.Dir(path))
	}

	// Override honored (and created).
	custom := filepath.Join(dir, "custom-reports")
	if err := st.SaveSettings(store.Settings{StreakAlert: 2, ReportDir: custom, Defaults: store.DefaultSettings().Defaults}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	path, err = u.exportReport(p)
	if err != nil {
		t.Fatalf("export custom: %v", err)
	}
	if filepath.Dir(path) != custom {
		t.Fatalf("custom dir = %s, want %s", filepath.Dir(path), custom)
	}

	// Cleared → back to default.
	if err := st.SaveSettings(store.Settings{StreakAlert: 2, Defaults: store.DefaultSettings().Defaults}); err != nil {
		t.Fatalf("SaveSettings clear: %v", err)
	}
	path, _ = u.exportReport(p)
	if filepath.Dir(path) != filepath.Join(st.Dir(), "reports") {
		t.Fatalf("cleared dir = %s", filepath.Dir(path))
	}
}

// New-provider form prefills from settings (R3/A4).
func TestOpenFormPrefillsDefaults(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.New(dir)
	if err := st.SaveSettings(store.Settings{
		Defaults:    store.SettingsDefaults{MaxTokens: 512, IntervalSec: 120, TimeoutSec: 45, TTFTTimeoutMs: 9000, TTFTSlowMs: 2500, Enabled: true, IncludeUsage: true},
		StreakAlert: 4,
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	d := openForm(nil, Deps{Store: st}, 0, nil)
	f, _ := d.collectForm()
	if f.MaxTokens != 512 || f.IntervalSec != 120 || f.TimeoutSec != 45 || f.TTFTTimeoutMs != 9000 || f.TTFTSlowMs != 2500 || !f.IncludeUsage {
		t.Fatalf("prefilled form = %+v", f)
	}
}

// toggleCurrent (x key): flips enabled in the store, notifies the mutator,
// and reports the state change on the status line.
func TestToggleCurrent(t *testing.T) {
	st, _ := store.New(t.TempDir())
	ids := seedProviders(t, st, 1) // enabled: true
	mut := &recordingMutator{}
	app := newTestApp(t)
	u := &ui{app: app, deps: Deps{Store: st, Mutator: mut}}
	u.statusBar = tview.NewTextView().SetDynamicColors(true)
	u.overview = newOverviewPane(st, nil, nil)
	u.overview.selected = ids[0]
	u.detail = newDetailPane(st, nil)
	u.detailView = tview.NewTextView().SetDynamicColors(true)
	u.pages = tview.NewPages()

	u.toggleCurrent()
	p, _ := st.GetProvider(ids[0])
	if p.Enabled {
		t.Fatal("toggle must disable an enabled provider")
	}
	if len(mut.updated) != 1 || mut.updated[0] != ids[0] {
		t.Fatalf("mutator updated = %v, want [%d]", mut.updated, ids[0])
	}
	if !waitForStatus(u, "已停用") {
		t.Fatalf("status = %q, want 已停用", u.statusBar.GetText(true))
	}

	u.toggleCurrent()
	p, _ = st.GetProvider(ids[0])
	if !p.Enabled {
		t.Fatal("second toggle must re-enable")
	}
	if !waitForStatus(u, "已启用") {
		t.Fatalf("status = %q, want 已启用", u.statusBar.GetText(true))
	}
}

type recordingMutator struct {
	added   []int
	updated []int
	removed []int
}

func (m *recordingMutator) Add(p store.Provider)    { m.added = append(m.added, p.ID) }
func (m *recordingMutator) Update(p store.Provider) { m.updated = append(m.updated, p.ID) }
func (m *recordingMutator) Remove(id int)           { m.removed = append(m.removed, id) }
