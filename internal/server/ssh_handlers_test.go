package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// sshStatusEnvelope is a minimal valid apl-ssh status envelope.
const sshStatusEnvelope = `{"status":"ok","pi_present":true,"password_auth_allowed":false,"password_hash_unlocked":false,"managed_key_present":false}`

// sshAppliedEnvelope is a minimal mutating-verb success envelope.
const sshAppliedEnvelope = `{"status":"applied","password_set":true,"reloaded":true}`

func TestSSHStatus_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r, err := c.Do(httpRequest(t, http.MethodGet, ts.URL+"/api/ssh", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestSSHStatus_ForwardsEnvelope(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(sshStatusEnvelope)}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/ssh", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if !strings.Contains(mustReadAll(t, r.Body), "pi_present") {
		t.Fatal("body did not contain pi_present")
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-ssh", "status", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	if len(calls[0].stdin) != 0 {
		t.Fatalf("status sent stdin %q, want empty", calls[0].stdin)
	}
}

// sshPost posts an SSH mutation with the harness's authenticated client and a
// matching Origin header.
func sshPost(t *testing.T, h *writeHarness, path string, body any) *http.Response {
	t.Helper()
	return postJSON(t, h.client, h.ts.URL+path, body)
}

func TestSSHEnablePassword_ReauthStripsAndForwards(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(sshAppliedEnvelope)}

	r := sshPost(t, h, "/api/ssh/enable-password", map[string]any{
		"current_password": testPassword,
		"password":         "a-strong-pi-password",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-ssh", "enable-password", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	// current_password must NOT reach the helper's stdin; the SSH password must.
	var sent map[string]any
	if err := json.Unmarshal(calls[0].stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %s", calls[0].stdin)
	}
	if _, ok := sent["current_password"]; ok {
		t.Fatalf("current_password leaked to helper stdin: %s", calls[0].stdin)
	}
	if sent["password"] != "a-strong-pi-password" {
		t.Fatalf("ssh password not forwarded: %v", sent)
	}
	// Belt-and-braces: the admin password string must not appear anywhere in
	// the bytes piped to the helper.
	if bytes.Contains(calls[0].stdin, []byte(testPassword)) {
		t.Fatalf("admin password appeared in helper stdin: %s", calls[0].stdin)
	}
}

func TestSSHSetPassword_WrongPasswordReturns401AndNoHelperCall(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(sshAppliedEnvelope)}

	r := sshPost(t, h, "/api/ssh/set-password", map[string]any{
		"current_password": "wrong-but-long-enough",
		"password":         "a-strong-pi-password",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	// On a re-auth failure the privileged helper must never be invoked.
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatalf("helper invoked despite wrong re-auth password: %v", calls)
	}
}

func TestSSHMutate_MissingCurrentPasswordReturns401(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(sshAppliedEnvelope)}

	r := sshPost(t, h, "/api/ssh/set-key", map[string]any{
		"key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 user@host",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatalf("helper invoked without re-auth: %v", calls)
	}
}

func TestSSHDisablePassword_ForwardsEmptyObject(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"applied","password_disabled":true,"reloaded":true}`)}

	r := sshPost(t, h, "/api/ssh/disable-password", map[string]any{
		"current_password": testPassword,
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-ssh", "disable-password", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	// After stripping current_password, disable-password forwards an empty
	// object (no fields), never the admin password.
	var sent map[string]any
	if err := json.Unmarshal(calls[0].stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %s", calls[0].stdin)
	}
	if len(sent) != 0 {
		t.Fatalf("disable-password forwarded extra fields: %v", sent)
	}
}

func TestSSHSetKey_ForwardsKeyAfterReauth(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"applied","key_set":true}`)}

	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabc user@host"
	r := sshPost(t, h, "/api/ssh/set-key", map[string]any{
		"current_password": testPassword,
		"key":              key,
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["key"] != key {
		t.Fatalf("key not forwarded: %v", sent)
	}
	if _, ok := sent["current_password"]; ok {
		t.Fatalf("current_password leaked: %s", calls[0].stdin)
	}
}

// TestSSHMutate_RejectedEnvelopeMapsTo400 confirms the helper's rejected
// envelope (e.g. password too short, bad pubkey) maps to 400 and is forwarded.
func TestSSHMutate_RejectedEnvelopeMapsTo400(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"rejected","reason":"password_too_short","message":"password must be at least 12 characters"}`)}

	r := sshPost(t, h, "/api/ssh/enable-password", map[string]any{
		"current_password": testPassword,
		"password":         "short",
	})
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if !strings.Contains(mustReadAll(t, r.Body), "password_too_short") {
		t.Fatal("rejected envelope not forwarded to client")
	}
}

func TestSSHMutate_LockTimeoutMapsTo503(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"lock_timeout","message":"another ssh operation is in progress"}`)}

	r := sshPost(t, h, "/api/ssh/clear-key", map[string]any{"current_password": testPassword})
	defer r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
}

func TestSSHMutate_EmptyHelperStdoutMapsTo500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: nil, Stderr: []byte("apl-ssh: binary missing")}

	r := sshPost(t, h, "/api/ssh/clear-key", map[string]any{"current_password": testPassword})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestSSHMutate_RequiresOriginHeader(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	body, _ := json.Marshal(map[string]any{"current_password": testPassword})
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/ssh/clear-key", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Origin header → requireOriginMatchesHost rejects before the handler.
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", r.StatusCode)
	}
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatalf("helper invoked despite origin rejection: %v", calls)
	}
}

func TestSSHMutate_NullBodyRejected(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/ssh/set-key", strings.NewReader("null"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin(h.ts.URL))
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatal("helper invoked for null body")
	}
}

func TestSSHMutate_RejectsNonJSONContentType(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/ssh/clear-key", strings.NewReader("current_password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin(h.ts.URL))
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatal("helper invoked for non-JSON body")
	}
}

// TestDefaultPrivilegedArgv_SSH pins the production argv shapes so the Go
// default and the sudoers file under files/etc/sudoers.d/ cannot drift.
func TestDefaultPrivilegedArgv_SSH(t *testing.T) {
	t.Parallel()
	priv := DefaultPrivilegedArgv()
	want := map[string][]string{
		"status":           {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-ssh", "status", "--json"},
		"enable-password":  {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-ssh", "enable-password", "--json"},
		"set-password":     {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-ssh", "set-password", "--json"},
		"disable-password": {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-ssh", "disable-password", "--json"},
		"set-key":          {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-ssh", "set-key", "--json"},
		"clear-key":        {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-ssh", "clear-key", "--json"},
	}
	got := map[string][]string{
		"status":           priv.SSHStatus,
		"enable-password":  priv.SSHEnablePassword,
		"set-password":     priv.SSHSetPassword,
		"disable-password": priv.SSHDisablePassword,
		"set-key":          priv.SSHSetKey,
		"clear-key":        priv.SSHClearKey,
	}
	for verb, w := range want {
		if !reflect.DeepEqual(got[verb], w) {
			t.Errorf("SSH %s argv = %v, want %v", verb, got[verb], w)
		}
	}
}
