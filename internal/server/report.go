package server

import (
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if err := view.RenderReport(w, s.st, s.eng, p, rev, source, window); err != nil {
		// Headers are already sent; nothing sane to rewrite mid-stream.
		http.Error(w, "report rendering failed: "+err.Error(), http.StatusInternalServerError)
	}
}
