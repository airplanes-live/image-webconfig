package feedmeta

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"
)

// fixedNow is the reference timestamp used across these tests. The
// RFC 3339 UTC literal matches what BuildApplyPayload emits for this time.
// Microsecond precision (6 zero digits after the dot) — formatRFC3339UTC
// always emits .000000 even for an exact-second time, so the LWW gate
// can compare two same-second writes without colliding under truncation.
var fixedNow = time.Date(2026, 5, 16, 12, 34, 56, 0, time.UTC)

const fixedStamp = "2026-05-16T12:34:56.000000Z"

func TestFormatRFC3339UTC_ProducesZSuffix(t *testing.T) {
	got := formatRFC3339UTC(fixedNow)
	if got != fixedStamp {
		t.Fatalf("formatRFC3339UTC: want %q, got %q", fixedStamp, got)
	}
}

func TestFormatRFC3339UTC_NormalizesNonUTCInput(t *testing.T) {
	cest := time.FixedZone("CEST", 2*60*60)
	local := time.Date(2026, 5, 16, 14, 34, 56, 0, cest)
	got := formatRFC3339UTC(local)
	if got != fixedStamp {
		t.Fatalf("formatRFC3339UTC: want %q, got %q", fixedStamp, got)
	}
}

func TestFormatRFC3339UTC_EmitsMicrosecondFractional(t *testing.T) {
	// A non-zero sub-second component must surface in the stamp so two
	// same-second writes from this layer can be ordered by the LWW gate.
	sub := time.Date(2026, 5, 16, 12, 34, 56, 123456789, time.UTC)
	got := formatRFC3339UTC(sub)
	want := "2026-05-16T12:34:56.123456Z"
	if got != want {
		t.Fatalf("formatRFC3339UTC: want %q, got %q (nanoseconds truncated to microseconds)", want, got)
	}
}

func TestIsTracked(t *testing.T) {
	for _, k := range TrackedKeys {
		if !IsTracked(k) {
			t.Errorf("IsTracked(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"GAIN", "INPUT", "UAT_INPUT", "DUMP978_GAIN", "GEO_CONFIGURED", "TARGET"} {
		if IsTracked(k) {
			t.Errorf("IsTracked(%q) = true, want false", k)
		}
	}
}

func TestBuildApplyPayload_TrackedKeyChanged(t *testing.T) {
	current := map[string]string{"MLAT_USER": "alice"}
	posted := map[string]string{"MLAT_USER": "bob"}
	got := BuildApplyPayload(current, posted, fixedNow)
	want := map[string]any{
		"MLAT_USER": FieldUpdate{Value: "bob", EditedAt: fixedStamp, EditedBy: "feeder"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload: want %#v, got %#v", want, got)
	}
}

func TestBuildApplyPayload_TrackedKeyUnchangedIsBareString(t *testing.T) {
	// Unchanged tracked keys flow through as bare strings so the apply
	// library's GEO_CONFIGURED auto-derive (which triggers on LAT/LON
	// being present in the payload, not on whether they changed) still
	// fires, and so the metadata path does not bump edited_at for
	// fields the user did not touch.
	current := map[string]string{"MLAT_USER": "alice"}
	posted := map[string]string{"MLAT_USER": "alice"}
	got := BuildApplyPayload(current, posted, fixedNow)
	if v, ok := got["MLAT_USER"]; !ok {
		t.Fatalf("unchanged tracked key must remain in payload as bare string, got %#v", got)
	} else if s, isStr := v.(string); !isStr || s != "alice" {
		t.Fatalf("MLAT_USER want bare string %q, got %#v", "alice", v)
	}
	if _, isUpdate := got["MLAT_USER"].(FieldUpdate); isUpdate {
		t.Fatal("unchanged tracked key must NOT carry FieldUpdate metadata")
	}
}

func TestBuildApplyPayload_UnchangedLatLonStillPassesThrough(t *testing.T) {
	// Regression for the GEO_CONFIGURED auto-derive path. Apply triggers
	// the derive based on LAT/LON appearing in the payload, regardless
	// of whether their value changed. Omitting unchanged lat/lon would
	// silently suppress the derive on every save.
	current := map[string]string{"LATITUDE": "47.0", "LONGITUDE": "8.0"}
	posted := map[string]string{"LATITUDE": "47.0", "LONGITUDE": "8.0"}
	got := BuildApplyPayload(current, posted, fixedNow)
	for _, k := range []string{"LATITUDE", "LONGITUDE"} {
		v, ok := got[k]
		if !ok {
			t.Fatalf("%s must remain in payload so apply auto-derives GEO_CONFIGURED, got %#v", k, got)
		}
		if _, isUpdate := v.(FieldUpdate); isUpdate {
			t.Errorf("%s must be bare string when unchanged, got FieldUpdate", k)
		}
	}
}

func TestBuildApplyPayload_TrackedKeyBootstrap(t *testing.T) {
	// Empty current (e.g. fresh install, feed.env absent from this view) —
	// any tracked key posted is treated as a change.
	current := map[string]string{}
	posted := map[string]string{"ALTITUDE": "120m"}
	got := BuildApplyPayload(current, posted, fixedNow)
	want := map[string]any{
		"ALTITUDE": FieldUpdate{Value: "120m", EditedAt: fixedStamp, EditedBy: "feeder"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bootstrap payload: want %#v, got %#v", want, got)
	}
}

func TestBuildApplyPayload_NonTrackedKeyPassthrough(t *testing.T) {
	current := map[string]string{"GAIN": "40", "INPUT": "rtlsdr"}
	posted := map[string]string{"GAIN": "40", "INPUT": "sdrplay"}
	got := BuildApplyPayload(current, posted, fixedNow)
	// Both pass through as bare strings, including the unchanged GAIN —
	// non-tracked keys don't participate in metadata gating.
	if got["GAIN"] != "40" {
		t.Errorf("GAIN: want bare string %q, got %#v", "40", got["GAIN"])
	}
	if got["INPUT"] != "sdrplay" {
		t.Errorf("INPUT: want bare string %q, got %#v", "sdrplay", got["INPUT"])
	}
	for _, v := range got {
		if _, isUpdate := v.(FieldUpdate); isUpdate {
			t.Errorf("non-tracked keys must not be FieldUpdates: %#v", got)
		}
	}
}

func TestBuildApplyPayload_MixedChangeUnchangedPassthrough(t *testing.T) {
	current := map[string]string{
		"LATITUDE":     "47.0",
		"LONGITUDE":    "8.0",
		"ALTITUDE":     "120m",
		"MLAT_USER":    "alice",
		"MLAT_ENABLED": "true",
		"MLAT_PRIVATE": "false",
		"GAIN":         "40",
	}
	// User edits only MLAT_USER; the form posts every visible field.
	posted := map[string]string{
		"LATITUDE":     "47.0",
		"LONGITUDE":    "8.0",
		"ALTITUDE":     "120m",
		"MLAT_USER":    "bob",
		"MLAT_ENABLED": "true",
		"MLAT_PRIVATE": "false",
		"GAIN":         "40",
	}
	got := BuildApplyPayload(current, posted, fixedNow)

	// MLAT_USER is the only tracked key that moves — it gets metadata.
	wantUpdate := FieldUpdate{Value: "bob", EditedAt: fixedStamp, EditedBy: "feeder"}
	if !reflect.DeepEqual(got["MLAT_USER"], wantUpdate) {
		t.Errorf("MLAT_USER: want %#v, got %#v", wantUpdate, got["MLAT_USER"])
	}

	// Every other tracked key passes through as a bare string (no metadata).
	for _, k := range []string{"LATITUDE", "LONGITUDE", "ALTITUDE", "MLAT_ENABLED", "MLAT_PRIVATE"} {
		v, ok := got[k]
		if !ok {
			t.Errorf("unchanged tracked key %s must remain in payload (bare string), missing", k)
			continue
		}
		if _, isUpdate := v.(FieldUpdate); isUpdate {
			t.Errorf("unchanged tracked key %s must be bare string, got FieldUpdate", k)
		}
		if s, isStr := v.(string); !isStr || s != current[k] {
			t.Errorf("%s bare-string passthrough: want %q, got %#v", k, current[k], v)
		}
	}

	// Non-tracked key passes through unchanged-value-and-all.
	if got["GAIN"] != "40" {
		t.Errorf("GAIN passthrough: want %q, got %#v", "40", got["GAIN"])
	}
}

func TestBuildApplyPayload_BooleanFlipsCarryMetadata(t *testing.T) {
	// MLAT_ENABLED and MLAT_PRIVATE are the only boolean tracked keys.
	// Either direction (false → true and true → false) must produce a
	// FieldUpdate.
	for _, tc := range []struct {
		key  string
		from string
		to   string
	}{
		{"MLAT_ENABLED", "false", "true"},
		{"MLAT_ENABLED", "true", "false"},
		{"MLAT_PRIVATE", "false", "true"},
		{"MLAT_PRIVATE", "true", "false"},
	} {
		t.Run(tc.key+"_"+tc.from+"_to_"+tc.to, func(t *testing.T) {
			current := map[string]string{tc.key: tc.from}
			posted := map[string]string{tc.key: tc.to}
			got := BuildApplyPayload(current, posted, fixedNow)
			want := FieldUpdate{Value: tc.to, EditedAt: fixedStamp, EditedBy: "feeder"}
			if !reflect.DeepEqual(got[tc.key], want) {
				t.Errorf("flip %s %s->%s: want %#v, got %#v", tc.key, tc.from, tc.to, want, got[tc.key])
			}
		})
	}
}

func TestBuildApplyPayload_AltitudeCanonicalEqualIsBareString(t *testing.T) {
	// Apply library appends `m` to bare-number ALTITUDE before storing
	// (mirrors _apl_feed_apply_canonicalize_altitude). A user typing
	// "120" while disk holds "120m" must NOT produce object metadata —
	// the canonical value is the same and the sidecar must not be bumped.
	for _, tc := range []struct {
		current, posted string
	}{
		{"120m", "120"},
		{"120", "120m"},
		{"120m", "120m"},
		{"120ft", "120ft"},
		{"-30.49", "-30.49"},
	} {
		t.Run(tc.current+"_vs_"+tc.posted, func(t *testing.T) {
			got := BuildApplyPayload(
				map[string]string{"ALTITUDE": tc.current},
				map[string]string{"ALTITUDE": tc.posted},
				fixedNow,
			)
			if _, isUpdate := got["ALTITUDE"].(FieldUpdate); isUpdate {
				t.Errorf("ALTITUDE canonically equal (%q vs %q): expected bare string, got FieldUpdate", tc.current, tc.posted)
			}
		})
	}
}

func TestBuildApplyPayload_AltitudeRealChangeCarriesMetadata(t *testing.T) {
	// Genuinely different ALTITUDE values (different canonical form)
	// still get metadata.
	got := BuildApplyPayload(
		map[string]string{"ALTITUDE": "120m"},
		map[string]string{"ALTITUDE": "150"},
		fixedNow,
	)
	want := FieldUpdate{Value: "150", EditedAt: fixedStamp, EditedBy: "feeder"}
	if got["ALTITUDE"] != want {
		t.Errorf("ALTITUDE real change: want %#v, got %#v", want, got["ALTITUDE"])
	}
}

func TestCanonicalizeForCompare(t *testing.T) {
	for _, tc := range []struct {
		key, in, want string
	}{
		{"ALTITUDE", "120", "120m"},
		{"ALTITUDE", "120m", "120m"},
		{"ALTITUDE", "120ft", "120ft"},
		{"ALTITUDE", "-100", "-100m"},
		{"ALTITUDE", "", ""},
		{"MLAT_USER", "bob", "bob"}, // non-altitude → no-op
		{"LATITUDE", "47.0", "47.0"},
	} {
		got := canonicalizeForCompare(tc.key, tc.in)
		if got != tc.want {
			t.Errorf("canonicalizeForCompare(%q, %q) = %q, want %q", tc.key, tc.in, got, tc.want)
		}
	}
}

func TestBareStringPayload(t *testing.T) {
	posted := map[string]string{
		"MLAT_USER":    "bob",
		"MLAT_ENABLED": "true",
		"GAIN":         "42",
	}
	got := BareStringPayload(posted)
	for k, v := range posted {
		if got[k] != v {
			t.Errorf("%s: want bare string %q, got %#v", k, v, got[k])
		}
	}
	for _, v := range got {
		if _, isUpdate := v.(FieldUpdate); isUpdate {
			t.Errorf("BareStringPayload must not emit FieldUpdate, got %#v", v)
		}
	}
}

func TestBuildApplyPayload_AllTrackedKeysCovered(t *testing.T) {
	// Bootstrap every tracked key — proves the tracked-keys list and the
	// metadata-emit branch agree.
	current := map[string]string{}
	posted := map[string]string{
		"LATITUDE":     "47.0",
		"LONGITUDE":    "8.0",
		"ALTITUDE":     "120m",
		"MLAT_USER":    "bob",
		"MLAT_ENABLED": "true",
		"MLAT_PRIVATE": "false",
	}
	got := BuildApplyPayload(current, posted, fixedNow)
	for _, k := range TrackedKeys {
		u, ok := got[k].(FieldUpdate)
		if !ok {
			t.Fatalf("tracked key %s must be a FieldUpdate, got %#v", k, got[k])
		}
		if u.EditedBy != "feeder" {
			t.Errorf("%s: edited_by want %q, got %q", k, "feeder", u.EditedBy)
		}
		if u.EditedAt != fixedStamp {
			t.Errorf("%s: edited_at want %q, got %q", k, fixedStamp, u.EditedAt)
		}
		if u.Value != posted[k] {
			t.Errorf("%s: value want %q, got %q", k, posted[k], u.Value)
		}
	}
}

// TestBuildApplyPayload_JSONShape pins the wire shape the apply CLI sees.
// Tracked-key entries must serialize as an object with exactly the three
// known fields; non-tracked entries as a JSON string.
func TestBuildApplyPayload_JSONShape(t *testing.T) {
	got := BuildApplyPayload(
		map[string]string{"MLAT_USER": "alice", "GAIN": "40"},
		map[string]string{"MLAT_USER": "bob", "GAIN": "42"},
		fixedNow,
	)
	wrap := map[string]any{"updates": got}
	raw, err := json.Marshal(wrap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed struct {
		Updates map[string]json.RawMessage `json:"updates"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(parsed.Updates["GAIN"]) != `"42"` {
		t.Errorf("GAIN wire shape: want %q, got %q", `"42"`, parsed.Updates["GAIN"])
	}
	var fu FieldUpdate
	if err := json.Unmarshal(parsed.Updates["MLAT_USER"], &fu); err != nil {
		t.Fatalf("MLAT_USER decode: %v", err)
	}
	if fu != (FieldUpdate{Value: "bob", EditedAt: fixedStamp, EditedBy: "feeder"}) {
		t.Errorf("MLAT_USER struct: got %#v", fu)
	}
}

// TestTrackedKeys_Sorted is a smoke test: order doesn't affect behavior
// (we build a set), but a stable canonical order makes diffs against the
// feed-side list easier to read.
func TestTrackedKeys_Sorted(t *testing.T) {
	got := append([]string(nil), TrackedKeys...)
	want := []string{
		"ALTITUDE", "LATITUDE", "LONGITUDE", "MLAT_ENABLED", "MLAT_PRIVATE", "MLAT_USER",
	}
	sortedGot := append([]string(nil), got...)
	sort.Strings(sortedGot)
	if !reflect.DeepEqual(sortedGot, want) {
		t.Errorf("TrackedKeys set: want sorted %v, got sorted %v", want, sortedGot)
	}
}
