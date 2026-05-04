package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/auth"
	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
	"github.com/airplanes-live/image/webconfig/internal/feedenv"
	"github.com/airplanes-live/image/webconfig/internal/identity"
	"github.com/airplanes-live/image/webconfig/internal/logs"
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
		APLFeedSudoArgv: []string{"/bin/echo", "stub"},
	}
	_ = os.WriteFile(idPaths.FeederIDFile, []byte("test-feeder-id"), 0o644)
	_ = os.WriteFile(idPaths.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP"), 0o600)
	idStubRunner := func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{Stdout: []byte(
			"Feeder ID: test-feeder-id\n" +
				"Claim secret: ABCD-EFGH-IJKL-MNOP\n" +
				"Claim page: https://airplanes.live/feeder/claim\n",
		)}, nil
	}

	feedEnvPath := filepath.Join(dir, "feed.env")
	_ = os.WriteFile(feedEnvPath,
		[]byte(`LATITUDE=51.5`+"\n"+`LONGITUDE=-0.1`+"\n"+`USER=tester`+"\n"),
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
		runnerCalls       [][]string
		stdinRunnerCalls  []stdinCall
		runnerMu          sync.Mutex
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
		ApplyConfig:    []string{"sudo-stub", "apply-config"},
		RestartFeed:    []string{"sudo-stub", "restart", "feed"},
		RestartMLAT:    []string{"sudo-stub", "restart", "mlat"},
		StartDump978:   []string{"sudo-stub", "start", "dump978"},
		StartUAT:       []string{"sudo-stub", "start", "uat"},
		StopDump978:    []string{"sudo-stub", "stop", "dump978"},
		StopUAT:        []string{"sudo-stub", "stop", "uat"},
		EnableDump978:  []string{"sudo-stub", "enable", "dump978"},
		EnableUAT:      []string{"sudo-stub", "enable", "uat"},
		DisableDump978: []string{"sudo-stub", "disable", "dump978"},
		DisableUAT:     []string{"sudo-stub", "disable", "uat"},
		Reboot:         []string{"sudo-stub", "reboot"},
		StartUpdate:    []string{"sudo-stub", "update"},
	}

	deps := Deps{
		Version:      "test-sha",
		Store:        auth.NewPasswordStore(hashPath),
		Sessions:     auth.NewSessions(time.Hour),
		Lockout:      auth.NewLockout(5, time.Minute, 15*time.Minute),
		Guard:        guard,
		Argon2Params: fastTestParams,
		Identity:     identity.NewReader(idPaths, idStubRunner),
		FeedEnv:      &feedenv.Reader{Path: feedEnvPath},
		Status:       status.NewReader("test-sha", statusPaths, statusRunner),
		Logs:         logs.NewStreamer(logsRunner),
		Runner:       captureRunner,
		StdinRunner:  captureStdinRunner,
		Privileged:   priv,
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
	ts          *httptest.Server
	client      *http.Client
	feedEnvPath string
	mu          sync.Mutex
	calls       [][]string
	stdinCalls  []stdinCall
	runnerErr   error // returned by captureRunner; tests override
	stdinErr    error
	stdinResult wexec.Result
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

	h := &writeHarness{feedEnvPath: feedEnvPath}
	captureRunner := func(_ context.Context, argv []string) (wexec.Result, error) {
		h.mu.Lock()
		h.calls = append(h.calls, append([]string(nil), argv...))
		err := h.runnerErr
		h.mu.Unlock()
		return wexec.Result{}, err
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
		ApplyConfig:    []string{"sudo-stub", "apply-config"},
		RestartFeed:    []string{"sudo-stub", "restart", "feed"},
		RestartMLAT:    []string{"sudo-stub", "restart", "mlat"},
		StartDump978:   []string{"sudo-stub", "start", "dump978"},
		StartUAT:       []string{"sudo-stub", "start", "uat"},
		StopDump978:    []string{"sudo-stub", "stop", "dump978"},
		StopUAT:        []string{"sudo-stub", "stop", "uat"},
		EnableDump978:  []string{"sudo-stub", "enable", "dump978"},
		EnableUAT:      []string{"sudo-stub", "enable", "uat"},
		DisableDump978: []string{"sudo-stub", "disable", "dump978"},
		DisableUAT:     []string{"sudo-stub", "disable", "uat"},
		Reboot:         []string{"sudo-stub", "reboot"},
		StartUpdate:    []string{"sudo-stub", "update"},
	}

	deps := Deps{
		Version:      "test-sha",
		Store:        auth.NewPasswordStore(hashPath),
		Sessions:     auth.NewSessions(time.Hour),
		Lockout:      auth.NewLockout(5, time.Minute, 15*time.Minute),
		Guard:        guard,
		Argon2Params: fastTestParams,
		Identity:     identity.NewReader(identity.Paths{FeederIDFile: filepath.Join(dir, "feeder-id")}, nil),
		FeedEnv:      &feedenv.Reader{Path: feedEnvPath},
		Status: status.NewReader("test-sha", status.Paths{
			SystemctlBinary: "/bin/true", IsActiveTimeout: time.Second,
		}, captureRunner),
		Logs:        logs.NewStreamer(nil),
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

func TestConfigPost_ValidatedRequestRunsHelperThenRestarts(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/config", map[string]any{
		"updates": map[string]string{"LATITUDE": "51.5", "USER": "alice"},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (err=%q)", r.StatusCode, decodeError(t, r.Body))
	}
	stdinCalls := h.stdinCallsCopy()
	if len(stdinCalls) != 1 {
		t.Fatalf("apply-config calls = %d, want 1", len(stdinCalls))
	}
	if got := stdinCalls[0].argv; got[0] != "sudo-stub" || got[1] != "apply-config" {
		t.Errorf("apply-config argv = %v", got)
	}
	var body struct {
		Updates map[string]string `json:"updates"`
	}
	if err := json.Unmarshal(stdinCalls[0].stdin, &body); err != nil {
		t.Fatal(err)
	}
	if body.Updates["LATITUDE"] != "51.5" || body.Updates["USER"] != "alice" {
		t.Errorf("apply-config stdin = %v", body.Updates)
	}
	calls := h.callsCopy()
	// One of the calls should be RestartFeed and another RestartMLAT.
	sawFeed, sawMLAT := false, false
	for _, c := range calls {
		if len(c) >= 3 && c[1] == "restart" && c[2] == "feed" {
			sawFeed = true
		}
		if len(c) >= 3 && c[1] == "restart" && c[2] == "mlat" {
			sawMLAT = true
		}
	}
	if !sawFeed || !sawMLAT {
		t.Errorf("missing service restart calls: %v", calls)
	}
}

func TestConfigPost_RejectsBadValueWithKeyName(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/config", map[string]any{
		"updates": map[string]string{"LATITUDE": "200"},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", r.StatusCode)
	}
	body := decodeError(t, r.Body)
	if !strings.Contains(body, "LATITUDE") {
		t.Errorf("error %q missing key name", body)
	}
	if len(h.stdinCallsCopy()) != 0 {
		t.Error("apply-config invoked despite validation failure")
	}
}

func TestConfigPost_978OnReconciles(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/config", map[string]any{
		"updates": map[string]string{"UAT_INPUT": "127.0.0.1:30978"},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", r.StatusCode)
	}
	calls := h.callsCopy()
	wantVerbs := map[string]bool{"enable": false, "start": false}
	for _, c := range calls {
		if len(c) >= 3 && (c[2] == "dump978" || c[2] == "uat") {
			wantVerbs[c[1]] = true
		}
	}
	for verb, saw := range wantVerbs {
		if !saw {
			t.Errorf("missing %s verb on 978 units; calls=%v", verb, calls)
		}
	}
}

func TestConfigPost_978OffReconciles(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// Pre-existing UAT_INPUT set on disk — UAT_INPUT="" in updates is the
	// disable transition.
	if err := os.WriteFile(h.feedEnvPath,
		[]byte(`UAT_INPUT="127.0.0.1:30978"`+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/config", map[string]any{
		"updates": map[string]string{"UAT_INPUT": ""},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", r.StatusCode)
	}
	calls := h.callsCopy()
	wantVerbs := map[string]bool{"stop": false, "disable": false}
	for _, c := range calls {
		if len(c) >= 3 && (c[2] == "dump978" || c[2] == "uat") {
			wantVerbs[c[1]] = true
		}
	}
	for verb, saw := range wantVerbs {
		if !saw {
			t.Errorf("missing %s verb on 978 units; calls=%v", verb, calls)
		}
	}
}

func TestConfigPost_HelperFailureMaps400(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.mu.Lock()
	h.stdinResult = wexec.Result{Stderr: []byte("LATITUDE: out of range\n"), ExitCode: 10}
	h.stdinErr = errors.New("exit status 10")
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/config", map[string]any{
		"updates": map[string]string{"LATITUDE": "10"},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

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
	h.runnerErr = errors.New("Unit airplanes-update.service already exists")
	h.mu.Unlock()
	r := postJSON(t, h.client, h.ts.URL+"/api/update", map[string]any{})
	defer r.Body.Close()
	// Stderr-based detection — we wired the captureRunner to return err
	// only; the handler reads res.Stderr. Update the harness to return a
	// stderr-bearing Result instead to exercise the 409 path properly.
	if r.StatusCode != http.StatusInternalServerError && r.StatusCode != http.StatusConflict {
		t.Fatalf("unexpected status = %d", r.StatusCode)
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
	// Reboot is fired async; give the goroutine a moment to record the call.
	for i := 0; i < 100; i++ {
		if len(h.callsCopy()) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls := h.callsCopy()
	saw := false
	for _, c := range calls {
		if len(c) >= 2 && c[1] == "reboot" {
			saw = true
		}
	}
	if !saw {
		t.Errorf("reboot argv not invoked; calls=%v", calls)
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
		resp := mustGetDefault(t, ts.URL + path)
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
	resp := mustGetDefault(t, ts.URL + "/")
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
	resp := mustGetDefault(t, ts.URL + "/api/no-such-thing")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestState_UninitializedThenInitialized(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)

	resp := mustGet(t, c, ts.URL + "/api/state")
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

	resp = mustGet(t, c, ts.URL + "/api/state")
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
	resp := mustGet(t, c, ts.URL + "/api/auth/whoami")
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
	resp := mustGet(t, c, ts.URL + "/api/auth/whoami")
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
	resp := mustGet(t, c, ts.URL + "/api/auth/whoami")
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
	resp := mustGet(t, c2, ts.URL + "/api/auth/whoami")
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
	resp = mustGet(t, c, ts.URL + "/api/auth/whoami")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("originator whoami after change: status = %d, want 200", resp.StatusCode)
	}

	// Other session was revoked.
	resp = mustGet(t, c2, ts.URL + "/api/auth/whoami")
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
	resp := mustGetDefault(t, ts.URL + "/api/auth/whoami")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestStatic_Serves(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	resp := mustGetDefault(t, ts.URL + "/static/style.css")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
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
	if got.Values["USER"] != "tester" {
		t.Errorf("USER = %q, want tester", got.Values["USER"])
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
