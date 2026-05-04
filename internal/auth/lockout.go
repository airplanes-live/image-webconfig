package auth

import (
	"sync"
	"time"
)

// Lockout is a global login throttle: after `threshold` failures within
// `window`, all login attempts are refused for `duration`. Webconfig sees
// every request as RemoteAddr=127.0.0.1 (lighttpd reverse-proxy), so this
// is per-instance, not per-IP. Single-admin Pi → effectively per-admin.
type Lockout struct {
	mu          sync.Mutex
	failures    []time.Time
	lockedUntil time.Time
	threshold   int
	window      time.Duration
	duration    time.Duration
	now         func() time.Time
}

// NewLockout creates a throttle that locks after `threshold` failures within
// `window`, holding the lock for `duration`.
func NewLockout(threshold int, window, duration time.Duration) *Lockout {
	return &Lockout{
		threshold: threshold,
		window:    window,
		duration:  duration,
		now:       time.Now,
	}
}

// Locked reports whether the throttle is currently active. Returns the
// lock-end time if locked, otherwise the zero value.
func (l *Lockout) Locked() (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.locked()
}

// locked is the unlocked-mu helper.
func (l *Lockout) locked() (bool, time.Time) {
	now := l.now()
	if now.Before(l.lockedUntil) {
		return true, l.lockedUntil
	}
	return false, time.Time{}
}

// RecordFailure adds a failed-login event. Returns (true, lockedUntil) if
// the threshold is now crossed and the lock has been activated.
func (l *Lockout) RecordFailure() (bool, time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Before(l.lockedUntil) {
		// Already locked; don't extend.
		return true, l.lockedUntil
	}
	// Drop failures older than the window.
	cutoff := now.Add(-l.window)
	kept := l.failures[:0]
	for _, t := range l.failures {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.failures = append(kept, now)
	if len(l.failures) >= l.threshold {
		l.lockedUntil = now.Add(l.duration)
		l.failures = nil // start fresh after lock expires
		return true, l.lockedUntil
	}
	return false, time.Time{}
}

// Reset clears all failures and any active lockout. Called on successful login.
func (l *Lockout) Reset() {
	l.mu.Lock()
	l.failures = nil
	l.lockedUntil = time.Time{}
	l.mu.Unlock()
}
