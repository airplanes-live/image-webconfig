// Package server wires the airplanes-webconfig HTTP routes and middleware.
package server

import (
	"io/fs"
	"net/http"
	"sync"

	"github.com/airplanes-live/image/webconfig/internal/auth"
	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
	"github.com/airplanes-live/image/webconfig/internal/feedenv"
	"github.com/airplanes-live/image/webconfig/internal/identity"
	"github.com/airplanes-live/image/webconfig/internal/logs"
	"github.com/airplanes-live/image/webconfig/internal/schemacache"
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
	runner       wexec.CommandRunner
	stdinRunner  wexec.CommandRunnerStdin
	schema       *schemacache.Cache
	priv         PrivilegedArgv
	configMu     sync.Mutex // serializes POST /api/config transactions
}

// PrivilegedArgv carries the exact sudoers-allowed argv shapes for every
// command webconfig elevates. Each slice is invoked verbatim — no
// concatenation, no shell.
//
// ApplyFeed and SchemaFeed both target the apl-feed binary installed by
// the feed scripts (canonical writer + schema endpoint). The feed CLI
// owns feed.env validation, restart fan-out, and the on-disk schema —
// webconfig is a thin HTTP shell around its JSON interface.
type PrivilegedArgv struct {
	ApplyFeed   []string // sudo -n /usr/local/bin/apl-feed apply --json --lock-timeout 5
	SchemaFeed  []string // /usr/local/bin/apl-feed schema --json (no sudo: read-only)
	Reboot      []string
	StartUpdate []string // sudo systemd-run --unit=airplanes-update ...
}

// DefaultPrivilegedArgv returns the production argv shapes for the
// airplanes-webconfig sudoers entry. Override per-test via Deps.
//
// ApplyFeed pins --lock-timeout 5 so webconfig owns the wall-clock
// budget end-to-end: apl-feed waits at most 5s for the feed.env flock,
// then either succeeds or emits a structured lock_timeout envelope. The
// applyConfigTimeout below (lockTimeout + post-lock budget) sets the
// outer ceiling.
func DefaultPrivilegedArgv() PrivilegedArgv {
	sudo := func(args ...string) []string {
		return append([]string{"/usr/bin/sudo", "-n"}, args...)
	}
	return PrivilegedArgv{
		ApplyFeed:  sudo("/usr/local/bin/apl-feed", "apply", "--json", "--lock-timeout", "5"),
		SchemaFeed: []string{"/usr/local/bin/apl-feed", "schema", "--json"},
		Reboot:     sudo("/usr/bin/systemctl", "reboot"),
		StartUpdate: sudo(
			"/usr/bin/systemd-run",
			"--unit=airplanes-update",
			"--collect",
			"--property=ExecStopPost=/usr/bin/systemctl kill -s HUP airplanes-webconfig.service",
			"/usr/local/share/airplanes/update.sh",
		),
	}
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
	Schema       *schemacache.Cache       // schema cache; required (use schemacache.New)
	Runner       wexec.CommandRunner      // override for tests; nil → exec.RealRunner
	StdinRunner  wexec.CommandRunnerStdin // ditto; piped variant for apl-feed apply
	Privileged   PrivilegedArgv
}

// New returns the top-level HTTP handler.
func New(d Deps) http.Handler {
	runner := d.Runner
	if runner == nil {
		runner = wexec.RealRunner
	}
	stdinRunner := d.StdinRunner
	if stdinRunner == nil {
		stdinRunner = wexec.RealRunnerStdin
	}
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
		runner:       runner,
		stdinRunner:  stdinRunner,
		schema:       d.Schema,
		priv:         d.Privileged,
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
	mux.HandleFunc("POST /api/config", s.requireSession(s.handleConfigPost))
	mux.HandleFunc("POST /api/update", s.requireSession(s.handleUpdate))
	mux.HandleFunc("POST /api/reboot", s.requireSession(s.handleReboot))

	// Static assets at /static/*; the SPA shell is served by the GET /
	// handler below. no-store cache policy: assets are embedded in the
	// binary and a binary update is the only way they change, so a stale
	// cached copy after rollout would mask the new UI.
	staticFS, err := fs.Sub(webassets.FS, "assets")
	if err != nil {
		panic(err) // compile-time guarantee — embed.FS always has this dir
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", noStore(http.FileServer(http.FS(staticFS)))))

	// SPA shell: serve index.html on the root and reject anything else that
	// fell through to /-prefix matching.
	mux.HandleFunc("GET /{$}", s.handleIndex)

	// Compose middleware: security headers on every response; Origin/Host
	// check on every mutating method.
	return securityHeaders(requireOriginMatchesHost(mux))
}

func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
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
