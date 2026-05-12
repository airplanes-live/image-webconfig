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
	"github.com/airplanes-live/image/webconfig/internal/runtimestate"
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
	MlatStateFile       string // /run/airplanes-mlat/state
	FeedStateFile       string // /run/airplanes-feed/state
	UAT978StateFile     string // /run/airplanes-978/state
	Dump978FAStateFile  string // /run/dump978-fa/state
	SystemctlSudoArgv0  []string // [sudo, -n, ...] OR [systemctl] (no sudo: is-active is read-only)
	SystemctlBinary     string   // /usr/bin/systemctl
	IsActiveTimeout     time.Duration
}

func DefaultPaths() Paths {
	return Paths{
		ManifestFile:       "/etc/airplanes/build-manifest.json",
		AircraftJSONFile:   "/run/readsb/aircraft.json",
		MlatStateFile:      "/run/airplanes-mlat/state",
		FeedStateFile:      "/run/airplanes-feed/state",
		UAT978StateFile:    "/run/airplanes-978/state",
		Dump978FAStateFile: "/run/dump978-fa/state",
		SystemctlBinary:    "/usr/bin/systemctl",
		IsActiveTimeout:    2 * time.Second,
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
	Version  string            `json:"webconfig_version"`
	Services map[string]string `json:"services"` // unit → "active" / "inactive" / "unknown"
	Manifest json.RawMessage   `json:"manifest,omitempty"`
	Feed     *FeedStats        `json:"feed,omitempty"`
	// Decisions are the daemons' published config-classifications, read
	// from /run/<service>/state. omitempty so an old daemon (pre PR 1
	// in feed, pre PR 4 in image for UAT) without a state file doesn't
	// break the JSON shape — clients fall back to the service active-state
	// classification (or, for UAT, to UAT_INPUT-truthy from /api/config).
	//
	// UATDecision is airplanes-978.sh's publication (the consumer side of
	// the 978 family). Dump978FADecision is dump978-fa.sh's publication
	// (the producer side, now hardware-aware via its USB-serial probe).
	// Frontend consumers render the tiles independently; the producer's
	// no_hardware decision propagates into UATDecision.Reason as
	// peer_no_hardware so the airplanes-978 tile can render an "idle relay"
	// state without polling two endpoints.
	MlatDecision     *Decision `json:"mlat_decision,omitempty"`
	FeedDecision     *Decision `json:"feed_decision,omitempty"`
	UATDecision      *Decision `json:"uat_decision,omitempty"`
	Dump978FADecision *Decision `json:"dump978fa_decision,omitempty"`
}

// Decision is the daemon's last-published runtime decision.
// State is one of "enabled" | "disabled" | "misconfigured" (from
// runtimestate.AllowedDecisions; unknown tokens are filtered out).
// Reason is a stable token specific to the state; UI text is the
// consumer's responsibility.
type Decision struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
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

	out.MlatDecision = r.readMlatDecision(ctx, out.Services["airplanes-mlat.service"])
	out.FeedDecision = r.readFeedDecision(out.Services["airplanes-feed.service"])
	out.UATDecision = r.readUATDecision(ctx, out.Services["airplanes-978.service"])
	out.Dump978FADecision = r.readDump978FADecision(ctx, out.Services["dump978-fa.service"])

	return out, nil
}

// readMlatDecision reads the airplanes-mlat daemon's published decision.
// Consulted when the unit is active|activating|reloading (state file is
// fresh) OR failed with ExecMainStatus=64 (the strict misconfig fail; the
// state file persists across the failed terminal state via
// RuntimeDirectoryPreserve=yes per feed PR 1+2). Returns nil for any
// other state (consumer falls back to systemd-only classification).
func (r *Reader) readMlatDecision(ctx context.Context, svcState string) *Decision {
	consult := false
	switch svcState {
	case "active", "activating", "reloading":
		consult = true
	case "failed":
		if r.execMainStatus(ctx, "airplanes-mlat.service") == "64" {
			consult = true
		}
	}
	if !consult {
		return nil
	}
	return decisionFromFile(r.paths.MlatStateFile, runtimestate.AllowedReasonsMLAT)
}

// readFeedDecision: simpler than mlat; no exit-64 special case (feed
// daemon has no strict-misconfig path). Only consulted for active /
// transitioning states.
func (r *Reader) readFeedDecision(svcState string) *Decision {
	switch svcState {
	case "active", "activating", "reloading":
		return decisionFromFile(r.paths.FeedStateFile, runtimestate.AllowedReasonsFeed)
	}
	return nil
}

// readUATDecision reads the airplanes-978 daemon's published decision.
// Same consultation rule as MLAT (active|activating|reloading OR failed
// with ExecMainStatus=64): when UAT_INPUT is empty/invalid, the wrapper
// writes state=disabled or state=misconfigured then exits 64, and
// RuntimeDirectoryPreserve=yes keeps the file across the failed terminal
// state. Returns nil otherwise so consumers fall back to UAT_INPUT-truthy
// classification (the prior behavior).
func (r *Reader) readUATDecision(ctx context.Context, svcState string) *Decision {
	consult := false
	switch svcState {
	case "active", "activating", "reloading":
		consult = true
	case "failed":
		if r.execMainStatus(ctx, "airplanes-978.service") == "64" {
			consult = true
		}
	}
	if !consult {
		return nil
	}
	return decisionFromFile(r.paths.UAT978StateFile, runtimestate.AllowedReasons978)
}

// readDump978FADecision reads dump978-fa.sh's published decision. Same
// consult rule as the other 64-exit self-disable wrappers. The
// no_hardware reason is the new state-machine entry from the wrapper's
// USB-serial probe — dashboards should render it as an "SDR absent" tile
// rather than a generic failure.
func (r *Reader) readDump978FADecision(ctx context.Context, svcState string) *Decision {
	consult := false
	switch svcState {
	case "active", "activating", "reloading":
		consult = true
	case "failed":
		if r.execMainStatus(ctx, "dump978-fa.service") == "64" {
			consult = true
		}
	}
	if !consult {
		return nil
	}
	return decisionFromFile(r.paths.Dump978FAStateFile, runtimestate.AllowedReasonsDump978FA)
}

// decisionFromFile reads, validates, and returns the Decision encoded in a
// runtime state file. allowedReasons gates the reason vocabulary per
// daemon-owner so a malformed 978 file claiming an MLAT-only reason is
// dropped rather than passed through.
func decisionFromFile(path string, allowedReasons map[string]bool) *Decision {
	rs, err := runtimestate.Read(path)
	if err != nil {
		return nil
	}
	state := rs.Values["state"]
	if !runtimestate.AllowedDecisions[state] {
		return nil
	}
	reason := rs.Values["reason"]
	if !allowedReasons[reason] {
		return nil
	}
	return &Decision{State: state, Reason: reason}
}

// execMainStatus runs `systemctl show --property=ExecMainStatus --value <unit>`
// and returns the trimmed value, or empty string on any error / timeout.
// Bounded by IsActiveTimeout to match the existing per-call budget.
func (r *Reader) execMainStatus(ctx context.Context, unit string) string {
	cctx, cancel := context.WithTimeout(ctx, r.paths.IsActiveTimeout)
	defer cancel()
	res, err := r.runner(cctx, []string{
		r.paths.SystemctlBinary,
		"show",
		"--property=ExecMainStatus",
		"--value",
		unit,
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(res.Stdout))
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
