package view

import (
	"os"

	"llm-monitor/internal/store"
)

// ValidateSettings is the single owner of the tool-level settings rules,
// shared by the server's PUT /api/settings handler and the TUI settings
// form — same rules, same words, one owner, mirroring ProviderForm.Validate.
// Returns the first violated rule's Chinese message, or "" when the section
// is sound. The report-dir writability check runs here too: the export
// mkdirs on demand, so validation must check the same way.
func ValidateSettings(s store.Settings) string {
	d := s.Defaults
	switch {
	case s.StreakAlert < 2:
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
	case s.ReportDir != "" && !DirWritable(s.ReportDir):
		return "报告目录不可写或无法创建"
	}
	return ""
}

// DirWritable reports whether dir exists as a directory or can be created.
func DirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}
