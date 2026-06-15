package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestReadUpgradeState_Cases exhaustively covers the readUpgradeState
// branches. The handler is trivial — it forwards the result into a
// JSON envelope — so these unit cases are the load-bearing coverage
// for the state machine's HTTP surface.
func TestReadUpgradeState_Cases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")

	cases := []struct {
		name     string
		contents string
		write    bool // false → leave the file missing
		want     string
	}{
		{"clean", "clean\n", true, "clean"},
		{"in-progress", "in-progress\n", true, "in-progress"},
		{"failed", "failed\n", true, "failed"},
		{"clean_no_newline", "clean", true, "clean"},
		{"clean_with_trailing_whitespace", "clean   \n", true, "clean"},
		{"clean_with_leading_whitespace", "  clean\n", true, "clean"},
		{"empty", "", true, "unknown"},
		{"whitespace_only", "   \n", true, "unknown"},
		{"unknown_token", "garbage\n", true, "unknown"},
		{"trailing_garbage_on_first_line", "clean garbage\n", true, "unknown"},
		{"multi_line_first_wins", "clean\nfailed\n", true, "clean"},
		{"missing_file", "", false, "unknown"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var path string
			if tc.write {
				path = filepath.Join(t.TempDir(), "upgrade-state")
				if err := os.WriteFile(path, []byte(tc.contents), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				path = missing
			}
			got := readUpgradeState(path)
			if got != tc.want {
				t.Errorf("readUpgradeState(%q contents=%q) = %q; want %q",
					path, tc.contents, got, tc.want)
			}
		})
	}
}

// TestUpgradeStatus_RequiresAuth covers the requireSession wiring.
func TestUpgradeStatus_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r, err := c.Get(ts.URL + "/api/status/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

// TestUpgradeStatus_AuthedReturnsState walks the three on-disk states
// through the live HTTP route. Each case writes the marker file the
// handler reads (via Deps.UpgradeStatePath injection in the harness)
// and asserts the JSON envelope shape the SPA depends on.
func TestUpgradeStatus_AuthedReturnsState(t *testing.T) {
	t.Parallel()
	cases := []struct {
		contents string
		want     string
	}{
		{"clean\n", "clean"},
		{"in-progress\n", "in-progress"},
		{"failed\n", "failed"},
		{"garbage\n", "unknown"},
		{"", "unknown"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want+"_"+tc.contents, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "upgrade-state")
			if err := os.WriteFile(path, []byte(tc.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			h := newWriteHarness(t, withUpgradeStatePath(path))
			r, err := h.client.Get(h.ts.URL + "/api/status/upgrade")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			if r.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", r.StatusCode)
			}
			var got map[string]string
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			if got["state"] != tc.want {
				t.Errorf("state = %q; want %q", got["state"], tc.want)
			}
			// Envelope is exactly {"state": ...} — no extra keys.
			if len(got) != 1 {
				t.Errorf("body has %d keys, want 1; body = %#v", len(got), got)
			}
		})
	}
}

// TestUpgradeStatus_MissingFileReturnsUnknown covers the production
// default-path case: a freshly-flashed feeder where the helper has
// never written a marker. The endpoint must answer "unknown" rather
// than 5xx, so the SPA can render "no upgrade activity" without retrying.
func TestUpgradeStatus_MissingFileReturnsUnknown(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "upgrade-state-does-not-exist")
	h := newWriteHarness(t, withUpgradeStatePath(missing))
	r, err := h.client.Get(h.ts.URL + "/api/status/upgrade")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	var got map[string]string
	if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if got["state"] != "unknown" {
		t.Errorf("state = %q; want unknown", got["state"])
	}
}
