package schemacache

import (
	"context"
	"errors"
	"testing"

	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

func stub(out []byte, err error) wexec.CommandRunner {
	return func(_ context.Context, _ []string) (wexec.Result, error) {
		return wexec.Result{Stdout: out, ExitCode: 0}, err
	}
}

func TestLoad_Happy(t *testing.T) {
	body := []byte(`{"version":1,"writable_keys":["LATITUDE","MLAT_USER"],"readable_keys":["LATITUDE","INPUT","INPUT_TYPE","MLAT_USER"]}`)
	c := New(nil, stub(body, nil))
	if err := c.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.IsWritable("LATITUDE") {
		t.Errorf("IsWritable(LATITUDE) should be true")
	}
	if c.IsWritable("INPUT") {
		t.Errorf("IsWritable(INPUT) should be false (read-only)")
	}
	if !c.IsReadable("INPUT_TYPE") {
		t.Errorf("IsReadable(INPUT_TYPE) should be true")
	}
	if c.Degraded() {
		t.Errorf("Degraded should be false after successful Load")
	}
}

func TestLoad_ExecError_TriggersDegraded(t *testing.T) {
	c := New(nil, stub(nil, errors.New("apl-feed not found")))
	if err := c.Load(context.Background()); err == nil {
		t.Fatalf("Load: expected error")
	}
	if !c.Degraded() {
		t.Errorf("Degraded should be true on first-boot failure")
	}
}

func TestLoad_ParseError_TriggersDegraded(t *testing.T) {
	c := New(nil, stub([]byte("not json"), nil))
	if err := c.Load(context.Background()); err == nil {
		t.Fatalf("Load: expected parse error")
	}
	if !c.Degraded() {
		t.Errorf("Degraded should be true on parse failure with empty cache")
	}
}

func TestLoad_UnsupportedVersion_TriggersDegraded(t *testing.T) {
	body := []byte(`{"version":99,"writable_keys":["x"],"readable_keys":["x"]}`)
	c := New(nil, stub(body, nil))
	if err := c.Load(context.Background()); err == nil {
		t.Fatalf("Load: expected version error")
	}
	if !c.Degraded() {
		t.Errorf("Degraded should be true on unknown schema version")
	}
}

func TestLoad_RefreshFailure_KeepsPreviousCache(t *testing.T) {
	body := []byte(`{"version":1,"writable_keys":["LATITUDE"],"readable_keys":["LATITUDE"]}`)
	c := New(nil, stub(body, nil))
	if err := c.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Swap to a failing runner and force a refresh.
	c.exec = stub(nil, errors.New("transient"))
	if err := c.Load(context.Background()); err == nil {
		t.Fatalf("Load: expected error on refresh")
	}
	// Previous cache must survive the failed refresh.
	if !c.IsWritable("LATITUDE") {
		t.Errorf("LATITUDE should still be writable after failed refresh")
	}
	if c.Degraded() {
		t.Errorf("Degraded should remain false when previous cache is intact")
	}
}
