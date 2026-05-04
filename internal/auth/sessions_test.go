package auth

import (
	"testing"
	"time"
)

func TestSessions_IssueValidateRoundtrip(t *testing.T) {
	t.Parallel()
	s := NewSessions(time.Hour)
	token, _, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(token); err != nil {
		t.Fatalf("validate freshly-issued token: %v", err)
	}
}

func TestSessions_UnknownToken(t *testing.T) {
	t.Parallel()
	s := NewSessions(time.Hour)
	if _, err := s.Validate("never-issued"); err == nil {
		t.Fatal("Validate(unknown) returned no error")
	}
	if _, err := s.Validate(""); err == nil {
		t.Fatal("Validate(empty) returned no error")
	}
}

func TestSessions_TokensAreUnique(t *testing.T) {
	t.Parallel()
	s := NewSessions(time.Hour)
	seen := map[string]struct{}{}
	for i := 0; i < 256; i++ {
		tok, _, err := s.Issue()
		if err != nil {
			t.Fatal(err)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token at iter %d", i)
		}
		seen[tok] = struct{}{}
	}
}

func TestSessions_SlidingTTL(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	s := NewSessions(10 * time.Second)
	s.now = func() time.Time { return now }

	tok, expires1, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if want, got := now.Add(10*time.Second), expires1; !want.Equal(got) {
		t.Errorf("initial expiry %v, want %v", got, want)
	}

	now = now.Add(5 * time.Second) // half-way through
	expires2, err := s.Validate(tok)
	if err != nil {
		t.Fatalf("validate at +5s: %v", err)
	}
	if want, got := now.Add(10*time.Second), expires2; !want.Equal(got) {
		t.Errorf("post-validate expiry %v, want %v (sliding)", got, want)
	}
}

func TestSessions_LazyExpiry(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_000_000, 0)
	s := NewSessions(10 * time.Second)
	s.now = func() time.Time { return now }

	tok, _, _ := s.Issue()
	now = now.Add(11 * time.Second) // past expiry
	if _, err := s.Validate(tok); err == nil {
		t.Fatal("expired token validated")
	}
	if c := s.Count(); c != 0 {
		t.Errorf("expired entry not lazily cleaned (count=%d)", c)
	}
}

func TestSessions_Revoke(t *testing.T) {
	t.Parallel()
	s := NewSessions(time.Hour)
	tok, _, _ := s.Issue()
	s.Revoke(tok)
	if _, err := s.Validate(tok); err == nil {
		t.Fatal("revoked token validated")
	}
}

func TestSessions_RevokeAll(t *testing.T) {
	t.Parallel()
	s := NewSessions(time.Hour)
	a, _, _ := s.Issue()
	b, _, _ := s.Issue()
	s.RevokeAll()
	if _, err := s.Validate(a); err == nil {
		t.Error("session A validated after RevokeAll")
	}
	if _, err := s.Validate(b); err == nil {
		t.Error("session B validated after RevokeAll")
	}
	if c := s.Count(); c != 0 {
		t.Errorf("Count after RevokeAll = %d, want 0", c)
	}
}

func TestSessions_RevokeUnknownNoOp(t *testing.T) {
	t.Parallel()
	s := NewSessions(time.Hour)
	s.Revoke("never-issued") // must not panic / error
}
