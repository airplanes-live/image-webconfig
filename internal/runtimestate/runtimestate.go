// Package runtimestate parses the env-style state files that the
// airplanes-feed and airplanes-mlat daemons publish to /run/<service>/state.
//
// Format mirrors scripts/lib/state-writer.sh in the feed repo:
//   - first line is "schema_version=1"
//   - subsequent lines are "KEY=VALUE" with key matching [A-Za-z_][A-Za-z0-9_]*
//   - values are not shell-quoted; readers must NOT `source` the file
//
// This package is symmetric with scripts/lib/state-reader.sh in feed.
// Round-trip tests against the writer's output keep the contracts in sync.
package runtimestate

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

// ErrUnknownSchema is returned when the file's schema_version line is
// absent or not "1". Callers should treat this as "decision unknown" and
// fall through to systemd-only rendering.
var ErrUnknownSchema = errors.New("runtimestate: unknown schema version")

var keyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// State holds the parsed contents of a /run/<service>/state file.
type State struct {
	SchemaVersion int
	Values        map[string]string
}

// AllowedDecisions lists the daemon-decision tokens consumers can rely
// on. Unknown tokens (e.g. from a future schema_version) should be
// treated as "decision unknown" — callers must not surface them to UI.
var AllowedDecisions = map[string]bool{
	"enabled":       true,
	"disabled":      true,
	"misconfigured": true,
}

// Reason vocabularies are owner-specific so a malformed state file from
// one daemon cannot pass through with another daemon's reason token.
// Callers select the right map per the daemon they're reading. Adding a
// new reason in the bash wrappers requires adding it here in the same
// PR.

// AllowedReasonsMLAT mirrors airplanes-mlat.sh's _mlat_classify output.
var AllowedReasonsMLAT = map[string]bool{
	"ok":                 true,
	"mlat_enabled_false": true,
	"latitude_zero":      true,
	"longitude_zero":     true,
	"mlat_user_empty":    true,
}

// AllowedReasonsFeed mirrors airplanes-feed.sh — currently only "ok"
// (the daemon has no disable/misconfig path).
var AllowedReasonsFeed = map[string]bool{
	"ok": true,
}

// AllowedReasons978 mirrors airplanes-978.sh's _978_classify output.
var AllowedReasons978 = map[string]bool{
	"ok":                true,
	"uat_disabled":      true,
	"uat_input_invalid": true,
}

// Read parses a runtime state file. Returns ErrUnknownSchema if the
// schema_version line is missing or not "1". Returns os.ErrNotExist
// (wrapped) for a missing file. Other errors propagate as-is.
//
// Uses os.Lstat so symlinked targets are rejected with "not a regular
// file" — defensive against accidental fs surface; the writer never
// emits via a symlink.
func Read(path string) (*State, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, &fs.PathError{Op: "stat", Path: path, Err: errors.New("not a regular file")}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	s := &State{Values: map[string]string{}}
	sc := bufio.NewScanner(f)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if line != "schema_version=1" {
				return nil, ErrUnknownSchema
			}
			s.SchemaVersion = 1
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			// Skip malformed lines (no `=` or empty key) without
			// failing the whole read — defense against future writers
			// that add comments or blank lines.
			continue
		}
		key := line[:eq]
		if !keyRE.MatchString(key) {
			continue
		}
		value := line[eq+1:]
		if strings.ContainsRune(value, '\r') {
			// Defensive: writer never emits CR; if the file has been
			// tampered with, drop the offending key rather than confuse
			// the caller.
			continue
		}
		s.Values[key] = value
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.SchemaVersion != 1 {
		// File was empty or read no lines.
		return nil, ErrUnknownSchema
	}
	return s, nil
}
