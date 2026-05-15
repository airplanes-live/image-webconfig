package feedenv

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/airplanes-live/image-webconfig/internal/configspec"
)

// TestReadKeysMatchConfigspecAllReadKeys enforces the comment-block invariant
// in configspec.go that feedenv.ReadKeys and configspec.AllReadKeys stay
// aligned. The duplicate exists so apply-config can validate-preserve keys
// without depending on feedenv's file-IO surface; the price is hand-syncing
// two lists, and this test makes drift a build break instead of a silent bug.
func TestReadKeysMatchConfigspecAllReadKeys(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(ReadKeys, configspec.AllReadKeys) {
		t.Errorf("feedenv.ReadKeys and configspec.AllReadKeys diverged.\nReadKeys: %v\nAllReadKeys: %v", ReadKeys, configspec.AllReadKeys)
	}
}

func writeEnv(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "feed.env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadAll_Empty(t *testing.T) {
	t.Parallel()
	r := &Reader{Path: writeEnv(t, "")}
	got, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestReadAll_QuotedAndUnquoted(t *testing.T) {
	t.Parallel()
	r := &Reader{Path: writeEnv(t,
		`LATITUDE="51.5"`+"\n"+
			`LONGITUDE=-0.1`+"\n"+
			`MLAT_USER="Dave Display"`+"\n"+ // quoted with space
			`MLAT_ENABLED=true`+"\n"+
			`GAIN=auto`+"\n",
	)}
	got, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"LATITUDE":     "51.5",
		"LONGITUDE":    "-0.1",
		"MLAT_USER":    "Dave Display",
		"MLAT_ENABLED": "true",
		"GAIN":         "auto",
	}
	if !mapsEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestReadAll_FiltersToWhitelist(t *testing.T) {
	t.Parallel()
	r := &Reader{Path: writeEnv(t,
		`LATITUDE=51.5`+"\n"+
			`MYSTERY_KEY=should-be-ignored`+"\n"+
			`USER=legacy-not-whitelisted`+"\n"+
			`MLAT_USER=alice`+"\n",
	)}
	got, _ := r.ReadAll()
	if _, ok := got["MYSTERY_KEY"]; ok {
		t.Error("non-whitelisted key leaked into output")
	}
	if _, ok := got["USER"]; ok {
		t.Error("legacy USER key leaked into output (should be excluded post-split)")
	}
	if got["LATITUDE"] != "51.5" || got["MLAT_USER"] != "alice" {
		t.Errorf("missing whitelisted values: %v", got)
	}
}

func TestReadAll_HandlesCommentsAndBlankLines(t *testing.T) {
	t.Parallel()
	r := &Reader{Path: writeEnv(t,
		`# this is a comment`+"\n"+
			"\n"+
			`   # indented comment`+"\n"+
			`LATITUDE=51.5`+"\n",
	)}
	got, _ := r.ReadAll()
	if got["LATITUDE"] != "51.5" {
		t.Errorf("got %v, want LATITUDE=51.5", got)
	}
}

func TestReadAll_EmptyValueIsKept(t *testing.T) {
	t.Parallel()
	r := &Reader{Path: writeEnv(t, `UAT_INPUT=`+"\n")}
	got, _ := r.ReadAll()
	v, present := got["UAT_INPUT"]
	if !present {
		t.Error("UAT_INPUT= line should produce empty-string value, not be skipped")
	}
	if v != "" {
		t.Errorf("got %q, want empty string", v)
	}
}

func TestReadAll_NotFound(t *testing.T) {
	t.Parallel()
	r := &Reader{Path: filepath.Join(t.TempDir(), "no-such-file")}
	_, err := r.ReadAll()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestReadAll_InvalidLineSkipped(t *testing.T) {
	t.Parallel()
	// "lower-case" key is invalid per regex; "FOO=value oops" is valid as
	// "FOO=value\\soops" is NOT a quoted value, so the regex's \S* won't
	// match the embedded whitespace. We expect it to be skipped.
	r := &Reader{Path: writeEnv(t,
		`bad-line-no-equals`+"\n"+
			`lowercase=ignored`+"\n"+
			`LATITUDE 51.5`+"\n"+
			`LATITUDE=51.5`+"\n",
	)}
	got, _ := r.ReadAll()
	if got["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q, want 51.5", got["LATITUDE"])
	}
	if _, ok := got["lowercase"]; ok {
		t.Error("lowercase key leaked")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
