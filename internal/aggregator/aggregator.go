// Package aggregator maps the apl-aggregator helper's JSON envelope into HTTP
// responses. The privileged bash helper
// (files/usr/local/bin/apl-aggregator) owns input validation, on-demand vendor
// acquisition, the systemd lifecycle, and atomic state writes; this package is
// the thin Go adapter that lets webconfig pipe JSON through the sudoers-pinned
// argv and surface the result to the browser.
//
// It is deliberately a status/error mapper only — it does NOT model adapter
// descriptors. The helper's status envelope is forwarded to the SPA verbatim;
// the Go side only needs the envelope discriminators (result / error_code) and
// the protocol version to translate a helper reply into an HTTP status.
package aggregator

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
)

// ProtocolVersion is the apl-aggregator RPC protocol this binary speaks. Every
// helper envelope carries protocol_version; a reply whose version differs is
// rejected as a contract violation. The overlay self-update can swap the
// apl-aggregator helper while an older webconfig is still running (or vice
// versa) mid-update, so the version is checked on every reply rather than
// assumed. Bump in lockstep with the helper's AGG_PROTOCOL_VERSION.
const ProtocolVersion = 1

// error_code values the helper emits on its result:"error" envelopes. Keep in
// sync with the _emit_error call sites in files/usr/local/bin/apl-aggregator.
const (
	CodeRejected           = "rejected"
	CodeNotFound           = "not_found"
	CodeParseError         = "parse_error"
	CodeUsageError         = "usage_error"
	CodeDecoderUnavailable = "decoder_unavailable"
	CodeAcquireFailed      = "acquire_failed"
	CodeSignupFailed       = "signup_failed"
	CodeStateError         = "state_error"
	CodeLockTimeout        = "lock_timeout"
)

// Envelope is the minimal shape every apl-aggregator verb emits. Read verbs
// (status) carry an aggregators array and no result; mutating verbs carry
// result:"ok"; errors carry result:"error" plus error_code and message. The
// handler forwards the helper's stdout verbatim, so this type only needs the
// discriminators and the protocol version.
type Envelope struct {
	ProtocolVersion int             `json:"protocol_version"`
	Result          string          `json:"result"`
	ErrorCode       string          `json:"error_code"`
	Aggregators     json.RawMessage `json:"aggregators"`
}

// ErrProtocolMismatch is returned by Parse when the helper's reply carries a
// protocol_version this binary does not understand — a release-skew condition
// (an overlay flipped the helper out from under the running server, or vice
// versa). The handler treats it as an internal error, not a user-facing one.
var ErrProtocolMismatch = errors.New("aggregator: helper protocol version mismatch")

// Parse extracts the envelope discriminators from the helper's stdout and
// verifies the protocol version. A non-object/empty/null body is a parse error
// (every apl-aggregator code path emits a JSON object); a version mismatch is
// ErrProtocolMismatch. Both are contract violations the handler maps to 500.
func Parse(body []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return Envelope{}, fmt.Errorf("aggregator: parse helper stdout: %w", err)
	}
	if env.ProtocolVersion != ProtocolVersion {
		return env, ErrProtocolMismatch
	}
	return env, nil
}

// HTTPStatus maps a parsed envelope to the HTTP status the browser sees.
// Success is recognised strictly: a mutating verb's result:"ok", the
// fire-and-forget enable's result:"accepted", or the status verb's no-result
// envelope that carries an aggregators array. Any other shape — an unknown
// result string, or an empty result with no aggregators — is a contract
// violation (500), never a silent 200. An error envelope maps by error_code:
//
//	(enable queued: result "accepted")    → 202 (poll status for progress)
//	rejected | parse_error | usage_error  → 400 (bad input / state)
//	not_found                             → 404 (unknown adapter id)
//	decoder_unavailable | lock_timeout    → 503 (transient; retry)
//	acquire_failed | signup_failed        → 502 (vendor upstream failed)
//	state_error                           → 500 (local filesystem/internal)
//	anything else                         → 500 (contract violation)
func HTTPStatus(env Envelope) int {
	switch env.Result {
	case "ok":
		return http.StatusOK
	case "accepted":
		return http.StatusAccepted
	case "":
		// The status verb is the only success path with no result; it always
		// carries an aggregators array (possibly empty, but present). An empty
		// result with no aggregators is a malformed envelope.
		if len(env.Aggregators) > 0 {
			return http.StatusOK
		}
		return http.StatusInternalServerError
	case "error":
		switch env.ErrorCode {
		case CodeRejected, CodeParseError, CodeUsageError:
			return http.StatusBadRequest
		case CodeNotFound:
			return http.StatusNotFound
		case CodeDecoderUnavailable, CodeLockTimeout:
			return http.StatusServiceUnavailable
		case CodeAcquireFailed, CodeSignupFailed:
			return http.StatusBadGateway
		case CodeStateError:
			return http.StatusInternalServerError
		default:
			return http.StatusInternalServerError
		}
	default:
		return http.StatusInternalServerError
	}
}

// idRegexp matches the helper's AGG_ID_RE: a lowercase alphanumeric start
// followed by lowercase alnum, underscore, or hyphen. The Go side gates the
// URL {id} against this before piping anything to the helper so encoded
// slashes, dot-dot, or control bytes fail at the HTTP layer; the helper
// additionally requires a matching descriptor file (returns not_found / 404
// for an unregistered id), so this is a syntactic gate only.
var idRegexp = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ValidID reports whether id is a syntactically valid adapter id. The length
// cap bounds a hostile path segment well below any real adapter id.
func ValidID(id string) bool {
	return len(id) >= 1 && len(id) <= 64 && idRegexp.MatchString(id)
}
