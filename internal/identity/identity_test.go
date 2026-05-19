package identity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestPaths(t *testing.T) (Paths, string) {
	t.Helper()
	dir := t.TempDir()
	return Paths{
		FeederIDFile:     filepath.Join(dir, "feeder-id"),
		ClaimSecretFile:  filepath.Join(dir, "feeder-claim-secret"),
		ClaimVersionFile: filepath.Join(dir, "feeder-claim-secret.version"),
		ClaimPageURL:     "https://airplanes.live/feeder/claim",
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
	_ = os.WriteFile(p.ClaimVersionFile, []byte("3\n"), 0o640)
	beforeWrite := time.Now().Add(-2 * time.Second).UTC()
	r := NewReader(p)
	got, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !got.ClaimSecretPresent {
		t.Error("ClaimSecretPresent = false, want true")
	}
	if got.ClaimSecretVersion != 3 {
		t.Errorf("ClaimSecretVersion = %d, want 3", got.ClaimSecretVersion)
	}
	parsed, err := time.Parse(time.RFC3339, got.ClaimSecretUpdatedAt)
	if err != nil {
		t.Fatalf("ClaimSecretUpdatedAt = %q, not RFC3339: %v", got.ClaimSecretUpdatedAt, err)
	}
	afterWrite := time.Now().Add(2 * time.Second).UTC()
	// 1-second tolerance to absorb RFC3339's subsecond truncation: the
	// stored mtime can round down to a value that is slightly before
	// our pre-write reading by up to a second.
	if parsed.Before(beforeWrite.Add(-time.Second)) || parsed.After(afterWrite) {
		t.Errorf("ClaimSecretUpdatedAt = %v, not within [%v, %v]",
			parsed, beforeWrite, afterWrite)
	}
}

func TestRead_SecretPresentNoVersionFile(t *testing.T) {
	t.Parallel()
	// `apl-feed claim set` deliberately removes the version file. UI
	// should still show "Claimed" + updated-at, with version omitted.
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
	if got.ClaimSecretVersion != 0 {
		t.Errorf("ClaimSecretVersion = %d, want 0", got.ClaimSecretVersion)
	}
	if got.ClaimSecretUpdatedAt == "" {
		t.Error("ClaimSecretUpdatedAt empty, want a value")
	}
}

func TestRead_SecretFileEmptyNotPresent(t *testing.T) {
	t.Parallel()
	// Empty-on-disk is malformed (aborted write or hand-edit). Read()
	// reports not-present so the dashboard surfaces "Not yet claimed";
	// Reveal() separately surfaces the real corruption when the user
	// reaches for the secret.
	p, _ := newTestPaths(t)
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	_ = os.WriteFile(p.ClaimSecretFile, []byte(""), 0o640)
	r := NewReader(p)
	got, err := r.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimSecretPresent {
		t.Error("ClaimSecretPresent = true, want false (size 0)")
	}
	if got.ClaimSecretUpdatedAt != "" {
		t.Errorf("ClaimSecretUpdatedAt = %q, want empty", got.ClaimSecretUpdatedAt)
	}
	if got.ClaimSecretVersion != 0 {
		t.Errorf("ClaimSecretVersion = %d, want 0", got.ClaimSecretVersion)
	}
}

func TestRead_VersionFileVariants(t *testing.T) {
	t.Parallel()
	// Best-effort parser: anything that isn't a positive integer in the
	// first line returns 0. Pin the variants so an over-zealous refactor
	// can't make us start surfacing -1 or NaN in the UI.
	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"empty", "", 0},
		{"whitespace_only", "   \n", 0},
		{"crlf_terminated", "5\r\n", 5},
		{"trailing_newline", "5\n", 5},
		{"zero", "0\n", 0},
		{"negative", "-1\n", 0},
		{"overflow_int", "99999999999999999999\n", 0},
		{"non_numeric", "v3\n", 0},
		{"multiline_first_wins", "7\n8\n9\n", 7},
		{"hex", "0x10\n", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, _ := newTestPaths(t)
			_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
			_ = os.WriteFile(p.ClaimSecretFile, []byte("ABCDEFGHIJKLMNOP\n"), 0o640)
			_ = os.WriteFile(p.ClaimVersionFile, []byte(tc.content), 0o640)
			r := NewReader(p)
			got, err := r.Read()
			if err != nil {
				t.Fatal(err)
			}
			if got.ClaimSecretVersion != tc.want {
				t.Errorf("content=%q: ClaimSecretVersion = %d, want %d",
					tc.content, got.ClaimSecretVersion, tc.want)
			}
		})
	}
}

func TestRead_StatErrorPropagates(t *testing.T) {
	t.Parallel()
	// A stat error OTHER than ErrNotExist must surface as a real error,
	// not get collapsed to "unclaimed" — a transient permission glitch
	// rendering as "Not yet claimed" would mislead the operator into
	// pressing Register on a feeder that's actually claimed. Trigger
	// it with a non-traversable parent directory.
	tmp := t.TempDir()
	parent := filepath.Join(tmp, "noperm")
	if err := os.Mkdir(parent, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root; mode 0000 still grants access")
	}
	p := Paths{
		FeederIDFile:     filepath.Join(tmp, "feeder-id"),
		ClaimSecretFile:  filepath.Join(parent, "feeder-claim-secret"),
		ClaimVersionFile: filepath.Join(parent, "feeder-claim-secret.version"),
		ClaimPageURL:     "https://airplanes.live/feeder/claim",
	}
	_ = os.WriteFile(p.FeederIDFile, []byte("abc-123\n"), 0o644)
	r := NewReader(p)
	_, err := r.Read()
	if err == nil {
		t.Fatal("expected stat error to propagate, got nil")
	}
	if errors.Is(err, ErrNoFeederID) {
		t.Fatalf("err = ErrNoFeederID, want a stat error")
	}
}

func TestIdentity_JSON_WireShapeStable(t *testing.T) {
	t.Parallel()
	// Wire-shape pin: every field — including the new ones — is always
	// present in the JSON, never omitted. Downstream JS reads these
	// keys directly (`id.claim_secret_version`, etc.) and an omitted
	// key would surface as `undefined`, distinct from `0` or `""`.
	// Spotting a regression here also catches a stray `omitempty` tag
	// addition during future maintenance.
	cases := []struct {
		name string
		in   Identity
		want []string // substrings that must appear in the marshalled JSON
	}{
		{
			name: "unclaimed",
			in:   Identity{FeederID: "abc"},
			want: []string{
				`"feeder_id":"abc"`,
				`"claim_secret_present":false`,
				`"claim_secret_updated_at":""`,
				`"claim_secret_version":0`,
			},
		},
		{
			name: "claimed",
			in: Identity{
				FeederID:             "abc",
				ClaimSecretPresent:   true,
				ClaimSecretUpdatedAt: "2026-05-19T12:34:56Z",
				ClaimSecretVersion:   7,
			},
			want: []string{
				`"feeder_id":"abc"`,
				`"claim_secret_present":true`,
				`"claim_secret_updated_at":"2026-05-19T12:34:56Z"`,
				`"claim_secret_version":7`,
			},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			s := string(b)
			for _, sub := range tc.want {
				if !strings.Contains(s, sub) {
					t.Errorf("JSON missing %s\nfull: %s", sub, s)
				}
			}
		})
	}
}

func TestNewReader_DefaultsClaimVersionFileWhenEmpty(t *testing.T) {
	t.Parallel()
	r := NewReader(Paths{
		FeederIDFile:    "/tmp/_unused",
		ClaimSecretFile: "/tmp/_unused",
		// ClaimVersionFile omitted on purpose.
	})
	if r.paths.ClaimVersionFile != defaultClaimVersionFile {
		t.Errorf("ClaimVersionFile = %q, want %q",
			r.paths.ClaimVersionFile, defaultClaimVersionFile)
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

// Pin the canonical claim-state file paths. If any of these drift from
// feed's secret_final_path / secret_version_path / feeder_id_path, the
// reveal / status-card UX breaks silently.
func TestDefaultPaths_PinsClaimSecretFile(t *testing.T) {
	t.Parallel()
	p := DefaultPaths()
	if p.ClaimSecretFile != "/etc/airplanes/feeder-claim-secret" {
		t.Errorf("ClaimSecretFile = %q, want /etc/airplanes/feeder-claim-secret", p.ClaimSecretFile)
	}
	if p.ClaimVersionFile != "/etc/airplanes/feeder-claim-secret.version" {
		t.Errorf("ClaimVersionFile = %q, want /etc/airplanes/feeder-claim-secret.version", p.ClaimVersionFile)
	}
	if p.FeederIDFile != "/etc/airplanes/feeder-id" {
		t.Errorf("FeederIDFile = %q, want /etc/airplanes/feeder-id", p.FeederIDFile)
	}
}
