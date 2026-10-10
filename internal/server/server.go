// Package server serves the local REST API and the embedded web panel
// (docs/REQUIREMENTS.md §10). It listens on 127.0.0.1 only; cross-origin
// write requests are rejected and no CORS headers are ever set (§5.4).
package server

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"

	"llm-monitor/internal/store"
	"llm-monitor/internal/view"
)

// EngineAPI / ProviderMutator live in internal/view (task 10-09 阶段 3):
// both front ends (panel server, TUI) share the same narrow engine slice,
// adapted from the concrete engine in cmd. The aliases below keep the
// server's signatures stable.
type (
	EngineAPI       = view.EngineAPI
	ProviderMutator = view.ProviderMutator
)

// Server wires the store, the engine and the embedded panel assets into one
// http.Handler.
type Server struct {
	st    *store.Store
	eng   EngineAPI
	mut   ProviderMutator
	webFS fs.FS
	port  int
}

// New builds the server. eng and mut may be nil (no engine attached);
// webFS is the embedded panel filesystem (web.FS()).
func New(st *store.Store, eng EngineAPI, mut ProviderMutator, webFS fs.FS, port int) *Server {
	return &Server{st: st, eng: eng, mut: mut, webFS: webFS, port: port}
}

// Handler returns the complete routing tree, ready for http.Serve.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/providers", s.handleListProviders)
	mux.HandleFunc("POST /api/providers", s.sameOrigin(s.handleAddProvider))
	mux.HandleFunc("POST /api/providers/{id}/clone", s.sameOrigin(s.handleCloneProvider))
	mux.HandleFunc("PUT /api/providers/{id}", s.sameOrigin(s.handleUpdateProvider))
	mux.HandleFunc("DELETE /api/providers/{id}", s.sameOrigin(s.handleDeleteProvider))
	mux.HandleFunc("POST /api/providers/{id}/probe", s.sameOrigin(s.handleProbe))
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/series", s.handleSeries)
	mux.HandleFunc("GET /api/results", s.handleResults)
	mux.HandleFunc("GET /api/report", s.handleReport)
	mux.Handle("/", http.FileServerFS(s.webFS))
	return mux
}

// writeJSON emits a JSON response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits the uniform API error body {"error": "..."}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// sameOrigin guards mutating endpoints (REQUIREMENTS.md §5.4): browsers
// always send Origin on cross-site requests, so an Origin header whose host
// is not the local listener is rejected with 403. Requests without Origin
// (curl, same-origin GETs) pass. No CORS headers are ever set.
func (s *Server) sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(origin) {
			writeError(w, http.StatusForbidden, "拒绝跨域请求")
			return
		}
		next(w, r)
	}
}

// originAllowed checks Origin against the local listener addresses
// 127.0.0.1 / localhost / [::1] on the configured port.
func (s *Server) originAllowed(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Port() != strconv.Itoa(s.port) {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}
