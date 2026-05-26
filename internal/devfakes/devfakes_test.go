package devfakes

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
)

// altitudeFixtureCase mirrors a single entry in the shared canonical
// fixture vendored from feed. We load it relative to the feedmeta
// package's testdata dir so the bash / Go / JS / devfakes parity all
// derive from the same source of truth.
type altitudeFixtureCase struct {
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
	ExpectedOK     bool   `json:"expected_ok"`
	Note           string `json:"note,omitempty"`
}

func loadAltitudeFixture(t *testing.T) []altitudeFixtureCase {
	t.Helper()
	path := filepath.Join("..", "feedmeta", "testdata", "altitude-canonicalization.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read altitude fixture (%s): %v", path, err)
	}
	var doc struct {
		Cases []altitudeFixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode altitude fixture: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("altitude fixture: zero cases")
	}
	return doc.Cases
}

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
	// Clearing LATITUDE only — LONGITUDE stays "-0.1". Production
	// considers an axis "zero" only when both are empty/zero; with a
	// non-zero LONGITUDE, GEO_CONFIGURED stays true.
	payload := `{"updates":{"LATITUDE":""}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "true" {
		t.Fatalf("GEO_CONFIGURED flipped despite non-zero LONGITUDE, snapshot=%v", s.FeedEnvSnapshot())
	}
	// Clearing LONGITUDE too — now both axes are zero/empty → false.
	payload = `{"updates":{"LONGITUDE":""}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "false" {
		t.Fatalf("GEO_CONFIGURED not flipped after clearing both axes, snapshot=%v", s.FeedEnvSnapshot())
	}
	// Re-set just LATITUDE — that one axis non-zero is enough for true.
	payload = `{"updates":{"LATITUDE":"51.5"}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "true" {
		t.Fatalf("GEO_CONFIGURED not restored, snapshot=%v", s.FeedEnvSnapshot())
	}
}

func TestApplyFeed_GeoDerive_ZeroIsNotMeaningful(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	// LATITUDE=0 + LONGITUDE=0 → false (both axes numerically zero).
	payload := `{"updates":{"LATITUDE":"0","LONGITUDE":"0"}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "false" {
		t.Fatalf("zero/zero should derive false, snapshot=%v", s.FeedEnvSnapshot())
	}
	// LATITUDE=0 alone with prior LONGITUDE intact: production treats
	// 0 as "zero axis", but the prior LONGITUDE was -0.1 — wait, we
	// just cleared both. Use a fresh state and exercise the equator
	// case: lat=0 alongside a real lon stays true.
	s2 := mustNewState(t)
	r2 := StdinRunner(s2, priv)
	payload = `{"updates":{"LATITUDE":"0"}}`
	if _, err := r2(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s2.FeedEnvSnapshot()["GEO_CONFIGURED"] != "true" {
		t.Fatalf("equator (lat=0, lon=-0.1) should derive true, snapshot=%v", s2.FeedEnvSnapshot())
	}
}

func TestApplyFeed_ExplicitGeoOverrideWins(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	// Caller pins GEO_CONFIGURED=false alongside a non-zero lat: the
	// explicit override skips derivation entirely.
	payload := `{"updates":{"LATITUDE":"51.5","GEO_CONFIGURED":"false"}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if s.FeedEnvSnapshot()["GEO_CONFIGURED"] != "false" {
		t.Fatalf("explicit GEO_CONFIGURED=false was overridden, snapshot=%v", s.FeedEnvSnapshot())
	}
}

func TestApplyFeed_AltitudeCanonicalisedToBareMetres(t *testing.T) {
	t.Parallel()
	// Sanity case: 400ft must land on disk as "121.92" (the
	// post-conversion bare-metres canonical) regardless of the
	// operator's input suffix. This is the parity case feed's apply
	// layer covers; without canonicalisation here, the dev-server
	// would diverge from production behaviour and the SPA's
	// dirty-state comparator would thrash.
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := StdinRunner(s, priv)
	payload := `{"updates":{"ALTITUDE":"400ft"}}`
	if _, err := runner(context.Background(), priv.ApplyFeed, strings.NewReader(payload)); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if got := s.FeedEnvSnapshot()["ALTITUDE"]; got != "121.92" {
		t.Fatalf("ALTITUDE=%q want 121.92", got)
	}
}

func TestApplyFeed_AltitudeFixtureParity(t *testing.T) {
	// Drives the shared canonical fixture through the fake apply
	// path. Valid inputs land on disk as the fixture's
	// expected_output (the bare-metres canonical form). Invalid
	// inputs (expected_ok=false) must produce a rejected envelope
	// AND leave the prior on-disk value untouched — matching what
	// feed's apply-layer validator does to garbage before its
	// canonicaliser ever runs.
	t.Parallel()
	priv := StubPrivilegedArgv()
	for _, tc := range loadAltitudeFixture(t) {
		tc := tc
		t.Run("input="+tc.Input, func(t *testing.T) {
			s := mustNewState(t)
			before := s.FeedEnvSnapshot()["ALTITUDE"]
			runner := StdinRunner(s, priv)
			body, _ := json.Marshal(map[string]any{
				"updates": map[string]any{"ALTITUDE": tc.Input},
			})
			res, err := runner(context.Background(), priv.ApplyFeed, bytes.NewReader(body))
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			var env struct {
				Status string            `json:"status"`
				Errors map[string]string `json:"errors"`
			}
			if jerr := json.Unmarshal(res.Stdout, &env); jerr != nil {
				t.Fatalf("envelope decode: %v (body=%q)", jerr, res.Stdout)
			}
			got := s.FeedEnvSnapshot()["ALTITUDE"]
			if !tc.ExpectedOK {
				if env.Status != "rejected" {
					t.Errorf("input=%q expected_ok=false: status=%q, want rejected (body=%q)",
						tc.Input, env.Status, res.Stdout)
				}
				if env.Errors["ALTITUDE"] == "" {
					t.Errorf("input=%q: rejected envelope missing per-key error (body=%q)",
						tc.Input, res.Stdout)
				}
				if got != before {
					t.Errorf("input=%q (rejected): ALTITUDE on disk = %q, want unchanged %q",
						tc.Input, got, before)
				}
				return
			}
			if env.Status != "applied" {
				t.Errorf("input=%q expected_ok=true: status=%q, want applied (body=%q)",
					tc.Input, env.Status, res.Stdout)
			}
			if got != tc.ExpectedOutput {
				t.Errorf("ALTITUDE on disk after apply(%q) = %q, want %q (note=%s)",
					tc.Input, got, tc.ExpectedOutput, tc.Note)
			}
		})
	}
}

// TestApplyFeed_StrictFieldValidation exercises the devfake's server-parity
// validators for MLAT_USER, GAIN, DUMP978_SDR_SERIAL, and DUMP978_GAIN.
// Each rule mirrors feed/scripts/lib/configure-validators.sh; this test
// is the local guard against the devfake quietly accepting input the
// real apl-feed would reject.
func TestApplyFeed_StrictFieldValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		key    string
		value  string
		reject bool
	}{
		// MLAT_USER: empty OK, else [A-Za-z0-9_-]{1,64}.
		{"mlat empty ok", "MLAT_USER", "", false},
		{"mlat alpha ok", "MLAT_USER", "alice", false},
		{"mlat mixed ok", "MLAT_USER", "ALice_99-x", false},
		{"mlat with bang", "MLAT_USER", "alice!", true},
		{"mlat with space", "MLAT_USER", "alice bob", true},
		{"mlat too long", "MLAT_USER", strings.Repeat("a", 65), true},
		{"mlat exactly 64", "MLAT_USER", strings.Repeat("a", 64), false},
		// GAIN: auto/min/max or numeric in [0, 60].
		{"gain auto", "GAIN", "auto", false},
		{"gain min", "GAIN", "min", false},
		{"gain max", "GAIN", "max", false},
		{"gain zero", "GAIN", "0", false},
		{"gain sixty", "GAIN", "60", false},
		{"gain decimal", "GAIN", "30.5", false},
		{"gain over", "GAIN", "61", true},
		{"gain negative", "GAIN", "-1", true},
		{"gain scientific", "GAIN", "1e1", true},
		{"gain empty", "GAIN", "", true},
		{"gain hex", "GAIN", "0x10", true},
		{"gain leading dot", "GAIN", ".5", true},
		// DUMP978_SDR_SERIAL: empty OK, else [0-9A-Za-z_-]{1,32}.
		{"dump978 serial empty ok", "DUMP978_SDR_SERIAL", "", false},
		{"dump978 serial num ok", "DUMP978_SDR_SERIAL", "978", false},
		{"dump978 serial mixed ok", "DUMP978_SDR_SERIAL", "abc-def_123", false},
		{"dump978 serial bang", "DUMP978_SDR_SERIAL", "abc!", true},
		{"dump978 serial too long", "DUMP978_SDR_SERIAL", strings.Repeat("a", 33), true},
		// DUMP978_GAIN: numeric in [0, 60], no auto/min/max.
		{"dump978 gain default", "DUMP978_GAIN", "42.1", false},
		{"dump978 gain zero", "DUMP978_GAIN", "0", false},
		{"dump978 gain sixty", "DUMP978_GAIN", "60", false},
		{"dump978 gain auto", "DUMP978_GAIN", "auto", true},
		{"dump978 gain over", "DUMP978_GAIN", "61", true},
		{"dump978 gain empty", "DUMP978_GAIN", "", true},
	}
	priv := StubPrivilegedArgv()
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := mustNewState(t)
			before := s.FeedEnvSnapshot()[tc.key]
			runner := StdinRunner(s, priv)
			body, _ := json.Marshal(map[string]any{
				"updates": map[string]any{tc.key: tc.value},
			})
			res, err := runner(context.Background(), priv.ApplyFeed, bytes.NewReader(body))
			if err != nil {
				t.Fatalf("runner: %v", err)
			}
			var env struct {
				Status string            `json:"status"`
				Errors map[string]string `json:"errors"`
			}
			if jerr := json.Unmarshal(res.Stdout, &env); jerr != nil {
				t.Fatalf("envelope decode: %v (body=%q)", jerr, res.Stdout)
			}
			after := s.FeedEnvSnapshot()[tc.key]
			if tc.reject {
				if env.Status != "rejected" {
					t.Errorf("status=%q, want rejected (body=%q)", env.Status, res.Stdout)
				}
				if env.Errors[tc.key] == "" {
					t.Errorf("rejected envelope missing per-key error for %s (body=%q)", tc.key, res.Stdout)
				}
				if after != before {
					t.Errorf("%s on disk = %q after rejection, want unchanged %q", tc.key, after, before)
				}
				return
			}
			if env.Status != "applied" {
				t.Errorf("status=%q, want applied (body=%q)", env.Status, res.Stdout)
			}
			if after != tc.value {
				t.Errorf("%s on disk = %q after apply(%q), want %q",
					tc.key, after, tc.value, tc.value)
			}
		})
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

	// Add a network with test:true (the SPA default). Production returns
	// {status, id, uuid, ssid, active, changed} and leaves the tested
	// profile active.
	addEnv := decodeStdinCall(t, runStdin, priv.WifiAdd, `{"ssid":"new-net","psk":"abcd1234","priority":3,"test":true}`)
	if addEnv["status"] != "applied" {
		t.Fatalf("add status=%v (env=%v)", addEnv["status"], addEnv)
	}
	id, _ := addEnv["id"].(string)
	if id == "" {
		t.Fatalf("add: no id returned (env=%v)", addEnv)
	}
	if addEnv["uuid"] == "" || addEnv["uuid"] == nil {
		t.Fatalf("add: uuid missing (env=%v)", addEnv)
	}
	if addEnv["ssid"] != "new-net" {
		t.Fatalf("add: ssid=%v want new-net", addEnv["ssid"])
	}
	if addEnv["active"] != true {
		t.Fatalf("add: active=%v want true (test:true should activate)", addEnv["active"])
	}
	changed, _ := addEnv["changed"].([]any)
	if len(changed) != 1 || changed[0] != id {
		t.Fatalf("add: changed=%v want [%s]", addEnv["changed"], id)
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
	// maintenanceUnitActive calls all maintenance units in one
	// is-active. They must be inactive so the maintenance guard lets
	// reboot/update through.
	res, err := runner(context.Background(), []string{
		"/usr/bin/systemctl", "is-active",
		"airplanes-system-upgrade.service",
		"airplanes-update.service",
		"airplanes-update-orchestrator.service",
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

func TestSystemdRun_PinsMaintenanceUnitActivating(t *testing.T) {
	t.Parallel()
	s := mustNewState(t)
	priv := StubPrivilegedArgv()
	runner := Runner(s, priv)
	// Fire the update transient — the maintenance unit must flip to
	// `activating` so a follow-up handlers.maintenanceUnitActive guard
	// returns the unit name and the second click sees 409.
	if _, err := runner(context.Background(), priv.StartUpdate); err != nil {
		t.Fatalf("runner: %v", err)
	}
	if got := s.ServiceState("airplanes-update.service"); got != "activating" {
		t.Fatalf("after StartUpdate: airplanes-update.service=%q want activating", got)
	}
	// Fan-out is-active over the maintenance units; the update
	// one must come back activating, the others inactive.
	res, _ := runner(context.Background(), []string{
		"/usr/bin/systemctl", "is-active",
		"airplanes-system-upgrade.service",
		"airplanes-update.service",
		"airplanes-update-orchestrator.service",
	})
	lines := strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "inactive" || lines[1] != "activating" || lines[2] != "inactive" {
		t.Fatalf("is-active fan-out=%v want [inactive activating inactive]", lines)
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
