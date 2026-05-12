package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/auth"
	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
	"github.com/airplanes-live/image/webconfig/internal/feedenv"
	"github.com/airplanes-live/image/webconfig/internal/identity"
	"github.com/airplanes-live/image/webconfig/internal/logs"
	"github.com/airplanes-live/image/webconfig/internal/schemacache"
	"github.com/airplanes-live/image/webconfig/internal/status"
)

const testPassword = "correct horse battery staple"

// fastTestParams keeps argon2 hashes fast (~1ms vs 50ms).
var fastTestParams = auth.Params{TimeCost: 1, MemoryKB: 8, Threads: 1, KeyLen: 32, SaltLen: 16}

// newTestServer wires server.Deps with in-memory components and a tempfile-
// backed PasswordStore. Read-side deps (identity / feedenv / status / logs)
// are stubbed with deterministic fixtures.
func newTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	dir := t.TempDir()
	hashPath := filepath.Join(dir, "password.hash")
	guard, err := auth.NewHashGuard(2)
	if err != nil {
		t.Fatal(err)
	}

	idPaths := identity.Paths{
		FeederIDFile:    filepath.Join(dir, "feeder-id"),
		ClaimSecretFile: filepath.Join(dir, "feeder-claim-secret"),
		ClaimPageURL:    "https://airplanes.live/feeder/claim",
	}
	_ = os.WriteFile(idPaths.FeederIDFile, []byte("test-feeder-id"), 0o644)
	_ = os.WriteFile(idPaths.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP"), 0o640)

	feedEnvPath := filepath.Join(dir, "feed.env")
	_ = os.WriteFile(feedEnvPath,
		[]byte(`LATITUDE=51.5`+"\n"+`LONGITUDE=-0.1`+"\n"+`MLAT_USER=tester`+"\n"+`MLAT_ENABLED=true`+"\n"),
		0o644,
	)

	statusPaths := status.Paths{
		ManifestFile:     filepath.Join(dir, "build-manifest.json"),
		AircraftJSONFile: filepath.Join(dir, "aircraft.json"),
		SystemctlBinary:  "/usr/bin/systemctl",
		IsActiveTimeout:  time.Second,
	}
	statusRunner := func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{Stdout: []byte("active\n")}, nil
	}
	logsRunner := func(_ context.Context, w io.Writer, _ []string) error {
		_, _ = w.Write([]byte("test-log-line\n"))
		return nil
	}

	// Capture every privileged shell-out so write-endpoint tests can assert
	// the argv shape that hit sudo.
	var (
		runnerCalls      [][]string
		stdinRunnerCalls []stdinCall
		runnerMu         sync.Mutex
	)
	captureRunner := func(_ context.Context, argv []string) (wexec.Result, error) {
		runnerMu.Lock()
		runnerCalls = append(runnerCalls, append([]string(nil), argv...))
		runnerMu.Unlock()
		return wexec.Result{}, nil
	}
	captureStdinRunner := func(_ context.Context, argv []string, stdin io.Reader) (wexec.Result, error) {
		body, _ := io.ReadAll(stdin)
		runnerMu.Lock()
		stdinRunnerCalls = append(stdinRunnerCalls, stdinCall{argv: append([]string(nil), argv...), stdin: body})
		runnerMu.Unlock()
		return wexec.Result{}, nil
	}

	priv := PrivilegedArgv{
		ApplyFeed:          []string{"sudo-stub", "apl-feed", "apply", "--json", "--lock-timeout", "5"},
		SchemaFeed:         []string{"apl-feed", "schema", "--json"},
		Reboot:             []string{"sudo-stub", "reboot"},
		Poweroff:           []string{"sudo-stub", "poweroff"},
		StartUpdate:        []string{"sudo-stub", "update"},
		StartSystemUpgrade: []string{"sudo-stub", "system-upgrade"},
		RegisterClaim:      []string{"sudo-stub", "systemctl", "start", "--no-block", "airplanes-claim.service"},
		WifiList:           []string{"sudo-stub", "apl-wifi", "list", "--json"},
		WifiAdd:            []string{"sudo-stub", "apl-wifi", "add", "--json"},
		WifiUpdate:         []string{"sudo-stub", "apl-wifi", "update", "--json"},
		WifiDelete:         []string{"sudo-stub", "apl-wifi", "delete", "--json"},
		WifiTest:           []string{"sudo-stub", "apl-wifi", "test", "--json"},
		WifiActivate:       []string{"sudo-stub", "apl-wifi", "activate", "--json"},
		WifiStatus:         []string{"sudo-stub", "apl-wifi", "status", "--json"},
	}

	deps := Deps{
		Version:      "test-sha",
		Store:        auth.NewPasswordStore(hashPath),
		Sessions:     auth.NewSessions(time.Hour),
		Lockout:      auth.NewLockout(5, time.Minute, 15*time.Minute),
		Guard:        guard,
		Argon2Params: fastTestParams,
		Identity:     identity.NewReader(idPaths),
		FeedEnv:      &feedenv.Reader{Path: feedEnvPath},
		Status:       status.NewReader("test-sha", statusPaths, statusRunner),
		Logs:         logs.NewStreamer(logsRunner),
		Schema: schemacache.NewPrepopulated(
			[]string{"LATITUDE", "LONGITUDE", "ALTITUDE", "GEO_CONFIGURED", "MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE", "GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN"},
			[]string{"LATITUDE", "LONGITUDE", "ALTITUDE", "GEO_CONFIGURED", "MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE", "INPUT", "INPUT_TYPE", "GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN"},
		),
		Runner:      captureRunner,
		StdinRunner: captureStdinRunner,
		Privileged:  priv,
	}
	handler := New(deps)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	s := &Server{
		version: deps.Version, store: deps.Store, sessions: deps.Sessions,
		lockout: deps.Lockout, guard: deps.Guard, argon2Params: deps.Argon2Params,
	}
	t.Cleanup(func() {
		// Detach captures from goroutine leakage (logs streamer etc.)
		runnerMu.Lock()
		_ = runnerCalls
		_ = stdinRunnerCalls
		runnerMu.Unlock()
	})
	return ts, s
}

// stdinCall captures one stdin-piping shell-out for assertion.
type stdinCall struct {
	argv  []string
	stdin []byte
}

// writeHarness is the test harness for POST /api/config / /api/update /
// /api/reboot — it wires deterministic captures for both runners and
// pre-authenticates the returned client.
type writeHarness struct {
	ts              *httptest.Server
	client          *http.Client
	feedEnvPath     string
	mu              sync.Mutex
	calls           [][]string
	stdinCalls      []stdinCall
	runnerErr       error                            // returned by captureRunner; tests override
	runnerErrFor    func(argv []string) error        // optional: per-argv error; falls back to runnerErr when nil
	runnerResultFor func(argv []string) wexec.Result // optional: per-argv canned Result (stdout+stderr); falls back to zero value
	stdinErr        error
	stdinResult     wexec.Result
}

func newWriteHarness(t *testing.T) *writeHarness {
	t.Helper()
	dir := t.TempDir()
	hashPath := filepath.Join(dir, "password.hash")
	guard, err := auth.NewHashGuard(2)
	if err != nil {
		t.Fatal(err)
	}
	feedEnvPath := filepath.Join(dir, "feed.env")
	if err := os.WriteFile(feedEnvPath,
		[]byte(`LATITUDE="0"`+"\n"+`UAT_INPUT=""`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	h := &writeHarness{
		feedEnvPath: feedEnvPath,
		// Default response from apl-feed apply --json: every write
		// succeeds and lands an `applied` envelope. Individual tests
		// override h.stdinResult / h.stdinErr to exercise rejected,
		// lock_timeout, filesystem_error, etc.
		stdinResult: wexec.Result{Stdout: []byte(`{"status":"applied","changed":[],"pending_restart":[]}`)},
	}
	captureRunner := func(_ context.Context, argv []string) (wexec.Result, error) {
		h.mu.Lock()
		h.calls = append(h.calls, append([]string(nil), argv...))
		var err error
		if h.runnerErrFor != nil {
			err = h.runnerErrFor(argv)
		} else {
			err = h.runnerErr
		}
		var res wexec.Result
		if h.runnerResultFor != nil {
			res = h.runnerResultFor(argv)
		}
		h.mu.Unlock()
		return res, err
	}
	captureStdinRunner := func(_ context.Context, argv []string, stdin io.Reader) (wexec.Result, error) {
		body, _ := io.ReadAll(stdin)
		h.mu.Lock()
		h.stdinCalls = append(h.stdinCalls, stdinCall{argv: append([]string(nil), argv...), stdin: body})
		err := h.stdinErr
		res := h.stdinResult
		h.mu.Unlock()
		return res, err
	}

	priv := PrivilegedArgv{
		ApplyFeed:          []string{"sudo-stub", "apl-feed", "apply", "--json", "--lock-timeout", "5"},
		SchemaFeed:         []string{"apl-feed", "schema", "--json"},
		Reboot:             []string{"sudo-stub", "reboot"},
		Poweroff:           []string{"sudo-stub", "poweroff"},
		StartUpdate:        []string{"sudo-stub", "update"},
		StartSystemUpgrade: []string{"sudo-stub", "system-upgrade"},
		RegisterClaim:      []string{"sudo-stub", "systemctl", "start", "--no-block", "airplanes-claim.service"},
		WifiList:           []string{"sudo-stub", "apl-wifi", "list", "--json"},
		WifiAdd:            []string{"sudo-stub", "apl-wifi", "add", "--json"},
		WifiUpdate:         []string{"sudo-stub", "apl-wifi", "update", "--json"},
		WifiDelete:         []string{"sudo-stub", "apl-wifi", "delete", "--json"},
		WifiTest:           []string{"sudo-stub", "apl-wifi", "test", "--json"},
		WifiActivate:       []string{"sudo-stub", "apl-wifi", "activate", "--json"},
		WifiStatus:         []string{"sudo-stub", "apl-wifi", "status", "--json"},
	}

	deps := Deps{
		Version:      "test-sha",
		Store:        auth.NewPasswordStore(hashPath),
		Sessions:     auth.NewSessions(time.Hour),
		Lockout:      auth.NewLockout(5, time.Minute, 15*time.Minute),
		Guard:        guard,
		Argon2Params: fastTestParams,
		Identity:     identity.NewReader(identity.Paths{FeederIDFile: filepath.Join(dir, "feeder-id")}),
		FeedEnv:      &feedenv.Reader{Path: feedEnvPath},
		Status: status.NewReader("test-sha", status.Paths{
			SystemctlBinary: "/bin/true", IsActiveTimeout: time.Second,
		}, captureRunner),
		Logs: logs.NewStreamer(nil),
		Schema: schemacache.NewPrepopulated(
			[]string{"LATITUDE", "LONGITUDE", "ALTITUDE", "GEO_CONFIGURED", "MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE", "GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN"},
			[]string{"LATITUDE", "LONGITUDE", "ALTITUDE", "GEO_CONFIGURED", "MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE", "INPUT", "INPUT_TYPE", "GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN"},
		),
		Runner:      captureRunner,
		StdinRunner: captureStdinRunner,
		Privileged:  priv,
	}
	h.ts = httptest.NewServer(New(deps))
	t.Cleanup(h.ts.Close)
	h.client = httpClient(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/setup", map[string]string{"password": testPassword})
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d", r.StatusCode)
	}
	return h
}

func (h *writeHarness) callsCopy() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([][]string, len(h.calls))
	copy(out, h.calls)
	return out
}

func (h *writeHarness) stdinCallsCopy() []stdinCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]stdinCall, len(h.stdinCalls))
	copy(out, h.stdinCalls)
	return out
}

// --- write endpoint tests ---

func TestConfigPost_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/config", map[string]any{"updates": map[string]string{}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestConfigPost_AppliedEnvelopeForwarded(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{
		Stdout: []byte(`{"status":"applied","changed":["MLAT_PRIVATE"],"pending_restart":[]}`),
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"MLAT_PRIVATE": "true"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var body applyFeedResponse
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Status != "applied" {
		t.Errorf("status = %q, want applied", body.Status)
	}
	if len(body.Changed) != 1 || body.Changed[0] != "MLAT_PRIVATE" {
		t.Errorf("changed = %v, want [MLAT_PRIVATE]", body.Changed)
	}
}

func TestConfigPost_RejectedEnvelopeReturns400(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{
		Stdout: []byte(`{"status":"rejected","errors":{"LATITUDE":"must be in [-90, 90]"}}`),
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"LATITUDE": "200"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
	var body applyFeedResponse
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Status != "rejected" || body.Errors["LATITUDE"] == "" {
		t.Errorf("body = %+v, want rejected envelope with per-key error", body)
	}
	// The flat `error` field must be populated for the form's
	// `r.payload.error` fallback path. Without this the user sees
	// "save failed" instead of the actual reason.
	if !strings.Contains(body.Error, "LATITUDE") || !strings.Contains(body.Error, "must be in") {
		t.Errorf("body.Error = %q, want it to surface the per-key reason", body.Error)
	}
}

func TestApplyConfigTimeout_ExceedsApplyLockTimeout(t *testing.T) {
	// The HTTP wall-clock budget must sit above the lock-acquisition
	// budget passed to apl-feed, or webconfig will kill the helper
	// mid-wait and surface a generic 500 instead of the structured
	// lock_timeout 503. Pin the relationship as a contract.
	if applyConfigTimeout <= applyLockTimeoutSeconds*time.Second {
		t.Errorf("applyConfigTimeout %v must exceed applyLockTimeoutSeconds %ds",
			applyConfigTimeout, applyLockTimeoutSeconds)
	}
	// And the sudoers-pinned argv must carry the matching --lock-timeout
	// token; otherwise sudo rejects the call entirely.
	argv := DefaultPrivilegedArgv().ApplyFeed
	want := fmt.Sprintf("%d", applyLockTimeoutSeconds)
	found := false
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == "--lock-timeout" && argv[i+1] == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("DefaultPrivilegedArgv().ApplyFeed = %v, want --lock-timeout %s pair", argv, want)
	}
}

func TestConfigPost_ApplyFeedArgvCarriesLockTimeout(t *testing.T) {
	// Live integration: a POST to /api/config must invoke apl-feed
	// with the full sudoers-pinned argv including --lock-timeout 5.
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"MLAT_PRIVATE": "true"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	h.mu.Lock()
	calls := append([]stdinCall(nil), h.stdinCalls...)
	h.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-feed", "apply", "--json", "--lock-timeout", "5"}
	if len(calls[0].argv) != len(want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	for i := range want {
		if calls[0].argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, calls[0].argv[i], want[i])
		}
	}
}

func TestConfigPost_LockTimeoutReturns503(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{
		Stdout: []byte(`{"status":"lock_timeout","message":"could not acquire lock after 30s"}`),
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"MLAT_PRIVATE": "true"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", r.StatusCode)
	}
	var body applyFeedResponse
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Error == "" || !strings.Contains(body.Error, "lock") {
		t.Errorf("body.Error = %q, want it to surface the lock-timeout message", body.Error)
	}
}

func TestConfigPost_PreservesHelperSuppliedError(t *testing.T) {
	// If apl-feed (or a future helper) emits a flat error field directly,
	// synthesizeError must not overwrite it with the joined errors or
	// the generic last-resort string.
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{
		Stdout: []byte(`{"status":"rejected","error":"helper-supplied summary","errors":{"LATITUDE":"x"}}`),
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"LATITUDE": "bad"}})
	defer r.Body.Close()
	var body applyFeedResponse
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Error != "helper-supplied summary" {
		t.Errorf("body.Error = %q, want helper-supplied summary preserved", body.Error)
	}
}

func TestConfigPost_RejectedErrorIsSorted(t *testing.T) {
	// Map iteration order is randomised in Go, so the synthesized error
	// string must sort keys for stable rendering.
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{
		Stdout: []byte(`{"status":"rejected","errors":{"LONGITUDE":"x","ALTITUDE":"y","LATITUDE":"z"}}`),
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"LATITUDE": "bad"}})
	defer r.Body.Close()
	var body applyFeedResponse
	_ = json.NewDecoder(r.Body).Decode(&body)
	want := "ALTITUDE: y; LATITUDE: z; LONGITUDE: x"
	if body.Error != want {
		t.Errorf("body.Error = %q, want %q (sorted)", body.Error, want)
	}
}

func TestConfigPost_UnknownKeyRejectedPreShellout(t *testing.T) {
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": map[string]string{"BOGUS_KEY": "x"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
	// stdinRunner must NOT have been invoked — the schema cache catches
	// it pre-shellout so we save a privileged sudo call on the obvious
	// client bug.
	if len(h.stdinCallsCopy()) != 0 {
		t.Errorf("stdinRunner was called for an unknown key; want pre-shellout reject")
	}
}

// PR 4 retired reconcile978On/Off. Both 978 services now restart on every
// /api/config POST, regardless of UAT_INPUT direction; the daemons self-decide
// from UAT_INPUT and either exec the daemon, sleep (disabled), or exit 64
// (misconfigured input) when not requested. systemctl restart returns 0
// once the start job is dispatched, so the pending_restart payload stays
// empty on the disable transition.

// pending_restart surfaces 978 unit failures alongside feed/mlat. Confirms
// that both 978 entries land in the response when their restart fails.

func TestUpdate_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/update", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

func TestUpdate_HappyPathReturns202(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/update", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", r.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(r.Body).Decode(&got)
	if got["unit"] != "airplanes-update.service" {
		t.Errorf("unit = %q", got["unit"])
	}
}

func TestUpdate_AlreadyRunning409(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.mu.Lock()
	// The handler runs `systemctl is-active` first (empty stdout → no unit
	// active, proceed), then `sudo systemd-run ...`. Tag the systemd-run
	// path with the "already exists" stderr so the handler's contains-check
	// fires and maps to 409.
	h.runnerErrFor = func(argv []string) error {
		if len(argv) >= 2 && argv[1] == "update" {
			return errors.New("systemd-run failed")
		}
		return nil
	}
	h.runnerResultFor = func(argv []string) wexec.Result {
		if len(argv) >= 2 && argv[1] == "update" {
			return wexec.Result{Stderr: []byte("Unit airplanes-update.service already exists")}
		}
		return wexec.Result{}
	}
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/update", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
}

func TestSystemUpgrade_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/system-upgrade", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

func TestSystemUpgrade_HappyPathReturns202(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/system-upgrade", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", r.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(r.Body).Decode(&got)
	if got["unit"] != "airplanes-system-upgrade.service" {
		t.Errorf("unit = %q", got["unit"])
	}
}

func TestSystemUpgrade_AlreadyRunning409(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.mu.Lock()
	h.runnerErrFor = func(argv []string) error {
		if len(argv) >= 2 && argv[1] == "system-upgrade" {
			return errors.New("systemd-run failed")
		}
		return nil
	}
	h.runnerResultFor = func(argv []string) wexec.Result {
		if len(argv) >= 2 && argv[1] == "system-upgrade" {
			return wexec.Result{Stderr: []byte("Unit airplanes-system-upgrade.service already exists")}
		}
		return wexec.Result{}
	}
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/system-upgrade", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
}

func TestSystemUpgrade_ArgvShape(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/system-upgrade", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", r.StatusCode)
	}
	saw := false
	for _, c := range h.callsCopy() {
		if len(c) >= 2 && c[0] == "sudo-stub" && c[1] == "system-upgrade" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("system-upgrade argv not invoked; calls=%v", h.callsCopy())
	}
}

// activeFor stubs `systemctl is-active u1 u2` to report "active" for the
// named unit (mapped by position via maintenanceUnits) and "inactive" for
// the other. Returns a wexec.Result with the multi-line stdout shape that
// systemctl emits for multi-unit is-active calls.
func activeFor(unit string) func([]string) wexec.Result {
	return func(argv []string) wexec.Result {
		if len(argv) < 2 || argv[1] != "is-active" {
			return wexec.Result{}
		}
		var lines []string
		for _, u := range argv[2:] {
			if u == unit {
				lines = append(lines, "active")
			} else {
				lines = append(lines, "inactive")
			}
		}
		return wexec.Result{Stdout: []byte(strings.Join(lines, "\n") + "\n")}
	}
}

func TestUpdate_RefusedDuringSystemUpgrade(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.mu.Lock()
	h.runnerResultFor = activeFor("airplanes-system-upgrade.service")
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/update", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
	// systemd-run argv must not have been invoked.
	for _, c := range h.callsCopy() {
		if len(c) >= 2 && c[1] == "update" {
			t.Fatalf("StartUpdate argv invoked despite 409: %v", c)
		}
	}
}

func TestSystemUpgrade_RefusedDuringFeedUpdate(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.mu.Lock()
	h.runnerResultFor = activeFor("airplanes-update.service")
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/system-upgrade", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
	for _, c := range h.callsCopy() {
		if len(c) >= 2 && c[1] == "system-upgrade" {
			t.Fatalf("StartSystemUpgrade argv invoked despite 409: %v", c)
		}
	}
}

func TestReboot_RefusedDuringMaintenance(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{
		"airplanes-system-upgrade.service",
		"airplanes-update.service",
	} {
		unit := unit
		t.Run(unit, func(t *testing.T) {
			t.Parallel()
			h := newWriteHarness(t)
			h.mu.Lock()
			h.runnerResultFor = activeFor(unit)
			h.mu.Unlock()
			r := postJSON(t, h.client, h.ts.URL+"/api/reboot", map[string]any{})
			defer r.Body.Close()
			if r.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", r.StatusCode)
			}
			// Reboot argv must not be in the call list.
			time.Sleep(50 * time.Millisecond) // race-buffer; goroutine should NOT fire
			for _, c := range h.callsCopy() {
				if len(c) >= 2 && c[1] == "reboot" {
					t.Fatalf("reboot argv invoked despite 409: %v", c)
				}
			}
		})
	}
}

func TestReboot_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/reboot", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

func TestReboot_AuthedReturns202(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/reboot", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", r.StatusCode)
	}
	// The handler runs a synchronous `systemctl is-active` guard before
	// firing the async reboot goroutine (250ms delay). Wait specifically
	// for the reboot argv to appear, not just any call.
	saw := false
	for i := 0; i < 100; i++ {
		for _, c := range h.callsCopy() {
			if len(c) >= 2 && c[1] == "reboot" {
				saw = true
				break
			}
		}
		if saw {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !saw {
		t.Errorf("reboot argv not invoked; calls=%v", h.callsCopy())
	}
}

func TestPoweroff_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/poweroff", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

func TestPoweroff_AuthedReturns202(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/poweroff", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", r.StatusCode)
	}
	// The handler runs a synchronous `systemctl is-active` guard before
	// firing the async poweroff goroutine (250ms delay). Wait specifically
	// for the poweroff argv to appear, not just any call.
	want := []string{"sudo-stub", "poweroff"}
	saw := false
	for i := 0; i < 100; i++ {
		for _, c := range h.callsCopy() {
			if reflect.DeepEqual(c, want) {
				saw = true
				break
			}
		}
		if saw {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !saw {
		t.Errorf("poweroff argv not invoked; calls=%v", h.callsCopy())
	}
}

func TestPoweroff_RefusedDuringMaintenance(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{
		"airplanes-system-upgrade.service",
		"airplanes-update.service",
	} {
		unit := unit
		t.Run(unit, func(t *testing.T) {
			t.Parallel()
			h := newWriteHarness(t)
			h.mu.Lock()
			h.runnerResultFor = activeFor(unit)
			h.mu.Unlock()
			r := postJSON(t, h.client, h.ts.URL+"/api/poweroff", map[string]any{})
			defer r.Body.Close()
			if r.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d, want 409", r.StatusCode)
			}
			time.Sleep(50 * time.Millisecond)
			for _, c := range h.callsCopy() {
				if len(c) >= 2 && c[1] == "poweroff" {
					t.Fatalf("poweroff argv invoked despite 409: %v", c)
				}
			}
		})
	}
}

// TestDefaultPrivilegedArgv_Poweroff pins the production argv shape so the Go
// default and the sudoers file in stage-airplanes/05-install-webconfig cannot
// drift. overlay-smoke catches sudoers-side drift on the image; this catches
// the Go-source side at unit-test time.
func TestDefaultPrivilegedArgv_Poweroff(t *testing.T) {
	t.Parallel()
	want := []string{"/usr/bin/sudo", "-n", "/usr/bin/systemctl", "poweroff"}
	got := DefaultPrivilegedArgv().Poweroff
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Poweroff = %v, want %v", got, want)
	}
}

func TestClaimRegister_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/claim/register", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

func TestClaimRegister_AuthedReturns202(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/claim/register", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", r.StatusCode)
	}
	var got map[string]string
	_ = json.NewDecoder(r.Body).Decode(&got)
	if got["unit"] != "airplanes-claim.service" {
		t.Errorf("unit = %q", got["unit"])
	}
	// Compare the full argv: position-based checks let a regression in the
	// sudo prefix (e.g. dropping -n or sudo) slip through silently.
	want := []string{"sudo-stub", "systemctl", "start", "--no-block", "airplanes-claim.service"}
	calls := h.callsCopy()
	saw := false
	for _, c := range calls {
		if reflect.DeepEqual(c, want) {
			saw = true
		}
	}
	if !saw {
		t.Errorf("claim-register argv %v not invoked; calls=%v", want, calls)
	}
}

func TestClaimRegister_RunnerErrorReturns500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.mu.Lock()
	h.runnerErr = errors.New("Failed to start airplanes-claim.service")
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/claim/register", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

// httpClient builds a client with a CookieJar so /api/auth/login's
// Set-Cookie carries through subsequent requests.
func httpClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

// postJSON helper: applies Origin (matching ts.URL) + Content-Type.
func postJSON(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin(url))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// origin returns the scheme://host portion of a full URL, used for the
// Origin header on test requests.
func origin(u string) string {
	idx := strings.Index(u[len("http://"):], "/")
	if idx == -1 {
		return u
	}
	return u[:len("http://")+idx]
}

func decodeError(t *testing.T, body io.Reader) string {
	t.Helper()
	var dst map[string]string
	if err := json.NewDecoder(body).Decode(&dst); err != nil {
		t.Fatal(err)
	}
	return dst["error"]
}

func TestHealth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(body), "ok test-sha") {
		t.Errorf("body = %q, want prefix 'ok test-sha'", body)
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	for _, path := range []string{"/", "/health", "/api/state"} {
		resp := mustGetDefault(t, ts.URL+path)
		if resp.Header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: missing X-Frame-Options DENY", path)
		}
		if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing X-Content-Type-Options nosniff", path)
		}
		if resp.Header.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s: missing Referrer-Policy same-origin", path)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("%s: CSP missing default-src 'self'", path)
		}
		_ = resp.Body.Close()
	}
}

func TestRootServesSPAShell(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "airplanes.live") {
		t.Errorf("/ body missing 'airplanes.live' marker")
	}
}

func TestUnknownAPIPath404(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/no-such-thing")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestState_UninitializedThenInitialized(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)

	resp := mustGet(t, c, ts.URL+"/api/state")
	var state stateResponse
	_ = json.NewDecoder(resp.Body).Decode(&state)
	resp.Body.Close()
	if state.State != "uninitialized" {
		t.Fatalf("state = %q, want uninitialized", state.State)
	}

	r := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d, want 200 (err=%q)", r.StatusCode, decodeError(t, r.Body))
	}

	resp = mustGet(t, c, ts.URL+"/api/state")
	_ = json.NewDecoder(resp.Body).Decode(&state)
	resp.Body.Close()
	if state.State != "initialized" {
		t.Fatalf("post-setup state = %q, want initialized", state.State)
	}
}

func TestSetup_RejectsWeakPassword(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": "short"})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestSetup_RejectsAfterInitialized(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword})
	r.Body.Close()
	r = postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": "another1234567"})
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("second setup status = %d, want 409", r.StatusCode)
	}
}

func TestSetup_RejectsMissingContentType(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)

	body := strings.NewReader(`{"password":"correct horse battery staple"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/setup", body)
	req.Header.Set("Origin", ts.URL)
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestSetup_RejectsMissingOrigin(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	body := strings.NewReader(`{"password":"correct horse battery staple"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/setup", body)
	req.Header.Set("Content-Type", "application/json")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestSetup_RejectsMismatchedOrigin(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	body := strings.NewReader(`{"password":"correct horse battery staple"}`)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/setup", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example.com")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
}

func TestSetup_RejectsOversizedBody(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	huge := strings.Repeat("x", 2048)
	r := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": huge})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body too big)", r.StatusCode)
	}
}

func TestSetup_AutoLoginsCallerOnSuccess(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword})
	r.Body.Close()
	hasCookie := false
	for _, ck := range c.Jar.Cookies(mustParse(ts.URL)) {
		if ck.Name == SessionCookieName && ck.Value != "" {
			hasCookie = true
		}
	}
	if !hasCookie {
		t.Fatal("setup did not set session cookie")
	}
	// Confirm the session is actually valid via /api/auth/whoami.
	resp := mustGet(t, c, ts.URL+"/api/auth/whoami")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("whoami after setup status = %d, want 200", resp.StatusCode)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()
	clearJar(c, ts.URL)

	r := postJSON(t, c, ts.URL+"/api/auth/login", map[string]string{"password": "wrong-but-long-enough"})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestLogin_CorrectPasswordIssuesCookie(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()
	clearJar(c, ts.URL)

	r := postJSON(t, c, ts.URL+"/api/auth/login", map[string]string{"password": testPassword})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (err=%q)", r.StatusCode, decodeError(t, r.Body))
	}
	resp := mustGet(t, c, ts.URL+"/api/auth/whoami")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("whoami after login status = %d, want 200", resp.StatusCode)
	}
}

func TestLogin_LockoutAfterRepeatedFailures(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()
	clearJar(c, ts.URL)

	for i := 0; i < 5; i++ {
		r := postJSON(t, c, ts.URL+"/api/auth/login", map[string]string{"password": "wrong-but-long-enough"})
		r.Body.Close()
	}
	r := postJSON(t, c, ts.URL+"/api/auth/login", map[string]string{"password": testPassword})
	defer r.Body.Close()
	if r.StatusCode != http.StatusLocked {
		t.Fatalf("status = %d, want 423 (locked)", r.StatusCode)
	}
}

func TestLogout_ClearsCookieAndRevokes(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()

	r := postJSON(t, c, ts.URL+"/api/auth/logout", map[string]string{})
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("logout status = %d, want 200", r.StatusCode)
	}
	resp := mustGet(t, c, ts.URL+"/api/auth/whoami")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("whoami after logout status = %d, want 401", resp.StatusCode)
	}
}

func TestPasswordChange_VerifiesOldAndRotatesAllSessions(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()

	// Wrong old password.
	r := postJSON(t, c, ts.URL+"/api/auth/password", map[string]any{
		"old_password": "wrong",
		"new_password": "another-long-password",
	})
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Errorf("change w/ wrong old: status = %d, want 401", r.StatusCode)
	}

	// Establish a second session via login, then change password — that
	// other session must be revoked. Spin up a second jar.
	c2 := httpClient(t)
	postJSON(t, c2, ts.URL+"/api/auth/login", map[string]string{"password": testPassword}).Body.Close()
	resp := mustGet(t, c2, ts.URL+"/api/auth/whoami")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("c2 baseline whoami status = %d, want 200", resp.StatusCode)
	}

	r = postJSON(t, c, ts.URL+"/api/auth/password", map[string]any{
		"old_password": testPassword,
		"new_password": "brand-new-password-2026",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("change status = %d, want 200 (err=%q)", r.StatusCode, decodeError(t, r.Body))
	}

	// Original caller (c) gets a freshly issued cookie → still authed.
	resp = mustGet(t, c, ts.URL+"/api/auth/whoami")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("originator whoami after change: status = %d, want 200", resp.StatusCode)
	}

	// Other session was revoked.
	resp = mustGet(t, c2, ts.URL+"/api/auth/whoami")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("co-session whoami after change: status = %d, want 401", resp.StatusCode)
	}
}

func TestPasswordChange_RejectsWeakNew(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()

	r := postJSON(t, c, ts.URL+"/api/auth/password", map[string]any{
		"old_password": testPassword,
		"new_password": "short",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestWhoami_RequiresSession(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/auth/whoami")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestStatic_Serves(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/static/style.css")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
	}
}

func TestLogin_RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword}).Body.Close()
	clearJar(c, ts.URL)

	r := postJSON(t, c, ts.URL+"/api/auth/login", map[string]any{
		"password": testPassword,
		"extra":    "field",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

// helpers ---

func mustParse(s string) (u *url.URL) {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func clearJar(c *http.Client, baseURL string) {
	c.Jar.SetCookies(mustParse(baseURL), nil)
}

func mustGet(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func mustGetDefault(t *testing.T, url string) *http.Response {
	return mustGet(t, http.DefaultClient, url)
}

func mustDo(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do %s %s: %v", req.Method, req.URL, err)
	}
	return resp
}

// authedClient returns a client whose CookieJar holds a valid session,
// established by running setup against the freshly-provisioned tempfile
// password store.
func authedClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"password": testPassword})
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup status = %d, want 200", r.StatusCode)
	}
	return c
}

// --- read endpoint tests ---

func TestIdentity_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/identity")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestIdentity_AuthedReturnsFeederID(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/identity")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got["feeder_id"] != "test-feeder-id" {
		t.Errorf("feeder_id = %v, want test-feeder-id", got["feeder_id"])
	}
	if got["claim_secret_present"] != true {
		t.Errorf("claim_secret_present = %v, want true", got["claim_secret_present"])
	}
}

func TestIdentitySecret_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/identity/secret", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestIdentitySecret_AuthedReturnsSecret(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	r := postJSON(t, c, ts.URL+"/api/identity/secret", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", r.StatusCode)
	}
	var got map[string]any
	_ = json.NewDecoder(r.Body).Decode(&got)
	if got["claim_secret"] != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("claim_secret = %v", got["claim_secret"])
	}
	if got["feeder_id"] != "test-feeder-id" {
		t.Errorf("feeder_id = %v", got["feeder_id"])
	}
}

func TestConfigGet_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/config")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestConfigGet_AuthedReturnsValues(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/config")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Values map[string]string `json:"values"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Values["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q, want 51.5", got.Values["LATITUDE"])
	}
	if got.Values["MLAT_USER"] != "tester" {
		t.Errorf("MLAT_USER = %q, want tester", got.Values["MLAT_USER"])
	}
}

func TestStatus_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestStatus_AuthedReturnsServiceStates(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var got struct {
		Version  string            `json:"webconfig_version"`
		Services map[string]string `json:"services"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if got.Version != "test-sha" {
		t.Errorf("version = %q, want test-sha", got.Version)
	}
	if got.Services["airplanes-feed.service"] != "active" {
		t.Errorf("airplanes-feed = %q, want active", got.Services["airplanes-feed.service"])
	}
}

func TestLog_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL+"/api/log/feed")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestLog_UnknownUnit(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/log/no-such-unit")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestLog_KnownUnitStreamsSSE(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/log/feed")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got, want := resp.Header.Get("Content-Type"), "text/event-stream"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "data: test-log-line") {
		t.Errorf("SSE body missing test-log-line: %q", body)
	}
}

// TestDefaultPrivilegedArgv_SudoersParity guards against drift between the
// production argv shapes and the sudoers entries that authorize them. The
// argv tail (after the `/usr/bin/sudo -n` prefix) must match the sudoers
// command-spec byte-for-byte, otherwise sudo refuses the call at runtime.
func TestDefaultPrivilegedArgv_SudoersParity(t *testing.T) {
	t.Parallel()
	sudoersPath := filepath.Join("..", "..", "..", "stage-airplanes", "05-install-webconfig", "files", "etc", "sudoers.d", "010_airplanes-webconfig")
	raw, err := os.ReadFile(sudoersPath)
	if err != nil {
		t.Fatalf("read sudoers: %v", err)
	}
	sudoers := string(raw)

	priv := DefaultPrivilegedArgv()
	cases := []struct {
		label string
		argv  []string
	}{
		{"ApplyFeed", priv.ApplyFeed},
		{"Reboot", priv.Reboot},
		{"Poweroff", priv.Poweroff},
		{"StartUpdate", priv.StartUpdate},
		{"StartSystemUpgrade", priv.StartSystemUpgrade},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			if len(tc.argv) < 3 || tc.argv[0] != "/usr/bin/sudo" || tc.argv[1] != "-n" {
				t.Fatalf("%s: argv must start with /usr/bin/sudo -n, got %v", tc.label, tc.argv)
			}
			tail := strings.Join(tc.argv[2:], " ")
			if !strings.Contains(sudoers, tail) {
				t.Errorf("%s: argv tail not present in sudoers: %q", tc.label, tail)
			}
		})
	}
}

// --- pending_restart surfacing (PR 3) ---
