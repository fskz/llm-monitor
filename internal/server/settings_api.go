package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"llm-monitor/internal/store"
)

// settingsRequest is the PUT /api/settings body: the settings section as
// persisted. Field-by-field validation happens in validateSettings.
type settingsRequest struct {
	Defaults    store.SettingsDefaults `json:"defaults"`
	StreakAlert int                    `json:"streak_alert"`
	ReportDir   string                 `json:"report_dir"`
}

// SettingsView is the GET /api/settings response — the settings plus the
// effective report dir (default resolved) so the form can show it.
type SettingsView struct {
	Defaults    store.SettingsDefaults `json:"defaults"`
	StreakAlert int                    `json:"streak_alert"`
	ReportDir   string                 `json:"report_dir"`
}

// handleGetSettings returns the effective settings (PRD R2).
func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st := s.st.GetSettings()
	writeJSON(w, http.StatusOK, SettingsView{
		Defaults:    st.Defaults,
		StreakAlert: st.StreakAlert,
		ReportDir:   st.ReportDir,
	})
}

// handlePutSettings validates and persists the whole settings section.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var c settingsRequest
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if msg := validateSettings(c); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.st.SaveSettings(store.Settings{
		Defaults:    c.Defaults,
		StreakAlert: c.StreakAlert,
		ReportDir:   strings.TrimSpace(c.ReportDir),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "保存设置失败："+err.Error())
		return
	}
	st := s.st.GetSettings()
	writeJSON(w, http.StatusOK, SettingsView{
		Defaults:    st.Defaults,
		StreakAlert: st.StreakAlert,
		ReportDir:   st.ReportDir,
	})
}

// validateSettings mirrors the provider-form rules for the default subset
// (one owner of the words: keep them aligned with view.ProviderForm's
// messages where the rule is shared).
func validateSettings(c settingsRequest) string {
	d := c.Defaults
	switch {
	case c.StreakAlert < 2:
		return "连败提醒阈值需至少为 2"
	case d.MaxTokens <= 0:
		return "max_tokens 必须大于 0"
	case d.TimeoutSec <= 0:
		return "总超时必须大于 0"
	case d.IntervalSec != 0 && d.IntervalSec < 60:
		return "探测间隔为 0（仅手动）或至少 1 分钟"
	case !(d.TTFTSlowMs > 0 && d.TTFTSlowMs < d.TTFTTimeoutMs):
		return "需要 0 < 慢阈值 < 首内容超时"
	case d.TTFTTimeoutMs > d.TimeoutSec*1000:
		return "首内容超时不能大于总超时"
	case c.ReportDir != "" && !filepathIsDirWritable(c.ReportDir):
		return "报告目录不可写或无法创建"
	}
	return ""
}

// filepathIsDirWritable reports whether dir exists as a directory or can
// be created (the export mkdirs on demand — validate the same way).
func filepathIsDirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
