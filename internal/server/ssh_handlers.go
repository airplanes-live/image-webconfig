package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	"github.com/airplanes-live/image-webconfig/internal/ssh"
)

const (
	// sshHelperTimeout caps every apl-ssh child. All verbs are fast: status
	// runs sshd -T + passwd -S + getent; the mutating verbs touch a couple of
	// files plus chpasswd/passwd and one systemctl reload. 15s is comfortable
	// headroom over the helper's own 5s flock budget.
	sshHelperTimeout = 15 * time.Second

	// sshBodyLimit caps the SSH mutation request bodies. The largest payload is
	// a current_password (re-auth) plus a single OpenSSH public key; an RSA-4096
	// key line is ~750 bytes, so 8 KiB is generous headroom without inviting a
	// memory-exhaustion flood.
	sshBodyLimit = 8192
)

// invokeSSH pipes body through the sudoers-pinned argv, parses the helper's
// envelope status, and maps it to an HTTP status. Returns the helper's stdout
// verbatim (forwarded to the browser), the HTTP status, and a non-nil error
// only on runner-layer failures (binary missing, empty or unparseable stdout).
// The helper's own rejection paths land here as (stdout, 4xx/5xx, nil).
//
// Like the aggregator handlers (and unlike wifi_handlers.go), the helper's raw
// stdout is NOT included in the returned error and is never logged: a future
// status field could carry device facts we'd rather not splash into the
// journal, and the byte length is enough to triage a contract violation.
func (s *Server) invokeSSH(ctx context.Context, argv []string, body []byte) ([]byte, int, error) {
	cctx, cancel := context.WithTimeout(ctx, sshHelperTimeout)
	defer cancel()
	res, _ := s.stdinRunner(cctx, argv, bytes.NewReader(body))
	if len(res.Stdout) == 0 {
		return nil, 0, fmt.Errorf("apl-ssh %v: empty stdout (stderr=%q)", argv, strings.TrimSpace(string(res.Stderr)))
	}
	status, perr := ssh.Parse(res.Stdout)
	if perr != nil {
		return nil, 0, fmt.Errorf("apl-ssh %v: %w (stdout %d bytes, not logged)", argv, perr, len(res.Stdout))
	}
	return res.Stdout, ssh.HTTPStatus(status), nil
}

// writeSSHResponse forwards the helper's JSON envelope to the browser with
// no-store cache headers and the HTTP status the envelope maps to.
func (s *Server) writeSSHResponse(w http.ResponseWriter, body []byte, httpStatus int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(httpStatus)
	_, _ = w.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, _ = w.Write([]byte("\n"))
	}
}

// handleSSHStatus (GET /api/ssh) returns the granular SSH facts (pi account
// present, password auth allowed, password hash unlocked, managed key present).
// Read-only; the helper takes no stdin.
func (s *Server) handleSSHStatus(w http.ResponseWriter, r *http.Request) {
	body, httpStatus, err := s.invokeSSH(r.Context(), s.priv.SSHStatus, nil)
	if err != nil {
		log.Printf("ssh status: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "ssh status failed")
		return
	}
	s.writeSSHResponse(w, body, httpStatus)
}

// reauthAndForward is the shared body for every mutating SSH verb. The request
// carries `current_password`; we verify it against a FRESHLY RE-READ argon2
// hash (never a cached value) and return 401 on mismatch. On success the
// current_password field is STRIPPED from the payload before the remaining
// JSON is piped to the helper's stdin — the privileged helper must never
// receive the admin password.
//
// label is used only for log lines. The remaining (stripped) payload is
// forwarded as-is; the helper owns field validation (password length, pubkey
// shape) and emits the rejected envelope on bad input.
func (s *Server) reauthAndForward(w http.ResponseWriter, r *http.Request, label string, argv []string) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeJSONError(w, http.StatusBadRequest, errBadContentType.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, sshBodyLimit)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "request body too large or unreadable")
		return
	}

	// Decode into a generic map so we can lift out current_password and forward
	// the rest untouched. A null/non-object body is a 400, never a panic.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		writeJSONError(w, http.StatusBadRequest, "request body must be a JSON object")
		return
	}
	rawPw, ok := fields["current_password"]
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "current_password is required")
		return
	}
	var currentPassword string
	if err := json.Unmarshal(rawPw, &currentPassword); err != nil {
		writeJSONError(w, http.StatusBadRequest, "current_password must be a string")
		return
	}

	if !s.verifyAdminPassword(r.Context(), currentPassword) {
		writeJSONError(w, http.StatusUnauthorized, "wrong password")
		return
	}

	// Strip the admin password before it can reach the privileged helper's
	// stdin, then re-serialise the remaining fields.
	delete(fields, "current_password")
	forward, err := json.Marshal(fields)
	if err != nil {
		log.Printf("ssh %s: re-marshal: %v", label, err)
		writeJSONError(w, http.StatusInternalServerError, "ssh operation failed")
		return
	}

	resp, httpStatus, ierr := s.invokeSSH(r.Context(), argv, forward)
	if ierr != nil {
		log.Printf("ssh %s: %v", label, ierr)
		writeJSONError(w, http.StatusInternalServerError, "ssh operation failed")
		return
	}
	s.writeSSHResponse(w, resp, httpStatus)
}

// verifyAdminPassword re-reads the on-disk hash under the store mutex and
// verifies password against it via the argon2 guard. The hash is read fresh on
// every call (not cached) so a concurrent password change is reflected
// immediately. Returns false on any error (read failure, guard saturation,
// verify error) — fail closed.
func (s *Server) verifyAdminPassword(ctx context.Context, password string) bool {
	s.store.Lock()
	phc, err := s.store.Read()
	s.store.Unlock()
	if err != nil {
		log.Printf("ssh reauth: read hash: %v", err)
		return false
	}
	var ok bool
	var verifyErr error
	if guardErr := s.guard.RunCtx(ctx, func() {
		ok, verifyErr = auth.Verify(password, phc)
	}); guardErr != nil {
		log.Printf("ssh reauth: guard: %v", guardErr)
		return false
	}
	if verifyErr != nil {
		log.Printf("ssh reauth: verify: %v", verifyErr)
		return false
	}
	return ok
}

func (s *Server) handleSSHEnablePassword(w http.ResponseWriter, r *http.Request) {
	s.reauthAndForward(w, r, "enable-password", s.priv.SSHEnablePassword)
}

func (s *Server) handleSSHSetPassword(w http.ResponseWriter, r *http.Request) {
	s.reauthAndForward(w, r, "set-password", s.priv.SSHSetPassword)
}

func (s *Server) handleSSHDisablePassword(w http.ResponseWriter, r *http.Request) {
	s.reauthAndForward(w, r, "disable-password", s.priv.SSHDisablePassword)
}

func (s *Server) handleSSHSetKey(w http.ResponseWriter, r *http.Request) {
	s.reauthAndForward(w, r, "set-key", s.priv.SSHSetKey)
}

func (s *Server) handleSSHClearKey(w http.ResponseWriter, r *http.Request) {
	s.reauthAndForward(w, r, "clear-key", s.priv.SSHClearKey)
}
