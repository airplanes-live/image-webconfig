package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/hardware"
)

// snap is a small constructor that builds a Snapshot from a Health
// summary + severity + (optionally) a Throttle pointer. The tests
// below only care about Health.{Severity, Summary} for the CLI's
// wire shape, so we don't bother populating System.
func snap(sev, summary string, t *hardware.Throttle) *hardware.Snapshot {
	return &hardware.Snapshot{
		PiThrottle: t,
		Health:     hardware.Health{Severity: sev, Summary: summary},
	}
}

func TestAsciiSafe(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"healthy", "healthy"},
		{"healthy · 56°C", "healthy * 56C"},
		{"undervolted now · throttling now", "undervolted now * throttling now"},
		{"82°C", "82C"},
		{"", ""},
	}
	for _, c := range cases {
		if got := asciiSafe(c.in); got != c.want {
			t.Errorf("asciiSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripTempSuffix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"healthy · 56°C", "healthy"},
		{"healthy · 5°C", "healthy"},
		{"healthy · 100°C", "healthy"},
		{"healthy", "healthy"},
		{"generic Linux · healthy · 56°C", "generic Linux · healthy"},
		// Don't strip a temp that's inside the findings list (warn/err
		// state), only the trailing suffix.
		{"82°C · mem 4% free", "82°C · mem 4% free"},
	}
	for _, c := range cases {
		if got := stripTempSuffix(c.in); got != c.want {
			t.Errorf("stripTempSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFormatCLILine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   *hardware.Snapshot
		want string
	}{
		{
			name: "ok with temp suffix drops the suffix",
			in:   snap("ok", "healthy · 56°C", nil),
			want: "ok\thealthy",
		},
		{
			name: "ok without temp suffix keeps summary as-is",
			in:   snap("ok", "healthy", nil),
			want: "ok\thealthy",
		},
		{
			name: "ok on non-Pi: keep generic-linux prefix, drop temp",
			in:   snap("ok", "generic Linux · healthy · 56°C", nil),
			want: "ok\tgeneric Linux * healthy",
		},
		{
			name: "warn with embedded temp keeps the temp (it's a finding, not a suffix)",
			// stripTempSuffix runs only on Severity == "ok"; warn/err
			// leaves any embedded temperature finding intact.
			in:   snap("warn", "undervoltage history · 78°C", nil),
			want: "warn\tundervoltage history * 78C",
		},
		{
			name: "err worst-case keeps all blurbs",
			in:   snap("err", "undervolted now · throttling now · arm freq capped now", nil),
			want: "err\tundervolted now * throttling now * arm freq capped now",
		},
		{
			name: "err with PSU-enriched undervoltage (already ASCII, no transform)",
			in:   snap("err", "undervolted now (PSU 3A, needs 5A) · throttling now", nil),
			want: "err\tundervolted now (PSU 3A, needs 5A) * throttling now",
		},
		{
			name: "na probe failed",
			in:   snap("na", "probe failed", nil),
			want: "na\tprobe failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatCLILine(c.in); got != c.want {
				t.Errorf("formatCLILine = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRunHardwareCmd_HappyPath(t *testing.T) {
	t.Parallel()
	probe := func(_ context.Context) *hardware.Snapshot {
		return snap("ok", "healthy · 56°C", nil)
	}
	var stdout, stderr bytes.Buffer
	code := runHardwareCmd(&stdout, &stderr, probe, time.Second)
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got := stdout.String(); got != "ok\thealthy\n" {
		t.Errorf("stdout = %q, want %q", got, "ok\thealthy\n")
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr nonempty: %q", stderr.String())
	}
}

func TestRunHardwareCmd_NilProbeResult(t *testing.T) {
	t.Parallel()
	probe := func(_ context.Context) *hardware.Snapshot { return nil }
	var stdout, stderr bytes.Buffer
	code := runHardwareCmd(&stdout, &stderr, probe, time.Second)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on nil result: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no result") {
		t.Errorf("stderr should mention no result, got %q", stderr.String())
	}
}

func TestRunHardwareCmd_RespectsTimeout(t *testing.T) {
	t.Parallel()
	probe := func(ctx context.Context) *hardware.Snapshot {
		<-ctx.Done()
		return snap("na", "probe failed", nil)
	}
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := runHardwareCmd(&stdout, &stderr, probe, 50*time.Millisecond)
	elapsed := time.Since(start)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 (na is still a valid result)", code)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("elapsed = %v, want close to 50ms", elapsed)
	}
	if got := stdout.String(); got != "na\tprobe failed\n" {
		t.Errorf("stdout = %q, want %q", got, "na\tprobe failed\n")
	}
}
