package server

import (
	"bufio"
	"net/http"
	"os"
	"strings"
)

// DefaultUpgradeStatePath is where the self-update helper writes the
// upgrade-state marker. /var/lib/airplanes-webconfig-upgrade/ is a new,
// root-owned parent — intentionally NOT /var/lib/airplanes-webconfig/,
// which is mode 0700 owned by the unprivileged service account and would
// let the account unlink files there regardless of file ownership.
const DefaultUpgradeStatePath = "/var/lib/airplanes-webconfig-upgrade/upgrade-state"

// readUpgradeState returns one of "clean", "in-progress", "failed",
// "unknown". "unknown" covers every non-success case the caller cannot
// distinguish operationally: missing file, empty file, whitespace-only,
// unparseable token, read error. The SPA renders "unknown" as "no
// upgrade activity" — operators are not asked to triage a missing file.
func readUpgradeState(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return "unknown"
	}
	switch strings.TrimSpace(sc.Text()) {
	case "clean":
		return "clean"
	case "in-progress":
		return "in-progress"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

// handleUpgradeStatus serves GET /api/status/upgrade. The SPA polls this
// after POST /api/webconfig-update so it can render "rolling back" or
// "wedged — manual recovery required" without grepping the SSE log.
//
// /health intentionally stays plain-text `ok <version>` — the SPA
// captures it as raw text and treats any version change as "upgrade
// applied", so JSON-ifying /health would misreport a rolled-back-with-
// failed-marker device as a successful upgrade (the binary's version
// byte-changes after a partial extract). Upgrade state belongs on a
// dedicated status endpoint, not a health probe.
func (s *Server) handleUpgradeStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"state": readUpgradeState(s.upgradeStatePath),
	})
}
