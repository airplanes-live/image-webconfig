package status

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

func newTestPaths(t *testing.T) (Paths, string) {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		ManifestFile:     filepath.Join(dir, "build-manifest.json"),
		AircraftJSONFile: filepath.Join(dir, "aircraft.json"),
		SystemctlBinary:  "/usr/bin/systemctl",
		IsActiveTimeout:  2 * time.Second,
	}, dir
}

// fixedRunner returns the supplied result for any argv.
func fixedRunner(stdout string, exitErr error) wexec.CommandRunner {
	return func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{Stdout: []byte(stdout)}, exitErr
	}
}

func TestRead_AllUnitsActive(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader("v1", p, fixedRunner("active\n", nil))
	got, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v1" {
		t.Errorf("Version = %q, want v1", got.Version)
	}
	for _, unit := range MonitoredServices {
		if got.Services[unit] != "active" {
			t.Errorf("Services[%s] = %q, want active", unit, got.Services[unit])
		}
	}
}

func TestRead_InactiveUnitReportsInactive(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader("v", p, fixedRunner("inactive\n", errors.New("exit 3")))
	got, _ := r.Read(context.Background())
	for _, unit := range MonitoredServices {
		if got.Services[unit] != "inactive" {
			t.Errorf("Services[%s] = %q, want inactive", unit, got.Services[unit])
		}
	}
}

func TestRead_TimeoutReportsUnknown(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	p.IsActiveTimeout = 5 * time.Millisecond
	slowRunner := func(ctx context.Context, _ []string) (wexec.Result, error) {
		<-ctx.Done()
		return wexec.Result{}, ctx.Err()
	}
	r := NewReader("v", p, slowRunner)
	got, _ := r.Read(context.Background())
	for _, unit := range MonitoredServices {
		if got.Services[unit] != "unknown" {
			t.Errorf("Services[%s] = %q on timeout, want unknown", unit, got.Services[unit])
		}
	}
}

func TestRead_ManifestPassthrough(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	manifest := []byte(`{"schema_version":1,"channel":"dev","arch":"arm64"}`)
	if err := os.WriteFile(p.ManifestFile, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewReader("v", p, fixedRunner("active", nil))
	got, _ := r.Read(context.Background())
	var roundtrip map[string]any
	if err := json.Unmarshal(got.Manifest, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip["channel"] != "dev" {
		t.Errorf("manifest channel = %v, want dev", roundtrip["channel"])
	}
}

func TestRead_ManifestMissingOmitted(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader("v", p, fixedRunner("active", nil))
	got, _ := r.Read(context.Background())
	if got.Manifest != nil {
		t.Errorf("Manifest = %s, want nil (file missing)", got.Manifest)
	}
}

func TestRead_ManifestCorruptOmitted(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.ManifestFile, []byte("not json"), 0o644)
	r := NewReader("v", p, fixedRunner("active", nil))
	got, _ := r.Read(context.Background())
	if got.Manifest != nil {
		t.Errorf("Manifest passed through corrupt JSON: %s", got.Manifest)
	}
}

func TestRead_AircraftSummary(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	body := `{"now":1717000000,"messages":1234,"aircraft":[{},{},{}]}`
	_ = os.WriteFile(p.AircraftJSONFile, []byte(body), 0o644)
	r := NewReader("v", p, fixedRunner("active", nil))
	got, _ := r.Read(context.Background())
	if got.Feed == nil {
		t.Fatal("Feed = nil, want populated")
	}
	if got.Feed.AircraftCount != 3 {
		t.Errorf("AircraftCount = %d, want 3", got.Feed.AircraftCount)
	}
	if got.Feed.MessagesCounter != 1234 {
		t.Errorf("MessagesCounter = %d, want 1234", got.Feed.MessagesCounter)
	}
}

func TestRead_AircraftMissingOmitted(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader("v", p, fixedRunner("active", nil))
	got, _ := r.Read(context.Background())
	if got.Feed != nil {
		t.Errorf("Feed = %+v, want nil (file missing)", *got.Feed)
	}
}

// Per-unit argv: ensure systemctl is invoked with `is-active <unit>` shape.
func TestRead_PassesIsActiveArgvForEachUnit(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	var (
		mu       sync.Mutex
		captured []string
	)
	captureRunner := func(_ context.Context, argv []string) (wexec.Result, error) {
		mu.Lock()
		captured = append(captured, argv[2])
		mu.Unlock()
		return wexec.Result{Stdout: []byte("active")}, nil
	}
	r := NewReader("v", p, captureRunner)
	_, _ = r.Read(context.Background())
	mu.Lock()
	defer mu.Unlock()
	for _, unit := range MonitoredServices {
		found := false
		for _, c := range captured {
			if c == unit {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unit %s not queried", unit)
		}
	}
	for _, c := range captured {
		if !strings.HasSuffix(c, ".service") {
			t.Errorf("queried argv ends with %q, want .service-suffixed unit", c)
		}
	}
}
