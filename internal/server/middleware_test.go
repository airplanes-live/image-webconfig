package server

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLog redirects the stdlib logger to a buffer for the duration of
// the test, with all formatting flags off and an empty prefix so the
// captured output is just the raw Printf payload. Restores the previous
// writer / flags / prefix in t.Cleanup. Tests using this MUST NOT call
// t.Parallel — the stdlib logger is process-global.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	prevPrefix := log.Prefix()
	log.SetOutput(buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	})
	return buf
}

// okHandler is a trivial 200 handler used to drive the middleware.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestRequestLogger_LogsPOST(t *testing.T) {
	buf := captureLog(t)

	h := requestLogger(okHandler)
	req := httptest.NewRequest(http.MethodPost, "/api/foo", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := buf.String()
	if !strings.Contains(got, "method=POST") {
		t.Errorf("expected method=POST, got %q", got)
	}
	if !strings.Contains(got, `path="/api/foo"`) {
		t.Errorf("expected quoted path=%q, got %q", "/api/foo", got)
	}
	if !strings.Contains(got, "status=200") {
		t.Errorf("expected status=200, got %q", got)
	}
	if !strings.Contains(got, "dur_ms=") {
		t.Errorf("expected dur_ms field, got %q", got)
	}
	if !strings.Contains(got, "airplanes-webconfig") {
		t.Errorf("expected airplanes-webconfig prefix, got %q", got)
	}
}

func TestRequestLogger_SilentForGET(t *testing.T) {
	buf := captureLog(t)

	h := requestLogger(okHandler)
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/api/state", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}

	if got := buf.String(); got != "" {
		t.Errorf("expected no log output for GET/HEAD/OPTIONS, got %q", got)
	}
}

func TestRequestLogger_CapturesNon200Status(t *testing.T) {
	buf := captureLog(t)

	unauth := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	h := requestLogger(unauth)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got := buf.String(); !strings.Contains(got, "status=401") {
		t.Errorf("expected status=401, got %q", got)
	}
}

func TestRequestLogger_FirstWriteHeaderWins(t *testing.T) {
	buf := captureLog(t)

	doubleWrite := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.WriteHeader(http.StatusOK) // ignored by net/http and by statusRecorder
	})
	h := requestLogger(doubleWrite)
	req := httptest.NewRequest(http.MethodPost, "/api/foo", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	got := buf.String()
	if !strings.Contains(got, "status=401") {
		t.Errorf("expected status=401, got %q", got)
	}
	if strings.Contains(got, "status=200") {
		t.Errorf("expected no status=200 (first WriteHeader should win), got %q", got)
	}
}

func TestRequestLogger_QuotesPathToBlockControlChars(t *testing.T) {
	buf := captureLog(t)

	h := requestLogger(okHandler)
	// Path with a literal newline — would split the log line in two if
	// emitted unquoted. %q must render it as \n inside double-quotes.
	req := httptest.NewRequest(http.MethodPost, "/api/foo", nil)
	req.URL.Path = "/api/wifi/evil\nfake-log-line"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	got := buf.String()
	if strings.Count(got, "\n") != 1 {
		t.Errorf("expected exactly one newline (end of log line), got %d in %q",
			strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, `\n`) {
		t.Errorf("expected escaped \\n inside quoted path, got %q", got)
	}
}

func TestRequestLogger_CapturesOriginRejection(t *testing.T) {
	buf := captureLog(t)

	// Wrap the full chain so the origin-check 403 flows back through
	// requestLogger's status recorder.
	chain := securityHeaders(requestLogger(requireOriginMatchesHost(okHandler)))
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Host = "feeder.local"
	req.Header.Set("Origin", "http://attacker.example")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected origin-mismatch 403, got %d", rec.Code)
	}
	if got := buf.String(); !strings.Contains(got, "status=403") {
		t.Errorf("expected status=403 from origin rejection, got %q", got)
	}
}
