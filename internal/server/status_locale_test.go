package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/hardware"
	"github.com/airplanes-live/image-webconfig/internal/status"
)

type stubHardwareProbe struct{ snap *hardware.Snapshot }

func (s stubHardwareProbe) Probe(context.Context) *hardware.Snapshot { return s.snap }

func statusReaderWithHardware(t *testing.T, snap *hardware.Snapshot) *status.Reader {
	t.Helper()
	paths := status.Paths{
		ManifestFile:     filepath.Join(t.TempDir(), "no-manifest.json"),
		AircraftJSONFile: filepath.Join(t.TempDir(), "no-aircraft.json"),
		SystemctlBinary:  "/usr/bin/systemctl",
		IsActiveTimeout:  time.Second,
	}
	runner := func(context.Context, []string) (wexec.Result, error) {
		return wexec.Result{Stdout: []byte("active\n")}, nil
	}
	opts := []status.Option{}
	if snap != nil {
		opts = append(opts, status.WithHardware(stubHardwareProbe{snap: snap}))
	}
	return status.NewReader("test-sha", paths, runner, opts...)
}

func getStatus(t *testing.T, reader *status.Reader, acceptLang string) (map[string]any, *http.Response) {
	t.Helper()
	s := &Server{status: reader}
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	if acceptLang != "" {
		req.Header.Set("Accept-Language", acceptLang)
	}
	rec := httptest.NewRecorder()
	s.handleStatus(rec, req)
	resp := rec.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body, resp
}

func hwSummary(t *testing.T, body map[string]any) string {
	t.Helper()
	hh, ok := body["hardware_health"].(map[string]any)
	if !ok {
		t.Fatalf("hardware_health missing or wrong type: %v", body["hardware_health"])
	}
	return hh["summary"].(string)
}

func TestHandleStatus_FahrenheitForUSLocale(t *testing.T) {
	c := 56.0
	reader := statusReaderWithHardware(t, &hardware.Snapshot{
		System: hardware.System{CPUTempCelsius: &c},
		Health: hardware.Health{Severity: "ok", Summary: "healthy · 56°C", IsRaspberryPi: true},
	})
	body, resp := getStatus(t, reader, "en-US,en;q=0.9")

	if body["temp_unit"] != "F" {
		t.Errorf("temp_unit = %v, want F", body["temp_unit"])
	}
	if got := hwSummary(t, body); got != "healthy · 133°F" {
		t.Errorf("summary = %q, want healthy · 133°F", got)
	}
	// Raw value stays canonical Celsius.
	sys := body["system"].(map[string]any)
	if sys["cpu_temp_c"].(float64) != 56.0 {
		t.Errorf("system.cpu_temp_c = %v, want 56", sys["cpu_temp_c"])
	}
	if v := resp.Header.Get("Vary"); !strings.Contains(v, "Accept-Language") {
		t.Errorf("Vary = %q, want to contain Accept-Language", v)
	}
}

func TestHandleStatus_CelsiusForNonUSLocale(t *testing.T) {
	c := 56.0
	reader := statusReaderWithHardware(t, &hardware.Snapshot{
		System: hardware.System{CPUTempCelsius: &c},
		Health: hardware.Health{Severity: "ok", Summary: "healthy · 56°C", IsRaspberryPi: true},
	})
	body, _ := getStatus(t, reader, "de-DE,de;q=0.9")

	if body["temp_unit"] != "C" {
		t.Errorf("temp_unit = %v, want C", body["temp_unit"])
	}
	if got := hwSummary(t, body); got != "healthy · 56°C" {
		t.Errorf("summary = %q, want unchanged Celsius", got)
	}
}

func TestHandleStatus_NoHardwareHealthDoesNotPanic(t *testing.T) {
	reader := statusReaderWithHardware(t, nil) // no hardware probe wired
	body, _ := getStatus(t, reader, "en-US")

	if body["temp_unit"] != "F" {
		t.Errorf("temp_unit = %v, want F", body["temp_unit"])
	}
	if _, present := body["hardware_health"]; present {
		t.Errorf("hardware_health should be omitted when no probe is wired")
	}
}
