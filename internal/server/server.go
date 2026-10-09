// Package server serves the local REST API and the embedded web panel
// (docs/REQUIREMENTS.md §10). It listens on 127.0.0.1 only; cross-origin
// write requests are rejected and no CORS headers are ever set (§5.4).
package server

import (
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"

	"llm-monitor/internal/store"
)

// EngineAPI is the narrow slice of the scheduling engine the server needs.
// It is an interface (instead of a concrete *engine.Engine) so this package
// compiles independently of internal/engine; main adapts the engine to it.
type EngineAPI interface {
	// Probing reports whether a probe is currently in flight for the
	// provider ("正在探测" is a hint only, it never overrides the
	// monitor status, REQUIREMENTS.md §7.2).
	Probing(id int) bool
	// StorageError returns a non-empty description of the latest
	// engine-side storage failure, or "" when healthy. The panel surfaces
	// storage failures explicitly instead of blaming the probed API (§5.5).
	StorageError() string
	// ProbeNow runs one manual probe to completion and returns its
	// persisted result (REQUIREMENTS.md §4.6).
	ProbeNow(id int) (*store.Result, error)
	// IsInFlight reports whether err means "a probe is already running"
	// (mapped to HTTP 409 by the server).
	IsInFlight(err error) bool
}

// ProviderMutator forwards configuration changes to the engine so it can
// cancel in-flight probes and reschedule. New accepts nil when no engine is
// wired (engine under parallel development); cancellation then simply does
// not happen, which is safe for tests.
type ProviderMutator interface {
	Add(p store.Provider)
	Update(p store.Provider)
	Remove(id int)
}

// Server wires the store, the engine and the embedded panel assets into one
// http.Handler.
type Server struct {
	st         *store.Store
	eng        EngineAPI
	mut        ProviderMutator
	webFS      fs.FS
	port       int
	reportTmpl *template.Template
}

// New builds the server. eng and mut may be nil (no engine attached);
// webFS is the embedded panel filesystem (web.FS()).
func New(st *store.Store, eng EngineAPI, mut ProviderMutator, webFS fs.FS, port int) *Server {
	tmpl := template.Must(template.New("report").Funcs(reportTmplFuncs).Parse(reportTmplSrc))
	return &Server{st: st, eng: eng, mut: mut, webFS: webFS, port: port, reportTmpl: tmpl}
}

// Handler returns the complete routing tree, ready for http.Serve.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/providers", s.handleListProviders)
	mux.HandleFunc("POST /api/providers", s.sameOrigin(s.handleAddProvider))
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
