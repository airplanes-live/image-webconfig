package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// httpRequest is a small wrapper around http.NewRequest that attaches the
// Origin header for the requireOriginMatchesHost middleware.
func httpRequest(t *testing.T, method, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Origin", origin(url))
	return req
}

func TestWifiList_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r, err := c.Do(httpRequest(t, http.MethodGet, ts.URL+"/api/wifi", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestWifiList_ForwardsEnvelope(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"ok","networks":[{"id":"airplanes-config-wifi","ssid":"HomeNet","managed":true}]}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/wifi", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
	body := mustReadAll(t, r.Body)
	if !strings.Contains(body, "HomeNet") {
		t.Fatalf("body did not contain SSID: %s", body)
	}

	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-wifi", "list", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
}

func TestWifiStatus_NoStdin(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"ok","active_connection":null,"non_wifi_uplinks":[]}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/wifi/status", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}

	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	if len(calls[0].stdin) > 0 {
		t.Fatalf("status sent stdin body %q, want empty", calls[0].stdin)
	}
}

func TestWifiAdd_PipesBodyAndForwardsApplied(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"applied","id":"airplanes-wifi-homenet","uuid":"00000000-0000-4000-8000-000000000000","ssid":"HomeNet"}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r := postJSON(t, h.client, h.ts.URL+"/api/wifi",
		map[string]any{"ssid": "HomeNet", "psk": "hunter22hunter22", "test": false})
	defer r.Body.Close()

	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}

	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	wantArgv := []string{"sudo-stub", "apl-wifi", "add", "--json"}
	if !equalSlice(calls[0].argv, wantArgv) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, wantArgv)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %s; err=%v", calls[0].stdin, err)
	}
	if sent["ssid"] != "HomeNet" || sent["psk"] != "hunter22hunter22" {
		t.Fatalf("stdin body did not preserve fields: %v", sent)
	}
}

func TestWifiAdd_RejectedMapsTo400(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"rejected","errors":{"ssid":"too long"}}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r := postJSON(t, h.client, h.ts.URL+"/api/wifi",
		map[string]any{"ssid": strings.Repeat("a", 33), "psk": "hunter22"})
	defer r.Body.Close()

	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
}

func TestWifiAdd_TestFailedMapsTo409(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"test_failed","reason":"auth_failed","rolled_back":true}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r := postJSON(t, h.client, h.ts.URL+"/api/wifi",
		map[string]any{"ssid": "HomeNet", "psk": "wrongpsk", "test": true})
	defer r.Body.Close()

	if r.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
}

func TestWifiAdd_LockTimeoutMapsTo503(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"lock_timeout","message":"busy"}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r := postJSON(t, h.client, h.ts.URL+"/api/wifi",
		map[string]any{"ssid": "HomeNet", "psk": "hunter22"})
	defer r.Body.Close()

	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", r.StatusCode)
	}
}

func TestWifiUpdate_InjectsIDFromPath(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	envelope := `{"status":"applied","id":"airplanes-wifi-x","uuid":"deadbeef-dead-4dead-8dead-deaddeaddead","ssid":"X"}`
	h.stdinResult = wexec.Result{Stdout: []byte(envelope)}

	r := putJSON(t, h.client, h.ts.URL+"/api/wifi/airplanes-wifi-x",
		map[string]any{"ssid": "X-renamed"})
	defer r.Body.Close()

	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}

	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	if calls[0].argv[len(calls[0].argv)-2] != "update" {
		t.Fatalf("argv verb = %v, want update", calls[0].argv)
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["id"] != "airplanes-wifi-x" {
		t.Fatalf("update stdin did not carry id from path: %v", sent)
	}
	if sent["ssid"] != "X-renamed" {
		t.Fatalf("update stdin lost body field: %v", sent)
	}
}

func TestWifiUpdate_PathIDOverridesBody(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"applied","id":"airplanes-wifi-x"}`)}

	r := putJSON(t, h.client, h.ts.URL+"/api/wifi/airplanes-wifi-x",
		map[string]any{"id": "attacker-supplied", "ssid": "X"})
	defer r.Body.Close()

	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["id"] != "airplanes-wifi-x" {
		t.Fatalf("path id should override client body id; got %v", sent["id"])
	}
}

func TestWifiDelete_EmptyBodyAccepted(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"applied","id":"airplanes-wifi-x","deleted":true}`)}

	req := httpRequest(t, http.MethodDelete, h.ts.URL+"/api/wifi/airplanes-wifi-x", "")
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["id"] != "airplanes-wifi-x" {
		t.Fatalf("delete stdin missing id from path: %v", sent)
	}
}

func TestWifiActivate_InjectsID(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"applied","id":"airplanes-wifi-x","active":true}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/wifi/airplanes-wifi-x/activate",
		map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 || calls[0].argv[len(calls[0].argv)-2] != "activate" {
		t.Fatalf("argv = %v", calls)
	}
}

func TestWifiTest_PipesBody(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"status":"test_passed","ipv4":"10.0.0.2"}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/wifi/test",
		map[string]any{"ssid": "CafeNet", "psk": "hunter22"})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	calls := h.stdinCallsCopy()
	if calls[0].argv[len(calls[0].argv)-2] != "test" {
		t.Fatalf("argv verb = %v", calls[0].argv)
	}
}

func TestWifi_EmptyHelperStdoutMapsTo500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: nil, Stderr: []byte("apl-wifi: binary missing")}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/wifi", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestWifi_UnparseableHelperStdoutMapsTo500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte("not json")}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/wifi", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestWifi_InvalidPathIDRejectedBeforeHelper(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/wifi/airplanes-wifi-..%2Fetc%2Fpasswd", `{"ssid":"X"}`},
		{http.MethodPut, "/api/wifi/foreign-net", `{"ssid":"X"}`},
		{http.MethodPut, "/api/wifi/airplanes-wifi-", `{"ssid":"X"}`},
		{http.MethodPut, "/api/wifi/airplanes-config-wifi-extra", `{"ssid":"X"}`},
		{http.MethodDelete, "/api/wifi/airplanes-wifi-..", ""},
		{http.MethodPost, "/api/wifi/foreign/activate", ""},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req := httpRequest(t, tc.method, h.ts.URL+tc.path, tc.body)
			r, err := h.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if r.StatusCode != http.StatusBadRequest && r.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 400 or 404 (path mismatch); body=%s", r.StatusCode, mustReadAll(t, r.Body))
			}
		})
	}

	// The helper must never be invoked for any of these.
	calls := h.stdinCallsCopy()
	for _, c := range calls {
		t.Fatalf("helper unexpectedly invoked for invalid id: argv=%v", c.argv)
	}
}

func TestWifiAdd_RequiresOriginHeader(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)

	body, _ := json.Marshal(map[string]any{"ssid": "HomeNet", "psk": "hunter22"})
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/wifi", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Origin header → requireOriginMatchesHost rejects.
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", r.StatusCode)
	}
}

// --- shared test helpers --------------------------------------------------

func putJSON(t *testing.T, c *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
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

func mustReadAll(t *testing.T, r io.Reader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
