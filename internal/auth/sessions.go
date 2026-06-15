package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Sessions is a session store with sliding TTL. Entries are keyed by the
// SHA-256 of the token, never the token itself. With a persistence path set,
// the table is mirrored to disk so sessions survive a service restart — the
// runtime-overlay self-update restarts webconfig mid-flight and would
// otherwise log the operator out of the update progress view. The production
// path lives under /run (tmpfs), so a reboot still logs everyone out.
//
// Tokens are 32 bytes from crypto/rand (see Issue), so an unsalted SHA-256
// is preimage-safe: a read-only leak of the file cannot recover or mint a
// usable session cookie.
type Sessions struct {
	mu      sync.Mutex
	entries map[string]time.Time // SHA-256(token) hex → expiresAt
	ttl     time.Duration
	path    string // "" = in-memory only
	// lastPersist throttles sliding-TTL persistence: Validate bumps the
	// in-memory expiry on every request (the SPA polls every few seconds),
	// but rewrites the file at most once per persistInterval. After a
	// restart the persisted expiry may lag by up to that interval — noise
	// against a TTL measured in hours.
	lastPersist time.Time
	now         func() time.Time // injectable clock for tests
}

// persistInterval bounds how often Validate's sliding-TTL bump rewrites the
// session file. Issue/Revoke/RevokeAll persist immediately.
const persistInterval = time.Minute

// sessionsFileSchemaVersion gates the on-disk format. The overlay update
// swaps the binary underneath the file; a future format change bumps this
// and the loader starts empty instead of misreading old state.
const sessionsFileSchemaVersion = 1

type sessionsFile struct {
	SchemaVersion int                  `json:"schema_version"`
	Sessions      map[string]time.Time `json:"sessions"`
}

// ErrUnknownSession is returned when a token has no active entry (expired,
// revoked, or never issued).
var ErrUnknownSession = errors.New("auth: unknown session")

// NewSessions creates an in-memory session store with the given sliding TTL.
func NewSessions(ttl time.Duration) *Sessions {
	return NewPersistentSessions(ttl, "")
}

// NewPersistentSessions creates a session store that mirrors its entries
// (token hashes + expiries only) to path. An existing file is loaded
// tolerantly: missing, unreadable, corrupt, or wrong-schema files start the
// store empty rather than failing — losing sessions degrades to a re-login,
// never to a dead webconfig. path == "" disables persistence.
func NewPersistentSessions(ttl time.Duration, path string) *Sessions {
	s := &Sessions{
		entries: make(map[string]time.Time),
		ttl:     ttl,
		path:    path,
		now:     time.Now,
	}
	s.load()
	return s
}

// hashToken maps a raw bearer token to its storage key. Unsalted SHA-256 is
// sufficient because tokens are high-entropy random values, not passwords.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Sessions) load() {
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("auth: session file %s unreadable, starting empty: %v", s.path, err)
		}
		return
	}
	var f sessionsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("auth: session file %s corrupt, starting empty: %v", s.path, err)
		return
	}
	if f.SchemaVersion != sessionsFileSchemaVersion {
		log.Printf("auth: session file %s has schema %d (want %d), starting empty", s.path, f.SchemaVersion, sessionsFileSchemaVersion)
		return
	}
	now := s.now()
	for key, expires := range f.Sessions {
		// Hygiene: only well-formed, still-live hash entries survive a
		// reload.
		if len(key) != sha256.Size*2 {
			continue
		}
		if now.After(expires) {
			continue
		}
		s.entries[key] = expires
	}
}

// persistLocked mirrors the current table to disk atomically (temp file +
// rename, 0600). Callers must hold s.mu.
func (s *Sessions) persistLocked() error {
	if s.path == "" {
		return nil
	}
	f := sessionsFile{
		SchemaVersion: sessionsFileSchemaVersion,
		Sessions:      s.entries,
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("auth: marshal sessions: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("auth: create session temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: chmod session temp file: %w", err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("auth: write session temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("auth: close session temp file: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("auth: rename session file: %w", err)
	}
	s.lastPersist = s.now()
	return nil
}

// Issue creates a new session token with expires=now+ttl. Returns the
// base64url-encoded token (32 random bytes). A persistence failure is logged
// but does not fail the login — the session still works until the next
// service restart.
func (s *Sessions) Issue() (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	expires := s.now().Add(s.ttl)
	s.entries[hashToken(token)] = expires
	if err := s.persistLocked(); err != nil {
		log.Printf("auth: issue: %v", err)
	}
	return token, expires, nil
}

// Validate reports whether the token is active. On success it bumps the
// expiry to now+ttl (sliding) and returns the new expires-at. Expired entries
// are deleted lazily. The bump is persisted at most once per persistInterval.
func (s *Sessions) Validate(token string) (time.Time, error) {
	if token == "" {
		return time.Time{}, ErrUnknownSession
	}
	key := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.entries[key]
	now := s.now()
	if !ok {
		return time.Time{}, ErrUnknownSession
	}
	if now.After(expiresAt) {
		delete(s.entries, key)
		if err := s.persistLocked(); err != nil {
			log.Printf("auth: expire: %v", err)
		}
		return time.Time{}, ErrUnknownSession
	}
	newExpiry := now.Add(s.ttl)
	s.entries[key] = newExpiry
	if s.path != "" && now.Sub(s.lastPersist) >= persistInterval {
		if err := s.persistLocked(); err != nil {
			log.Printf("auth: validate: %v", err)
		}
	}
	return newExpiry, nil
}

// Revoke deletes the given token (no error if it didn't exist). A
// persistence failure is logged; worst case a restart resurrects a session
// the user logged out of, bounded by the TTL.
func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, hashToken(token))
	if err := s.persistLocked(); err != nil {
		log.Printf("auth: revoke: %v", err)
	}
}

// RevokeAll deletes every session. Used at password change / reset, so the
// disk mirror must go too: the file is removed (absent file = empty store)
// and a failure to remove it propagates — the caller should fail loudly
// rather than let a stale file resurrect pre-change sessions at the next
// restart. The in-memory table is cleared regardless.
func (s *Sessions) RevokeAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = make(map[string]time.Time)
	if s.path == "" {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("auth: revoke-all: remove session file: %w", err)
	}
	return nil
}

// Count reports the current number of active entries (for tests / metrics).
func (s *Sessions) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
