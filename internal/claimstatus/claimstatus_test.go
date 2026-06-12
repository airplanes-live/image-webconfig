package claimstatus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// --- parseOutput ---------------------------------------------------------

func TestParseOutput(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		wantOK bool
		result string
	}{
		{"valid claimed", `{"schema_version":1,"result":"claimed","owner_present":true}`, true, "claimed"},
		{"valid with trailing newline", "{\"schema_version\":1,\"result\":\"unclaimed\"}\n", true, "unclaimed"},
		{"empty", "", false, ""},
		{"whitespace only", "   \n", false, ""},
		{"not json", "<html>error</html>", false, ""},
		{"wrong schema", `{"schema_version":2,"result":"claimed"}`, false, ""},
		{"missing schema", `{"result":"claimed"}`, false, ""},
		{"empty result", `{"schema_version":1,"result":""}`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := parseOutput([]byte(tc.in))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && out.Result != tc.result {
				t.Fatalf("result = %q, want %q", out.Result, tc.result)
			}
		})
	}
}

func TestParseOutput_Claimability(t *testing.T) {
	t.Run("claimable false with reason", func(t *testing.T) {
		out, ok := parseOutput([]byte(`{"schema_version":1,"result":"unclaimed","claimable":false,"claim_unavailable_reason":"not_seen_feeding"}`))
		if !ok {
			t.Fatal("parse failed")
		}
		if out.Claimable == nil || *out.Claimable {
			t.Errorf("claimable = %v, want false", out.Claimable)
		}
		if out.ClaimUnavailableReason == nil || *out.ClaimUnavailableReason != "not_seen_feeding" {
			t.Errorf("reason = %v, want not_seen_feeding", out.ClaimUnavailableReason)
		}
	})
	t.Run("claimable true", func(t *testing.T) {
		out, ok := parseOutput([]byte(`{"schema_version":1,"result":"unclaimed","claimable":true,"claim_unavailable_reason":null}`))
		if !ok {
			t.Fatal("parse failed")
		}
		if out.Claimable == nil || !*out.Claimable {
			t.Errorf("claimable = %v, want true", out.Claimable)
		}
		if out.ClaimUnavailableReason != nil {
			t.Errorf("reason = %v, want nil", out.ClaimUnavailableReason)
		}
	})
	t.Run("fields absent on older feed CLI", func(t *testing.T) {
		out, ok := parseOutput([]byte(`{"schema_version":1,"result":"unclaimed"}`))
		if !ok {
			t.Fatal("parse failed")
		}
		if out.Claimable != nil {
			t.Errorf("claimable = %v, want nil", out.Claimable)
		}
		if out.ClaimUnavailableReason != nil {
			t.Errorf("reason = %v, want nil", out.ClaimUnavailableReason)
		}
	})
}

// --- Prober --------------------------------------------------------------

func fakeRunner(stdout string, exit int, err error) wexec.CommandRunner {
	return func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{Stdout: []byte(stdout), ExitCode: exit}, err
	}
}

func TestProber_ParsesJSONEvenOnNonZeroExit(t *testing.T) {
	// apl-feed claim status exits 2 on unreachable/error but still prints
	// JSON; the prober must trust stdout, not the exit code.
	p := Prober{Runner: fakeRunner(`{"schema_version":1,"result":"unreachable"}`, 2, errors.New("exit status 2"))}
	out, err := p.Probe(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Result != ResultUnreachable {
		t.Fatalf("result = %q, want unreachable", out.Result)
	}
}

func TestProber_UnavailableOnUnparseable(t *testing.T) {
	// Old feed rejecting the subcommand: non-zero exit, help text on
	// stderr, no JSON on stdout → unavailable + error.
	p := Prober{Runner: fakeRunner("", 2, errors.New("exit status 2"))}
	out, err := p.Probe(context.Background())
	if err == nil {
		t.Fatal("want error for unparseable output")
	}
	if out.Result != ResultUnavailable {
		t.Fatalf("result = %q, want unavailable", out.Result)
	}
}

func TestProber_UnavailableOnExecError(t *testing.T) {
	// Binary missing: Start() fails, ExitCode -1, empty stdout.
	p := Prober{Runner: fakeRunner("", -1, errors.New("exec: not found"))}
	out, _ := p.Probe(context.Background())
	if out.Result != ResultUnavailable {
		t.Fatalf("result = %q, want unavailable", out.Result)
	}
}

// --- Cache ---------------------------------------------------------------

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0).UTC()} }
func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func okOutput(result string) Output {
	b := result == ResultClaimed
	return Output{SchemaVersion: 1, Result: result, OwnerPresent: &b}
}

func TestCache_FreshHitDoesNotReprobe(t *testing.T) {
	clk := newClock()
	var n int32
	probe := func(context.Context) (Output, error) {
		atomic.AddInt32(&n, 1)
		return okOutput(ResultClaimed), nil
	}
	c := NewCache(probe, clk.now)

	r1 := c.Get(context.Background(), "k", time.Minute)
	if r1.Result != ResultClaimed || r1.Stale {
		t.Fatalf("first: %+v", r1)
	}
	clk.advance(30 * time.Second) // within maxAge
	r2 := c.Get(context.Background(), "k", time.Minute)
	if r2.Result != ResultClaimed {
		t.Fatalf("second: %+v", r2)
	}
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("probe count = %d, want 1 (served from cache)", got)
	}
}

func TestCache_ForceRefreshClampedToFloor(t *testing.T) {
	clk := newClock()
	var n int32
	probe := func(context.Context) (Output, error) {
		atomic.AddInt32(&n, 1)
		return okOutput(ResultUnclaimed), nil
	}
	c := NewCache(probe, clk.now)

	c.Get(context.Background(), "k", 0)         // probes
	clk.advance(3 * time.Second)                // < floor
	c.Get(context.Background(), "k", 0)         // force, but floored → cache
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("probe count = %d, want 1 (floor blocks rapid re-probe)", got)
	}
	clk.advance(MinProbeInterval) // now past the floor
	c.Get(context.Background(), "k", 0)
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("probe count = %d, want 2 (floor elapsed)", got)
	}
}

func TestCache_IdentityChangeBypassesFloorAndReprobes(t *testing.T) {
	clk := newClock()
	var n int32
	probe := func(context.Context) (Output, error) {
		atomic.AddInt32(&n, 1)
		return okOutput(ResultUnclaimed), nil
	}
	c := NewCache(probe, clk.now)

	c.Get(context.Background(), "feeder-A", time.Minute)
	clk.advance(time.Second) // well within floor
	c.Get(context.Background(), "feeder-B", time.Minute)
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("probe count = %d, want 2 (identity change forces re-probe)", got)
	}
}

func TestCache_StaleFallbackOnTransientAfterGood(t *testing.T) {
	clk := newClock()
	var calls int32
	probe := func(context.Context) (Output, error) {
		if atomic.AddInt32(&calls, 1) == 1 {
			return okOutput(ResultClaimed), nil
		}
		return Output{SchemaVersion: 1, Result: ResultUnreachable}, nil
	}
	c := NewCache(probe, clk.now)

	first := c.Get(context.Background(), "k", time.Minute)
	if first.Result != ResultClaimed || first.Stale {
		t.Fatalf("first: %+v", first)
	}
	clk.advance(2 * time.Minute) // force a refresh (past maxAge + floor)
	second := c.Get(context.Background(), "k", time.Minute)
	if second.Result != ResultClaimed {
		t.Fatalf("want last-good claimed, got %q", second.Result)
	}
	if !second.Stale || second.Error != ResultUnreachable {
		t.Fatalf("want stale=true error=unreachable, got stale=%v error=%q", second.Stale, second.Error)
	}
	// CheckedAt must reflect the last GOOD probe, not the failed refresh.
	if second.CheckedAt != first.CheckedAt {
		t.Fatalf("stale CheckedAt = %q, want last-good %q", second.CheckedAt, first.CheckedAt)
	}
}

func TestCache_TransientWithNoPriorGoodSurfacedDirectly(t *testing.T) {
	clk := newClock()
	probe := func(context.Context) (Output, error) {
		ra := 42
		return Output{SchemaVersion: 1, Result: ResultRateLimited, RetryAfterSeconds: &ra}, nil
	}
	c := NewCache(probe, clk.now)
	r := c.Get(context.Background(), "k", time.Minute)
	if r.Result != ResultRateLimited || r.Stale {
		t.Fatalf("got %+v, want rate_limited non-stale", r)
	}
	if r.RetryAfterSeconds == nil || *r.RetryAfterSeconds != 42 {
		t.Fatalf("retry_after not surfaced: %+v", r.RetryAfterSeconds)
	}
}

func TestCache_SingleFlightCoalesces(t *testing.T) {
	clk := newClock()
	var n int32
	release := make(chan struct{})
	entered := make(chan struct{}, 16)
	probe := func(context.Context) (Output, error) {
		atomic.AddInt32(&n, 1)
		entered <- struct{}{}
		<-release // hold the probe open until both callers are waiting
		return okOutput(ResultUnclaimed), nil
	}
	c := NewCache(probe, clk.now)

	const N = 8
	var wg sync.WaitGroup
	results := make([]Response, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = c.Get(context.Background(), "k", time.Minute)
		}(i)
	}
	<-entered      // first probe started
	time.Sleep(20 * time.Millisecond) // let the rest pile into the waiter path
	close(release) // let the single probe finish
	wg.Wait()

	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("probe count = %d, want 1 (single-flight)", got)
	}
	for i, r := range results {
		if r.Result != ResultUnclaimed {
			t.Fatalf("result[%d] = %q, want unclaimed", i, r.Result)
		}
	}
}

func TestCache_RateLimitedHonorsRetryAfter(t *testing.T) {
	// A 429 retry_after longer than the floor must hold off the next probe
	// past the floor — even a forced refresh.
	clk := newClock()
	var n int32
	probe := func(context.Context) (Output, error) {
		atomic.AddInt32(&n, 1)
		ra := 90
		return Output{SchemaVersion: 1, Result: ResultRateLimited, RetryAfterSeconds: &ra}, nil
	}
	c := NewCache(probe, clk.now)

	c.Get(context.Background(), "k", 0) // probe 1 → rate_limited, next allowed in 90s
	clk.advance(MinProbeInterval + time.Second) // past the 7s floor, within retry_after
	c.Get(context.Background(), "k", 0)          // forced, but retry_after blocks
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("probe count = %d, want 1 (retry_after must outrank the floor)", got)
	}
	clk.advance(90 * time.Second)
	c.Get(context.Background(), "k", 0)
	if got := atomic.LoadInt32(&n); got != 2 {
		t.Fatalf("probe count = %d, want 2 (retry_after elapsed)", got)
	}
}

func TestCache_NonDefinitiveNotCachedAsGood(t *testing.T) {
	// An `unavailable` (probe-level failure) must not become the cached
	// good value that later refreshes fall back to.
	clk := newClock()
	var calls int32
	probe := func(context.Context) (Output, error) {
		switch atomic.AddInt32(&calls, 1) {
		case 1:
			return Output{Result: ResultUnavailable}, errors.New("boom")
		default:
			return okOutput(ResultClaimed), nil
		}
	}
	c := NewCache(probe, clk.now)

	r1 := c.Get(context.Background(), "k", time.Minute)
	if r1.Result != ResultUnavailable {
		t.Fatalf("first: %+v", r1)
	}
	clk.advance(MinProbeInterval + time.Second)
	r2 := c.Get(context.Background(), "k", time.Minute)
	if r2.Result != ResultClaimed || r2.Stale {
		t.Fatalf("second should re-probe to a fresh definitive verdict, got %+v", r2)
	}
}
