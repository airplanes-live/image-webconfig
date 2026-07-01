package server

import "testing"

// TestFHSDefaultPaths locks the on-device default paths the FHS install
// layout pins. These consts are otherwise unverified — the live tests inject
// temp paths — so a silent edit would ship a wrong path that only fails on a
// booted feeder (the orchestrator readiness gate, the sudoers-pinned
// trampoline, or the upgrade-state reader).
func TestFHSDefaultPaths(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"orchestratorTrampolinePath", orchestratorTrampolinePath, "/opt/airplanes/libexec/start-orchestrator.sh"},
		{"orchestratorTargetPath", orchestratorTargetPath, "/opt/airplanes/current/lib/airplanes-update-orchestrator"},
		{"DefaultUpgradeStatePath", DefaultUpgradeStatePath, "/var/lib/airplanes/webconfig-upgrade/upgrade-state"},
		{"DefaultOrchestratorStatePath", DefaultOrchestratorStatePath, "/run/airplanes/orchestrator.state"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}
