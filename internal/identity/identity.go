// Package identity reads the feeder's identity (UUID + claim secret).
//
// /api/identity (GET) returns just the UUID + a present/absent flag for the
// secret; the file is at /etc/airplanes/feeder-id and readable by any user,
// so no privilege bump is needed.
//
// /api/identity/secret (POST, authed) returns the full claim secret. The
// secret file is /etc/airplanes/feeder-claim-secret, mode 0640 group=
// airplanes-feed (set by feed/scripts/apl-feed/common.sh:write_secret_file).
// airplanes-webconfig is a member of the airplanes-feed group (added in
// stage-airplanes/05-install-webconfig/01-run-chroot.sh), so we read the
// file directly via group permissions — no sudo, no apl-feed CLI shell-out.
package identity

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
)

// Paths webconfig reads. Override per-test.
type Paths struct {
	FeederIDFile    string // /etc/airplanes/feeder-id
	ClaimSecretFile string // /etc/airplanes/feeder-claim-secret
	ClaimPageURL    string // https://airplanes.live/feeder/claim
}

const defaultClaimPageURL = "https://airplanes.live/feeder/claim"

// DefaultPaths returns production paths.
func DefaultPaths() Paths {
	return Paths{
		FeederIDFile:    "/etc/airplanes/feeder-id",
		ClaimSecretFile: "/etc/airplanes/feeder-claim-secret",
		ClaimPageURL:    defaultClaimPageURL,
	}
}

// Reader exposes the methods the HTTP handlers call. Tests can substitute
// Paths to point at fixture files.
type Reader struct {
	paths Paths
}

// NewReader builds a Reader. Empty ClaimPageURL falls back to the
// production default so callers don't have to specify it.
func NewReader(p Paths) *Reader {
	if p.ClaimPageURL == "" {
		p.ClaimPageURL = defaultClaimPageURL
	}
	return &Reader{paths: p}
}

// Identity is the GET /api/identity payload.
type Identity struct {
	FeederID           string `json:"feeder_id"`
	ClaimSecretPresent bool   `json:"claim_secret_present"`
}

// IdentityWithSecret is the POST /api/identity/secret payload.
type IdentityWithSecret struct {
	FeederID    string `json:"feeder_id"`
	ClaimSecret string `json:"claim_secret"` // formatted XXXX-XXXX-XXXX-XXXX
	ClaimPage   string `json:"claim_page"`
}

// ErrNoFeederID is returned when /etc/airplanes/feeder-id is missing or empty.
var ErrNoFeederID = errors.New("identity: feeder-id missing")

// ErrNoClaimSecret is returned when no claim secret has been generated yet.
var ErrNoClaimSecret = errors.New("identity: claim secret missing")

// canonicalSecret matches feed/scripts/apl-feed/common.sh:validate_secret —
// 16 chars of A-Z / 0-9 after canonicalization (whitespace + hyphens
// stripped, uppercased). Drift here would silently reject secrets that
// apl-feed would accept (or vice versa).
var canonicalSecret = regexp.MustCompile(`^[A-Z0-9]{16}$`)

// Read returns the redacted identity. The feeder-id file is plain-text and
// readable by anyone; the claim-secret file we only stat for presence.
func (r *Reader) Read() (Identity, error) {
	feederID, err := readSingleLine(r.paths.FeederIDFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Identity{}, ErrNoFeederID
		}
		return Identity{}, err
	}
	if feederID == "" {
		return Identity{}, ErrNoFeederID
	}
	present := false
	if info, err := os.Stat(r.paths.ClaimSecretFile); err == nil && info.Size() > 0 {
		present = true
	}
	return Identity{
		FeederID:           feederID,
		ClaimSecretPresent: present,
	}, nil
}

// Reveal returns the full claim secret. Reads /etc/airplanes/feeder-claim-secret
// directly via group permissions — no sudo, no shell-out.
//
// "File missing" is the only condition that maps to ErrNoClaimSecret (the
// 404 "register first" UX). An existing file whose first line doesn't
// canonicalize to a valid 16-char A-Z 0-9 secret is treated as malformed,
// not absent — surfacing real corruption instead of telling the operator
// to register a secret they already have.
func (r *Reader) Reveal() (IdentityWithSecret, error) {
	feederID, err := readSingleLine(r.paths.FeederIDFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return IdentityWithSecret{}, ErrNoFeederID
		}
		return IdentityWithSecret{}, err
	}
	if feederID == "" {
		return IdentityWithSecret{}, ErrNoFeederID
	}

	raw, err := os.ReadFile(r.paths.ClaimSecretFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return IdentityWithSecret{}, ErrNoClaimSecret
		}
		// EISDIR, permission-denied, anything else: surface as a genuine
		// read failure rather than masquerading as "no claim secret".
		return IdentityWithSecret{}, fmt.Errorf("read claim secret: %w", err)
	}
	// First line only — no TrimSpace before canonicalize. canonicalize
	// already strips ASCII whitespace and hyphens, so a CRLF-terminated
	// secret normalizes correctly without TrimSpace's broader Unicode
	// behavior diverging from feed's `tr -d '[:space:]-'` posture.
	firstLine, _, _ := strings.Cut(string(raw), "\n")
	canonical := canonicalize(firstLine)
	if !canonicalSecret.MatchString(canonical) {
		return IdentityWithSecret{}, errors.New("identity: claim secret on disk is not canonical")
	}

	return IdentityWithSecret{
		FeederID:    feederID,
		ClaimSecret: format4x4(canonical),
		ClaimPage:   r.paths.ClaimPageURL,
	}, nil
}

// canonicalize mirrors feed's canonicalize_secret: strip whitespace and
// hyphens, uppercase the rest. Robust against drift in how the secret was
// originally written (e.g. lowercase, hyphenated).
func canonicalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, ch := range s {
		switch {
		case ch == '-':
			continue
		case ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n':
			continue
		case ch >= 'a' && ch <= 'z':
			b.WriteRune(ch - 'a' + 'A')
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// format4x4 formats a 16-char canonical secret as XXXX-XXXX-XXXX-XXXX.
// Caller must validate length first.
func format4x4(canonical string) string {
	return canonical[0:4] + "-" + canonical[4:8] + "-" + canonical[8:12] + "-" + canonical[12:16]
}

func readSingleLine(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line), nil
}
