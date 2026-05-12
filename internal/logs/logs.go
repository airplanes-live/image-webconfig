// Package logs streams journalctl output as Server-Sent Events for the
// webconfig dashboard's "tail logs" panel. Webconfig (running as
// airplanes-webconfig) is added to the systemd-journal group at install
// time so journalctl can read every system unit's journal without sudo.
//
// Unit names are validated against a strict whitelist before reaching
// argv — no user input ever flows into the exec.
package logs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// Whitelist defines which units are exposable via /api/log/{unit}.
// Keep slugs in sync with LOG_SLUG_TO_UNIT in web/assets/app.js — a slug
// missing here returns 404, while a slug missing there just hides the link.
var Whitelist = map[string]string{
	"feed":           "airplanes-feed.service",
	"mlat":           "airplanes-mlat.service",
	"readsb":         "readsb.service",
	"dump978":        "dump978-fa.service",
	"uat":            "airplanes-978.service",
	"claim":          "airplanes-claim.service",
	"webconfig":      "airplanes-webconfig.service",
	"update":         "airplanes-update.service",
	"system-upgrade": "airplanes-system-upgrade.service",
}

// JournalctlBinary is overridable for tests.
var JournalctlBinary = "/usr/bin/journalctl"

// streamMaxLifetime caps how long any single SSE stream stays open. The
// global http.Server.WriteTimeout is disabled for this handler via
// per-write deadlines, so without this cap a stream could outlive an
// operator's session revocation (logout / password-change) indefinitely.
// Default session TTL is 24h; 1h leaves comfortable headroom while still
// bounding auth-revocation exposure.
const streamMaxLifetime = 1 * time.Hour

// perWriteTimeout is the deadline applied before each line/ping write.
// Replaces the implicit cap that http.Server.WriteTimeout would impose;
// a stuck client trips this and the handler returns, which terminates
// the journalctl child via ctx cancellation.
const perWriteTimeout = 5 * time.Second

// Streamer wraps the journalctl exec. The streamer parameter is injectable
// so tests can substitute a synthetic source.
type Streamer struct {
	streamer wexec.StreamRunner
}

// NewStreamer constructs a Streamer using exec.RealStreamer if streamer is nil.
func NewStreamer(streamer wexec.StreamRunner) *Streamer {
	if streamer == nil {
		streamer = wexec.RealStreamer
	}
	return &Streamer{streamer: streamer}
}

// ErrUnknownUnit is returned when /api/log/{unit} is hit with a unit slug
// not in the whitelist.
var ErrUnknownUnit = errors.New("logs: unit not in whitelist")

// Resolve maps a URL slug to the systemd unit it streams from. Returns
// ErrUnknownUnit for slugs outside the whitelist.
func Resolve(slug string) (string, error) {
	unit, ok := Whitelist[slug]
	if !ok {
		return "", ErrUnknownUnit
	}
	return unit, nil
}

// ServeSSE streams the unit's journalctl output to w as Server-Sent Events.
// Returns when ctx is canceled, the per-stream lifetime cap elapses, the
// journalctl process exits, or a write/flush fails. A `:keepalive` ping
// every 15s defeats LB / proxy idle timeouts; a per-write 5s deadline
// guards against stuck clients.
func (s *Streamer) ServeSSE(ctx context.Context, w http.ResponseWriter, slug string) error {
	unit, err := Resolve(slug)
	if err != nil {
		return err
	}

	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return fmt.Errorf("logs: initial flush: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, streamMaxLifetime)
	defer cancel()

	pr, pw := io.Pipe()
	var (
		mu     sync.Mutex
		closed bool
	)
	closeOnce := func(err error) {
		mu.Lock()
		if !closed {
			closed = true
			_ = pw.CloseWithError(err)
		}
		mu.Unlock()
	}

	argv := []string{
		JournalctlBinary,
		"-u", unit,
		"--follow",
		"--no-pager",
		"--lines=100",
		"--output=cat",
	}
	go func() {
		err := s.streamer(ctx, pw, argv)
		closeOnce(err)
	}()

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	pingTick := time.NewTicker(15 * time.Second)
	defer pingTick.Stop()

	lineCh := make(chan string, 64)
	scanErrCh := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			select {
			case lineCh <- scanner.Text():
			case <-ctx.Done():
				return
			}
		}
		scanErrCh <- scanner.Err()
		close(lineCh)
	}()

	// deadlineUnsupported latches to true the first time SetWriteDeadline
	// returns http.ErrNotSupported. Production writers always support
	// deadlines; tests using httptest.ResponseRecorder don't.
	deadlineUnsupported := false
	write := func(format string, args ...any) error {
		if !deadlineUnsupported {
			if err := rc.SetWriteDeadline(time.Now().Add(perWriteTimeout)); err != nil {
				if !errors.Is(err, http.ErrNotSupported) {
					return err
				}
				deadlineUnsupported = true
			}
		}
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return err
		}
		return rc.Flush()
	}

	for {
		select {
		case <-ctx.Done():
			closeOnce(ctx.Err())
			return nil
		case line, ok := <-lineCh:
			if !ok {
				closeOnce(nil)
				if err := <-scanErrCh; err != nil && !errors.Is(err, io.ErrClosedPipe) {
					return err
				}
				return nil
			}
			if err := write("data: %s\n\n", strings.ReplaceAll(line, "\n", "\\n")); err != nil {
				closeOnce(err)
				return err
			}
		case <-pingTick.C:
			if err := write(":ping\n\n"); err != nil {
				closeOnce(err)
				return err
			}
		}
	}
}
