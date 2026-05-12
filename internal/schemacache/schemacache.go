// Package schemacache caches the feed.env schema published by
// `apl-feed schema --json`. The webconfig binary fetches it at boot,
// holds an atomic-swap copy in memory, and refreshes it on SIGHUP after
// `airplanes-update.service` runs (a feed update can introduce new keys
// or retire old ones; webconfig must not stale-cache the previous list).
//
// On boot-time fetch failure the cache enters degraded mode: the
// /api/config endpoint returns 503, but the rest of the webconfig
// surface (login, /api/update, /api/log, /api/reboot, status tiles)
// stays alive so an operator can still recover via the dashboard.
package schemacache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// schemaJSON mirrors the wire shape emitted by `apl-feed schema --json`.
// version=1: the writable+readable string sets are guaranteed; future
// versions add fields. Unknown future versions cause Load to fail loud
// so we don't silently lose schema info.
type schemaJSON struct {
	Version       int      `json:"version"`
	WritableKeys  []string `json:"writable_keys"`
	ReadableKeys  []string `json:"readable_keys"`
}

// Cache is goroutine-safe; a single instance is shared across handlers.
// The internal RWMutex serializes the rare SIGHUP-driven swap against
// the frequent reads from /api/config GET / POST.
type Cache struct {
	argv []string
	exec wexec.CommandRunner

	mu       sync.RWMutex
	writable map[string]struct{}
	readable map[string]struct{}
	degraded bool
}

// New constructs a Cache with the privileged argv used to fetch the
// schema. exec is the CommandRunner that actually executes the call —
// tests substitute their own.
func New(argv []string, exec wexec.CommandRunner) *Cache {
	return &Cache{
		argv: argv,
		exec: exec,
	}
}

// NewPrepopulated constructs a Cache already in the loaded state with
// the given writable/readable key sets. Intended for tests that don't
// want to exercise the apl-feed shellout path.
func NewPrepopulated(writable, readable []string) *Cache {
	c := &Cache{}
	w := make(map[string]struct{}, len(writable))
	for _, k := range writable {
		w[k] = struct{}{}
	}
	r := make(map[string]struct{}, len(readable))
	for _, k := range readable {
		r[k] = struct{}{}
	}
	c.writable = w
	c.readable = r
	return c
}

// Load fetches the schema once and atomically swaps it into the cache.
// On failure, returns the error and leaves any previously-cached schema
// in place. The first-boot failure path sets degraded=true so callers
// can render a 503.
//
// Callers: webconfig main goroutine at boot; SIGHUP handler after
// airplanes-update.service finishes.
func (c *Cache) Load(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	res, err := c.exec(cctx, c.argv)
	if err != nil {
		c.markDegradedIfEmpty()
		return fmt.Errorf("schema fetch: %w (stderr=%q exit=%d)", err, res.Stderr, res.ExitCode)
	}

	var doc schemaJSON
	if err := json.Unmarshal(res.Stdout, &doc); err != nil {
		c.markDegradedIfEmpty()
		return fmt.Errorf("schema parse: %w (body=%q)", err, res.Stdout)
	}
	if doc.Version != 1 {
		c.markDegradedIfEmpty()
		return fmt.Errorf("schema version: unsupported %d (expected 1)", doc.Version)
	}
	if len(doc.WritableKeys) == 0 || len(doc.ReadableKeys) == 0 {
		c.markDegradedIfEmpty()
		return errors.New("schema: writable_keys or readable_keys is empty")
	}

	writable := make(map[string]struct{}, len(doc.WritableKeys))
	for _, k := range doc.WritableKeys {
		writable[k] = struct{}{}
	}
	readable := make(map[string]struct{}, len(doc.ReadableKeys))
	for _, k := range doc.ReadableKeys {
		readable[k] = struct{}{}
	}

	c.mu.Lock()
	c.writable = writable
	c.readable = readable
	c.degraded = false
	c.mu.Unlock()
	return nil
}

func (c *Cache) markDegradedIfEmpty() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.writable) == 0 || len(c.readable) == 0 {
		c.degraded = true
	}
}

// IsWritable reports whether the given key was in the schema's
// writable_keys at the time of the last successful Load.
func (c *Cache) IsWritable(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.writable[key]
	return ok
}

// IsReadable reports whether the given key was in the schema's
// readable_keys. Used to filter GET /api/config so the API never leaks
// keys the schema doesn't acknowledge (e.g. legacy migration debris).
func (c *Cache) IsReadable(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.readable[key]
	return ok
}

// WritableKeys returns a copy of the writable-key set. Used for tests
// and any caller that needs to iterate (rare — IsWritable is the hot
// path).
func (c *Cache) WritableKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.writable))
	for k := range c.writable {
		keys = append(keys, k)
	}
	return keys
}

// Degraded reports whether the cache is in degraded mode — i.e. no
// successful Load has happened. Handlers gate /api/config behind this
// and return 503 when true.
func (c *Cache) Degraded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.degraded
}
