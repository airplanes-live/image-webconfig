package server

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// DefaultSudoersPaths returns the on-disk paths of the sudoers files the
// release tarball lays down. Used by ValidatePrivilegedArgvParity when the
// caller wants the runtime check against an installed image.
func DefaultSudoersPaths() []string {
	return []string{
		"/etc/sudoers.d/010_airplanes-webconfig",
	}
}

// LoadSudoersCommands parses sudoers files and returns the set of NOPASSWD
// command-specs they authorize, keyed by the command string after the colon.
// Comments and lines without NOPASSWD: are skipped; whitespace in the command
// is normalised to single spaces so the comparison ignores sudoers' tolerance
// for extra spacing.
func LoadSudoersCommands(paths ...string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read sudoers %s: %w", p, err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			idx := strings.Index(line, "NOPASSWD:")
			if idx < 0 {
				continue
			}
			cmd := strings.TrimSpace(line[idx+len("NOPASSWD:"):])
			cmd = strings.Join(strings.Fields(cmd), " ")
			out[cmd] = struct{}{}
		}
	}
	return out, nil
}

// privilegedArgvCase pairs a PrivilegedArgv field name with its argv. Used by
// ValidatePrivilegedArgvParity to enumerate every sudo'd shape.
type privilegedArgvCase struct {
	label string
	argv  []string
}

// privilegedArgvCases enumerates every sudo'd shape the binary uses. The
// non-sudo SchemaFeed is intentionally excluded — read-only paths don't need
// a sudoers grant.
func privilegedArgvCases(priv PrivilegedArgv) []privilegedArgvCase {
	return []privilegedArgvCase{
		{"ApplyFeed", priv.ApplyFeed},
		{"Reboot", priv.Reboot},
		{"Poweroff", priv.Poweroff},
		{"StartSystemUpgrade", priv.StartSystemUpgrade},
		{"StartOrchestrator", priv.StartOrchestrator},
		{"RegisterClaim", priv.RegisterClaim},
		{"WifiList", priv.WifiList},
		{"WifiAdd", priv.WifiAdd},
		{"WifiUpdate", priv.WifiUpdate},
		{"WifiDelete", priv.WifiDelete},
		{"WifiTest", priv.WifiTest},
		{"WifiActivate", priv.WifiActivate},
		{"WifiStatus", priv.WifiStatus},
		{"ExportIdentity", priv.ExportIdentity},
		{"ImportIdentity", priv.ImportIdentity},
	}
}

// ValidatePrivilegedArgvParity asserts the set of sudo-prefixed argv shapes
// in priv equals the set of NOPASSWD lines in the supplied sudoers files —
// both directions. Exact match — not substring — because sudo authorizes
// by full command line, so an entry allowing `apl-feed apply --json` would
// NOT authorize `apl-feed apply --json --extra` even though a substring
// match would say it did.
//
// "Missing" entries (argv shape with no matching sudoers line) are the
// usual case: a new binary calls something the deployed sudoers files don't
// permit; the sudo call fails at runtime with an unhelpful diagnostic.
//
// "Extra" entries (sudoers line with no matching argv shape) are the
// security-relevant case: a deprecated argv shape was removed from the
// binary but its NOPASSWD line still lives on the device. The grant is
// dead code that the running binary doesn't exercise; an attacker who
// pivots into the airplanes-webconfig service account inherits it
// unnecessarily.
//
// Returns nil on parity, an error otherwise. The error message lists
// missing and extra entries separately so an operator can act on the
// right side.
//
// Used in two places:
//   - The unit test TestDefaultPrivilegedArgv_SudoersParity (against the
//     in-tree files/etc/sudoers.d/* — catches build-time drift).
//   - The `airplanes-webconfig --validate-sudoers` runtime subcommand
//     (against /etc/sudoers.d/010_* — catches drift between an upgraded
//     binary and the not-yet-replaced sudoers file, i.e. the cross-version
//     case the unit test cannot reach).
func ValidatePrivilegedArgvParity(priv PrivilegedArgv, sudoersPaths ...string) error {
	commands, err := LoadSudoersCommands(sudoersPaths...)
	if err != nil {
		return err
	}

	cases := privilegedArgvCases(priv)
	expected := make(map[string]struct{}, len(cases))
	var missing []string
	for _, tc := range cases {
		if len(tc.argv) < 3 || tc.argv[0] != "/usr/bin/sudo" || tc.argv[1] != "-n" {
			return fmt.Errorf("%s: argv must start with /usr/bin/sudo -n, got %v", tc.label, tc.argv)
		}
		tail := strings.Join(tc.argv[2:], " ")
		expected[tail] = struct{}{}
		if _, ok := commands[tail]; !ok {
			missing = append(missing, fmt.Sprintf("  %s: %q", tc.label, tail))
		}
	}

	var extras []string
	for cmd := range commands {
		if _, ok := expected[cmd]; !ok {
			extras = append(extras, fmt.Sprintf("  %q", cmd))
		}
	}
	sort.Strings(extras)

	if len(missing) == 0 && len(extras) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "sudoers parity check failed against %v", sudoersPaths)
	if len(missing) > 0 {
		fmt.Fprintf(&b, "\n%d argv shape(s) have no matching NOPASSWD line:\n%s", len(missing), strings.Join(missing, "\n"))
	}
	if len(extras) > 0 {
		fmt.Fprintf(&b, "\n%d NOPASSWD line(s) have no matching argv shape:\n%s", len(extras), strings.Join(extras, "\n"))
	}
	return errors.New(b.String())
}
