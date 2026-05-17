package feedmeta

import (
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestTrackedKeysParity_WithFeedSide fetches
// feed/scripts/lib/feed-env-apply.sh from airplanes-live/feed and parses
// the canonical APL_FEED_APPLY_META_TRACKED_KEYS array, then asserts
// equality with feedmeta.TrackedKeys. Drift between the two lists would
// silently break metadata stamping for the new key (Go would forward
// bare strings; apply would gate on metadata).
//
// Known limitations:
//   - The fetch points at feed/dev, so concurrent feed-side activity
//     can cause a transient CI failure on an unrelated image-webconfig
//     PR until this list is bumped to match. Acceptable: real drift is
//     surfaced loudly; the alternative (silent drift) is worse.
//   - Offline / network failure skips the test rather than failing,
//     so a fully air-gapped CI would not detect drift via this path.
//     Long-term, the contract surface should move to
//     `apl-feed schema --json` so the check runs against runtime state
//     rather than a remote file.
//
// Branch: dev (workspace convention — production deploys from main, dev
// is the integration branch).
func TestTrackedKeysParity_WithFeedSide(t *testing.T) {
	if os.Getenv("TESTING_OFFLINE") == "1" {
		t.Skip("TESTING_OFFLINE=1; skipping cross-repo drift check")
	}
	const url = "https://raw.githubusercontent.com/airplanes-live/feed/dev/scripts/lib/feed-env-apply.sh"
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Skipf("network fetch failed (%v); skipping cross-repo drift check", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("HTTP %d fetching %s; skipping cross-repo drift check", resp.StatusCode, url)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		t.Fatalf("read feed-env-apply.sh: %v", err)
	}

	feedKeys, err := parseTrackedKeys(string(body))
	if err != nil {
		t.Fatalf("parse APL_FEED_APPLY_META_TRACKED_KEYS: %v", err)
	}

	// Order-insensitive comparison.
	gotSorted := append([]string(nil), TrackedKeys...)
	feedSorted := append([]string(nil), feedKeys...)
	sort.Strings(gotSorted)
	sort.Strings(feedSorted)

	if !equalStringSlices(gotSorted, feedSorted) {
		t.Errorf("tracked-keys drift between feedmeta.TrackedKeys and feed's APL_FEED_APPLY_META_TRACKED_KEYS:\n  Go:   %v\n  feed: %v\nUpdate feedmeta.TrackedKeys to match feed.", gotSorted, feedSorted)
	}
}

// parseTrackedKeys extracts the bash array body of
// APL_FEED_APPLY_META_TRACKED_KEYS=( ... ) from the script text. The
// array literal can span multiple lines and contain comments.
var trackedKeysAssignRe = regexp.MustCompile(`(?s)APL_FEED_APPLY_META_TRACKED_KEYS=\(\s*(.*?)\s*\)`)

func parseTrackedKeys(script string) ([]string, error) {
	m := trackedKeysAssignRe.FindStringSubmatch(script)
	if m == nil {
		return nil, errNoTrackedKeysAssign
	}
	body := m[1]
	// Strip in-line comments. Bash `#` not preceded by `$` or in a quoted
	// context starts a comment to end-of-line. We don't expect quoted
	// identifiers here, so a simple strip is sufficient.
	var cleaned strings.Builder
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		cleaned.WriteString(line)
		cleaned.WriteByte(' ')
	}
	fields := strings.Fields(cleaned.String())
	if len(fields) == 0 {
		return nil, errEmptyTrackedKeys
	}
	return fields, nil
}

type parseError string

func (e parseError) Error() string { return string(e) }

const (
	errNoTrackedKeysAssign parseError = "APL_FEED_APPLY_META_TRACKED_KEYS assignment not found in script"
	errEmptyTrackedKeys    parseError = "APL_FEED_APPLY_META_TRACKED_KEYS parsed to zero keys"
)

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestParseTrackedKeys_FixtureShape is a unit test for the parser using
// a fixture; runs offline so the parser is covered even when the
// cross-repo fetch is skipped.
func TestParseTrackedKeys_FixtureShape(t *testing.T) {
	fixture := `
APL_FEED_APPLY_META_TRACKED_KEYS=(
    LATITUDE LONGITUDE ALTITUDE MLAT_USER MLAT_ENABLED MLAT_PRIVATE  # tracked
)
`
	got, err := parseTrackedKeys(fixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"LATITUDE", "LONGITUDE", "ALTITUDE", "MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE"}
	if !equalStringSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTrackedKeys_MissingAssignment(t *testing.T) {
	_, err := parseTrackedKeys("no array here")
	if err != errNoTrackedKeysAssign {
		t.Errorf("err = %v, want %v", err, errNoTrackedKeysAssign)
	}
}
