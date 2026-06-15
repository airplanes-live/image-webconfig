package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// --- /api/identity/export -----------------------------------------------

func TestIdentityExport_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/identity/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestIdentityExport_ForwardsWrapperStdout(t *testing.T) {
	h := newWriteHarness(t)
	envelope := `{"schema_version":1,"created_at":"2026-05-18T00:00:00Z","feeder_uuid":"11111111-2222-3333-4444-555555555555","claim":{"secret":"ABCDEFGHIJKLMNOP","version":null}}`
	h.runnerResultFor = func(argv []string) wexec.Result {
		if len(argv) >= 2 && argv[1] == "identity-export" {
			return wexec.Result{Stdout: []byte(envelope)}
		}
		return wexec.Result{}
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", r.Header.Get("Cache-Control"))
	}
	body := readBody(t, r)
	if string(body) != envelope {
		t.Errorf("body = %q, want %q", body, envelope)
	}
	// Verify the privileged argv hit the runner.
	calls := h.callsCopy()
	found := false
	for _, c := range calls {
		if len(c) >= 2 && c[1] == "identity-export" {
			found = true
		}
	}
	if !found {
		t.Errorf("calls = %v, expected identity-export argv", calls)
	}
}

func TestIdentityExport_RejectsNonCanonicalWrapperOutput(t *testing.T) {
	h := newWriteHarness(t)
	h.runnerResultFor = func(_ []string) wexec.Result {
		return wexec.Result{Stdout: []byte(`{"schema_version":2,"feeder_uuid":"x"}`)}
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestIdentityExport_RunnerErrorMaps500(t *testing.T) {
	h := newWriteHarness(t)
	h.runnerErrFor = func(argv []string) error {
		if len(argv) >= 2 && argv[1] == "identity-export" {
			return errors.New("apl-feed exit 1")
		}
		return nil
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/export", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

// --- /api/identity/import -----------------------------------------------

func TestIdentityImport_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/identity/import", canonicalImportBody())
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestIdentityImport_RejectsNonJSONContentType(t *testing.T) {
	h := newWriteHarness(t)
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/identity/import",
		bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", h.ts.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestIdentityImport_RejectsExtraFields(t *testing.T) {
	h := newWriteHarness(t)
	body := map[string]any{
		"schema_version": 1,
		"created_at":     "2026-05-18T00:00:00Z",
		"feeder_uuid":    "11111111-2222-3333-4444-555555555555",
		"claim":          map[string]any{"secret": "ABCDEFGHIJKLMNOP", "version": nil},
		"extra_field":    "rejected",
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", body)
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestIdentityImport_RejectsWrongSchemaVersion(t *testing.T) {
	h := newWriteHarness(t)
	body := map[string]any{
		"schema_version": 2,
		"feeder_uuid":    "11111111-2222-3333-4444-555555555555",
		"claim":          map[string]any{"secret": "ABCDEFGHIJKLMNOP", "version": nil},
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", body)
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

func TestIdentityImport_RejectsMalformedUUID(t *testing.T) {
	h := newWriteHarness(t)
	bad := []string{
		"",
		"not-a-uuid",
		"11111111-2222-3333-4444-55555555555",  // too short
		"11111111-2222-3333-4444-5555555555555", // too long
		"GGGGGGGG-2222-3333-4444-555555555555",  // non-hex
		"11111111_2222-3333-4444-555555555555",  // wrong separator
	}
	for _, u := range bad {
		body := map[string]any{
			"schema_version": 1,
			"feeder_uuid":    u,
			"claim":          map[string]any{"secret": "ABCDEFGHIJKLMNOP", "version": nil},
		}
		r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", body)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("uuid=%q status = %d, want 400", u, r.StatusCode)
		}
	}
}

func TestIdentityImport_AcceptsLowercaseUUID(t *testing.T) {
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", map[string]any{
		"schema_version": 1,
		"feeder_uuid":    "abcdef01-2345-6789-abcd-ef0123456789",
		"claim":          map[string]any{"secret": "ABCDEFGHIJKLMNOP", "version": nil},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
}

func TestIdentityImport_AcceptsHyphenatedSecret(t *testing.T) {
	h := newWriteHarness(t)
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", map[string]any{
		"schema_version": 1,
		"feeder_uuid":    "11111111-2222-3333-4444-555555555555",
		"claim":          map[string]any{"secret": "ABCD-EFGH-IJKL-MNOP", "version": nil},
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
}

func TestIdentityImport_RejectsMalformedSecret(t *testing.T) {
	h := newWriteHarness(t)
	bad := []string{
		"",
		"too-short",
		"ABCDEFGHIJKLMNO",   // 15 chars after canonicalisation
		"ABCDEFGHIJKLMNOPQ", // 17
		"!@#$%^&*()12345A",  // bad characters
	}
	for _, s := range bad {
		body := map[string]any{
			"schema_version": 1,
			"feeder_uuid":    "11111111-2222-3333-4444-555555555555",
			"claim":          map[string]any{"secret": s, "version": nil},
		}
		r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", body)
		r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("secret=%q status = %d, want 400", s, r.StatusCode)
		}
	}
}

func TestIdentityImport_PipesCanonicalEnvelope(t *testing.T) {
	h := newWriteHarness(t)
	body := map[string]any{
		"schema_version": 1,
		"created_at":     "2026-05-18T12:34:56Z",
		"feeder_uuid":    "abcdef01-2345-6789-abcd-ef0123456789",
		"claim":          map[string]any{"secret": "ABCD-EFGH-IJKL-MNOP", "version": 7},
	}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", body)
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	if got := calls[0].argv; len(got) < 2 || got[1] != "identity-import" {
		t.Errorf("argv = %v, want identity-import", got)
	}
	// The handler re-marshals before sending: the body shouldn't
	// match the original Map (Go's encoding/json maintains struct
	// field order). Just assert it's parseable canonical envelope.
	var seen identityBackupEnvelope
	if err := json.Unmarshal(calls[0].stdin, &seen); err != nil {
		t.Fatalf("stdin not JSON: %v (body=%q)", err, calls[0].stdin)
	}
	if seen.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", seen.SchemaVersion)
	}
	if seen.FeederUUID != "abcdef01-2345-6789-abcd-ef0123456789" {
		t.Errorf("feeder_uuid = %q", seen.FeederUUID)
	}
	if seen.Claim.Secret != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("claim.secret = %q (handler should pass user input through; apl-feed canonicalises)", seen.Claim.Secret)
	}
	if seen.Claim.Version == nil || *seen.Claim.Version != 7 {
		t.Errorf("claim.version = %v, want 7", seen.Claim.Version)
	}
}

func TestIdentityImport_RunnerErrorMaps500AndHidesStderr(t *testing.T) {
	h := newWriteHarness(t)
	h.stdinErr = errors.New("apl-feed exit 1")
	h.stdinResult = wexec.Result{Stderr: []byte("super-secret-internal-path: /home/foo/leaked\n")}
	r := postJSON(t, h.client, h.ts.URL+"/api/identity/import", canonicalImportBody())
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
	body := readBody(t, r)
	if strings.Contains(string(body), "super-secret-internal-path") {
		t.Errorf("response body leaked stderr: %s", body)
	}
}

func canonicalImportBody() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"feeder_uuid":    "11111111-2222-3333-4444-555555555555",
		"claim":          map[string]any{"secret": "ABCDEFGHIJKLMNOP", "version": nil},
	}
}

// --- helpers (light wrappers over harness primitives) -------------------

func readBody(t *testing.T, r *http.Response) []byte {
	t.Helper()
	b := new(bytes.Buffer)
	if _, err := b.ReadFrom(r.Body); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
