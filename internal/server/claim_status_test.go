package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	"github.com/airplanes-live/image-webconfig/internal/claimstatus"
	"github.com/airplanes-live/image-webconfig/internal/feedenv/feedenvtest"
	"github.com/airplanes-live/image-webconfig/internal/identity"
)

func TestParseClaimStatusMaxAge(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", defaultClaimStatusMaxAge},
		{"garbage", defaultClaimStatusMaxAge},
		{"0", 0},
		{"-5", 0},
		{"30", 30 * time.Second},
		{"99999", 3600 * time.Second}, // capped
	}
	for _, tc := range cases {
		if got := parseClaimStatusMaxAge(tc.raw); got != tc.want {
			t.Errorf("parseClaimStatusMaxAge(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// claimStatusServer builds a minimal authed-capable server wired with a
// claimstatus cache over the given probe. dir is the caller's TempDir so
// the test can mutate the identity files to exercise cache keying.
func claimStatusServer(t *testing.T, dir string, probe claimstatus.ProbeFunc) *httptest.Server {
	t.Helper()
	guard, err := auth.NewHashGuard(2)
	if err != nil {
		t.Fatal(err)
	}
	idp := identity.Paths{
		FeederIDFile:     filepath.Join(dir, "feeder-id"),
		ClaimSecretFile:  filepath.Join(dir, "feeder-claim-secret"),
		ClaimVersionFile: filepath.Join(dir, "feeder-claim-secret.version"),
	}
	_ = os.WriteFile(idp.FeederIDFile, []byte("test-feeder-id\n"), 0o644)
	_ = os.WriteFile(idp.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\n"), 0o640)
	_ = os.WriteFile(idp.ClaimVersionFile, []byte("1\n"), 0o640)

	h := New(Deps{
		Version:      "test",
		Store:        auth.NewPasswordStore(filepath.Join(dir, "password.hash")),
		Sessions:     auth.NewSessions(time.Hour),
		Lockout:      auth.NewLockout(5, time.Minute, 15*time.Minute),
		Guard:        guard,
		Argon2Params: fastTestParams,
		Identity:     identity.NewReader(idp, feedenvtest.Reader(nil)),
		ClaimStatus:  claimstatus.NewCache(probe, nil),
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestClaimStatus_RequiresAuth(t *testing.T) {
	t.Parallel()
	ts := claimStatusServer(t, t.TempDir(), func(context.Context) (claimstatus.Output, error) {
		return claimstatus.Output{SchemaVersion: 1, Result: claimstatus.ResultUnclaimed}, nil
	})
	resp := mustGetDefault(t, ts.URL+"/api/claim/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestClaimStatus_AuthedReturnsVerdict(t *testing.T) {
	t.Parallel()
	owner := false
	ts := claimStatusServer(t, t.TempDir(), func(context.Context) (claimstatus.Output, error) {
		return claimstatus.Output{SchemaVersion: 1, Result: claimstatus.ResultUnclaimed, OwnerPresent: &owner}, nil
	})
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/claim/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got claimstatus.Response
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Result != claimstatus.ResultUnclaimed {
		t.Errorf("result = %q, want unclaimed", got.Result)
	}
	if got.OwnerPresent == nil || *got.OwnerPresent {
		t.Errorf("owner_present = %v, want false", got.OwnerPresent)
	}
	if got.CheckedAt == "" {
		t.Errorf("checked_at empty")
	}
}

func TestClaimStatus_ClaimabilityRoundTrips(t *testing.T) {
	t.Parallel()
	owner := false
	claimable := false
	reason := "not_seen_feeding"
	ts := claimStatusServer(t, t.TempDir(), func(context.Context) (claimstatus.Output, error) {
		return claimstatus.Output{
			SchemaVersion:          1,
			Result:                 claimstatus.ResultUnclaimed,
			OwnerPresent:           &owner,
			Claimable:              &claimable,
			ClaimUnavailableReason: &reason,
		}, nil
	})
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/claim/status")
	defer resp.Body.Close()
	var got claimstatus.Response
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Claimable == nil || *got.Claimable {
		t.Errorf("claimable = %v, want false", got.Claimable)
	}
	if got.ClaimUnavailableReason == nil || *got.ClaimUnavailableReason != "not_seen_feeding" {
		t.Errorf("claim_unavailable_reason = %v, want not_seen_feeding", got.ClaimUnavailableReason)
	}
}

func TestClaimStatus_ClaimabilityOmittedWhenAbsent(t *testing.T) {
	t.Parallel()
	owner := false
	ts := claimStatusServer(t, t.TempDir(), func(context.Context) (claimstatus.Output, error) {
		return claimstatus.Output{SchemaVersion: 1, Result: claimstatus.ResultUnclaimed, OwnerPresent: &owner}, nil
	})
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/claim/status")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "claimable") {
		t.Errorf("claimable key present in body despite absent probe field: %s", body)
	}
}

func TestClaimStatus_NilCacheReturns503(t *testing.T) {
	t.Parallel()
	// newTestServer wires no ClaimStatus cache; the handler must degrade
	// to 503 rather than panic.
	ts, _ := newTestServer(t)
	c := authedClient(t, ts)
	resp := mustGet(t, c, ts.URL+"/api/claim/status")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}

func TestClaimStatus_IdentityChangeReprobes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var n int32
	probe := func(context.Context) (claimstatus.Output, error) {
		owner := atomic.AddInt32(&n, 1) >= 2 // flips claimed on the 2nd probe
		result := claimstatus.ResultUnclaimed
		if owner {
			result = claimstatus.ResultClaimed
		}
		return claimstatus.Output{SchemaVersion: 1, Result: result, OwnerPresent: &owner}, nil
	}
	ts := claimStatusServer(t, dir, probe)
	c := authedClient(t, ts)

	first := mustGet(t, c, ts.URL+"/api/claim/status")
	var r1 claimstatus.Response
	_ = json.NewDecoder(first.Body).Decode(&r1)
	first.Body.Close()
	if r1.Result != claimstatus.ResultUnclaimed {
		t.Fatalf("first verdict = %q, want unclaimed", r1.Result)
	}

	// A within-window re-request must be served from cache (same identity).
	again := mustGet(t, c, ts.URL+"/api/claim/status")
	again.Body.Close()
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("probe count = %d after cached re-request, want 1", got)
	}

	// Bump the claim-secret version: the identity fingerprint changes, so
	// the cache key changes and the next request re-probes despite the floor.
	if err := os.WriteFile(filepath.Join(dir, "feeder-claim-secret.version"), []byte("2\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	third := mustGet(t, c, ts.URL+"/api/claim/status")
	var r3 claimstatus.Response
	_ = json.NewDecoder(third.Body).Decode(&r3)
	third.Body.Close()
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("probe count = %d after identity change, want 2", got)
	}
	if r3.Result != claimstatus.ResultClaimed {
		t.Fatalf("post-change verdict = %q, want claimed", r3.Result)
	}
}
