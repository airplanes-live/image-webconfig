package server

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"testing"
	"time"
)

// rfc3339UTCRe matches the strict shape `apl-feed apply --json` accepts
// for `edited_at`. Pin parity with feed/scripts/lib/feed-env-apply.sh
// (APL_FEED_APPLY_EDITED_AT_RE).
var rfc3339UTCRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$`)

// captureUpdates POSTs a /api/config payload and returns the raw
// `updates` map that apl-feed apply --json saw on stdin. Each value is
// either a JSON string (non-tracked / unchanged-stripped path doesn't
// occur — those keys are omitted) or a `{value, edited_at, edited_by}`
// object (tracked, changed). The caller decodes per-key.
func captureUpdates(t *testing.T, h *writeHarness, updates map[string]string) map[string]json.RawMessage {
	t.Helper()
	r := postJSON(t, h.client, h.ts.URL+"/api/config",
		map[string]any{"updates": updates})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	calls := h.stdinCallsCopy()
	if len(calls) != 1 {
		t.Fatalf("stdinCalls = %d, want 1", len(calls))
	}
	var parsed struct {
		Updates map[string]json.RawMessage `json:"updates"`
	}
	if err := json.Unmarshal(calls[0].stdin, &parsed); err != nil {
		t.Fatalf("decode stdin %q: %v", string(calls[0].stdin), err)
	}
	return parsed.Updates
}

// assertFieldUpdate decodes raw as the metadata object form and asserts
// the metadata shape: value matches wantValue, edited_by is "feeder",
// and edited_at matches the RFC 3339 UTC regex AND falls inside the
// [start, end] window. The window check rules out a static-hour bug
// without pinning time.Now via injection.
func assertFieldUpdate(t *testing.T, key string, raw json.RawMessage, wantValue string, start, end time.Time) {
	t.Helper()
	var fu struct {
		Value    string `json:"value"`
		EditedAt string `json:"edited_at"`
		EditedBy string `json:"edited_by"`
	}
	if err := json.Unmarshal(raw, &fu); err != nil {
		t.Fatalf("%s: not a metadata object — got %s", key, string(raw))
	}
	if fu.Value != wantValue {
		t.Errorf("%s.value = %q, want %q", key, fu.Value, wantValue)
	}
	if fu.EditedBy != "feeder" {
		t.Errorf("%s.edited_by = %q, want %q", key, fu.EditedBy, "feeder")
	}
	if !rfc3339UTCRe.MatchString(fu.EditedAt) {
		t.Errorf("%s.edited_at = %q, want RFC 3339 UTC (Z-suffixed)", key, fu.EditedAt)
	}
	stamped, err := time.Parse("2006-01-02T15:04:05Z", fu.EditedAt)
	if err != nil {
		t.Errorf("%s.edited_at parse: %v", key, err)
		return
	}
	if stamped.Before(start) || stamped.After(end) {
		t.Errorf("%s.edited_at = %v, want within [%v, %v]", key, stamped, start, end)
	}
}

func TestConfigPost_TrackedKeyChangedCarriesMetadata(t *testing.T) {
	// feed.env known state: MLAT_USER=alice. POST flips it to bob.
	h := newWriteHarness(t)
	if err := os.WriteFile(h.feedEnvPath,
		[]byte(`MLAT_USER=alice`+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	got := captureUpdates(t, h, map[string]string{"MLAT_USER": "bob"})
	end := time.Now().UTC().Add(time.Second)

	if len(got) != 1 {
		t.Fatalf("updates = %v, want 1 entry", got)
	}
	assertFieldUpdate(t, "MLAT_USER", got["MLAT_USER"], "bob", start, end)
}

func TestConfigPost_UnchangedTrackedKeyOmitted(t *testing.T) {
	// feed.env has MLAT_USER=alice. POST resends the same value plus a
	// non-tracked key. Captured stdin must NOT contain MLAT_USER, must
	// contain GAIN as a bare string.
	h := newWriteHarness(t)
	if err := os.WriteFile(h.feedEnvPath,
		[]byte(`MLAT_USER=alice`+"\n"+`GAIN=40`+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	got := captureUpdates(t, h, map[string]string{
		"MLAT_USER": "alice",
		"GAIN":      "42",
	})
	if _, ok := got["MLAT_USER"]; ok {
		t.Errorf("MLAT_USER should be omitted (unchanged tracked key); got %s", string(got["MLAT_USER"]))
	}
	if got["GAIN"] == nil {
		t.Fatal("GAIN missing from updates")
	}
	if string(got["GAIN"]) != `"42"` {
		t.Errorf("GAIN wire shape = %s, want bare string %q", string(got["GAIN"]), `"42"`)
	}
}

func TestConfigPost_NonTrackedKeyPassesThroughAsString(t *testing.T) {
	h := newWriteHarness(t)
	if err := os.WriteFile(h.feedEnvPath, []byte(`GAIN=40`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := captureUpdates(t, h, map[string]string{"GAIN": "42"})
	if got["GAIN"] == nil {
		t.Fatal("GAIN missing")
	}
	if string(got["GAIN"]) != `"42"` {
		t.Errorf("GAIN = %s, want bare string %q", string(got["GAIN"]), `"42"`)
	}
}

func TestConfigPost_BootstrapTrackedKeyHasMetadata(t *testing.T) {
	// feed.env is missing the tracked key entirely. The POST is treated
	// as a bootstrap write — metadata attached.
	h := newWriteHarness(t)
	if err := os.WriteFile(h.feedEnvPath, []byte(``), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	got := captureUpdates(t, h, map[string]string{"ALTITUDE": "120m"})
	end := time.Now().UTC().Add(time.Second)
	assertFieldUpdate(t, "ALTITUDE", got["ALTITUDE"], "120m", start, end)
}

func TestConfigPost_MixedChangeUnchangedPassthrough(t *testing.T) {
	// Mirror the "user edits one MLAT field, form posts every visible
	// field" scenario end-to-end through the HTTP path.
	h := newWriteHarness(t)
	if err := os.WriteFile(h.feedEnvPath, []byte(
		`LATITUDE=47.0`+"\n"+
			`LONGITUDE=8.0`+"\n"+
			`ALTITUDE=120m`+"\n"+
			`MLAT_USER=alice`+"\n"+
			`MLAT_ENABLED=true`+"\n"+
			`MLAT_PRIVATE=false`+"\n"+
			`GAIN=40`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	got := captureUpdates(t, h, map[string]string{
		"LATITUDE":     "47.0",
		"LONGITUDE":    "8.0",
		"ALTITUDE":     "120m",
		"MLAT_USER":    "bob", // only this changes
		"MLAT_ENABLED": "true",
		"MLAT_PRIVATE": "false",
		"GAIN":         "40",
	})
	end := time.Now().UTC().Add(time.Second)

	// MLAT_USER is the only tracked key carrying metadata.
	assertFieldUpdate(t, "MLAT_USER", got["MLAT_USER"], "bob", start, end)

	// Every other tracked key omitted.
	for _, k := range []string{"LATITUDE", "LONGITUDE", "ALTITUDE", "MLAT_ENABLED", "MLAT_PRIVATE"} {
		if raw, ok := got[k]; ok {
			t.Errorf("unchanged tracked key %s should be omitted, got %s", k, string(raw))
		}
	}

	// Non-tracked key still goes through, as a bare string, even when
	// its value matches current.
	if string(got["GAIN"]) != `"40"` {
		t.Errorf("GAIN = %s, want bare string %q", string(got["GAIN"]), `"40"`)
	}
}

// TestConfigPost_FeedEnvReadFailureTreatsAllAsBootstrap proves the
// degrade-rather-than-refuse posture: if feed.env can't be read at the
// metadata-gate step, every tracked key in the payload is treated as a
// bootstrap write so the save still goes through.
func TestConfigPost_FeedEnvReadFailureTreatsAllAsBootstrap(t *testing.T) {
	h := newWriteHarness(t)
	// Remove the seed feed.env so ReadAll returns ErrNotFound.
	if err := os.Remove(h.feedEnvPath); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	got := captureUpdates(t, h, map[string]string{
		"MLAT_USER": "bob",
		"GAIN":      "42",
	})
	end := time.Now().UTC().Add(time.Second)
	assertFieldUpdate(t, "MLAT_USER", got["MLAT_USER"], "bob", start, end)
	if string(got["GAIN"]) != `"42"` {
		t.Errorf("GAIN = %s, want bare string", string(got["GAIN"]))
	}
}
