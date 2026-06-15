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
// To match the real CLI's wire shape (one entry per readable key), every
// known readable key absent from values is emitted as an explicit null —
// not omitted — so tests exercise the same envelope production sees. An
// empty string value stays an empty string on the wire (explicitly-empty).
// Present keys outside the known readable set are still emitted, letting
// tests simulate a newer feed CLI with extra readable keys.
func Output(values map[string]string) []byte {
	wire := make(map[string]any, len(feedenv.APIReadKeys)+1+len(values))
	for _, k := range append(append([]string{}, feedenv.APIReadKeys...), "APL_FEED_WEBSITE_URL") {
		wire[k] = nil
	}
	for k, v := range values {
		wire[k] = v
	}
	b, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"values":         wire,
	})
	if err != nil {
		panic(err) // string/nil map values cannot fail to marshal
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
