package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// TestOrchestratorStart_UnavailableReturns503 covers the capability gate
// on the start endpoint. With the orchestrator surfaces absent — the
// production default on any machine that doesn't have the image-owned
// trampoline / overlay-target — the start path must hard-refuse with
// 503 + {"reason":"orchestrator_unavailable"} so the SPA can render the
// per-step fallback rather than queue a sudo call that would either
// fail at the privileged-exec layer or worse, succeed against a stale
// binary.
func TestOrchestratorStart_UnavailableReturns503(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t, withOrchestratorCapable(false))
	r := postJSON(t, h.client, h.ts.URL+"/api/orchestrator/start", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["reason"] != "orchestrator_unavailable" {
		t.Errorf("reason = %q, want orchestrator_unavailable", got["reason"])
	}
	// The privileged argv must NOT have been invoked — the capability
	// gate sits in front of the start path.
	for _, c := range h.callsCopy() {
		if len(c) >= 2 && c[1] == "orchestrator" {
			t.Fatalf("StartOrchestrator argv invoked despite 503: %v", c)
		}
	}
}

// TestOrchestratorState_UnavailableReturnsUnavailable covers the same
// capability gate on the state endpoint. The endpoint is informational —
// the UI uses it to decide whether to render the unified button — so
// the failure response is a 200 with {"step":"unavailable"}, not a 503.
func TestOrchestratorState_UnavailableReturnsUnavailable(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t, withOrchestratorCapable(false))
	r, err := h.client.Get(h.ts.URL + "/api/orchestrator/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["step"] != "unavailable" {
		t.Errorf("step = %q, want unavailable", got["step"])
	}
}

// TestOrchestratorState_CapableButMissingStateFileReturnsIdle is the
// normal post-boot state on a feeder that has the orchestrator
// available but hasn't run it since boot. /run is tmpfs, so the state
// file is genuinely absent — not "unknown" or 500.
func TestOrchestratorState_CapableButMissingStateFileReturnsIdle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "orchestrator.state")
	h := newWriteHarness(t,
		withOrchestratorCapable(true),
		withOrchestratorStatePath(missing),
	)
	r, err := h.client.Get(h.ts.URL + "/api/orchestrator/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["step"] != "idle" {
		t.Errorf("step = %q, want idle", got["step"])
	}
}

// TestOrchestratorState_CapableForwardsValidJSON covers the happy path:
// the orchestrator has written a state file and the endpoint forwards
// its bytes verbatim. We assert on round-tripped fields so a future
// schema field the orchestrator adds reaches the SPA without a webconfig
// release.
func TestOrchestratorState_CapableForwardsValidJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "orchestrator.state")
	payload := `{"step":"feed","status":"running","started_at":"2026-05-22T10:00:00Z","apt_irreversible":false}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newWriteHarness(t,
		withOrchestratorCapable(true),
		withOrchestratorStatePath(path),
	)
	r, err := h.client.Get(h.ts.URL + "/api/orchestrator/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	want := map[string]any{
		"step":             "feed",
		"status":           "running",
		"started_at":       "2026-05-22T10:00:00Z",
		"apt_irreversible": false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("body = %#v, want %#v", got, want)
	}
}

// TestOrchestratorState_CorruptFileReturns500 covers the failure-mode
// reporting: a half-written state file (or anything that doesn't
// unmarshal as JSON) returns 500 with {"reason":"state_corrupt"}, not
// the corrupt bytes verbatim — the SPA can't act on garbage.
func TestOrchestratorState_CorruptFileReturns500(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "orchestrator.state")
	// Plain text that doesn't parse as JSON.
	if err := os.WriteFile(path, []byte("not json garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newWriteHarness(t,
		withOrchestratorCapable(true),
		withOrchestratorStatePath(path),
	)
	r, err := h.client.Get(h.ts.URL + "/api/orchestrator/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["reason"] != "state_corrupt" {
		t.Errorf("reason = %q, want state_corrupt", got["reason"])
	}
}

// TestOrchestratorStart_AlreadyRunningReturns409 covers the in-flight
// guard: a state file describing a non-terminal step (running through
// any of apt / feed / webconfig / runtime) blocks a second start.
func TestOrchestratorStart_AlreadyRunningReturns409(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "orchestrator.state")
	payload := `{"step":"feed","status":"running","started_at":"2026-05-22T10:00:00Z"}`
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newWriteHarness(t,
		withOrchestratorCapable(true),
		withOrchestratorStatePath(path),
	)
	r := postJSON(t, h.client, h.ts.URL+"/api/orchestrator/start", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["reason"] != "already_running" {
		t.Errorf("reason = %q, want already_running", got["reason"])
	}
	for _, c := range h.callsCopy() {
		if len(c) >= 2 && c[1] == "orchestrator" {
			t.Fatalf("StartOrchestrator argv invoked despite 409: %v", c)
		}
	}
}

// TestOrchestratorStart_TerminalStateAllowsRestart covers the inverse
// of the running-check: a `done` (or `failed`) step in the state file
// is a leftover from the prior run and must NOT block a new one. The
// state file persists on /run/ across the orchestrator process exit
// until something else clobbers it; the next start has to be allowed.
func TestOrchestratorStart_TerminalStateAllowsRestart(t *testing.T) {
	t.Parallel()
	for _, step := range []string{"done", "failed", "idle"} {
		step := step
		t.Run(step, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "orchestrator.state")
			payload := `{"step":"` + step + `","status":"ok"}`
			if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
				t.Fatal(err)
			}
			h := newWriteHarness(t,
				withOrchestratorCapable(true),
				withOrchestratorStatePath(path),
			)
			r := postJSON(t, h.client, h.ts.URL+"/api/orchestrator/start", map[string]any{})
			defer r.Body.Close()
			if r.StatusCode != http.StatusAccepted {
				t.Fatalf("status = %d, want 202", r.StatusCode)
			}
			// The privileged argv must have been invoked.
			saw := false
			for _, c := range h.callsCopy() {
				if len(c) >= 2 && c[0] == "sudo-stub" && c[1] == "orchestrator" {
					saw = true
				}
			}
			if !saw {
				t.Errorf("StartOrchestrator argv not invoked; calls=%v", h.callsCopy())
			}
		})
	}
}

// TestOrchestratorStart_HappyPathInvokesPrivilegedArgv covers the
// capable + no-state-file path: the start endpoint must invoke the
// sudoers-pinned argv and respond 202 with the unit name + started_at.
func TestOrchestratorStart_HappyPathInvokesPrivilegedArgv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "orchestrator.state")
	h := newWriteHarness(t,
		withOrchestratorCapable(true),
		withOrchestratorStatePath(missing),
	)
	r := postJSON(t, h.client, h.ts.URL+"/api/orchestrator/start", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["unit"] != "airplanes-update-orchestrator.service" {
		t.Errorf("unit = %q, want airplanes-update-orchestrator.service", got["unit"])
	}
	if got["status"] != "running" {
		t.Errorf("status = %q, want running", got["status"])
	}
	want := []string{"sudo-stub", "orchestrator"}
	saw := false
	for _, c := range h.callsCopy() {
		if reflect.DeepEqual(c, want) {
			saw = true
		}
	}
	if !saw {
		t.Errorf("StartOrchestrator argv %v not invoked; calls=%v", want, h.callsCopy())
	}
}

// TestOrchestratorStart_RequiresAuth pins the requireSession wiring.
func TestOrchestratorStart_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/orchestrator/start", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

// TestOrchestratorState_RequiresAuth pins the requireSession wiring.
func TestOrchestratorState_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r, err := c.Get(ts.URL + "/api/orchestrator/state")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

// TestDefaultPrivilegedArgv_SudoersParity_CoversOrchestrator extends
// the existing parity coverage with a dedicated check that
// StartOrchestrator appears in the in-tree sudoers file. The full
// parity sweep is in TestValidatePrivilegedArgvParity_PassesAgainstInTreeFiles,
// which already covers every argv; this case fails loudly with the
// orchestrator field name in the error message if a future refactor
// drops the entry from sudoers or skips it in privilegedArgvCases.
func TestDefaultPrivilegedArgv_SudoersParity_CoversOrchestrator(t *testing.T) {
	t.Parallel()
	if err := ValidatePrivilegedArgvParity(
		DefaultPrivilegedArgv(),
		filepath.Join("..", "..", "files", "etc", "sudoers.d", "010_airplanes-webconfig"),
	); err != nil {
		t.Fatalf("parity check failed against in-tree sudoers: %v", err)
	}
	// The default argv must match the literal sudoers line so a stray
	// rename of the trampoline doesn't pass parity via the round-trip
	// in privilegedArgvCases.
	want := []string{
		"/usr/bin/sudo", "-n",
		"/usr/bin/systemd-run",
		"--unit=airplanes-update-orchestrator.service",
		"--collect",
		"--property=ExecStopPost=/usr/bin/systemctl kill -s HUP airplanes-webconfig.service",
		"/usr/local/lib/airplanes-webconfig/start-orchestrator.sh",
	}
	got := DefaultPrivilegedArgv().StartOrchestrator
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("StartOrchestrator = %v, want %v", got, want)
	}
}

// TestOrchestratorStart_RefusedDuringMaintenance covers the no-overlap
// guard: a busy apt upgrade / feed update / prior orchestrator unit must
// block a start. The orchestrator drives all of them under the hood, so an
// overlapping kickoff would deadlock on dpkg or corrupt release state.
func TestOrchestratorStart_RefusedDuringMaintenance(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{
		"airplanes-system-upgrade.service",
		"airplanes-update.service",
		"airplanes-update-orchestrator.service",
	} {
		unit := unit
		t.Run(unit, func(t *testing.T) {
			t.Parallel()
			h := newWriteHarness(t, withOrchestratorCapable(true))
			h.mu.Lock()
			h.runnerResultFor = activeFor(unit)
			h.mu.Unlock()
			r := postJSON(t, h.client, h.ts.URL+"/api/orchestrator/start", map[string]any{})
			defer r.Body.Close()
			if r.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", r.StatusCode)
			}
			// StartOrchestrator argv must not have been invoked.
			for _, c := range h.callsCopy() {
				if len(c) >= 2 && c[1] == "orchestrator" {
					t.Fatalf("StartOrchestrator argv invoked despite 409: %v", c)
				}
			}
		})
	}
}

// TestOrchestratorStart_SystemdRunAlreadyExistsReturns409 covers the
// rare race where systemd-run reports the transient unit name already
// taken — both the running-state probe and the maintenance guard
// missed it (e.g. the transient unit cleanup is still in flight despite
// --collect, or two starts raced through the lock from different
// authenticated sessions). The stderr-contains heuristic on the
// existing transient endpoints maps to 409; the orchestrator endpoint
// must too.
//
// The state path is pinned to a fresh tempfile so the test exercises
// the systemd-run branch — not the running-state probe branch — and
// the test asserts the orchestrator argv WAS invoked. Without these
// guards, the test could quietly pass against the running-state probe
// on a machine where /run/airplanes/orchestrator.state happens to
// exist.
func TestOrchestratorStart_SystemdRunAlreadyExistsReturns409(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "orchestrator.state")
	h := newWriteHarness(t,
		withOrchestratorCapable(true),
		withOrchestratorStatePath(missing),
	)
	h.mu.Lock()
	h.runnerErrFor = func(argv []string) error {
		if len(argv) >= 2 && argv[1] == "orchestrator" {
			return errors.New("systemd-run failed")
		}
		return nil
	}
	h.runnerResultFor = func(argv []string) wexec.Result {
		if len(argv) >= 2 && argv[1] == "orchestrator" {
			return wexec.Result{Stderr: []byte("Unit airplanes-update-orchestrator.service already exists")}
		}
		return wexec.Result{}
	}
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/orchestrator/start", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["reason"] != "already_running" {
		t.Errorf("reason = %q, want already_running", got["reason"])
	}
	// Confirm we actually hit the systemd-run branch (not the
	// running-state probe). The orchestrator argv must appear in the
	// runner call log.
	saw := false
	for _, c := range h.callsCopy() {
		if len(c) >= 2 && c[0] == "sudo-stub" && c[1] == "orchestrator" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("StartOrchestrator argv not invoked; want the systemd-run branch to fire; calls=%v", h.callsCopy())
	}
}

// TestOrchestratorRunning_NonTerminalSteps walks the orchestrator's
// step names and asserts which count as "still running" for the start
// path's in-flight guard.
func TestOrchestratorRunning_NonTerminalSteps(t *testing.T) {
	t.Parallel()
	cases := []struct {
		step    string
		running bool
	}{
		{"idle", false},
		{"done", false},
		{"failed", false},
		{"", false},
		{"apt", true},
		{"feed", true},
		{"webconfig", true},
		{"runtime", true},
		{"rollback", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.step, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "orchestrator.state")
			payload := `{"step":"` + tc.step + `"}`
			if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := orchestratorRunning(path)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.running {
				t.Fatalf("orchestratorRunning(%q) = %v, want %v", tc.step, got, tc.running)
			}
		})
	}
}

// TestOrchestratorRunning_CorruptOrEmptyFails returns running=true and
// a non-nil error: an unreadable or unparseable state file means we
// can't prove no run is in progress, so the start path must refuse.
func TestOrchestratorRunning_CorruptOrEmptyFails(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "  ", "not json"} {
		body := body
		t.Run(strings.ReplaceAll(body, " ", "_"), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "orchestrator.state")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			running, err := orchestratorRunning(path)
			if !running || err == nil {
				t.Fatalf("orchestratorRunning(corrupt %q) = (%v, %v), want (true, non-nil)", body, running, err)
			}
		})
	}
}
