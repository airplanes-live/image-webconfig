package runtimestate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestRead_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	writeFile(t, path, `schema_version=1
service=airplanes-mlat
state=disabled
reason=mlat_enabled_false
mlat_user=
mlat_enabled=false
latitude=52.520
longitude=13.405
`)
	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if s.SchemaVersion != 1 {
		t.Errorf("schema version = %d, want 1", s.SchemaVersion)
	}
	if got := s.Values["state"]; got != "disabled" {
		t.Errorf("state = %q, want disabled", got)
	}
	if got := s.Values["reason"]; got != "mlat_enabled_false" {
		t.Errorf("reason = %q, want mlat_enabled_false", got)
	}
	if got, ok := s.Values["mlat_user"]; !ok || got != "" {
		t.Errorf("mlat_user = (%q, %v), want empty value present", got, ok)
	}
	if got := s.Values["latitude"]; got != "52.520" {
		t.Errorf("latitude = %q, want 52.520", got)
	}
}

func TestRead_UnknownSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	writeFile(t, path, "schema_version=2\nstate=enabled\n")
	_, err := Read(path)
	if !errors.Is(err, ErrUnknownSchema) {
		t.Errorf("Read: err = %v, want ErrUnknownSchema", err)
	}
}

func TestRead_MissingSchemaLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	writeFile(t, path, "service=airplanes-mlat\nstate=enabled\n")
	_, err := Read(path)
	if !errors.Is(err, ErrUnknownSchema) {
		t.Errorf("Read: err = %v, want ErrUnknownSchema", err)
	}
}

func TestRead_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	writeFile(t, path, "")
	_, err := Read(path)
	if !errors.Is(err, ErrUnknownSchema) {
		t.Errorf("Read: err = %v, want ErrUnknownSchema for empty file", err)
	}
}

func TestRead_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nope")
	_, err := Read(path)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read: err = %v, want fs.ErrNotExist", err)
	}
}

func TestRead_DirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := Read(path)
	if err == nil {
		t.Errorf("Read: err = nil, want non-regular-file error")
	}
}

func TestRead_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	writeFile(t, target, "schema_version=1\nstate=enabled\n")
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	_, err := Read(link)
	if err == nil {
		t.Errorf("Read: err = nil, want non-regular-file error for symlink")
	}
}

func TestRead_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	writeFile(t, path, `schema_version=1
state=enabled
no_equals_sign
1bad_key=ignored
state-also-bad-key=also_ignored
reason=ok
`)
	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got := s.Values["state"]; got != "enabled" {
		t.Errorf("state = %q, want enabled", got)
	}
	if got := s.Values["reason"]; got != "ok" {
		t.Errorf("reason = %q, want ok", got)
	}
	if _, ok := s.Values["1bad_key"]; ok {
		t.Errorf("1bad_key was accepted; should be skipped")
	}
	if _, ok := s.Values["state-also-bad-key"]; ok {
		t.Errorf("dashed key was accepted; should be skipped")
	}
}

func TestRead_ValueWithCRSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	writeFile(t, path, "schema_version=1\nstate=enabled\nreason=foo\rbar\n")
	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, ok := s.Values["reason"]; ok {
		t.Errorf("reason was accepted despite CR; should be skipped")
	}
	if got := s.Values["state"]; got != "enabled" {
		t.Errorf("state = %q, want enabled (other keys should still parse)", got)
	}
}

func TestAllowedDecisions(t *testing.T) {
	for _, tok := range []string{"enabled", "disabled", "misconfigured"} {
		if !AllowedDecisions[tok] {
			t.Errorf("AllowedDecisions[%q] = false, want true", tok)
		}
	}
	for _, tok := range []string{"unknown", "running", "", "ENABLED"} {
		if AllowedDecisions[tok] {
			t.Errorf("AllowedDecisions[%q] = true, want false", tok)
		}
	}
}

// Round-trip: simulate the bash writer's output and confirm the Go
// reader parses it identically. Mirrors scripts/lib/state-writer.sh
// from the feed repo: schema_version=1 first, then KEY=VALUE lines
// in caller-provided order, no shell-quoting on values.
func TestRead_RoundTripWithBashWriterShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state")
	// This is exactly what bash `airplanes_write_state` would produce
	// for an enabled MLAT daemon with effective config.
	writeFile(t, path, `schema_version=1
service=airplanes-mlat
state=enabled
reason=ok
decided_at=2026-05-08T11:42:13Z
mlat_enabled=true
mlat_user=alice
latitude=52.520
longitude=13.405
`)
	s, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := map[string]string{
		"service":      "airplanes-mlat",
		"state":        "enabled",
		"reason":       "ok",
		"decided_at":   "2026-05-08T11:42:13Z",
		"mlat_enabled": "true",
		"mlat_user":    "alice",
		"latitude":     "52.520",
		"longitude":    "13.405",
	}
	for k, v := range want {
		if got := s.Values[k]; got != v {
			t.Errorf("Values[%q] = %q, want %q", k, got, v)
		}
	}
}
