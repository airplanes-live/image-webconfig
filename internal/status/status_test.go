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
	"github.com/airplanes-live/image/webconfig/internal/pihealth"
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

// --- Daemon decision (PR 3) ---

// perArgvRunner dispatches based on the systemctl subcommand. Lets tests
// stub `is-active <unit>` and `show --property=ExecMainStatus --value <unit>`
// independently per unit.
func perArgvRunner(handlers map[string]func(unit string) (string, error)) wexec.CommandRunner {
	return func(_ context.Context, argv []string) (wexec.Result, error) {
		// argv shape:
		//   [systemctl-bin, is-active, <unit>]                                  (3 args)
		//   [systemctl-bin, show, --property=ExecMainStatus, --value, <unit>]   (5 args)
		if len(argv) >= 3 && argv[1] == "is-active" {
			h, ok := handlers["is-active:"+argv[2]]
			if !ok {
				h = handlers["is-active:default"]
			}
			if h == nil {
				return wexec.Result{Stdout: []byte("active")}, nil
			}
			out, err := h(argv[2])
			return wexec.Result{Stdout: []byte(out)}, err
		}
		if len(argv) >= 5 && argv[1] == "show" && argv[2] == "--property=ExecMainStatus" {
			h, ok := handlers["show-exec-main-status:"+argv[4]]
			if !ok {
				h = handlers["show-exec-main-status:default"]
			}
			if h == nil {
				return wexec.Result{Stdout: []byte("0")}, nil
			}
			out, err := h(argv[4])
			return wexec.Result{Stdout: []byte(out)}, err
		}
		return wexec.Result{}, errors.New("perArgvRunner: unhandled argv")
	}
}

func writeStateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newDecisionTestPaths(t *testing.T) Paths {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		ManifestFile:     filepath.Join(dir, "build-manifest.json"),
		AircraftJSONFile: filepath.Join(dir, "aircraft.json"),
		MlatStateFile:    filepath.Join(dir, "run", "airplanes-mlat", "state"),
		FeedStateFile:    filepath.Join(dir, "run", "airplanes-feed", "state"),
		UAT978StateFile:  filepath.Join(dir, "run", "airplanes-978", "state"),
		SystemctlBinary:  "systemctl",
		IsActiveTimeout:  2 * time.Second,
	}
}

func TestRead_MlatDecisionPopulatedWhenActive(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.MlatStateFile, "schema_version=1\nservice=airplanes-mlat\nstate=disabled\nreason=mlat_enabled_false\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:default": func(_ string) (string, error) { return "active", nil },
	}))
	got, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MlatDecision == nil {
		t.Fatalf("MlatDecision = nil, want non-nil")
	}
	if got.MlatDecision.State != "disabled" {
		t.Errorf("MlatDecision.State = %q, want disabled", got.MlatDecision.State)
	}
	if got.MlatDecision.Reason != "mlat_enabled_false" {
		t.Errorf("MlatDecision.Reason = %q, want mlat_enabled_false", got.MlatDecision.Reason)
	}
}

func TestRead_MlatDecisionNilWhenStateFileAbsent(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:default": func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.MlatDecision != nil {
		t.Errorf("MlatDecision = %+v, want nil (no state file)", got.MlatDecision)
	}
}

func TestRead_MlatDecisionNilWhenInactive(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	// Even with a state file present, the reader must NOT consult it when
	// the unit is inactive — could be stale across an unmount.
	writeStateFile(t, p.MlatStateFile, "schema_version=1\nstate=enabled\nreason=ok\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-mlat.service": func(_ string) (string, error) { return "inactive", errors.New("exit 3") },
		"is-active:default":                func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.MlatDecision != nil {
		t.Errorf("MlatDecision = %+v, want nil for inactive unit", got.MlatDecision)
	}
}

func TestRead_MlatDecisionPopulatedOnFailedExit64(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.MlatStateFile, "schema_version=1\nstate=misconfigured\nreason=mlat_private_invalid\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-mlat.service":             func(_ string) (string, error) { return "failed", errors.New("exit 3") },
		"is-active:default":                            func(_ string) (string, error) { return "active", nil },
		"show-exec-main-status:airplanes-mlat.service": func(_ string) (string, error) { return "64", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.MlatDecision == nil {
		t.Fatalf("MlatDecision = nil, want non-nil for failed+exit-64")
	}
	if got.MlatDecision.State != "misconfigured" {
		t.Errorf("MlatDecision.State = %q, want misconfigured", got.MlatDecision.State)
	}
	if got.MlatDecision.Reason != "mlat_private_invalid" {
		t.Errorf("MlatDecision.Reason = %q, want mlat_private_invalid", got.MlatDecision.Reason)
	}
}

func TestRead_MlatDecisionNilOnFailedExitNon64(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.MlatStateFile, "schema_version=1\nstate=misconfigured\nreason=mlat_private_invalid\n")
	// failed+ExecMainStatus=1 → not the strict-misconfig shape; don't
	// consult the (potentially stale) state file.
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-mlat.service":             func(_ string) (string, error) { return "failed", errors.New("exit 3") },
		"is-active:default":                            func(_ string) (string, error) { return "active", nil },
		"show-exec-main-status:airplanes-mlat.service": func(_ string) (string, error) { return "1", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.MlatDecision != nil {
		t.Errorf("MlatDecision = %+v, want nil for failed+exit-1", got.MlatDecision)
	}
}

func TestRead_MlatDecisionNilOnUnknownStateToken(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	// Forward-compat: a future schema_version=1 with an unknown decision
	// token surfaces as nil so the JS dashboard falls back to legacy
	// classification rather than rendering the unknown token.
	writeStateFile(t, p.MlatStateFile, "schema_version=1\nstate=future_unknown_token\nreason=future_reason\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:default": func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.MlatDecision != nil {
		t.Errorf("MlatDecision = %+v, want nil for unknown decision token", got.MlatDecision)
	}
}

func TestRead_FeedDecisionPopulatedWhenActive(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.FeedStateFile, "schema_version=1\nservice=airplanes-feed\nstate=enabled\nreason=ok\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:default": func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.FeedDecision == nil {
		t.Fatalf("FeedDecision = nil, want non-nil")
	}
	if got.FeedDecision.State != "enabled" {
		t.Errorf("FeedDecision.State = %q, want enabled", got.FeedDecision.State)
	}
}

func TestRead_FeedDecisionNilOnFailed(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	// Feed has no exit-64 special case; a failed feed daemon doesn't
	// surface a decision (no strict-misconfig path on the feed side).
	writeStateFile(t, p.FeedStateFile, "schema_version=1\nstate=enabled\nreason=ok\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-feed.service": func(_ string) (string, error) { return "failed", errors.New("exit 3") },
		"is-active:default":                func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.FeedDecision != nil {
		t.Errorf("FeedDecision = %+v, want nil for failed feed unit", got.FeedDecision)
	}
}

// ---- UAT decision (PR 4) — mirrors MLAT shape on airplanes-978.service ----

func TestRead_UATDecisionPopulatedWhenActive(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.UAT978StateFile, "schema_version=1\nservice=airplanes-978\nstate=enabled\nreason=ok\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:default": func(_ string) (string, error) { return "active", nil },
	}))
	got, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.UATDecision == nil {
		t.Fatalf("UATDecision = nil, want non-nil")
	}
	if got.UATDecision.State != "enabled" {
		t.Errorf("UATDecision.State = %q, want enabled", got.UATDecision.State)
	}
	if got.UATDecision.Reason != "ok" {
		t.Errorf("UATDecision.Reason = %q, want ok", got.UATDecision.Reason)
	}
}

func TestRead_UATDecisionPopulatedOnFailedExit64(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.UAT978StateFile, "schema_version=1\nstate=disabled\nreason=uat_disabled\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-978.service":             func(_ string) (string, error) { return "failed", errors.New("exit 3") },
		"is-active:default":                           func(_ string) (string, error) { return "active", nil },
		"show-exec-main-status:airplanes-978.service": func(_ string) (string, error) { return "64", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.UATDecision == nil {
		t.Fatalf("UATDecision = nil, want non-nil for failed+exit-64")
	}
	if got.UATDecision.State != "disabled" {
		t.Errorf("UATDecision.State = %q, want disabled", got.UATDecision.State)
	}
	if got.UATDecision.Reason != "uat_disabled" {
		t.Errorf("UATDecision.Reason = %q, want uat_disabled", got.UATDecision.Reason)
	}
}

func TestRead_UATDecisionMisconfigOnFailedExit64(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.UAT978StateFile, "schema_version=1\nstate=misconfigured\nreason=uat_input_invalid\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-978.service":             func(_ string) (string, error) { return "failed", errors.New("exit 3") },
		"is-active:default":                           func(_ string) (string, error) { return "active", nil },
		"show-exec-main-status:airplanes-978.service": func(_ string) (string, error) { return "64", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.UATDecision == nil {
		t.Fatalf("UATDecision = nil, want non-nil for failed+exit-64 misconfig")
	}
	if got.UATDecision.State != "misconfigured" {
		t.Errorf("UATDecision.State = %q", got.UATDecision.State)
	}
	if got.UATDecision.Reason != "uat_input_invalid" {
		t.Errorf("UATDecision.Reason = %q", got.UATDecision.Reason)
	}
}

func TestRead_UATDecisionNilOnFailedExitNon64(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.UAT978StateFile, "schema_version=1\nstate=enabled\nreason=ok\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-978.service":             func(_ string) (string, error) { return "failed", errors.New("exit 3") },
		"is-active:default":                           func(_ string) (string, error) { return "active", nil },
		"show-exec-main-status:airplanes-978.service": func(_ string) (string, error) { return "1", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.UATDecision != nil {
		t.Errorf("UATDecision = %+v, want nil for failed+exit-1 (real failure, not strict misconfig)", got.UATDecision)
	}
}

func TestRead_UATDecisionNilOnInactive(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	// Inactive 978 unit (e.g., never started yet). Even with a stale state
	// file lying around, the reader should NOT consult it.
	writeStateFile(t, p.UAT978StateFile, "schema_version=1\nstate=enabled\nreason=ok\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:airplanes-978.service": func(_ string) (string, error) { return "inactive", errors.New("exit 3") },
		"is-active:default":               func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.UATDecision != nil {
		t.Errorf("UATDecision = %+v, want nil for inactive 978 unit", got.UATDecision)
	}
}

// Cross-owner reason rejection: a 978 state file claiming an MLAT-only reason
// (e.g. `mlat_enabled_false`) is dropped by the owner-aware reason whitelist —
// AllowedReasons978 doesn't include it.
func TestRead_UATDecisionRejectsCrossOwnerReason(t *testing.T) {
	t.Parallel()
	p := newDecisionTestPaths(t)
	writeStateFile(t, p.UAT978StateFile, "schema_version=1\nstate=misconfigured\nreason=mlat_enabled_false\n")
	r := NewReader("v", p, perArgvRunner(map[string]func(unit string) (string, error){
		"is-active:default": func(_ string) (string, error) { return "active", nil },
	}))
	got, _ := r.Read(context.Background())
	if got.UATDecision != nil {
		t.Errorf("UATDecision = %+v, want nil (mlat_enabled_false is not a valid 978 reason)", got.UATDecision)
	}
}

func TestRead_RebootRequiredFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		create bool
		want   bool
	}{
		{"present", true, true},
		{"absent", false, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, dir := newTestPaths(t)
			p.RebootRequiredFile = filepath.Join(dir, "reboot-required")
			if tc.create {
				if err := os.WriteFile(p.RebootRequiredFile, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			r := NewReader("v", p, fixedRunner("active", nil))
			got, err := r.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.RebootRequired != tc.want {
				t.Errorf("RebootRequired = %v, want %v", got.RebootRequired, tc.want)
			}
		})
	}
}

// stubPiHealthProbe returns a fixed *pihealth.PiHealth.
type stubPiHealthProbe struct{ payload *pihealth.PiHealth }

func (s stubPiHealthProbe) Probe(_ context.Context) *pihealth.PiHealth { return s.payload }

func TestRead_PiHealthEmbedded(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	payload := &pihealth.PiHealth{
		Severity:       "ok",
		Summary:        "healthy · 56°C",
		IsRaspberryPi:  true,
		ThrottleProbed: true,
		TempProbed:     true,
		CPUTempCelsius: 56,
	}
	r := NewReader("v", p, fixedRunner("active", nil), WithPiHealth(stubPiHealthProbe{payload}))
	got, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.PiHealth == nil {
		t.Fatal("PiHealth = nil, want populated")
	}
	if got.PiHealth.Summary != "healthy · 56°C" {
		t.Errorf("PiHealth.Summary = %q", got.PiHealth.Summary)
	}
	blob, _ := json.Marshal(got)
	if !strings.Contains(string(blob), `"pi_health"`) {
		t.Errorf("marshaled JSON missing pi_health field: %s", blob)
	}
}

func TestRead_NoPiHealthOption_OmitsField(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader("v", p, fixedRunner("active", nil))
	got, _ := r.Read(context.Background())
	if got.PiHealth != nil {
		t.Errorf("PiHealth = %+v, want nil (no WithPiHealth)", got.PiHealth)
	}
	blob, _ := json.Marshal(got)
	if strings.Contains(string(blob), `"pi_health"`) {
		t.Errorf("marshaled JSON should omit pi_health: %s", blob)
	}
}
