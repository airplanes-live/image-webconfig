package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// Sessions is an in-memory session store with sliding TTL. Service restart =
// everyone logged out; this is intentional (no on-disk persistence).
type Sessions struct {
	mu      sync.Mutex
	entries map[string]time.Time // token → expiresAt
	ttl     time.Duration
	now     func() time.Time // injectable clock for tests
}

// ErrUnknownSession is returned when a token has no active entry (expired,
// revoked, or never issued).
var ErrUnknownSession = errors.New("auth: unknown session")

// NewSessions creates a session store with the given sliding TTL.
func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{
		entries: make(map[string]time.Time),
		ttl:     ttl,
		now:     time.Now,
	}
}

// Issue creates a new session token with expires=now+ttl. Returns the
// base64url-encoded token (32 random bytes).
func (s *Sessions) Issue() (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	expires := s.now().Add(s.ttl)
	s.entries[token] = expires
	return token, expires, nil
}

// Validate reports whether the token is active. On success it bumps the
// expiry to now+ttl (sliding) and returns the new expires-at. Expired entries
// are deleted lazily.
func (s *Sessions) Validate(token string) (time.Time, error) {
	if token == "" {
		return time.Time{}, ErrUnknownSession
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	expiresAt, ok := s.entries[token]
	now := s.now()
	if !ok {
		return time.Time{}, ErrUnknownSession
	}
	if now.After(expiresAt) {
		delete(s.entries, token)
		return time.Time{}, ErrUnknownSession
	}
	newExpiry := now.Add(s.ttl)
	s.entries[token] = newExpiry
	return newExpiry, nil
}

// Revoke deletes the given token (no error if it didn't exist).
func (s *Sessions) Revoke(token string) {
	s.mu.Lock()
	delete(s.entries, token)
	s.mu.Unlock()
}

// RevokeAll deletes every session. Used at password change / reset.
func (s *Sessions) RevokeAll() {
	s.mu.Lock()
	s.entries = make(map[string]time.Time)
	s.mu.Unlock()
}

// Count reports the current number of active entries (for tests / metrics).
func (s *Sessions) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
