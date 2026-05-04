package auth

import (
	"context"
	"errors"
)

// HashGuard limits concurrent argon2id evaluations. argon2id at 64 MiB is
// memory-hungry on a Pi Zero 2 W; an attacker spamming /api/auth/login
// could otherwise pin RAM. The guard caps simultaneous hashes at the
// configured concurrency; further requests fail fast with ErrBusy so the
// HTTP handler can return 429.
type HashGuard struct {
	sem chan struct{}
}

// ErrBusy is returned by TryRun when the concurrency cap is saturated.
var ErrBusy = errors.New("hash guard: at concurrency limit")

// NewHashGuard returns a guard that allows up to `concurrency` simultaneous
// runs. concurrency must be >= 1.
func NewHashGuard(concurrency int) (*HashGuard, error) {
	if concurrency < 1 {
		return nil, errors.New("hash guard: concurrency must be >= 1")
	}
	return &HashGuard{sem: make(chan struct{}, concurrency)}, nil
}

// TryRun runs fn with a slot held. If no slot is available immediately it
// returns ErrBusy without queueing.
func (g *HashGuard) TryRun(fn func()) error {
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
		fn()
		return nil
	default:
		return ErrBusy
	}
}

// RunCtx blocks until a slot is available or ctx is canceled. Used in
// non-attacker contexts where queueing is acceptable (e.g. setup).
func (g *HashGuard) RunCtx(ctx context.Context, fn func()) error {
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
		fn()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
