package logs

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

func TestResolve_Whitelist(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"feed":      "airplanes-feed.service",
		"mlat":      "airplanes-mlat.service",
		"readsb":    "readsb.service",
		"dump978":   "dump978-fa.service",
		"uat":       "airplanes-978.service",
		"claim":     "airplanes-claim.service",
		"webconfig": "airplanes-webconfig.service",
		"update":    "airplanes-update.service",
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

// nonFlushingRW deliberately does NOT implement http.Flusher so we can
// exercise the runtime guard.
type nonFlushingRW struct{ header http.Header }

func (n *nonFlushingRW) Header() http.Header        { return n.header }
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

func (r *deadlineRecordingRW) Header() http.Header                  { return r.header }
func (r *deadlineRecordingRW) Write(p []byte) (int, error)          { return len(p), nil }
func (r *deadlineRecordingRW) WriteHeader(int)                      {}
func (r *deadlineRecordingRW) Flush()                               {}
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
