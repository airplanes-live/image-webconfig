package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashGuard_NewRejectsZero(t *testing.T) {
	t.Parallel()
	if _, err := NewHashGuard(0); err == nil {
		t.Fatal("accepted concurrency=0")
	}
	if _, err := NewHashGuard(-1); err == nil {
		t.Fatal("accepted concurrency=-1")
	}
}

func TestHashGuard_TryRunBelowLimit(t *testing.T) {
	t.Parallel()
	g, _ := NewHashGuard(2)
	ran := false
	if err := g.TryRun(func() { ran = true }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
}

func TestHashGuard_TryRunAtLimitReturnsBusy(t *testing.T) {
	t.Parallel()
	g, _ := NewHashGuard(1)

	// Hold the only slot in a goroutine.
	holding := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = g.TryRun(func() {
			close(holding)
			<-release
		})
	}()
	<-holding

	err := g.TryRun(func() { t.Error("fn ran while saturated") })
	if !errors.Is(err, ErrBusy) {
		t.Errorf("err = %v, want ErrBusy", err)
	}
	close(release)
}

func TestHashGuard_RunCtxQueues(t *testing.T) {
	t.Parallel()
	g, _ := NewHashGuard(1)

	holding := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = g.TryRun(func() {
			close(holding)
			<-release
		})
	}()
	<-holding

	// RunCtx should block until release closes the holder.
	var ran atomic.Bool
	done := make(chan struct{})
	go func() {
		_ = g.RunCtx(context.Background(), func() { ran.Store(true) })
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("RunCtx returned before slot released")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	if !ran.Load() {
		t.Fatal("RunCtx fn did not run")
	}
}

func TestHashGuard_RunCtxRespectsCancel(t *testing.T) {
	t.Parallel()
	g, _ := NewHashGuard(1)

	holding := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		_ = g.TryRun(func() {
			close(holding)
			<-release
		})
	}()
	<-holding

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := g.RunCtx(ctx, func() { t.Error("fn ran despite ctx cancel") })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
}

func TestHashGuard_ConcurrentTryRunRespectsLimit(t *testing.T) {
	t.Parallel()
	g, _ := NewHashGuard(2)

	var inFlight, peak atomic.Int32
	var wg sync.WaitGroup
	updatePeak := func(v int32) {
		for {
			cur := peak.Load()
			if v <= cur || peak.CompareAndSwap(cur, v) {
				return
			}
		}
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = g.TryRun(func() {
				v := inFlight.Add(1)
				updatePeak(v)
				time.Sleep(time.Millisecond)
				inFlight.Add(-1)
			})
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Errorf("peak in-flight = %d, want <= 2", peak.Load())
	}
}
