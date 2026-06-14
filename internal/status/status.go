// Package status assembles the GET /api/status payload: per-service
// active/inactive state, the last-seen aircraft snapshot from
// /run/readsb/aircraft.json, and two build manifests — the immutable
// image manifest at /etc/airplanes/build-manifest.json (flash-time
// provenance) and the runtime-overlay manifest at
// /etc/airplanes/runtime-manifest.json (the live component versions,
// which advance on every overlay update). Only local sources — no
// network calls — so /api/status stays cheap to poll.
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

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/hardware"
	"github.com/airplanes-live/image-webconfig/internal/runtimestate"
	"github.com/airplanes-live/image-webconfig/internal/wifi"
)

// HardwareProbe is the interface status.Reader uses to fetch a hardware
// snapshot. Concrete production type is *hardware.Reader; tests inject
// a stub.
type HardwareProbe interface {
	Probe(ctx context.Context) *hardware.Snapshot
}

// WifiProbe is the interface status.Reader uses to fetch WiFi signal
// state. Concrete production type is *wifi.SignalReader; tests inject a
// stub.
type WifiProbe interface {
	Probe(ctx context.Context) *wifi.Signal
}

// Option configures optional Reader behavior. Keeps NewReader's 3-arg
// signature stable so existing callers compile unchanged.
type Option func(*Reader)

// WithHardware wires a HardwareProbe into the Reader. When set, Read()
// runs the probe in parallel with the systemctl fan-out and embeds the
// result as Status.{PiThrottle, System, HardwareHealth}.
func WithHardware(p HardwareProbe) Option {
	return func(r *Reader) { r.hardware = p }
}

// WithWifi wires a WifiProbe into the Reader. When set, Read() runs the
// probe in parallel with the other goroutines and embeds the result as
// Status.Wifi. A nil probe result yields an omitted field (the frontend
// hides its tile entirely).
func WithWifi(p WifiProbe) Option {
	return func(r *Reader) { r.wifi = p }
}

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
	ImageManifestFile   string   // /etc/airplanes/build-manifest.json (flash-time, immutable)
	RuntimeManifestFile string   // /etc/airplanes/runtime-manifest.json (live overlay, advances on update)
	AircraftJSONFile    string   // /run/readsb/aircraft.json
	MlatStateFile       string   // /run/airplanes-mlat/state
	FeedStateFile       string   // /run/airplanes-feed/state
	UAT978StateFile     string   // /run/airplanes-978/state
	Dump978FAStateFile  string   // /run/dump978-fa/state
	RebootRequiredFile  string   // /var/run/reboot-required (written by needrestart after kernel/libc upgrades)
	SystemctlSudoArgv0  []string // [sudo, -n, ...] OR [systemctl] (no sudo: is-active is read-only)
	SystemctlBinary     string   // /usr/bin/systemctl
	IsActiveTimeout     time.Duration
}

func DefaultPaths() Paths {
	return Paths{
		ImageManifestFile:   "/etc/airplanes/build-manifest.json",
		RuntimeManifestFile: "/etc/airplanes/runtime-manifest.json",
		AircraftJSONFile:    "/run/readsb/aircraft.json",
		MlatStateFile:       "/run/airplanes-mlat/state",
		FeedStateFile:       "/run/airplanes-feed/state",
		UAT978StateFile:     "/run/airplanes-978/state",
		Dump978FAStateFile:  "/run/dump978-fa/state",
		RebootRequiredFile:  "/var/run/reboot-required",
		SystemctlBinary:     "/usr/bin/systemctl",
		IsActiveTimeout:     2 * time.Second,
	}
}

// Reader assembles the status payload by sharding work across goroutines:
// each is-active call runs in its own goroutine so a single 2s timeout
// caps total latency at 2s regardless of service count.
type Reader struct {
	paths    Paths
	runner   wexec.CommandRunner
	version  string
	hardware HardwareProbe
	wifi     WifiProbe
}

func NewReader(version string, p Paths, r wexec.CommandRunner, opts ...Option) *Reader {
	if r == nil {
		r = wexec.RealRunner
	}
	rr := &Reader{paths: p, runner: r, version: version}
	for _, o := range opts {
		o(rr)
	}
	return rr
}

// Status is the GET /api/status payload.
type Status struct {
	Version  string            `json:"webconfig_version"`
	Services map[string]string `json:"services"` // unit → "active" / "inactive" / "unknown"
	// ImageManifest is the immutable flash-time manifest (pi_gen,
	// build_timestamp, baked component SHAs). RuntimeManifest is the live
	// overlay manifest whose component versions advance on every overlay
	// update — the field a "what am I running" check should read. Both are
	// passed through raw; either is omitted on a read/parse error.
	ImageManifest   json.RawMessage `json:"image_manifest,omitempty"`
	RuntimeManifest json.RawMessage `json:"runtime_manifest,omitempty"`
	Feed            *FeedStats      `json:"feed,omitempty"`
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
	MlatDecision      *Decision `json:"mlat_decision,omitempty"`
	FeedDecision      *Decision `json:"feed_decision,omitempty"`
	UATDecision       *Decision `json:"uat_decision,omitempty"`
	Dump978FADecision *Decision `json:"dump978fa_decision,omitempty"`

	// Hardware-health surface. Three top-level keys mirror the
	// airplanes-live/website feeder-diagnostics split:
	//   - pi_throttle: Pi-only, the 8 vcgencmd get_throttled bits +
	//     optional PSU enrichment. Omitted on non-Pi or throttle probe
	//     failure.
	//   - system: universal, always present (emits at least `{}` even
	//     when no probe is wired). Per-sub-probe success carried by
	//     pointer-omitempty on each field.
	//   - hardware_health: local-only rollup the SPA tile and
	//     --hardware CLI consume. Omitted when no HardwareProbe is
	//     wired (test-only path).
	PiThrottle     *hardware.Throttle `json:"pi_throttle,omitempty"`
	System         hardware.System    `json:"system"`
	HardwareHealth *hardware.Health   `json:"hardware_health,omitempty"`

	// Wifi is the live signal snapshot. omitempty when there is no WiFi
	// hardware (or the probe wasn't configured) so the frontend can hide
	// its tile rather than show a misleading "—".
	Wifi *wifi.Signal `json:"wifi,omitempty"`

	// RebootRequired is true when /var/run/reboot-required exists, which
	// needrestart writes after a kernel or libc upgrade. The dashboard
	// renders a banner when this flips on.
	RebootRequired bool `json:"reboot_required"`
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

	// Hardware probe runs in parallel with the systemctl fan-out. The
	// probe is bounded by its own per-sub-probe timeouts inside the
	// hardware package; we just collect its result.
	var hwWG sync.WaitGroup
	var hwResult *hardware.Snapshot
	if r.hardware != nil {
		hwWG.Add(1)
		go func() {
			defer hwWG.Done()
			hwResult = r.hardware.Probe(ctx)
		}()
	}

	// WiFi signal probe runs alongside the others. Internal timeouts in
	// the wifi package bound wall-clock; nil result is the normal case
	// (no hardware) and just means the field is omitted from the payload.
	var wifiWG sync.WaitGroup
	var wifiResult *wifi.Signal
	if r.wifi != nil {
		wifiWG.Add(1)
		go func() {
			defer wifiWG.Done()
			wifiResult = r.wifi.Probe(ctx)
		}()
	}

	wg.Wait()
	close(ch)
	for s := range ch {
		out.Services[s.unit] = s.state
	}

	hwWG.Wait()
	if hwResult != nil {
		out.PiThrottle = hwResult.PiThrottle
		out.System = hwResult.System
		out.HardwareHealth = &hwResult.Health
	}

	wifiWG.Wait()
	out.Wifi = wifiResult

	out.ImageManifest = readRawManifest(r.paths.ImageManifestFile)
	out.RuntimeManifest = readRawManifest(r.paths.RuntimeManifestFile)

	if fs, err := readAircraftJSON(r.paths.AircraftJSONFile); err == nil {
		out.Feed = fs
	}

	out.MlatDecision = r.readMlatDecision(ctx, out.Services["airplanes-mlat.service"])
	out.FeedDecision = r.readFeedDecision(out.Services["airplanes-feed.service"])
	out.UATDecision = r.readUATDecision(ctx, out.Services["airplanes-978.service"])
	out.Dump978FADecision = r.readDump978FADecision(ctx, out.Services["dump978-fa.service"])

	if r.paths.RebootRequiredFile != "" {
		if _, err := os.Stat(r.paths.RebootRequiredFile); err == nil {
			out.RebootRequired = true
		}
	}

	return out, nil
}

// readMlatDecision reads the airplanes-mlat daemon's published decision.
// Consulted when the unit is active|activating|reloading (state file is
// fresh) OR failed with ExecMainStatus=64 (the strict-misconfig branch
// in the wrapper exits 64; RuntimeDirectoryPreserve=yes keeps the
// state file alive across that failure per feed PR 1+2). Returns nil
// for any other state (consumer falls back to systemd-only classification).
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
// with ExecMainStatus=64): the uat_disabled wrapper branch writes
// state=disabled then sleeps (unit stays active), so we hit the active
// branch in normal use; the uat_input_invalid branch exits 64 and
// surfaces via the failed branch. Returns nil otherwise so consumers
// fall back to UAT_INPUT-truthy classification (the prior behavior).
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
// consult rule as the other wrappers (active branch covers the
// disabled/sleeping path; failed-with-status-64 covers misconfigured
// input). The no_hardware reason comes from the wrapper's non-mutating
// USB-serial probe — dashboards should render it as an "SDR absent"
// tile rather than a generic failure.
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
	Now      float64    `json:"now"`
	Messages int64      `json:"messages"`
	Aircraft []struct{} `json:"aircraft"`
}

// readRawManifest returns a manifest file's bytes only if they parse as a
// JSON object, so /api/status never embeds garbage or a bare null/array/
// scalar (which would surface as e.g. `"runtime_manifest": null`). Returns
// nil on a missing or non-object file (the corresponding field is then
// omitted). The runtime manifest path is a symlink into the active overlay;
// os.ReadFile follows it to the current target.
func readRawManifest(path string) json.RawMessage {
	m, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	// Unmarshalling into a map rejects arrays and scalars; the explicit nil
	// check rejects a literal `null` (which decodes into a nil map without
	// error). Only a JSON object survives.
	var obj map[string]json.RawMessage
	if json.Unmarshal(m, &obj) != nil || obj == nil {
		return nil
	}
	return m
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
