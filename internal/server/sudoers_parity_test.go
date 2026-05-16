package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePrivilegedArgvParity_PassesAgainstInTreeFiles(t *testing.T) {
	t.Parallel()
	if err := ValidatePrivilegedArgvParity(
		DefaultPrivilegedArgv(),
		filepath.Join("..", "..", "files", "etc", "sudoers.d", "010_airplanes-webconfig"),
		filepath.Join("..", "..", "files", "etc", "sudoers.d", "011_airplanes-webconfig-update"),
	); err != nil {
		t.Fatalf("parity check failed against in-tree sudoers: %v", err)
	}
}

func TestValidatePrivilegedArgvParity_FailsOnMissingEntry(t *testing.T) {
	t.Parallel()
	// Synthesize a sudoers file that authorizes only the ApplyFeed argv;
	// every other argv must be reported as missing.
	dir := t.TempDir()
	path := filepath.Join(dir, "010_partial")
	priv := DefaultPrivilegedArgv()
	tail := strings.Join(priv.ApplyFeed[2:], " ")
	body := "airplanes-webconfig ALL=(root) NOPASSWD: " + tail + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ValidatePrivilegedArgvParity(priv, path)
	if err == nil {
		t.Fatal("expected error from partial sudoers, got nil")
	}
	msg := err.Error()
	// Must surface at least one missing entry by name so the diagnostic
	// helps an operator triage cross-version drift on a feeder.
	for _, want := range []string{"Reboot", "StartUpdate", "WifiList"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}

func TestValidatePrivilegedArgvParity_FailsOnMissingFile(t *testing.T) {
	t.Parallel()
	err := ValidatePrivilegedArgvParity(DefaultPrivilegedArgv(), "/nonexistent/sudoers.d/010")
	if err == nil {
		t.Fatal("expected error reading missing file, got nil")
	}
}

func TestValidatePrivilegedArgvParity_RejectsArgvWithoutSudoPrefix(t *testing.T) {
	t.Parallel()
	priv := DefaultPrivilegedArgv()
	priv.Reboot = []string{"/usr/bin/systemctl", "reboot"} // missing /usr/bin/sudo -n

	dir := t.TempDir()
	path := filepath.Join(dir, "010_full")
	// Include every other argv as authorized; the only failure should be
	// the malformed Reboot.
	body := strings.Builder{}
	for _, tc := range privilegedArgvCases(priv) {
		if tc.label == "Reboot" {
			continue
		}
		body.WriteString("airplanes-webconfig ALL=(root) NOPASSWD: " + strings.Join(tc.argv[2:], " ") + "\n")
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	err := ValidatePrivilegedArgvParity(priv, path)
	if err == nil || !strings.Contains(err.Error(), "Reboot") || !strings.Contains(err.Error(), "/usr/bin/sudo") {
		t.Fatalf("expected error mentioning Reboot + /usr/bin/sudo, got %v", err)
	}
}
