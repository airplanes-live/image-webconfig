package status

import (
	"strings"
	"testing"
)

// TestFHSDefaultRunPaths locks the consolidated /run/airplanes/* daemon-state
// defaults. DefaultPaths is otherwise unverified (the live tests inject
// t.TempDir paths), so a silent edit would make the status reader look in the
// wrong place on a booted feeder. The readsb paths stay under /run/readsb
// (upstream) and are intentionally not asserted here.
func TestFHSDefaultRunPaths(t *testing.T) {
	p := DefaultPaths()
	got := map[string]string{
		"MlatStateFile":      p.MlatStateFile,
		"FeedStateFile":      p.FeedStateFile,
		"UAT978StateFile":    p.UAT978StateFile,
		"Dump978FAStateFile": p.Dump978FAStateFile,
	}
	want := map[string]string{
		"MlatStateFile":      "/run/airplanes/mlat/state",
		"FeedStateFile":      "/run/airplanes/feed/state",
		"UAT978StateFile":    "/run/airplanes/978/state",
		"Dump978FAStateFile": "/run/airplanes/dump978-fa/state",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
		if !strings.HasPrefix(got[k], "/run/airplanes/") {
			t.Errorf("%s = %q, want prefix /run/airplanes/", k, got[k])
		}
	}
}
