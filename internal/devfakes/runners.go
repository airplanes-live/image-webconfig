package devfakes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strconv"
	"strings"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedmeta"
	"github.com/airplanes-live/image-webconfig/internal/server"
)

// Devfake server-parity validators. Mirror feed/scripts/lib/configure-validators.sh
// rules so manual testing through cmd/devserver matches what production
// apl-feed accepts. Drift between these regexes and feed's bash twins
// surfaces as "client thinks input is fine but real Pi 400s" in the
// field — keep them aligned.
var (
	mlatUserRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	gainNumericRE    = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
	dump978SerialRE  = regexp.MustCompile(`^[0-9A-Za-z_-]{1,32}$`)
)

// validateGainValue mirrors valid_gain: auto/min/max OR numeric in [0, 60].
// Returns "" on success, the user-facing error message on failure.
func validateGainValue(v string) string {
	if v == "auto" || v == "min" || v == "max" {
		return ""
	}
	if !gainNumericRE.MatchString(v) {
		return "must be in [0, 60] or one of auto/min/max"
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || n > 60 {
		return "must be in [0, 60] or one of auto/min/max"
	}
	return ""
}

// validateDump978GainValue mirrors valid_dump978_gain: numeric in [0, 60].
// dump978-fa rejects auto/min/max so we do too. Returns "" on success.
func validateDump978GainValue(v string) string {
	if !gainNumericRE.MatchString(v) {
		return "must be a number in [0, 60]"
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil || n < 0 || n > 60 {
		return "must be a number in [0, 60]"
	}
	return ""
}

// StubPrivilegedArgv returns a PrivilegedArgv that the fake runners
// can dispatch on without colliding with anything a developer might
// have on $PATH. The leading "dev-stub" sentinel makes it obvious in
// stderr logs which call hit the dev path, and the second token is
// the verb the fake dispatches on. Production sudoers parity does
// NOT apply to this argv set; the parity test runs against
// DefaultPrivilegedArgv().
func StubPrivilegedArgv() server.PrivilegedArgv {
	return server.PrivilegedArgv{
		ApplyFeed:            []string{"dev-stub", "apl-feed", "apply"},
		SchemaFeed:           []string{"dev-stub", "apl-feed", "schema"},
		Reboot:               []string{"dev-stub", "systemctl", "reboot"},
		Poweroff:             []string{"dev-stub", "systemctl", "poweroff"},
		StartOrchestrator:    []string{"dev-stub", "systemd-run", "airplanes-update-orchestrator"},
		RegisterClaim:        []string{"dev-stub", "systemctl", "claim-register"},
		SyncConfig:           []string{"dev-stub", "systemctl", "config-sync"},
		WifiList:             []string{"dev-stub", "apl-wifi", "list"},
		WifiAdd:              []string{"dev-stub", "apl-wifi", "add"},
		WifiUpdate:           []string{"dev-stub", "apl-wifi", "update"},
		WifiDelete:           []string{"dev-stub", "apl-wifi", "delete"},
		WifiTest:             []string{"dev-stub", "apl-wifi", "test"},
		WifiActivate:         []string{"dev-stub", "apl-wifi", "activate"},
		WifiAdopt:            []string{"dev-stub", "apl-wifi", "adopt"},
		WifiStatus:           []string{"dev-stub", "apl-wifi", "status"},
		ExportIdentity:       []string{"dev-stub", "identity", "export"},
		ImportIdentity:       []string{"dev-stub", "identity", "import"},
	}
}

// Runner returns a fake wexec.CommandRunner that dispatches the
// non-stdin subprocess calls the webconfig handlers issue. Concretely:
//
//   - /usr/bin/systemctl is-active <unit>... — used by status.Reader
//     (single-unit, per-service) and by handlers.maintenanceUnitActive
//     (multi-unit, single call). The fake returns one state line per
//     trailing argv element drawn from State.ServiceState.
//   - /usr/bin/systemctl show --property=ExecMainStatus --value <unit>
//     — used by status.Reader.execMainStatus when the unit is failed.
//     The fake returns "0" so the dashboard never renders an exit-status
//     warning over the simulated feed.
//   - dev-stub systemctl {reboot,poweroff} — log the intent and exit 0.
//   - dev-stub systemd-run airplanes-update-orchestrator
//     — log the intent and exit 0. The HTTP handler writes 202.
//   - dev-stub systemctl claim-register — calls state.RegisterClaim()
//     so the next GET /api/identity reports claim_secret_present=true.
//
// argv[0] is always either the real systemctl binary path or the
// stub sentinel "dev-stub". Other shapes fall through to a log line
// and a zero Result; they should not happen in the wired-up devserver.
func Runner(state *State, priv server.PrivilegedArgv) wexec.CommandRunner {
	return func(ctx context.Context, argv []string) (wexec.Result, error) {
		if len(argv) == 0 {
			return wexec.Result{}, errors.New("devfakes: empty argv")
		}
		// Real systemctl shapes from status.Reader.
		if strings.HasSuffix(argv[0], "/systemctl") || argv[0] == "systemctl" {
			return dispatchSystemctl(state, argv)
		}
		// Stub argv from PrivilegedArgv. "dev-stub" sentinel keeps the
		// fake out of the way of any real binary on PATH.
		if argv[0] == "dev-stub" {
			return dispatchStub(state, priv, argv, nil)
		}
		log.Printf("devfakes.Runner: unhandled argv %v", argv)
		return wexec.Result{}, nil
	}
}

// StdinRunner returns the piped variant. apl-feed apply and every
// apl-wifi subcommand flow through here in production
// (internal/server/wifi_handlers.go:invokeWifi uses s.stdinRunner for
// every wifi subcommand, including read-only list/status). The fake
// reads the payload, mutates State, and returns the wire envelope the
// SPA expects.
func StdinRunner(state *State, priv server.PrivilegedArgv) wexec.CommandRunnerStdin {
	return func(ctx context.Context, argv []string, stdin io.Reader) (wexec.Result, error) {
		body, _ := io.ReadAll(stdin)
		if len(argv) == 0 {
			return wexec.Result{}, errors.New("devfakes: empty argv")
		}
		if argv[0] == "dev-stub" {
			return dispatchStub(state, priv, argv, body)
		}
		log.Printf("devfakes.StdinRunner: unhandled argv %v", argv)
		return wexec.Result{}, nil
	}
}

// dispatchSystemctl handles `/usr/bin/systemctl <verb> <args...>` from
// status.Reader. is-active is the only verb that comes through with
// units to look up; show emits a numeric value that status.Reader
// trims for ExecMainStatus.
func dispatchSystemctl(state *State, argv []string) (wexec.Result, error) {
	if len(argv) < 2 {
		return wexec.Result{}, nil
	}
	switch argv[1] {
	case "is-active":
		// Refresh the aircraft snapshot on every is-active fan-out: the
		// /api/status handler reads aircraft.json right after the systemctl
		// loop, so this keeps the feed tile's counters and aircraft count
		// drifting with wall-clock time. Errors are non-fatal — a stale
		// snapshot is recoverable, a refused is-active call is not.
		if err := state.RefreshAircraftJSON(); err != nil {
			log.Printf("devfakes: refresh aircraft.json: %v", err)
		}
		var b strings.Builder
		for _, unit := range argv[2:] {
			b.WriteString(state.ServiceState(unit))
			b.WriteByte('\n')
		}
		return wexec.Result{Stdout: []byte(b.String())}, nil
	case "show":
		// Only ExecMainStatus is asked for in this codepath.
		return wexec.Result{Stdout: []byte("0\n")}, nil
	}
	log.Printf("devfakes: unhandled systemctl verb %v", argv)
	return wexec.Result{}, nil
}

// dispatchStub handles every argv whose first token is "dev-stub".
// The second token is the binary identifier; the third is the
// subcommand. body is the stdin bytes for the apply/wifi paths; nil
// for the non-stdin variants.
func dispatchStub(state *State, priv server.PrivilegedArgv, argv []string, body []byte) (wexec.Result, error) {
	if len(argv) < 3 {
		log.Printf("devfakes: malformed stub argv %v", argv)
		return wexec.Result{}, nil
	}
	switch argv[1] {
	case "apl-feed":
		switch argv[2] {
		case "apply":
			return applyFeed(state, body)
		case "schema":
			return schemaFeed(state)
		}
	case "identity":
		switch argv[2] {
		case "export":
			return identityExport(state)
		case "import":
			return identityImport(state, body)
		}
	case "apl-wifi":
		return wifiCmd(state, argv[2], body)
	case "systemctl":
		switch argv[2] {
		case "reboot":
			log.Printf("devfakes: would reboot")
		case "poweroff":
			log.Printf("devfakes: would poweroff")
		case "claim-register":
			log.Printf("devfakes: would register claim secret")
			if err := state.RegisterClaim(); err != nil {
				log.Printf("devfakes: RegisterClaim: %v", err)
			}
		case "config-sync":
			log.Printf("devfakes: would nudge config-sync")
		}
		return wexec.Result{}, nil
	case "systemd-run":
		unit := argv[2] + ".service"
		log.Printf("devfakes: would systemd-run %s", unit)
		// Pin the maintenance unit `activating` for a short window so a
		// double-click on System upgrade / Update System exercises
		// handlers.maintenanceUnitActive's 409 guard the same way it would
		// on a real Pi. Production systemd flips a transient unit through
		// activating → active → inactive on its own; we approximate with a
		// fixed 5 s window which is comfortably longer than the SPA's
		// confirm-modal round-trip.
		state.SetServiceState(unit, "activating")
		time.AfterFunc(5*time.Second, func() {
			state.SetServiceState(unit, "inactive")
		})
		return wexec.Result{}, nil
	}
	log.Printf("devfakes: unhandled stub argv %v", argv)
	return wexec.Result{}, nil
}

// applyFeedPayload mirrors the wire shape `handleConfigPost` builds:
//
//	{"updates": {"KEY": <bare string OR {"value": "...", "edited_at":...,
//	                                     "edited_by":"..."}>}}
//
// We accept either form because the production server emits both
// (tracked + changed → object, everything else → string). The fake
// extracts string values and feeds them into State.ApplyFeedEnv.
type applyFeedPayload struct {
	Updates map[string]json.RawMessage `json:"updates"`
}

func applyFeed(state *State, body []byte) (wexec.Result, error) {
	var p applyFeedPayload
	if err := json.Unmarshal(body, &p); err != nil {
		env := map[string]any{
			"status":  "parse_error",
			"message": fmt.Sprintf("payload not valid JSON: %v", err),
		}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	updates := make(map[string]string, len(p.Updates))
	for k, raw := range p.Updates {
		s, ok := extractApplyValue(raw)
		if !ok {
			env := map[string]any{
				"status":  "parse_error",
				"message": fmt.Sprintf("key %q has unsupported value shape", k),
			}
			b, _ := json.Marshal(env)
			return wexec.Result{Stdout: b}, nil
		}
		updates[k] = s
	}
	// Mirror feed's apply-layer validator: invalid input gets rejected
	// before any disk mutation. Production apl-feed accumulates per-key
	// errors and returns a `rejected` envelope; we do the same so the
	// SPA's rejected-envelope branch sees the same shape in dev as in
	// production. Each key here mirrors the rule from
	// feed/scripts/lib/configure-validators.sh — keep the messages
	// aligned with feed/scripts/lib/feed-env-apply.sh's wording.
	rejectErrors := map[string]string{}
	if alt, present := updates["ALTITUDE"]; present {
		if _, ok := feedmeta.AltitudeToBareMetres(alt); !ok {
			rejectErrors["ALTITUDE"] = "must parse as a metric or imperial altitude in [-1000, 10000] metres (e.g. 120m, 400ft, 0)"
		}
	}
	if u, present := updates["MLAT_USER"]; present && u != "" {
		if !mlatUserRE.MatchString(u) {
			rejectErrors["MLAT_USER"] = "must match [A-Za-z0-9_-]{1,64} or be empty"
		}
	}
	if g, present := updates["GAIN"]; present {
		if msg := validateGainValue(g); msg != "" {
			rejectErrors["GAIN"] = msg
		}
	}
	if s, present := updates["DUMP978_SDR_SERIAL"]; present && s != "" {
		if !dump978SerialRE.MatchString(s) {
			rejectErrors["DUMP978_SDR_SERIAL"] = "must match [0-9A-Za-z_-]{1,32} or be empty"
		}
	}
	if g, present := updates["DUMP978_GAIN"]; present {
		if msg := validateDump978GainValue(g); msg != "" {
			rejectErrors["DUMP978_GAIN"] = msg
		}
	}
	if len(rejectErrors) > 0 {
		env := map[string]any{
			"status": "rejected",
			"errors": rejectErrors,
		}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	changed, err := state.ApplyFeedEnv(updates)
	if err != nil {
		env := map[string]any{
			"status":  "filesystem_error",
			"message": err.Error(),
		}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	env := map[string]any{
		"status":          "applied",
		"changed":         changed,
		"pending_restart": []string{},
	}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

// extractApplyValue unwraps both legs of feedmeta's heterogeneous
// payload: a bare JSON string OR an object with a "value" field.
func extractApplyValue(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		return s, true
	}
	if raw[0] == '{' {
		var obj struct {
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &obj); err != nil {
			return "", false
		}
		return obj.Value, true
	}
	// Allow bare numbers / bools so the SPA's coercion-shy fields
	// (priority etc.) don't blow up. Json-encoded numbers come back
	// as their text rep, which matches what feed.env expects.
	return strings.Trim(string(raw), `"`), true
}

// identityExport mirrors `apl-feed backup -`: emit the canonical backup
// envelope on stdout. The handler relays this verbatim to the SPA.
func identityExport(state *State) (wexec.Result, error) {
	uuid, secret, version := state.Identity()
	if secret == "" {
		// Match apl-feed backup's failure shape — the binary exits
		// non-zero with stderr when no claim secret is registered. The
		// handler maps any non-nil error to a 500.
		return wexec.Result{
			Stderr:   []byte("no claim secret registered\n"),
			ExitCode: 1,
		}, errors.New("no claim secret registered")
	}
	envelope := map[string]any{
		"schema_version": 1,
		"created_at":     time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"feeder_uuid":    uuid,
		"claim": map[string]any{
			"secret":  secret,
			"version": version, // nil → JSON null
		},
	}
	b, err := json.Marshal(envelope)
	if err != nil {
		return wexec.Result{ExitCode: 1, Stderr: []byte(err.Error())}, err
	}
	return wexec.Result{Stdout: append(b, '\n')}, nil
}

// identityImport mirrors `apl-feed restore /dev/stdin --force`: parse
// the canonical backup envelope from stdin and overwrite the on-disk
// identity files via the State helper.
func identityImport(state *State, body []byte) (wexec.Result, error) {
	var env struct {
		SchemaVersion int    `json:"schema_version"`
		FeederUUID    string `json:"feeder_uuid"`
		Claim         struct {
			Secret  string `json:"secret"`
			Version *int   `json:"version"`
		} `json:"claim"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return wexec.Result{ExitCode: 1, Stderr: []byte(err.Error())}, err
	}
	if env.SchemaVersion != 1 || env.FeederUUID == "" || env.Claim.Secret == "" {
		return wexec.Result{
			ExitCode: 1,
			Stderr:   []byte("malformed backup envelope (post-validation)\n"),
		}, errors.New("malformed backup envelope")
	}
	if err := state.ImportIdentity(env.FeederUUID, env.Claim.Secret, env.Claim.Version); err != nil {
		return wexec.Result{ExitCode: 1, Stderr: []byte(err.Error())}, err
	}
	return wexec.Result{Stdout: []byte("Restored feeder config for Feeder ID " + env.FeederUUID + "\n")}, nil
}

func schemaFeed(state *State) (wexec.Result, error) {
	// Not called by the running server (schemacache.NewPrepopulated
	// skips this), but emit a valid envelope for symmetry. Mirrors
	// internal/configspec/configspec.go AllReadKeys + the writable set
	// the schemacache tests use.
	writable := []string{
		"LATITUDE", "LONGITUDE", "ALTITUDE", "GEO_CONFIGURED",
		"MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE",
		"REPORT_STATUS", "REMOTE_CONFIG_ENABLED",
		"GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN",
	}
	readable := append(append([]string{}, writable...), "INPUT", "INPUT_TYPE")
	env := map[string]any{
		"version":       1,
		"writable_keys": writable,
		"readable_keys": readable,
	}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

// --- apl-wifi -------------------------------------------------------------

func wifiCmd(state *State, sub string, body []byte) (wexec.Result, error) {
	switch sub {
	case "list":
		return wifiList(state)
	case "status":
		return wifiStatus(state)
	case "add":
		return wifiAdd(state, body)
	case "update":
		return wifiUpdate(state, body)
	case "delete":
		return wifiDelete(state, body)
	case "test":
		return wifiTest(state, body)
	case "activate":
		return wifiActivate(state, body)
	case "adopt":
		return wifiAdopt(state, body)
	}
	env := map[string]any{"status": "usage_error", "message": "unknown subcommand " + sub}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiList(state *State) (wexec.Result, error) {
	nets, active := state.WifiList()
	env := map[string]any{
		"status":                   "ok",
		"active_connection":        active,
		"non_wifi_uplinks":         []any{},
		"networks":                 nets,
		"networkmanager_available": true,
	}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiStatus(state *State) (wexec.Result, error) {
	active, dev := state.WifiStatus()
	env := map[string]any{
		"status":                   "ok",
		"active_connection":        active,
		"wifi_device":              dev,
		"non_wifi_uplinks":         []any{},
		"networkmanager_available": true,
	}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

// wifiInput captures the subset of the apl-wifi {add,update,test,delete,activate}
// body fields the fake needs. The helper accepts more, but the SPA only
// posts these. `Test` mirrors the apl-wifi `test` flag — when true the
// helper runs the new keyfile through nmcli's connect-before-save flow
// and the saved profile ends up active; when false the keyfile is
// written but not activated.
type wifiInput struct {
	ID       string `json:"id"`
	SSID     string `json:"ssid"`
	PSK      string `json:"psk"`
	Country  string `json:"country"`
	Hidden   bool   `json:"hidden"`
	Priority int    `json:"priority"`
	Test     bool   `json:"test"`
}

func parseWifiInput(body []byte) (wifiInput, error) {
	var in wifiInput
	if len(body) == 0 {
		return in, nil
	}
	if err := json.Unmarshal(body, &in); err != nil {
		return in, err
	}
	return in, nil
}

func wifiAdd(state *State, body []byte) (wexec.Result, error) {
	in, err := parseWifiInput(body)
	if err != nil {
		env := map[string]any{"status": "parse_error", "message": err.Error()}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	if in.SSID == "" {
		env := map[string]any{"status": "rejected", "reason": "ssid_required"}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	id, _ := state.WifiAdd(in.SSID, in.PSK, in.Country, in.Hidden, in.Priority)
	// Production apl-wifi add returns {status, id, uuid, ssid, active, changed}.
	// When `test:true` (the SPA's default), nmcli connects before save and
	// the new profile ends up active — simulate that by flipping the active
	// flag in State.
	if in.Test {
		state.WifiActivate(id)
	}
	uuid := ""
	for _, n := range state.networksSnapshot() {
		if n.ID == id {
			uuid = n.UUID
			break
		}
	}
	env := map[string]any{
		"status":  "applied",
		"id":      id,
		"uuid":    uuid,
		"ssid":    in.SSID,
		"active":  in.Test,
		"changed": []string{id},
	}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiUpdate(state *State, body []byte) (wexec.Result, error) {
	in, err := parseWifiInput(body)
	if err != nil {
		env := map[string]any{"status": "parse_error", "message": err.Error()}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	if !state.WifiUpdate(in.ID, in.SSID, in.PSK, in.Country, in.Hidden, in.Priority) {
		env := map[string]any{"status": "rejected", "reason": "unknown_id", "id": in.ID}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	env := map[string]any{"status": "applied", "id": in.ID}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiDelete(state *State, body []byte) (wexec.Result, error) {
	in, _ := parseWifiInput(body)
	if !state.WifiDelete(in.ID) {
		env := map[string]any{"status": "rejected", "reason": "unknown_id", "id": in.ID}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	env := map[string]any{"status": "applied", "id": in.ID}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiTest(state *State, body []byte) (wexec.Result, error) {
	in, err := parseWifiInput(body)
	if err != nil {
		env := map[string]any{"status": "parse_error", "message": err.Error()}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	if in.SSID == "" {
		env := map[string]any{"status": "rejected", "reason": "ssid_required"}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	env := map[string]any{"status": "test_passed", "ssid": in.SSID}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiActivate(state *State, body []byte) (wexec.Result, error) {
	in, _ := parseWifiInput(body)
	if !state.WifiActivate(in.ID) {
		env := map[string]any{"status": "rejected", "reason": "unknown_id", "id": in.ID}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	env := map[string]any{"status": "applied", "id": in.ID}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

func wifiAdopt(state *State, body []byte) (wexec.Result, error) {
	in, _ := parseWifiInput(body)
	newID, ok := state.WifiAdopt(in.ID)
	if !ok {
		env := map[string]any{"status": "rejected", "reason": "unknown_id", "id": in.ID}
		b, _ := json.Marshal(env)
		return wexec.Result{Stdout: b}, nil
	}
	env := map[string]any{"status": "applied", "id": newID, "adopted": true, "changed": []string{newID}}
	b, _ := json.Marshal(env)
	return wexec.Result{Stdout: b}, nil
}

// --- journalctl streamer --------------------------------------------------

// unitLogLines maps the slug → unit name the logs.Whitelist exposes
// to per-unit canned content. Each entry is a short loop the streamer
// rotates through, prefixed with a fresh timestamp on every line.
var unitLogLines = map[string][]string{
	"airplanes-feed.service": {
		"feed: tail-uplink connected",
		"feed: 4218 msgs/s, 34 aircraft",
		"feed: heartbeat ok",
	},
	"airplanes-mlat.service": {
		"mlat: 12 peers, sync ok",
		"mlat: clock drift +0.4 ms",
		"mlat: solved 8 positions",
	},
	"readsb.service": {
		"readsb: 32 aircraft, 4 with positions",
		"readsb: rate=4200 msg/s gain=auto",
	},
	"dump978-fa.service": {
		"dump978-fa: no UAT frames",
		"dump978-fa: SDR idle (UAT_INPUT empty)",
	},
	"airplanes-978.service": {
		"airplanes-978: consumer idle",
	},
	"airplanes-claim.service": {
		"claim: registering with feed.airplanes.live",
		"claim: 200 OK, secret materialised",
	},
	"airplanes-webconfig.service": {
		"webconfig: dev-mode active",
		"webconfig: /api/status served in 1.2ms",
	},
	"airplanes-update-orchestrator.service": {
		"update-orchestrator: sequencing apt -> runtime",
		"update-orchestrator: idle",
	},
}

// StreamRunner returns a wexec.StreamRunner that emits one canned
// line per second to w until ctx is cancelled. The unit name is
// recovered from `-u <name>` in argv (logs.go's invocation shape).
func StreamRunner(state *State) wexec.StreamRunner {
	return func(ctx context.Context, w io.Writer, argv []string) error {
		_ = state // reserved for future per-unit state-driven lines
		unit := unitFromJournalctlArgv(argv)
		lines := unitLogLines[unit]
		if len(lines) == 0 {
			lines = []string{"(no canned log content for unit)"}
		}
		i := 0
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		// Emit one line right away so the SSE viewer doesn't sit
		// blank for a second on open.
		if err := emitLogLine(w, unit, lines[i]); err != nil {
			return err
		}
		i = (i + 1) % len(lines)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				if err := emitLogLine(w, unit, lines[i]); err != nil {
					return err
				}
				i = (i + 1) % len(lines)
			}
		}
	}
}

func emitLogLine(w io.Writer, unit, msg string) error {
	// Format matches journalctl --output=short: local time, space-padded
	// day (e.g. "May  9 14:23:45"). Production lines go through the same
	// hostname-strip path in internal/logs as fake ones do here.
	stamp := time.Now().Format("Jan _2 15:04:05")
	line := fmt.Sprintf("%s feeder-dev %s: %s\n", stamp, unit, msg)
	_, err := io.Copy(w, bytes.NewReader([]byte(line)))
	return err
}

func unitFromJournalctlArgv(argv []string) string {
	for i, a := range argv {
		if a == "-u" && i+1 < len(argv) {
			return argv[i+1]
		}
		if strings.HasPrefix(a, "--unit=") {
			return strings.TrimPrefix(a, "--unit=")
		}
	}
	return "unknown.service"
}
