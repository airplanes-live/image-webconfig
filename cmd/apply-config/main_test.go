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
