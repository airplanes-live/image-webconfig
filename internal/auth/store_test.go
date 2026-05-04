package auth

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *PasswordStore {
	t.Helper()
	dir := t.TempDir()
	return NewPasswordStore(filepath.Join(dir, "password.hash"))
}

func TestStore_ExistsFalseInitially(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	exists, err := s.Exists()
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("Exists() = true on fresh store")
	}
}

func TestStore_SetupCreatesFile(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.Setup("phc-stub"); err != nil {
		t.Fatal(err)
	}
	exists, _ := s.Exists()
	if !exists {
		t.Fatal("Exists() = false after Setup")
	}
	got, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != "phc-stub" {
		t.Errorf("Read() = %q, want %q", got, "phc-stub")
	}
}

func TestStore_SetupRejectsExisting(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.Setup("first"); err != nil {
		t.Fatal(err)
	}
	err := s.Setup("second")
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Setup err = %v, want ErrExists", err)
	}
	got, _ := s.Read()
	if got != "first" {
		t.Errorf("Read() after rejected second Setup = %q, want %q", got, "first")
	}
}

func TestStore_ReplaceOverwrites(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_ = s.Setup("first")
	if err := s.Replace("second"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "second" {
		t.Errorf("Read() after Replace = %q, want %q", got, "second")
	}
}

func TestStore_ReadEmptyFile(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	// Drop a zero-byte file directly.
	if err := os.WriteFile(s.path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Read()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read() of empty file err = %v, want fs.ErrNotExist", err)
	}
}

func TestStore_FilePerms(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	if err := s.Setup("phc"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("hash file perms = %o, want 0600", mode)
	}
}

func TestStore_NoLingeringTempfile(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_ = s.Setup("phc")
	// No tempfile should remain in the dir.
	dir := filepath.Dir(s.path)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "password.hash" {
			continue
		}
		t.Errorf("lingering file in dir: %s", e.Name())
	}
}

func TestStore_LockSerialization(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	_ = s.Setup("first")

	s.Lock()
	done := make(chan struct{})
	go func() {
		// This Replace blocks on the mutex held above.
		_ = s.Replace("second")
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Replace ran while Lock held")
	default:
	}
	got, _ := s.Read()
	if got != "first" {
		t.Errorf("Read() under Lock saw %q, want %q (background Replace leaked)", got, "first")
	}
	s.Unlock()
	<-done
	got, _ = s.Read()
	if got != "second" {
		t.Errorf("Read() after Unlock saw %q, want %q", got, "second")
	}
}
