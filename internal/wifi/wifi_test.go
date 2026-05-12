package wifi

import (
	"net/http"
	"testing"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantStat  string
		wantError bool
	}{
		{"applied", `{"status":"applied","changed":["x"]}`, StatusApplied, false},
		{"no_change", `{"status":"no_change"}`, StatusNoChange, false},
		{"rejected with errors", `{"status":"rejected","errors":{"ssid":"too long"}}`, StatusRejected, false},
		{"test_failed", `{"status":"test_failed","reason":"auth_failed"}`, StatusTestFailed, false},
		{"lock_timeout", `{"status":"lock_timeout","message":"busy"}`, StatusLockTimeout, false},
		{"empty body is an error", ``, "", true},
		{"not an object", `[1,2,3]`, "", true},
		{"missing status field is a contract violation", `{"foo":"bar"}`, "", true},
		{"explicit empty status is a contract violation", `{"status":""}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.body))
			if tc.wantError && err == nil {
				t.Fatalf("want error, got nil; status=%q", got)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantStat {
				t.Fatalf("status: got %q want %q", got, tc.wantStat)
			}
		})
	}
}

func TestValidID(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"airplanes-config-wifi", true},
		{"airplanes-wifi-home", true},
		{"airplanes-wifi-cafenet-2", true},
		{"airplanes-wifi-0", true},
		{"airplanes-wifi-a" + repeat("a", 40), true},
		{"airplanes-wifi-a" + repeat("a", 41), false},
		{"", false},
		{"airplanes-wifi-", false},
		{"airplanes-wifi--double", false},
		{"airplanes-wifi-UPPER", false},
		{"airplanes-wifi-with space", false},
		{"airplanes-wifi-with/slash", false},
		{"airplanes-wifi-../etc", false},
		{"airplanes-wifi-..", false},
		{"airplanes-wifi-with\nnewline", false},
		{"foreign-net", false},
		{"airplanes-config-wifi-extra", false},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			if got := ValidID(tc.id); got != tc.want {
				t.Fatalf("ValidID(%q) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func repeat(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

func TestHTTPStatus(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{StatusApplied, http.StatusOK},
		{StatusNoChange, http.StatusOK},
		{StatusOK, http.StatusOK},
		{StatusTestPassed, http.StatusOK},
		{StatusRejected, http.StatusBadRequest},
		{StatusTestFailed, http.StatusConflict},
		{StatusLockTimeout, http.StatusServiceUnavailable},
		{StatusParseError, http.StatusBadRequest},
		{StatusUsageError, http.StatusBadRequest},
		{StatusFilesystemError, http.StatusInternalServerError},
		{"surprise_status", http.StatusInternalServerError},
		{"", http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := HTTPStatus(tc.in); got != tc.want {
				t.Fatalf("HTTPStatus(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
