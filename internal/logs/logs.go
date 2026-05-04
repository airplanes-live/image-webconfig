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
var Whitelist = map[string]string{
	"feed":      "airplanes-feed.service",
	"mlat":      "airplanes-mlat.service",
	"readsb":    "readsb.service",
	"dump978":   "dump978-fa.service",
	"uat":       "airplanes-978.service",
	"claim":     "airplanes-claim.service",
	"webconfig": "airplanes-webconfig.service",
	"update":    "airplanes-update.service",
}

// JournalctlBinary is overridable for tests.
var JournalctlBinary = "/usr/bin/journalctl"

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
// Returns when the request context is canceled or the journalctl process
// exits. A `:keepalive` ping every 15s defeats LB / proxy idle timeouts.
func (s *Streamer) ServeSSE(ctx context.Context, w http.ResponseWriter, slug string) error {
	unit, err := Resolve(slug)
	if err != nil {
		return err
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return errors.New("logs: ResponseWriter does not support flushing")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable buffering in lighttpd
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	pr, pw := io.Pipe()
	var (
		mu      sync.Mutex
		closed  bool
	)
	closeOnce := func(err error) {
		mu.Lock()
		if !closed {
			closed = true
			_ = pw.CloseWithError(err)
		}
		mu.Unlock()
	}

	// Run journalctl in a goroutine, piping stdout into pr/pw.
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

	// Forward each newline-delimited journalctl line as an SSE event,
	// interleaved with periodic pings.
	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	pingTick := time.NewTicker(15 * time.Second)
	defer pingTick.Stop()

	lineCh := make(chan string, 64)
	scanErrCh := make(chan error, 1)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			select {
			case lineCh <- line:
			case <-ctx.Done():
				return
			}
		}
		scanErrCh <- scanner.Err()
		close(lineCh)
	}()

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
			fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(line, "\n", "\\n"))
			flusher.Flush()
		case <-pingTick.C:
			fmt.Fprint(w, ":ping\n\n")
			flusher.Flush()
		}
	}
}
