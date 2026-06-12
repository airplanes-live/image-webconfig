// Package feedenvtest builds feedenv.Readers backed by a canned
// `apl-feed config show --json` envelope, for tests that need a
// deterministic feeder configuration without exec'ing the real CLI.
package feedenvtest

import (
	"context"
	"encoding/json"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
)

// Output marshals the config-show envelope for the given present keys.
// Absent keys are simply omitted — the reader treats an omitted entry
// and an explicit null identically (key not in feed.env). An empty
// string value stays an empty string on the wire (explicitly-empty).
func Output(values map[string]string) []byte {
	if values == nil {
		values = map[string]string{} // nil would marshal to a null values object
	}
	b, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"values":         values,
	})
	if err != nil {
		panic(err) // map[string]string cannot fail to marshal
	}
	return b
}

// ReaderFunc returns a feedenv.Reader whose fake exec consults values()
// on every read, so a test can change the simulated configuration
// between requests. A non-nil error from values() surfaces as an exec
// failure (non-zero exit, error text on stderr).
func ReaderFunc(values func() (map[string]string, error)) *feedenv.Reader {
	return &feedenv.Reader{
		Exec: func(_ context.Context, _ []string) (wexec.Result, error) {
			vals, err := values()
			if err != nil {
				return wexec.Result{Stderr: []byte(err.Error()), ExitCode: 1}, err
			}
			return wexec.Result{Stdout: Output(vals)}, nil
		},
		Argv: []string{"fake", "apl-feed", "config", "show", "--json"},
	}
}

// Reader returns a feedenv.Reader serving a fixed value set. The map is
// read on every exec, so a test that mutates it (between requests, not
// concurrently with one) sees the change on the next read.
func Reader(values map[string]string) *feedenv.Reader {
	return ReaderFunc(func() (map[string]string, error) { return values, nil })
}
