package server

import (
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
		"/etc/sudoers.d/011_airplanes-webconfig-update",
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
		{"StartUpdate", priv.StartUpdate},
		{"StartSystemUpgrade", priv.StartSystemUpgrade},
		{"StartWebconfigUpdate", priv.StartWebconfigUpdate},
		{"RegisterClaim", priv.RegisterClaim},
		{"WifiList", priv.WifiList},
		{"WifiAdd", priv.WifiAdd},
		{"WifiUpdate", priv.WifiUpdate},
		{"WifiDelete", priv.WifiDelete},
		{"WifiTest", priv.WifiTest},
		{"WifiActivate", priv.WifiActivate},
		{"WifiStatus", priv.WifiStatus},
	}
}

// ValidatePrivilegedArgvParity asserts every sudo-prefixed argv in priv has
// an exact NOPASSWD line in the supplied sudoers files. Exact match — not
// substring — because sudo authorizes by full command line, so an entry
// allowing `apl-feed apply --json` would NOT authorize `apl-feed apply
// --json --extra` even though a substring match would say it did.
//
// Returns nil on parity, an error otherwise. The error message lists every
// missing entry and (for diagnosis) the known commands present in the
// sudoers files.
//
// Used in two places:
//   - The unit test TestDefaultPrivilegedArgv_SudoersParity (against the
//     in-tree files/etc/sudoers.d/* — catches build-time drift).
//   - The `airplanes-webconfig --validate-sudoers` runtime subcommand
//     (against /etc/sudoers.d/010_* + /etc/sudoers.d/011_* — catches drift
//     between an upgraded binary and the not-yet-replaced sudoers files,
//     i.e. the cross-version case the unit test cannot reach).
func ValidatePrivilegedArgvParity(priv PrivilegedArgv, sudoersPaths ...string) error {
	commands, err := LoadSudoersCommands(sudoersPaths...)
	if err != nil {
		return err
	}

	var missing []string
	for _, tc := range privilegedArgvCases(priv) {
		if len(tc.argv) < 3 || tc.argv[0] != "/usr/bin/sudo" || tc.argv[1] != "-n" {
			return fmt.Errorf("%s: argv must start with /usr/bin/sudo -n, got %v", tc.label, tc.argv)
		}
		tail := strings.Join(tc.argv[2:], " ")
		if _, ok := commands[tail]; !ok {
			missing = append(missing, fmt.Sprintf("  %s: %q", tc.label, tail))
		}
	}

	if len(missing) == 0 {
		return nil
	}

	known := make([]string, 0, len(commands))
	for k := range commands {
		known = append(known, k)
	}
	sort.Strings(known)

	return fmt.Errorf("sudoers parity check failed — %d argv shapes have no matching NOPASSWD line:\n%s\nknown NOPASSWD commands in %v:\n  %s",
		len(missing),
		strings.Join(missing, "\n"),
		sudoersPaths,
		strings.Join(known, "\n  "))
}
