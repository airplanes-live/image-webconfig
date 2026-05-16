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
var fixedNow = time.Date(2026, 5, 16, 12, 34, 56, 0, time.UTC)

const fixedStamp = "2026-05-16T12:34:56Z"

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

func TestBuildApplyPayload_TrackedKeyUnchangedIsOmitted(t *testing.T) {
	current := map[string]string{"MLAT_USER": "alice"}
	posted := map[string]string{"MLAT_USER": "alice"}
	got := BuildApplyPayload(current, posted, fixedNow)
	if _, ok := got["MLAT_USER"]; ok {
		t.Fatalf("unchanged tracked key MLAT_USER must be omitted, got %#v", got)
	}
	if len(got) != 0 {
		t.Fatalf("payload should be empty, got %#v", got)
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

	// MLAT_USER is the only tracked key that moves.
	wantUpdate := FieldUpdate{Value: "bob", EditedAt: fixedStamp, EditedBy: "feeder"}
	if !reflect.DeepEqual(got["MLAT_USER"], wantUpdate) {
		t.Errorf("MLAT_USER: want %#v, got %#v", wantUpdate, got["MLAT_USER"])
	}

	// All other tracked keys are omitted.
	for _, k := range []string{"LATITUDE", "LONGITUDE", "ALTITUDE", "MLAT_ENABLED", "MLAT_PRIVATE"} {
		if _, ok := got[k]; ok {
			t.Errorf("unchanged tracked key %s must be omitted, got %#v", k, got[k])
		}
	}

	// Non-tracked key passes through unchanged-value-and-all.
	if got["GAIN"] != "40" {
		t.Errorf("GAIN passthrough: want %q, got %#v", "40", got["GAIN"])
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
