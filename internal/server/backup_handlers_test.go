package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// --- fixtures + helpers ----------------------------------------------------

const testIdentitySection = `{"schema_version":1,"feeder_uuid":"11111111-2222-3333-4444-555555555555","claim":{"secret":"ABCDEFGHIJKLMNOP","version":null}}`

// testAggExportEnvelope is the no-aggregators-configured case (empty set), as
// produced by `apl-aggregator export` on a feeder with nothing set up. Restore
// must treat it as a skip, not a failure (TestBackupRestore_EmptyAggregatorsSkippedNotFailed).
const testAggExportEnvelope = `{"protocol_version":1,"result":"ok","kind":"aggregator-backup","schema_version":1,"aggregators":{}}`

// testAggImportPopulated carries one adapter, so a restore exercises the real
// import-helper path rather than the empty-set short-circuit. Export-shaped
// (protocol_version + result) to match the envelope a real combined backup
// embeds. Used by fullRestoreSections so the happy-path and enabled-adapter
// tests are meaningful.
const testAggImportPopulated = `{"protocol_version":1,"result":"ok","kind":"aggregator-backup","schema_version":1,"aggregators":{"fr24":{"mlat_enabled":false,"fields":{"sharing_key":"deadbeef"}}}}`

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
		"aggregators": json.RawMessage(testAggImportPopulated),
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
	// Aggregators section reshaped to {kind, schema_version, aggregators} — the
	// helper's RPC envelope fields (result, protocol_version) mean nothing at
	// rest and must be stripped, mirroring the Wi-Fi reshape above.
	agg := string(env.Sections["aggregators"])
	if strings.Contains(agg, "protocol_version") || strings.Contains(agg, `"result"`) {
		t.Errorf("aggregators section should not carry RPC envelope fields: %s", agg)
	}
	if !strings.Contains(agg, "aggregator-backup") {
		t.Errorf("aggregators section missing kind: %s", agg)
	}
}

// wireExportFakes points both fake runners at canned success outputs so a
// full-section export succeeds (identity via the plain runner, wifi and
// aggregators via the stdin runner).
func wireExportFakes(h *writeHarness) {
	h.runnerResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "identity-export") {
			return wexec.Result{Stdout: []byte(testIdentitySection)}
		}
		return wexec.Result{}
	}
	h.stdinResultFor = okStdin
}

// exportFilename POSTs an export and returns the Content-Disposition filename
// parameter, asserting the disposition itself is a well-formed attachment.
func exportFilename(t *testing.T, h *writeHarness) string {
	t.Helper()
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	disp, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition %q: %v", r.Header.Get("Content-Disposition"), err)
	}
	if disp != "attachment" {
		t.Errorf("disposition = %q, want attachment", disp)
	}
	return params["filename"]
}

func TestBackupExport_FilenameCarriesHostnameAndDate(t *testing.T) {
	// 23:59 UTC: the handler reads the clock once, so the filename date and
	// created_at can't disagree even at a midnight boundary.
	h := newWriteHarness(t,
		withNow(time.Date(2026, 7, 2, 23, 59, 59, 0, time.UTC)),
		withHostname("feeder-attic", nil),
	)
	wireExportFakes(h)
	if got, want := exportFilename(t, h), "airplanes-feeder-backup-feeder-attic-2026-07-02.json"; got != want {
		t.Errorf("filename = %q, want %q", got, want)
	}
}

func TestBackupExport_FilenameOmitsHostOnLookupFailure(t *testing.T) {
	h := newWriteHarness(t,
		withNow(time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)),
		withHostname("", errors.New("hostname unavailable")),
	)
	wireExportFakes(h)
	if got, want := exportFilename(t, h), "airplanes-feeder-backup-2026-07-02.json"; got != want {
		t.Errorf("filename = %q, want %q", got, want)
	}
}

func TestBackupExport_FilenameSanitizesHostileHostname(t *testing.T) {
	// A hostname with header-metacharacters must neither corrupt the header
	// nor smuggle a second one; the sanitizer reduces it to safe ASCII.
	h := newWriteHarness(t,
		withNow(time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)),
		withHostname("evil\r\nX-Injected: 1", nil),
	)
	wireExportFakes(h)
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if got := r.Header.Get("X-Injected"); got != "" {
		t.Errorf("X-Injected header smuggled through: %q", got)
	}
	disp, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
	if err != nil || disp != "attachment" {
		t.Fatalf("Content-Disposition %q: disp=%q err=%v", r.Header.Get("Content-Disposition"), disp, err)
	}
	if got, want := params["filename"], "airplanes-feeder-backup-evil-X-Injected-1-2026-07-02.json"; got != want {
		t.Errorf("filename = %q, want %q", got, want)
	}
}

func TestBackupExport_FilenameDateMatchesCreatedAt(t *testing.T) {
	// The handler must read the clock exactly once. The fake clock jumps a
	// full day on every read, so a second read inside the handler would put
	// the filename and created_at on different days.
	var mu sync.Mutex
	calls := 0
	base := time.Date(2026, 7, 2, 23, 59, 59, 0, time.UTC)
	h := newWriteHarness(t,
		withNowFunc(func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			now := base.AddDate(0, 0, calls)
			calls++
			return now
		}),
		withHostname("feeder-attic", nil),
	)
	wireExportFakes(h)

	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("parse Content-Disposition: %v", err)
	}
	var env combinedBackupEnvelope
	if err := json.Unmarshal(readBody(t, r), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(env.CreatedAt) < 10 {
		t.Fatalf("created_at = %q", env.CreatedAt)
	}
	if day := env.CreatedAt[:10]; !strings.Contains(params["filename"], day) {
		t.Errorf("filename %q does not carry created_at's date %q", params["filename"], day)
	}
}

func TestSanitizeHostToken(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		{"feeder-attic", "feeder-attic"},
		{"Feeder.Attic-2", "Feeder.Attic-2"},
		{"", ""},
		{"---", ""},
		{"...", ""},
		{"weird host!name", "weird-host-name"},
		{"tab\there", "tab-here"},
		{"\"quoted\\", "quoted"},
		{"höstnäme", "h-stn-me"},
		{"日本語", ""},
		// Overlong input is capped at 63 chars.
		{strings.Repeat("a", 80), strings.Repeat("a", 63)},
		// A separator landing on the cap boundary is re-trimmed.
		{strings.Repeat("a", 62) + "-b", strings.Repeat("a", 62)},
		{strings.Repeat("a", 62) + ".b", strings.Repeat("a", 62)},
	}
	for _, c := range cases {
		if got := sanitizeHostToken(c.in); got != c.want {
			t.Errorf("sanitizeHostToken(%q) = %q, want %q", c.in, got, c.want)
		}
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

func TestBackupExport_SelectionNarrowsSections(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = okStdin // wifi export
	// identity-export runner is deliberately NOT stubbed: selecting only
	// settings + wifi must never invoke it (a deselected secret isn't read).
	h.runnerResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "identity-export") {
			t.Errorf("identity export ran for a settings+wifi selection: %v", argv)
		}
		return wexec.Result{}
	}

	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{"sections": []string{"settings", "wifi"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var env combinedBackupEnvelope
	if err := json.Unmarshal(readBody(t, r), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	for _, want := range []string{"settings", "wifi"} {
		if _, ok := env.Sections[want]; !ok {
			t.Errorf("selected section %q missing", want)
		}
	}
	for _, omit := range []string{"identity", "aggregators", "password"} {
		if _, ok := env.Sections[omit]; ok {
			t.Errorf("deselected section %q present", omit)
		}
	}
}

func TestBackupExport_UnknownOnlySelectionRejected(t *testing.T) {
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{"sections": []string{"bogus"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestBackupExport_MalformedSelectionFallsBackToAll(t *testing.T) {
	// A wrong-typed selection element makes the decoder error after partially
	// populating the slice; the handler must discard that and export every
	// section rather than a silently narrowed (or empty) one.
	h := newWriteHarness(t)
	h.runnerResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "identity-export") {
			return wexec.Result{Stdout: []byte(testIdentitySection)}
		}
		return wexec.Result{}
	}
	h.stdinResultFor = okStdin

	r := postJSON(t, h.client, h.ts.URL+"/api/backup/export", map[string]any{"sections": []any{123}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var env combinedBackupEnvelope
	if err := json.Unmarshal(readBody(t, r), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	for _, name := range []string{"identity", "settings", "aggregators", "wifi", "password"} {
		if _, ok := env.Sections[name]; !ok {
			t.Errorf("malformed selection dropped section %q; want full backup", name)
		}
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

// The aggregators section is handed to the import helper byte-for-byte: the Go
// side owns no structural validation, so a transform here would silently corrupt
// a restore. (This is the verbatim-pipe guarantee the now-removed standalone
// /api/aggregators/import route used to pin.)
func TestBackupRestore_AggregatorsSectionPipedVerbatim(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = okStdin
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(fullRestoreSections(t)))
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	statuses, _ := parseRestoreStream(t, readBody(t, r))
	if statuses["aggregators"] != "applied" {
		t.Fatalf("aggregators status = %q, want applied", statuses["aggregators"])
	}
	var got []byte
	for _, c := range h.stdinCallsCopy() {
		if argvHas(c.argv, "apl-aggregator", "import") {
			got = c.stdin
			break
		}
	}
	if got == nil {
		t.Fatal("apl-aggregator import was never invoked")
	}
	if string(got) != testAggImportPopulated {
		t.Errorf("import stdin = %s\nwant verbatim section = %s", got, testAggImportPopulated)
	}
}

// Only a well-formed, genuinely-empty aggregator-backup short-circuits to a
// skip; everything else (populated, null, absent, array, wrong kind/schema)
// must still reach the import helper, which owns structural validation. The
// fake records whether import ran and returns a response per case, so the test
// pins both the resulting status AND the short-circuit boundary — removing the
// short-circuit flips the empty case to importCalled=true and "failed".
func TestBackupRestore_EmptyAggregatorsSkippedNotFailed(t *testing.T) {
	const rejectEmpty = `{"protocol_version":1,"result":"error","error_code":"rejected","message":"backup contains no aggregators"}`
	const ok = `{"protocol_version":1,"result":"ok"}`
	cases := []struct {
		name       string
		section    string
		importResp string
		wantStatus string
		wantImport bool
	}{
		// The real empty export — must skip without touching the helper.
		{"valid empty set", testAggExportEnvelope, rejectEmpty, "skipped", false},
		// The reshaped export drops result/protocol_version; a trimmed empty set
		// must still short-circuit to a skip exactly like the full envelope.
		{"trimmed empty set", `{"kind":"aggregator-backup","schema_version":1,"aggregators":{}}`, rejectEmpty, "skipped", false},
		// A populated backup goes through the helper and applies.
		{"populated", testAggImportPopulated, ok, "applied", true},
		// A trimmed populated export (new shape) must apply just the same.
		{"trimmed populated", `{"kind":"aggregator-backup","schema_version":1,"aggregators":{"fr24":{"mlat_enabled":false,"fields":{"sharing_key":"deadbeef"}}}}`, ok, "applied", true},
		// Malformed / ambiguous shapes must NOT be mistaken for empty: they
		// reach the helper rather than being silently skipped.
		{"aggregators null", `{"kind":"aggregator-backup","schema_version":1,"aggregators":null}`, rejectEmpty, "failed", true},
		{"aggregators absent", `{"kind":"aggregator-backup","schema_version":1}`, rejectEmpty, "failed", true},
		{"aggregators array", `{"kind":"aggregator-backup","schema_version":1,"aggregators":[]}`, rejectEmpty, "failed", true},
		{"wrong kind empty obj", `{"kind":"nope","schema_version":1,"aggregators":{}}`, rejectEmpty, "failed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWriteHarness(t)
			importCalled := false
			h.stdinResultFor = func(argv []string) wexec.Result {
				if argvHas(argv, "apl-aggregator", "import") {
					importCalled = true
					return wexec.Result{Stdout: []byte(tc.importResp)}
				}
				return okStdin(argv)
			}
			sections := fullRestoreSections(t)
			sections["aggregators"] = json.RawMessage(tc.section)
			r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(sections))
			defer r.Body.Close()
			statuses, _ := parseRestoreStream(t, readBody(t, r))
			if statuses["aggregators"] != tc.wantStatus {
				t.Errorf("aggregators status = %q, want %q", statuses["aggregators"], tc.wantStatus)
			}
			if importCalled != tc.wantImport {
				t.Errorf("import invoked = %v, want %v", importCalled, tc.wantImport)
			}
		})
	}
}

// A successful aggregator restore seeds identities disabled, so the checklist
// must carry a non-empty reason telling the operator to re-enable them —
// otherwise the row reads a bare "restored" and the restore looks like a no-op.
func TestBackupRestore_AggregatorsAppliedReasonGuidesEnable(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinResultFor = func(argv []string) wexec.Result {
		if argvHas(argv, "apl-aggregator", "import") {
			return wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"ok"}`)}
		}
		return okStdin(argv)
	}
	sections := fullRestoreSections(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/backup/restore", combinedBody(sections))
	defer r.Body.Close()

	var reason string
	found := false
	for _, line := range bytes.Split(bytes.TrimSpace(readBody(t, r)), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev restoreEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			t.Fatalf("bad ndjson line %q: %v", line, err)
		}
		if ev.Type == "section" && ev.Section == "aggregators" {
			reason, found = ev.Reason, true
		}
	}
	if !found {
		t.Fatal("no aggregators section event in restore stream")
	}
	if reason == "" {
		t.Error("aggregators applied reason is empty; operator gets no enable guidance")
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
