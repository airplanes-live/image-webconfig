// Package feedmeta builds the JSON payload for `apl-feed apply --json`
// from a form-posted updates map. Tracked keys carry explicit
// `{value, edited_at, edited_by}` metadata; non-tracked keys pass through
// as bare strings. Tracked keys whose posted value matches the current
// on-disk value are omitted entirely so an unchanged save does not bump
// the sidecar's edited_at for them.
//
// The tracked-keys list mirrors APL_FEED_APPLY_META_TRACKED_KEYS in
// feed/scripts/lib/feed-env-apply.sh. Drift is checked by
// internal/feedmeta/feedmeta_test.go::TestTrackedKeysMatchFeedSide once
// the contract surface lands in `apl-feed schema --json`; today the list
// is duplicated here.
package feedmeta

import "time"

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

// formatRFC3339UTC formats t as an RFC 3339 UTC timestamp with seconds
// precision and a literal `Z` suffix. `time.RFC3339` would emit `+00:00`
// for a UTC time, which the apl-feed apply.sh edited_at regex rejects.
func formatRFC3339UTC(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05Z")
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
//     edited_by="feeder". This is what stamps the sidecar.
//
//   - Tracked AND value matches current → omitted entirely. The apply
//     library would treat such a key as no-change anyway, but omitting
//     it here also prevents the explicit-metadata code path from
//     re-stamping the sidecar's edited_at (which the apply library does
//     when an object-form payload is present for a tracked key,
//     regardless of value-change).
//
// `now` is injected so tests can pin time without monkey-patching. In
// production callers pass `time.Now().UTC()`.
//
// The returned map's value type is `any` because the wire mixes bare
// strings (non-tracked) and FieldUpdate structs (tracked). Marshalling
// produces the heterogeneous shape `apl-feed apply --json` accepts.
func BuildApplyPayload(current, posted map[string]string, now time.Time) map[string]any {
	out := make(map[string]any, len(posted))
	stamp := formatRFC3339UTC(now)
	for k, v := range posted {
		if !IsTracked(k) {
			out[k] = v
			continue
		}
		// Tracked: compare against current; omit if unchanged.
		if cur, ok := current[k]; ok && cur == v {
			continue
		}
		out[k] = FieldUpdate{
			Value:    v,
			EditedAt: stamp,
			EditedBy: EditedBy,
		}
	}
	return out
}
