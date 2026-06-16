package ssh

import (
	"net/http"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
	got, err := Parse([]byte(`{"status":"applied","reloaded":true}`))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if got != "applied" {
		t.Fatalf("status = %q, want applied", got)
	}
}

func TestParse_RejectsMissingStatus(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(`{"pi_present":true}`)); err == nil {
		t.Fatal("expected error for envelope without status")
	}
}

func TestParse_RejectsNonObject(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("expected error for non-JSON body")
	}
}

func TestHTTPStatus(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		"ok":               http.StatusOK,
		"applied":          http.StatusOK,
		"rejected":         http.StatusBadRequest,
		"parse_error":      http.StatusBadRequest,
		"usage_error":      http.StatusBadRequest,
		"lock_timeout":     http.StatusServiceUnavailable,
		"filesystem_error": http.StatusInternalServerError,
		"surprise":         http.StatusInternalServerError,
	}
	for status, want := range cases {
		if got := HTTPStatus(status); got != want {
			t.Errorf("HTTPStatus(%q) = %d, want %d", status, got, want)
		}
	}
}
