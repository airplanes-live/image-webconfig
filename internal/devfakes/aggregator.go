package devfakes

import (
	"encoding/json"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// aggProtocolVersion mirrors the helper's AGG_PROTOCOL_VERSION. The Go server
// rejects an envelope whose protocol_version differs, so the fake must stamp
// every reply with it.
const aggProtocolVersion = 1

// aggregatorCmd fakes `apl-aggregator <verb> --json` for cmd/devserver. It
// emits the same protocol_version:1 envelopes the production bash helper does
// so the SPA renders identically in dev and on a Pi. State is held in-memory
// on *State; the id for the per-adapter verbs arrives in the body (the server
// injects it from the URL path).
func aggregatorCmd(state *State, verb string, body []byte) (wexec.Result, error) {
	switch verb {
	case "status", "list":
		return aggStatus(state), nil
	case "detail":
		return aggDetail(state, body), nil
	case "enable":
		return aggEnable(state, body), nil
	case "disable":
		return aggMutateID(body, func(id string) { state.AggregatorDisable(id) }), nil
	case "reset":
		// reset is fire-and-forget like enable: production runs the teardown in a
		// detached worker (to escape the webconfig sandbox) and returns accepted.
		return aggMutateAccepted(body, func(id string) { state.AggregatorReset(id) }), nil
	case "set":
		return aggSet(state, body), nil
	case "export":
		return aggExport(state), nil
	case "import":
		return aggImport(state, body), nil
	}
	return aggError("usage_error", "unknown verb "+verb), nil
}

// aggKnown reports whether id is an adapter the fake (and a real device with
// the fr24 descriptor) recognises. The real helper returns not_found for an
// unregistered id before any state mutation; the fake mirrors that so dev
// behaviour matches production.
func aggKnown(id string) bool { return id == "fr24" || id == "piaware" }

func aggEnvelope(v any) wexec.Result {
	b, _ := json.Marshal(v)
	return wexec.Result{Stdout: append(b, '\n')}
}

func aggError(code, msg string) wexec.Result {
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"result":           "error",
		"error_code":       code,
		"message":          msg,
	})
}

func aggOK(id string, extra map[string]any) wexec.Result {
	m := map[string]any{"protocol_version": aggProtocolVersion, "result": "ok", "id": id}
	for k, v := range extra {
		m[k] = v
	}
	return aggEnvelope(m)
}

// aggAccepted is the fire-and-forget envelope production returns for the async
// mutations (enable/reset/set); the SPA polls status until the overlay settles.
func aggAccepted(id string) wexec.Result {
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"result":           "accepted",
		"step":             "starting",
		"id":               id,
		"request_id":       "dev-" + id,
	})
}

// aggMutateAccepted applies the in-memory mutation, then returns accepted.
func aggMutateAccepted(body []byte, fn func(id string)) wexec.Result {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return aggError("parse_error", "request body must be a JSON object")
	}
	if !aggKnown(req.ID) {
		return aggError("not_found", "unknown aggregator id")
	}
	fn(req.ID)
	return aggAccepted(req.ID)
}

// aggStatus emits the adapters the dev fake knows about (fr24 + piaware), each
// with its lifecycle state derived from the in-memory record. The descriptor
// fields mirror files/usr/local/lib/airplanes-webconfig/aggregators/*.desc.
func aggStatus(state *State) wexec.Result {
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"aggregators":      []any{aggAdapterFR24(state), aggAdapterPiaware(state)},
	})
}

// aggDetail fakes the `detail` verb: the one adapter named by the id injected
// into the body, wrapped in the same envelope as the list, PLUS the mock
// status_detail the production helper builds by running the vendor status tool.
// status_detail lives only on the detail verb (the list/dashboard poll stays
// light), so dev mirrors production.
func aggDetail(state *State, body []byte) wexec.Result {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return aggError("parse_error", "request body must be a JSON object")
	}
	var adapter map[string]any
	switch req.ID {
	case "fr24":
		adapter = aggAdapterFR24(state)
	case "piaware":
		adapter = aggAdapterPiaware(state)
	default:
		return aggError("not_found", "unknown aggregator id")
	}
	if sd := aggStatusDetail(req.ID, adapter); sd != nil {
		adapter["status_detail"] = sd
	}
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"aggregators":      []any{adapter},
	})
}

// aggStatusDetail returns the mock status_detail rows for a configured adapter,
// mirroring the production helper's whitelist (and the real values seen on a
// feeder). nil for a not-configured adapter — the helper attaches nothing there.
// enabled ≈ the service is active.
func aggStatusDetail(id string, adapter map[string]any) []map[string]any {
	if adapter["configured"] != true {
		return nil
	}
	if enabled, _ := adapter["enabled"].(bool); !enabled {
		return []map[string]any{{"label": "Service", "value": "Stopped", "severity": "na"}}
	}
	switch id {
	case "fr24":
		// Mirrors the monitor.json producer: receiver connection + aircraft count.
		return []map[string]any{
			{"label": "Receiver", "value": "Connected", "severity": "ok"},
			{"label": "Aircraft tracked", "value": "7", "severity": "ok"},
		}
	case "piaware":
		// Mirrors the status.json producer (per-component message); mlat off = na.
		mlatVal, mlatSev := "Multilateration is not enabled", "na"
		if m, _ := adapter["configured_mlat_enabled"].(bool); m {
			mlatVal, mlatSev = "Multilateration synchronized", "ok"
		}
		return []map[string]any{
			{"label": "FlightAware", "value": "Connected to FlightAware and logged in", "severity": "ok"},
			{"label": "ADS-B", "value": "Received Mode S data recently", "severity": "ok"},
			{"label": "MLAT", "value": mlatVal, "severity": mlatSev},
		}
	}
	return nil
}

// aggLifecycle maps the in-memory record to the status enum the SPA renders.
func aggLifecycle(installing, enabled, configured bool) string {
	switch {
	case installing:
		return "installing"
	case enabled:
		return "running"
	case configured:
		return "stopped"
	}
	return "not_installed"
}

func aggAdapterFR24(state *State) map[string]any {
	enabled, mlat, fields := state.AggregatorRecord("fr24")
	installing := state.AggregatorInstalling("fr24")
	external := state.AggregatorExternal("fr24")
	secretsPresent := []string{}
	if fields["sharing_key"] != "" {
		secretsPresent = append(secretsPresent, "sharing_key")
	}
	configured := enabled || len(secretsPresent) > 0
	fr24 := map[string]any{
		"id":                      "fr24",
		"display_name":            "Flightradar24",
		"acquire_method":          "vendor-installer",
		"service_unit":            "airplanes-aggregator@fr24.service",
		"state":                   aggLifecycle(installing, enabled, configured),
		"enabled":                 enabled,
		"configured":              configured,
		"external_install":        external,
		"managed_install":         configured,
		"available":               true,
		"mlat_capable":            false,
		"mlat_default":            false,
		"configured_mlat_enabled": mlat,
		"effective_mlat_enabled":  mlat,
		"config_drift":            false,
		"fields": []map[string]any{
			{"id": "email", "label": "Email address", "required": true, "secret": false},
			{"id": "sharing_key", "label": "Sharing key (optional)", "required": false, "secret": true},
		},
		"secret_fields_present": secretsPresent,
		"decoder_reachable":     true,
		"claim_url":             "https://www.flightradar24.com/account/data-sharing",
		"desired_version":       "1.0.53-0",
		"version_drift":         false,
	}
	if configured {
		fr24["version"] = "1.0.53-0"
	}
	if installing {
		fr24["enable"] = map[string]any{"status": "running", "step": "acquiring", "request_id": "dev-fr24"}
	}
	if re := state.AggregatorReconcileError("fr24"); re != nil {
		fr24["reconcile_error"] = re
	}
	if fr24["state"] == "running" {
		fr24["feed_health"] = "feeding"
	}
	return fr24
}

func aggAdapterPiaware(state *State) map[string]any {
	enabled, mlat, fields := state.AggregatorRecord("piaware")
	installing := state.AggregatorInstalling("piaware")
	external := state.AggregatorExternal("piaware")
	secretsPresent := []string{}
	if fields["feeder_id"] != "" {
		secretsPresent = append(secretsPresent, "feeder_id")
	}
	configured := enabled || len(secretsPresent) > 0
	pw := map[string]any{
		"id":                      "piaware",
		"display_name":            "FlightAware",
		"acquire_method":          "pinned-deb",
		"service_unit":            "piaware.service",
		"state":                   aggLifecycle(installing, enabled, configured),
		"enabled":                 enabled,
		"configured":              configured,
		"external_install":        external,
		"managed_install":         configured,
		"available":               true,
		"mlat_capable":            true,
		"mlat_default":            true,
		"configured_mlat_enabled": mlat,
		"effective_mlat_enabled":  mlat,
		"config_drift":            false,
		"fields": []map[string]any{
			{"id": "feeder_id", "label": "Feeder ID (optional — reclaim an existing FlightAware feeder)", "required": false, "secret": true},
		},
		"secret_fields_present": secretsPresent,
		"decoder_reachable":     true,
		"claim_url":             "https://www.flightaware.com/adsb/piaware/claim",
		"desired_version":       "11.0",
		"version_drift":         false,
	}
	if configured {
		pw["version"] = "11.0"
	}
	if installing {
		pw["enable"] = map[string]any{"status": "running", "step": "acquiring", "request_id": "dev-piaware"}
	}
	if re := state.AggregatorReconcileError("piaware"); re != nil {
		pw["reconcile_error"] = re
	}
	if pw["state"] == "running" {
		pw["feed_health"] = "feeding"
	}
	return pw
}

func aggEnable(state *State, body []byte) wexec.Result {
	var req struct {
		ID     string            `json:"id"`
		Fields map[string]string `json:"fields"`
		Mlat   *bool             `json:"mlat_enabled"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return aggError("parse_error", "request body must be a JSON object")
	}
	if req.ID == "" {
		return aggError("not_found", "unknown aggregator id")
	}
	mlat := false
	if req.Mlat != nil {
		mlat = *req.Mlat
	}
	switch req.ID {
	case "fr24":
		// handled below
	case "piaware":
		// FlightAware enable is one-click: no required field. An optional
		// feeder-id reclaims an existing feeder; otherwise the fake mints one
		// (production reads it from /var/cache/piaware/feeder_id after start).
		_, _, stored := state.AggregatorRecord("piaware")
		feederID := req.Fields["feeder_id"]
		if feederID == "" {
			feederID = stored["feeder_id"]
		}
		if feederID == "" {
			feederID = "00000000-0000-4000-8000-00000000dev0"
		}
		if req.Mlat == nil {
			mlat = true // descriptor default
		}
		state.AggregatorEnable("piaware", map[string]string{"feeder_id": feederID}, mlat)
		return aggEnvelope(map[string]any{
			"protocol_version": aggProtocolVersion,
			"result":           "accepted",
			"step":             "starting",
			"id":               "piaware",
			"request_id":       "dev-piaware",
		})
	default:
		return aggError("rejected", "enable is not yet supported for "+req.ID)
	}
	_, _, stored := state.AggregatorRecord(req.ID)
	email := req.Fields["email"]
	if email == "" {
		email = stored["email"]
	}
	if email == "" {
		return aggError("rejected", "a valid email address is required")
	}
	key := req.Fields["sharing_key"]
	if key == "" {
		key = stored["sharing_key"]
	}
	if key == "" {
		// Dev: simulate the signup wizard minting a fresh sharing key.
		key = "DEVFR24KEY01"
	}
	state.AggregatorEnable(req.ID, map[string]string{"email": email, "sharing_key": key}, mlat)
	// Fire-and-forget, like production: return accepted; the SPA polls status
	// and watches the adapter flip installing → running.
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"result":           "accepted",
		"step":             "starting",
		"id":               req.ID,
		"request_id":       "dev-" + req.ID,
	})
}

func aggMutateID(body []byte, fn func(id string)) wexec.Result {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return aggError("parse_error", "request body must be a JSON object")
	}
	if !aggKnown(req.ID) {
		return aggError("not_found", "unknown aggregator id")
	}
	fn(req.ID)
	return aggOK(req.ID, nil)
}

func aggSet(state *State, body []byte) wexec.Result {
	var req struct {
		ID     string            `json:"id"`
		Mlat   *bool             `json:"mlat_enabled"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return aggError("parse_error", "request body must be a JSON object")
	}
	if !aggKnown(req.ID) {
		return aggError("not_found", "unknown aggregator id")
	}
	state.AggregatorSet(req.ID, req.Fields, req.Mlat)
	return aggAccepted(req.ID)
}

func aggExport(state *State) wexec.Result {
	aggs := map[string]any{}
	for id, rec := range state.AggregatorExport() {
		aggs[id] = map[string]any{"mlat_enabled": rec.Mlat, "fields": rec.Fields}
	}
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"result":           "ok",
		"kind":             "aggregator-backup",
		"schema_version":   1,
		"aggregators":      aggs,
	})
}

func aggImport(state *State, body []byte) wexec.Result {
	var blob struct {
		Kind        string `json:"kind"`
		Aggregators map[string]struct {
			Mlat   bool              `json:"mlat_enabled"`
			Fields map[string]string `json:"fields"`
		} `json:"aggregators"`
	}
	if err := json.Unmarshal(body, &blob); err != nil || blob.Kind != "aggregator-backup" {
		return aggError("parse_error", "not a valid aggregator backup")
	}
	if len(blob.Aggregators) == 0 {
		return aggError("rejected", "backup contains no aggregators")
	}
	// Validate the whole blob before writing anything — all-or-nothing, like
	// the helper. A mixed {fr24, unknown} or {fr24-enabled} blob must leave
	// device state untouched rather than partially import (map iteration order
	// would otherwise make the outcome nondeterministic).
	for id := range blob.Aggregators {
		if !aggKnown(id) {
			return aggError("rejected", "backup references unknown adapter '"+id+"'")
		}
		if enabled, _, _ := state.AggregatorRecord(id); enabled {
			return aggError("rejected", id+" is enabled; disable it before importing")
		}
	}
	n := 0
	for id, a := range blob.Aggregators {
		state.AggregatorImport(id, a.Fields, a.Mlat)
		n++
	}
	return aggEnvelope(map[string]any{
		"protocol_version": aggProtocolVersion,
		"result":           "ok",
		"imported":         n,
	})
}
