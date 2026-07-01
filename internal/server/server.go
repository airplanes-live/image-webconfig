// Package server wires the airplanes-webconfig HTTP routes and middleware.
package server

import (
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	"github.com/airplanes-live/image-webconfig/internal/claimstatus"
	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
	"github.com/airplanes-live/image-webconfig/internal/identity"
	"github.com/airplanes-live/image-webconfig/internal/logs"
	"github.com/airplanes-live/image-webconfig/internal/schemacache"
	"github.com/airplanes-live/image-webconfig/internal/sdr"
	"github.com/airplanes-live/image-webconfig/internal/status"
	webassets "github.com/airplanes-live/image-webconfig/web"
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
	claimStatus  *claimstatus.Cache
	logs         *logs.Streamer
	runner       wexec.CommandRunner
	stdinRunner  wexec.CommandRunnerStdin
	schema       *schemacache.Cache
	priv         PrivilegedArgv
	nowFunc      func() time.Time // injected for tests; defaults to time.Now
	// upgradeStatePath is the on-disk file the runtime overlay's update
	// path writes with "clean" / "in-progress" / "failed". Defaults to
	// DefaultUpgradeStatePath; tests override via Deps.UpgradeStatePath.
	upgradeStatePath string
	// orchestratorStatePath is the on-disk JSON state file the update
	// orchestrator atomically writes per step. Defaults to
	// DefaultOrchestratorStatePath; tests override via
	// Deps.OrchestratorStatePath.
	orchestratorStatePath string
	// orchestratorCapableFunc reports whether the orchestrator surfaces
	// are present + executable (trampoline + overlay target). Defaults to
	// the production capability check; tests override via
	// Deps.OrchestratorCapable.
	orchestratorCapableFunc func() bool
	// sdrSysfsRoot is where GET /api/sdr enumerates RTL-SDR devices.
	// Defaults to sdr.DefaultSysfsRoot; tests and the devserver inject a
	// fake sysfs tree.
	sdrSysfsRoot string
	// assetsFS, when non-nil, overrides the embedded web/assets used for
	// GET / and GET /static/*. Default (nil) keeps production behavior:
	// assets are served from the binary's go:embed FS. cmd/devserver sets
	// this to a disk-backed os.DirFS so UI edits don't require a rebuild.
	assetsFS      fs.FS
	configMu      sync.Mutex // serializes POST /api/config transactions
	maintenanceMu sync.Mutex // serializes the is-active guard + transient-unit kickoff
}

// now returns the current time via the injected clock so tests can pin
// the value used for sidecar `edited_at` stamps.
func (s *Server) now() time.Time { return s.nowFunc() }

// PrivilegedArgv carries the binary's argv shapes — every external
// command webconfig invokes. Each slice is invoked verbatim — no
// concatenation, no shell. Only the sudo-prefixed shapes must appear in
// the sudoers file; the parity test covers exactly those. Read-only
// shapes (SchemaFeed, ConfigShowFeed) run unprivileged.
//
// ApplyFeed, SchemaFeed, and ConfigShowFeed all target the apl-feed
// binary installed by the feed scripts (canonical writer + schema +
// config-read endpoints). The feed CLI owns feed.env validation,
// restart fan-out, and the on-disk schema — webconfig is a thin HTTP
// shell around its JSON interface.
//
// Wifi* target the apl-wifi binary installed by stage-airplanes/05. One
// entry per subcommand keeps the sudoers grant pinned to a single verb;
// a compromised webconfig cannot drift between the read-only (list /
// status) and mutating (add / update / delete / test / activate) surfaces
// via the same grant.
type PrivilegedArgv struct {
	ApplyFeed         []string // sudo -n /usr/local/bin/apl-feed apply --json --lock-timeout 5
	SchemaFeed        []string // /usr/local/bin/apl-feed schema --json (no sudo: read-only)
	ConfigShowFeed    []string // /usr/local/bin/apl-feed config show --json (no sudo: read-only)
	Reboot            []string // sudo -n /usr/bin/systemctl reboot
	Poweroff          []string // sudo -n /usr/bin/systemctl poweroff
	StartOrchestrator []string // sudo systemd-run --unit=airplanes-update-orchestrator ...
	RegisterClaim     []string // sudo systemctl start --no-block airplanes-claim.service
	RotateClaim       []string // sudo -n /opt/airplanes/current/lib/airplanes-webconfig/claim-rotate.sh
	SyncConfig        []string // sudo systemctl start --no-block airplanes-config-sync.service
	WifiList          []string
	WifiAdd           []string
	WifiUpdate        []string
	WifiDelete        []string
	WifiTest          []string
	WifiActivate      []string
	WifiAdopt         []string
	WifiStatus        []string
	WifiExport        []string // sudo -n /usr/local/bin/apl-wifi export --json (PSK-bearing; backup only)
	WifiImport        []string // sudo -n /usr/local/bin/apl-wifi import --json (non-disruptive restore)
	ExportIdentity    []string // sudo -n /opt/airplanes/current/lib/airplanes-webconfig/identity-export.sh
	ImportIdentity    []string // sudo -n /opt/airplanes/current/lib/airplanes-webconfig/identity-import.sh
	// Aggregator* target the apl-aggregator helper installed by the runtime
	// overlay. As with apl-wifi, one entry per verb keeps the sudoers grant
	// pinned to a single subcommand so a compromised webconfig cannot drift
	// between the read-only (status, export) and mutating (enable, disable,
	// set, reset, import) surfaces via the same grant. Secret values (e.g. an
	// FR24 sharing key) travel on the helper's stdin, never on argv.
	AggregatorStatus  []string
	AggregatorDetail  []string
	AggregatorEnable  []string
	AggregatorDisable []string
	AggregatorSet     []string
	AggregatorReset   []string
	AggregatorExport  []string
	AggregatorImport  []string
	// SSH* target the apl-ssh helper that manages SSH access for the pi
	// account only (a per-device password and a single managed authorized
	// key). As with apl-wifi/apl-aggregator, one entry per verb keeps the
	// sudoers grant pinned to a single subcommand so a compromised webconfig
	// cannot drift between the read-only (status) and mutating (enable / set /
	// disable / clear) surfaces via the same grant. The pi password value
	// travels on the helper's stdin, never on argv.
	SSHStatus          []string
	SSHEnablePassword  []string
	SSHSetPassword     []string
	SSHDisablePassword []string
	SSHSetKey          []string
	SSHClearKey        []string
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
		ApplyFeed:      sudo("/usr/local/bin/apl-feed", "apply", "--json", "--lock-timeout", "5"),
		SchemaFeed:     []string{"/usr/local/bin/apl-feed", "schema", "--json"},
		ConfigShowFeed: []string{"/usr/local/bin/apl-feed", "config", "show", "--json"},
		Reboot:         sudo("/usr/bin/systemctl", "reboot"),
		Poweroff:       sudo("/usr/bin/systemctl", "poweroff"),
		// StartOrchestrator launches the unified update orchestrator —
		// apt → runtime overlay — as a transient systemd unit. --collect
		// drops the unit record on exit so a repeat invocation doesn't 409
		// on a stale unit name. The ExecStopPost HUPs this service so the
		// schema cache reloads after the overlay leg potentially rewrote
		// /etc/airplanes/*.
		// Trampoline at /opt/airplanes/libexec/ is owned by
		// the image's pi-gen stage 06d; it execs into the orchestrator
		// binary inside the active runtime release.
		StartOrchestrator: sudo(
			"/usr/bin/systemd-run",
			"--unit=airplanes-update-orchestrator.service",
			"--collect",
			"--property=ExecStopPost=/usr/bin/systemctl kill -s HUP airplanes-webconfig.service",
			"/opt/airplanes/libexec/start-orchestrator.sh",
		),
		// --no-block: the unit is Type=oneshot and apl-feed claim register
		// retries on network failure for up to ~15s. A blocking start could
		// exceed systemctlTimeout; --no-block enqueues the job and returns
		// immediately. Progress and failures show up in the claim activity
		// log via the SSE stream the SPA opens after this returns.
		RegisterClaim: sudo("/usr/bin/systemctl", "start", "--no-block", "airplanes-claim.service"),
		// RotateClaim calls a wrapper script (not bare apl-feed) so the
		// sudoers grant is a single fixed argv that cannot drift to
		// `apl-feed claim rotate --abort` or other subcommands. The wrapper
		// pins `--json --max-retry-time 20`; claimRotateTimeout below adds
		// the headroom over that 20s budget.
		RotateClaim: sudo("/opt/airplanes/current/lib/airplanes-webconfig/claim-rotate.sh"),
		// Fired after a successful config save so a change reaches the server
		// on the next sync instead of the ~60s timer tick. --no-block: the
		// unit is Type=oneshot and `apl-feed config sync` does a network
		// round-trip; enqueue and return rather than risk systemctlTimeout.
		// The unit self-gates (claim secret + REMOTE_CONFIG_ENABLED opt-in),
		// so a redundant trigger is a no-op.
		SyncConfig:   sudo("/usr/bin/systemctl", "start", "--no-block", "airplanes-config-sync.service"),
		WifiList:     sudo("/usr/local/bin/apl-wifi", "list", "--json"),
		WifiAdd:      sudo("/usr/local/bin/apl-wifi", "add", "--json"),
		WifiUpdate:   sudo("/usr/local/bin/apl-wifi", "update", "--json"),
		WifiDelete:   sudo("/usr/local/bin/apl-wifi", "delete", "--json"),
		WifiTest:     sudo("/usr/local/bin/apl-wifi", "test", "--json"),
		WifiActivate: sudo("/usr/local/bin/apl-wifi", "activate", "--json"),
		WifiAdopt:    sudo("/usr/local/bin/apl-wifi", "adopt", "--json"),
		WifiStatus:   sudo("/usr/local/bin/apl-wifi", "status", "--json"),
		// export reads PSKs from root-only keyfiles for the combined backup;
		// import writes keyfiles non-disruptively. One pinned argv per verb.
		WifiExport: sudo("/usr/local/bin/apl-wifi", "export", "--json"),
		WifiImport: sudo("/usr/local/bin/apl-wifi", "import", "--json"),
		// Identity export/import call wrapper scripts (not bare apl-feed)
		// so each surface is its own fixed argv in sudoers. The wrappers
		// invoke `apl-feed backup -` and `apl-feed restore /dev/stdin
		// --force` internally.
		ExportIdentity: sudo("/opt/airplanes/current/lib/airplanes-webconfig/identity-export.sh"),
		ImportIdentity: sudo("/opt/airplanes/current/lib/airplanes-webconfig/identity-import.sh"),
		// apl-aggregator manages optional third-party feed aggregators. One
		// pinned argv per verb; the JSON request (and any secret field) is
		// read on stdin, not argv. The helper is laid down by the runtime
		// overlay at the same fixed path on every release.
		AggregatorStatus:  sudo("/usr/local/bin/apl-aggregator", "status", "--json"),
		AggregatorDetail:  sudo("/usr/local/bin/apl-aggregator", "detail", "--json"),
		AggregatorEnable:  sudo("/usr/local/bin/apl-aggregator", "enable", "--json"),
		AggregatorDisable: sudo("/usr/local/bin/apl-aggregator", "disable", "--json"),
		AggregatorSet:     sudo("/usr/local/bin/apl-aggregator", "set", "--json"),
		AggregatorReset:   sudo("/usr/local/bin/apl-aggregator", "reset", "--json"),
		AggregatorExport:  sudo("/usr/local/bin/apl-aggregator", "export", "--json"),
		AggregatorImport:  sudo("/usr/local/bin/apl-aggregator", "import", "--json"),
		// apl-ssh manages SSH access for the pi account. One pinned argv per
		// verb; the pi password (for enable/set) is read on stdin, not argv.
		// enable-password and set-password are the same operation server-side —
		// two verbs only so the grant + UI intent (enable vs rotate) read
		// clearly. The helper is laid down in the rootfs payload alongside this
		// argv contract so they version together.
		SSHStatus:          sudo("/usr/local/bin/apl-ssh", "status", "--json"),
		SSHEnablePassword:  sudo("/usr/local/bin/apl-ssh", "enable-password", "--json"),
		SSHSetPassword:     sudo("/usr/local/bin/apl-ssh", "set-password", "--json"),
		SSHDisablePassword: sudo("/usr/local/bin/apl-ssh", "disable-password", "--json"),
		SSHSetKey:          sudo("/usr/local/bin/apl-ssh", "set-key", "--json"),
		SSHClearKey:        sudo("/usr/local/bin/apl-ssh", "clear-key", "--json"),
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
	ClaimStatus  *claimstatus.Cache // account-claim status probe + cache
	Logs         *logs.Streamer
	Schema       *schemacache.Cache       // schema cache; required (use schemacache.New)
	Runner       wexec.CommandRunner      // override for tests; nil → exec.RealRunner
	StdinRunner  wexec.CommandRunnerStdin // ditto; piped variant for apl-feed apply
	Now          func() time.Time         // override for tests; nil → time.Now
	Privileged   PrivilegedArgv
	// UpgradeStatePath is the path the GET /api/status/upgrade handler
	// reads. Defaults to DefaultUpgradeStatePath; tests inject a tempfile.
	UpgradeStatePath string
	// OrchestratorStatePath is the path the GET /api/orchestrator/state
	// handler reads. Defaults to DefaultOrchestratorStatePath; tests
	// inject a tempfile.
	OrchestratorStatePath string
	// OrchestratorCapable, when non-nil, overrides the production
	// capability check (trampoline + overlay target are X_OK accessible).
	// Tests inject a closure that flips the gate without touching the
	// filesystem at well-known paths owned by airplanes-live/image.
	OrchestratorCapable func() bool
	// SDRSysfsRoot is the sysfs directory GET /api/sdr enumerates.
	// Defaults to sdr.DefaultSysfsRoot; tests and cmd/devserver inject a
	// fake tree.
	SDRSysfsRoot string
	// AssetsFS, when non-nil, overrides the embedded web/assets used for
	// GET / and GET /static/*. Default (nil) keeps production behavior:
	// assets are served from the binary's go:embed FS. cmd/devserver sets
	// this to a disk-backed os.DirFS so UI edits don't require a rebuild.
	AssetsFS fs.FS
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
	nowFunc := d.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	upgradeStatePath := d.UpgradeStatePath
	if upgradeStatePath == "" {
		upgradeStatePath = DefaultUpgradeStatePath
	}
	orchestratorStatePath := d.OrchestratorStatePath
	if orchestratorStatePath == "" {
		orchestratorStatePath = DefaultOrchestratorStatePath
	}
	orchestratorCapable := d.OrchestratorCapable
	if orchestratorCapable == nil {
		orchestratorCapable = defaultOrchestratorCapable
	}
	sdrSysfsRoot := d.SDRSysfsRoot
	if sdrSysfsRoot == "" {
		sdrSysfsRoot = sdr.DefaultSysfsRoot
	}
	s := &Server{
		version:                 d.Version,
		store:                   d.Store,
		sessions:                d.Sessions,
		lockout:                 d.Lockout,
		guard:                   d.Guard,
		argon2Params:            d.Argon2Params,
		identity:                d.Identity,
		feedEnv:                 d.FeedEnv,
		status:                  d.Status,
		claimStatus:             d.ClaimStatus,
		logs:                    d.Logs,
		runner:                  runner,
		stdinRunner:             stdinRunner,
		schema:                  d.Schema,
		nowFunc:                 nowFunc,
		priv:                    d.Privileged,
		upgradeStatePath:        upgradeStatePath,
		orchestratorStatePath:   orchestratorStatePath,
		orchestratorCapableFunc: orchestratorCapable,
		sdrSysfsRoot:            sdrSysfsRoot,
		assetsFS:                d.AssetsFS,
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
	// Combined device backup / restore. export + the configured-device restore
	// require a session; restore-setup is public but gated on the uninitialized
	// state (the same trust window as POST /api/setup) so a fresh flash can be
	// restored before a password exists. Restore streams NDJSON progress.
	mux.HandleFunc("POST /api/backup/export", s.requireSession(s.handleBackupExport))
	mux.HandleFunc("POST /api/backup/restore", s.requireSession(s.handleBackupRestore))
	mux.HandleFunc("POST /api/backup/restore-setup", s.handleBackupRestoreSetup)
	mux.HandleFunc("GET /api/config", s.requireSession(s.handleConfigGet))
	mux.HandleFunc("GET /api/sdr", s.requireSession(s.handleSDRList))
	mux.HandleFunc("GET /api/status", s.requireSession(s.handleStatus))
	mux.HandleFunc("GET /api/status/upgrade", s.requireSession(s.handleUpgradeStatus))
	mux.HandleFunc("GET /api/log/{unit}", s.requireSession(s.handleLog))
	mux.HandleFunc("POST /api/config", s.requireSession(s.handleConfigPost))
	mux.HandleFunc("POST /api/orchestrator/start", s.requireSession(s.handleOrchestratorStart))
	mux.HandleFunc("GET /api/orchestrator/state", s.requireSession(s.handleOrchestratorState))
	mux.HandleFunc("POST /api/reboot", s.requireSession(s.handleReboot))
	mux.HandleFunc("POST /api/poweroff", s.requireSession(s.handlePoweroff))
	mux.HandleFunc("POST /api/claim/register", s.requireSession(s.handleClaimRegister))
	mux.HandleFunc("POST /api/claim/rotate", s.requireSession(s.handleClaimRotate))
	mux.HandleFunc("GET /api/claim/status", s.requireSession(s.handleClaimStatus))

	// Wi-Fi network management — privileged subcommands of /usr/local/bin/apl-wifi.
	// No `s.wifiMu` mutex: the helper's flock at /run/airplanes/wifi.lock is
	// the cross-process serialization point; a Go mutex would queue concurrent
	// requests behind a 30s-test instead of letting the helper return a fast
	// 503 lock_timeout to the second caller.
	mux.HandleFunc("GET /api/wifi", s.requireSession(s.handleWifiList))
	mux.HandleFunc("GET /api/wifi/status", s.requireSession(s.handleWifiStatus))
	mux.HandleFunc("POST /api/wifi", s.requireSession(s.handleWifiAdd))
	mux.HandleFunc("POST /api/wifi/test", s.requireSession(s.handleWifiTest))
	mux.HandleFunc("PUT /api/wifi/{id}", s.requireSession(s.handleWifiUpdate))
	mux.HandleFunc("DELETE /api/wifi/{id}", s.requireSession(s.handleWifiDelete))
	mux.HandleFunc("POST /api/wifi/{id}/activate", s.requireSession(s.handleWifiActivate))
	mux.HandleFunc("POST /api/wifi/{id}/adopt", s.requireSession(s.handleWifiAdopt))

	// Third-party aggregators — privileged subcommands of
	// /usr/local/bin/apl-aggregator. As with Wi-Fi, no Go-side mutex: the
	// helper's flock at /run/airplanes/aggregator.lock serializes across
	// processes (and a webconfig restart racing the CLI) and returns a fast
	// lock_timeout 503 to the second caller. The aggregator sign-in details
	// (including sharing keys) are backed up and restored through the combined
	// device backup (/api/backup/*), which reuses the export/import helper verbs.
	mux.HandleFunc("GET /api/aggregators", s.requireSession(s.handleAggregatorList))
	mux.HandleFunc("GET /api/aggregators/{id}", s.requireSession(s.handleAggregatorDetail))
	mux.HandleFunc("POST /api/aggregators/{id}/enable", s.requireSession(s.handleAggregatorEnable))
	mux.HandleFunc("POST /api/aggregators/{id}/disable", s.requireSession(s.handleAggregatorDisable))
	mux.HandleFunc("POST /api/aggregators/{id}/set", s.requireSession(s.handleAggregatorSet))
	mux.HandleFunc("POST /api/aggregators/{id}/reset", s.requireSession(s.handleAggregatorReset))

	// SSH access for the pi account — privileged subcommands of
	// /usr/local/bin/apl-ssh. The status read is GET; every mutating verb is a
	// POST so it routes through requireOriginMatchesHost and carries a
	// current_password the handler re-verifies (and strips) before forwarding
	// to the helper. No Go-side mutex: the helper's flock at
	// /run/airplanes/ssh.lock serializes across processes and returns a fast
	// lock_timeout 503 to a second concurrent caller.
	mux.HandleFunc("GET /api/ssh", s.requireSession(s.handleSSHStatus))
	mux.HandleFunc("POST /api/ssh/enable-password", s.requireSession(s.handleSSHEnablePassword))
	mux.HandleFunc("POST /api/ssh/set-password", s.requireSession(s.handleSSHSetPassword))
	mux.HandleFunc("POST /api/ssh/disable-password", s.requireSession(s.handleSSHDisablePassword))
	mux.HandleFunc("POST /api/ssh/set-key", s.requireSession(s.handleSSHSetKey))
	mux.HandleFunc("POST /api/ssh/clear-key", s.requireSession(s.handleSSHClearKey))

	// Static assets at /static/*; the SPA shell is served by the GET /
	// handler below. no-store cache policy: assets are embedded in the
	// binary and a binary update is the only way they change, so a stale
	// cached copy after rollout would mask the new UI. cmd/devserver
	// overrides s.assetsFS with a disk-backed os.DirFS so UI iterations
	// don't require a rebuild — the same no-store policy means a browser
	// reload always picks up the edit.
	mux.Handle("GET /static/", http.StripPrefix("/static/", noStore(http.FileServer(http.FS(s.assets())))))

	// SPA shell: serve index.html on the root and reject anything else that
	// fell through to /-prefix matching.
	mux.HandleFunc("GET /{$}", s.handleIndex)

	// Compose middleware: security headers on every response; Origin/Host
	// check on every mutating method. requestLogger sits above the origin
	// check so 403s from origin-check rejections still appear in the
	// journal — a suspicious request is exactly what we want to see.
	return securityHeaders(requestLogger(requireOriginMatchesHost(mux)))
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

// assets returns the FS that backs GET / and GET /static/*. Defaults to
// the embedded web/assets subtree; cmd/devserver overrides it with a
// disk-backed os.DirFS via Deps.AssetsFS.
func (s *Server) assets() fs.FS {
	if s.assetsFS != nil {
		return s.assetsFS
	}
	sub, _ := fs.Sub(webassets.FS, "assets")
	return sub
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	body, err := fs.ReadFile(s.assets(), "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}
