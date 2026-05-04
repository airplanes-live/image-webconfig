// Package exec wraps os/exec for webconfig's external-command shell-outs.
// All callers go through CommandRunner, a single function type that returns
// stdout / stderr / error from a fixed argv slice. Real production code
// uses RealRunner, which executes the system process with a context-bound
// timeout. Tests inject their own CommandRunner to canned-respond.
//
// argv slices are always concrete strings — never concatenated, never
// run through a shell. Sudoers entries elsewhere in the system pin the
// allowed argv shape; keeping the Go side argv-only keeps that promise
// honest.
package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
)

// Result carries the outputs of a single Run.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// CommandRunner is the function-type interface every shell-out goes through.
type CommandRunner func(ctx context.Context, argv []string) (Result, error)

// StreamRunner is the streaming variant used by /api/<unit>/log SSE.
// Until ctx is canceled, lines from the child's stdout are written to w as
// they arrive. Returns nil on clean ctx cancel; non-nil on process error.
type StreamRunner func(ctx context.Context, w io.Writer, argv []string) error

// RealRunner runs argv via os/exec.CommandContext. argv must be non-empty.
func RealRunner(ctx context.Context, argv []string) (Result, error) {
	if len(argv) == 0 {
		return Result{}, errors.New("exec: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	return res, err
}

// RealStreamer runs argv and copies the child's stdout into w until the
// child exits or ctx is canceled. exec.CommandContext kills the child
// when ctx is done; the io.Copy exits when stdout is closed.
func RealStreamer(ctx context.Context, w io.Writer, argv []string) error {
	if len(argv) == 0 {
		return errors.New("exec: empty argv")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	_, _ = io.Copy(w, stdout)
	werr := cmd.Wait()
	if ctx.Err() != nil {
		return nil // ctx-driven shutdown isn't an error to the caller
	}
	return werr
}
