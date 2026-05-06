package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestPaths(t *testing.T) (Paths, string) {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		FeederIDFile:    filepath.Join(dir, "feeder-id"),
		ClaimSecretFile: filepath.Join(dir, "feeder-claim-secret"),
		ClaimPageURL:    "https://airplanes.live/feeder/claim",
	}, dir
}

func TestRead_Missing(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader(p)
	_, err := r.Read()
	if !errors.Is(err, ErrNoFeederID) {
		t.Fatalf("err = %v, want ErrNoFeederID", err)
	}
}

func TestRead_FeederIDPresent_NoSecret(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	if err := os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewReader(p)
	got, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.FeederID != "abc-123" {
		t.Errorf("FeederID = %q, want abc-123", got.FeederID)
	}
	if got.ClaimSecretPresent {
		t.Error("ClaimSecretPresent = true, want false (file missing)")
	}
}

func TestRead_FeederIDAndSecretPresent(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\n"), 0o640)
	r := NewReader(p)
	got, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !got.ClaimSecretPresent {
		t.Error("ClaimSecretPresent = false, want true")
	}
}

func TestRead_FeederIDEmptyTreatedAsMissing(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("\n"), 0o644)
	r := NewReader(p)
	if _, err := r.Read(); !errors.Is(err, ErrNoFeederID) {
		t.Fatalf("err = %v, want ErrNoFeederID", err)
	}
}

func TestReveal_HappyPath_CanonicalOnDisk(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\n"), 0o640)
	r := NewReader(p)
	got, err := r.Reveal()
	if err != nil {
		t.Fatal(err)
	}
	if got.FeederID != "abc-123" {
		t.Errorf("FeederID = %q, want abc-123", got.FeederID)
	}
	if got.ClaimSecret != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("ClaimSecret = %q, want ABCD-EFGH-IJKL-MNOP", got.ClaimSecret)
	}
	if got.ClaimPage != "https://airplanes.live/feeder/claim" {
		t.Errorf("ClaimPage = %q", got.ClaimPage)
	}
}

func TestReveal_CanonicalizesLowercaseHyphenatedOnDisk(t *testing.T) {
	t.Parallel()
	// feed's canonicalize_secret accepts lowercase + hyphens; mirror that
	// here so a feeder where the secret was hand-written in lowercase
	// hyphenated form (rare but possible via `apl-feed claim set`) still
	// reveals correctly.
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte("abcd-efgh-ijkl-mnop\n"), 0o640)
	r := NewReader(p)
	got, err := r.Reveal()
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimSecret != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("ClaimSecret = %q, want ABCD-EFGH-IJKL-MNOP", got.ClaimSecret)
	}
}

func TestReveal_NoFeederID(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\n"), 0o640)
	r := NewReader(p)
	if _, err := r.Reveal(); !errors.Is(err, ErrNoFeederID) {
		t.Fatalf("err = %v, want ErrNoFeederID", err)
	}
}

func TestReveal_SecretMissing(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	r := NewReader(p)
	if _, err := r.Reveal(); !errors.Is(err, ErrNoClaimSecret) {
		t.Fatalf("err = %v, want ErrNoClaimSecret", err)
	}
}

func TestReveal_SecretEmptyFileTreatedAsMalformed(t *testing.T) {
	t.Parallel()
	// An EXISTING claim-secret file that's empty represents corrupted on-disk
	// state, not "no claim secret yet". Surface it as malformed so a stuck
	// feeder gets a 500 with a logged cause instead of a 404 "register first"
	// that would mislead the operator into trying to re-register a secret
	// the server already has.
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte(""), 0o640)
	r := NewReader(p)
	_, err := r.Reveal()
	if err == nil {
		t.Fatal("expected non-nil error for empty secret file")
	}
	if errors.Is(err, ErrNoClaimSecret) {
		t.Fatalf("err = ErrNoClaimSecret, want a malformed-secret error")
	}
}

func TestReveal_SecretWhitespaceOnlyTreatedAsMalformed(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte("   \n"), 0o640)
	r := NewReader(p)
	_, err := r.Reveal()
	if err == nil {
		t.Fatal("expected non-nil error for whitespace-only secret file")
	}
	if errors.Is(err, ErrNoClaimSecret) {
		t.Fatalf("err = ErrNoClaimSecret, want malformed")
	}
}

func TestReveal_SecretCRLFAccepted(t *testing.T) {
	t.Parallel()
	// A Windows editor that drops CRLF line endings shouldn't reject the
	// secret — canonicalize() strips \r along with other whitespace.
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\r\n"), 0o640)
	r := NewReader(p)
	got, err := r.Reveal()
	if err != nil {
		t.Fatalf("CRLF-terminated secret rejected: %v", err)
	}
	if got.ClaimSecret != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("ClaimSecret = %q, want ABCD-EFGH-IJKL-MNOP", got.ClaimSecret)
	}
}

func TestReveal_SecretFileIsDirectoryReturnsReadError(t *testing.T) {
	t.Parallel()
	// A directory at the secret path triggers EISDIR. Must surface as a
	// generic read error, NOT ErrNoClaimSecret — distinguishing real I/O
	// failures from the legitimate "no secret yet" path.
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	if err := os.Mkdir(p.ClaimSecretFile, 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewReader(p)
	_, err := r.Reveal()
	if err == nil {
		t.Fatal("expected non-nil error when secret path is a directory")
	}
	if errors.Is(err, ErrNoClaimSecret) {
		t.Fatalf("err = ErrNoClaimSecret, want a read error")
	}
}

func TestReveal_SecretMalformedRejected(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	// Garbage that doesn't canonicalize to 16 chars A-Z 0-9.
	_ = os.WriteFile(p.ClaimSecretFile, []byte("not-a-secret\n"), 0o640)
	r := NewReader(p)
	_, err := r.Reveal()
	if err == nil {
		t.Fatal("expected non-nil error for malformed secret")
	}
	if errors.Is(err, ErrNoClaimSecret) {
		// Distinct from "no claim secret" — file exists, contents are wrong.
		t.Fatalf("err = ErrNoClaimSecret, want a malformed-secret error")
	}
}

func TestNewReader_DefaultsClaimPageURLWhenEmpty(t *testing.T) {
	t.Parallel()
	r := NewReader(Paths{
		FeederIDFile:    "/tmp/_unused",
		ClaimSecretFile: "/tmp/_unused",
		// ClaimPageURL omitted on purpose.
	})
	if r.paths.ClaimPageURL != "https://airplanes.live/feeder/claim" {
		t.Errorf("ClaimPageURL = %q, want default", r.paths.ClaimPageURL)
	}
}

// Pin the canonical claim-secret file path. If it drifts from feed's
// secret_final_path() (/etc/airplanes/feeder-claim-secret), the reveal
// breaks silently.
func TestDefaultPaths_PinsClaimSecretFile(t *testing.T) {
	t.Parallel()
	p := DefaultPaths()
	if p.ClaimSecretFile != "/etc/airplanes/feeder-claim-secret" {
		t.Errorf("ClaimSecretFile = %q, want /etc/airplanes/feeder-claim-secret", p.ClaimSecretFile)
	}
	if p.FeederIDFile != "/etc/airplanes/feeder-id" {
		t.Errorf("FeederIDFile = %q, want /etc/airplanes/feeder-id", p.FeederIDFile)
	}
}
