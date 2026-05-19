// Package feedmeta builds the JSON payload for `apl-feed apply --json`
// from a form-posted updates map. Tracked keys whose posted value
// differs from the current on-disk value carry explicit
// `{value, edited_at, edited_by}` metadata; tracked keys whose value
// matches current pass through as bare strings; non-tracked keys also
// pass through as bare strings.
//
// Bare-string passthrough on unchanged tracked keys is deliberate. The
// apply library auto-derives GEO_CONFIGURED when LATITUDE or LONGITUDE
// is present in the touched-payload set regardless of value change, and
// the metadata path would bump the sidecar's edited_at for any tracked
// key carrying explicit metadata even if its canonical value matched.
// Bare strings skip the metadata path and only stamp on actual change.
//
// The tracked-keys list mirrors APL_FEED_APPLY_META_TRACKED_KEYS in
// feed/scripts/lib/feed-env-apply.sh. Parity is checked at test time by
// drift_test.go.
package feedmeta

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// EditedBy is the provenance tag attached to local-webconfig writes.
// Matches the enum in apl-feed apply.sh (feeder|website|legacy).
const EditedBy = "feeder"

// TrackedKeys are the feed.env keys whose per-write (edited_at, edited_by)
// tuple is tracked in /etc/airplanes/feed.meta.json. Keep aligned with
// APL_FEED_APPLY_META_TRACKED_KEYS in feed/scripts/lib/feed-env-apply.sh.
var TrackedKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"MLAT_USER",
	"MLAT_ENABLED",
	"MLAT_PRIVATE",
}

// FieldUpdate is the object-form payload value for a tracked key.
// JSON shape matches what `apl-feed apply --json` accepts; the strict
// RFC 3339 UTC regex on EditedAt is enforced server-side by the feed
// CLI — Format below produces the exact shape.
type FieldUpdate struct {
	Value    string `json:"value"`
	EditedAt string `json:"edited_at"`
	EditedBy string `json:"edited_by"`
}

// formatRFC3339UTC formats t as an RFC 3339 UTC timestamp with microsecond
// precision and a literal `Z` suffix. `time.RFC3339` would emit `+00:00`
// for a UTC time, which the apl-feed apply.sh edited_at regex rejects.
//
// Microsecond precision matters: the apply library's LWW gate normalises
// both incoming and on-disk stamps to a fixed-width microsecond form
// before comparing. With second-precision stamps, two webconfig saves
// to the same tracked field within the same wall-clock-second would
// produce equal post-normalize strings; the gate's strictly-newer rule
// would then silently drop the second save. Six fractional digits buys
// the resolution we need without overstating the precision (the
// server-side Python emits microsecond too via datetime.isoformat()).
func formatRFC3339UTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000Z")
}

// trackedSet is a lookup table for TrackedKeys built once at init.
var trackedSet = func() map[string]struct{} {
	s := make(map[string]struct{}, len(TrackedKeys))
	for _, k := range TrackedKeys {
		s[k] = struct{}{}
	}
	return s
}()

// IsTracked reports whether key is in the sidecar-tracked set.
func IsTracked(key string) bool {
	_, ok := trackedSet[key]
	return ok
}

// altitudeShapeRE matches the suffix-tolerant altitude input shape
// `^-?<digits>(.<digits>)?(m|ft)?$`. The capture groups are 1=signed
// numeric, 2=unit suffix ("", "m", "ft"). Mirrors the bash regex used
// by feed's altitude_to_bare_metres helper byte-for-byte.
var altitudeShapeRE = regexp.MustCompile(`^(-?[0-9]+(?:\.[0-9]+)?)(m|ft)?$`)

// trimAltitudeFraction strips trailing zeros after the decimal point
// and a trailing bare decimal point from a fixed-point ASCII number.
// Mirrors `sed -E 's/\.?0+$//'` in feed's altitude_to_bare_metres byte
// for byte: "120.0000000000" → "120", "121.9200000000" → "121.92",
// "10.0" → "10", "10." → "10". Inputs without a decimal point pass
// through unchanged.
func trimAltitudeFraction(s string) string {
	if !strings.ContainsRune(s, '.') {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// AltitudeToBareMetres parses a suffix-tolerant altitude string and
// returns its bare-metres canonical form alongside an ok flag.
//
//	""        → ("", true)        tombstone passthrough
//	"<n>"     → ("<n>", true)     already bare metres
//	"<n>m"    → ("<n>", true)     strip metre suffix
//	"<n>ft"   → ("<n>×0.3048", true) convert feet to metres
//
// Range-gates the POST-CONVERSION metres value against [-1000, 10000];
// out-of-range or regex-mismatched inputs return ("", false).
//
// Output format is fixed-point — never scientific notation — with ten
// fractional digits before trimming trailing zeros and a bare decimal
// point. Mirrors feed/scripts/lib/configure-validators.sh's
// altitude_to_bare_metres byte-for-byte; the shared fixture at
// internal/feedmeta/testdata/altitude-canonicalization.json (vendored
// from feed) pins the exact expected outputs.
//
// Used by:
//   - canonicalizeForCompare so BuildApplyPayload sees feed's storage
//     view of the value when deciding "did this change?"
//   - internal/devfakes ApplyFeedEnv to mirror feed's apply-layer
//     canonicalization in the dev-server.
func AltitudeToBareMetres(value string) (string, bool) {
	if value == "" {
		return "", true
	}
	m := altitudeShapeRE.FindStringSubmatch(value)
	if m == nil {
		return "", false
	}
	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return "", false
	}
	metres := num
	if m[2] == "ft" {
		metres = num * 0.3048
	}
	if metres < -1000 || metres > 10000 {
		return "", false
	}
	formatted := trimAltitudeFraction(strconv.FormatFloat(metres, 'f', 10, 64))
	return formatted, true
}

// canonicalizeForCompare returns a value normalised to the form the
// apply library would store on disk after its own canonicalization, so
// the "is this an actual value change" decision in BuildApplyPayload
// matches apply's view rather than raw-byte equality.
//
// For ALTITUDE this delegates to AltitudeToBareMetres so that a user
// typing `400ft` while disk holds `121.92` (the bare-metres value feed
// canonicalises both to) compares equal — Go must not send object
// metadata for a value the apply layer would treat as no-change.
//
// On parse failure (out-of-range, regex mismatch) we return the
// original value so the validator catches it on the apply round trip
// rather than papering over the error with a synthetic "no change".
//
// Other tracked keys (LATITUDE/LONGITUDE/MLAT_*) are byte-equal under
// apply's storage rules, so this helper is a no-op for them.
func canonicalizeForCompare(key, value string) string {
	switch key {
	case "ALTITUDE":
		out, ok := AltitudeToBareMetres(value)
		if !ok {
			return value
		}
		return out
	default:
		return value
	}
}

// BuildApplyPayload returns the `updates` map to ship to `apl-feed apply
// --json`, given the current on-disk values and the form-posted values.
//
// For each posted key:
//
//   - Non-tracked → passed through as a bare string. Validation, restart
//     fan-out and audit logging happen in the apply library.
//
//   - Tracked AND value differs from current (or absent from current,
//     i.e. bootstrap) → emitted as a FieldUpdate with edited_at=now,
//     edited_by="feeder". This is what stamps the sidecar with the
//     local-edit provenance.
//
//   - Tracked AND value matches current → passed through as a bare
//     string. Two reasons:
//
//     1. The apply library auto-derives GEO_CONFIGURED when LATITUDE
//        or LONGITUDE appears in the touched-payload set, regardless of
//        whether the value changed. Omitting unchanged lat/lon would
//        suppress the derive on every save that doesn't move the
//        coordinates.
//
//     2. The apply library's metadata path stamps the sidecar whenever
//        explicit incoming metadata is present and the LWW gate passes,
//        even for unchanged values (the heal path apl-feed config sync
//        uses). Sending object metadata for an unchanged tracked key
//        would therefore re-stamp edited_at on every form save and
//        clobber legitimate concurrent edits under LWW. Bare strings
//        bypass the metadata path and only stamp on actual change.
//
// `now` is injected so tests can pin time without monkey-patching. In
// production callers pass `time.Now().UTC()`.
//
// The returned map's value type is `any` because the wire mixes bare
// strings (non-tracked + unchanged-tracked) and FieldUpdate structs
// (changed-tracked). Marshalling produces the heterogeneous shape that
// `apl-feed apply --json` accepts.
func BuildApplyPayload(current, posted map[string]string, now time.Time) map[string]any {
	out := make(map[string]any, len(posted))
	stamp := formatRFC3339UTC(now)
	for k, v := range posted {
		if !IsTracked(k) {
			out[k] = v
			continue
		}
		if cur, ok := current[k]; ok && canonicalizeForCompare(k, cur) == canonicalizeForCompare(k, v) {
			// Tracked + canonically-equal-to-disk → bare string. Apply
			// library will determine no_change without bumping the
			// sidecar. canonicalizeForCompare matches apply's view so
			// ALTITUDE byte-different-but-canonically-equal entries
			// (e.g. user types "120", disk has "120m") don't trigger a
			// metadata write.
			out[k] = v
			continue
		}
		// Tracked + changed (or bootstrap from missing-in-current).
		out[k] = FieldUpdate{
			Value:    v,
			EditedAt: stamp,
			EditedBy: EditedBy,
		}
	}
	return out
}

// BareStringPayload returns `posted` converted to map[string]any with
// every value as a bare string and no metadata. Used by the HTTP
// handler when the pre-read of feed.env failed and we cannot tell which
// tracked keys actually changed — degrading to today's bare-string
// behavior (apply library default-stamps on actual change only) is
// strictly safer than treating every tracked key as a bootstrap write,
// which would stamp metadata on every form field every save.
func BareStringPayload(posted map[string]string) map[string]any {
	out := make(map[string]any, len(posted))
	for k, v := range posted {
		out[k] = v
	}
	return out
}
