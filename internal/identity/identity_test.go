package identity

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

func newTestPaths(t *testing.T) (Paths, string) {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		FeederIDFile:    filepath.Join(dir, "feeder-id"),
		ClaimSecretFile: filepath.Join(dir, "feeder-claim-secret"),
		APLFeedSudoArgv: []string{"/bin/echo", "stub"},
	}, dir
}

func TestRead_Missing(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	r := NewReader(p, nil)
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
	r := NewReader(p, nil)
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
	_ = os.WriteFile(p.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\n"), 0o600)
	r := NewReader(p, nil)
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
	r := NewReader(p, nil)
	if _, err := r.Read(); !errors.Is(err, ErrNoFeederID) {
		t.Fatalf("err = %v, want ErrNoFeederID", err)
	}
}

func TestReveal_HappyPath(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	fakeRunner := func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{
			Stdout: []byte(
				"Feeder ID: abc-123\n" +
					"Claim secret: ABCD-EFGH-IJKL-MNOP\n" +
					"Claim page: https://airplanes.live/feeder/claim\n" +
					"Version: 3\n",
			),
		}, nil
	}
	r := NewReader(p, fakeRunner)
	got, err := r.Reveal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.FeederID != "abc-123" {
		t.Errorf("FeederID = %q", got.FeederID)
	}
	if got.ClaimSecret != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("ClaimSecret = %q", got.ClaimSecret)
	}
	if got.ClaimPage != "https://airplanes.live/feeder/claim" {
		t.Errorf("ClaimPage = %q", got.ClaimPage)
	}
}

func TestReveal_RunnerError(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	fakeRunner := func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{}, errors.New("sudo: a password is required")
	}
	r := NewReader(p, fakeRunner)
	if _, err := r.Reveal(context.Background()); err == nil {
		t.Fatal("expected error from runner")
	}
}

func TestReveal_MissingSecretInOutput(t *testing.T) {
	t.Parallel()
	p, _ := newTestPaths(t)
	fakeRunner := func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{Stdout: []byte("Feeder ID: abc-123\n")}, nil
	}
	r := NewReader(p, fakeRunner)
	_, err := r.Reveal(context.Background())
	if !errors.Is(err, ErrNoClaimSecret) {
		t.Fatalf("err = %v, want ErrNoClaimSecret", err)
	}
}

func TestParseClaimShow_IgnoresUnknownLines(t *testing.T) {
	t.Parallel()
	out := "preamble line\n" +
		"Feeder ID: abc-123\n" +
		"some other text\n" +
		"Claim secret: ABCD-EFGH-IJKL-MNOP\n" +
		"Version: 9\n" +
		"trailing junk"
	got, err := parseClaimShow(out)
	if err != nil {
		t.Fatal(err)
	}
	if got.FeederID != "abc-123" || got.ClaimSecret != "ABCD-EFGH-IJKL-MNOP" {
		t.Errorf("parsed = %+v", got)
	}
}
