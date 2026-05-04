// Package identity reads the feeder's identity (UUID + claim secret).
//
// /api/identity (GET) returns just the UUID + a present/absent flag for the
// secret; the file is at /etc/airplanes/feeder-id and readable by any user,
// so no shell-out is needed.
//
// /api/identity/secret (POST, authed) returns the full claim secret. The
// secret file is /etc/airplanes/feeder-claim-secret, mode 0600 owned by the
// `airplanes` user — webconfig (running as airplanes-webconfig) can't read
// it directly. Reveal goes through `sudo -n -u airplanes /usr/local/bin/
// apl-feed claim show` whose argv is pinned in /etc/sudoers.d.
package identity

import (
	"bufio"
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// Paths webconfig reads / shells out to. Override per-test.
type Paths struct {
	FeederIDFile     string // /etc/airplanes/feeder-id
	ClaimSecretFile  string // /etc/airplanes/feeder-claim-secret
	APLFeedSudoArgv  []string
}

// DefaultPaths returns production paths and the sudo argv shape that the
// shipped sudoers entry permits.
func DefaultPaths() Paths {
	return Paths{
		FeederIDFile:    "/etc/airplanes/feeder-id",
		ClaimSecretFile: "/etc/airplanes/feeder-claim-secret",
		APLFeedSudoArgv: []string{
			"/usr/bin/sudo", "-n", "-u", "airplanes",
			"/usr/local/bin/apl-feed", "claim", "show",
		},
	}
}

// Reader exposes the methods the HTTP handlers call. Tests substitute the
// CommandRunner for canned `apl-feed claim show` output.
type Reader struct {
	paths   Paths
	runner  wexec.CommandRunner
}

func NewReader(p Paths, r wexec.CommandRunner) *Reader {
	if r == nil {
		r = wexec.RealRunner
	}
	return &Reader{paths: p, runner: r}
}

// Identity is the GET /api/identity payload.
type Identity struct {
	FeederID            string `json:"feeder_id"`
	ClaimSecretPresent  bool   `json:"claim_secret_present"`
}

// IdentityWithSecret is the POST /api/identity/secret payload.
type IdentityWithSecret struct {
	FeederID    string `json:"feeder_id"`
	ClaimSecret string `json:"claim_secret"`  // formatted XXXX-XXXX-XXXX-XXXX
	ClaimPage   string `json:"claim_page"`
}

// ErrNoFeederID is returned when /etc/airplanes/feeder-id is missing or empty.
var ErrNoFeederID = errors.New("identity: feeder-id missing")

// ErrNoClaimSecret is returned when no claim secret has been generated yet.
var ErrNoClaimSecret = errors.New("identity: claim secret missing")

// Read returns the redacted identity. The feeder-id file is plain-text and
// readable by anyone; the claim-secret file is mode 0600 and we only stat it.
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

// Reveal returns the full claim secret via the pinned-argv `apl-feed claim
// show` shell-out.
func (r *Reader) Reveal(ctx context.Context) (IdentityWithSecret, error) {
	res, err := r.runner(ctx, r.paths.APLFeedSudoArgv)
	if err != nil {
		// Pass through stderr if any so /var/log/journal shows the cause.
		return IdentityWithSecret{}, errors.New("identity reveal: " + err.Error())
	}
	parsed, err := parseClaimShow(string(res.Stdout))
	if err != nil {
		return IdentityWithSecret{}, err
	}
	if parsed.FeederID == "" || parsed.ClaimSecret == "" {
		return IdentityWithSecret{}, ErrNoClaimSecret
	}
	return parsed, nil
}

// parseClaimShow accepts the `apl-feed claim show` output:
//
//	Feeder ID: <uuid>
//	Claim secret: XXXX-XXXX-XXXX-XXXX
//	Claim page: https://...
//	Version: <n>     (optional)
//
// Lines beyond these are ignored.
func parseClaimShow(out string) (IdentityWithSecret, error) {
	var got IdentityWithSecret
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Feeder ID:"):
			got.FeederID = strings.TrimSpace(strings.TrimPrefix(line, "Feeder ID:"))
		case strings.HasPrefix(line, "Claim secret:"):
			got.ClaimSecret = strings.TrimSpace(strings.TrimPrefix(line, "Claim secret:"))
		case strings.HasPrefix(line, "Claim page:"):
			got.ClaimPage = strings.TrimSpace(strings.TrimPrefix(line, "Claim page:"))
		}
	}
	if err := scanner.Err(); err != nil {
		return IdentityWithSecret{}, err
	}
	return got, nil
}

func readSingleLine(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line), nil
}
