// Package feedenv reads /etc/airplanes/feed.env. It NEVER sources the
// file (the file is shell-syntax) — instead it parses each KEY=VALUE
// line strictly, respects double-quoting, and returns the values for
// whitelisted keys only.
package feedenv

import (
	"bufio"
	"errors"
	"os"
	"regexp"
	"strings"
)

// DefaultPath is the rootfs location.
const DefaultPath = "/etc/airplanes/feed.env"

// ReadKeys is the GET /api/config whitelist.
var ReadKeys = []string{
	"LATITUDE",
	"LONGITUDE",
	"ALTITUDE",
	"MLAT_USER",
	"MLAT_ENABLED",
	"INPUT",
	"INPUT_TYPE",
	"NET_OPTIONS",
	"JSON_OPTIONS",
	"MLATSERVER",
	"TARGET",
	"REDUCE_INTERVAL",
	"GAIN",
	"RESULTS",
	"UAT_INPUT",
}

// keyLine matches `KEY=` or `KEY=value` or `KEY="value"` (with quoted
// value allowed to contain anything except an unescaped quote; we do
// NOT support shell escapes — quoted is verbatim). Leading whitespace
// tolerated.
var keyLine = regexp.MustCompile(`^\s*([A-Z_][A-Z0-9_]*)=(?:"([^"]*)"|(\S*))\s*$`)

// Reader reads feed.env at the configured path. Path can be overridden for
// tests.
type Reader struct {
	Path string
}

// New returns a Reader rooted at DefaultPath.
func New() *Reader { return &Reader{Path: DefaultPath} }

// ErrNotFound is returned when feed.env doesn't exist.
var ErrNotFound = errors.New("feedenv: feed.env not found")

// ReadAll returns whitelisted keys mapped to their value. Keys absent from
// feed.env are simply omitted from the returned map.
func (r *Reader) ReadAll() (map[string]string, error) {
	file, err := os.Open(r.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer file.Close()

	whitelist := make(map[string]struct{}, len(ReadKeys))
	for _, k := range ReadKeys {
		whitelist[k] = struct{}{}
	}

	out := make(map[string]string, len(ReadKeys))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if i := strings.IndexAny(line, "#"); i == 0 {
			continue
		}
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		m := keyLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		key := m[1]
		value := m[2]
		if value == "" {
			value = m[3] // unquoted
		}
		if _, ok := whitelist[key]; !ok {
			continue
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
