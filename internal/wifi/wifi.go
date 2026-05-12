// Package wifi maps the apl-wifi helper's JSON envelope into HTTP responses.
// The helper owns input validation, atomic keyfile writes, lock-out
// enforcement, and the connect-before-save test flow; this package is the
// thin Go adapter that lets webconfig pipe JSON through the sudoers-pinned
// argv and surface the result to the browser.
package wifi

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
)

// Status constants mirror the strings apl-wifi emits in its envelope.
const (
	StatusApplied         = "applied"
	StatusNoChange        = "no_change"
	StatusOK              = "ok"
	StatusRejected        = "rejected"
	StatusTestFailed      = "test_failed"
	StatusTestPassed      = "test_passed"
	StatusLockTimeout     = "lock_timeout"
	StatusFilesystemError = "filesystem_error"
	StatusParseError      = "parse_error"
	StatusUsageError      = "usage_error"
)

// Envelope is the minimal shape every apl-wifi subcommand emits. The full
// envelope can carry additional fields per subcommand (id, uuid, networks,
// errors, …); the handler forwards the helper's stdout verbatim, so this
// type only needs the discriminator.
type Envelope struct {
	Status string `json:"status"`
}

// Parse extracts the status field from the helper's stdout. Returns an
// empty status + error if the bytes are not a JSON object or the field is
// absent — apl-wifi's contract is that every code path emits a valid
// envelope, so a failure here is a contract violation.
func Parse(body []byte) (string, error) {
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", err
	}
	if env.Status == "" {
		return "", errors.New("envelope missing required \"status\" field")
	}
	return env.Status, nil
}

// idRegexp matches the same managed-id shape apl-wifi accepts:
// `airplanes-config-wifi` (the first-run bootstrap profile) or
// `airplanes-wifi-<slug>` where the slug is 1-41 lowercase alnum-or-hyphen
// chars starting alphanumerically. The Go side gates the URL `{id}` against
// this before piping anything to the helper so attempts at encoded slashes,
// dot-dot, or control bytes fail at the HTTP layer.
var idRegexp = regexp.MustCompile(`^(airplanes-config-wifi|airplanes-wifi-[a-z0-9][a-z0-9-]{0,40})$`)

// ValidID reports whether id is a syntactically valid managed Wi-Fi profile id.
func ValidID(id string) bool {
	return idRegexp.MatchString(id)
}

// HTTPStatus maps a helper status to the HTTP status the browser sees.
//
//   applied | no_change | ok | test_passed → 200
//   rejected                                → 400 (validation / state)
//   test_failed                             → 409 (data valid; NM refused)
//   lock_timeout                            → 503
//   parse_error | usage_error               → 400
//   filesystem_error                        → 500
//   anything else                           → 500 (contract violation)
func HTTPStatus(s string) int {
	switch s {
	case StatusApplied, StatusNoChange, StatusOK, StatusTestPassed:
		return http.StatusOK
	case StatusRejected:
		return http.StatusBadRequest
	case StatusTestFailed:
		return http.StatusConflict
	case StatusLockTimeout:
		return http.StatusServiceUnavailable
	case StatusParseError, StatusUsageError:
		return http.StatusBadRequest
	case StatusFilesystemError:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
