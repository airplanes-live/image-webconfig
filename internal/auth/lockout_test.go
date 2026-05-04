package auth

import (
	"testing"
	"time"
)

func newTestLockout(now *time.Time) *Lockout {
	l := NewLockout(5, time.Minute, 15*time.Minute)
	l.now = func() time.Time { return *now }
	return l
}

func TestLockout_BelowThresholdNotLocked(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	l := newTestLockout(&now)

	for i := 0; i < 4; i++ {
		locked, _ := l.RecordFailure()
		if locked {
			t.Fatalf("locked after %d failures, want unlocked", i+1)
		}
	}
	if locked, _ := l.Locked(); locked {
		t.Fatal("Locked() reported locked after 4 failures")
	}
}

func TestLockout_ThresholdLocks(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	l := newTestLockout(&now)

	for i := 0; i < 5; i++ {
		l.RecordFailure()
	}
	locked, until := l.Locked()
	if !locked {
		t.Fatal("not locked after 5 failures")
	}
	if want := now.Add(15 * time.Minute); !until.Equal(want) {
		t.Errorf("lockedUntil = %v, want %v", until, want)
	}
}

func TestLockout_ExpiresAfterDuration(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	l := newTestLockout(&now)

	for i := 0; i < 5; i++ {
		l.RecordFailure()
	}
	now = now.Add(15*time.Minute + time.Second)
	if locked, _ := l.Locked(); locked {
		t.Fatal("still locked past duration")
	}
}

func TestLockout_OldFailuresDropOutOfWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	l := newTestLockout(&now)

	for i := 0; i < 4; i++ {
		l.RecordFailure()
	}
	now = now.Add(2 * time.Minute) // past window
	locked, _ := l.RecordFailure()
	if locked {
		t.Fatal("locked when prior failures should have dropped out of window")
	}
}

func TestLockout_ResetClearsState(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	l := newTestLockout(&now)

	for i := 0; i < 5; i++ {
		l.RecordFailure()
	}
	l.Reset()
	if locked, _ := l.Locked(); locked {
		t.Fatal("still locked after Reset")
	}
}

func TestLockout_DoesNotExtendActiveLockout(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	l := newTestLockout(&now)

	for i := 0; i < 5; i++ {
		l.RecordFailure()
	}
	_, originalUntil := l.Locked()
	now = now.Add(time.Minute) // still locked
	l.RecordFailure()
	_, newUntil := l.Locked()
	if !originalUntil.Equal(newUntil) {
		t.Fatalf("RecordFailure during lock extended expiry from %v to %v", originalUntil, newUntil)
	}
}
