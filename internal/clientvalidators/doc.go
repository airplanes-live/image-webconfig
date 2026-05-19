// Package clientvalidators carries no production code. It exists solely as
// a home for the validator-parity test that pins the JS validators in
// web/assets/app.js (delimited by the /* @validator-parity */ markers)
// against their bash twins in:
//
//   - feed/scripts/lib/configure-validators.sh (cross-repo at airplanes-live/feed/dev)
//   - files/usr/local/lib/airplanes/wifi-validators.sh (in-repo)
//
// The test executes the actual shipped JS via a Node subprocess (so a
// JS-only tightening can't slip past review) and the actual shipped bash
// functions via a bash subprocess (so a bash-only tightening can't either).
// Both sides are checked against a shared per-validator input-vector table.
//
// Skip semantics:
//
//   - node not in $PATH: the test is skipped. CI installs Node via
//     actions/setup-node before running `go test ./...`; the test only
//     skips on local runs where the dev box lacks Node.
//   - TESTING_OFFLINE=1, network fetch failure, or non-200 response from
//     raw.githubusercontent.com: the feed-cross-repo sub-tests are skipped
//     for that run. The in-repo wifi sub-tests still run.
//
// Go test caching can hide live drift in feed/dev between runs. CI runs
// `go test ./...` over a fresh tree on every push, so the cache is cold
// per-PR; for local manual repeats, prefer
// `go test -count=1 ./internal/clientvalidators/...`.
package clientvalidators
