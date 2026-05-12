// Package pihealth probes Raspberry Pi hardware health for the webconfig
// dashboard's "Raspberry Pi" tile. All sources are local — vcgencmd, the
// /sys/class/thermal sysfs file, /proc/{meminfo,uptime}, timedatectl, and
// a statfs of the root mount point.
//
// Sub-probes are independent: a failure in one (e.g. vcgencmd unavailable)
// does not suppress others. The aggregator reports severity only over
// sub-probes that actually succeeded, so a partial failure can never
// synthesise a misleading "healthy" summary.
package pihealth

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// Throttle bit positions from the Raspberry Pi docs. "Now" bits live in
// the low nibble; the matching "since-boot" bits are 16 positions up.
const (
	bitUndervoltageNow   uint32 = 1 << 0
	bitARMFreqCapNow     uint32 = 1 << 1
	bitThrottlingNow     uint32 = 1 << 2
	bitSoftTempLimitNow  uint32 = 1 << 3
	bitUndervoltageEver  uint32 = 1 << 16
	bitARMFreqCapEver    uint32 = 1 << 17
	bitThrottlingEver    uint32 = 1 << 18
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
		return 0, errors.New("pihealth: statfs reported zero blocks")
	}
	// Bavail is blocks available to unprivileged users — what we
	// actually want for "free space" from the dashboard's perspective.
	return float64(s.Bavail) / float64(s.Blocks) * 100, nil
}

// Reader runs the probes and aggregates a PiHealth.
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

// PiHealth is the JSON payload embedded in /api/status.pi_health.
type PiHealth struct {
	Severity string `json:"severity"`
	Summary  string `json:"summary"`

	IsRaspberryPi bool `json:"is_raspberry_pi"`

	ThrottleProbed bool `json:"throttle_probed"`
	TempProbed     bool `json:"temp_probed"`
	TimeProbed     bool `json:"time_probed"`
	MemProbed      bool `json:"mem_probed"`
	DiskProbed     bool `json:"disk_probed"`

	UndervoltageNow   bool `json:"undervoltage_now"`
	UndervoltageEver  bool `json:"undervoltage_ever"`
	ARMFreqCapNow     bool `json:"arm_freq_cap_now"`
	ARMFreqCapEver    bool `json:"arm_freq_cap_ever"`
	ThrottlingNow     bool `json:"throttling_now"`
	ThrottlingEver    bool `json:"throttling_ever"`
	SoftTempLimitNow  bool `json:"soft_temp_limit_now"`
	SoftTempLimitEver bool `json:"soft_temp_limit_ever"`

	CPUTempCelsius  float64 `json:"cpu_temp_c,omitempty"`
	NTPSynchronized bool    `json:"ntp_synchronized"`
	UptimeSeconds   float64 `json:"uptime_s,omitempty"`
	MemoryAvailPct  float64 `json:"mem_avail_pct,omitempty"`
	DiskFreePct     float64 `json:"disk_free_pct,omitempty"`
}

// Probe runs every sub-probe and returns the aggregated PiHealth. Never
// returns nil; the caller can read *Probed flags to learn which sub-checks
// produced data.
func (r *Reader) Probe(ctx context.Context) *PiHealth {
	out := &PiHealth{}

	if model, err := os.ReadFile(r.paths.DeviceTreeModel); err == nil {
		out.IsRaspberryPi = isRaspberryPi(model)
	}

	r.probeThrottle(ctx, out)
	r.probeTemp(out)
	r.probeTime(ctx, out)
	r.probeMem(out)
	r.probeDisk(out)

	classify(out, r.thresholds)
	return out
}

func (r *Reader) probeThrottle(ctx context.Context, out *PiHealth) {
	cctx, cancel := context.WithTimeout(ctx, r.paths.ProbeTimeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.paths.VcgencmdBinary, "get_throttled"})
	if err != nil {
		return
	}
	bits, ok := parseThrottled(string(res.Stdout))
	if !ok {
		return
	}
	out.ThrottleProbed = true
	out.UndervoltageNow = bits&bitUndervoltageNow != 0
	out.UndervoltageEver = bits&bitUndervoltageEver != 0
	out.ARMFreqCapNow = bits&bitARMFreqCapNow != 0
	out.ARMFreqCapEver = bits&bitARMFreqCapEver != 0
	out.ThrottlingNow = bits&bitThrottlingNow != 0
	out.ThrottlingEver = bits&bitThrottlingEver != 0
	out.SoftTempLimitNow = bits&bitSoftTempLimitNow != 0
	out.SoftTempLimitEver = bits&bitSoftTempLimitEver != 0
}

func (r *Reader) probeTemp(out *PiHealth) {
	b, err := os.ReadFile(r.paths.ThermalZoneFile)
	if err != nil {
		return
	}
	c, ok := parseSysfsTempMilliC(b)
	if !ok {
		return
	}
	out.TempProbed = true
	out.CPUTempCelsius = c
}

func (r *Reader) probeTime(ctx context.Context, out *PiHealth) {
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
	out.TimeProbed = true
	out.NTPSynchronized = synced
	if b, err := os.ReadFile(r.paths.UptimeFile); err == nil {
		if s, ok := parseUptime(b); ok {
			out.UptimeSeconds = s
		}
	}
}

func (r *Reader) probeMem(out *PiHealth) {
	b, err := os.ReadFile(r.paths.MeminfoFile)
	if err != nil {
		return
	}
	pct, ok := parseMeminfo(b)
	if !ok {
		return
	}
	out.MemProbed = true
	out.MemoryAvailPct = pct
}

func (r *Reader) probeDisk(out *PiHealth) {
	pct, err := r.diskProber(r.paths.RootMountPoint)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		return
	}
	out.DiskProbed = true
	out.DiskFreePct = pct
}

// severity ranks. classify() collects (rank, blurb) pairs and emits the
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

// classify fills out.Severity + out.Summary based on probed sub-checks.
// The ordering of the priority cases here matches the plan's summary-
// ordering table.
func classify(out *PiHealth, t Thresholds) {
	var b []blurb
	add := func(prio, sev int, text string) {
		b = append(b, blurb{priority: prio, severity: sev, text: text})
	}

	if out.ThrottleProbed {
		if out.UndervoltageNow {
			add(1, sevErr, "undervolted now")
		}
		if out.ThrottlingNow {
			add(2, sevErr, "throttling now")
		}
		if out.ARMFreqCapNow {
			add(3, sevErr, "arm freq capped now")
		}
		if out.SoftTempLimitNow {
			add(5, sevWarn, "thermal cap reached")
		}
	}

	if out.TimeProbed && !out.NTPSynchronized {
		if out.UptimeSeconds >= t.NTPGraceSeconds {
			add(4, sevErr, "time not synced")
		} else {
			add(14, sevWarn, "time sync pending")
		}
	}

	if out.TempProbed {
		switch {
		case out.CPUTempCelsius >= t.TempErrC:
			add(6, sevErr, fmt.Sprintf("%.0f°C", out.CPUTempCelsius))
		case out.CPUTempCelsius >= t.TempWarnC:
			add(6, sevWarn, fmt.Sprintf("%.0f°C", out.CPUTempCelsius))
		}
	}

	if out.MemProbed {
		switch {
		case out.MemoryAvailPct < t.MemErrPct:
			add(7, sevErr, fmt.Sprintf("mem %.0f%% free", out.MemoryAvailPct))
		case out.MemoryAvailPct < t.MemWarnPct:
			add(7, sevWarn, fmt.Sprintf("mem %.0f%% free", out.MemoryAvailPct))
		}
	}

	if out.DiskProbed {
		switch {
		case out.DiskFreePct < t.DiskErrPct:
			add(8, sevErr, fmt.Sprintf("disk %.0f%% free", out.DiskFreePct))
		case out.DiskFreePct < t.DiskWarnPct:
			add(8, sevWarn, fmt.Sprintf("disk %.0f%% free", out.DiskFreePct))
		}
	}

	if out.ThrottleProbed {
		if out.UndervoltageEver && !out.UndervoltageNow {
			add(9, sevWarn, "undervoltage history")
		}
		if out.ThrottlingEver && !out.ThrottlingNow {
			add(10, sevWarn, "throttling history")
		}
		if out.ARMFreqCapEver && !out.ARMFreqCapNow {
			add(11, sevWarn, "arm freq cap history")
		}
		if out.SoftTempLimitEver && !out.SoftTempLimitNow {
			add(12, sevWarn, "soft temp limit history")
		}
	}

	// Partial-failure marker: Pi confirmed but throttle probe couldn't
	// answer. Distinct from "not a Pi" (where we wouldn't expect
	// vcgencmd at all).
	if out.IsRaspberryPi && !out.ThrottleProbed {
		add(13, sevWarn, "vcgencmd unavailable")
	}

	// All-probes-failed: nothing to say. severity=na, summary="probe failed".
	if !anyProbed(out) {
		out.Severity = sevName[sevNA]
		out.Summary = "probe failed"
		return
	}

	// Aggregate severity and pick the top 3 blurbs by priority.
	maxSev := sevOK
	for _, x := range b {
		if x.severity > maxSev {
			maxSev = x.severity
		}
	}
	out.Severity = sevName[maxSev]

	if len(b) == 0 {
		// Everything OK — emit the canonical healthy summary.
		summary := "healthy"
		if out.TempProbed {
			summary += fmt.Sprintf(" · %.0f°C", out.CPUTempCelsius)
		}
		if !out.IsRaspberryPi {
			summary = "generic Linux · " + summary
		}
		out.Summary = summary
		return
	}

	// Order findings by priority and take the first three.
	sortBlurbs(b)
	parts := make([]string, 0, 3)
	for _, x := range b {
		parts = append(parts, x.text)
		if len(parts) == 3 {
			break
		}
	}
	summary := strings.Join(parts, " · ")
	if !out.IsRaspberryPi {
		summary = "generic Linux · " + summary
	}
	out.Summary = summary
}

func anyProbed(p *PiHealth) bool {
	return p.ThrottleProbed || p.TempProbed || p.TimeProbed || p.MemProbed || p.DiskProbed
}

// sortBlurbs is an in-place insertion sort by priority ascending. The
// slice is tiny (~10 elements at most), so the cost is negligible and we
// avoid pulling in sort.Slice for one tiny call.
func sortBlurbs(b []blurb) {
	for i := 1; i < len(b); i++ {
		j := i
		for j > 0 && b[j].priority < b[j-1].priority {
			b[j], b[j-1] = b[j-1], b[j]
			j--
		}
	}
}
