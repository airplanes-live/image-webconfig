// Package configspec is the single source of truth for what feed.env keys
// webconfig is willing to write and how their values must be shaped. Both
// the airplanes-webconfig daemon and the apply-config helper import this
// package so the two binaries cannot drift apart.
package configspec

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// WriteKeys is the set of feed.env keys POST /api/config will accept. Other
// keys returned by GET /api/config are read-only — the UI has no editor for
// them in v1.
var WriteKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"MLAT_USER",
	"MLAT_ENABLED",
	"GAIN",
	"UAT_INPUT",
}

// AllReadKeys mirrors feedenv.ReadKeys; duplicated here so apply-config
// doesn't need to depend on the feedenv package (which has the file IO
// surface). Keep this list manually in sync with feedenv.ReadKeys; add
// a cross-package drift test before changing either list.
var AllReadKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"MLAT_USER",
	"MLAT_ENABLED",
	"INPUT",
	"INPUT_TYPE",
	"GAIN",
	"UAT_INPUT",
}

// universalReject contains every byte that is unsafe to emit unescaped into
// feed.env (which gets shell-sourced by airplanes-feed.sh and -mlat.sh).
// Defense-in-depth: this rejection runs in addition to the per-key shape
// checks so a regex-passing value containing a metachar cannot reach disk.
var universalReject = `"\\$` + "`" + `;&|<>#` + "\n\r\x00'"

// ValidationError carries the offending key for clean HTTP error mapping.
type ValidationError struct {
	Key    string
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Key == "" {
		return "configspec: " + e.Reason
	}
	return fmt.Sprintf("configspec: %s: %s", e.Key, e.Reason)
}

func validationError(key, reason string) error {
	return &ValidationError{Key: key, Reason: reason}
}

// IsValidationError reports whether err originated in configspec validation.
func IsValidationError(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

// CheckUniversal applies the universal-reject scan to a single value. Used
// both for whitelisted-write keys (after per-key validation) and for
// preserved keys (defense-in-depth before re-emitting them).
func CheckUniversal(key, value string) error {
	if i := strings.IndexAny(value, universalReject); i != -1 {
		return validationError(key, fmt.Sprintf("contains forbidden character %q", value[i]))
	}
	return nil
}

// Validate enforces the per-key shape (and universal-reject). Returns nil
// if value is acceptable for the given write-whitelist key.
//
// Empty string is accepted only where it has a defined semantic:
//   - UAT_INPUT="" → 978 disabled
//   - MLAT_USER="" → MLAT name unset (must be paired with MLAT_ENABLED=false)
//
// Other keys reject empty.
func Validate(key, value string) error {
	if !isWriteKey(key) {
		return validationError(key, "not in write whitelist")
	}
	if err := CheckUniversal(key, value); err != nil {
		return err
	}
	switch key {
	case "LATITUDE":
		return validateLatitude(value)
	case "LONGITUDE":
		return validateLongitude(value)
	case "ALTITUDE":
		return validateAltitude(value)
	case "MLAT_USER":
		return validateMlatUser(value)
	case "MLAT_ENABLED":
		return validateMlatEnabled(value)
	case "GAIN":
		return validateGain(value)
	case "UAT_INPUT":
		return validateUATInput(value)
	default:
		// Unreachable given the isWriteKey guard above, but keep the
		// switch exhaustive in case WriteKeys grows.
		return validationError(key, "no validator")
	}
}

// ValidateConsistency enforces cross-key rules on the final merged config
// after each individual key has passed Validate. Today the only cross-key
// rule is the MLAT pair: MLAT_ENABLED=true requires a non-empty MLAT_USER
// (otherwise airplanes-mlat.sh strict-fails with exit 64 and the user sees
// the unit failed without an obvious cause).
//
// Apply-config and server both call this before persisting a merged config
// so an inconsistent state never reaches disk.
func ValidateConsistency(merged map[string]string) error {
	mlatEnabled, hasEnabled := merged["MLAT_ENABLED"]
	mlatUser, hasUser := merged["MLAT_USER"]
	if hasEnabled && mlatEnabled == "true" {
		// MLAT_USER missing or empty + MLAT_ENABLED=true is the inconsistent
		// shape that strict-fails the daemon.
		if !hasUser || mlatUser == "" {
			return validationError("MLAT_USER", "must be non-empty when MLAT_ENABLED=true")
		}
	}
	return nil
}

// Canonicalize returns the normalized on-disk form of a value (e.g. ALTITUDE
// always carries the explicit `m` suffix). Validate must succeed first.
func Canonicalize(key, value string) string {
	switch key {
	case "ALTITUDE":
		// Strip suffix and re-add — already validated to one of m/ft/none.
		v := strings.TrimSuffix(strings.TrimSuffix(value, "m"), "ft")
		if strings.HasSuffix(value, "ft") {
			return v + "ft"
		}
		return v + "m"
	default:
		return value
	}
}

func isWriteKey(key string) bool {
	for _, k := range WriteKeys {
		if k == key {
			return true
		}
	}
	return false
}

func validateLatitude(v string) error {
	f, err := parseFiniteFloat(v)
	if err != nil {
		return validationError("LATITUDE", err.Error())
	}
	if f < -90 || f > 90 {
		return validationError("LATITUDE", "must be in [-90, 90]")
	}
	return nil
}

func validateLongitude(v string) error {
	f, err := parseFiniteFloat(v)
	if err != nil {
		return validationError("LONGITUDE", err.Error())
	}
	if f < -180 || f > 180 {
		return validationError("LONGITUDE", "must be in [-180, 180]")
	}
	return nil
}

var altitudeRE = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)(m|ft)?$`)

func validateAltitude(v string) error {
	m := altitudeRE.FindStringSubmatch(v)
	if m == nil {
		return validationError("ALTITUDE", `must match -?\d+(\.\d+)?(m|ft)?`)
	}
	f, err := parseFiniteFloat(m[1])
	if err != nil {
		return validationError("ALTITUDE", err.Error())
	}
	if f < -1000 || f > 10000 {
		return validationError("ALTITUDE", "must be in [-1000, 10000]")
	}
	return nil
}

var mlatUserRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// validateMlatUser accepts an empty string (MLAT name explicitly cleared,
// must be paired with MLAT_ENABLED=false) or a sanitized identifier of
// 1-64 chars in [A-Za-z0-9_-]. Pairing is enforced at the apply-config
// layer, not here, so a single-key POST can clear the name independently
// of toggling the boolean.
func validateMlatUser(v string) error {
	if v == "" {
		return nil
	}
	if !mlatUserRE.MatchString(v) {
		return validationError("MLAT_USER", `must match [A-Za-z0-9_-]{1,64} or be empty`)
	}
	return nil
}

// validateMlatEnabled accepts only the literal strings "true" and "false".
// MLAT_ENABLED is a boolean toggle on disk: airplanes-mlat.sh and
// apl-feed/status.sh both check the predicate `MLAT_ENABLED == "true"`,
// so any other value (yes/no/1/0/empty) would fall through to disabled.
// Reject those at write time to keep the schema unambiguous.
func validateMlatEnabled(v string) error {
	switch v {
	case "true", "false":
		return nil
	}
	return validationError("MLAT_ENABLED", `must be "true" or "false"`)
}

func validateGain(v string) error {
	switch v {
	case "auto", "min", "max":
		return nil
	}
	f, err := parseFiniteFloat(v)
	if err != nil {
		return validationError("GAIN", err.Error())
	}
	if f < 0 || f > 60 {
		return validationError("GAIN", "must be in [0, 60] or one of auto/min/max")
	}
	return nil
}

func validateUATInput(v string) error {
	// v1: only "" (978 disabled) or the local dump978-fa endpoint.
	switch v {
	case "", "127.0.0.1:30978":
		return nil
	}
	return validationError("UAT_INPUT", `must be "" or "127.0.0.1:30978"`)
}

// parseFiniteFloat rejects empty input, NaN, and ±Inf — strconv.ParseFloat
// happily accepts the literal strings "NaN" and "Inf", which would otherwise
// pass our range checks.
func parseFiniteFloat(v string) (float64, error) {
	if v == "" {
		return 0, errors.New("must not be empty")
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, errors.New("not a valid number")
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errors.New("must be finite")
	}
	return f, nil
}
