// Package hardware probes local hardware-health signals for the webconfig
// dashboard. Sources are local — vcgencmd, the /sys/class/thermal sysfs
// file, /proc/{meminfo,uptime}, timedatectl, and a statfs of the root
// mount point.
//
// The wire surface mirrors the airplanes-live/website feeder-diagnostics
// split: Pi-only signals live under `pi_throttle` (the eight
// `vcgencmd get_throttled` bits), universal signals under `system` (CPU
// temp, NTP, memory, disk, uptime). A local-only `hardware_health`
// rollup carries the aggregated severity / summary the dashboard tile
// and the --hardware CLI consume.
//
// Sub-probes are independent: a failure in one (e.g. vcgencmd
// unavailable) does not suppress others. Per-sub-probe success is
// carried by pointer-omitempty on each field — a nil pointer means the
// probe didn't run; a present value means it did, including the
// dangerous 0% memory/disk cases.
package hardware

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// Throttle bit positions from the Raspberry Pi docs. "Now" bits live in
// the low nibble; the matching "since-boot" bits are 16 positions up.
// Naming follows the JSON wire tags (freq_capped / throttled), not the
// historic vcgencmd field labels (arm_freq_cap / throttling).
const (
	bitUndervoltageNow   uint32 = 1 << 0
	bitFreqCappedNow     uint32 = 1 << 1
	bitThrottledNow      uint32 = 1 << 2
	bitSoftTempLimitNow  uint32 = 1 << 3
	bitUndervoltageEver  uint32 = 1 << 16
	bitFreqCappedEver    uint32 = 1 << 17
	bitThrottledEver     uint32 = 1 << 18
	bitSoftTempLimitEver uint32 = 1 << 19
)

// Paths configures file lookups. Defaults match the rootfs layout; tests
// override.
type Paths struct {
	VcgencmdBinary    string
	TimedatectlBinary string
	ThermalZoneFile   string
	MeminfoFile       string
	UptimeFile        string
	DeviceTreeModel   string
	RootMountPoint    string
	ProbeTimeout      time.Duration
}

func DefaultPaths() Paths {
	return Paths{
		VcgencmdBinary:    "/usr/bin/vcgencmd",
		TimedatectlBinary: "/usr/bin/timedatectl",
		ThermalZoneFile:   "/sys/class/thermal/thermal_zone0/temp",
		MeminfoFile:       "/proc/meminfo",
		UptimeFile:        "/proc/uptime",
		DeviceTreeModel:   "/sys/firmware/devicetree/base/model",
		RootMountPoint:    "/",
		ProbeTimeout:      2 * time.Second,
	}
}

// DiskProber is the disk-free-percent probe. Injectable because
// syscall.Statfs against CI tmpdirs flakes; tests pass a stub.
type DiskProber func(path string) (freePct float64, err error)

// statfsProber is the production DiskProber: a real syscall.Statfs against
// the given mount point.
func statfsProber(path string) (float64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	if s.Blocks == 0 {
		return 0, errors.New("hardware: statfs reported zero blocks")
	}
	return float64(s.Bavail) / float64(s.Blocks) * 100, nil
}

// Reader runs the probes and aggregates a Snapshot.
type Reader struct {
	paths      Paths
	thresholds Thresholds
	runner     wexec.CommandRunner
	diskProber DiskProber
}

// NewReader constructs a Reader. A nil runner defaults to wexec.RealRunner;
// a nil diskProber defaults to syscall.Statfs against Paths.RootMountPoint.
func NewReader(p Paths, t Thresholds, r wexec.CommandRunner, dp DiskProber) *Reader {
	if r == nil {
		r = wexec.RealRunner
	}
	if dp == nil {
		dp = statfsProber
	}
	if p.ProbeTimeout <= 0 {
		p.ProbeTimeout = 2 * time.Second
	}
	return &Reader{paths: p, thresholds: t, runner: r, diskProber: dp}
}

// Throttle is the Pi-only `vcgencmd get_throttled` decode plus optional
// PSU enrichment (Pi 5 / CM5). The eight bool keys match the
// airplanes-live/website pi_throttle contract verbatim; PSU pointer
// fields are a webconfig-local extension used only by the local
// dashboard's undervoltage-blurb enrichment.
//
// Emitted in /api/status.pi_throttle only when the Pi was detected
// AND get_throttled produced parseable output — presence of the key
// is the signal, so there is no `probed` field.
type Throttle struct {
	UndervoltageNow   bool `json:"undervoltage_now"`
	UndervoltageEver  bool `json:"undervoltage_ever"`
	FreqCappedNow     bool `json:"freq_capped_now"`
	FreqCappedEver    bool `json:"freq_capped_ever"`
	ThrottledNow      bool `json:"throttled_now"`
	ThrottledEver     bool `json:"throttled_ever"`
	SoftTempLimitNow  bool `json:"soft_temp_limit_now"`
	SoftTempLimitEver bool `json:"soft_temp_limit_ever"`

	// PSU pointer-omitempty: nil = vcgencmd get_config psu_max_current
	// unread; present = firmware-reported. Expected current only
	// serialized when the actual was read so a Pi 4 doesn't emit a
	// phantom 3000 mA expectation against no measurement.
	PSUMaxCurrentMA *int `json:"psu_max_current_ma,omitempty"`
	PSUExpectedMA   *int `json:"psu_expected_ma,omitempty"`
}

// System is the universal hardware-health snapshot. Always present in
// /api/status (the wire emits at least `system: {}` on probe-less
// configurations). Per-sub-probe success is carried by
// pointer-omitempty: a nil field = sub-probe didn't run; a non-nil
// field = probed (including 0% memory/disk free, which is a real and
// dangerous value worth showing).
type System struct {
	CPUTempCelsius  *float64 `json:"cpu_temp_c,omitempty"`
	NTPSynchronized *bool    `json:"ntp_synchronized,omitempty"`
	UptimeSeconds   *float64 `json:"uptime_s,omitempty"`
	MemoryAvailPct  *float64 `json:"mem_avail_pct,omitempty"`
	DiskFreePct     *float64 `json:"disk_free_pct,omitempty"`
}

// Health is the aggregator rollup over Throttle + System. Local-only —
// only the webconfig's SPA and --hardware CLI consume it. Summary is
// UTF-8 (e.g. "healthy · 56°C"); the CLI handler folds to ASCII via
// asciiSafe before emitting on stdout.
type Health struct {
	Severity      string `json:"severity"` // "na" | "ok" | "warn" | "err"
	Summary       string `json:"summary"`
	IsRaspberryPi bool   `json:"is_raspberry_pi"`
}

// Snapshot is the bundle Reader.Probe returns. A typed struct so callers
// (status.Reader, devfakes, tests) can't reorder fields by mistake
// and so nil/default semantics are explicit at the field level.
type Snapshot struct {
	PiThrottle *Throttle
	System     System
	Health     Health
}

// Probe runs every sub-probe and returns the aggregated Snapshot. Never
// returns nil; callers can inspect Snapshot.PiThrottle == nil to learn
// the device wasn't a Pi (or the throttle probe failed).
func (r *Reader) Probe(ctx context.Context) *Snapshot {
	snap := &Snapshot{}

	// The model file is consumed by two probes: IsRaspberryPi detection
	// and the PSU family-expectation lookup. Read once and pass through.
	var model []byte
	if b, err := os.ReadFile(r.paths.DeviceTreeModel); err == nil {
		model = b
	}
	isPi := isRaspberryPi(model)

	if isPi {
		if t := r.probeThrottle(ctx); t != nil {
			r.probePSU(ctx, t, model)
			snap.PiThrottle = t
		}
	}

	snap.System = r.probeSystem(ctx)
	snap.Health = *Summarize(snap.PiThrottle, &snap.System, r.thresholds, isPi)
	return snap
}

// probeThrottle returns a populated *Throttle on success, nil if
// vcgencmd is missing or its output is unparseable. PSU fields are
// left nil here — probePSU fills them when the firmware exposes
// psu_max_current.
func (r *Reader) probeThrottle(ctx context.Context) *Throttle {
	cctx, cancel := context.WithTimeout(ctx, r.paths.ProbeTimeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.paths.VcgencmdBinary, "get_throttled"})
	if err != nil {
		return nil
	}
	bits, ok := parseThrottled(string(res.Stdout))
	if !ok {
		return nil
	}
	return &Throttle{
		UndervoltageNow:   bits&bitUndervoltageNow != 0,
		UndervoltageEver:  bits&bitUndervoltageEver != 0,
		FreqCappedNow:     bits&bitFreqCappedNow != 0,
		FreqCappedEver:    bits&bitFreqCappedEver != 0,
		ThrottledNow:      bits&bitThrottledNow != 0,
		ThrottledEver:     bits&bitThrottledEver != 0,
		SoftTempLimitNow:  bits&bitSoftTempLimitNow != 0,
		SoftTempLimitEver: bits&bitSoftTempLimitEver != 0,
	}
}

// probePSU runs `vcgencmd get_config psu_max_current` to discover the
// PSU rating the firmware negotiated (Pi 5 / CM5 only — older boards
// don't expose this key and vcgencmd either errors or returns a zero
// value, both treated as "not probed").
//
// Nil-safe in the throttle pointer: callers may invoke this when the
// throttle probe failed; we short-circuit and don't allocate phantom
// PSU enrichment unattached to a Throttle.
//
// Expected current is only set when the actual was read, so a Pi 4
// doesn't emit a phantom 3000 mA expectation against no measurement.
func (r *Reader) probePSU(ctx context.Context, t *Throttle, model []byte) {
	if t == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, r.paths.ProbeTimeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.paths.VcgencmdBinary, "get_config", "psu_max_current"})
	if err != nil {
		return
	}
	mA, ok := parsePSUMaxCurrent(string(res.Stdout))
	if !ok {
		return
	}
	t.PSUMaxCurrentMA = intPtr(mA)
	if exp := expectedPSUMaxCurrentMA(model); exp > 0 {
		t.PSUExpectedMA = intPtr(exp)
	}
}

// probeSystem runs the universal sub-probes (CPU temp, time, memory,
// disk) and returns a populated System. Sub-probe failure leaves the
// corresponding pointer field nil; success populates it via the
// fooPtr helpers so a zero value (e.g. 0% disk free) is preserved.
func (r *Reader) probeSystem(ctx context.Context) System {
	var s System
	r.probeTemp(&s)
	r.probeTime(ctx, &s)
	r.probeMem(&s)
	r.probeDisk(&s)
	return s
}

func (r *Reader) probeTemp(s *System) {
	b, err := os.ReadFile(r.paths.ThermalZoneFile)
	if err != nil {
		return
	}
	c, ok := parseSysfsTempMilliC(b)
	if !ok {
		return
	}
	s.CPUTempCelsius = float64Ptr(c)
}

func (r *Reader) probeTime(ctx context.Context, s *System) {
	cctx, cancel := context.WithTimeout(ctx, r.paths.ProbeTimeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.paths.TimedatectlBinary, "show"})
	if err != nil {
		return
	}
	synced, found := parseTimedatectlShow(string(res.Stdout))
	if !found {
		return
	}
	s.NTPSynchronized = boolPtr(synced)
	if b, err := os.ReadFile(r.paths.UptimeFile); err == nil {
		if u, ok := parseUptime(b); ok {
			s.UptimeSeconds = float64Ptr(u)
		}
	}
}

func (r *Reader) probeMem(s *System) {
	b, err := os.ReadFile(r.paths.MeminfoFile)
	if err != nil {
		return
	}
	pct, ok := parseMeminfo(b)
	if !ok {
		return
	}
	s.MemoryAvailPct = float64Ptr(pct)
}

func (r *Reader) probeDisk(s *System) {
	pct, err := r.diskProber(r.paths.RootMountPoint)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		return
	}
	s.DiskFreePct = float64Ptr(pct)
}

// Pointer-omitempty helpers. Small, ubiquitous, and avoid the
// `var x = v; field = &x` boilerplate at every call site.
func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }
func boolPtr(v bool) *bool          { return &v }

// MarshalJSON guarantees System emits at least `{}` when every
// sub-probe is nil, so the wire matches the universal-always
// contract. Default Go marshaling of a value-typed struct already
// does this; this method exists to make the contract visible and
// to anchor tests that assert on it.
func (s System) MarshalJSON() ([]byte, error) {
	type alias System
	return json.Marshal(alias(s))
}

// severity ranks. Summarize collects (rank, blurb) pairs and emits the
// max rank + top-3 blurbs by priority order.
const (
	sevNA   = 0
	sevOK   = 1
	sevWarn = 2
	sevErr  = 3
)

var sevName = map[int]string{
	sevNA:   "na",
	sevOK:   "ok",
	sevWarn: "warn",
	sevErr:  "err",
}

// blurb is a single user-facing finding contributed by a sub-check.
// Priority controls summary-line ordering — lower priority comes first.
type blurb struct {
	priority int
	severity int
	text     string
}

// undervoltedNowBlurb returns the user-facing "undervolted now" text,
// optionally enriched with the negotiated PSU rating vs the family
// expectation when the firmware reports an under-spec supply. The
// enrichment only fires when both PSU pointers are non-nil AND the
// rating is strictly below the documented expectation — a 5A PSU on
// a Pi 5 renders as plain "undervolted now" (PSU isn't the obvious
// culprit).
//
// Format example: "undervolted now (PSU 3A, needs 5A)" — uses %g to
// strip noise zeros so 4500 mA reads "4.5A", not "4.50A".
func undervoltedNowBlurb(t *Throttle) string {
	if t.PSUMaxCurrentMA == nil || t.PSUExpectedMA == nil || *t.PSUExpectedMA <= 0 {
		return "undervolted now"
	}
	if *t.PSUMaxCurrentMA >= *t.PSUExpectedMA {
		return "undervolted now"
	}
	return fmt.Sprintf("undervolted now (PSU %gA, needs %gA)",
		float64(*t.PSUMaxCurrentMA)/1000,
		float64(*t.PSUExpectedMA)/1000)
}

// Summarize derives the aggregator rollup from probe results. Pure
// function; called by Reader.Probe to populate Snapshot.Health and
// (with fresh probe results) by the --hardware CLI handler.
//
// Behaviour:
//   - Throttle blurbs (8 bits + history-only edges) only when t != nil.
//   - System blurbs (CPU temp warn/err, NTP miss, mem, disk) gated
//     per-pointer-field on the System block.
//   - "vcgencmd unavailable" partial-probe marker fires when
//     isPi && t == nil.
//   - Aggregate severity = max over blurb severities; ok if anything
//     probed, na if nothing.
//   - Health.IsRaspberryPi = isPi argument (so it survives Throttle
//     being nil).
//
// Summary is UTF-8 (· separator, °C suffix). The CLI handler folds to
// ASCII via asciiSafe before emitting; the SPA reads UTF-8 directly.
func Summarize(t *Throttle, s *System, thr Thresholds, isPi bool) *Health {
	out := &Health{IsRaspberryPi: isPi}

	var b []blurb
	add := func(prio, sev int, text string) {
		b = append(b, blurb{priority: prio, severity: sev, text: text})
	}

	if t != nil {
		if t.UndervoltageNow {
			add(1, sevErr, undervoltedNowBlurb(t))
		}
		if t.ThrottledNow {
			add(2, sevErr, "throttling now")
		}
		if t.FreqCappedNow {
			add(3, sevErr, "arm freq capped now")
		}
		if t.SoftTempLimitNow {
			add(5, sevWarn, "thermal cap reached")
		}
	}

	if s != nil && s.NTPSynchronized != nil && !*s.NTPSynchronized {
		uptime := 0.0
		if s.UptimeSeconds != nil {
			uptime = *s.UptimeSeconds
		}
		if uptime >= thr.NTPGraceSeconds {
			add(4, sevErr, "time not synced")
		} else {
			add(14, sevWarn, "time sync pending")
		}
	}

	if s != nil && s.CPUTempCelsius != nil {
		c := *s.CPUTempCelsius
		switch {
		case c >= thr.TempErrC:
			add(6, sevErr, fmt.Sprintf("%.0f°C", c))
		case c >= thr.TempWarnC:
			add(6, sevWarn, fmt.Sprintf("%.0f°C", c))
		}
	}

	if s != nil && s.MemoryAvailPct != nil {
		p := *s.MemoryAvailPct
		switch {
		case p < thr.MemErrPct:
			add(7, sevErr, fmt.Sprintf("mem %.0f%% free", p))
		case p < thr.MemWarnPct:
			add(7, sevWarn, fmt.Sprintf("mem %.0f%% free", p))
		}
	}

	if s != nil && s.DiskFreePct != nil {
		p := *s.DiskFreePct
		switch {
		case p < thr.DiskErrPct:
			add(8, sevErr, fmt.Sprintf("disk %.0f%% free", p))
		case p < thr.DiskWarnPct:
			add(8, sevWarn, fmt.Sprintf("disk %.0f%% free", p))
		}
	}

	if t != nil {
		if t.UndervoltageEver && !t.UndervoltageNow {
			add(9, sevWarn, "undervoltage history")
		}
		if t.ThrottledEver && !t.ThrottledNow {
			add(10, sevWarn, "throttling history")
		}
		if t.FreqCappedEver && !t.FreqCappedNow {
			add(11, sevWarn, "arm freq cap history")
		}
		if t.SoftTempLimitEver && !t.SoftTempLimitNow {
			add(12, sevWarn, "soft temp limit history")
		}
	}

	// Partial-probe marker: Pi confirmed but throttle probe couldn't
	// answer. Distinct from "not a Pi" (where we wouldn't expect
	// vcgencmd at all). Emitted at sevOK so the tile stays green when
	// every other probe is happy.
	if isPi && t == nil {
		add(13, sevOK, "vcgencmd unavailable")
	}

	if !anyProbed(t, s) {
		out.Severity = sevName[sevNA]
		out.Summary = "probe failed"
		return out
	}

	maxSev := sevOK
	for _, x := range b {
		if x.severity > maxSev {
			maxSev = x.severity
		}
	}
	out.Severity = sevName[maxSev]

	tempProbed := s != nil && s.CPUTempCelsius != nil

	if len(b) == 0 {
		summary := "healthy"
		if tempProbed {
			summary += fmt.Sprintf(" · %.0f°C", *s.CPUTempCelsius)
		}
		if !isPi {
			summary = "generic Linux · " + summary
		}
		out.Summary = summary
		return out
	}

	sortBlurbs(b)
	parts := make([]string, 0, 3)
	for _, x := range b {
		parts = append(parts, x.text)
		if len(parts) == 3 {
			break
		}
	}
	var summary string
	if maxSev == sevOK {
		summary = "healthy"
		if tempProbed {
			summary += fmt.Sprintf(" · %.0f°C", *s.CPUTempCelsius)
		}
		summary += " · " + strings.Join(parts, " · ")
	} else {
		summary = strings.Join(parts, " · ")
	}
	if !isPi {
		summary = "generic Linux · " + summary
	}
	out.Summary = summary
	return out
}

// anyProbed returns true if any sub-probe contributed a signal. Used to
// distinguish "all probes failed" from "all probes ok but quiet".
func anyProbed(t *Throttle, s *System) bool {
	if t != nil {
		return true
	}
	if s == nil {
		return false
	}
	return s.CPUTempCelsius != nil ||
		s.NTPSynchronized != nil ||
		s.MemoryAvailPct != nil ||
		s.DiskFreePct != nil
}

// sortBlurbs is an in-place insertion sort by priority ascending. The
// slice is tiny (~10 elements at most), so the cost is negligible and
// we avoid pulling in sort.Slice for one tiny call.
func sortBlurbs(b []blurb) {
	for i := 1; i < len(b); i++ {
		j := i
		for j > 0 && b[j].priority < b[j-1].priority {
			b[j], b[j-1] = b[j-1], b[j]
			j--
		}
	}
}
