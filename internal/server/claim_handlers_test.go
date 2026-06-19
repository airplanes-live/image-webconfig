package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// rotateResp mirrors claimRotateResponse for decoding in tests.
type rotateResp struct {
	Status  string `json:"status"`
	Version *int   `json:"version"`
	Error   string `json:"error"`
	Message string `json:"message"`
}

func decodeRotate(t *testing.T, r *http.Response) rotateResp {
	t.Helper()
	var got rotateResp
	if err := json.Unmarshal(readBody(t, r), &got); err != nil {
		t.Fatalf("decode rotate response: %v", err)
	}
	return got
}

// rotateArgv reports whether an argv is the rotate wrapper invocation
// (the stub priv pins it to a trailing "claim-rotate" token).
func rotateArgv(argv []string) bool {
	return len(argv) > 0 && argv[len(argv)-1] == "claim-rotate"
}

func rotateInvoked(h *writeHarness) bool {
	for _, c := range h.callsCopy() {
		if rotateArgv(c) {
			return true
		}
	}
	return false
}

func TestClaimRotate_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t)
	c := httpClient(t)
	r := postJSON(t, c, ts.URL+"/api/claim/rotate", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.StatusCode)
	}
}

func TestClaimRotate_RotatedInvokesWrapperAndReturnsVersion(t *testing.T) {
	h := newWriteHarness(t)
	h.runnerResultFor = func(argv []string) wexec.Result {
		if rotateArgv(argv) {
			return wexec.Result{Stdout: []byte(`{"schema_version":1,"result":"rotated","version":7,"error":null,"detail":null}`)}
		}
		return wexec.Result{}
	}

	r := postJSON(t, h.client, h.ts.URL+"/api/claim/rotate", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	got := decodeRotate(t, r)
	if got.Status != "rotated" || got.Version == nil || *got.Version != 7 {
		t.Fatalf("response = %+v, want rotated v7", got)
	}
	// The privileged wrapper argv (not bare apl-feed) must be what hit sudo.
	if !rotateInvoked(h) {
		t.Fatalf("rotate wrapper argv not invoked; calls=%v", h.callsCopy())
	}
}

func TestClaimRotate_StructuredErrorsMapStatus(t *testing.T) {
	cases := []struct {
		name       string
		code       string
		wantStatus int
		wantLabel  string
	}{
		{"rejected", "rotation_rejected", http.StatusConflict, "error"},
		{"deadline", "deadline_exceeded", http.StatusConflict, "pending"},
		{"blocked", "blocked", http.StatusLocked, "error"},
		{"reset_locked", "reset_locked", http.StatusLocked, "error"},
		{"network", "network", http.StatusBadGateway, "error"},
		{"unexpected", "unexpected_status", http.StatusBadGateway, "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newWriteHarness(t)
			// apl-feed exits non-zero on a structured error; the handler must
			// still trust the parsed JSON over the exit code.
			h.runnerErrFor = func(argv []string) error {
				if rotateArgv(argv) {
					return errors.New("exit status 1")
				}
				return nil
			}
			h.runnerResultFor = func(argv []string) wexec.Result {
				if rotateArgv(argv) {
					return wexec.Result{
						Stdout: []byte(`{"schema_version":1,"result":"error","version":null,"error":"` + tc.code + `","detail":"boom"}`),
					}
				}
				return wexec.Result{}
			}

			r := postJSON(t, h.client, h.ts.URL+"/api/claim/rotate", map[string]any{})
			defer r.Body.Close()
			if r.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", r.StatusCode, tc.wantStatus)
			}
			got := decodeRotate(t, r)
			if got.Status != tc.wantLabel || got.Error != tc.code {
				t.Fatalf("response = %+v, want status=%q error=%q", got, tc.wantLabel, tc.code)
			}
		})
	}
}

func TestClaimRotate_NoPayloadNonZeroMaps500(t *testing.T) {
	// A precondition / local-IO die: non-zero exit, reason on stderr only,
	// no JSON on stdout. The handler can't trust an exit code alone, so this
	// is a generic 500, not a misleading structured status.
	h := newWriteHarness(t)
	h.runnerErrFor = func(argv []string) error {
		if rotateArgv(argv) {
			return errors.New("exit status 1")
		}
		return nil
	}
	h.runnerResultFor = func(argv []string) wexec.Result {
		if rotateArgv(argv) {
			return wexec.Result{Stderr: []byte("no active claim secret at /etc/airplanes/...\n")}
		}
		return wexec.Result{}
	}

	r := postJSON(t, h.client, h.ts.URL+"/api/claim/rotate", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestClaimRotate_MalformedJSONMaps500(t *testing.T) {
	h := newWriteHarness(t)
	h.runnerResultFor = func(argv []string) wexec.Result {
		if rotateArgv(argv) {
			return wexec.Result{Stdout: []byte("not json at all")}
		}
		return wexec.Result{}
	}

	r := postJSON(t, h.client, h.ts.URL+"/api/claim/rotate", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", r.StatusCode)
	}
}

func TestClaimRotate_TimeoutMapsUnknown(t *testing.T) {
	// Shrink the operation budget and make the runner outlive it, so the
	// handler's context fires (CommandContext would SIGKILL the child) and
	// the response is the unknown-state verdict, not a confident result.
	orig := claimRotateTimeout
	claimRotateTimeout = 30 * time.Millisecond
	t.Cleanup(func() { claimRotateTimeout = orig })

	h := newWriteHarness(t)
	h.runnerBlockUntilCtxDone = true

	r := postJSON(t, h.client, h.ts.URL+"/api/claim/rotate", map[string]any{})
	defer r.Body.Close()
	if r.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", r.StatusCode)
	}
	if got := decodeRotate(t, r); got.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", got.Status)
	}
}
