package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

func TestResolve_Whitelist(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"feed":                "airplanes-feed.service",
		"mlat":                "airplanes-mlat.service",
		"readsb":              "readsb.service",
		"dump978":             "dump978-fa.service",
		"uat":                 "airplanes-978.service",
		"claim":               "airplanes-claim.service",
		"webconfig":           "airplanes-webconfig.service",
		"update":              "airplanes-update.service",
		"system-upgrade":      "airplanes-system-upgrade.service",
		"update-orchestrator": "airplanes-update-orchestrator.service",
	}
	for slug, want := range cases {
		got, err := Resolve(slug)
		if err != nil {
			t.Errorf("Resolve(%q) err = %v", slug, err)
		}
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", slug, got, want)
		}
	}
}

func TestResolve_Unknown(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{
		"", "ssh", "../etc/passwd", "feed.service", "feed; rm -rf /",
		"airplanes-feed", "FEED",
	} {
		_, err := Resolve(slug)
		if !errors.Is(err, ErrUnknownUnit) {
			t.Errorf("Resolve(%q) err = %v, want ErrUnknownUnit", slug, err)
		}
	}
}

// fakeStreamer writes the canned `lines` to w then exits. Tests assert the
// SSE wire format on the receiving side.
func fakeStreamer(lines []string, holdOpen time.Duration) wexec.StreamRunner {
	return func(ctx context.Context, w io.Writer, _ []string) error {
		for _, l := range lines {
			if _, err := io.WriteString(w, l+"\n"); err != nil {
				return err
			}
		}
		select {
		case <-time.After(holdOpen):
		case <-ctx.Done():
		}
		return nil
	}
}

func TestServeSSE_StreamsLines(t *testing.T) {
	t.Parallel()
	s := NewStreamer(fakeStreamer([]string{"line one", "line two"}, 50*time.Millisecond))

	w := httptest.NewRecorder()
	w.Body.Reset()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.ServeSSE(ctx, w, "feed"); err != nil {
		t.Fatal(err)
	}

	got := w.Body.String()
	if !strings.Contains(got, "data: line one\n\n") {
		t.Errorf("missing SSE-formatted 'line one' in: %q", got)
	}
	if !strings.Contains(got, "data: line two\n\n") {
		t.Errorf("missing SSE-formatted 'line two' in: %q", got)
	}
	if got, want := w.Header().Get("Content-Type"), "text/event-stream"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := w.Header().Get("Cache-Control"), "no-cache"; got != want {
		t.Errorf("Cache-Control = %q, want %q", got, want)
	}
}

// TestServeSSE_ArgvUsesShortOutput locks in the journalctl output
// format. --output=short is the contract the UI's per-line timestamp
// parser depends on; a regression to --output=cat would silently
// restore the "logs without timestamps" bug.
func TestServeSSE_ArgvUsesShortOutput(t *testing.T) {
	t.Parallel()
	var captured []string
	capture := func(_ context.Context, _ io.Writer, argv []string) error {
		captured = argv
		return nil
	}
	s := NewStreamer(capture)
	w := httptest.NewRecorder()
	if err := s.ServeSSE(context.Background(), w, "feed"); err != nil {
		t.Fatal(err)
	}
	var has bool
	for _, a := range captured {
		if a == "--output=short" {
			has = true
			break
		}
	}
	if !has {
		t.Errorf("argv missing --output=short: %v", captured)
	}
}

// TestServeSSE_StripsHostname asserts the end-to-end wire reformatting:
// a real --output=short line goes in with hostname, it comes out
// without hostname while preserving timestamp and syslog identifier.
// Boot separators pass through unchanged.
func TestServeSSE_StripsHostname(t *testing.T) {
	t.Parallel()
	s := NewStreamer(fakeStreamer([]string{
		"May 19 14:23:45 raspberrypi airplanes-feed[1234]: connected",
		"-- Boot 2c5f... --",
	}, 50*time.Millisecond))

	w := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.ServeSSE(ctx, w, "feed"); err != nil {
		t.Fatal(err)
	}
	got := w.Body.String()
	if !strings.Contains(got, "data: May 19 14:23:45 airplanes-feed[1234]: connected\n\n") {
		t.Errorf("hostname not stripped from short-format line; body: %q", got)
	}
	if !strings.Contains(got, "data: -- Boot 2c5f... --\n\n") {
		t.Errorf("boot separator not passed through unchanged; body: %q", got)
	}
}

func TestStripHostname(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "normal short-format line",
			in:   "May 19 14:23:45 raspberrypi airplanes-feed[1234]: hello",
			want: "May 19 14:23:45 airplanes-feed[1234]: hello",
		},
		{
			name: "space-padded single-digit day",
			in:   "May  9 14:23:45 raspberrypi systemd[1]: Started airplanes-feed.service.",
			want: "May  9 14:23:45 systemd[1]: Started airplanes-feed.service.",
		},
		{
			name: "syslog id without pid bracket",
			in:   "Jan 02 03:04:05 pi-feeder kernel: USB disconnect",
			want: "Jan 02 03:04:05 kernel: USB disconnect",
		},
		{
			name: "boot separator passes through",
			in:   "-- Boot 2c5f1e3c4abc... --",
			want: "-- Boot 2c5f1e3c4abc... --",
		},
		{
			name: "continuation line passes through",
			in:   "    stack trace continued",
			want: "    stack trace continued",
		},
		{
			name: "too-short line passes through",
			in:   "ok",
			want: "ok",
		},
		{
			name: "no syslog identifier passes through",
			in:   "May 19 14:23:45 raspberrypi this-has-no-colon",
			want: "May 19 14:23:45 raspberrypi this-has-no-colon",
		},
		{
			name: "extra spaces in message preserved",
			in:   "May 19 14:23:45 raspberrypi airplanes-feed[1]: aircraft   412   pending",
			want: "May 19 14:23:45 airplanes-feed[1]: aircraft   412   pending",
		},
		{
			name: "empty line passes through",
			in:   "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripHostname(tc.in); got != tc.want {
				t.Errorf("stripHostname(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// nonFlushingRW deliberately does NOT implement http.Flusher so we can
// exercise the runtime guard.
type nonFlushingRW struct{ header http.Header }

func (n *nonFlushingRW) Header() http.Header         { return n.header }
func (n *nonFlushingRW) Write(p []byte) (int, error) { return len(p), nil }
func (n *nonFlushingRW) WriteHeader(int)             {}

func TestServeSSE_RequiresFlusher(t *testing.T) {
	t.Parallel()
	s := NewStreamer(fakeStreamer(nil, 0))
	w := &nonFlushingRW{header: http.Header{}}
	if err := s.ServeSSE(context.Background(), w, "feed"); err == nil {
		t.Fatal("expected error when ResponseWriter doesn't support flushing")
	}
}

func TestServeSSE_UnknownUnit(t *testing.T) {
	t.Parallel()
	s := NewStreamer(fakeStreamer(nil, 0))
	w := httptest.NewRecorder()
	if err := s.ServeSSE(context.Background(), w, "no-such-unit"); !errors.Is(err, ErrUnknownUnit) {
		t.Fatalf("err = %v, want ErrUnknownUnit", err)
	}
}

// deadlineRecordingRW implements http.Flusher + SetWriteDeadline so we can
// observe the per-write deadlines the handler applies. Production
// hijackable writers always support SetWriteDeadline; the existing
// httptest.NewRecorder path in TestServeSSE_StreamsLines exercises the
// ErrNotSupported fallback (it has no SetWriteDeadline method).
type deadlineRecordingRW struct {
	header    http.Header
	deadlines []time.Time
}

func (r *deadlineRecordingRW) Header() http.Header         { return r.header }
func (r *deadlineRecordingRW) Write(p []byte) (int, error) { return len(p), nil }
func (r *deadlineRecordingRW) WriteHeader(int)             {}
func (r *deadlineRecordingRW) Flush()                      {}
func (r *deadlineRecordingRW) SetWriteDeadline(t time.Time) error {
	r.deadlines = append(r.deadlines, t)
	return nil
}

func TestServeSSE_SetsPerWriteDeadline(t *testing.T) {
	t.Parallel()
	s := NewStreamer(fakeStreamer([]string{"line one", "line two"}, 20*time.Millisecond))

	w := &deadlineRecordingRW{header: http.Header{}}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := s.ServeSSE(ctx, w, "feed"); err != nil {
		t.Fatal(err)
	}

	if len(w.deadlines) < 2 {
		t.Fatalf("expected at least 2 SetWriteDeadline calls (one per line), got %d", len(w.deadlines))
	}
	for i, d := range w.deadlines {
		delta := time.Until(d)
		if delta <= 0 {
			t.Errorf("deadline #%d already in the past (delta=%v); want a future deadline", i, delta)
		}
		if delta > perWriteTimeout {
			t.Errorf("deadline #%d delta = %v; want at most perWriteTimeout=%v", i, delta, perWriteTimeout)
		}
	}
}

// rateStreamer emits "tick\n" at `interval` until `duration` elapses or
// ctx is canceled. Used by TestServeSSE_OutlastsHTTPServerWriteTimeout to
// generate a steady SSE feed across a real net/http server.
func rateStreamer(interval, duration time.Duration) wexec.StreamRunner {
	return func(ctx context.Context, w io.Writer, _ []string) error {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		deadline := time.After(duration)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-deadline:
				return nil
			case <-ticker.C:
				if _, err := io.WriteString(w, "tick\n"); err != nil {
					return err
				}
			}
		}
	}
}

// TestServeSSE_OutlastsHTTPServerWriteTimeout boots a real net/http server
// with a 200ms WriteTimeout and asserts the SSE stream keeps producing data
// well past that timeout. Without the per-write SetWriteDeadline plumbing
// in ServeSSE, the global WriteTimeout would chop the response off at
// ~200ms and we'd see at most ~4 ticks; with the fix the deadline gets
// pushed out 5s before every write so ticks keep flowing.
func TestServeSSE_OutlastsHTTPServerWriteTimeout(t *testing.T) {
	t.Parallel()

	const (
		tickInterval   = 50 * time.Millisecond
		streamDuration = 1500 * time.Millisecond
		writeTimeout   = 200 * time.Millisecond
		// Expect ~30 ticks; assert >= 15 for comfortable margin under -race.
		minExpectedTicks = 15
	)
	s := NewStreamer(rateStreamer(tickInterval, streamDuration))

	mux := http.NewServeMux()
	mux.HandleFunc("/api/log/feed", func(w http.ResponseWriter, r *http.Request) {
		_ = s.ServeSSE(r.Context(), w, "feed")
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:      mux,
		WriteTimeout: writeTimeout,
	}
	go func() { _ = srv.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	resp, err := http.Get("http://" + listener.Addr().String() + "/api/log/feed")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var got bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&got, resp.Body)
		close(readDone)
	}()
	select {
	case <-readDone:
	case <-time.After(streamDuration + 500*time.Millisecond):
		// Stream still open after fake streamer exited — force close so
		// the read goroutine returns and we can assert.
		_ = resp.Body.Close()
		<-readDone
	}

	n := strings.Count(got.String(), "tick")
	if n < minExpectedTicks {
		preview := got.String()
		if len(preview) > 500 {
			preview = preview[:500]
		}
		t.Errorf("received %d SSE ticks; want at least %d (http.Server.WriteTimeout=%v chopped the stream?). body head: %q",
			n, minExpectedTicks, writeTimeout, preview)
	}
}
