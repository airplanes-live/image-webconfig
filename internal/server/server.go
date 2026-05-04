// Package server wires the airplanes-webconfig HTTP routes and middleware.
package server

import (
	"io/fs"
	"net/http"

	"github.com/airplanes-live/image/webconfig/internal/auth"
	"github.com/airplanes-live/image/webconfig/internal/feedenv"
	"github.com/airplanes-live/image/webconfig/internal/identity"
	"github.com/airplanes-live/image/webconfig/internal/logs"
	"github.com/airplanes-live/image/webconfig/internal/status"
	webassets "github.com/airplanes-live/image/webconfig/web"
)

// Server holds the runtime auth components. Constructed in cmd/webconfig.
type Server struct {
	version      string
	store        *auth.PasswordStore
	sessions     *auth.Sessions
	lockout      *auth.Lockout
	guard        *auth.HashGuard
	argon2Params auth.Params
	identity     *identity.Reader
	feedEnv      *feedenv.Reader
	status       *status.Reader
	logs         *logs.Streamer
}

// Deps is the injection bundle main passes to NewServer.
type Deps struct {
	Version      string
	Store        *auth.PasswordStore
	Sessions     *auth.Sessions
	Lockout      *auth.Lockout
	Guard        *auth.HashGuard
	Argon2Params auth.Params
	Identity     *identity.Reader
	FeedEnv      *feedenv.Reader
	Status       *status.Reader
	Logs         *logs.Streamer
}

// New returns the top-level HTTP handler.
func New(d Deps) http.Handler {
	s := &Server{
		version:      d.Version,
		store:        d.Store,
		sessions:     d.Sessions,
		lockout:      d.Lockout,
		guard:        d.Guard,
		argon2Params: d.Argon2Params,
		identity:     d.Identity,
		feedEnv:      d.FeedEnv,
		status:       d.Status,
		logs:         d.Logs,
	}

	mux := http.NewServeMux()

	// Public endpoints (no auth required).
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)

	// Authed endpoints.
	mux.HandleFunc("POST /api/auth/logout", s.requireSession(s.handleLogout))
	mux.HandleFunc("POST /api/auth/password", s.requireSession(s.handleChangePassword))
	mux.HandleFunc("GET /api/auth/whoami", s.requireSession(s.handleWhoami))
	mux.HandleFunc("GET /api/identity", s.requireSession(s.handleIdentity))
	mux.HandleFunc("POST /api/identity/secret", s.requireSession(s.handleIdentitySecret))
	mux.HandleFunc("GET /api/config", s.requireSession(s.handleConfigGet))
	mux.HandleFunc("GET /api/status", s.requireSession(s.handleStatus))
	mux.HandleFunc("GET /api/log/{unit}", s.requireSession(s.handleLog))

	// Static assets at /static/*; the SPA shell is served by the GET /
	// handler below.
	staticFS, err := fs.Sub(webassets.FS, "assets")
	if err != nil {
		panic(err) // compile-time guarantee — embed.FS always has this dir
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// SPA shell: serve index.html on the root and reject anything else that
	// fell through to /-prefix matching.
	mux.HandleFunc("GET /{$}", s.handleIndex)

	// Compose middleware: security headers on every response; Origin/Host
	// check on every mutating method.
	return securityHeaders(requireOriginMatchesHost(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok " + s.version + "\n"))
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	staticFS, _ := fs.Sub(webassets.FS, "assets")
	body, err := fs.ReadFile(staticFS, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}
