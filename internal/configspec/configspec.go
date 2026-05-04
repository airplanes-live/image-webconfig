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
	"USER",
	"GAIN",
	"UAT_INPUT",
}

// AllReadKeys mirrors feedenv.ReadKeys; duplicated here so apply-config
// doesn't need to depend on the feedenv package (which has the file IO
// surface). Keep the two lists in sync via the cross-package test in
// internal/feedenv.
var AllReadKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"USER",
	"INPUT",
	"INPUT_TYPE",
	"NET_OPTIONS",
	"JSON_OPTIONS",
	"MLATSERVER",
	"TARGET",
	"REDUCE_INTERVAL",
	"GAIN",
	"RESULTS",
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
// Empty string is accepted only where it has a defined semantic (currently
// just UAT_INPUT="" → 978 disabled). Other keys reject empty.
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
	case "USER":
		return validateUser(value)
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

var userRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validateUser(v string) error {
	if !userRE.MatchString(v) {
		return validationError("USER", `must match [A-Za-z0-9_-]{1,64}`)
	}
	return nil
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
