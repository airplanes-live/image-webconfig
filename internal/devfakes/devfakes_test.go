package devfakes

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
)

func mustNewState(t *testing.T) *State {
	t.Helper()
	dir := t.TempDir()
	s := NewState(DefaultPaths(dir))
	if err := s.SyncAll(); err != nil {
		t.Fatalf("SyncAll: %v", err)
	}
	return s
}

func TestApplyFeed_BareStringUpdates(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	payload := `{"updates":{"GAIN":"30","UAT_INPUT":"rtlsdr"}}`
	res, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	var env struct {
		Status  string   `json:"status"`
		Changed []string `json:"changed"`
	}
	if err := json.Unmarshal(res.Stdout, &env); err != nil {
		t.Fatalf("unmarshal: %v body=%q", err, res.Stdout)
	}
	if env.Status != "applied" {
		t.Fatalf("status=%q want applied (body=%q)", env.Status, res.Stdout)
	}
	if !containsAll(env.Changed, "GAIN", "UAT_INPUT") {
		t.Fatalf("changed=%v want GAIN+UAT_INPUT", env.Changed)
	}
	if got := s.FeedEnvSnapshot()["GAIN"]; got != "30" {
		t.Fatalf("feedEnv[GAIN]=%q want 30", got)
	}
}

func TestApplyFeed_ObjectFormUpdates(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	payload := `{"updates":{"LATITUDE":{"value":"52.0","edited_at":"2026-05-17T12:00:00Z","edited_by":"feeder"}}}`
	res, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	var env struct {
		Status  string   `json:"status"`
		Changed []string `json:"changed"`
	}
	_ = json.Unmarshal(res.Stdout, &env)
	if env.Status != "applied" {
		t.Fatalf("status=%q body=%q", env.Status, res.Stdout)
	}
	if s.FeedEnvSnapshot()["LATITUDE"] != "52.0" {
		t.Fatalf("LATITUDE didn't take: %q", s.FeedEnvSnapshot()["LATITUDE"])
	}
}

func TestApplyFeed_GeoConfiguredDerived(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	// Clearing LATITUDE should flip GEO_CONFIGURED to false even though
	// the client only posted LATITUDE.
	payload := `{"updates":{"LATITUDE":""}}`
	res, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	var env struct {
		Changed []string `json:"changed"`
	}
	_ = json.Unmarshal(res.Stdout, &env)
	if !containsAll(env.Changed, "LATITUDE", "GEO_CONFIGURED") {
		t.Fatalf("changed=%v want LATITUDE+GEO_CONFIGURED", env.Changed)
	}
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "false" {
		t.Fatalf("GEO_CONFIGURED not flipped, snapshot=%v", s.FeedEnvSnapshot())
	}
	// Restoring LATITUDE flips it back.
	payload = `{"updates":{"LATITUDE":"51.5"}}`
	_, _ = runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload))
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "true" {
		t.Fatalf("GEO_CONFIGURED not restored, snapshot=%v", s.FeedEnvSnapshot())
	}
}

func TestApplyFeed_WritesAreVisibleToFeedenvReader(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	payload := `{"updates":{"MLAT_ENABLED":"false"}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	rd := &feedenv.Reader{Path: s.Paths.FeedEnv}
	got, err := rd.ReadAll()
	if err != nil {
		t.Fatalf("feedenv.ReadAll: %v", err)
	}
	if got["MLAT_ENABLED"] != "false" {
		t.Fatalf("feedenv read after fake apply: MLAT_ENABLED=%q want false", got["MLAT_ENABLED"])
	}
}

func TestWifi_FullRoundtrip(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runStdin := StdinRunner(s, priv)

	listEnv := decodeStdinCall(t, runStdin, priv.WifiList, "")
	if len(listEnv["networks"].([]any)) != 2 {
		t.Fatalf("seed list networks=%d want 2", len(listEnv["networks"].([]any)))
	}

	// Add a network.
	addEnv := decodeStdinCall(t, runStdin, priv.WifiAdd, `{"ssid":"new-net","psk":"abcd1234","priority":3}`)
	if addEnv["status"] != "applied" {
		t.Fatalf("add status=%v (env=%v)", addEnv["status"], addEnv)
	}
	id, _ := addEnv["id"].(string)
	if id == "" {
		t.Fatalf("add: no id returned (env=%v)", addEnv)
	}

	listEnv = decodeStdinCall(t, runStdin, priv.WifiList, "")
	if len(listEnv["networks"].([]any)) != 3 {
		t.Fatalf("after add: networks=%d want 3", len(listEnv["networks"].([]any)))
	}

	// Activate the new one.
	actEnv := decodeStdinCall(t, runStdin, priv.WifiActivate, `{"id":"`+id+`"}`)
	if actEnv["status"] != "applied" {
		t.Fatalf("activate status=%v", actEnv["status"])
	}
	statusEnv := decodeStdinCall(t, runStdin, priv.WifiStatus, "")
	active, _ := statusEnv["active_connection"].(map[string]any)
	if active == nil || active["ssid"] != "new-net" {
		t.Fatalf("status active=%v want ssid=new-net", active)
	}

	// Delete.
	delEnv := decodeStdinCall(t, runStdin, priv.WifiDelete, `{"id":"`+id+`"}`)
	if delEnv["status"] != "applied" {
		t.Fatalf("delete status=%v", delEnv["status"])
	}
	listEnv = decodeStdinCall(t, runStdin, priv.WifiList, "")
	if len(listEnv["networks"].([]any)) != 2 {
		t.Fatalf("after delete: networks=%d want 2", len(listEnv["networks"].([]any)))
	}
}

func TestSystemctl_IsActiveMixedUnits(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := Runner(s, priv)
	// maintenanceUnitActive calls all three maintenance units in one
	// is-active. They must be inactive so the maintenance guard lets
	// reboot/update through.
	res, err := runner(context.Background(), []string{
		"/usr/bin/systemctl", "is-active",
		"airplanes-system-upgrade.service",
		"airplanes-update.service",
		"airplanes-webconfig-update.service",
	})
	if err != nil {
		t.Fatalf("runner: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 state lines, got %d (out=%q)", len(lines), res.Stdout)
	}
	for _, l := range lines {
		if l != "inactive" {
			t.Fatalf("maintenance state=%q want inactive (out=%q)", l, res.Stdout)
		}
	}
	// And the dashboard's monitored services are active.
	res, _ = runner(context.Background(), []string{"/usr/bin/systemctl", "is-active", "airplanes-feed.service"})
	if strings.TrimSpace(string(res.Stdout)) != "active" {
		t.Fatalf("monitored service state=%q want active", res.Stdout)
	}
}

func TestClaimRegister_MaterialisesSecret(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := Runner(s, priv)
	if _, err := runner(context.Background(), priv.RegisterClaim); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s.ClaimSecret() == "" {
		t.Fatal("ClaimSecret empty after register")
	}
	info, err := os.Stat(s.Paths.ClaimSecret)
	if err != nil {
		t.Fatalf("ClaimSecret file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("ClaimSecret file empty")
	}
}

func TestConcurrentApply_AtomicWrites(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	rd := &feedenv.Reader{Path: s.Paths.FeedEnv}

	const writers = 16
	const iters = 25
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				body := []byte(`{"updates":{"GAIN":"` + intToStr(i*100+j) + `"}}`)
				if _, err := runner(context.Background(), priv.ApplyFeed, bytes.NewReader(body)); err != nil {
					t.Errorf("apply: %v", err)
					return
				}
				if _, err := rd.ReadAll(); err != nil {
					t.Errorf("read during write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestStreamRunner_EmitsCannedLines(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	streamer := StreamRunner(s)
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	var buf bytes.Buffer
	_ = streamer(ctx, &buf, []string{"/usr/bin/journalctl", "-u", "airplanes-feed.service", "--follow"})
	if !bytes.Contains(buf.Bytes(), []byte("airplanes-feed.service")) {
		t.Fatalf("stream output missing unit name: %q", buf.String())
	}
}

func decodeStdinCall(t *testing.T, run wexec.CommandRunnerStdin, argv []string, body string) map[string]any {
	t.Helper()
	res, err := run(context.Background(), argv, strings.NewReader(body))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(res.Stdout, &env); err != nil {
		t.Fatalf("unmarshal %q: %v", res.Stdout, err)
	}
	return env
}

func containsAll(xs []string, needles ...string) bool {
	for _, n := range needles {
		hit := false
		for _, x := range xs {
			if x == n {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

func intToStr(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
