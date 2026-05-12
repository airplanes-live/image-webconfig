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
		{"missing status field parses but returns empty", `{"foo":"bar"}`, "", false},
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
