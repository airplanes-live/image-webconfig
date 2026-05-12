package main

import (
	"os"
	"strings"
	"testing"
)

// readUpdates is package-internal; round-trip a few canonical shapes here.
// The full attack corpus lives in internal/configspec.

func TestReadUpdates_RejectsOversize(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(strings.Repeat("a", stdinCap+1))
	_, err := readUpdates(body)
	if err == nil {
		t.Fatal("readUpdates accepted oversized stdin")
	}
}

func TestReadUpdates_RejectsNonString(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"updates":{"UAT_INPUT":null}}`)
	_, err := readUpdates(body)
	if err == nil {
		t.Fatal("readUpdates accepted null value (would silently disable 978)")
	}
}

func TestReadUpdates_RejectsExtraDocument(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"updates":{}}{"updates":{}}`)
	if _, err := readUpdates(body); err == nil {
		t.Fatal("readUpdates accepted multi-document body")
	}
}

func TestReadUpdates_RejectsUnknownTopLevel(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"updates":{},"extra":1}`)
	if _, err := readUpdates(body); err == nil {
		t.Fatal("readUpdates accepted unknown top-level field")
	}
}

func TestReadUpdates_AcceptsEmpty(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"updates":{}}`)
	got, err := readUpdates(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

func TestReadUpdates_AcceptsStringValues(t *testing.T) {
	t.Parallel()
	body := strings.NewReader(`{"updates":{"LATITUDE":"51.5","UAT_INPUT":""}}`)
	got, err := readUpdates(body)
	if err != nil {
		t.Fatal(err)
	}
	if got["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q", got["LATITUDE"])
	}
	if v, ok := got["UAT_INPUT"]; !ok || v != "" {
		t.Errorf("UAT_INPUT = (%q, %v), want (\"\", true)", v, ok)
	}
}

func TestMergeAndValidate_PreservedValueFailsClosedOnUnsafeChars(t *testing.T) {
	t.Parallel()
	existing := map[string]string{
		"NET_OPTIONS": `--something "with quote"`, // shell-sensitive char
	}
	updates := map[string]string{}
	if _, err := mergeAndValidate(existing, updates); err == nil {
		t.Fatal("mergeAndValidate accepted preserved value with unsafe chars")
	}
}

func TestMergeAndValidate_AppliesUpdates(t *testing.T) {
	t.Parallel()
	existing := map[string]string{
		"LATITUDE":    "0",
		"NET_OPTIONS": "--quiet",
	}
	updates := map[string]string{"LATITUDE": "51.5"}
	got, err := mergeAndValidate(existing, updates)
	if err != nil {
		t.Fatal(err)
	}
	if got["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q, want 51.5", got["LATITUDE"])
	}
	if got["NET_OPTIONS"] != "--quiet" {
		t.Errorf("NET_OPTIONS = %q, want --quiet (preserved)", got["NET_OPTIONS"])
	}
}

func TestMergeAndValidate_CanonicalizesAltitude(t *testing.T) {
	t.Parallel()
	got, err := mergeAndValidate(map[string]string{}, map[string]string{"ALTITUDE": "120"})
	if err != nil {
		t.Fatal(err)
	}
	if got["ALTITUDE"] != "120m" {
		t.Errorf("ALTITUDE = %q, want 120m", got["ALTITUDE"])
	}
}

func TestMergeAndValidate_AutoSetsGeoConfiguredTrueWhenCoordsSubmitted(t *testing.T) {
	t.Parallel()
	// Frontend submits lat/lon/alt together; auto-derive infers GEO_CONFIGURED
	// "user is configuring geo" and writes true. MLAT_USER carries through
	// untouched. ALTITUDE is also required so the GEO_CONFIGURED=>coords+alt
	// rule passes ValidateConsistency.
	got, err := mergeAndValidate(
		map[string]string{"MLAT_USER": "alice"},
		map[string]string{"LATITUDE": "51.5", "LONGITUDE": "-0.1", "ALTITUDE": "35m"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["GEO_CONFIGURED"] != "true" {
		t.Errorf("GEO_CONFIGURED = %q, want true", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_AutoSetsGeoConfiguredFalseForZeroPair(t *testing.T) {
	t.Parallel()
	// Caller submits a (0,0) placeholder pair (image freeze, reset to
	// defaults, etc.); auto-derive infers unconfigured even though the
	// caller didn't say so explicitly.
	got, err := mergeAndValidate(
		map[string]string{"MLAT_USER": "alice"},
		map[string]string{"LATITUDE": "0", "LONGITUDE": "0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["GEO_CONFIGURED"] != "false" {
		t.Errorf("GEO_CONFIGURED = %q, want false", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_AutoSetsGeoConfiguredTrueForEquator(t *testing.T) {
	t.Parallel()
	// Real equator coordinate (lat=0, lon=13). Must NOT be classified as
	// the placeholder pair. Altitude submitted alongside so the consistency
	// rule's altitude-required-when-GEO_CONFIGURED=true clause is satisfied.
	got, err := mergeAndValidate(
		map[string]string{"MLAT_USER": "alice"},
		map[string]string{"LATITUDE": "0", "LONGITUDE": "13", "ALTITUDE": "20m"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["GEO_CONFIGURED"] != "true" {
		t.Errorf("GEO_CONFIGURED = %q, want true (equator is configured)", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_ExplicitGeoConfiguredWinsOverAutoDerive(t *testing.T) {
	t.Parallel()
	// Caller explicitly sets GEO_CONFIGURED=false while also submitting
	// real coords (rare but possible, e.g., "save these values but mark
	// as not-yet-configured"). The explicit value wins.
	got, err := mergeAndValidate(
		map[string]string{"MLAT_USER": "alice"},
		map[string]string{"LATITUDE": "51.5", "LONGITUDE": "-0.1", "GEO_CONFIGURED": "false"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["GEO_CONFIGURED"] != "false" {
		t.Errorf("GEO_CONFIGURED = %q, want false (explicit wins)", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_MlatEnabledRequiresAltitude(t *testing.T) {
	t.Parallel()
	// MLAT_ENABLED=true with lat/lon but empty altitude must be rejected
	// at the apply-config layer so a webconfig POST never lands a state the
	// daemon would strict-fail on as altitude_empty.
	_, err := mergeAndValidate(
		map[string]string{"LATITUDE": "51.5", "LONGITUDE": "-0.1", "ALTITUDE": ""},
		map[string]string{"MLAT_ENABLED": "true", "GEO_CONFIGURED": "true"},
	)
	if err == nil {
		t.Fatalf("expected error for MLAT_ENABLED=true with empty ALTITUDE; got nil")
	}
	if !strings.Contains(err.Error(), "ALTITUDE") {
		t.Errorf("error = %q; want it to name ALTITUDE", err)
	}
}

func TestMergeAndValidate_NoAutoSetWhenCoordsUntouched(t *testing.T) {
	t.Parallel()
	// Caller updates an unrelated key (MLAT_ENABLED). GEO_CONFIGURED must
	// not be invented from existing values; the rule fires only when the
	// caller submits coords.
	got, err := mergeAndValidate(
		map[string]string{"MLAT_USER": "alice", "LATITUDE": "51.5", "LONGITUDE": "-0.1"},
		map[string]string{"MLAT_ENABLED": "false"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["GEO_CONFIGURED"]; ok {
		t.Errorf("GEO_CONFIGURED was set when coords weren't touched: %q", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_AltitudeOnlyUpdatePreservesExistingGeoConfigured(t *testing.T) {
	t.Parallel()
	// Operator already configured location with GEO_CONFIGURED=false
	// (e.g., they're saving values but haven't confirmed real coords yet).
	// An altitude-only update must NOT auto-flip the flag to true.
	got, err := mergeAndValidate(
		map[string]string{
			"MLAT_USER":      "alice",
			"LATITUDE":       "51.5",
			"LONGITUDE":      "-0.1",
			"GEO_CONFIGURED": "false",
		},
		map[string]string{"ALTITUDE": "120"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["GEO_CONFIGURED"] != "false" {
		t.Errorf("GEO_CONFIGURED = %q, want false (altitude-only must preserve existing flag)", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_AltitudeOnlyUpdateAtZeroPairPreservesTrue(t *testing.T) {
	t.Parallel()
	// Inverse: existing config has GEO_CONFIGURED=true with (0,0) coords
	// (legitimately the Atlantic, or a deliberate hand-edit). An altitude-
	// only update must NOT auto-flip to false based on the (0,0) heuristic.
	got, err := mergeAndValidate(
		map[string]string{
			"MLAT_USER":      "alice",
			"LATITUDE":       "0",
			"LONGITUDE":      "0",
			"GEO_CONFIGURED": "true",
		},
		map[string]string{"ALTITUDE": "120"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got["GEO_CONFIGURED"] != "true" {
		t.Errorf("GEO_CONFIGURED = %q, want true (altitude-only must preserve explicit true)", got["GEO_CONFIGURED"])
	}
}

func TestMergeAndValidate_NoAutoSetWhenCounterpartCoordEmpty(t *testing.T) {
	t.Parallel()
	// Existing config has empty LONGITUDE (partial earlier write or
	// hand-edit). Updating LATITUDE alone must NOT auto-derive — both
	// axes must be non-empty in the merged config for the heuristic to
	// have valid inputs.
	_, err := mergeAndValidate(
		map[string]string{"MLAT_USER": "alice", "LATITUDE": "0", "LONGITUDE": ""},
		map[string]string{"LATITUDE": "51.5"},
	)
	// Auto-derive should be skipped; no consistency error fires because
	// GEO_CONFIGURED stays absent.
	if err != nil {
		t.Fatalf("unexpected error from single-axis update with empty counterpart: %v", err)
	}
}

func TestMergeAndValidate_RejectsGeoConfiguredTrueWithoutLatitude(t *testing.T) {
	t.Parallel()
	// Cross-key consistency: explicit GEO_CONFIGURED=true requires
	// LATITUDE and LONGITUDE both present so the daemon doesn't strict-
	// fail downstream.
	_, err := mergeAndValidate(
		map[string]string{},
		map[string]string{"GEO_CONFIGURED": "true", "LONGITUDE": "13"},
	)
	if err == nil {
		t.Fatal("expected consistency error for GEO_CONFIGURED=true without LATITUDE")
	}
}

func TestRenderFeedEnv_DeterministicAlphabetical(t *testing.T) {
	t.Parallel()
	a := renderFeedEnv(map[string]string{"MLAT_USER": "alice", "LATITUDE": "0", "ALTITUDE": "0m"})
	b := renderFeedEnv(map[string]string{"ALTITUDE": "0m", "LATITUDE": "0", "MLAT_USER": "alice"})
	if string(a) != string(b) {
		t.Fatalf("non-deterministic render:\n%s---\n%s", a, b)
	}
	if !strings.Contains(string(a), `LATITUDE="0"`) {
		t.Errorf("missing LATITUDE=\"0\" line: %s", a)
	}
}

func TestReadExisting_ParsesQuotedAndUnquoted(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir() + "/feed.env"
	body := `# header
LATITUDE="51.5"
MLAT_USER=alice
MLAT_ENABLED=true

# comment
NET_OPTIONS="--quiet --verbose"
`
	if err := writeAll(tmp, body); err != nil {
		t.Fatal(err)
	}
	got, err := readExisting(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if got["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q, want 51.5", got["LATITUDE"])
	}
	if got["MLAT_USER"] != "alice" {
		t.Errorf("MLAT_USER = %q, want alice", got["MLAT_USER"])
	}
	if got["MLAT_ENABLED"] != "true" {
		t.Errorf("MLAT_ENABLED = %q, want true", got["MLAT_ENABLED"])
	}
	if got["NET_OPTIONS"] != "--quiet --verbose" {
		t.Errorf("NET_OPTIONS = %q", got["NET_OPTIONS"])
	}
}

func writeAll(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0o644)
}
