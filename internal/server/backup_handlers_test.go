package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// --- fixtures + helpers ----------------------------------------------------

const testIdentitySection = `{"schema_version":1,"feeder_uuid":"11111111-2222-3333-4444-555555555555","claim":{"secret":"ABCDEFGHIJKLMNOP","version":null}}`

const testAggExportEnvelope = `{"protocol_version":1,"result":"ok","kind":"aggregator-backup","schema_version":1,"aggregators":{}}`

func validPHC(t *testing.T) string {
	t.Helper()
	phc, err := auth.Hash(testPassword, fastTestParams)
	if err != nil {
		t.Fatal(err)
	}
	return phc
}

// argvHas reports whether every token appears somewhere in argv.
func argvHas(argv []string, tokens ...string) bool {
	for _, tok := range tokens {
		found := false
		for _, a := range argv {
			if a == tok {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// okStdin is a stdinResultFor that returns a success envelope for each stdin
// shell-out a restore (or export) fans out to.
func okStdin(argv []string) wexec.Result {
	switch {
	case argvHas(argv, "apl-feed", "apply"):
		return wexec.Result{Stdout: []byte(`{"status":"applied","changed":[],"pending_restart":[]}`)}
	case argvHas(argv, "identity-import"):
		return wexec.Result{} // success = nil error; stdout ignored
	case argvHas(argv, "apl-aggregator", "import"):
		return wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"ok"}`)}
	case argvHas(argv, "apl-aggregator", "export"):
		return wexec.Result{Stdout: []byte(testAggExportEnvelope)}
	case argvHas(argv, "apl-wifi", "import"):
		return wexec.Result{Stdout: []byte(`{"status":"applied","schema_version":1,"imported":0,"results":[]}`)}
	case argvHas(argv, "apl-wifi", "export"):
		return wexec.Result{Stdout: []byte(`{"status":"ok","schema_version":1,"networks":[]}`)}
	}
	return wexec.Result{}
}

func combinedBody(sections map[string]any) map[string]any {
	return map[string]any{
		"schema_version": combinedBackupVersion,
		"kind":           combinedBackupKind,
		"sections":       sections,
	}
}

// fullRestoreSections is a complete, valid set of sections for a restore.
func fullRestoreSections(t *testing.T) map[string]any {
	return map[string]any{
		"identity":    json.RawMessage(testIdentitySection),
		"settings":    map[string]any{"schema_version": 1, "values": map[string]string{"LATITUDE": "51.5"}},
		"aggregators": json.RawMessage(testAggExportEnvelope),
		"wifi":        map[string]any{"schema_version": 1, "networks": []any{}},
		"password":    map[string]any{"schema_version": 1, "phc": validPHC(t)},
	}
}

// parseRestoreStream decodes the NDJSON restore response into a section→status
// map plus the terminal summary event.
func parseRestoreStream(t *testing.T, b []byte) (map[string]string, restoreEvent) {
	t.Helper()
	statuses := map[string]string{}
	var summary restoreEvent
	sawSummary := false
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev restoreEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("bad ndjson line %q: %v", line, err)
		}
		switch ev.Type {
		case "section":
			statuses[ev.Section] = ev.Status
		case "summary":
			summary = ev
			sawSummary = true
		}
	}
	if !sawSummary {
		t.Fatalf("restore stream had no summary event; body=%q", b)
	}
	return statuses, summary
}

// --- export ----------------------------------------------------------------

func TestBackupExport_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestBackupExport_AssemblesAllSections(t *testing.T) {
	h := newWriteHarness(t)
	h.runnerResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "identity-export") {
			return wexec.Result{Stdout: []byte(testIdentitySection)}
		}
		return wexec.Result{}
	}
	h.stdinResultFor = okStdin

	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if cd := r.Header.Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want attachment", cd)
	}
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", r.Header.Get("Cache-Control"))
	}

	var env combinedBackupEnvelope
	if err := json.Unmarshal(readBody(t, r), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Kind != combinedBackupKind || env.SchemaVersion != combinedBackupVersion {
		t.Errorf("envelope kind/version = %q/%d", env.Kind, env.SchemaVersion)
	}
	for _, name := range []string{"identity", "settings", "aggregators", "wifi", "password"} {
		if _, ok := env.Sections[name]; !ok {
			t.Errorf("missing section %q", name)
		}
	}
	// Identity section forwarded verbatim.
	if !strings.Contains(string(env.Sections["identity"]), "11111111-2222-3333-4444-555555555555") {
		t.Errorf("identity section = %s", env.Sections["identity"])
	}
	// Password section carries the on-disk argon2id hash, not a plaintext.
	var pw passwordSection
	if err := json.Unmarshal(env.Sections["password"], &pw); err != nil {
		t.Fatalf("decode password section: %v", err)
	}
	if !strings.HasPrefix(pw.PHC, "$argon2id$") {
		t.Errorf("password.phc = %q, want argon2id PHC", pw.PHC)
	}
	// Wi-Fi section reshaped to {schema_version, networks} — the helper's
	// "status" field must be stripped so the importer (which rejects unknown
	// keys) accepts it.
	if strings.Contains(string(env.Sections["wifi"]), "status") {
		t.Errorf("wifi section should not carry status: %s", env.Sections["wifi"])
	}
}

func TestBackupExport_OmitsIdentityWhenUnclaimed(t *testing.T) {
	// A feeder that hasn't been claimed has no identity to back up — the
	// identity section is omitted, but the rest of the backup still succeeds.
	h := newWriteHarness(t, withoutIdentity())
	h.stdinResultFor = okStdin
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var env combinedBackupEnvelope
	if err := json.Unmarshal(readBody(t, r), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if _, ok := env.Sections["identity"]; ok {
		t.Error("identity section present, want omitted for an unclaimed feeder")
	}
	for _, name := range []string{"settings", "wifi", "password"} {
		if _, ok := env.Sections[name]; !ok {
			t.Errorf("missing section %q on an unclaimed-feeder backup", name)
		}
	}
}

func TestBackupExport_FailsOnCorruptIdentity(t *testing.T) {
	// A present-but-corrupt claim secret is NOT "unclaimed" — the export must
	// fail loud, never produce an identity-less backup that hides the damage.
	h := newWriteHarness(t, withCorruptIdentity())
	h.stdinResultFor = okStdin
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (corrupt identity must fail loud)", r.StatusCode)
	}
}

func TestBackupExport_FailsLoudlyOnSectionError(t *testing.T) {
	h := newWriteHarness(t)
	// Identity export returns non-canonical JSON → the whole export must 500,
	// never emit a backup missing the identity section.
	h.runnerResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "identity-export") {
			return wexec.Result{Stdout: []byte(`not json`)}
		}
		return wexec.Result{}
	}
	h.stdinResultFor = okStdin
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

// --- authed restore --------------------------------------------------------

func TestBackupRestore_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/backup/restore", combinedBody(map[string]any{}))
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestBackupRestore_StreamsPerSectionResults(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = okStdin
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(fullRestoreSections(t)))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); !strings.Contains(ct, "ndjson") {
		t.Errorf("Content-Type = %q, want ndjson", ct)
	}
	statuses, summary := parseRestoreStream(t, readBody(t, r))
	for _, name := range []string{"settings", "identity", "aggregators", "wifi", "password"} {
		if statuses[name] != "applied" {
			t.Errorf("section %q status = %q, want applied", name, statuses[name])
		}
	}
	if !summary.PasswordChanged {
		t.Error("summary.password_changed = false, want true (password section applied)")
	}
}

func TestBackupRestore_PartialFailureDoesNotAbort(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "apl-wifi", "import") {
			return wexec.Result{Stdout: []byte(`{"status":"rejected"}`)}
		}
		return okStdin(argv)
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(fullRestoreSections(t)))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	statuses, _ := parseRestoreStream(t, readBody(t, r))
	if statuses["wifi"] != "failed" {
		t.Errorf("wifi status = %q, want failed", statuses["wifi"])
	}
	// A failed section must not stop the others.
	if statuses["settings"] != "applied" {
		t.Errorf("settings status = %q, want applied", statuses["settings"])
	}
}

func TestBackupRestore_AggregatorEnabledIsSkippedNotFailed(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "apl-aggregator", "import") {
			return wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"error","error_code":"rejected","message":"fr24 is enabled; disable it before importing"}`)}
		}
		return okStdin(argv)
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(fullRestoreSections(t)))
	defer r.Body.Close()
	statuses, _ := parseRestoreStream(t, readBody(t, r))
	if statuses["aggregators"] != "skipped" {
		t.Errorf("aggregators status = %q, want skipped (enabled adapter)", statuses["aggregators"])
	}
}

func TestBackupRestore_RejectsOverCostPHC(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = okStdin
	// A PHC declaring a memory cost above MaxStoredParams would turn every
	// future login into a DoS — it must be rejected before any section runs.
	badPHC := strings.Replace(validPHC(t), "m=8,", "m=2000000,", 1)
	sections := fullRestoreSections(t)
	sections["password"] = map[string]any{"schema_version": 1, "phc": badPHC}
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(sections))
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

// --- first-run restore -----------------------------------------------------

func TestBackupRestoreSetup_RejectsWhenInitialized(t *testing.T) {
	h := newWriteHarness(t) // sets a password → initialized
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore-setup", combinedBody(fullRestoreSections(t)))
	defer r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", r.StatusCode)
	}
}

func TestBackupRestoreSetup_RequiresPasswordSection(t *testing.T) {
	h := newWriteHarness(t, withoutSetup())
	sections := fullRestoreSections(t)
	delete(sections, "password")
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore-setup", combinedBody(sections))
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestBackupExport_FailsOnMalformedWifiSection(t *testing.T) {
	h := newWriteHarness(t)
	h.runnerResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "identity-export") {
			return wexec.Result{Stdout: []byte(testIdentitySection)}
		}
		return wexec.Result{}
	}
	h.stdinResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "apl-wifi", "export") {
			// "ok" but no networks — must NOT become a backup that lost the
			// saved networks; the export must fail loudly instead.
			return wexec.Result{Stdout: []byte(`{"status":"ok","schema_version":1}`)}
		}
		return okStdin(argv)
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestBackupRestore_InvalidIdentityFails(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = okStdin
	sections := fullRestoreSections(t)
	sections["identity"] = json.RawMessage(`{"schema_version":1,"feeder_uuid":"not-a-uuid","claim":{"secret":"ABCDEFGHIJKLMNOP","version":null}}`)
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(sections))
	defer r.Body.Close()
	statuses, _ := parseRestoreStream(t, readBody(t, r))
	if statuses["identity"] != "failed" {
		t.Errorf("identity status = %q, want failed", statuses["identity"])
	}
	if statuses["settings"] != "applied" {
		t.Errorf("settings status = %q, want applied (a bad identity must not abort the rest)", statuses["settings"])
	}
}

func TestBackupRestore_RejectsOversizedBody(t *testing.T) {
	h := newWriteHarness(t)
	huge := strings.Repeat("a", combinedBackupBodyLimit+1024)
	sections := map[string]any{"settings": map[string]any{"schema_version": 1, "values": map[string]string{"NOTE": huge}}}
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(sections))
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestRestoreSettings_SkipsWhenSchemaUnavailable(t *testing.T) {
	// Direct method test: with no schema cache, settings restore must skip
	// (not crash, not apply a partial config).
	s := &Server{}
	env := combinedBackupEnvelope{Sections: map[string]json.RawMessage{
		"settings": json.RawMessage(`{"schema_version":1,"values":{"LATITUDE":"1"}}`),
	}}
	status, reason := s.restoreSettings(context.Background(), env)
	if status != "skipped" || reason != "schema_unavailable" {
		t.Errorf("got (%q,%q), want (skipped, schema_unavailable)", status, reason)
	}
}

func TestBackupRestoreSetup_RejectsOverCostPHCWithoutInitializing(t *testing.T) {
	h := newWriteHarness(t, withoutSetup())
	badPHC := strings.Replace(validPHC(t), "m=8,", "m=2000000,", 1)
	sections := fullRestoreSections(t)
	sections["password"] = map[string]any{"schema_version": 1, "phc": badPHC}
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore-setup", combinedBody(sections))
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
	// The over-cost PHC must have been rejected BEFORE store.Setup — the device
	// is still uninitialized, so a normal setup still succeeds.
	r2 := postJSON(t, httpClient(t), h.ts.URL+"/api/setup", map[string]string{"password": "a valid long password"})
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Errorf("post-reject /api/setup status = %d, want 200 (still uninitialized)", r2.StatusCode)
	}
}

func TestBackupRestoreSetup_AppliesPasswordAndAutoLogsIn(t *testing.T) {
	h := newWriteHarness(t, withoutSetup())
	h.stdinResultFor = okStdin
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore-setup", combinedBody(fullRestoreSections(t)))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	// Auto-login: a session cookie is issued in the response headers.
	sawSession := false
	for _, c := range r.Cookies() {
		if c.Name == SessionCookieName && c.Value != "" {
			sawSession = true
		}
	}
	if !sawSession {
		t.Error("no session cookie issued on first-run restore")
	}
	statuses, _ := parseRestoreStream(t, readBody(t, r))
	if statuses["password"] != "applied" {
		t.Errorf("password status = %q, want applied", statuses["password"])
	}
	if statuses["settings"] != "applied" {
		t.Errorf("settings status = %q, want applied", statuses["settings"])
	}

	// The device is now initialized — a second setup attempt is refused.
	r2 := postJSON(t, httpClient(t), h.ts.URL+"/api/setup", map[string]string{"password": "another long password"})
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("post-restore /api/setup status = %d, want 409 (initialized)", r2.StatusCode)
	}
}
