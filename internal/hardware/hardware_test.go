package hardware

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// === parser tests ===

func TestParseThrottled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		bits uint32
		ok   bool
	}{
		{"throttled=0x0\n", 0x0, true},
		{"throttled=0x00000\n", 0x0, true},
		{"throttled=0x1\n", 0x1, true},
		{"throttled=0x4\n", 0x4, true},
		{"throttled=0x10000\n", 0x10000, true},
		{"throttled=0x50000\n", 0x50000, true},
		{"throttled=0x80000\n", 0x80000, true},
		{"throttled=0xF000F\n", 0xF000F, true},
		{"  throttled=0xF000F  \n", 0xF000F, true},
		{"throttled=0X10\n", 0x10, true},
		{"", 0, false},
		{"throttled=", 0, false},
		{"throttled=notahex\n", 0, false},
		{"completely-different-output", 0, false},
	}
	for _, c := range cases {
		got, ok := parseThrottled(c.in)
		if ok != c.ok || got != c.bits {
			t.Errorf("parseThrottled(%q) = (0x%x, %v), want (0x%x, %v)",
				c.in, got, ok, c.bits, c.ok)
		}
	}
}

func TestParseTimedatectlShow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		in         string
		wantSynced bool
		wantFound  bool
	}{
		{"yes", "NTPSynchronized=yes\nTimezone=UTC\n", true, true},
		{"no", "NTPSynchronized=no\n", false, true},
		{"true", "NTPSynchronized=true\n", true, true},
		{"false", "NTPSynchronized=false\n", false, true},
		{"mid-output", "Timezone=UTC\nNTPSynchronized=yes\nLocalRTC=no\n", true, true},
		{"missing", "Timezone=UTC\nLocalRTC=no\n", false, false},
		{"legacy NTP only (not used as fallback)", "NTP=yes\nTimezone=UTC\n", false, false},
		{"empty", "", false, false},
	}
	for _, c := range cases {
		gotS, gotF := parseTimedatectlShow(c.in)
		if gotS != c.wantSynced || gotF != c.wantFound {
			t.Errorf("%s: parseTimedatectlShow = (%v, %v), want (%v, %v)",
				c.name, gotS, gotF, c.wantSynced, c.wantFound)
		}
	}
}

func TestParseMeminfo(t *testing.T) {
	t.Parallel()
	b := []byte("MemTotal:        4000000 kB\nMemFree:          500000 kB\nMemAvailable:     100000 kB\nBuffers:          12000 kB\n")
	pct, ok := parseMeminfo(b)
	if !ok {
		t.Fatal("expected ok")
	}
	if pct < 2.49 || pct > 2.51 {
		t.Errorf("availPct = %v, want ≈2.50", pct)
	}
	if _, ok := parseMeminfo([]byte("MemFree: 100 kB\n")); ok {
		t.Error("expected !ok when MemTotal absent")
	}
	if _, ok := parseMeminfo([]byte("MemTotal: 0 kB\nMemAvailable: 0 kB\n")); ok {
		t.Error("expected !ok when MemTotal is zero")
	}
}

func TestParseUptime(t *testing.T) {
	t.Parallel()
	s, ok := parseUptime([]byte("123456.78 90123.45\n"))
	if !ok || s < 123456 || s > 123457 {
		t.Errorf("parseUptime = (%v, %v), want ≈123456.78", s, ok)
	}
	if _, ok := parseUptime([]byte("garbage\n")); ok {
		t.Error("expected !ok for garbage")
	}
}

func TestParseSysfsTempMilliC(t *testing.T) {
	t.Parallel()
	c, ok := parseSysfsTempMilliC([]byte("73000\n"))
	if !ok || c != 73.0 {
		t.Errorf("got (%v, %v), want (73.0, true)", c, ok)
	}
	if _, ok := parseSysfsTempMilliC([]byte("garbage")); ok {
		t.Error("expected !ok for garbage")
	}
}

func TestIsRaspberryPi(t *testing.T) {
	t.Parallel()
	if !isRaspberryPi([]byte("Raspberry Pi 4 Model B Rev 1.4\x00")) {
		t.Error("Pi 4 model should match")
	}
	if !isRaspberryPi([]byte("Raspberry Pi 5\x00")) {
		t.Error("Pi 5 model should match")
	}
	if isRaspberryPi([]byte("Some other SBC\x00")) {
		t.Error("non-Pi should not match")
	}
	if isRaspberryPi(nil) {
		t.Error("empty should not match")
	}
}

// === Probe integration tests ===

// canned wires a CommandRunner that returns the given stdout for argv[0]
// matching one of the bins. vcgencmd dispatches on the first sub-arg so
// `get_throttled` and `get_config psu_max_current` mock independently.
type canned struct {
	vcgencmd       string // get_throttled
	vcgencmdErr    error
	vcgencmdPSU    string // get_config psu_max_current
	vcgencmdPSUErr error
	timedatectl    string
	timeErr        error
}

func (c canned) runner() wexec.CommandRunner {
	return func(ctx context.Context, argv []string) (wexec.Result, error) {
		if len(argv) == 0 {
			return wexec.Result{}, errors.New("empty argv")
		}
		base := filepath.Base(argv[0])
		switch base {
		case "vcgencmd":
			if len(argv) >= 3 && argv[1] == "get_config" && argv[2] == "psu_max_current" {
				return wexec.Result{Stdout: []byte(c.vcgencmdPSU)}, c.vcgencmdPSUErr
			}
			return wexec.Result{Stdout: []byte(c.vcgencmd)}, c.vcgencmdErr
		case "timedatectl":
			return wexec.Result{Stdout: []byte(c.timedatectl)}, c.timeErr
		}
		return wexec.Result{}, errors.New("unexpected argv")
	}
}

// fixture builds a Reader pointed at a tempdir, with configurable file
// contents + canned runner + stub diskProber.
type fixture struct {
	t          *testing.T
	model      string // device-tree model contents; empty = file absent
	thermal    string // sysfs temp file contents; empty = absent
	meminfo    string
	uptime     string
	disk       DiskProber
	canned     canned
	thresholds Thresholds
	wantPaths  func(Paths) Paths // optional override hook
}

func (f *fixture) reader() *Reader {
	dir := f.t.TempDir()
	p := Paths{
		VcgencmdBinary:    "/usr/bin/vcgencmd",
		TimedatectlBinary: "/usr/bin/timedatectl",
		ThermalZoneFile:   filepath.Join(dir, "thermal"),
		MeminfoFile:       filepath.Join(dir, "meminfo"),
		UptimeFile:        filepath.Join(dir, "uptime"),
		DeviceTreeModel:   filepath.Join(dir, "model"),
		RootMountPoint:    "/",
		ProbeTimeout:      time.Second,
	}
	if f.model != "" {
		writeFile(f.t, p.DeviceTreeModel, []byte(f.model))
	}
	if f.thermal != "" {
		writeFile(f.t, p.ThermalZoneFile, []byte(f.thermal))
	}
	if f.meminfo != "" {
		writeFile(f.t, p.MeminfoFile, []byte(f.meminfo))
	}
	if f.uptime != "" {
		writeFile(f.t, p.UptimeFile, []byte(f.uptime))
	}
	if f.wantPaths != nil {
		p = f.wantPaths(p)
	}
	t := f.thresholds
	if (t == Thresholds{}) {
		t = DefaultThresholds()
	}
	return NewReader(p, t, f.canned.runner(), f.disk)
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func fixedDiskProber(freePct float64) DiskProber {
	return func(string) (float64, error) { return freePct, nil }
}

func errDiskProber(err error) DiskProber {
	return func(string) (float64, error) { return 0, err }
}

func TestProbe_AllProbesFail(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:      t,
		canned: canned{vcgencmdErr: errors.New("not found"), timeErr: errors.New("not found")},
		disk:   errDiskProber(errors.New("statfs: no such file")),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "na" {
		t.Errorf("severity = %q, want na", snap.Health.Severity)
	}
	if snap.Health.Summary != "probe failed" {
		t.Errorf("summary = %q, want \"probe failed\"", snap.Health.Summary)
	}
	if snap.Health.IsRaspberryPi {
		t.Error("IsRaspberryPi should be false when device-tree file missing")
	}
	if snap.PiThrottle != nil {
		t.Errorf("PiThrottle = %+v, want nil", snap.PiThrottle)
	}
}

func TestProbe_HealthyPi(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B Rev 1.4\x00",
		thermal: "56000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67 9876.54\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\nTimezone=UTC\n",
		},
		disk: fixedDiskProber(45),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "ok" {
		t.Errorf("severity = %q, want ok", snap.Health.Severity)
	}
	if !strings.HasPrefix(snap.Health.Summary, "healthy") {
		t.Errorf("summary = %q, want prefix \"healthy\"", snap.Health.Summary)
	}
	if !strings.Contains(snap.Health.Summary, "56°C") {
		t.Errorf("summary = %q, expected to contain 56°C", snap.Health.Summary)
	}
	if !snap.Health.IsRaspberryPi {
		t.Error("IsRaspberryPi should be true")
	}
	if snap.PiThrottle == nil {
		t.Fatal("PiThrottle = nil, want populated on healthy Pi")
	}
	if snap.System.CPUTempCelsius == nil {
		t.Error("System.CPUTempCelsius should be populated")
	}
	if snap.System.NTPSynchronized == nil {
		t.Error("System.NTPSynchronized should be populated")
	}
	if snap.System.MemoryAvailPct == nil {
		t.Error("System.MemoryAvailPct should be populated")
	}
	if snap.System.DiskFreePct == nil {
		t.Error("System.DiskFreePct should be populated")
	}
}

func TestProbe_GenericLinux_PiAbsent(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		thermal: "60000\n",
		meminfo: "MemTotal: 16000000 kB\nMemAvailable: 8000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmdErr: errors.New("exec: file not found"),
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(60),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.IsRaspberryPi {
		t.Error("IsRaspberryPi should be false")
	}
	if snap.Health.Severity != "ok" {
		t.Errorf("severity = %q, want ok", snap.Health.Severity)
	}
	if !strings.HasPrefix(snap.Health.Summary, "generic Linux") {
		t.Errorf("summary = %q, want prefix \"generic Linux\"", snap.Health.Summary)
	}
	if snap.PiThrottle != nil {
		t.Errorf("PiThrottle = %+v, want nil on non-Pi", snap.PiThrottle)
	}
}

func TestProbe_PiButVcgencmdMissing(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 5\x00",
		thermal: "55000\n",
		meminfo: "MemTotal: 8000000 kB\nMemAvailable: 6000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmdErr: errors.New("exec: file not found"),
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if !snap.Health.IsRaspberryPi {
		t.Error("IsRaspberryPi should be true (device-tree confirmed)")
	}
	if snap.PiThrottle != nil {
		t.Errorf("PiThrottle should be nil when vcgencmd missing, got %+v", snap.PiThrottle)
	}
	// Missing vcgencmd alone (with every other probe happy) stays sevOK
	// so the dashboard tile doesn't paint amber over a feeder that is
	// otherwise fine; the summary still mentions the partial probe.
	if snap.Health.Severity != "ok" {
		t.Errorf("severity = %q, want ok", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "healthy") {
		t.Errorf("summary = %q, expected to mention \"healthy\"", snap.Health.Summary)
	}
	if !strings.Contains(snap.Health.Summary, "vcgencmd unavailable") {
		t.Errorf("summary = %q, expected to mention \"vcgencmd unavailable\"", snap.Health.Summary)
	}
}

// Codex regression sentinel: Pi confirmed, get_throttled fails, get_config
// psu_max_current would succeed. The Reader must NOT call probePSU on a
// nil Throttle pointer and must NOT leak a PSU-only PiThrottle.
func TestProbe_PiModelTrue_ThrottleFailsButPSUWouldWork_NoPanic(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 5\x00",
		thermal: "55000\n",
		meminfo: "MemTotal: 8000000 kB\nMemAvailable: 6000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmdErr: errors.New("exec: file not found"),
			vcgencmdPSU: "psu_max_current=5000\n", // unreachable: vcgencmdErr fires first
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	// Production probeThrottle errors out via vcgencmdErr; probePSU is
	// then never called because PiThrottle stays nil. The test asserts
	// the no-panic + no-phantom-PiThrottle posture explicitly.
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle != nil {
		t.Errorf("PiThrottle should be nil; got %+v", snap.PiThrottle)
	}
}

func TestProbe_UndervoltageNow(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "70000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x10001\n", // bits 0 + 16: undervolt now + ever
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle == nil {
		t.Fatal("PiThrottle = nil")
	}
	if !snap.PiThrottle.UndervoltageNow || !snap.PiThrottle.UndervoltageEver {
		t.Errorf("undervolt flags wrong: %+v", snap.PiThrottle)
	}
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err", snap.Health.Severity)
	}
	if !strings.HasPrefix(snap.Health.Summary, "undervolted now") {
		t.Errorf("summary = %q, expected to lead with \"undervolted now\"", snap.Health.Summary)
	}
}

func TestProbe_ThrottledEverOnly_IsWarn(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x50000\n", // bits 16 + 18: undervolt ever + throttled ever
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle == nil {
		t.Fatal("PiThrottle = nil")
	}
	if !snap.PiThrottle.UndervoltageEver || !snap.PiThrottle.ThrottledEver {
		t.Errorf("expected undervolt-ever + throttled-ever, got %+v", snap.PiThrottle)
	}
	if snap.PiThrottle.UndervoltageNow || snap.PiThrottle.ThrottledNow {
		t.Errorf("now flags should be false")
	}
	if snap.Health.Severity != "warn" {
		t.Errorf("severity = %q, want warn", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "undervoltage history") {
		t.Errorf("summary = %q, expected mention of history", snap.Health.Summary)
	}
}

func TestProbe_TemperatureWarn(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "78000\n", // 78°C — between warn (75) and err (80)
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "warn" {
		t.Errorf("severity = %q, want warn (temp=78)", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "78°C") {
		t.Errorf("summary = %q, expected 78°C", snap.Health.Summary)
	}
}

func TestProbe_TemperatureErr(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "82000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err (temp=82)", snap.Health.Severity)
	}
}

func TestProbe_NTPNotSynced_WithinGrace(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "60.0\n", // 60s < 300s grace
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=no\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "warn" {
		t.Errorf("severity = %q, want warn (within grace)", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "time sync pending") {
		t.Errorf("summary = %q, expected \"time sync pending\"", snap.Health.Summary)
	}
}

func TestProbe_NTPNotSynced_PastGrace(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "400.0\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=no\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err (past grace)", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "time not synced") {
		t.Errorf("summary = %q, expected \"time not synced\"", snap.Health.Summary)
	}
}

func TestProbe_MemoryErr(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 100000 kB\n", // 2.5%
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err (mem 2.5%%)", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "mem 2% free") {
		t.Errorf("summary = %q, expected mem 2%% free", snap.Health.Summary)
	}
}

func TestProbe_DiskWarnAndErr(t *testing.T) {
	t.Parallel()

	warnCase := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(12),
	}
	snap := warnCase.reader().Probe(context.Background())
	if snap.Health.Severity != "warn" {
		t.Errorf("disk 12%%: severity = %q, want warn", snap.Health.Severity)
	}

	errCase := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(3),
	}
	snap = errCase.reader().Probe(context.Background())
	if snap.Health.Severity != "err" {
		t.Errorf("disk 3%%: severity = %q, want err", snap.Health.Severity)
	}
}

// Codex regression sentinel: a 0% disk-free reading must NOT be hidden by
// pointer-omitempty (the value is real, dangerous, and worth showing).
// Same for 0% memory.
func TestProbe_ZeroDiskFreePct_NotHidden(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmdErr: errors.New("not found"),
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(0),
	}
	snap := f.reader().Probe(context.Background())
	if snap.System.DiskFreePct == nil {
		t.Fatal("DiskFreePct should be populated even at 0%")
	}
	if *snap.System.DiskFreePct != 0 {
		t.Errorf("DiskFreePct = %v, want 0", *snap.System.DiskFreePct)
	}
	blob, _ := json.Marshal(snap.System)
	if !strings.Contains(string(blob), `"disk_free_pct":0`) {
		t.Errorf("marshaled JSON missing disk_free_pct:0 — pointer-omitempty hiding zero: %s", blob)
	}
}

func TestProbe_ZeroMemoryAvailPct_NotHidden(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 0 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmdErr: errors.New("not found"),
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.System.MemoryAvailPct == nil {
		t.Fatal("MemoryAvailPct should be populated even at 0%")
	}
	if *snap.System.MemoryAvailPct != 0 {
		t.Errorf("MemoryAvailPct = %v, want 0", *snap.System.MemoryAvailPct)
	}
	blob, _ := json.Marshal(snap.System)
	if !strings.Contains(string(blob), `"mem_avail_pct":0`) {
		t.Errorf("marshaled JSON missing mem_avail_pct:0: %s", blob)
	}
}

func TestProbe_WorstCase_SummaryOrdering(t *testing.T) {
	t.Parallel()
	// Throw everything at the classifier; verify summary leads with
	// voltage-now → throttling-now → arm-cap-now (top 3 by priority).
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "82000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 100000 kB\n",
		uptime:  "500.0\n",
		canned: canned{
			vcgencmd:    "throttled=0xF000F\n", // every now-bit + every ever-bit
			timedatectl: "NTPSynchronized=no\n",
		},
		disk: fixedDiskProber(3),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err", snap.Health.Severity)
	}
	wantLead := "undervolted now · throttling now · arm freq capped now"
	if !strings.HasPrefix(snap.Health.Summary, wantLead) {
		t.Errorf("summary = %q, want prefix %q", snap.Health.Summary, wantLead)
	}
}

func TestProbe_PartialSuccess_MemOnly(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 100000 kB\n",
		canned: canned{
			vcgencmdErr: errors.New("not found"),
			timeErr:     errors.New("not found"),
		},
		disk: errDiskProber(errors.New("statfs failed")),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err (mem low)", snap.Health.Severity)
	}
	if !strings.Contains(snap.Health.Summary, "mem") {
		t.Errorf("summary = %q, expected mention of mem", snap.Health.Summary)
	}
	if strings.Contains(snap.Health.Summary, "healthy") {
		t.Errorf("summary = %q, should NOT say healthy when only mem probed", snap.Health.Summary)
	}
}

func TestProbe_HealthyWithoutTemp(t *testing.T) {
	t.Parallel()
	// Thermal sysfs unreadable, everything else green → summary should
	// be just "healthy" without a temperature suffix.
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x0\n",
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.Health.Severity != "ok" {
		t.Errorf("severity = %q, want ok", snap.Health.Severity)
	}
	if snap.Health.Summary != "healthy" {
		t.Errorf("summary = %q, want exactly \"healthy\"", snap.Health.Summary)
	}
}

// === NewReader sanity ===

func TestNewReader_NilRunnerDefaultsToReal(t *testing.T) {
	t.Parallel()
	r := NewReader(DefaultPaths(), DefaultThresholds(), nil, nil)
	if r.runner == nil {
		t.Error("nil runner should fall back to RealRunner")
	}
	if r.diskProber == nil {
		t.Error("nil diskProber should fall back to statfsProber")
	}
	if r.paths.ProbeTimeout <= 0 {
		t.Error("zero ProbeTimeout should default to non-zero")
	}
}

// === PSU probe + undervoltage enrichment ===

func TestParsePSUMaxCurrent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   int
		wantOK bool
	}{
		{"psu_max_current=5000\n", 5000, true},
		{"psu_max_current=3000", 3000, true},
		{"  psu_max_current=5000  \n", 5000, true},
		{"psu_max_current=0\n", 0, false},
		{"psu_max_current=\n", 0, false},
		{"psu_max_current=garbage\n", 0, false},
		{"some_other_key=5000\n", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := parsePSUMaxCurrent(c.in)
		if ok != c.wantOK || got != c.want {
			t.Errorf("parsePSUMaxCurrent(%q) = (%d, %v), want (%d, %v)",
				c.in, got, ok, c.want, c.wantOK)
		}
	}
}

func TestExpectedPSUMaxCurrentMA(t *testing.T) {
	t.Parallel()
	cases := []struct {
		model string
		want  int
	}{
		{"Raspberry Pi 5 Model B Rev 1.0\x00", 5000},
		{"Raspberry Pi 5\x00", 5000},
		{"Raspberry Pi Compute Module 5 Rev 1.0\x00", 5000},
		{"Raspberry Pi 4 Model B Rev 1.4\x00", 3000},
		{"Raspberry Pi Compute Module 4\x00", 3000},
		{"Raspberry Pi 3 Model B+\x00", 2500},
		{"Raspberry Pi Zero 2 W Rev 1.0\x00", 2500},
		{"Raspberry Pi 2 Model B\x00", 0},
		{"Some other SBC\x00", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := expectedPSUMaxCurrentMA([]byte(c.model)); got != c.want {
			t.Errorf("expectedPSUMaxCurrentMA(%q) = %d, want %d", c.model, got, c.want)
		}
	}
}

func TestUndervoltedNowBlurb(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   *Throttle
		want string
	}{
		{
			name: "PSU not probed → plain blurb",
			in:   &Throttle{},
			want: "undervolted now",
		},
		{
			name: "PSU rating present but expected unknown → plain blurb",
			in:   &Throttle{PSUMaxCurrentMA: intPtr(1500)},
			want: "undervolted now",
		},
		{
			name: "PSU rating matches expectation → plain blurb",
			in:   &Throttle{PSUMaxCurrentMA: intPtr(5000), PSUExpectedMA: intPtr(5000)},
			want: "undervolted now",
		},
		{
			name: "PSU rating exceeds expectation → plain blurb",
			in:   &Throttle{PSUMaxCurrentMA: intPtr(5500), PSUExpectedMA: intPtr(5000)},
			want: "undervolted now",
		},
		{
			name: "Pi 5 with 3A PSU → enriched",
			in:   &Throttle{PSUMaxCurrentMA: intPtr(3000), PSUExpectedMA: intPtr(5000)},
			want: "undervolted now (PSU 3A, needs 5A)",
		},
		{
			name: "Non-integer rating uses %g (4.5A not 4.50A)",
			in:   &Throttle{PSUMaxCurrentMA: intPtr(4500), PSUExpectedMA: intPtr(5000)},
			want: "undervolted now (PSU 4.5A, needs 5A)",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := undervoltedNowBlurb(c.in); got != c.want {
				t.Errorf("undervoltedNowBlurb = %q, want %q", got, c.want)
			}
		})
	}
}

func TestProbe_PSU_Pi5With3APSU_UndervoltageEnriched(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 5 Model B Rev 1.0\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 8000000 kB\nMemAvailable: 6000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x10001\n",
			timedatectl: "NTPSynchronized=yes\n",
			vcgencmdPSU: "psu_max_current=3000\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle == nil {
		t.Fatal("PiThrottle = nil")
	}
	if snap.PiThrottle.PSUMaxCurrentMA == nil || *snap.PiThrottle.PSUMaxCurrentMA != 3000 {
		t.Errorf("PSUMaxCurrentMA wrong: %v", snap.PiThrottle.PSUMaxCurrentMA)
	}
	if snap.PiThrottle.PSUExpectedMA == nil || *snap.PiThrottle.PSUExpectedMA != 5000 {
		t.Errorf("PSUExpectedMA wrong: %v", snap.PiThrottle.PSUExpectedMA)
	}
	if snap.Health.Severity != "err" {
		t.Errorf("severity = %q, want err", snap.Health.Severity)
	}
	wantLead := "undervolted now (PSU 3A, needs 5A)"
	if !strings.HasPrefix(snap.Health.Summary, wantLead) {
		t.Errorf("summary = %q, want prefix %q", snap.Health.Summary, wantLead)
	}
}

func TestProbe_PSU_Pi5With5APSU_NoEnrichment(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 5 Model B Rev 1.0\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 8000000 kB\nMemAvailable: 6000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x10001\n",
			timedatectl: "NTPSynchronized=yes\n",
			vcgencmdPSU: "psu_max_current=5000\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle == nil || snap.PiThrottle.PSUMaxCurrentMA == nil ||
		*snap.PiThrottle.PSUMaxCurrentMA != 5000 {
		t.Errorf("PSUMaxCurrentMA wrong: %+v", snap.PiThrottle)
	}
	if !strings.HasPrefix(snap.Health.Summary, "undervolted now") ||
		strings.Contains(snap.Health.Summary, "PSU") {
		t.Errorf("summary should be plain 'undervolted now', got %q", snap.Health.Summary)
	}
}

// Codex regression sentinel: Pi 4 (no firmware psu_max_current). The
// previous code set PSUExpectedMA from the device-tree model regardless
// of probe outcome — emitting "expected 3000 mA" against no actual
// measurement. Pointer-omitempty fixes this: expected is only set when
// actual was read.
func TestProbe_PSU_Pi4NoVcgencmdReport_NoExpectedLeak(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B Rev 1.4\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x10001\n",
			timedatectl: "NTPSynchronized=yes\n",
			// Pi 4 firmware doesn't expose psu_max_current — empty stdout.
			vcgencmdPSU: "",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle == nil {
		t.Fatal("PiThrottle = nil — throttle should still be populated even on PSU probe miss")
	}
	if snap.PiThrottle.PSUMaxCurrentMA != nil {
		t.Errorf("PSUMaxCurrentMA should be nil on Pi 4 (no firmware report), got %d",
			*snap.PiThrottle.PSUMaxCurrentMA)
	}
	if snap.PiThrottle.PSUExpectedMA != nil {
		t.Errorf("PSUExpectedMA should be nil when actual not read, got %d",
			*snap.PiThrottle.PSUExpectedMA)
	}
	blob, _ := json.Marshal(snap.PiThrottle)
	if strings.Contains(string(blob), "psu_expected_ma") {
		t.Errorf("marshaled JSON leaks psu_expected_ma: %s", blob)
	}
	if strings.Contains(snap.Health.Summary, "PSU") {
		t.Errorf("summary should not mention PSU when not probed, got %q", snap.Health.Summary)
	}
}

func TestProbe_PSU_NonPi_NoEnrichment(t *testing.T) {
	t.Parallel()
	// No model file → not a Pi → PiThrottle stays nil → no PSU enrichment
	// even if vcgencmd were somehow to return a value.
	f := &fixture{
		t:       t,
		thermal: "60000\n",
		meminfo: "MemTotal: 16000000 kB\nMemAvailable: 8000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x10001\n",
			timedatectl: "NTPSynchronized=yes\n",
			vcgencmdPSU: "psu_max_current=3000\n",
		},
		disk: fixedDiskProber(40),
	}
	snap := f.reader().Probe(context.Background())
	if snap.PiThrottle != nil {
		t.Errorf("PiThrottle should be nil on non-Pi, got %+v", snap.PiThrottle)
	}
	if strings.Contains(snap.Health.Summary, "PSU") {
		t.Errorf("summary should not mention PSU on non-Pi, got %q", snap.Health.Summary)
	}
}

// === System struct JSON marshaling ===

// MarshalJSON contract: System emits at least "{}" when every sub-probe
// is nil. Anchors the universal-always-present-on-wire contract.
func TestSystem_EmptyMarshalsToEmptyObject(t *testing.T) {
	t.Parallel()
	var s System
	blob, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(blob) != "{}" {
		t.Errorf("empty System marshaled to %q, want \"{}\"", blob)
	}
}
