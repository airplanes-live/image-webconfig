// Package status assembles the GET /api/status payload: per-service
// active/inactive state, the last-seen aircraft snapshot from
// /run/readsb/aircraft.json, and the build manifest at
// /etc/airplanes/build-manifest.json. Only local sources — no network
// calls — so /api/status stays cheap to poll.
package status

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"
	"sync"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// MonitoredServices is the static list every /api/status query reports on.
var MonitoredServices = []string{
	"airplanes-feed.service",
	"airplanes-mlat.service",
	"readsb.service",
	"dump978-fa.service",
	"airplanes-978.service",
	"lighttpd.service",
	"airplanes-webconfig.service",
}

// Paths configures file lookups; defaults match the rootfs layout. Override
// in tests.
type Paths struct {
	ManifestFile        string // /etc/airplanes/build-manifest.json
	AircraftJSONFile    string // /run/readsb/aircraft.json
	SystemctlSudoArgv0  []string // [sudo, -n, ...] OR [systemctl] (no sudo: is-active is read-only)
	SystemctlBinary     string   // /usr/bin/systemctl
	IsActiveTimeout     time.Duration
}

func DefaultPaths() Paths {
	return Paths{
		ManifestFile:     "/etc/airplanes/build-manifest.json",
		AircraftJSONFile: "/run/readsb/aircraft.json",
		SystemctlBinary:  "/usr/bin/systemctl",
		IsActiveTimeout:  2 * time.Second,
	}
}

// Reader assembles the status payload by sharding work across goroutines:
// each is-active call runs in its own goroutine so a single 2s timeout
// caps total latency at 2s regardless of service count.
type Reader struct {
	paths   Paths
	runner  wexec.CommandRunner
	version string
}

func NewReader(version string, p Paths, r wexec.CommandRunner) *Reader {
	if r == nil {
		r = wexec.RealRunner
	}
	return &Reader{paths: p, runner: r, version: version}
}

// Status is the GET /api/status payload.
type Status struct {
	Version  string             `json:"webconfig_version"`
	Services map[string]string  `json:"services"` // unit → "active" / "inactive" / "unknown"
	Manifest json.RawMessage    `json:"manifest,omitempty"`
	Feed     *FeedStats         `json:"feed,omitempty"`
}

// FeedStats summarizes /run/readsb/aircraft.json.
type FeedStats struct {
	NowSeconds      float64 `json:"now"`              // readsb's `now` field
	MessagesCounter int64   `json:"messages_counter"` // readsb's `messages` field
	AircraftCount   int     `json:"aircraft_count"`
}

// Read assembles the status payload. Failures in one source don't fail the
// whole call — services are individually labeled "unknown" on timeout, the
// manifest is omitted on read error, and the feed snapshot is omitted on
// missing/unparseable file.
func (r *Reader) Read(ctx context.Context) (Status, error) {
	out := Status{
		Version:  r.version,
		Services: make(map[string]string, len(MonitoredServices)),
	}

	// Concurrent systemctl is-active.
	type svcResult struct {
		unit  string
		state string
	}
	ch := make(chan svcResult, len(MonitoredServices))
	var wg sync.WaitGroup
	for _, unit := range MonitoredServices {
		wg.Add(1)
		go func(unit string) {
			defer wg.Done()
			ch <- svcResult{unit, r.isActive(ctx, unit)}
		}(unit)
	}
	wg.Wait()
	close(ch)
	for s := range ch {
		out.Services[s.unit] = s.state
	}

	if m, err := os.ReadFile(r.paths.ManifestFile); err == nil {
		// Validate JSON shape so we never embed garbage in the response.
		var dummy json.RawMessage
		if json.Unmarshal(m, &dummy) == nil {
			out.Manifest = m
		}
	}

	if fs, err := readAircraftJSON(r.paths.AircraftJSONFile); err == nil {
		out.Feed = fs
	}

	return out, nil
}

func (r *Reader) isActive(ctx context.Context, unit string) string {
	cctx, cancel := context.WithTimeout(ctx, r.paths.IsActiveTimeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.paths.SystemctlBinary, "is-active", unit})
	if err != nil {
		// systemctl is-active exits 3 when not active; that's not a
		// runtime error, just an answer.
		state := strings.TrimSpace(string(res.Stdout))
		if state != "" {
			return state
		}
		return "unknown"
	}
	return strings.TrimSpace(string(res.Stdout))
}

// readsbAircraftJSON is the relevant subset of readsb's aircraft.json schema.
type readsbAircraftJSON struct {
	Now      float64 `json:"now"`
	Messages int64   `json:"messages"`
	Aircraft []struct{} `json:"aircraft"`
}

func readAircraftJSON(path string) (*FeedStats, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var raw readsbAircraftJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	return &FeedStats{
		NowSeconds:      raw.Now,
		MessagesCounter: raw.Messages,
		AircraftCount:   len(raw.Aircraft),
	}, nil
}
