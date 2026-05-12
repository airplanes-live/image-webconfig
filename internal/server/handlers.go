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

// /api/config (GET): feed.env values filtered against the schema-cached
// readable_keys set. Returns 503 when the schema cache is unavailable
// (boot-time apl-feed schema --json fetch failed and no SIGHUP has
// since refreshed it).
func (s *Server) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	if s.schema == nil || s.schema.Degraded() {
		writeJSONError(w, http.StatusServiceUnavailable, "schema unavailable; retry after the next feed update")
		return
	}
	values, err := s.feedEnv.ReadAll()
	if err != nil {
		log.Printf("feedenv read: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "feed.env read failed")
		return
	}
	filtered := make(map[string]string, len(values))
	for k, v := range values {
		if s.schema.IsReadable(k) {
			filtered[k] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": filtered})
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

// /api/config (POST): proxies the body to `apl-feed apply --json` over a
// sudoers-pinned argv. The feed CLI owns validation, the universal-reject
// scan, the atomic write, and the dirty-key service-restart fan-out;
// webconfig translates exit codes + JSON envelopes into HTTP responses.
//
// The schema cache must be loaded (i.e. !s.schema.Degraded()) before this
// endpoint accepts writes — without it we cannot pre-filter the payload
// against the writable_keys set.
func (s *Server) handleConfigPost(w http.ResponseWriter, r *http.Request) {
	if s.schema == nil || s.schema.Degraded() {
		writeJSONError(w, http.StatusServiceUnavailable, "schema unavailable; retry after the next feed update")
		return
	}
	var req configRequest
	if err := readJSON(w, r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reject keys not in the schema's writable-key set BEFORE shelling out
	// — saves a privileged invocation on obvious client bugs and produces
	// a clearer per-key 400.
	for k := range req.Updates {
		if !s.schema.IsWritable(k) {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("not a writable key: %s", k))
			return
		}
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()

	resp, status, err := s.invokeApplyFeed(r.Context(), req.Updates)
	if err != nil {
		log.Printf("config-post: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "config write failed")
		return
	}
	if status != http.StatusOK {
		// Forward the structured error envelope from apl-feed apply
		// (per-field reasons, lock_timeout message, etc.) without
		// reshaping it — the client renders the per-key error directly.
		writeJSON(w, status, resp)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// applyFeedResponse mirrors the JSON envelope emitted by
// `apl-feed apply --json`. Any subset of fields can be populated
// depending on status; the client renders them in priority order
// (errors > pending_restart > changed).
type applyFeedResponse struct {
	Status         string            `json:"status"`
	Changed        []string          `json:"changed,omitempty"`
	PendingRestart []string          `json:"pending_restart,omitempty"`
	Errors         map[string]string `json:"errors,omitempty"`
	Message        string            `json:"message,omitempty"`
}

// invokeApplyFeed pipes the request body through `sudo apl-feed apply --json`
// and maps the structured response to an HTTP status. The error return
// is reserved for invocation-layer failures (binary missing, timeout)
// that are NOT part of the apply contract — those become 500.
//
// Status mapping:
//   applied          → 200 (pending_restart may be non-empty)
//   no_change        → 200
//   rejected         → 400
//   lock_timeout     → 503
//   filesystem_error → 500
//   parse_error      → 400 (apl-feed received malformed input from us, but
//                          forward as 400 so the client sees the message)
//   usage_error      → 500 (programmer error: argv shape diverged)
func (s *Server) invokeApplyFeed(ctx context.Context, updates map[string]string) (applyFeedResponse, int, error) {
	body, err := json.Marshal(map[string]any{"updates": updates})
	if err != nil {
		return applyFeedResponse{}, 0, err
	}
	cctx, cancel := context.WithTimeout(ctx, applyConfigTimeout)
	defer cancel()

	res, runErr := s.stdinRunner(cctx, s.priv.ApplyFeed, bytes.NewReader(body))
	var parsed applyFeedResponse
	if perr := json.Unmarshal(res.Stdout, &parsed); perr != nil {
		// Helper produced no JSON envelope on stdout — treat as an
		// internal error and surface stderr in the log only.
		log.Printf("apply-feed: cannot parse stdout: %v stdout=%q stderr=%q",
			perr, res.Stdout, strings.TrimSpace(string(res.Stderr)))
		return applyFeedResponse{}, 0, fmt.Errorf("apply-feed: %w", perr)
	}
	switch parsed.Status {
	case "applied", "no_change":
		return parsed, http.StatusOK, nil
	case "rejected":
		return parsed, http.StatusBadRequest, nil
	case "lock_timeout":
		return parsed, http.StatusServiceUnavailable, nil
	case "filesystem_error":
		return parsed, http.StatusInternalServerError, nil
	case "parse_error":
		return parsed, http.StatusBadRequest, nil
	default:
		log.Printf("apply-feed: unknown status %q exit=%d stderr=%q err=%v",
			parsed.Status, res.ExitCode, strings.TrimSpace(string(res.Stderr)), runErr)
		return parsed, http.StatusInternalServerError, nil
	}
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
