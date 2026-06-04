package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/aggregator"
)

const (
	// aggregatorHelperTimeout caps every apl-aggregator child. All verbs are
	// fast: status/disable/set/reset/export/import touch state files plus at
	// most one systemctl call, and enable is fire-and-forget — it validates
	// synchronously then launches the slow vendor acquire as a detached
	// systemd-run worker and returns {result:"accepted"} immediately. 30s is
	// comfortable headroom; the helper's own flock budget (5s) is the
	// contention cap.
	aggregatorHelperTimeout = 30 * time.Second

	// aggregatorBodyLimit caps the enable/set request bodies (lat/lon/alt plus
	// a small fields object). 4 KiB is far above any realistic payload.
	aggregatorBodyLimit = 4096

	// aggregatorImportBodyLimit caps the import backup blob, matching the
	// helper's own input cap (AGG_STATE_MAX_BYTES = 65536).
	aggregatorImportBodyLimit = 65536
)

// invokeAggregator pipes body through the sudoers-pinned argv, verifies the
// helper's protocol version, and maps the envelope to an HTTP status. Returns
// the helper's stdout verbatim (forwarded to the browser), the HTTP status,
// and a non-nil error only on runner-layer failures (binary missing, empty or
// unparseable stdout, protocol mismatch). The helper's own rejection paths
// land here as (stdout, 4xx/5xx, nil) — like apl-wifi, the child's non-zero
// exit is ignored in favour of the JSON envelope it always emits.
//
// On a parse/protocol failure the helper's raw stdout is NOT included in the
// returned error: the export verb's stdout is a backup blob carrying the FR24
// sharing key, and the error is logged to the journal. Only the byte length is
// surfaced.
func (s *Server) invokeAggregator(ctx context.Context, argv []string, body []byte, timeout time.Duration) ([]byte, int, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, _ := s.stdinRunner(cctx, argv, bytes.NewReader(body))
	if len(res.Stdout) == 0 {
		return nil, 0, fmt.Errorf("apl-aggregator %v: empty stdout (stderr=%q)", argv, strings.TrimSpace(string(res.Stderr)))
	}
	env, perr := aggregator.Parse(res.Stdout)
	if perr != nil {
		return nil, 0, fmt.Errorf("apl-aggregator %v: %w (stdout %d bytes, not logged)", argv, perr, len(res.Stdout))
	}
	return res.Stdout, aggregator.HTTPStatus(env), nil
}

// writeAggregatorResponse forwards the helper's JSON envelope to the browser
// with no-store cache headers and the HTTP status the envelope maps to.
func (s *Server) writeAggregatorResponse(w http.ResponseWriter, body []byte, httpStatus int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(httpStatus)
	_, _ = w.Write(body)
	if len(body) == 0 || body[len(body)-1] != '\n' {
		_, _ = w.Write([]byte("\n"))
	}
}

// readAggregatorBody reads the enable/set request body with the aggregator
// size cap and Content-Type enforcement, returning the raw bytes for
// forwarding (the helper owns field validation). An empty body becomes "{}"
// so the path-id injection always has an object to merge into.
func readAggregatorBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.ContentLength == 0 {
		return []byte("{}"), nil
	}
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		return nil, errBadContentType
	}
	r.Body = http.MaxBytesReader(w, r.Body, aggregatorBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return []byte("{}"), nil
	}
	return body, nil
}

// --- read-only / top-level verbs ------------------------------------------

// handleAggregatorList (GET /api/aggregators) returns the per-adapter status
// descriptors. Read-only; the helper takes no stdin.
func (s *Server) handleAggregatorList(w http.ResponseWriter, r *http.Request) {
	body, httpStatus, err := s.invokeAggregator(r.Context(), s.priv.AggregatorStatus, nil, aggregatorHelperTimeout)
	if err != nil {
		log.Printf("aggregator status: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "aggregator status failed")
		return
	}
	s.writeAggregatorResponse(w, body, httpStatus)
}

// handleAggregatorExport (POST /api/aggregators/export) returns the
// recoverable identities INCLUDING secret values as a backup blob. POST (not
// GET) so it routes through the origin check and never lands in browser
// history — the same posture as the feeder-identity export. no-store plus an
// attachment disposition keep a proxy/browser from caching the secret-bearing
// body. The helper takes no stdin.
func (s *Server) handleAggregatorExport(w http.ResponseWriter, r *http.Request) {
	body, httpStatus, err := s.invokeAggregator(r.Context(), s.priv.AggregatorExport, nil, aggregatorHelperTimeout)
	if err != nil {
		log.Printf("aggregator export: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "aggregator export failed")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="airplanes-aggregators-backup.json"`)
	s.writeAggregatorResponse(w, body, httpStatus)
}

// handleAggregatorImport (POST /api/aggregators/import) seeds identities from a
// backup blob. The helper does the full structural validation (kind / schema /
// key-format) and refuses an enabled adapter; the Go side only enforces the
// Content-Type and the size cap before piping the body through.
func (s *Server) handleAggregatorImport(w http.ResponseWriter, r *http.Request) {
	if !isJSONContentType(r.Header.Get("Content-Type")) {
		writeJSONError(w, http.StatusBadRequest, errBadContentType.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, aggregatorImportBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "backup body too large or unreadable")
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty backup body")
		return
	}
	resp, httpStatus, ierr := s.invokeAggregator(r.Context(), s.priv.AggregatorImport, body, aggregatorHelperTimeout)
	if ierr != nil {
		log.Printf("aggregator import: %v", ierr)
		writeJSONError(w, http.StatusInternalServerError, "aggregator import failed")
		return
	}
	s.writeAggregatorResponse(w, resp, httpStatus)
}

// --- per-adapter mutating verbs -------------------------------------------

// aggregatorMutate is the shared body for the per-adapter verbs
// (enable / disable / set / reset): gate the path id syntactically, optionally
// read the request body, inject the authoritative id from the path, then pipe
// through the verb's argv. enable / set carry a body (lat/lon/alt, fields,
// mlat_enabled); disable / reset ignore the client body and operate on the id
// alone. The helper re-validates the id against its descriptor registry and
// returns not_found / 404 for an unregistered adapter.
//
// There is no Go-side serialization mutex here — like the Wi-Fi handlers, the
// helper's flock at /run/airplanes/aggregator.lock is the cross-process
// serialization point and returns a fast lock_timeout / 503 to a second
// concurrent caller. The detached enable worker holds that flock for the whole
// (minutes-long) vendor acquire, so a mutation issued mid-enable gets a prompt
// lock_timeout rather than blocking an HTTP worker.
func (s *Server) aggregatorMutate(w http.ResponseWriter, r *http.Request, argv []string, timeout time.Duration, readBody, injectGeo bool) {
	id := r.PathValue("id")
	if !aggregator.ValidID(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid aggregator id")
		return
	}
	body := []byte("{}")
	if readBody {
		b, err := readAggregatorBody(w, r)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		body = b
	}
	body, err := injectID(body, id)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if injectGeo {
		body, err = s.injectFeedEnvGeo(body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	resp, httpStatus, ierr := s.invokeAggregator(r.Context(), argv, body, timeout)
	if ierr != nil {
		log.Printf("aggregator %q: %v", id, ierr)
		writeJSONError(w, http.StatusInternalServerError, "aggregator operation failed")
		return
	}
	s.writeAggregatorResponse(w, resp, httpStatus)
}

// injectFeedEnvGeo overwrites the lat/lon/alt in an enable payload with the
// feeder's authoritative location from feed.env (LATITUDE/LONGITUDE/ALTITUDE),
// so the user never re-types coordinates per aggregator and a client cannot
// supply its own. A value that is absent or non-numeric is dropped rather than
// forwarded — the helper's geo validation then rejects with guidance instead
// of acting on a bad coordinate. ALTITUDE in feed.env is already bare metres.
func (s *Server) injectFeedEnvGeo(body []byte) ([]byte, error) {
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(body, &m); err != nil || m == nil {
		return nil, errors.New("request body must be a JSON object")
	}
	var values map[string]string
	if s.feedEnv != nil {
		values, _ = s.feedEnv.ReadAll() // absent/unreadable → inject nothing, helper rejects on missing geo
	}
	for envKey, field := range map[string]string{"LATITUDE": "lat", "LONGITUDE": "lon", "ALTITUDE": "alt"} {
		if raw, ok := values[envKey]; ok {
			if f, perr := strconv.ParseFloat(strings.TrimSpace(raw), 64); perr == nil {
				// json.Marshal rejects NaN/Inf — let those fall through to the
				// delete below rather than emitting a null coordinate.
				if jb, merr := json.Marshal(f); merr == nil {
					m[field] = jb
					continue
				}
			}
		}
		delete(m, field) // never forward a client-supplied or malformed coordinate
	}
	return json.Marshal(m)
}

func (s *Server) handleAggregatorEnable(w http.ResponseWriter, r *http.Request) {
	s.aggregatorMutate(w, r, s.priv.AggregatorEnable, aggregatorHelperTimeout, true, true)
}

func (s *Server) handleAggregatorDisable(w http.ResponseWriter, r *http.Request) {
	s.aggregatorMutate(w, r, s.priv.AggregatorDisable, aggregatorHelperTimeout, false, false)
}

func (s *Server) handleAggregatorSet(w http.ResponseWriter, r *http.Request) {
	s.aggregatorMutate(w, r, s.priv.AggregatorSet, aggregatorHelperTimeout, true, false)
}

func (s *Server) handleAggregatorReset(w http.ResponseWriter, r *http.Request) {
	s.aggregatorMutate(w, r, s.priv.AggregatorReset, aggregatorHelperTimeout, false, false)
}
