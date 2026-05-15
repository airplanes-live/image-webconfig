// Package configspec used to own the feed.env schema for webconfig: the
// write-key list, per-key validators, cross-key consistency rules, and
// the apply-config invocation contract.
//
// After the apl-feed apply refactor the schema lives in feed/'s
// scripts/lib/feed-env-keys.sh, the validators live in
// scripts/lib/configure-validators.sh, and the writable-key allowlist
// is fetched at boot via `apl-feed schema --json` into
// internal/schemacache. This package now only carries the static
// read-key list that internal/feedenv pins via a drift test — kept here
// to avoid pulling the feedenv package's file I/O surface into callers
// that just want the list shape.
package configspec

// AllReadKeys mirrors feedenv.ReadKeys; duplicated here so the drift
// test in internal/feedenv/feedenv_test.go has something to compare
// against without an import cycle. Will be retired once the schema
// endpoint's readable_keys is consumed end-to-end.
var AllReadKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"GEO_CONFIGURED",
	"MLAT_USER",
	"MLAT_ENABLED",
	"MLAT_PRIVATE",
	"INPUT",
	"INPUT_TYPE",
	"GAIN",
	"UAT_INPUT",
	"DUMP978_SDR_SERIAL",
	"DUMP978_GAIN",
}
