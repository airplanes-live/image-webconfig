// Package claimstatus surfaces the feeder's account-claim status to the
// webconfig UI. The webconfig itself makes no network calls; it shells out
// to `apl-feed claim status --json`, which probes the backend with the
// local claim secret and reports whether the feeder is registered and
// whether a user account has claimed it (owner_present).
//
// The result is cached server-side with a single-flight guard so browser
// reloads and multiple tabs collapse to one backend probe, keeping us well
// inside the feeder-status endpoint's per-UUID rate limit. The cache is
// keyed by the feeder identity (UUID + claim-secret fingerprint) so an
// identity import / claim rotation invalidates a stale "claimed" verdict
// without any explicit hook.
package claimstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// Result tokens. The first group is "definitive": a known local or
// server-confirmed state worth caching and falling back to. The second
// group is "transient": the probe could not get a definitive answer this
// time, so we prefer a previously-cached definitive value if we have one.
const (
	ResultClaimed            = "claimed"
	ResultUnclaimed          = "unclaimed"
	ResultSecretMismatch     = "secret_mismatch"
	ResultServerUnregistered = "server_unregistered"
	ResultUnregistered       = "unregistered"
	ResultSecretInvalid      = "secret_invalid"
	ResultNoIdentity         = "no_identity"
	ResultBlocked            = "blocked"

	ResultRateLimited = "rate_limited"
	ResultUnreachable = "unreachable"
	ResultError       = "error"
	// ResultUnavailable is webconfig-local: the apl-feed claim status
	// command could not be run or did not emit a schema-v1 object (old
	// feed without the subcommand, missing binary, crash). Distinct from
	// the feed-emitted transient results above.
	ResultUnavailable = "unavailable"
)

// definitiveResults are the verdicts safe to cache and serve as a
// last-known value when a later probe fails.
var definitiveResults = map[string]bool{
	ResultClaimed:            true,
	ResultUnclaimed:          true,
	ResultSecretMismatch:     true,
	ResultServerUnregistered: true,
	ResultUnregistered:       true,
	ResultSecretInvalid:      true,
	ResultNoIdentity:         true,
	ResultBlocked:            true,
}

func isDefinitive(result string) bool { return definitiveResults[result] }

// Output is the `apl-feed claim status --json` schema-v1 payload. Pointer
// fields distinguish "absent" from a zero value so the API response can
// omit them rather than assert false/0.
type Output struct {
	SchemaVersion      int     `json:"schema_version"`
	Result             string  `json:"result"`
	Registered         *bool   `json:"registered"`
	OwnerPresent       *bool   `json:"owner_present"`
	Version            *int    `json:"version"`
	ResetUntil         *string `json:"reset_until"`
	LastSeenAt         *string `json:"last_seen_at"`
	LastSeenAgeSeconds *int    `json:"last_seen_age_seconds"`
	RetryAfterSeconds  *int    `json:"retry_after_seconds"`
	Detail             *string `json:"detail"`
}

// Response is the GET /api/claim/status body. It is the probe Output plus
// freshness metadata the SPA renders ("checked N ago", stale banner).
type Response struct {
	Result             string  `json:"result"`
	Registered         *bool   `json:"registered,omitempty"`
	OwnerPresent       *bool   `json:"owner_present,omitempty"`
	Version            *int    `json:"version,omitempty"`
	ResetUntil         *string `json:"reset_until,omitempty"`
	LastSeenAt         *string `json:"last_seen_at,omitempty"`
	LastSeenAgeSeconds *int    `json:"last_seen_age_seconds,omitempty"`
	RetryAfterSeconds  *int    `json:"retry_after_seconds,omitempty"`
	// CheckedAt is the RFC3339 UTC time of the probe that produced this
	// verdict. For a stale fallback it is the time of the last good probe,
	// not the failed refresh, so "checked N ago" stays truthful.
	CheckedAt string `json:"checked_at"`
	// Stale is true when a refresh failed and we are serving an older
	// definitive verdict. Error then names the failure (unreachable /
	// rate_limited / unavailable / error).
	Stale bool   `json:"stale"`
	Error string `json:"error,omitempty"`
}

func responseFrom(o Output, at time.Time) Response {
	return Response{
		Result:             o.Result,
		Registered:         o.Registered,
		OwnerPresent:       o.OwnerPresent,
		Version:            o.Version,
		ResetUntil:         o.ResetUntil,
		LastSeenAt:         o.LastSeenAt,
		LastSeenAgeSeconds: o.LastSeenAgeSeconds,
		RetryAfterSeconds:  o.RetryAfterSeconds,
		CheckedAt:          at.UTC().Format(time.RFC3339),
	}
}

// DefaultArgv is the read-only, unprivileged command the prober runs. No
// sudo: apl-feed claim status reads the group-readable claim secret and
// makes a network probe — neither needs root, so this argv is NOT part of
// the sudoers/PrivilegedArgv parity contract.
var DefaultArgv = []string{"/usr/local/bin/apl-feed", "claim", "status", "--json"}

// DefaultProbeTimeout bounds a single CLI invocation. apl-feed's curl uses
// connect-timeout 10 + max-time 30, so the slowest legitimate probe is
// ~30s; 35s leaves slack without letting a hung child pin the single
// in-flight slot indefinitely.
const DefaultProbeTimeout = 35 * time.Second

// Prober runs the apl-feed claim status CLI and parses its output.
type Prober struct {
	Runner  wexec.CommandRunner
	Argv    []string
	Timeout time.Duration
}

// Probe runs the CLI and returns the parsed Output. A non-zero exit is
// expected for the transient results (apl-feed exits 2 on unreachable /
// error while still printing JSON), so the exit code is NOT consulted —
// the contract is "valid schema-v1 JSON on stdout". Anything else (old
// feed rejecting the subcommand, missing binary, crash, truncated output)
// yields ResultUnavailable plus a descriptive error for server-side logs.
func (p Prober) Probe(ctx context.Context) (Output, error) {
	argv := p.Argv
	if len(argv) == 0 {
		argv = DefaultArgv
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	res, runErr := p.Runner(cctx, argv)
	out, ok := parseOutput(res.Stdout)
	if !ok {
		return Output{Result: ResultUnavailable}, fmt.Errorf(
			"claim status probe: no schema-v1 JSON (exit=%d err=%v stderr=%q)",
			res.ExitCode, runErr, truncate(res.Stderr, 200))
	}
	return out, nil
}

// parseOutput decodes stdout and validates it is the schema-v1 contract.
// Returns ok=false for empty / non-JSON / wrong-schema / empty-result
// bodies so the caller maps them to ResultUnavailable.
func parseOutput(stdout []byte) (Output, bool) {
	trimmed := strings.TrimSpace(string(stdout))
	if trimmed == "" {
		return Output{}, false
	}
	var o Output
	if err := json.Unmarshal([]byte(trimmed), &o); err != nil {
		return Output{}, false
	}
	if o.SchemaVersion != 1 || o.Result == "" {
		return Output{}, false
	}
	return o, true
}

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ProbeFunc is the seam the Cache calls; production wires Prober.Probe,
// tests inject a canned function.
type ProbeFunc func(context.Context) (Output, error)

// Cache is a single-entry, single-flight cache over a ProbeFunc. It serves
// the last definitive verdict for the current identity within maxAge,
// coalesces concurrent refreshes into one probe, enforces a minimum probe
// interval (rate-limit safety), and falls back to the last good verdict
// when a refresh fails.
type Cache struct {
	probe        ProbeFunc
	now          func() time.Time
	floor        time.Duration
	probeTimeout time.Duration

	mu          sync.Mutex
	key         string
	good        *Response // last definitive verdict for key
	goodAt      time.Time
	last        *Response // last verdict served for key (definitive or fallback)
	nextProbeAt time.Time // earliest next probe for key (floor, or 429 retry_after)
	inflight    chan struct{}
	inflightKey string // identity the in-flight probe is for
}

// MinProbeInterval is the floor between probes for one identity. 7s caps
// the worst case near ~8/min — under the backend's 10/min sustained
// per-UUID budget even if a client hammers "Check now".
const MinProbeInterval = 7 * time.Second

// NewCache builds a Cache. now defaults to time.Now; floor defaults to
// MinProbeInterval.
func NewCache(probe ProbeFunc, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{
		probe:        probe,
		now:          now,
		floor:        MinProbeInterval,
		probeTimeout: DefaultProbeTimeout + 5*time.Second,
	}
}

// Get returns the claim-status verdict for the given identity key. maxAge
// is the caller's freshness tolerance; it is clamped up to the floor so a
// forced refresh (maxAge 0) still cannot probe more than once per floor
// for the same identity. A key change (identity import / rotation) resets
// the slot and bypasses the floor.
func (c *Cache) Get(ctx context.Context, key string, maxAge time.Duration) Response {
	eff := maxAge
	if eff < c.floor {
		eff = c.floor
	}

	c.mu.Lock()
	if c.key != key {
		// New identity: drop the prior slot entirely (the in-flight probe,
		// if any, belongs to the old identity and self-clears on finish).
		c.key = key
		c.good = nil
		c.last = nil
		c.goodAt = time.Time{}
		c.nextProbeAt = time.Time{}
	}
	// Fresh definitive verdict for this identity.
	if c.good != nil && c.now().Sub(c.goodAt) <= eff {
		r := *c.good
		c.mu.Unlock()
		return r
	}
	// Rate guard: a probe for this identity happened too recently (the 7s
	// floor, or the server's 429 retry_after). Serve best-available rather
	// than hit the rate-limited endpoint again.
	if !c.nextProbeAt.IsZero() && c.now().Before(c.nextProbeAt) {
		r := c.bestLocked()
		c.mu.Unlock()
		return r
	}
	// Coalesce only with an in-flight probe for the SAME identity — wait
	// for it, then serve what it stored. A probe for a different identity
	// is left to run concurrently (different UUID, separate rate bucket).
	if c.inflight != nil && c.inflightKey == key {
		ch := c.inflight
		c.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
		}
		c.mu.Lock()
		r := c.bestLocked()
		c.mu.Unlock()
		return r
	}
	ch := make(chan struct{})
	c.inflight = ch
	c.inflightKey = key
	c.mu.Unlock()

	// Detach the probe from the caller's request context: a single shared
	// probe backs every coalesced waiter, so one client disconnecting must
	// not cancel it. The prober applies its own timeout; this outer bound
	// is a backstop against a stub/probe that ignores ctx.
	probeCtx, cancel := context.WithTimeout(context.Background(), c.probeTimeout)
	out, err := c.probe(probeCtx)
	cancel()

	c.mu.Lock()
	defer func() {
		// Only clear the slot if it still points at OUR probe — a
		// concurrent different-identity probe may have replaced it.
		if c.inflight == ch {
			c.inflight = nil
		}
		close(ch)
		c.mu.Unlock()
	}()
	now := c.now()
	// A concurrent Get may have reset the slot to a different identity
	// while we were probing; don't store our (now-stale-identity) result.
	if c.key != key {
		return responseFrom(out, now)
	}
	// Schedule the next allowed probe for this identity: the floor, or the
	// server's retry_after when it rate-limited us.
	delay := c.floor
	if out.Result == ResultRateLimited && out.RetryAfterSeconds != nil {
		if ra := time.Duration(*out.RetryAfterSeconds) * time.Second; ra > delay {
			delay = ra
		}
	}
	c.nextProbeAt = now.Add(delay)
	resp := responseFrom(out, now)
	switch {
	case err == nil && isDefinitive(out.Result):
		c.good = &resp
		c.goodAt = now
		c.last = &resp
		return resp
	case c.good != nil:
		// Transient/unavailable refresh, but we have a prior good verdict:
		// keep showing it, flagged stale, with the failure reason and any
		// backoff hint carried over.
		fallback := *c.good
		fallback.Stale = true
		fallback.Error = out.Result
		fallback.RetryAfterSeconds = out.RetryAfterSeconds
		c.last = &fallback
		return fallback
	default:
		// No prior good verdict: surface the transient result as-is.
		c.last = &resp
		return resp
	}
}

// bestLocked returns the best currently-known verdict without probing.
// Caller must hold c.mu.
func (c *Cache) bestLocked() Response {
	if c.last != nil {
		return *c.last
	}
	if c.good != nil {
		return *c.good
	}
	return Response{Result: ResultUnavailable, CheckedAt: c.now().UTC().Format(time.RFC3339)}
}
