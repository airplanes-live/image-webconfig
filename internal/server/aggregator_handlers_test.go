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

// okStatus is a minimal valid apl-aggregator status envelope.
const aggStatusEnvelope = `{"protocol_version":1,"aggregators":[{"id":"fr24","display_name":"Flightradar24","state":"not_installed","enabled":false}]}`

func TestAggregatorList_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r, err := c.Do(httpRequest(t, http.MethodGet, ts.URL+"/api/aggregators", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestAggregatorList_ForwardsEnvelope(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(aggStatusEnvelope)}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if !strings.Contains(mustReadAll(t, r.Body), "fr24") {
		t.Fatal("body did not contain adapter id")
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-aggregator", "status", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	if len(calls[0].stdin) != 0 {
		t.Fatalf("status sent stdin %q, want empty", calls[0].stdin)
	}
}

func TestAggregatorDetail_ForwardsAndInjectsID(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"aggregators":[{"id":"fr24","display_name":"Flightradar24","state":"running","enabled":true,"status_detail":[{"label":"Connection","value":"connected","severity":"ok"}]}]}`)}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators/fr24", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if body := mustReadAll(t, r.Body); !strings.Contains(body, "status_detail") {
		t.Fatalf("detail body lacked status_detail: %s", body)
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-aggregator", "detail", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %s", calls[0].stdin)
	}
	if sent["id"] != "fr24" {
		t.Fatalf("detail stdin did not carry id from path: %v", sent)
	}
}

func TestAggregatorDetail_InvalidIDRejectedBeforeHelper(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators/BadID", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatalf("helper was invoked for an invalid id: %v", calls)
	}
}

func TestAggregatorDetail_UnknownAdapterMapsTo404(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"error","error_code":"not_found","message":"unknown aggregator id"}`)}
	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators/ghost", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
}

func TestAggregatorEnable_PipesBodyAndInjectsID(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// enable is fire-and-forget: the helper validates + launches a worker and
	// returns result:"accepted" → 202.
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"accepted","step":"starting","id":"fr24","request_id":"r1"}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/enable",
		map[string]any{"lat": 51.5, "lon": -0.1, "alt": 30, "fields": map[string]string{"email": "a@b.c"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body = %s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	want := []string{"sudo-stub", "apl-aggregator", "enable", "--json"}
	if !equalSlice(calls[0].argv, want) {
		t.Fatalf("argv = %v, want %v", calls[0].argv, want)
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %s; err=%v", calls[0].stdin, err)
	}
	if sent["id"] != "fr24" {
		t.Fatalf("enable stdin did not carry id from path: %v", sent)
	}
	fields, _ := sent["fields"].(map[string]any)
	if fields["email"] != "a@b.c" {
		t.Fatalf("enable stdin lost fields: %v", sent)
	}
}

func TestAggregatorEnable_PathIDOverridesBody(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"accepted","step":"starting","id":"fr24","request_id":"r1"}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/enable",
		map[string]any{"id": "attacker", "fields": map[string]string{"email": "a@b.c"}})
	defer r.Body.Close()
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["id"] != "fr24" {
		t.Fatalf("path id should override client body id; got %v", sent["id"])
	}
}

func TestAggregatorEnable_ErrorCodeMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code string
		want int
	}{
		{"rejected", http.StatusBadRequest},
		{"not_found", http.StatusNotFound},
		{"decoder_unavailable", http.StatusServiceUnavailable},
		{"lock_timeout", http.StatusServiceUnavailable},
		{"acquire_failed", http.StatusBadGateway},
		{"signup_failed", http.StatusBadGateway},
		{"state_error", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			h := newWriteHarness(t)
			h.stdinResult = wexec.Result{Stdout: []byte(
				`{"protocol_version":1,"result":"error","error_code":"` + tc.code + `","message":"x"}`)}
			r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/enable",
				map[string]any{"fields": map[string]string{"email": "a@b.c"}})
			defer r.Body.Close()
			if r.StatusCode != tc.want {
				t.Fatalf("error_code %q → status %d, want %d", tc.code, r.StatusCode, tc.want)
			}
		})
	}
}

func TestAggregatorDisable_InjectsIDNoBody(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"ok","id":"fr24"}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/disable", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 || calls[0].argv[len(calls[0].argv)-2] != "disable" {
		t.Fatalf("argv = %v", calls)
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["id"] != "fr24" {
		t.Fatalf("disable stdin missing id from path: %v", sent)
	}
}

func TestAggregatorSet_PipesBodyAndInjectsID(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"ok","id":"fr24"}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/set",
		map[string]any{"mlat_enabled": true, "fields": map[string]string{"sharing_key": "ABC123def"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 || calls[0].argv[len(calls[0].argv)-2] != "set" {
		t.Fatalf("argv = %v", calls)
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["id"] != "fr24" || sent["mlat_enabled"] != true {
		t.Fatalf("set stdin not preserved: %v", sent)
	}
}

func TestAggregatorReset_InjectsID(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"ok","id":"fr24"}`)}

	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/reset", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 || calls[0].argv[len(calls[0].argv)-2] != "reset" {
		t.Fatalf("argv = %v", calls)
	}
}

func TestAggregator_EmptyHelperStdoutMapsTo500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	h.stdinResult = wexec.Result{Stdout: nil, Stderr: []byte("apl-aggregator: binary missing")}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestAggregator_ProtocolMismatchMapsTo500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// A helper that speaks a newer protocol than this binary understands.
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":2,"aggregators":[]}`)}

	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (protocol mismatch)", r.StatusCode)
	}
}

func TestAggregator_InvalidPathIDRejectedBeforeHelper(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	for _, id := range []string{"FR24", "-bad", "fr.24", "fr%20"} {
		t.Run(id, func(t *testing.T) {
			r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/"+id+"/enable", map[string]any{})
			defer r.Body.Close()
			if r.StatusCode != http.StatusBadRequest && r.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want 400/404; body=%s", r.StatusCode, mustReadAll(t, r.Body))
			}
		})
	}
	if calls := h.stdinCallsCopy(); len(calls) != 0 {
		t.Fatalf("helper invoked for invalid id: %v", calls)
	}
}

func TestAggregatorEnable_NullBodyRejected(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// A literal JSON `null` body must be rejected (not panic the injectID
	// path) before the helper is invoked.
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/aggregators/fr24/enable", strings.NewReader("null"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.ts.URL)
	r, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	if len(h.stdinCallsCopy()) != 0 {
		t.Fatal("helper invoked for null body")
	}
}

func TestAggregator_UnknownResultMapsTo500(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// A helper envelope with neither result:"ok"/"error" nor an aggregators
	// array is a contract violation, not a silent 200.
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"surprise"}`)}
	r, err := h.client.Do(httpRequest(t, http.MethodGet, h.ts.URL+"/api/aggregators", ""))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestAggregatorEnable_ClientGeoWinsOverFeedEnv(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// feed.env has a location, but a client that supplies its own coordinates
	// wins — the user can send an explicit (e.g. different) location to
	// Flightradar24. The helper still range-validates what it receives.
	h.setFeedEnv(map[string]string{"LATITUDE": "51.5", "LONGITUDE": "-0.12", "ALTITUDE": "30"})
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"accepted","step":"starting","id":"fr24","request_id":"r1"}`)}
	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/enable",
		map[string]any{"lat": 1.0, "lon": 2.0, "alt": 9999, "fields": map[string]string{"email": "a@b.c"}})
	defer r.Body.Close()
	if r.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", r.StatusCode, mustReadAll(t, r.Body))
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	if err := json.Unmarshal(calls[0].stdin, &sent); err != nil {
		t.Fatalf("stdin not JSON: %s", calls[0].stdin)
	}
	if sent["lat"] != float64(1) || sent["lon"] != float64(2) || sent["alt"] != float64(9999) {
		t.Fatalf("client geo must win over feed.env: %v", sent)
	}
	if fields, _ := sent["fields"].(map[string]any); fields["email"] != "a@b.c" {
		t.Fatalf("fields lost during geo handling: %v", sent)
	}
}

func TestAggregatorEnable_FillsGeoFromFeedEnvWhenClientOmits(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// A client that omits geo (e.g. piaware, or any caller without coordinates)
	// gets the feeder location filled in from feed.env.
	h.setFeedEnv(map[string]string{"LATITUDE": "51.5", "LONGITUDE": "-0.12", "ALTITUDE": "30"})
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"accepted","step":"starting","id":"fr24","request_id":"r1"}`)}
	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/enable",
		map[string]any{"fields": map[string]string{"email": "a@b.c"}})
	defer r.Body.Close()
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["lat"] != 51.5 || sent["lon"] != -0.12 || sent["alt"] != float64(30) {
		t.Fatalf("geo not filled from feed.env when client omits it: %v", sent)
	}
}

func TestAggregatorEnable_KeepsClientGeoWhenLocationUnset(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	// feed.env has no location yet, but the client supplies coordinates — they're
	// forwarded so a user can set up Flightradar24 before configuring the feeder
	// location.
	h.setFeedEnv(map[string]string{"UAT_INPUT": ""})
	h.stdinResult = wexec.Result{Stdout: []byte(`{"protocol_version":1,"result":"accepted","id":"fr24","request_id":"r1"}`)}
	r := postJSON(t, h.client, h.ts.URL+"/api/aggregators/fr24/enable",
		map[string]any{"lat": 1.0, "lon": 2.0, "alt": 9999, "fields": map[string]string{"email": "a@b.c"}})
	defer r.Body.Close()
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d", len(calls))
	}
	var sent map[string]any
	_ = json.Unmarshal(calls[0].stdin, &sent)
	if sent["lat"] != float64(1) || sent["lon"] != float64(2) || sent["alt"] != float64(9999) {
		t.Fatalf("client geo must be forwarded when feed.env has none: %v", sent)
	}
}

func TestAggregatorEnable_RequiresOriginHeader(t *testing.T) {
	t.Parallel()
	h := newWriteHarness(t)
	body, _ := json.Marshal(map[string]any{"fields": map[string]string{"email": "a@b.c"}})
	req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/api/aggregators/fr24/enable", bytes.NewReader(body))
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

// TestDefaultPrivilegedArgv_Aggregator pins the production argv shapes so the
// Go default and the sudoers file under files/etc/sudoers.d/ cannot drift.
// (Set parity is also enforced by TestValidatePrivilegedArgvParity_*; this
// catches a per-verb typo at unit-test time.)
func TestDefaultPrivilegedArgv_Aggregator(t *testing.T) {
	t.Parallel()
	priv := DefaultPrivilegedArgv()
	want := map[string][]string{
		"status":  {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "status", "--json"},
		"enable":  {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "enable", "--json"},
		"disable": {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "disable", "--json"},
		"set":     {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "set", "--json"},
		"reset":   {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "reset", "--json"},
		"export":  {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "export", "--json"},
		"import":  {"/usr/bin/sudo", "-n", "/usr/local/bin/apl-aggregator", "import", "--json"},
	}
	got := map[string][]string{
		"status":  priv.AggregatorStatus,
		"enable":  priv.AggregatorEnable,
		"disable": priv.AggregatorDisable,
		"set":     priv.AggregatorSet,
		"reset":   priv.AggregatorReset,
		"export":  priv.AggregatorExport,
		"import":  priv.AggregatorImport,
	}
	for verb, w := range want {
		if !reflect.DeepEqual(got[verb], w) {
			t.Errorf("Aggregator %s argv = %v, want %v", verb, got[verb], w)
		}
	}
}
