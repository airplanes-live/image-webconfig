// Package feedenv reads the feeder's effective configuration through
// `apl-feed config show --json`. The feed CLI owns feed.env parsing,
// quoting rules, and the readable-key registry; webconfig consumes the
// JSON envelope instead of carrying its own parser — one parser, owned
// by the writer.
package feedenv

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// DefaultArgv is the production invocation. Read-only and unprivileged —
// no sudo, so no sudoers entry (same posture as the schema fetch).
var DefaultArgv = []string{"/usr/local/bin/apl-feed", "config", "show", "--json"}

// APIReadKeys is webconfig's GET /api/config surface filter: which of
// the CLI's readable keys the HTTP API exposes. It is NOT a parser
// allowlist — the feed CLI decides what is readable; this list decides
// what reaches the v1 UI surface. Brand endpoints (MLATSERVER, TARGET),
// readsb tuning (NET_OPTIONS, JSON_OPTIONS, REDUCE_INTERVAL), the
// mlat-client result-output bundle (RESULTS, RESULTS2-4), and the
// backend pointer APL_FEED_WEBSITE_URL stay out: those are advanced or
// internal knobs, not part of the v1 UI surface, even where the CLI
// reports them.
var APIReadKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"GEO_CONFIGURED",
	"MLAT_USER",
	"MLAT_ENABLED",
	"MLAT_PRIVATE",
	"REPORT_STATUS",
	"REMOTE_CONFIG_ENABLED",
	"INPUT",
	"INPUT_TYPE",
	"GAIN",
	"READSB_SDR_SERIAL",
	"UAT_INPUT",
	"DUMP978_SDR_SERIAL",
	"DUMP978_GAIN",
}

// readTimeout caps each config-show exec. Reads happen on live request
// paths (GET /api/config, the POST metadata-gating pre-read under
// configMu, aggregator geo injection), so the budget is tighter than
// schemacache's boot-time 10s.
const readTimeout = 5 * time.Second

// Reader execs the config-show argv. The zero value works: a nil Exec
// falls back to wexec.RealRunner and an empty Argv to DefaultArgv, so a
// bare &Reader{} cannot silently read nothing.
type Reader struct {
	Exec wexec.CommandRunner
	Argv []string
}

// New returns a Reader on the production runner + argv.
func New() *Reader { return &Reader{Exec: wexec.RealRunner, Argv: DefaultArgv} }

// showJSON mirrors the wire shape emitted by `apl-feed config show
// --json`: schema_version=1 guarantees one entry per readable key —
// string when present (empty string for explicitly-empty), null when
// the key is absent from feed.env. Values stays raw so a missing object
// is distinguishable from an empty one.
type showJSON struct {
	SchemaVersion int             `json:"schema_version"`
	Values        json.RawMessage `json:"values"`
}

// ReadAll returns the feeder configuration filtered to APIReadKeys.
// Keys the CLI reports as null are omitted from the map; explicitly-
// empty values are kept as "" — callers distinguish the two (metadata
// gating, aggregator geo injection).
func (r *Reader) ReadAll(ctx context.Context) (map[string]string, error) {
	values, err := r.values(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(APIReadKeys))
	for _, k := range APIReadKeys {
		if v, ok := values[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// WebsiteURL returns APL_FEED_WEBSITE_URL, or "" when the key is absent
// or the read fails — the caller falls back to the production default,
// matching apl-feed's _resolve_website_url posture. Reads the
// unfiltered value set: the key is deliberately NOT in APIReadKeys
// (internal backend pointer, not a /api/config surface).
func (r *Reader) WebsiteURL(ctx context.Context) string {
	values, err := r.values(ctx)
	if err != nil {
		return ""
	}
	return values["APL_FEED_WEBSITE_URL"]
}

// values execs config show and returns the unfiltered present-key map
// (null entries dropped). Rejects any envelope that violates the
// schema_version=1 contract rather than guessing at its meaning.
func (r *Reader) values(ctx context.Context) (map[string]string, error) {
	exec := r.Exec
	if exec == nil {
		exec = wexec.RealRunner
	}
	argv := r.Argv
	if len(argv) == 0 {
		argv = DefaultArgv
	}
	cctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	res, err := exec(cctx, argv)
	if err != nil {
		return nil, fmt.Errorf("config show: %w (stderr=%q exit=%d)", err, res.Stderr, res.ExitCode)
	}
	var doc showJSON
	if err := json.Unmarshal(res.Stdout, &doc); err != nil {
		return nil, fmt.Errorf("config show parse: %w (body=%q)", err, res.Stdout)
	}
	if doc.SchemaVersion != 1 {
		return nil, fmt.Errorf("config show schema_version: unsupported %d (expected 1)", doc.SchemaVersion)
	}
	// json.Unmarshal leaves the map nil on a literal `null` without
	// erroring, so a null `values` must be caught alongside a missing one.
	if len(doc.Values) == 0 || string(doc.Values) == "null" {
		return nil, fmt.Errorf("config show: missing values object (body=%q)", res.Stdout)
	}
	// map[string]*string rejects every entry that is neither string nor
	// null — numbers, objects, arrays, bools are contract violations.
	var raw map[string]*string
	if err := json.Unmarshal(doc.Values, &raw); err != nil {
		return nil, fmt.Errorf("config show values: %w (body=%q)", err, res.Stdout)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if v == nil {
			continue // null: key absent from feed.env
		}
		out[k] = *v
	}
	return out, nil
}
