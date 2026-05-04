package server

import (
	"errors"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"github.com/airplanes-live/image/webconfig/internal/auth"
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
