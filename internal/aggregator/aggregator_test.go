package aggregator

import (
	"errors"
	"net/http"
	"testing"
)

func TestParse_StatusEnvelope(t *testing.T) {
	t.Parallel()
	env, err := Parse([]byte(`{"protocol_version":1,"aggregators":[]}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if env.Result != "" {
		t.Errorf("status envelope Result = %q, want empty", env.Result)
	}
	if got := HTTPStatus(env); got != http.StatusOK {
		t.Errorf("HTTPStatus(status) = %d, want 200", got)
	}
}

func TestParse_OKEnvelope(t *testing.T) {
	t.Parallel()
	env, err := Parse([]byte(`{"protocol_version":1,"result":"ok","id":"fr24"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := HTTPStatus(env); got != http.StatusOK {
		t.Errorf("HTTPStatus(ok) = %d, want 200", got)
	}
}

func TestParse_AcceptedEnvelope(t *testing.T) {
	t.Parallel()
	// The fire-and-forget enable returns result:"accepted" → 202.
	env, err := Parse([]byte(`{"protocol_version":1,"result":"accepted","step":"starting","id":"fr24","request_id":"abc"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := HTTPStatus(env); got != http.StatusAccepted {
		t.Errorf("HTTPStatus(accepted) = %d, want 202", got)
	}
}

func TestParse_RejectsNonObject(t *testing.T) {
	t.Parallel()
	for _, body := range []string{``, `[]`, `"x"`, `42`, `not json`} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("Parse(%q) err = nil, want non-nil", body)
		}
	}
}

func TestParse_ProtocolMismatch(t *testing.T) {
	t.Parallel()
	// A newer helper, a missing version (treated as 0), and null all fail.
	for _, body := range []string{
		`{"protocol_version":2,"result":"ok","id":"fr24"}`,
		`{"result":"ok","id":"fr24"}`,
		`null`,
	} {
		_, err := Parse([]byte(body))
		if !errors.Is(err, ErrProtocolMismatch) {
			t.Errorf("Parse(%q) err = %v, want ErrProtocolMismatch", body, err)
		}
	}
}

func TestHTTPStatus_ErrorCodeMapping(t *testing.T) {
	t.Parallel()
	cases := map[string]int{
		CodeRejected:           http.StatusBadRequest,
		CodeParseError:         http.StatusBadRequest,
		CodeUsageError:         http.StatusBadRequest,
		CodeNotFound:           http.StatusNotFound,
		CodeDecoderUnavailable: http.StatusServiceUnavailable,
		CodeLockTimeout:        http.StatusServiceUnavailable,
		CodeAcquireFailed:      http.StatusBadGateway,
		CodeSignupFailed:       http.StatusBadGateway,
		CodeStateError:         http.StatusInternalServerError,
		"some_future_code":     http.StatusInternalServerError,
	}
	for code, want := range cases {
		env := Envelope{ProtocolVersion: ProtocolVersion, Result: "error", ErrorCode: code}
		if got := HTTPStatus(env); got != want {
			t.Errorf("HTTPStatus(error_code=%q) = %d, want %d", code, got, want)
		}
	}
}

func TestHTTPStatus_StrictSuccess(t *testing.T) {
	t.Parallel()
	// status verb: empty result with an aggregators array → 200.
	statusEnv, err := Parse([]byte(`{"protocol_version":1,"aggregators":[{"id":"fr24"}]}`))
	if err != nil {
		t.Fatalf("Parse status: %v", err)
	}
	if got := HTTPStatus(statusEnv); got != http.StatusOK {
		t.Errorf("status envelope → %d, want 200", got)
	}
	// empty result with NO aggregators → contract violation 500.
	if got := HTTPStatus(Envelope{ProtocolVersion: ProtocolVersion}); got != http.StatusInternalServerError {
		t.Errorf("empty result, no aggregators → %d, want 500", got)
	}
	// unknown result string → contract violation 500.
	if got := HTTPStatus(Envelope{ProtocolVersion: ProtocolVersion, Result: "bogus"}); got != http.StatusInternalServerError {
		t.Errorf("unknown result → %d, want 500", got)
	}
}

func TestValidID(t *testing.T) {
	t.Parallel()
	valid := []string{"fr24", "piaware", "a", "adsb-fi", "radar_box", "x9"}
	for _, id := range valid {
		if !ValidID(id) {
			t.Errorf("ValidID(%q) = false, want true", id)
		}
	}
	invalid := []string{
		"",              // empty
		"-fr24",         // leading hyphen
		"_x",            // leading underscore
		"FR24",          // uppercase
		"fr 24",         // space
		"fr/24",         // slash
		"fr..24",        // dots
		"../etc/passwd", // traversal
		"fr24\n",        // newline
	}
	for _, id := range invalid {
		if ValidID(id) {
			t.Errorf("ValidID(%q) = true, want false", id)
		}
	}
	// Over the length cap.
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if ValidID(string(long)) {
		t.Errorf("ValidID(65 chars) = true, want false")
	}
}
