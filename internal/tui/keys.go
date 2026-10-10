package tui

// Keymap (design.md §7). 阶段 4a binds the read-side keys; the action keys
// arrive with 阶段 4b and stay listed here as the single reference.
//
//	Global
//	  ↑/↓, j/k   move within the focused pane (tview built-in)
//	  Tab        cycle focus: provider list ↔ detail area
//	  q / Ctrl+C quit (graceful engine shutdown)
//	  w          拉起/关闭按需 Web 面板          (阶段 4b)
//	Provider list
//	  ↑/↓, j/k   select provider
//	Detail area
//	  1/2/3      时间窗 1h / 24h / 7d
//	  R          版本: 当前 / 全部 (cycle)
//	  S          来源: 定时 / 手动 / 全部 (cycle)
//	  PgDn/PgUp  results table next / previous page
//	  p          手动探测当前对象               (阶段 4b)
//	  e / n / d  编辑 / 新建 / 删除对象         (阶段 4b)
//	  r          导出 HTML 报告                 (阶段 4b)
const (
	keyQuit      = 'q'
	keyWindow1h  = '1'
	keyWindow24h = '2'
	keyWindow7d  = '3'
	keyRevision  = 'R'
	keySource    = 'S'
	keyProbe     = 'p' // 阶段 4b
	keyEdit      = 'e' // 阶段 4b
	keyNew       = 'n' // 阶段 4b
	keyDelete    = 'd' // 阶段 4b
	keyExport    = 'r' // 阶段 4b
	keyWeb       = 'w' // 阶段 4b
	keySettings  = ',' // 10-10-settings-pack
)
