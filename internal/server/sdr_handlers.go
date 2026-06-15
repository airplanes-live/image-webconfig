package server

import (
	"net/http"

	"github.com/airplanes-live/image-webconfig/internal/sdr"
)

// handleSDRList returns the RTL-SDR devices currently visible in sysfs.
// Unprivileged read — sysfs attribute reads need no sudo and do not
// disturb a device readsb or dump978-fa holds open. The SPA uses this to
// offer detected serials in the SDR-pinning pickers; enumeration failure
// (or no hardware) is an empty list, never an error, so the config form
// stays usable without it.
func (s *Server) handleSDRList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": sdr.List(s.sdrSysfsRoot),
	})
}
