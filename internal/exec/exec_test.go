package exec

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRealRunner_HappyPath(t *testing.T) {
	t.Parallel()
	res, err := RealRunner(context.Background(), []string{"echo", "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "hello" {
		t.Errorf("stdout = %q, want %q", got, "hello")
	}
	if res.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", res.ExitCode)
	}
}

func TestRealRunner_NonzeroExit(t *testing.T) {
	t.Parallel()
	res, err := RealRunner(context.Background(), []string{"sh", "-c", "exit 7"})
	if err == nil {
		t.Fatal("expected error on exit 7")
	}
	if res.ExitCode != 7 {
		t.Errorf("exit = %d, want 7", res.ExitCode)
	}
}

func TestRealRunner_EmptyArgv(t *testing.T) {
	t.Parallel()
	if _, err := RealRunner(context.Background(), nil); err == nil {
		t.Fatal("empty argv should error")
	}
}

func TestRealRunner_RespectsCtxCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _ = RealRunner(ctx, []string{"sleep", "5"})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("did not honor ctx; elapsed = %v", elapsed)
	}
}

func TestRealStreamer_PipesStdout(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	err := RealStreamer(context.Background(), &buf,
		[]string{"sh", "-c", "echo line1; echo line2"})
	if err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "line1") || !strings.Contains(got, "line2") {
		t.Errorf("output = %q, want lines line1+line2", got)
	}
}

func TestRealStreamer_CtxCancelIsNotError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var buf bytes.Buffer
	err := RealStreamer(ctx, &buf, []string{"sh", "-c", "while true; do echo tick; sleep 0.05; done"})
	if err != nil {
		t.Errorf("ctx-canceled stream returned err = %v, want nil", err)
	}
}

func TestRealStreamer_EmptyArgv(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := RealStreamer(context.Background(), &buf, nil); err == nil {
		t.Fatal("empty argv should error")
	}
}
