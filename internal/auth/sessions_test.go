package auth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// ---- persistence ----------------------------------------------------------

func TestSessions_PersistRoundtrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	a := NewPersistentSessions(time.Hour, path)
	tok, _, err := a.Issue()
	if err != nil {
		t.Fatal(err)
	}

	b := NewPersistentSessions(time.Hour, path)
	if _, err := b.Validate(tok); err != nil {
		t.Fatalf("token issued before 'restart' did not validate after reload: %v", err)
	}
	if c := b.Count(); c != 1 {
		t.Errorf("reloaded Count = %d, want 1", c)
	}
}

func TestSessions_FileNeverContainsRawToken(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	s := NewPersistentSessions(time.Hour, path)
	tok, _, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), tok) {
		t.Fatal("session file contains the raw token")
	}
	if !strings.Contains(string(raw), hashToken(tok)) {
		t.Fatal("session file does not contain the token hash")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("session file mode = %o, want 600", mode)
	}
}

func TestSessions_CorruptFileStartsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewPersistentSessions(time.Hour, path)
	if c := s.Count(); c != 0 {
		t.Errorf("Count after corrupt load = %d, want 0", c)
	}
	// The store must still be usable.
	if _, _, err := s.Issue(); err != nil {
		t.Fatalf("Issue after corrupt load: %v", err)
	}
}

func TestSessions_WrongSchemaStartsEmpty(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	content := `{"schema_version":99,"sessions":{"` + hashToken("x") + `":"2099-01-01T00:00:00Z"}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewPersistentSessions(time.Hour, path)
	if c := s.Count(); c != 0 {
		t.Errorf("Count after wrong-schema load = %d, want 0", c)
	}
}

func TestSessions_ExpiredAndMalformedDroppedAtLoad(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	live := hashToken("live")
	content := `{"schema_version":1,"sessions":{` +
		`"` + live + `":"2099-01-01T00:00:00Z",` +
		`"` + hashToken("expired") + `":"2000-01-01T00:00:00Z",` +
		`"not-a-sha256-hash":"2099-01-01T00:00:00Z"}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewPersistentSessions(time.Hour, path)
	if c := s.Count(); c != 1 {
		t.Errorf("Count after pruning load = %d, want 1 (only the live well-formed entry)", c)
	}
	if _, err := s.Validate("live"); err != nil {
		t.Errorf("live entry did not survive the reload: %v", err)
	}
}

func TestSessions_RevokeAllRemovesFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	s := NewPersistentSessions(time.Hour, path)
	if _, _, err := s.Issue(); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAll(); err != nil {
		t.Fatalf("RevokeAll: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session file still present after RevokeAll (stat err: %v)", err)
	}
	if c := NewPersistentSessions(time.Hour, path).Count(); c != 0 {
		t.Errorf("Count after RevokeAll + reload = %d, want 0", c)
	}
}

func TestSessions_RevokePersists(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	a := NewPersistentSessions(time.Hour, path)
	keep, _, _ := a.Issue()
	drop, _, _ := a.Issue()
	a.Revoke(drop)

	b := NewPersistentSessions(time.Hour, path)
	if _, err := b.Validate(drop); err == nil {
		t.Error("revoked token validated after reload")
	}
	if _, err := b.Validate(keep); err != nil {
		t.Errorf("kept token did not validate after reload: %v", err)
	}
}

func TestSessions_ValidatePersistThrottled(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	now := time.Unix(1_000_000, 0)
	s := NewPersistentSessions(time.Hour, path)
	s.now = func() time.Time { return now }

	tok, _, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	persistedExpiry := func() time.Time {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var f sessionsFile
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatal(err)
		}
		return f.Sessions[hashToken(tok)]
	}
	issueExpiry := persistedExpiry()

	// Within persistInterval: the in-memory expiry slides but the file
	// must not be rewritten on every poll.
	now = now.Add(10 * time.Second)
	if _, err := s.Validate(tok); err != nil {
		t.Fatal(err)
	}
	if got := persistedExpiry(); !got.Equal(issueExpiry) {
		t.Errorf("file rewritten within persistInterval: expiry %v, want %v", got, issueExpiry)
	}

	// Past persistInterval: the bump lands on disk.
	now = now.Add(persistInterval)
	if _, err := s.Validate(tok); err != nil {
		t.Fatal(err)
	}
	if got, want := persistedExpiry(), now.Add(time.Hour); !got.Equal(want) {
		t.Errorf("file expiry after throttle window = %v, want %v", got, want)
	}
}

func TestSessions_ConcurrentValidateRevokeAll(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sessions.json")
	s := NewPersistentSessions(time.Hour, path)
	tok, _, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				s.Validate(tok) //nolint:errcheck // outcome irrelevant, exercising the lock
				s.Issue()       //nolint:errcheck
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 20; j++ {
			if err := s.RevokeAll(); err != nil {
				t.Errorf("RevokeAll: %v", err)
			}
		}
	}()
	wg.Wait()
	// After the dust settles a final RevokeAll must leave no file for a
	// reload to resurrect.
	if err := s.RevokeAll(); err != nil {
		t.Fatal(err)
	}
	if c := NewPersistentSessions(time.Hour, path).Count(); c != 0 {
		t.Errorf("Count after final RevokeAll + reload = %d, want 0", c)
	}
}
