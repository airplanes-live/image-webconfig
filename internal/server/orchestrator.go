package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Update-orchestrator surfaces — both shipped by airplanes-live/image
// (pi-gen stage 06d for the trampoline, the runtime overlay for the
// orchestrator binary). webconfig only sees them as opaque executables.
const (
	// orchestratorTrampolinePath is the stable image-owned launcher
	// that the sudoers grant pins to. The trampoline execs into the
	// orchestrator binary inside the currently-active runtime release,
	// so the absolute path here doesn't change when the overlay flips.
	orchestratorTrampolinePath = "/usr/local/lib/airplanes-webconfig/start-orchestrator.sh"
	// orchestratorTargetPath is the overlay-owned binary the trampoline
	// will exec. The path resolves through /opt/airplanes-runtime/current,
	// so an overlay flip is observed atomically.
	orchestratorTargetPath = "/opt/airplanes-runtime/current/lib/airplanes-update-orchestrator"
)

// DefaultOrchestratorStatePath is where the orchestrator atomically
// writes its current-step JSON. /run is tmpfs, so the file disappears
// across reboots — matching the orchestrator's semantics: a half-run
// upgrade does not survive a reboot.
const DefaultOrchestratorStatePath = "/run/airplanes/orchestrator.state"

// orchestratorStartTimeout caps the wall-clock budget for the
// systemd-run invocation. The transient unit is detached (--collect),
// so systemd-run returns once the unit has been queued — a few hundred
// milliseconds in practice. 5s gives headroom for a slow systemd bus
// without papering over a wedged systemd.
const orchestratorStartTimeout = 5 * time.Second

// Step constants the JSON state file (or this handler) can carry. The
// orchestrator writes free-form step names; "idle", "unavailable",
// "done", and "failed" are the cases webconfig itself synthesizes or
// matches against.
const (
	orchestratorStepIdle        = "idle"
	orchestratorStepUnavailable = "unavailable"
	orchestratorStepDone        = "done"
	orchestratorStepFailed      = "failed"
)

// defaultOrchestratorCapable reports whether both the image-owned
// trampoline and the overlay-owned orchestrator binary are present and
// executable. Used as the production gate; the in-memory test override
// lives in Deps.OrchestratorCapable.
//
// unix.Access maps to Linux access(2), which checks the real UID/GID
// (NOT the effective creds). The airplanes-webconfig service runs
// without privilege changes, so real and effective are the same here
// and the result reflects what the airplanes-webconfig account can
// actually execute. The two checks together avoid two false positives:
// a missing file and a file present but unexecutable by the service
// account. The privilege boundary itself is still enforced by sudoers
// and by the fact that both paths must be root-owned and non-writable
// by the airplanes-webconfig account — capability here is UX-only.
func defaultOrchestratorCapable() bool {
	if unix.Access(orchestratorTrampolinePath, unix.X_OK) != nil {
		return false
	}
	if unix.Access(orchestratorTargetPath, unix.X_OK) != nil {
		return false
	}
	return true
}

// The on-disk JSON envelope the orchestrator writes to
// /run/airplanes/orchestrator.state has fields {step, status,
// started_at, finished_at, error, apt_irreversible}. The handler does
// NOT round-trip the parsed struct — it returns the file bytes as-is
// so future fields the orchestrator adds reach the SPA without a
// webconfig release. Only `step` is read here, by orchestratorRunning,
// to decide whether a prior run is still in flight.

// orchestratorRunning reports whether the on-disk state file describes
// an in-flight run. A terminal `step` (`done` or `failed`) counts as
// not-running; everything else (including a parse error or an unknown
// step name) is treated as running so a click can't kick a second
// orchestrator while one is still flushing its state file.
//
// Returns (running, parseErr). A parseErr is reported by readState so
// the start path can be told apart from the state-read path (the latter
// returns 500 on a corrupt file; the former is more conservative and
// blocks the start because we cannot prove no run is in progress).
func orchestratorRunning(path string) (bool, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return true, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		// Empty state file is treated as running. The orchestrator
		// writes atomically (tmp + rename) so an empty file means
		// something is mid-write; refusing the start is the safe
		// answer.
		return true, errors.New("orchestrator state file is empty")
	}
	var s struct {
		Step string `json:"step"`
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return true, err
	}
	switch s.Step {
	case orchestratorStepDone, orchestratorStepFailed, orchestratorStepIdle, "":
		return false, nil
	default:
		return true, nil
	}
}

// handleOrchestratorStart is POST /api/orchestrator/start.
//
// Capability-gated (503 if trampoline / target missing). 409 if a prior
// orchestrator run is still flushing state. Otherwise invokes the
// sudoers-pinned systemd-run argv via the existing privileged-exec
// path; the transient unit is --collect, so systemd-run returns as soon
// as the unit is queued and the orchestrator continues detached. The
// SPA polls /api/orchestrator/state for progress and a terminal step.
//
// The wire envelope on success is `{"status":"running","unit":...,
// "started_at":...}` — identical to /api/update so the SPA's
// transient-unit response shape is uniform across update flows.
func (s *Server) handleOrchestratorStart(w http.ResponseWriter, r *http.Request) {
	if !s.orchestratorCapableFunc() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"reason": "orchestrator_unavailable"})
		return
	}

	// Serialize the running-check + systemd-run kickoff against itself
	// and against the other transient-update endpoints. A concurrent
	// click cannot observe an idle state and then both kick off — by
	// the time the second contender acquires the lock, the first's
	// transient unit is queued and the state-file check sees it.
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()

	// Refuse if any other maintenance unit is active (apt upgrade /
	// feed update / orchestrator). All three touch dpkg or release
	// artefacts and would deadlock or corrupt state if they overlapped. The orchestrator itself appears in
	// maintenanceUnits so a second start-during-orchestrator click hits
	// this guard before the state-file probe runs.
	if busy := s.maintenanceUnitActive(r.Context()); busy != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"reason": "already_running"})
		return
	}

	running, err := orchestratorRunning(s.orchestratorStatePath)
	if err != nil {
		log.Printf("orchestrator start: state-file probe: %v", err)
	}
	if running {
		writeJSON(w, http.StatusConflict, map[string]string{"reason": "already_running"})
		return
	}

	cctx, cancel := context.WithTimeout(r.Context(), orchestratorStartTimeout)
	defer cancel()
	res, runErr := s.runner(cctx, s.priv.StartOrchestrator)
	if runErr != nil {
		stderr := strings.TrimSpace(string(res.Stderr))
		log.Printf("orchestrator start: %v stderr=%q", runErr, stderr)
		// systemd-run prints "already exists" / "already running" on
		// the rare race where two starts make it past the state-file
		// guard simultaneously (or the unit name lingers despite
		// --collect). Map to 409 so the SPA's "already running" copy
		// fires.
		if strings.Contains(stderr, "already exists") || strings.Contains(stderr, "already running") {
			writeJSON(w, http.StatusConflict, map[string]string{"reason": "already_running"})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "orchestrator start failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":     "running",
		"unit":       "airplanes-update-orchestrator.service",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// handleOrchestratorState is GET /api/orchestrator/state.
//
// The endpoint is informational — the UI uses it both to decide whether
// to render the unified "Update System" button (orchestrator capable =
// yes) and to render progress while a run is in flight. So the
// capability-failure response is a 200 with `{"step":"unavailable"}`,
// not a 503; only POST /api/orchestrator/start hard-refuses on
// capability failure.
//
// Missing state file → `{"step":"idle"}` (capable but no run yet on
// this boot — /run is tmpfs so this is the normal post-boot state).
// Parse error → 500 with `{"reason":"state_corrupt"}`.
//
// The file body is forwarded verbatim on the happy path so future
// fields the orchestrator adds reach the SPA without a webconfig
// release.
func (s *Server) handleOrchestratorState(w http.ResponseWriter, _ *http.Request) {
	if !s.orchestratorCapableFunc() {
		writeJSON(w, http.StatusOK, map[string]string{"step": orchestratorStepUnavailable})
		return
	}
	body, err := os.ReadFile(s.orchestratorStatePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusOK, map[string]string{"step": orchestratorStepIdle})
			return
		}
		log.Printf("orchestrator state: read: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"reason": "state_corrupt"})
		return
	}
	// Verify the file parses as JSON before forwarding. An empty file
	// or a half-written one would surface to the SPA as a parse error
	// in the client; surfacing as 500/state_corrupt here keeps the
	// failure mode consistent with read errors.
	var probe json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		log.Printf("orchestrator state: parse: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"reason": "state_corrupt"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
