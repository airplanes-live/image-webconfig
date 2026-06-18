// Package devfakes provides in-memory, stateful fake implementations of
// every system touchpoint the airplanes-webconfig binary depends on:
// subprocess shell-outs (apl-feed, apl-wifi, systemctl, journalctl,
// systemd-run), the readsb aircraft.json snapshot, the per-daemon
// runtime state files, the Raspberry Pi hardware probes, and the Wi-Fi
// signal probe. The fakes are consumed by cmd/devserver, which lets a
// developer iterate on the SPA without a Raspberry Pi.
//
// The package is deliberately confined to cmd/devserver. Nothing in
// cmd/webconfig or the production server code imports it; the
// production sudoers / argv contract is enforced by separate parity
// tests that don't run against these fakes.
//
// State ownership: every fake reads from the same *State. File-backed
// production readers (status.Reader, identity.Reader, runtimestate.Read)
// see a coherent on-disk view via atomic temp+rename writes; exec-backed
// readers (feedenv.Reader through the fake `apl-feed config show`) read
// the same in-memory map the fake apply mutates. The fakes are only
// the *subprocess* layer; the rest of the server stays on the
// production code path.
package devfakes

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/feedmeta"
)

// Paths bundles every on-disk file the production readers consult.
// cmd/devserver populates these from a single state directory; each
// field becomes a per-file child of that directory so cleanup is
// `rm -rf <state-dir>`.
type Paths struct {
	StateDir        string
	FeederID        string
	ClaimSecret     string
	PasswordHash    string
	Manifest        string
	RuntimeManifest string
	AircraftJSON    string
	ReadsbStats     string
	MlatState       string
	FeedState       string
	UAT978State     string
	Dump978FAState  string
	ReadsbState     string
	UpgradeState    string
	// OrchestratorState backs GET /api/orchestrator/state — the file the
	// simulated update-orchestrator run (StartOrchestratorRun) writes its
	// progression to, standing in for /run/airplanes/orchestrator.state.
	OrchestratorState string
}

// DefaultPaths returns a Paths bundle rooted at dir. The filenames are
// chosen so a developer ls'ing the state-dir sees recognisable shapes
// even though the production locations are spread across /etc, /run,
// and /var/lib.
func DefaultPaths(dir string) Paths {
	return Paths{
		StateDir:          dir,
		FeederID:          filepath.Join(dir, "feeder-id"),
		ClaimSecret:       filepath.Join(dir, "feeder-claim-secret"),
		PasswordHash:      filepath.Join(dir, "password.hash"),
		Manifest:          filepath.Join(dir, "build-manifest.json"),
		RuntimeManifest:   filepath.Join(dir, "runtime-manifest.json"),
		AircraftJSON:      filepath.Join(dir, "aircraft.json"),
		ReadsbStats:       filepath.Join(dir, "stats.json"),
		MlatState:         filepath.Join(dir, "mlat.state"),
		FeedState:         filepath.Join(dir, "feed.state"),
		UAT978State:       filepath.Join(dir, "uat978.state"),
		Dump978FAState:    filepath.Join(dir, "dump978fa.state"),
		ReadsbState:       filepath.Join(dir, "readsb.state"),
		UpgradeState:      filepath.Join(dir, "upgrade-state"),
		OrchestratorState: filepath.Join(dir, "orchestrator.state"),
	}
}

// WifiNetwork is the wire shape the apl-wifi helper emits for each
// keyfile (see files/usr/local/bin/apl-wifi `_apl_wifi_network_json`).
// All fields are reflected verbatim into the JSON envelope; the SPA's
// Wi-Fi panel reads `ssid`, `priority`, `hidden`, `has_psk`, `managed`,
// and `active`.
type WifiNetwork struct {
	ID              string `json:"id"`
	UUID            string `json:"uuid"`
	SSID            string `json:"ssid"`
	Hidden          bool   `json:"hidden"`
	Priority        int    `json:"priority"`
	HasPSK          bool   `json:"has_psk"`
	Managed         bool   `json:"managed"`
	FirstRunProfile bool   `json:"first_run_profile"`
	Autoconnect     string `json:"autoconnect"`
	Active          bool   `json:"active"`
	Device          string `json:"-"` // active interface, omitted from per-network JSON
	IPv4            string `json:"-"` // active connection IPv4, omitted from per-network JSON
}

// State holds the entire in-memory simulation. All mutations and
// reads go through methods that take the mutex; callers must not
// touch the fields directly. Every mutation that affects an on-disk
// view (feed.env, runtime state files, claim secret, aircraft.json,
// wifi signal sidecar) is synced inside the same critical section
// so the production reader can never observe a partial write.
type State struct {
	Paths    Paths
	mu       sync.Mutex
	feedEnv  map[string]string
	networks []WifiNetwork
	services map[string]string // unit name → "active"|"inactive"|"failed"
	uuid     string            // feeder UUID; seeded with a dev value, replaceable via ImportIdentity
	claim    string            // 16-char A-Z0-9 secret; empty until RegisterClaim or ImportIdentity
	version  *int              // claim.version (nil unless ImportIdentity supplied one)
	// claimRegisteredAt is when RegisterClaim minted the secret; zero for
	// imported/pre-seeded identities. Drives the fake "waiting for first
	// data" window in claimStatusFeed.
	claimRegisteredAt time.Time
	feedStart         time.Time
	tickStart         time.Time
	// aggregators holds dev-only third-party aggregator records keyed by
	// adapter id (e.g. "fr24"). Purely in-memory — the production
	// apl-aggregator helper persists to /etc/airplanes/aggregators; the fake
	// just lets the SPA exercise enable / disable / config / backup flows
	// without a Pi.
	aggregators map[string]*aggRecord
	// ssh holds dev-only SSH-access state for the pi account. Purely
	// in-memory — the production apl-ssh helper touches /etc/shadow + the sshd
	// drop-in + the managed key file; the fake just lets the SPA exercise the
	// enable/rotate/disable + set-key/clear-key flows without a Pi.
	ssh sshFakeState
	// orchestratorOutcome selects how a simulated update-orchestrator
	// run ends (OrchestratorOutcome* constants). Empty means ok.
	orchestratorOutcome string
	// orchestratorAbort/orchestratorAborted/orchestratorWG let
	// AbortOrchestratorRuns stop in-flight simulated runs and wait out
	// their goroutines, so test cleanup never races a state-file write.
	orchestratorAbort   chan struct{}
	orchestratorAborted bool
	orchestratorWG      sync.WaitGroup
}

// aggRecord is the dev-fake per-adapter aggregator state the verbs mutate.
type aggRecord struct {
	enabled bool
	mlat    bool
	fields  map[string]string // includes secret values (e.g. sharing_key)
	// installUntil mimics the production async enable: status reports the
	// adapter as "installing" until this time, then "running". Zero = not
	// installing. Compared by wall clock at status time (no timer/goroutine).
	installUntil time.Time
	// reconcileError mimics a failed background auto-update: the production
	// helper stamps {error_code, message} into state when a post-update
	// reconcile fails, and surfaces it via _adapter_json. nil = no failure.
	reconcileError map[string]string
	// external marks an adapter as installed OUTSIDE airplanes.live (a manual
	// vendor/apt install), so the SPA renders the read-only "Unmanaged" view.
	// Seeded from AGG_DEV_EXTERNAL; mirrors the helper's external_install bit.
	external bool
}

// sshFakeState is the dev-fake SSH-access state for the pi account. Seeded as
// "pi present, nothing webconfig-managed" so the card opens on the "Enable SSH"
// affordance, mirroring a fresh feeder.
type sshFakeState struct {
	piPresent       bool
	passwordEnabled bool // a webconfig-set password exists AND is unlocked
	keyPresent      bool // a managed key is set
}

// SSHStatus returns the current dev SSH facts under the state lock.
func (s *State) SSHStatus() (piPresent, passwordEnabled, keyPresent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ssh.piPresent, s.ssh.passwordEnabled, s.ssh.keyPresent
}

// SSHEnablePassword marks the pi password set + unlocked (enable/rotate).
func (s *State) SSHEnablePassword() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ssh.passwordEnabled = true
}

// SSHDisablePassword locks the pi password (always, like production).
func (s *State) SSHDisablePassword() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ssh.passwordEnabled = false
}

// SSHSetKey marks a managed key present.
func (s *State) SSHSetKey() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ssh.keyPresent = true
}

// SSHClearKey removes the managed key.
func (s *State) SSHClearKey() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ssh.keyPresent = false
}

// aggInstallDuration is how long the dev fake pretends a vendor acquire takes,
// so the SPA's 202-accepted → poll-status → running flow is exercisable.
const aggInstallDuration = 2 * time.Second

const devFakeFeederUUID = "11111111-2222-3333-4444-555555555555"

// NewState constructs a state with sensible seed values for first-load
// rendering. The on-disk projection is NOT written yet — callers must
// invoke SyncAll() once the state-dir exists so atomic rename has a
// place to land its temp files.
func NewState(p Paths) *State {
	now := time.Now()
	st := &State{
		Paths: p,
		uuid:  devFakeFeederUUID,
		feedEnv: map[string]string{
			"LATITUDE":              "51.5",
			"LONGITUDE":             "-0.1",
			"ALTITUDE":              "12", // bare metres — feed's apply layer canonicalises ft→m on write
			"GEO_CONFIGURED":        "true",
			"MLAT_USER":             "dev-feeder",
			"MLAT_ENABLED":          "true",
			"MLAT_PRIVATE":          "false",
			"REPORT_STATUS":         "true",
			"REMOTE_CONFIG_ENABLED": "true",
			"INPUT":                 "",
			"INPUT_TYPE":            "",
			"GAIN":                  "auto",
			"UAT_INPUT":             "",
			"DUMP978_SDR_SERIAL":    "",
			"DUMP978_GAIN":          "42.1",
		},
		networks: []WifiNetwork{
			{
				ID: "airplanes-config-wifi", UUID: "00000000-0000-0000-0000-000000000001",
				SSID: "homenet", Hidden: false, Priority: 10, HasPSK: true,
				Managed: true, FirstRunProfile: true, Autoconnect: "true",
				Active: true, Device: "wlan0", IPv4: "192.168.1.42",
			},
			{
				ID: "airplanes-wifi-cafe", UUID: "00000000-0000-0000-0000-000000000002",
				SSID: "Cafe Wifi", Hidden: false, Priority: 5, HasPSK: false,
				Managed: true, Autoconnect: "true",
			},
			{
				// A foreign (flash-time / netplan) network so the Adopt flow is
				// exercisable in the dev UI without a Pi.
				ID: "foreign-00000000-0000-0000-0000-0000000000f0", UUID: "00000000-0000-0000-0000-0000000000f0",
				SSID: "GreenKingdom-Homelab", Hidden: false, Priority: 0, HasPSK: true,
				Managed: false, Autoconnect: "true",
			},
		},
		services: map[string]string{
			// Monitored services — shown on the dashboard tiles. Seeded active so
			// the dashboard renders something interesting on first load.
			"airplanes-feed.service":      "active",
			"airplanes-mlat.service":      "active",
			"readsb.service":              "active",
			"dump978-fa.service":          "active",
			"airplanes-978.service":       "active",
			"lighttpd.service":            "active",
			"airplanes-webconfig.service": "active",
			// Maintenance unit — must be inactive so the pre-flight
			// guard at handlers.go:maintenanceUnitActive lets the user click
			// reboot / Update System without a 409.
			"airplanes-update-orchestrator.service": "inactive",
			// Claim unit — never reported active; the SPA pulls progress from
			// the claim SSE log instead.
			"airplanes-claim.service": "inactive",
		},
		feedStart: now,
		tickStart: now,
		aggregators: map[string]*aggRecord{
			// Seed FlightAware as already set up so first load shows an aggregator
			// dashboard tile + the configured manage page; Flightradar24 stays
			// not-set-up so the setup form (with prefilled location) is exercisable.
			"piaware": {
				enabled: true,
				mlat:    true,
				fields:  map[string]string{"feeder_id": "e03c81bd-dbab-4237-8b2f-bea1c6dfb74f"},
				// Seed a failed background auto-update so the "Update — Failed" row
				// and the manage-page / list-row / dashboard-tile "Update failed"
				// hint are exercisable in the dev UI without a Pi.
				reconcileError: map[string]string{
					"error_code": "state_error",
					"message":    "piaware did not stay running after the update",
				},
			},
		},
		// SSH: the pi account exists but nothing is webconfig-managed yet, so the
		// SSH card opens on the "Enable SSH for pi" affordance like a fresh feeder.
		ssh: sshFakeState{piPresent: true},
	}
	// AGG_DEV_EXTERNAL=<id>[:conflict][,...] simulates a manual vendor install so
	// the read-only "Unmanaged" view is exercisable in the dev UI. Plain "<id>"
	// renders a pure unmanaged install (managed_install=false; Remove disabled);
	// "<id>:conflict" keeps a managed copy too (managed_install=true; Remove
	// enabled) — the reporter's vendor-copy-plus-our-copy scenario.
	for _, tok := range strings.Split(os.Getenv("AGG_DEV_EXTERNAL"), ",") {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			continue
		}
		id, mode, _ := strings.Cut(tok, ":")
		r := st.aggRecordLocked(id)
		r.external = true
		r.reconcileError = nil
		if mode == "conflict" {
			r.enabled = true // configured=true → managed_install=true
		} else {
			r.enabled = false
			r.mlat = false
			r.fields = map[string]string{}
		}
	}
	return st
}

// AggregatorExternal reports whether id is seeded as an external (unmanaged)
// install — the dev analogue of the helper's external_install bit.
func (s *State) AggregatorExternal(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.aggregators[id]
	return ok && r.external
}

// AggregatorRecord returns a copy of the dev aggregator record for id:
// whether it is enabled, whether MLAT is on, and the stored fields (including
// secret values). A never-touched adapter reports (false, false, empty).
func (s *State) AggregatorRecord(id string) (enabled, mlat bool, fields map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.aggregators[id]
	if !ok {
		return false, false, map[string]string{}
	}
	f := make(map[string]string, len(r.fields))
	for k, v := range r.fields {
		f[k] = v
	}
	return r.enabled, r.mlat, f
}

// AggregatorReconcileError returns a copy of the dev adapter's seeded
// reconcile_error ({error_code, message}), or nil when there is none. Mirrors
// the production helper, which surfaces a failed background auto-update.
func (s *State) AggregatorReconcileError(id string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.aggregators[id]
	if !ok || r.reconcileError == nil {
		return nil
	}
	out := make(map[string]string, len(r.reconcileError))
	for k, v := range r.reconcileError {
		out[k] = v
	}
	return out
}

// aggRecordLocked returns (creating if needed) the record for id. Caller holds s.mu.
func (s *State) aggRecordLocked(id string) *aggRecord {
	r := s.aggregators[id]
	if r == nil {
		r = &aggRecord{fields: map[string]string{}}
		s.aggregators[id] = r
	}
	return r
}

// AggregatorEnable marks id enabled, merges non-empty fields, sets MLAT, and
// flips the templated unit active so the dashboard tile renders running. It
// stamps a short installing window so status mimics the production async
// enable (accepted → installing → running).
func (s *State) AggregatorEnable(id string, fields map[string]string, mlat bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.aggRecordLocked(id)
	for k, v := range fields {
		if v != "" {
			r.fields[k] = v
		}
	}
	r.enabled = true
	r.mlat = mlat
	r.installUntil = time.Now().Add(aggInstallDuration)
	s.services[aggregatorUnit(id)] = "active"
}

// AggregatorInstalling reports whether id is within its simulated installing
// window — the dev analogue of a running enable worker.
func (s *State) AggregatorInstalling(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.aggregators[id]
	return r != nil && !r.installUntil.IsZero() && time.Now().Before(r.installUntil)
}

// AggregatorSet merges config (MLAT toggle + fields) without changing the
// enabled flag — mirrors the helper's state-only `set` verb.
func (s *State) AggregatorSet(id string, fields map[string]string, mlat *bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.aggRecordLocked(id)
	for k, v := range fields {
		if v != "" {
			r.fields[k] = v
		}
	}
	if mlat != nil {
		r.mlat = *mlat
	}
}

// AggregatorDisable clears the enabled flag but keeps the stored identity, and
// stops the unit. Mirrors `disable`.
func (s *State) AggregatorDisable(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.aggregators[id]; r != nil {
		r.enabled = false
	}
	s.services[aggregatorUnit(id)] = "inactive"
}

// AggregatorReset drops the airplanes.live-managed copy and stops the unit.
// Mirrors `reset`: production tears down only what we own, so an unmanaged vendor
// install (the conflict case) survives — keep the external marker if it was set.
func (s *State) AggregatorReset(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.aggregators[id]; ok && r.external {
		s.aggregators[id] = &aggRecord{fields: map[string]string{}, external: true}
	} else {
		delete(s.aggregators, id)
	}
	s.services[aggregatorUnit(id)] = "inactive"
}

// AggregatorExport returns a snapshot of every configured adapter's recoverable
// identity (id → fields incl. secrets, plus MLAT). Mirrors `export`.
func (s *State) AggregatorExport() map[string]struct {
	Mlat   bool
	Fields map[string]string
} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]struct {
		Mlat   bool
		Fields map[string]string
	}{}
	for id, r := range s.aggregators {
		if len(r.fields) == 0 {
			continue
		}
		f := make(map[string]string, len(r.fields))
		for k, v := range r.fields {
			f[k] = v
		}
		out[id] = struct {
			Mlat   bool
			Fields map[string]string
		}{Mlat: r.mlat, Fields: f}
	}
	return out
}

// AggregatorImport seeds id's identity from a backup (enabled stays false).
// Mirrors `import`.
func (s *State) AggregatorImport(id string, fields map[string]string, mlat bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.aggRecordLocked(id)
	r.enabled = false
	r.mlat = mlat
	for k, v := range fields {
		r.fields[k] = v
	}
}

// aggregatorUnit is the templated unit name for an adapter id.
func aggregatorUnit(id string) string { return "airplanes-aggregator@" + id + ".service" }

// SyncAll seeds every backing file the production readers depend on.
// Idempotent — re-running it overwrites with the current in-memory
// snapshot. Returns the first sync error so a misconfigured state-dir
// surfaces before the HTTP server starts.
func (s *State) SyncAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncIdentityLocked(); err != nil {
		return err
	}
	if err := s.syncAircraftJSONLocked(); err != nil {
		return err
	}
	if err := s.syncReadsbStatsLocked(); err != nil {
		return err
	}
	if err := s.syncRuntimeStatesLocked(); err != nil {
		return err
	}
	if err := s.syncManifestLocked(); err != nil {
		return err
	}
	if err := s.syncUpgradeStateLocked(); err != nil {
		return err
	}
	return nil
}

// ApplyFeedEnv merges the given updates into the in-memory feed.env
// and re-syncs the on-disk file. Mirrors `_apl_feed_apply_derive_geo`
// in feed/scripts/lib/feed-env-apply.sh: when LATITUDE or LONGITUDE
// appear in the payload (touched, not just changed) and GEO_CONFIGURED
// is NOT explicit in the same payload, derive GEO_CONFIGURED from the
// merged pair — false only when BOTH axes are empty or numerically
// zero, true otherwise. An explicit GEO_CONFIGURED override in the
// payload wins. Returns the sorted list of keys whose value actually
// moved.
func (s *State) ApplyFeedEnv(updates map[string]string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var changed []string
	touchedLat := false
	touchedLon := false
	explicitGeo := false
	for k, v := range updates {
		switch k {
		case "LATITUDE":
			touchedLat = true
		case "LONGITUDE":
			touchedLon = true
		case "GEO_CONFIGURED":
			explicitGeo = true
		case "ALTITUDE":
			// Mirror feed's apply-layer canonicalisation: store
			// bare metres on disk regardless of the operator's
			// input suffix. Production feed's apply validator
			// rejects unparseable / out-of-range input BEFORE
			// the canonicaliser ever runs, so the runner side
			// (applyFeed in runners.go) is responsible for the
			// rejected envelope; here we leave a value that
			// fails canonicalisation untouched, matching what
			// the bash side does if its validator is bypassed.
			if canon, ok := feedmeta.AltitudeToBareMetres(v); ok {
				v = canon
			}
		}
		prev, existed := s.feedEnv[k]
		if existed && prev == v {
			continue
		}
		s.feedEnv[k] = v
		changed = append(changed, k)
	}
	if !explicitGeo && (touchedLat || touchedLon) {
		derived := "false"
		if !isZeroCoord(s.feedEnv["LATITUDE"]) || !isZeroCoord(s.feedEnv["LONGITUDE"]) {
			derived = "true"
		}
		if s.feedEnv["GEO_CONFIGURED"] != derived {
			s.feedEnv["GEO_CONFIGURED"] = derived
			changed = appendIfMissing(changed, "GEO_CONFIGURED")
		}
	}
	sort.Strings(changed)
	// MLAT/UAT decisions depend on lat/lon and on UAT_INPUT — keep the
	// runtime state files in sync so the dashboard tile flips as the
	// operator edits config.
	if err := s.syncRuntimeStatesLocked(); err != nil {
		return nil, err
	}
	return changed, nil
}

// FeedEnvSnapshot returns a copy of the feed.env map for tests and
// for the JSON payload the fake apl-feed schema response emits.
func (s *State) FeedEnvSnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.feedEnv))
	for k, v := range s.feedEnv {
		out[k] = v
	}
	return out
}

// ServiceState returns the systemctl is-active string for unit, or
// "inactive" for any unit not in the seeded map. The maintenance-unit
// guard at handlers.go:maintenanceUnitActive walks the multi-unit
// response in order, so unknown units defaulting to inactive is the
// safe choice.
func (s *State) ServiceState(unit string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.services[unit]; ok {
		return st
	}
	return "inactive"
}

// SetServiceState pins a service to the given state. Used by the
// transient-unit fake so a second click of "Update" within the
// in-progress window returns 409 instead of stacking starts.
func (s *State) SetServiceState(unit, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.services[unit] = state
}

// RegisterClaim materialises a 16-char A-Z0-9 claim secret in-memory
// and syncs it to disk so the next GET /api/identity reports
// `claim_secret_present: true`. Idempotent — re-running with an
// existing secret keeps the prior value.
func (s *State) RegisterClaim() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claim != "" {
		return nil
	}
	s.claim = randomClaimSecret()
	s.claimRegisteredAt = time.Now()
	return s.syncClaimSecretLocked()
}

// RotateClaim mints a fresh secret and bumps the claim version, mirroring
// what apl-feed claim rotate does once the server accepts the new secret.
// Rewriting the secret file changes its mtime, which is what invalidates
// the server-side claim-status cache (keyed on the identity fingerprint).
// Returns the new version. Errors when there is no secret to rotate — the
// same precondition the real helper enforces.
func (s *State) RotateClaim() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claim == "" {
		return 0, fmt.Errorf("no active claim secret to rotate")
	}
	s.claim = randomClaimSecret()
	next := 1
	if s.version != nil {
		next = *s.version + 1
	}
	s.version = &next
	if err := s.syncClaimSecretLocked(); err != nil {
		return 0, err
	}
	return next, nil
}

// ClaimRegisteredAt returns when RegisterClaim minted the secret (zero
// for imported/pre-seeded identities). claimStatusFeed uses it to fake
// the server-side "waiting for first data" window after registration.
func (s *State) ClaimRegisteredAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimRegisteredAt
}

// IdentitySnapshot returns uuid, secret, version, and the registration
// moment under one lock so claimStatusFeed never mixes fields from two
// different identities during a concurrent register/import.
func (s *State) IdentitySnapshot() (uuid, secret string, version *int, registeredAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v *int
	if s.version != nil {
		copyV := *s.version
		v = &copyV
	}
	return s.uuid, s.claim, v, s.claimRegisteredAt
}

// ClaimSecret returns the materialised secret (empty until
// RegisterClaim is called). Tests use this; the running server
// reads the file via identity.Reader, not via this method.
func (s *State) ClaimSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claim
}

// Identity returns the current feeder UUID, claim secret (empty if not
// yet registered), and claim version (nil if not set). Tests use this;
// the production server reads the files directly.
func (s *State) Identity() (uuid, secret string, version *int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v *int
	if s.version != nil {
		copyV := *s.version
		v = &copyV
	}
	return s.uuid, s.claim, v
}

// ImportIdentity replaces the in-memory UUID + secret + version and
// re-syncs the on-disk projection. Mirrors what apl-feed restore would
// do on a real feeder: overwrite both files atomically.
func (s *State) ImportIdentity(uuid, secret string, version *int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uuid = uuid
	s.claim = secret
	// Imported identities have no registration moment; a stale window
	// from an earlier RegisterClaim must not leak onto them.
	s.claimRegisteredAt = time.Time{}
	if version != nil {
		v := *version
		s.version = &v
	} else {
		s.version = nil
	}
	return s.syncIdentityLocked()
}

// networksSnapshot returns a defensive copy of the network list. Used
// by the fake wifi runners when they need to read freshly-mutated
// state without re-entering the public WifiList path.
func (s *State) networksSnapshot() []WifiNetwork {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WifiNetwork(nil), s.networks...)
}

// WifiList returns a snapshot of the network list and the active-
// connection summary the apl-wifi list envelope embeds.
func (s *State) WifiList() ([]WifiNetwork, *ActiveConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]WifiNetwork(nil), s.networks...)
	return out, s.activeConnLocked()
}

// WifiStatus returns the active connection (or nil) and the wifi
// device summary the apl-wifi status envelope embeds. Same
// data the WifiProbe consumes for the dashboard tile.
func (s *State) WifiStatus() (*ActiveConn, WifiDevice) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dev := WifiDevice{Name: "wlan0", State: "disconnected"}
	if a := s.activeConnLocked(); a != nil {
		dev.Name = a.Device
		dev.State = "connected"
	}
	return s.activeConnLocked(), dev
}

// WifiAdd appends a new managed network. id is generated from ssid
// the way apl-wifi does (slugified). Returns the new network's id
// and any error from the on-disk wifi-signal sync.
func (s *State) WifiAdd(ssid, psk, country string, hidden bool, priority int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.uniqueWifiIDLocked(ssid)
	n := WifiNetwork{
		ID: id, UUID: newWifiUUID(), SSID: ssid, Hidden: hidden, Priority: priority,
		HasPSK: psk != "", Managed: true, Autoconnect: "true",
	}
	s.networks = append(s.networks, n)
	return id, nil
}

// WifiUpdate mutates the matching network in place. Returns false if
// the id is not in the list. psk is "" → unchanged HasPSK status.
func (s *State) WifiUpdate(id, ssid, psk, country string, hidden bool, priority int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.networks {
		if s.networks[i].ID != id {
			continue
		}
		if ssid != "" {
			s.networks[i].SSID = ssid
		}
		s.networks[i].Hidden = hidden
		s.networks[i].Priority = priority
		if psk != "" {
			s.networks[i].HasPSK = true
		}
		return true
	}
	return false
}

// WifiDelete removes the matching network. Returns false if not found.
func (s *State) WifiDelete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.networks {
		if s.networks[i].ID != id {
			continue
		}
		s.networks = append(s.networks[:i], s.networks[i+1:]...)
		return true
	}
	return false
}

// WifiAdopt converts a foreign network (foreign-<uuid>) into a managed keyfile:
// it gets a fresh managed id + uuid and Managed=true, mirroring apl-wifi adopt.
// Returns the new managed id, or ("", false) if id isn't a foreign network.
func (s *State) WifiAdopt(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.networks {
		if s.networks[i].ID != id || s.networks[i].Managed {
			continue
		}
		newID := s.uniqueWifiIDLocked(s.networks[i].SSID)
		s.networks[i].ID = newID
		s.networks[i].UUID = newWifiUUID()
		s.networks[i].Managed = true
		s.networks[i].FirstRunProfile = false
		return newID, true
	}
	return "", false
}

// WifiActivate flips the `active` flag onto the chosen network and
// clears it from all others. Returns false if the id is not found.
func (s *State) WifiActivate(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	hit := false
	for i := range s.networks {
		if s.networks[i].ID == id {
			s.networks[i].Active = true
			s.networks[i].Device = "wlan0"
			s.networks[i].IPv4 = "192.168.1.42"
			hit = true
		} else {
			s.networks[i].Active = false
			s.networks[i].Device = ""
			s.networks[i].IPv4 = ""
		}
	}
	return hit
}

// ActiveConn mirrors the apl-wifi list envelope's `active_connection`
// field. The SPA renders the SSID and device from it.
type ActiveConn struct {
	UUID   string `json:"uuid"`
	Device string `json:"device"`
	SSID   string `json:"ssid"`
	IPv4   string `json:"ipv4,omitempty"`
	ID     string `json:"id,omitempty"`
}

// WifiDevice mirrors the apl-wifi status envelope's `wifi_device`
// field; the SPA shows the state under the active SSID.
type WifiDevice struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func (s *State) activeConnLocked() *ActiveConn {
	for _, n := range s.networks {
		if !n.Active {
			continue
		}
		return &ActiveConn{
			UUID: n.UUID, Device: n.Device, SSID: n.SSID, IPv4: n.IPv4, ID: n.ID,
		}
	}
	return nil
}

// RefreshAircraftJSON re-projects the synthesised aircraft snapshot
// to Paths.AircraftJSON so a status read sees moving messages_counter
// + aircraft_count. The fake systemctl runner calls this before every
// is-active fan-out — handlers.handleStatus reads aircraft.json right
// after the fan-out, so the snapshot is always fresh to the SPA.
func (s *State) RefreshAircraftJSON() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncAircraftJSONLocked(); err != nil {
		return err
	}
	return s.syncReadsbStatsLocked()
}

// --- atomic on-disk projection --------------------------------------------

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".devfakes-*")
	if err != nil {
		return fmt.Errorf("devfakes: temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("devfakes: write temp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("devfakes: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("devfakes: close temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("devfakes: rename: %w", err)
	}
	return nil
}

func (s *State) syncIdentityLocked() error {
	if err := writeAtomic(s.Paths.FeederID, []byte(s.uuid+"\n"), 0o644); err != nil {
		return err
	}
	if s.claim == "" {
		return nil
	}
	return s.syncClaimSecretLocked()
}

func (s *State) syncClaimSecretLocked() error {
	return writeAtomic(s.Paths.ClaimSecret, []byte(s.claim+"\n"), 0o640)
}

func (s *State) syncManifestLocked() error {
	manifest := map[string]any{
		"version":    "dev",
		"kind":       "dev",
		"commit_sha": "0000000000000000000000000000000000000000",
		"build_date": s.feedStart.UTC().Format(time.RFC3339),
		"arches":     []string{"dev"},
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeAtomic(s.Paths.Manifest, b, 0o644); err != nil {
		return err
	}
	// Runtime-overlay manifest — distinct shape from the image manifest,
	// carrying the component versions that advance on update, so dev
	// /api/status mirrors the prod payload (image_manifest + runtime_manifest).
	runtime := map[string]any{
		"channel":    "dev",
		"commit_sha": "0000000000000000000000000000000000000000",
		"build_date": s.feedStart.UTC().Format(time.RFC3339),
		"arches":     []string{"dev"},
		"components": map[string]any{
			"feed_scripts": map[string]any{"commit_sha": "0000000000000000000000000000000000000000", "version": "dev"},
			"webconfig":    map[string]any{"commit_sha": "0000000000000000000000000000000000000000", "version": "dev"},
		},
	}
	rb, err := json.Marshal(runtime)
	if err != nil {
		return err
	}
	return writeAtomic(s.Paths.RuntimeManifest, rb, 0o644)
}

func (s *State) syncUpgradeStateLocked() error {
	return writeAtomic(s.Paths.UpgradeState, []byte("clean\n"), 0o644)
}

func (s *State) syncAircraftJSONLocked() error {
	elapsed := time.Since(s.feedStart).Seconds()
	doc := map[string]any{
		"now":      float64(time.Now().UnixNano()) / 1e9,
		"messages": int64(elapsed * 4200),
		"aircraft": make([]struct{}, 32+int(elapsed)%18),
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return writeAtomic(s.Paths.AircraftJSON, b, 0o644)
}

// syncReadsbStatsLocked projects a synthetic readsb stats.json so the dev UI
// renders the effective-gain surfaces. The gain steps a little over time
// (44.5–49.5 dB) to look like a live autogain settling, and the file is
// rewritten on every refresh so its mtime stays fresh (age_sec small).
func (s *State) syncReadsbStatsLocked() error {
	elapsed := time.Since(s.feedStart).Seconds()
	doc := map[string]any{
		"gain_db":  44.5 + float64(int(elapsed)%6),
		"messages": int64(elapsed * 4200),
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return writeAtomic(s.Paths.ReadsbStats, b, 0o644)
}

func (s *State) syncRuntimeStatesLocked() error {
	feed := s.feedEnv
	geoConfigured := feed["GEO_CONFIGURED"] == "true"
	mlatEnabled := feed["MLAT_ENABLED"] == "true"
	mlatPrivateValid := feed["MLAT_PRIVATE"] == "true" || feed["MLAT_PRIVATE"] == "false"
	uatInput := feed["UAT_INPUT"]

	mlatState, mlatReason := "enabled", "ok"
	switch {
	case !mlatEnabled:
		mlatState, mlatReason = "disabled", "mlat_enabled_false"
	case !geoConfigured:
		mlatState, mlatReason = "disabled", "geo_not_configured"
	case strings.TrimSpace(feed["ALTITUDE"]) == "":
		mlatState, mlatReason = "misconfigured", "altitude_empty"
	case !mlatPrivateValid:
		mlatState, mlatReason = "misconfigured", "mlat_private_invalid"
	}

	uatState, uatReason := "disabled", "uat_disabled"
	dump978State, dump978Reason := "disabled", "uat_disabled"
	if uatInput != "" {
		uatState, uatReason = "enabled", "ok"
		dump978State, dump978Reason = "enabled", "ok"
	}

	// readsb self-disables when a pinned 1090 serial isn't among the fake SDRs
	// (SeedSDRSysfs lays down 1090 + 00000001). Set READSB_SDR_SERIAL to a
	// value outside that set in the dev config to exercise the no_hardware
	// dashboard (readsb tile, feed-idle tile, hero title, overall "down").
	readsbState, readsbReason := "enabled", "ok"
	if serial := strings.TrimSpace(feed["READSB_SDR_SERIAL"]); serial != "" &&
		serial != "1090" && serial != "00000001" {
		readsbState, readsbReason = "disabled", "no_hardware"
	}

	writeState := func(path, decision, reason string) error {
		body := "schema_version=1\nstate=" + decision + "\nreason=" + reason + "\n"
		return writeAtomic(path, []byte(body), 0o644)
	}
	if err := writeState(s.Paths.MlatState, mlatState, mlatReason); err != nil {
		return err
	}
	if err := writeState(s.Paths.FeedState, "enabled", "ok"); err != nil {
		return err
	}
	if err := writeState(s.Paths.UAT978State, uatState, uatReason); err != nil {
		return err
	}
	if err := writeState(s.Paths.Dump978FAState, dump978State, dump978Reason); err != nil {
		return err
	}
	return writeState(s.Paths.ReadsbState, readsbState, readsbReason)
}

// --- helpers --------------------------------------------------------------

// isZeroCoord matches `_apl_feed_apply_derive_geo`'s lat_zero/lon_zero
// test: an axis is "zero" when its trimmed text is empty OR parses
// numerically to 0. Anything that doesn't parse (junk text) is treated
// as zero — the production awk falls into the same branch.
func isZeroCoord(v string) bool {
	t := strings.TrimSpace(v)
	if t == "" {
		return true
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return true
	}
	return f == 0
}

func appendIfMissing(s []string, v string) []string {
	for _, e := range s {
		if e == v {
			return s
		}
	}
	return append(s, v)
}

func (s *State) uniqueWifiIDLocked(ssid string) string {
	base := slugify(ssid)
	if base == "" {
		base = "network"
	}
	candidate := "airplanes-wifi-" + base
	for i := 2; ; i++ {
		used := false
		for _, n := range s.networks {
			if n.ID == candidate {
				used = true
				break
			}
		}
		if !used {
			return candidate
		}
		candidate = "airplanes-wifi-" + base + "-" + strconv.Itoa(i)
	}
}

func slugify(in string) string {
	in = strings.ToLower(in)
	var b strings.Builder
	prevDash := false
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 41 {
		s = s[:41]
	}
	return s
}

// randomClaimSecret returns a 16-character A-Z0-9 string. Matches
// `identity.canonicalSecret` so the production identity reader
// accepts it without further canonicalisation.
func randomClaimSecret() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	out := make([]byte, 16)
	for i, b := range buf {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out)
}

func newWifiUUID() string {
	// 4-byte hex randomness is enough for the simulator. Production
	// UUIDs come from NetworkManager.
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
