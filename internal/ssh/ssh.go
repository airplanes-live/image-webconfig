// Package ssh maps the apl-ssh helper's JSON envelope into HTTP responses.
// The privileged bash helper (files/usr/local/bin/apl-ssh) owns input
// validation, the chpasswd/passwd lifecycle, the atomic sshd-snippet and
// authorized-key writes, and flock enforcement; this package is the thin Go
// adapter that lets webconfig pipe JSON through the sudoers-pinned argv and
// surface the result to the browser.
//
// It is a status mapper only — the handler forwards the helper's stdout
// verbatim, so this type needs only the discriminator.
package ssh

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Status constants mirror the strings apl-ssh emits in its envelope.
const (
	StatusOK              = "ok"
	StatusApplied         = "applied"
	StatusRejected        = "rejected"
	StatusLockTimeout     = "lock_timeout"
	StatusFilesystemError = "filesystem_error"
	StatusParseError      = "parse_error"
	StatusUsageError      = "usage_error"
)

// Envelope is the minimal shape every apl-ssh verb emits. The full envelope
// carries more per-verb fields (pi_present, password_auth_allowed, reloaded,
// reason, …); the handler forwards the helper's stdout verbatim, so this type
// only needs the discriminator.
type Envelope struct {
	Status string `json:"status"`
}

// Parse extracts the status field from the helper's stdout. apl-ssh's contract
// is that every code path emits a JSON object with a status field, so a parse
// failure or an absent status is a contract violation the handler maps to 500.
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

// HTTPStatus maps a helper status to the HTTP status the browser sees.
//
//	ok | applied                  → 200
//	rejected                      → 400 (validation: short password, bad pubkey)
//	lock_timeout                  → 503
//	parse_error | usage_error     → 400
//	filesystem_error              → 500
//	anything else                 → 500 (contract violation)
func HTTPStatus(s string) int {
	switch s {
	case StatusOK, StatusApplied:
		return http.StatusOK
	case StatusRejected, StatusParseError, StatusUsageError:
		return http.StatusBadRequest
	case StatusLockTimeout:
		return http.StatusServiceUnavailable
	case StatusFilesystemError:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
