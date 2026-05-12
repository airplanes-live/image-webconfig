package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/pihealth"
)

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
		in   *pihealth.PiHealth
		want string
	}{
		{
			name: "ok with temp probed drops temp suffix",
			in: &pihealth.PiHealth{
				Severity:       "ok",
				Summary:        "healthy · 56°C",
				TempProbed:     true,
				IsRaspberryPi:  true,
				CPUTempCelsius: 56,
			},
			want: "ok\thealthy",
		},
		{
			name: "ok without temp probed keeps summary as-is",
			in: &pihealth.PiHealth{
				Severity:      "ok",
				Summary:       "healthy",
				IsRaspberryPi: true,
			},
			want: "ok\thealthy",
		},
		{
			name: "ok on non-Pi: keep generic-linux prefix, drop temp",
			in: &pihealth.PiHealth{
				Severity:       "ok",
				Summary:        "generic Linux · healthy · 56°C",
				TempProbed:     true,
				CPUTempCelsius: 56,
			},
			want: "ok\tgeneric Linux * healthy",
		},
		{
			name: "warn with history and temp blurb",
			in: &pihealth.PiHealth{
				Severity:         "warn",
				Summary:          "undervoltage history · 78°C",
				TempProbed:       true,
				CPUTempCelsius:   78,
				UndervoltageEver: true,
			},
			// stripTempSuffix only fires on severity==ok, so the temp
			// blurb stays — it's part of the issue list now.
			want: "warn\tundervoltage history * 78C",
		},
		{
			name: "err worst-case keeps all blurbs",
			in: &pihealth.PiHealth{
				Severity:        "err",
				Summary:         "undervolted now · throttling now · arm freq capped now",
				ThrottleProbed:  true,
				UndervoltageNow: true,
				ThrottlingNow:   true,
				ARMFreqCapNow:   true,
			},
			want: "err\tundervolted now * throttling now * arm freq capped now",
		},
		{
			name: "err with PSU-enriched undervoltage (already ASCII, no transform)",
			in: &pihealth.PiHealth{
				Severity:        "err",
				Summary:         "undervolted now (PSU 3A, needs 5A) · throttling now",
				ThrottleProbed:  true,
				UndervoltageNow: true,
				ThrottlingNow:   true,
				PSUProbed:       true,
				PSUMaxCurrentMA: 3000,
				PSUExpectedMA:   5000,
			},
			want: "err\tundervolted now (PSU 3A, needs 5A) * throttling now",
		},
		{
			name: "na probe failed",
			in: &pihealth.PiHealth{
				Severity: "na",
				Summary:  "probe failed",
			},
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

func TestRunPiHealthCmd_HappyPath(t *testing.T) {
	t.Parallel()
	probe := func(_ context.Context) *pihealth.PiHealth {
		return &pihealth.PiHealth{
			Severity:       "ok",
			Summary:        "healthy · 56°C",
			TempProbed:     true,
			IsRaspberryPi:  true,
			CPUTempCelsius: 56,
		}
	}
	var stdout, stderr bytes.Buffer
	code := runPiHealthCmd(&stdout, &stderr, probe, time.Second)
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

func TestRunPiHealthCmd_NilProbeResult(t *testing.T) {
	t.Parallel()
	probe := func(_ context.Context) *pihealth.PiHealth { return nil }
	var stdout, stderr bytes.Buffer
	code := runPiHealthCmd(&stdout, &stderr, probe, time.Second)
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

func TestRunPiHealthCmd_RespectsTimeout(t *testing.T) {
	t.Parallel()
	probe := func(ctx context.Context) *pihealth.PiHealth {
		// Block until ctx fires, then return a canned "na" result the
		// way pihealth.Reader.Probe would if all sub-probes were
		// cancelled.
		<-ctx.Done()
		return &pihealth.PiHealth{Severity: "na", Summary: "probe failed"}
	}
	var stdout, stderr bytes.Buffer
	start := time.Now()
	code := runPiHealthCmd(&stdout, &stderr, probe, 50*time.Millisecond)
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
