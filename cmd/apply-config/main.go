// apply-config is the privileged feed.env writer. It is invoked by the
// airplanes-webconfig daemon via a tightly-scoped sudo entry; the entry
// permits the binary at zero arguments only, so no flag/argv injection
// is possible. Stdin carries a JSON document {"updates": {KEY: value}};
// the helper validates every key, takes /run/airplanes/feed-env.lock,
// re-reads the existing feed.env, applies the updates, and atomically
// rewrites the file.
//
// All validation lives in webconfig/internal/configspec, shared with
// the daemon, so the two binaries cannot drift apart.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/configspec"
)

const (
	feedEnvPath  = "/etc/airplanes/feed.env"
	lockFilePath = "/run/airplanes/feed-env.lock"
	stdinCap     = 4096
	lockTimeout  = 5 * time.Second
)

// exit codes — surface to the caller via process exit so webconfig can map
// them to stable client errors without parsing stderr.
const (
	exitOK             = 0
	exitValidation     = 10 // bad input shape or value
	exitParse          = 11 // malformed JSON / wrong types
	exitOversize       = 12 // stdin > stdinCap
	exitFilesystem     = 20 // feed.env / lock / rename failures
	exitArgv           = 30 // someone called us with arguments
)

func main() {
	if err := run(); err != nil {
		var ec exitErr
		if errors.As(err, &ec) {
			fmt.Fprintln(os.Stderr, ec.message)
			os.Exit(ec.code)
		}
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(exitFilesystem)
	}
}

type exitErr struct {
	code    int
	message string
}

func (e exitErr) Error() string { return e.message }

func bail(code int, format string, args ...any) error {
	return exitErr{code: code, message: fmt.Sprintf(format, args...)}
}

func run() error {
	// Sudoers permits only the zero-argument form; defense in depth.
	if len(os.Args) != 1 {
		return bail(exitArgv, "apply-config: takes no arguments (got %d)", len(os.Args)-1)
	}

	updates, err := readUpdates(os.Stdin)
	if err != nil {
		return err
	}

	// Validate every requested update against the shared spec before we do
	// any I/O. The helper does this even though webconfig validates first
	// — the sudoers entry is the trust boundary; we don't trust the daemon.
	for k, v := range updates {
		if err := configspec.Validate(k, v); err != nil {
			return bail(exitValidation, "%v", err)
		}
	}

	lockFile, err := acquireLock()
	if err != nil {
		return err
	}
	defer lockFile.Close()

	existing, err := readExisting(feedEnvPath)
	if err != nil {
		return err
	}

	merged, err := mergeAndValidate(existing, updates)
	if err != nil {
		return err
	}

	if err := atomicWrite(feedEnvPath, renderFeedEnv(merged)); err != nil {
		return bail(exitFilesystem, "atomic write: %v", err)
	}
	return nil
}

// readUpdates reads stdin (≤stdinCap bytes), parses {"updates":{string→string}},
// rejects non-string values, multi-document bodies, and unknown fields.
func readUpdates(r io.Reader) (map[string]string, error) {
	limited := io.LimitReader(r, stdinCap+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, bail(exitParse, "stdin read: %v", err)
	}
	if len(body) > stdinCap {
		return nil, bail(exitOversize, "stdin exceeds %d-byte cap", stdinCap)
	}
	// Reject non-string values: decode into a json.RawMessage map first so
	// we can spot null / numbers / objects before coercing to string.
	var outer struct {
		Updates map[string]json.RawMessage `json:"updates"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&outer); err != nil {
		return nil, bail(exitParse, "json decode: %v", err)
	}
	if dec.More() {
		return nil, bail(exitParse, "json: extra input after first object")
	}
	updates := make(map[string]string, len(outer.Updates))
	for k, raw := range outer.Updates {
		// Reject null explicitly — json.Unmarshal silently coerces null
		// into an empty string, which would let {"UAT_INPUT": null}
		// inadvertently disable 978.
		if string(bytes.TrimSpace(raw)) == "null" {
			return nil, bail(exitParse, "value for %q must be a string (got null)", k)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, bail(exitParse, "value for %q must be a string", k)
		}
		updates[k] = s
	}
	return updates, nil
}

// acquireLock opens (creating if needed) /run/airplanes/feed-env.lock with
// O_NOFOLLOW + a flock LOCK_EX, retrying briefly if the lock is contended.
func acquireLock() (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(lockFilePath), 0o755); err != nil {
		return nil, bail(exitFilesystem, "mkdir %s: %v", filepath.Dir(lockFilePath), err)
	}
	f, err := os.OpenFile(
		lockFilePath,
		os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW,
		0o644,
	)
	if err != nil {
		return nil, bail(exitFilesystem, "open lockfile: %v", err)
	}
	deadline := time.Now().Add(lockTimeout)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, bail(exitFilesystem, "flock timed out after %v", lockTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// keyLine is a strict KEY=VALUE | KEY="VALUE" parser used to read the
// existing feed.env. Anything else (comments, blanks, malformed) is
// preserved as a "drop on rewrite" — we re-emit only valid key/value pairs.
var keyLine = regexp.MustCompile(`^\s*([A-Z_][A-Z0-9_]*)=(?:"([^"]*)"|(\S*))\s*$`)

func readExisting(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, bail(exitFilesystem, "read %s: %v", path, err)
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		value := m[2]
		if value == "" {
			value = m[3]
		}
		out[key] = value
	}
	return out, nil
}

func mergeAndValidate(existing, updates map[string]string) (map[string]string, error) {
	merged := make(map[string]string, len(existing)+len(updates))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range updates {
		merged[k] = configspec.Canonicalize(k, v)
	}
	// Defense in depth: every preserved value (not just the ones we wrote)
	// must pass the universal-reject scan. If the existing feed.env was
	// hand-edited and contains shell-sensitive characters, we fail closed
	// rather than re-emit them.
	for k, v := range merged {
		if err := configspec.CheckUniversal(k, v); err != nil {
			return nil, bail(exitValidation, "preserved value: %v", err)
		}
	}
	// Cross-key consistency: a single-key POST that toggles MLAT_ENABLED=true
	// without supplying MLAT_USER must fail here, otherwise the merged config
	// would write an inconsistent feed.env that strict-fails the daemon.
	if err := configspec.ValidateConsistency(merged); err != nil {
		return nil, bail(exitValidation, "%v", err)
	}
	return merged, nil
}

func renderFeedEnv(merged map[string]string) []byte {
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteString("# Managed by airplanes-webconfig. Edit via the dashboard.\n")
	for _, k := range keys {
		fmt.Fprintf(&buf, "%s=\"%s\"\n", k, merged[k])
	}
	return buf.Bytes()
}

func atomicWrite(path string, contents []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "feed.env.tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { os.Remove(tmpPath) }

	if _, err := tmp.Write(contents); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil // best effort; the rename has already landed
	}
	defer d.Close()
	_ = d.Sync()
	return nil
}
