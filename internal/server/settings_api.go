package server

import (
	"net/http"
	"strings"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
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
	if !decodeJSON(w, r, &c) {
		return
	}
	// Trim BEFORE validating: ValidateSettings→DirWritable runs MkdirAll on
	// the given path, so validating the raw value would create mis-named
	// directories ("dir " with trailing space) while saving the trimmed one.
	// One value flows through validate → save → response.
	c.ReportDir = strings.TrimSpace(c.ReportDir)
	if msg := view.ValidateSettings(store.Settings{
		Defaults:    c.Defaults,
		StreakAlert: c.StreakAlert,
		ReportDir:   c.ReportDir,
	}); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.st.SaveSettings(store.Settings{
		Defaults:    c.Defaults,
		StreakAlert: c.StreakAlert,
		ReportDir:   c.ReportDir,
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
