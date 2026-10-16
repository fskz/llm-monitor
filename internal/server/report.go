package server

import (
	"bytes"
	"fmt"
	"net/http"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// handleReport serves the single-file self-contained HTML report for one
// provider. Rendering itself is owned by view.RenderReport (shared with the
// TUI export, task 10-09 阶段 2); this handler keeps only HTTP semantics —
// parameter parsing, headers, error mapping. Read-only GET, so no
// same-origin guard; the browser downloads it via Content-Disposition.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	p, rev, window, ok := s.queryProvider(w, r)
	if !ok {
		return
	}
	source := r.URL.Query().Get("source")
	if source == "" {
		source = store.SourceAll // reports are archival snapshots: full history rows
	}
	if source != store.SourceAll && source != store.SourceScheduled && source != store.SourceManual {
		writeError(w, http.StatusBadRequest, "source 仅支持 scheduled / manual / all")
		return
	}

	filename := view.ReportFilename(p, window)
	// Render into a buffer first: the report is bounded (≤200 detail rows),
	// and only a successful render gets headers + body. A rendering error
	// must surface as a clean 500 instead of a corrupted mid-stream download
	// (headers cannot be rewritten once written).
	var buf bytes.Buffer
	if err := view.RenderReport(&buf, s.st, s.eng, p, rev, source, window); err != nil {
		writeError(w, http.StatusInternalServerError, "报告渲染失败："+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	_, _ = w.Write(buf.Bytes())
}
