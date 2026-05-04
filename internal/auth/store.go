package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PasswordStore mediates reads and writes of the on-disk hash file.
//
// Two atomic-write semantics:
//   - Setup() — create-if-absent. Writes a tempfile, fsyncs, then os.Link()
//     to the target; Link fails if the target already exists, so concurrent
//     setups race losers fail with ErrExists.
//   - Replace() — atomic replace. Writes a tempfile, fsyncs, renames over
//     the target, fsyncs the parent dir.
//
// A single mutex serializes Read/Setup/Replace; this fixes the
// password-change-during-login race documented in the threat model: a
// caller that needs to verify-then-act takes Read() under the mutex,
// finishes its work, and Replace() is blocked until the caller is done.
type PasswordStore struct {
	path string
	mu   sync.Mutex
}

// ErrExists is returned by Setup when a hash file already exists.
var ErrExists = errors.New("password hash already exists")

// NewPasswordStore creates a store rooted at the given file path. The file's
// directory must already exist with mode 0700 owned by the runtime user;
// stage 05 sets that up.
func NewPasswordStore(path string) *PasswordStore {
	return &PasswordStore{path: path}
}

// Path returns the file path (for diagnostics).
func (s *PasswordStore) Path() string { return s.path }

// Lock acquires the store's mutex; the caller must call Unlock when done.
// Used by login flow to serialize the verify+(maybe-issue-session) sequence
// against concurrent password changes.
func (s *PasswordStore) Lock()   { s.mu.Lock() }
func (s *PasswordStore) Unlock() { s.mu.Unlock() }

// Exists returns whether the hash file exists with non-empty content.
func (s *PasswordStore) Exists() (bool, error) {
	info, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.Size() > 0, nil
}

// Read returns the stored PHC string. Caller is responsible for holding the
// mutex (see Lock/Unlock) when serializing against writes.
func (s *PasswordStore) Read() (string, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return "", err
	}
	phc := strings.TrimRight(string(b), "\n")
	if phc == "" {
		return "", fs.ErrNotExist
	}
	return phc, nil
}

// Setup atomically creates the hash file with the given PHC content, but
// only if no hash file exists yet. Returns ErrExists if a hash is already
// present.
func (s *PasswordStore) Setup(phc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := s.writeTempfile(phc)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // safe even on success — Link copies the inode

	// os.Link is atomic create-if-absent on POSIX. The target's inode is
	// the tempfile's inode after success; we then drop the tempfile name.
	if err := os.Link(tmp, s.path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return fmt.Errorf("password store link: %w", err)
	}
	return s.fsyncParent()
}

// Replace atomically overwrites the existing hash file. Used by password
// change flow.
func (s *PasswordStore) Replace(phc string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := s.writeTempfile(phc)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("password store rename: %w", err)
	}
	return s.fsyncParent()
}

// writeTempfile writes phc + newline to a uniquely-named tempfile in the
// same directory as the target, fsyncs, closes, and returns the temp path.
func (s *PasswordStore) writeTempfile(phc string) (string, error) {
	dir := filepath.Dir(s.path)
	base := filepath.Base(s.path)
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	tmpPath := filepath.Join(dir, "."+base+".tmp."+hex.EncodeToString(suffix))
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("password store tempfile: %w", err)
	}
	cleanup := func(retErr *error) {
		if cerr := f.Close(); cerr != nil && *retErr == nil {
			*retErr = cerr
		}
		if *retErr != nil {
			os.Remove(tmpPath)
		}
	}
	var retErr error
	defer cleanup(&retErr)

	if _, retErr = f.WriteString(phc + "\n"); retErr != nil {
		return "", retErr
	}
	if retErr = f.Sync(); retErr != nil {
		return "", retErr
	}
	return tmpPath, retErr
}

func (s *PasswordStore) fsyncParent() error {
	d, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
