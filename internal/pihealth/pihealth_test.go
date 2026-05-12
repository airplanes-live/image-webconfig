package pihealth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
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
// matching one of the bins; any other argv returns the supplied default.
type canned struct {
	vcgencmd    string
	vcgencmdErr error
	timedatectl string
	timeErr     error
}

func (c canned) runner() wexec.CommandRunner {
	return func(ctx context.Context, argv []string) (wexec.Result, error) {
		if len(argv) == 0 {
			return wexec.Result{}, errors.New("empty argv")
		}
		base := filepath.Base(argv[0])
		switch base {
		case "vcgencmd":
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "na" {
		t.Errorf("severity = %q, want na", got.Severity)
	}
	if got.Summary != "probe failed" {
		t.Errorf("summary = %q, want \"probe failed\"", got.Summary)
	}
	if got.IsRaspberryPi {
		t.Error("IsRaspberryPi should be false when device-tree file missing")
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "ok" {
		t.Errorf("severity = %q, want ok (got: %+v)", got.Severity, got)
	}
	if !strings.HasPrefix(got.Summary, "healthy") {
		t.Errorf("summary = %q, want prefix \"healthy\"", got.Summary)
	}
	if !strings.Contains(got.Summary, "56°C") {
		t.Errorf("summary = %q, expected to contain 56°C", got.Summary)
	}
	if !got.IsRaspberryPi {
		t.Error("IsRaspberryPi should be true")
	}
	if !got.ThrottleProbed || !got.TempProbed || !got.TimeProbed || !got.MemProbed || !got.DiskProbed {
		t.Errorf("all *Probed flags should be true, got %+v", got)
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
	got := f.reader().Probe(context.Background())
	if got.IsRaspberryPi {
		t.Error("IsRaspberryPi should be false")
	}
	if got.Severity != "ok" {
		t.Errorf("severity = %q, want ok", got.Severity)
	}
	if !strings.HasPrefix(got.Summary, "generic Linux") {
		t.Errorf("summary = %q, want prefix \"generic Linux\"", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if !got.IsRaspberryPi {
		t.Error("IsRaspberryPi should be true (device-tree confirmed)")
	}
	if got.ThrottleProbed {
		t.Error("ThrottleProbed should be false")
	}
	if got.Severity != "warn" {
		t.Errorf("severity = %q, want warn (partial failure)", got.Severity)
	}
	if !strings.Contains(got.Summary, "vcgencmd unavailable") {
		t.Errorf("summary = %q, expected to mention vcgencmd unavailable", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if !got.UndervoltageNow || !got.UndervoltageEver {
		t.Errorf("undervolt flags wrong: %+v", got)
	}
	if got.Severity != "err" {
		t.Errorf("severity = %q, want err", got.Severity)
	}
	if !strings.HasPrefix(got.Summary, "undervolted now") {
		t.Errorf("summary = %q, expected to lead with \"undervolted now\"", got.Summary)
	}
}

func TestProbe_ThrottlingEverOnly_IsWarn(t *testing.T) {
	t.Parallel()
	f := &fixture{
		t:       t,
		model:   "Raspberry Pi 4 Model B\x00",
		thermal: "60000\n",
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 2000000 kB\n",
		uptime:  "12345.67\n",
		canned: canned{
			vcgencmd:    "throttled=0x50000\n", // bits 16 + 18: undervolt ever + throttling ever
			timedatectl: "NTPSynchronized=yes\n",
		},
		disk: fixedDiskProber(40),
	}
	got := f.reader().Probe(context.Background())
	if !got.UndervoltageEver || !got.ThrottlingEver {
		t.Errorf("expected undervolt-ever + throttling-ever, got %+v", got)
	}
	if got.UndervoltageNow || got.ThrottlingNow {
		t.Errorf("now flags should be false")
	}
	if got.Severity != "warn" {
		t.Errorf("severity = %q, want warn", got.Severity)
	}
	if !strings.Contains(got.Summary, "undervoltage history") {
		t.Errorf("summary = %q, expected mention of history", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "warn" {
		t.Errorf("severity = %q, want warn (temp=78)", got.Severity)
	}
	if !strings.Contains(got.Summary, "78°C") {
		t.Errorf("summary = %q, expected 78°C", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "err" {
		t.Errorf("severity = %q, want err (temp=82)", got.Severity)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "warn" {
		t.Errorf("severity = %q, want warn (within grace)", got.Severity)
	}
	if !strings.Contains(got.Summary, "time sync pending") {
		t.Errorf("summary = %q, expected \"time sync pending\"", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "err" {
		t.Errorf("severity = %q, want err (past grace)", got.Severity)
	}
	if !strings.Contains(got.Summary, "time not synced") {
		t.Errorf("summary = %q, expected \"time not synced\"", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "err" {
		t.Errorf("severity = %q, want err (mem 2.5%%)", got.Severity)
	}
	if !strings.Contains(got.Summary, "mem 2% free") {
		t.Errorf("summary = %q, expected mem 2%% free", got.Summary)
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
	got := warnCase.reader().Probe(context.Background())
	if got.Severity != "warn" {
		t.Errorf("disk 12%%: severity = %q, want warn", got.Severity)
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
	got = errCase.reader().Probe(context.Background())
	if got.Severity != "err" {
		t.Errorf("disk 3%%: severity = %q, want err", got.Severity)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "err" {
		t.Errorf("severity = %q, want err", got.Severity)
	}
	wantLead := "undervolted now · throttling now · arm freq capped now"
	if !strings.HasPrefix(got.Summary, wantLead) {
		t.Errorf("summary = %q, want prefix %q", got.Summary, wantLead)
	}
}

func TestProbe_PartialSuccess_MemOnly(t *testing.T) {
	t.Parallel()
	// Only mem probe succeeds. Verify no false "healthy" and severity
	// reflects the mem error.
	f := &fixture{
		t:       t,
		meminfo: "MemTotal: 4000000 kB\nMemAvailable: 100000 kB\n",
		canned: canned{
			vcgencmdErr: errors.New("not found"),
			timeErr:     errors.New("not found"),
		},
		disk: errDiskProber(errors.New("statfs failed")),
	}
	got := f.reader().Probe(context.Background())
	if got.Severity != "err" {
		t.Errorf("severity = %q, want err (mem low)", got.Severity)
	}
	if !strings.Contains(got.Summary, "mem") {
		t.Errorf("summary = %q, expected mention of mem", got.Summary)
	}
	if strings.Contains(got.Summary, "healthy") {
		t.Errorf("summary = %q, should NOT say healthy when only mem probed", got.Summary)
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
	got := f.reader().Probe(context.Background())
	if got.Severity != "ok" {
		t.Errorf("severity = %q, want ok", got.Severity)
	}
	if got.Summary != "healthy" {
		t.Errorf("summary = %q, want exactly \"healthy\"", got.Summary)
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
