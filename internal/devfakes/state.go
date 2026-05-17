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
// State ownership: every fake reads from the same *State and writes
// through to backing temp files via atomic temp+rename so the real
// production readers (feedenv.Reader, status.Reader, identity.Reader,
// runtimestate.Read) see a coherent on-disk view. The fakes are only
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
)

// Paths bundles every on-disk file the production readers consult.
// cmd/devserver populates these from a single state directory; each
// field becomes a per-file child of that directory so cleanup is
// `rm -rf <state-dir>`.
type Paths struct {
	StateDir       string
	FeedEnv        string
	FeederID       string
	ClaimSecret    string
	PasswordHash   string
	Manifest       string
	AircraftJSON   string
	MlatState      string
	FeedState      string
	UAT978State    string
	Dump978FAState string
	UpgradeState   string
}

// DefaultPaths returns a Paths bundle rooted at dir. The filenames are
// chosen so a developer ls'ing the state-dir sees recognisable shapes
// even though the production locations are spread across /etc, /run,
// and /var/lib.
func DefaultPaths(dir string) Paths {
	return Paths{
		StateDir:       dir,
		FeedEnv:        filepath.Join(dir, "feed.env"),
		FeederID:       filepath.Join(dir, "feeder-id"),
		ClaimSecret:    filepath.Join(dir, "feeder-claim-secret"),
		PasswordHash:   filepath.Join(dir, "password.hash"),
		Manifest:       filepath.Join(dir, "build-manifest.json"),
		AircraftJSON:   filepath.Join(dir, "aircraft.json"),
		MlatState:      filepath.Join(dir, "mlat.state"),
		FeedState:      filepath.Join(dir, "feed.state"),
		UAT978State:    filepath.Join(dir, "uat978.state"),
		Dump978FAState: filepath.Join(dir, "dump978fa.state"),
		UpgradeState:   filepath.Join(dir, "upgrade-state"),
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
	Paths     Paths
	mu        sync.Mutex
	feedEnv   map[string]string
	networks  []WifiNetwork
	services  map[string]string // unit name → "active"|"inactive"|"failed"
	claim     string            // 16-char A-Z0-9 secret; empty until RegisterClaim
	feedStart time.Time
	tickStart time.Time
}

// NewState constructs a state with sensible seed values for first-load
// rendering. The on-disk projection is NOT written yet — callers must
// invoke SyncAll() once the state-dir exists so atomic rename has a
// place to land its temp files.
func NewState(p Paths) *State {
	now := time.Now()
	return &State{
		Paths: p,
		feedEnv: map[string]string{
			"LATITUDE":              "51.5",
			"LONGITUDE":             "-0.1",
			"ALTITUDE":              "12",
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
			"DUMP978_GAIN":          "auto",
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
			// Maintenance units — must be inactive so the pre-flight
			// guard at handlers.go:maintenanceUnitActive lets the user click
			// reboot / update / system-upgrade without a 409.
			"airplanes-update.service":           "inactive",
			"airplanes-system-upgrade.service":   "inactive",
			"airplanes-webconfig-update.service": "inactive",
			// Claim unit — never reported active; the SPA pulls progress from
			// the claim SSE log instead.
			"airplanes-claim.service": "inactive",
		},
		feedStart: now,
		tickStart: now,
	}
}

// SyncAll seeds every backing file the production readers depend on.
// Idempotent — re-running it overwrites with the current in-memory
// snapshot. Returns the first sync error so a misconfigured state-dir
// surfaces before the HTTP server starts.
func (s *State) SyncAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.syncFeedEnvLocked(); err != nil {
		return err
	}
	if err := s.syncIdentityLocked(); err != nil {
		return err
	}
	if err := s.syncAircraftJSONLocked(); err != nil {
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
	if err := s.syncFeedEnvLocked(); err != nil {
		return nil, err
	}
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
	return s.syncClaimSecretLocked()
}

// ClaimSecret returns the materialised secret (empty until
// RegisterClaim is called). Tests use this; the running server
// reads the file via identity.Reader, not via this method.
func (s *State) ClaimSecret() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claim
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
	return s.syncAircraftJSONLocked()
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

func (s *State) syncFeedEnvLocked() error {
	keys := make([]string, 0, len(s.feedEnv))
	for k := range s.feedEnv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		// feed.env is bash-sourced in production; values without
		// shell-meaningful chars don't need quoting. The dev seed
		// values are all simple — wrapped in double quotes for
		// the few that contain spaces (none today, but cheap).
		v := s.feedEnv[k]
		if strings.ContainsAny(v, " \t\"'$`\\") {
			b.WriteString(k)
			b.WriteString("=\"")
			b.WriteString(strings.ReplaceAll(v, "\"", "\\\""))
			b.WriteString("\"\n")
			continue
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(v)
		b.WriteByte('\n')
	}
	return writeAtomic(s.Paths.FeedEnv, []byte(b.String()), 0o644)
}

func (s *State) syncIdentityLocked() error {
	if err := writeAtomic(s.Paths.FeederID, []byte("dev-feeder-01\n"), 0o644); err != nil {
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
		"version":     "dev",
		"kind":        "dev",
		"commit_sha":  "0000000000000000000000000000000000000000",
		"build_date":  s.feedStart.UTC().Format(time.RFC3339),
		"arches":      []string{"dev"},
	}
	b, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeAtomic(s.Paths.Manifest, b, 0o644)
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
	return writeState(s.Paths.Dump978FAState, dump978State, dump978Reason)
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
