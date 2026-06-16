package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
)

// Combined device backup/restore.
//
// One downloadable file captures everything a feeder accumulates that a fresh
// flash would wipe: feeder identity, feed settings, third-party aggregator
// credentials, saved Wi-Fi networks (incl. PSKs), and the webconfig admin
// password hash. Each lives in its own section so a section can be added or
// versioned without disturbing the others; on restore an inapplicable section
// is skipped and reported rather than failing the whole operation.
//
// Restore STREAMS its result as newline-delimited JSON, one event per section
// as it completes, so the UI shows live progress instead of a dead spinner —
// the operation can take up to a couple of minutes (identity restart, Wi-Fi
// writes, several privileged helpers).
const (
	combinedBackupKind    = "airplanes-combined-backup"
	combinedBackupVersion = 1

	// combinedBackupBodyLimit caps an uploaded backup. The largest contributor
	// is the aggregator section (helper cap 64 KiB); 128 KiB is comfortable
	// headroom for every section combined. NOT readJSON's 1 KiB cap.
	combinedBackupBodyLimit = 128 * 1024

	// backupExportTimeout bounds the whole export (five privileged reads).
	backupExportTimeout = 60 * time.Second
	// backupRestoreBudget bounds the whole restore. Section work runs under a
	// context derived from this, NOT the request context, so a browser
	// disconnect mid-restore cannot kill a privileged helper mid-write.
	backupRestoreBudget = 120 * time.Second
	// backupSectionTimeout bounds one section's privileged call.
	backupSectionTimeout = 30 * time.Second
)

// combinedBackupEnvelope is the top-level file shape. Sections are kept as raw
// JSON and validated individually so the envelope decode stays tolerant of
// sections this binary doesn't know (forward compatibility) and of the verbatim
// identity/aggregator sub-envelopes (which carry their own extra fields).
type combinedBackupEnvelope struct {
	SchemaVersion int                        `json:"schema_version"`
	Kind          string                     `json:"kind"`
	CreatedAt     string                     `json:"created_at,omitempty"`
	Sections      map[string]json.RawMessage `json:"sections"`
}

type settingsSection struct {
	SchemaVersion int               `json:"schema_version"`
	Values        map[string]string `json:"values"`
}

type wifiSection struct {
	SchemaVersion int             `json:"schema_version"`
	Networks      json.RawMessage `json:"networks"`
}

type passwordSection struct {
	SchemaVersion int    `json:"schema_version"`
	PHC           string `json:"phc"`
}

// restoreEvent is one NDJSON line emitted during a restore. type "section" is a
// per-section outcome (applied | skipped | failed); type "summary" is the final
// line and carries password_changed so the SPA knows to send the user to login.
type restoreEvent struct {
	Type            string `json:"type"`
	Section         string `json:"section,omitempty"`
	Status          string `json:"status,omitempty"`
	Reason          string `json:"reason,omitempty"`
	PasswordChanged bool   `json:"password_changed,omitempty"`
}

// --- export ----------------------------------------------------------------

// handleBackupExport (POST /api/backup/export, authed) assembles the combined
// file. POST (not GET) so it routes through the origin check and stays out of
// browser history; the body holds every device secret. If any section's export
// errors (as opposed to being legitimately empty), the whole export fails with
// a 500 naming the section — a half-captured file must never masquerade as a
// complete backup.
func (s *Server) handleBackupExport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), backupExportTimeout)
	defer cancel()

	sections := make(map[string]json.RawMessage, 5)
	for _, sec := range []struct {
		name string
		fn   func(context.Context) (json.RawMessage, error)
	}{
		{"identity", s.exportIdentitySection},
		{"settings", s.exportSettingsSection},
		{"aggregators", s.exportAggregatorsSection},
		{"wifi", s.exportWifiSection},
		{"password", func(context.Context) (json.RawMessage, error) { return s.exportPasswordSection() }},
	} {
		raw, err := sec.fn(ctx)
		if err != nil {
			log.Printf("backup export: %s section: %v", sec.name, err)
			writeJSONError(w, http.StatusInternalServerError, "backup export failed ("+sec.name+")")
			return
		}
		sections[sec.name] = raw
	}

	env := combinedBackupEnvelope{
		SchemaVersion: combinedBackupVersion,
		Kind:          combinedBackupKind,
		CreatedAt:     s.now().UTC().Format(time.RFC3339),
		Sections:      sections,
	}
	body, err := json.Marshal(env)
	if err != nil {
		log.Printf("backup export: marshal: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "backup export failed")
		return
	}
	filename := fmt.Sprintf("airplanes-feeder-backup-%s.json", s.now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (s *Server) exportIdentitySection(ctx context.Context) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, backupSectionTimeout)
	defer cancel()
	res, err := s.runner(cctx, s.priv.ExportIdentity)
	if err != nil {
		return nil, fmt.Errorf("identity export: %w (stderr=%q)", err, strings.TrimSpace(string(res.Stderr)))
	}
	var probe identityBackupEnvelope
	if perr := json.Unmarshal(res.Stdout, &probe); perr != nil || probe.SchemaVersion != 1 {
		return nil, fmt.Errorf("identity export produced invalid payload (schema=%d err=%v)", probe.SchemaVersion, perr)
	}
	return json.RawMessage(res.Stdout), nil
}

func (s *Server) exportSettingsSection(ctx context.Context) (json.RawMessage, error) {
	values, err := s.feedEnv.ReadAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings read: %w", err)
	}
	return json.Marshal(settingsSection{SchemaVersion: 1, Values: values})
}

func (s *Server) exportAggregatorsSection(ctx context.Context) (json.RawMessage, error) {
	body, status, err := s.invokeAggregator(ctx, s.priv.AggregatorExport, nil, backupSectionTimeout)
	if err != nil {
		return nil, fmt.Errorf("aggregator export: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("aggregator export: http %d", status)
	}
	// invokeAggregator only checks the generic RPC envelope; confirm this is an
	// actual aggregator-backup payload before embedding it, so a contract
	// regression can't bury an unusable section in the file. {} aggregators is
	// the legitimate "nothing configured" case (present, non-empty raw).
	var probe struct {
		Kind          string          `json:"kind"`
		SchemaVersion int             `json:"schema_version"`
		Aggregators   json.RawMessage `json:"aggregators"`
	}
	if perr := json.Unmarshal(body, &probe); perr != nil ||
		probe.Kind != "aggregator-backup" || probe.SchemaVersion != 1 || len(probe.Aggregators) == 0 {
		return nil, fmt.Errorf("aggregator export: not a valid backup payload (kind=%q schema=%d)", probe.Kind, probe.SchemaVersion)
	}
	return json.RawMessage(body), nil
}

// exportWifiSection reshapes the helper's export envelope into the section the
// importer accepts: {schema_version, networks} only. The helper's reply also
// carries a "status" field, which apl-wifi import rejects as an unknown key.
// The stdout is secret-bearing (PSKs) and is never logged.
func (s *Server) exportWifiSection(ctx context.Context) (json.RawMessage, error) {
	cctx, cancel := context.WithTimeout(ctx, backupSectionTimeout)
	defer cancel()
	res, _ := s.stdinRunner(cctx, s.priv.WifiExport, bytes.NewReader(nil))
	var probe struct {
		Status        string          `json:"status"`
		SchemaVersion int             `json:"schema_version"`
		Networks      json.RawMessage `json:"networks"`
	}
	if perr := json.Unmarshal(res.Stdout, &probe); perr != nil || probe.Status != "ok" || probe.SchemaVersion != 1 {
		return nil, fmt.Errorf("wifi export: status=%q schema=%d (%d bytes)", probe.Status, probe.SchemaVersion, len(res.Stdout))
	}
	// networks must be present AND a JSON array. A truncated success like
	// {"status":"ok"} must NOT become a backup that silently lost every saved
	// network — that's the exact partial-by-omission trap the fail-loud
	// contract guards against. An empty [] is the legitimate-empty case.
	var nets []json.RawMessage
	if len(probe.Networks) == 0 || json.Unmarshal(probe.Networks, &nets) != nil {
		return nil, fmt.Errorf("wifi export: networks missing or not an array (%d bytes)", len(res.Stdout))
	}
	return json.Marshal(wifiSection{SchemaVersion: probe.SchemaVersion, Networks: probe.Networks})
}

func (s *Server) exportPasswordSection() (json.RawMessage, error) {
	s.store.Lock()
	phc, err := s.store.Read()
	s.store.Unlock()
	if err != nil {
		return nil, fmt.Errorf("password read: %w", err)
	}
	return json.Marshal(passwordSection{SchemaVersion: 1, PHC: phc})
}

// --- restore ---------------------------------------------------------------

// handleBackupRestore (POST /api/backup/restore, authed) restores onto an
// already-configured device. Sections are applied settings → identity →
// aggregators → wifi → password (password last so a failure earlier doesn't
// log the user out before the rest landed). When the password section applies
// it rotates all sessions; the streamed summary's password_changed tells the
// SPA to send the user to login with the restored password.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	env, ok := s.decodeCombinedBackup(w, r)
	if !ok {
		return
	}
	// Vet the password section before streaming so a bad hash is a clean 400.
	if raw, present := env.Sections["password"]; present {
		if _, err := parsePasswordSection(raw); err != nil {
			writeJSONError(w, http.StatusBadRequest, "password section invalid: "+err.Error())
			return
		}
	}

	enc, flush := beginRestoreStream(w)
	bgctx, cancel := context.WithTimeout(context.Background(), backupRestoreBudget)
	defer cancel()

	runAndEmit(enc, flush, "settings", func() (string, string) { return s.restoreSettings(bgctx, env) })
	runAndEmit(enc, flush, "identity", func() (string, string) { return s.restoreIdentity(bgctx, env) })
	runAndEmit(enc, flush, "aggregators", func() (string, string) { return s.restoreAggregators(bgctx, env) })
	runAndEmit(enc, flush, "wifi", func() (string, string) { return s.restoreWifi(bgctx, env) })

	passwordChanged := false
	if raw, present := env.Sections["password"]; present {
		status, reason := s.restorePasswordReplace(raw)
		emitSection(enc, flush, "password", status, reason)
		passwordChanged = status == "applied"
	} else {
		emitSection(enc, flush, "password", "skipped", "absent")
	}

	_ = enc.Encode(restoreEvent{Type: "summary", PasswordChanged: passwordChanged})
	flush()
}

// handleBackupRestoreSetup (POST /api/backup/restore-setup, PUBLIC) restores
// onto a fresh flash from the password-setup screen. It is gated identically to
// handleSetup (uninitialized state only) — the same trust window the existing
// setup endpoint already exposes. The password section is required: it is
// applied FIRST via store.Setup so the session cookie can be issued in the
// response headers before the body stream begins (auto-login), then the
// remaining sections stream in.
func (s *Server) handleBackupRestoreSetup(w http.ResponseWriter, r *http.Request) {
	if s.detectState() != stateUninitialized {
		writeJSONError(w, http.StatusConflict, "webconfig already initialized")
		return
	}
	env, ok := s.decodeCombinedBackup(w, r)
	if !ok {
		return
	}
	raw, present := env.Sections["password"]
	if !present {
		writeJSONError(w, http.StatusBadRequest, "backup has no password section; cannot complete setup")
		return
	}
	pw, err := parsePasswordSection(raw)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "password section invalid: "+err.Error())
		return
	}

	// Clear any stale persisted sessions BEFORE the irreversible store.Setup —
	// a revoke failure must abort while the device is still uninitialized, not
	// after the password file exists (which would gate this endpoint out and
	// strand a half-restored device).
	if err := s.sessions.RevokeAll(); err != nil {
		log.Printf("backup restore-setup: revoke: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "restore failed")
		return
	}
	// Password next: create-if-absent, then issue the session cookie while we
	// can still set response headers (before streaming starts).
	if err := s.store.Setup(pw.PHC); err != nil {
		if errors.Is(err, auth.ErrExists) {
			writeJSONError(w, http.StatusConflict, "webconfig already initialized")
			return
		}
		log.Printf("backup restore-setup: store: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "restore failed")
		return
	}
	token, expires, err := s.sessions.Issue()
	if err != nil {
		log.Printf("backup restore-setup: issue: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "restore failed")
		return
	}
	setSessionCookie(w, token, expires)

	enc, flush := beginRestoreStream(w)
	emitSection(enc, flush, "password", "applied", "")

	bgctx, cancel := context.WithTimeout(context.Background(), backupRestoreBudget)
	defer cancel()
	runAndEmit(enc, flush, "settings", func() (string, string) { return s.restoreSettings(bgctx, env) })
	runAndEmit(enc, flush, "identity", func() (string, string) { return s.restoreIdentity(bgctx, env) })
	runAndEmit(enc, flush, "aggregators", func() (string, string) { return s.restoreAggregators(bgctx, env) })
	runAndEmit(enc, flush, "wifi", func() (string, string) { return s.restoreWifi(bgctx, env) })

	_ = enc.Encode(restoreEvent{Type: "summary"})
	flush()
}

// decodeCombinedBackup reads and validates the envelope shell. It does NOT use
// DisallowUnknownFields (forward compatibility) and keeps sections raw so each
// is validated by its own restorer.
func (s *Server) decodeCombinedBackup(w http.ResponseWriter, r *http.Request) (combinedBackupEnvelope, bool) {
	var env combinedBackupEnvelope
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeJSONError(w, http.StatusBadRequest, errBadContentType.Error())
		return env, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, combinedBackupBodyLimit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&env); err != nil {
		writeJSONError(w, http.StatusBadRequest, "backup body invalid or too large")
		return env, false
	}
	if dec.More() {
		writeJSONError(w, http.StatusBadRequest, "backup body contains more than one JSON value")
		return env, false
	}
	if env.Kind != combinedBackupKind || env.SchemaVersion != combinedBackupVersion {
		writeJSONError(w, http.StatusBadRequest, "unrecognized backup file")
		return env, false
	}
	return env, true
}

// beginRestoreStream sets the NDJSON headers, extends the per-request write
// deadline past the server's 30s WriteTimeout (a full restore can run longer),
// and returns an encoder plus a flush func so each section reaches the browser
// the moment it completes.
func beginRestoreStream(w http.ResponseWriter) (*json.Encoder, func()) {
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Now().Add(backupRestoreBudget + 30*time.Second))
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	flush := func() { _ = rc.Flush() }
	flush()
	return enc, flush
}

func emitSection(enc *json.Encoder, flush func(), section, status, reason string) {
	_ = enc.Encode(restoreEvent{Type: "section", Section: section, Status: status, Reason: reason})
	flush()
}

// runAndEmit runs one section restorer and streams its outcome. Kept separate
// so each section's (status, reason) tuple lands on the wire the instant it
// finishes, giving the UI live per-section progress.
func runAndEmit(enc *json.Encoder, flush func(), section string, fn func() (string, string)) {
	status, reason := fn()
	emitSection(enc, flush, section, status, reason)
}

func parsePasswordSection(raw json.RawMessage) (passwordSection, error) {
	var pw passwordSection
	if err := json.Unmarshal(raw, &pw); err != nil {
		return pw, errors.New("malformed")
	}
	if pw.SchemaVersion != 1 {
		return pw, errors.New("unsupported schema_version")
	}
	if err := auth.ValidateStoredPHC(pw.PHC); err != nil {
		return pw, err
	}
	return pw, nil
}

func (s *Server) restoreSettings(ctx context.Context, env combinedBackupEnvelope) (string, string) {
	raw, ok := env.Sections["settings"]
	if !ok {
		return "skipped", "absent"
	}
	var sec settingsSection
	if err := json.Unmarshal(raw, &sec); err != nil || sec.SchemaVersion != 1 {
		return "failed", "malformed"
	}
	if s.schema == nil || s.schema.Degraded() {
		return "skipped", "schema_unavailable"
	}
	updates := make(map[string]string, len(sec.Values))
	for k, v := range sec.Values {
		if s.schema.IsWritable(k) {
			updates[k] = v
		}
	}
	cctx, cancel := context.WithTimeout(ctx, backupSectionTimeout)
	defer cancel()
	resp, status, err := s.applyConfigLocked(cctx, updates)
	if err != nil {
		log.Printf("backup restore settings: %v", err)
		return "failed", "apply error"
	}
	if status != http.StatusOK {
		synthesizeError(&resp)
		return "failed", resp.Error
	}
	if resp.Status == "applied" {
		s.triggerConfigSyncAsync()
	}
	return "applied", ""
}

func (s *Server) restoreIdentity(ctx context.Context, env combinedBackupEnvelope) (string, string) {
	raw, ok := env.Sections["identity"]
	if !ok {
		return "skipped", "absent"
	}
	var req identityBackupEnvelope
	if err := json.Unmarshal(raw, &req); err != nil || req.SchemaVersion != 1 {
		return "failed", "malformed"
	}
	if !isCanonicalUUID(req.FeederUUID) || !isCanonicalClaimSecret(req.Claim.Secret) {
		return "failed", "invalid identity"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "failed", "marshal"
	}
	cctx, cancel := context.WithTimeout(ctx, backupSectionTimeout)
	defer cancel()
	res, runErr := s.stdinRunner(cctx, s.priv.ImportIdentity, bytes.NewReader(body))
	if runErr != nil {
		log.Printf("backup restore identity: %v stderr=%q", runErr, strings.TrimSpace(string(res.Stderr)))
		return "failed", "import error"
	}
	return "applied", ""
}

func (s *Server) restoreAggregators(ctx context.Context, env combinedBackupEnvelope) (string, string) {
	raw, ok := env.Sections["aggregators"]
	if !ok {
		return "skipped", "absent"
	}
	cctx, cancel := context.WithTimeout(ctx, backupSectionTimeout)
	defer cancel()
	resp, status, err := s.invokeAggregator(cctx, s.priv.AggregatorImport, raw, backupSectionTimeout)
	if err != nil {
		log.Printf("backup restore aggregators: %v", err)
		return "failed", "import error"
	}
	if status == http.StatusOK || status == http.StatusAccepted {
		return "applied", ""
	}
	// The helper returns the same `rejected` code for an empty backup, an
	// unknown adapter, AND an adapter that is currently enabled. Only the last
	// is a normal, recoverable state worth skipping rather than failing; match
	// it on the message (per the helper's "<id> is enabled; disable it before
	// importing").
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(resp, &msg)
	if strings.Contains(msg.Message, "is enabled") {
		return "skipped", "an aggregator is currently enabled — disable it, then import aggregators separately"
	}
	if msg.Message != "" {
		return "failed", msg.Message
	}
	return "failed", fmt.Sprintf("import http %d", status)
}

// restoreWifi pipes the wifi section through apl-wifi import. The section's
// stdout/stdin carry PSKs and are never logged. The helper is non-disruptive:
// it writes keyfiles and skips the active connection, so this can run even when
// the restore request itself arrived over Wi-Fi.
func (s *Server) restoreWifi(ctx context.Context, env combinedBackupEnvelope) (string, string) {
	raw, ok := env.Sections["wifi"]
	if !ok {
		return "skipped", "absent"
	}
	cctx, cancel := context.WithTimeout(ctx, backupSectionTimeout)
	defer cancel()
	res, _ := s.stdinRunner(cctx, s.priv.WifiImport, bytes.NewReader(raw))
	var probe struct {
		Status string `json:"status"`
	}
	if perr := json.Unmarshal(res.Stdout, &probe); perr != nil {
		return "failed", "unparseable response"
	}
	switch probe.Status {
	case "applied":
		return "applied", ""
	case "rejected":
		return "failed", "rejected"
	default:
		return "failed", probe.Status
	}
}

// restorePasswordReplace applies the password section on a configured device:
// validate, revoke all sessions, THEN replace the hash (revoke-before-replace
// so a failed revoke can't leave old sessions valid against the new password).
// No fresh session is issued — the SPA re-authenticates with the restored
// password.
func (s *Server) restorePasswordReplace(raw json.RawMessage) (string, string) {
	pw, err := parsePasswordSection(raw)
	if err != nil {
		return "failed", err.Error()
	}
	if err := s.sessions.RevokeAll(); err != nil {
		log.Printf("backup restore password: revoke: %v", err)
		return "failed", "session rotation"
	}
	if err := s.store.Replace(pw.PHC); err != nil {
		log.Printf("backup restore password: replace: %v", err)
		return "failed", "store write"
	}
	return "applied", ""
}
