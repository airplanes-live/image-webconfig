package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/auth"
	"github.com/airplanes-live/image/webconfig/internal/configspec"
	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
	"github.com/airplanes-live/image/webconfig/internal/identity"
	"github.com/airplanes-live/image/webconfig/internal/logs"
)

// MinPasswordLen is the minimum length we accept for setup / change-password.
const MinPasswordLen = 12

// stateResponse is returned by GET /api/state.
type stateResponse struct {
	State string `json:"state"`
}

const (
	stateUninitialized = "uninitialized"
	stateInitialized   = "initialized"
	stateCorrupt       = "corrupt"
)

// detectState stat's the password store. Empty/missing → uninitialized.
// Present and parseable → initialized. Present but malformed → corrupt
// (handled by handlers as a hard 500 with a recovery hint).
func (s *Server) detectState() string {
	exists, err := s.store.Exists()
	if err != nil || !exists {
		return stateUninitialized
	}
	phc, err := s.store.Read()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return stateUninitialized
		}
		return stateCorrupt
	}
	// Cheap structural check (no argon2 invocation): PHC starts with the
	// expected prefix and has the expected number of $ separators.
	if !strings.HasPrefix(phc, "$argon2id$v=19$") || strings.Count(phc, "$") != 5 {
		return stateCorrupt
	}
	return stateInitialized
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	st := s.detectState()
	if st == stateCorrupt {
		writeJSONError(w, http.StatusInternalServerError,
			"password file is corrupt; recover via /boot/firmware/airplanes-reset-password")
		return
	}
	writeJSON(w, http.StatusOK, stateResponse{State: st})
}

// passwordRequest covers POST /api/setup and the new-password half of
// POST /api/auth/login and POST /api/auth/password.
type passwordRequest struct {
	Password string `json:"password"`
}

type changePasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if st := s.detectState(); st != stateUninitialized {
		writeJSONError(w, http.StatusConflict, "webconfig already initialized")
		return
	}
	var req passwordRequest
	if err := readJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Password) < MinPasswordLen {
		writeJSONError(w, http.StatusBadRequest,
			"password must be at least 12 characters")
		return
	}

	var phc string
	var hashErr error
	if err := s.guard.RunCtx(r.Context(), func() {
		phc, hashErr = auth.Hash(req.Password, s.argon2Params)
	}); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "hash queue exhausted")
		return
	}
	if hashErr != nil {
		log.Printf("setup: hash: %v", hashErr)
		writeJSONError(w, http.StatusInternalServerError, "hash failed")
		return
	}

	if err := s.store.Setup(phc); err != nil {
		if errors.Is(err, auth.ErrExists) {
			writeJSONError(w, http.StatusConflict, "webconfig already initialized")
			return
		}
		log.Printf("setup: store: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "store write failed")
		return
	}

	// Auto-login — only the linkat winner reaches here.
	token, expires, err := s.sessions.Issue()
	if err != nil {
		log.Printf("setup: session issue: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "session issue failed")
		return
	}
	setSessionCookie(w, token, expires)
	writeJSON(w, http.StatusOK, map[string]string{"state": stateInitialized})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if st := s.detectState(); st != stateInitialized {
		writeJSONError(w, http.StatusConflict, "webconfig is "+st)
		return
	}
	if locked, _ := s.lockout.Locked(); locked {
		writeJSONError(w, http.StatusLocked, "too many failed attempts; try again later")
		return
	}

	var req passwordRequest
	if err := readJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Read the current hash under the store mutex. The mutex is released
	// before Argon2 (slow); we then re-read after verify to detect a
	// password-change race (codex finding).
	s.store.Lock()
	phc, err := s.store.Read()
	s.store.Unlock()
	if err != nil {
		log.Printf("login: read hash: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var ok bool
	var verifyErr error
	if guardErr := s.guard.TryRun(func() {
		ok, verifyErr = auth.Verify(req.Password, phc)
	}); guardErr != nil {
		writeJSONError(w, http.StatusTooManyRequests, "server busy; retry shortly")
		return
	}
	// Re-check lockout after acquiring (race with concurrent failures).
	if locked, _ := s.lockout.Locked(); locked {
		writeJSONError(w, http.StatusLocked, "too many failed attempts; try again later")
		return
	}

	if verifyErr != nil {
		log.Printf("login: verify: %v", verifyErr)
		writeJSONError(w, http.StatusInternalServerError, "verify failed")
		return
	}
	if !ok {
		s.lockout.RecordFailure()
		writeJSONError(w, http.StatusUnauthorized, "wrong password")
		return
	}

	// Race guard: the hash may have been replaced while Verify was running.
	// If it changed, the password we just verified against is stale —
	// refuse to issue a session.
	s.store.Lock()
	current, err := s.store.Read()
	s.store.Unlock()
	if err != nil || current != phc {
		s.lockout.RecordFailure()
		writeJSONError(w, http.StatusConflict, "password changed mid-flight; retry")
		return
	}

	s.lockout.Reset()
	token, expires, err := s.sessions.Issue()
	if err != nil {
		log.Printf("login: issue session: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "session issue failed")
		return
	}
	setSessionCookie(w, token, expires)
	writeJSON(w, http.StatusOK, map[string]string{"state": stateInitialized})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if tok := readSessionToken(r); tok != "" {
		s.sessions.Revoke(tok)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := readJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.NewPassword) < MinPasswordLen {
		writeJSONError(w, http.StatusBadRequest,
			"new password must be at least 12 characters")
		return
	}

	s.store.Lock()
	phc, err := s.store.Read()
	s.store.Unlock()
	if err != nil {
		log.Printf("change-password: read: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	var ok bool
	var verifyErr error
	if guardErr := s.guard.RunCtx(r.Context(), func() {
		ok, verifyErr = auth.Verify(req.OldPassword, phc)
	}); guardErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "hash queue exhausted")
		return
	}
	if verifyErr != nil {
		log.Printf("change-password: verify: %v", verifyErr)
		writeJSONError(w, http.StatusInternalServerError, "verify failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "wrong current password")
		return
	}

	var newPHC string
	var hashErr error
	if guardErr := s.guard.RunCtx(r.Context(), func() {
		newPHC, hashErr = auth.Hash(req.NewPassword, s.argon2Params)
	}); guardErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "hash queue exhausted")
		return
	}
	if hashErr != nil {
		log.Printf("change-password: hash: %v", hashErr)
		writeJSONError(w, http.StatusInternalServerError, "hash failed")
		return
	}
	if err := s.store.Replace(newPHC); err != nil {
		log.Printf("change-password: store: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "store write failed")
		return
	}

	// Rotate ALL sessions (codex: don't preserve the current one — captured
	// cookies survive a password change otherwise on LAN-HTTP).
	s.sessions.RevokeAll()
	token, expires, err := s.sessions.Issue()
	if err != nil {
		log.Printf("change-password: issue: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "session issue failed")
		return
	}
	setSessionCookie(w, token, expires)
	writeJSON(w, http.StatusOK, map[string]string{"status": "password changed"})
}

func (s *Server) handleWhoami(w http.ResponseWriter, _ *http.Request) {
	// requireSession middleware has already validated the session.
	writeJSON(w, http.StatusOK, map[string]string{"username": "admin"})
}

// /api/identity (GET): redacted view — feeder ID + claim_secret_present flag.
func (s *Server) handleIdentity(w http.ResponseWriter, _ *http.Request) {
	id, err := s.identity.Read()
	if err != nil {
		if errors.Is(err, identity.ErrNoFeederID) {
			writeJSON(w, http.StatusOK, identity.Identity{}) // empty struct: feeder not yet first-run
			return
		}
		log.Printf("identity read: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "identity read failed")
		return
	}
	writeJSON(w, http.StatusOK, id)
}

// /api/identity/secret (POST): full claim secret reveal. POST so it can't
// be cached or logged in browser history; Cache-Control: no-store via
// writeJSON.
func (s *Server) handleIdentitySecret(w http.ResponseWriter, _ *http.Request) {
	got, err := s.identity.Reveal()
	if err != nil {
		if errors.Is(err, identity.ErrNoClaimSecret) {
			writeJSONError(w, http.StatusNotFound, "no claim secret yet — register first")
			return
		}
		log.Printf("identity reveal: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "identity reveal failed")
		return
	}
	writeJSON(w, http.StatusOK, got)
}

// /api/config (GET): whitelisted feed.env keys.
func (s *Server) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	values, err := s.feedEnv.ReadAll()
	if err != nil {
		log.Printf("feedenv read: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "feed.env read failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

// /api/status (GET): service states + manifest + feed snapshot.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.status.Read(r.Context())
	if err != nil {
		log.Printf("status read: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "status read failed")
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// /api/log/{unit} (GET): SSE-stream journalctl output for the unit.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("unit")
	if err := s.logs.ServeSSE(r.Context(), w, slug); err != nil {
		if errors.Is(err, logs.ErrUnknownUnit) {
			writeJSONError(w, http.StatusNotFound, "unit not in webconfig log allowlist")
			return
		}
		log.Printf("log stream %s: %v", slug, err)
		// Headers may already be sent (SSE); no point writing JSON now.
	}
}

// configRequest is the POST /api/config payload.
type configRequest struct {
	Updates map[string]string `json:"updates"`
}

const (
	// applyConfigTimeout caps the helper's wall time. The helper itself
	// has a 5s lock-acquisition timeout; total budget is generous.
	applyConfigTimeout = 10 * time.Second
	// systemctlTimeout caps each per-unit systemctl call.
	systemctlTimeout = 10 * time.Second
)

// /api/config (POST): atomic feed.env write through the apply-config helper,
// followed by per-unit restart for feed/mlat/dump978-fa/airplanes-978. The
// whole transaction is serialized by an in-process mutex so concurrent posts
// can't interleave feed.env writes and unit restarts. The 978 daemons
// self-decide on UAT_INPUT (mlat-style: exit 64 when not requested), so the
// server only needs to kick a restart — no more enable/disable reconcile.
func (s *Server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	var req configRequest
	if err := readJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Daemon-side validation. The helper re-validates against the same
	// configspec — keep both layers; drift is exactly what the shared
	// package prevents.
	for k, v := range req.Updates {
		if err := configspec.Validate(k, v); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// Cross-key consistency precheck: reject inconsistent shapes (today
	// just GEO_CONFIGURED=true requiring non-empty LATITUDE + LONGITUDE)
	// early so the user gets a clear 400 from the dashboard rather than
	// a silently-failing daemon after the helper has already written the
	// bad state. We compute the projection of (existing ∪ updates) — full
	// merge happens in apply-config.
	{
		preview, err := s.feedEnv.ReadAll()
		if err != nil {
			log.Printf("config-post: read feed.env for consistency check: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "feed.env read failed")
			return
		}
		for k, v := range req.Updates {
			preview[k] = v
		}
		if err := configspec.ValidateConsistency(preview); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()

	if err := s.invokeApplyConfig(r.Context(), req.Updates); err != nil {
		var ce *configError
		if errors.As(err, &ce) {
			writeJSONError(w, ce.status, ce.message)
			return
		}
		log.Printf("config-post: helper: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "config write failed")
		return
	}

	// Service restart for feed + mlat + dump978-fa + airplanes-978. Failures
	// here log loudly AND get surfaced to the client as `pending_restart` so
	// the dashboard can alert the user that their saved config isn't actually
	// running yet. Restarting the daemon is what makes the new config take
	// effect; if it doesn't happen, /api/status will continue to reflect the
	// previous running config — making this a real foot-gun if silently
	// swallowed.
	//
	// `systemctl restart` returns 0 once the unit's start job has been
	// dispatched, regardless of whether the wrapper later exits 64 (the
	// self-disable path used by mlat and the 978 daemons when their config
	// says "off"). So restarting on "UAT_INPUT cleared by user" does NOT
	// generate a spurious pending_restart entry — the daemon is intentionally
	// failed-terminal, not "couldn't be restarted".
	var pendingRestart []string
	for _, namedArgv := range []struct {
		unit string
		argv []string
	}{
		{"airplanes-feed.service", s.priv.RestartFeed},
		{"airplanes-mlat.service", s.priv.RestartMLAT},
		{"dump978-fa.service", s.priv.RestartDump978},
		{"airplanes-978.service", s.priv.RestartUAT},
	} {
		if err := s.runSudo(r.Context(), namedArgv.argv, systemctlTimeout); err != nil {
			log.Printf("config-post: %s restart: %v", namedArgv.unit, err)
			pendingRestart = append(pendingRestart, namedArgv.unit)
		}
	}

	writeJSON(w, http.StatusOK, configApplyResponse{
		Status:         "applied",
		PendingRestart: pendingRestart,
	})
}

// configApplyResponse is the body returned by POST /api/config. The
// PendingRestart field is omitempty so the legacy {"status":"applied"}
// shape is preserved when all restarts succeed; new clients check for
// pending_restart and surface a "saved, but restart failed" banner.
// Unit names are full ("airplanes-mlat.service") so clients don't have
// to append .service.
type configApplyResponse struct {
	Status         string   `json:"status"`
	PendingRestart []string `json:"pending_restart,omitempty"`
}

// configError carries an HTTP status + safe-to-surface message.
type configError struct {
	status  int
	message string
}

func (e *configError) Error() string { return e.message }

// invokeApplyConfig pipes the JSON body to the helper via fixed-argv sudo.
// Helper exit codes 10/11/12 (validation/parse/oversize) → 400; 20
// (filesystem) → 500. Stderr is logged server-side; the response carries
// only a stable message to avoid leaking internals.
func (s *Server) invokeApplyConfig(ctx context.Context, updates map[string]string) error {
	body, err := json.Marshal(map[string]any{"updates": updates})
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, applyConfigTimeout)
	defer cancel()

	res, err := s.stdinRunner(cctx, s.priv.ApplyConfig, bytes.NewReader(body))
	if err == nil {
		return nil
	}
	exit := res.ExitCode
	logHelper(exit, res, err)
	switch exit {
	case 10, 11, 12, 30:
		return &configError{status: http.StatusBadRequest, message: "config rejected by helper"}
	default:
		return fmt.Errorf("helper exit %d: %w", exit, err)
	}
}

func logHelper(exit int, res wexec.Result, err error) {
	log.Printf("apply-config exit=%d err=%v stderr=%q",
		exit, err, strings.TrimSpace(string(res.Stderr)))
}

// runSudo runs argv with a per-call timeout and logs on failure.
func (s *Server) runSudo(ctx context.Context, argv []string, timeout time.Duration) error {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := s.runner(cctx, argv)
	if err != nil {
		return fmt.Errorf("sudo %v: %w (stderr=%q)", argv, err, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// /api/update (POST): kicks off a transient airplanes-update.service via
// systemd-run. Returns 202 + the unit name so the SPA can stream
// /api/log/update for live output. systemd-run exits with non-zero on
// "unit already exists" — we map that to 409.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	cctx, cancel := context.WithTimeout(r.Context(), systemctlTimeout)
	defer cancel()
	res, err := s.runner(cctx, s.priv.StartUpdate)
	if err != nil {
		stderr := strings.TrimSpace(string(res.Stderr))
		log.Printf("update: %v stderr=%q", err, stderr)
		if strings.Contains(stderr, "already exists") || strings.Contains(stderr, "already running") {
			writeJSONError(w, http.StatusConflict, "update is already in progress")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "update start failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "running",
		"unit":       "airplanes-update.service",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// /api/reboot (POST): writes 202 + flushes, then triggers reboot from a
// goroutine after a brief delay so the response actually leaves the wire
// before init starts tearing things down.
func (s *Server) handleReboot(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "rebooting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
		defer cancel()
		res, err := s.runner(ctx, s.priv.Reboot)
		if err != nil {
			log.Printf("reboot: %v stderr=%q", err, strings.TrimSpace(string(res.Stderr)))
		}
	}()
}
