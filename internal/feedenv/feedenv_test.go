package feedenv

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/configspec"
	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
)

// TestAPIReadKeysMatchConfigspecAllReadKeys enforces the comment-block
// invariant in configspec.go that feedenv.APIReadKeys and
// configspec.AllReadKeys stay aligned. The duplicate exists so
// apply-config can validate-preserve keys without depending on this
// package's exec surface; the price is hand-syncing two lists, and this
// test makes drift a build break instead of a silent bug.
func TestAPIReadKeysMatchConfigspecAllReadKeys(t *testing.T) {
	t.Parallel()
	if !reflect.DeepEqual(APIReadKeys, configspec.AllReadKeys) {
		t.Errorf("feedenv.APIReadKeys and configspec.AllReadKeys diverged.\nAPIReadKeys: %v\nAllReadKeys: %v", APIReadKeys, configspec.AllReadKeys)
	}
}

// TestAPIReadKeysExcludeWebsiteURL pins the privacy decision: the feed
// CLI reports APL_FEED_WEBSITE_URL as readable, but it must never be
// part of webconfig's /api/config surface.
func TestAPIReadKeysExcludeWebsiteURL(t *testing.T) {
	t.Parallel()
	for _, k := range APIReadKeys {
		if k == "APL_FEED_WEBSITE_URL" {
			t.Fatal("APL_FEED_WEBSITE_URL must not be in APIReadKeys")
		}
	}
}

// cannedReader returns a Reader whose exec emits the given stdout.
func cannedReader(stdout string) *Reader {
	return &Reader{
		Exec: func(_ context.Context, _ []string) (wexec.Result, error) {
			return wexec.Result{Stdout: []byte(stdout)}, nil
		},
		Argv: []string{"fake", "apl-feed", "config", "show", "--json"},
	}
}

func TestReadAll_StringNullAndEmpty(t *testing.T) {
	t.Parallel()
	// LATITUDE present, UAT_INPUT explicitly empty (string ""), GAIN
	// absent (null). null must be omitted from the map; "" must be kept
	// — the metadata-gating and aggregator callers rely on the
	// distinction.
	r := cannedReader(`{"schema_version":1,"values":{"LATITUDE":"51.5","UAT_INPUT":"","GAIN":null}}`)
	got, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q, want 51.5", got["LATITUDE"])
	}
	v, present := got["UAT_INPUT"]
	if !present {
		t.Error("UAT_INPUT explicitly empty must be present with empty-string value")
	}
	if v != "" {
		t.Errorf("UAT_INPUT = %q, want empty string", v)
	}
	if _, ok := got["GAIN"]; ok {
		t.Error("GAIN null (absent key) leaked into the map")
	}
}

func TestReadAll_FiltersToAPIReadKeys(t *testing.T) {
	t.Parallel()
	// Keys outside the API surface filter — including the readable-only
	// APL_FEED_WEBSITE_URL — must never appear in the returned map.
	r := cannedReader(`{"schema_version":1,"values":{"LATITUDE":"51.5","APL_FEED_WEBSITE_URL":"https://dev.airplanes.live","MYSTERY_KEY":"nope"}}`)
	got, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["APL_FEED_WEBSITE_URL"]; ok {
		t.Error("APL_FEED_WEBSITE_URL leaked through the API surface filter")
	}
	if _, ok := got["MYSTERY_KEY"]; ok {
		t.Error("non-API key leaked into output")
	}
	if got["LATITUDE"] != "51.5" {
		t.Errorf("LATITUDE = %q, want 51.5", got["LATITUDE"])
	}
}

func TestReadAll_EmptyValuesObject(t *testing.T) {
	t.Parallel()
	// A fresh device: every readable key null collapses to {} after the
	// CLI side, or the envelope carries explicit nulls — either way the
	// result is an empty map and NO error (this replaced the old
	// missing-file ErrNotFound).
	r := cannedReader(`{"schema_version":1,"values":{}}`)
	got, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want empty map", got)
	}
}

func TestReadAll_MalformedJSON(t *testing.T) {
	t.Parallel()
	r := cannedReader(`not json at all`)
	if _, err := r.ReadAll(context.Background()); err == nil {
		t.Fatal("expected parse error for malformed JSON")
	}
}

func TestReadAll_MissingValues(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"absent": `{"schema_version":1}`,
		"null":   `{"schema_version":1,"values":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := cannedReader(body)
			if _, err := r.ReadAll(context.Background()); err == nil {
				t.Fatalf("expected error for %s values object", name)
			}
		})
	}
}

func TestReadAll_NonStringValueRejected(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"number": `{"schema_version":1,"values":{"LATITUDE":51.5}}`,
		"object": `{"schema_version":1,"values":{"LATITUDE":{"v":"51.5"}}}`,
		"bool":   `{"schema_version":1,"values":{"MLAT_ENABLED":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := cannedReader(body)
			if _, err := r.ReadAll(context.Background()); err == nil {
				t.Fatalf("expected error for %s entry, got nil", name)
			}
		})
	}
}

func TestReadAll_WrongSchemaVersion(t *testing.T) {
	t.Parallel()
	r := cannedReader(`{"schema_version":2,"values":{}}`)
	_, err := r.ReadAll(context.Background())
	if err == nil {
		t.Fatal("expected error for schema_version 2")
	}
	if !strings.Contains(err.Error(), "unsupported 2") {
		t.Errorf("err = %v, want it to name the unsupported version", err)
	}
}

func TestReadAll_ExecFailureCarriesStderr(t *testing.T) {
	t.Parallel()
	// An old feed without the `config show` subcommand exits non-zero
	// with a usage message on stderr — the wrapped error must surface
	// both so the journal line is actionable.
	r := &Reader{
		Exec: func(_ context.Context, _ []string) (wexec.Result, error) {
			return wexec.Result{
				Stderr:   []byte("unknown subcommand: config"),
				ExitCode: 64,
			}, errors.New("exit status 64")
		},
	}
	_, err := r.ReadAll(context.Background())
	if err == nil {
		t.Fatal("expected exec failure to propagate")
	}
	if !strings.Contains(err.Error(), "unknown subcommand") || !strings.Contains(err.Error(), "64") {
		t.Errorf("err = %v, want stderr text and exit code", err)
	}
}

func TestReadAll_ContextCancellation(t *testing.T) {
	t.Parallel()
	// A caller-side deadline shorter than readTimeout must win: the
	// derived context the runner sees is done when the parent is.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	r := &Reader{
		Exec: func(cctx context.Context, _ []string) (wexec.Result, error) {
			<-cctx.Done()
			return wexec.Result{ExitCode: -1}, cctx.Err()
		},
	}
	if _, err := r.ReadAll(ctx); err == nil {
		t.Fatal("expected error from canceled context")
	}
}

func TestReadAll_AppliesInternalTimeout(t *testing.T) {
	t.Parallel()
	// ReadAll runs on live request paths; even with an unbounded parent
	// context the exec must see a deadline.
	var deadline time.Time
	var hasDeadline bool
	r := &Reader{
		Exec: func(cctx context.Context, _ []string) (wexec.Result, error) {
			deadline, hasDeadline = cctx.Deadline()
			return wexec.Result{Stdout: []byte(`{"schema_version":1,"values":{}}`)}, nil
		},
	}
	if _, err := r.ReadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasDeadline {
		t.Fatal("exec context has no deadline; readTimeout not applied")
	}
	if remaining := time.Until(deadline); remaining > readTimeout {
		t.Errorf("deadline %v from now, want <= %v", remaining, readTimeout)
	}
}

func TestReadAll_ZeroValueArgvDefaults(t *testing.T) {
	t.Parallel()
	// A bare &Reader{Exec: ...} with no Argv must exec DefaultArgv, not
	// an empty argv.
	var seen []string
	r := &Reader{
		Exec: func(_ context.Context, argv []string) (wexec.Result, error) {
			seen = append([]string(nil), argv...)
			return wexec.Result{Stdout: []byte(`{"schema_version":1,"values":{}}`)}, nil
		},
	}
	if _, err := r.ReadAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, DefaultArgv) {
		t.Errorf("argv = %v, want DefaultArgv %v", seen, DefaultArgv)
	}
}

func TestReadAll_ZeroValueExecDefaultsToRealRunner(t *testing.T) {
	t.Parallel()
	// nil Exec falls back to wexec.RealRunner. Point the argv at a real
	// binary that prints a valid envelope so the test stays hermetic.
	r := &Reader{Argv: []string{"/bin/echo", `{"schema_version":1,"values":{"LATITUDE":"1.0"}}`}}
	got, err := r.ReadAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["LATITUDE"] != "1.0" {
		t.Errorf("LATITUDE = %q, want 1.0", got["LATITUDE"])
	}
}

func TestNew_PinsDefaults(t *testing.T) {
	t.Parallel()
	r := New()
	if !reflect.DeepEqual(r.Argv, DefaultArgv) {
		t.Errorf("New().Argv = %v, want %v", r.Argv, DefaultArgv)
	}
	if r.Exec == nil {
		t.Error("New().Exec is nil, want RealRunner")
	}
}

func TestWebsiteURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{"present", `{"schema_version":1,"values":{"APL_FEED_WEBSITE_URL":"https://dev.airplanes.live"}}`, "https://dev.airplanes.live"},
		{"absent (null)", `{"schema_version":1,"values":{"APL_FEED_WEBSITE_URL":null}}`, ""},
		{"absent (omitted)", `{"schema_version":1,"values":{"LATITUDE":"1.0"}}`, ""},
		{"explicitly empty", `{"schema_version":1,"values":{"APL_FEED_WEBSITE_URL":""}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := cannedReader(tc.body)
			if got := r.WebsiteURL(context.Background()); got != tc.want {
				t.Errorf("WebsiteURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWebsiteURL_ErrorCollapsesToEmpty(t *testing.T) {
	t.Parallel()
	// Any failure — exec or contract violation — collapses to "" so the
	// caller falls back to the production default.
	for name, r := range map[string]*Reader{
		"exec failure": {
			Exec: func(_ context.Context, _ []string) (wexec.Result, error) {
				return wexec.Result{ExitCode: 1}, errors.New("boom")
			},
		},
		"malformed envelope": cannedReader(`{"schema_version":1}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := r.WebsiteURL(context.Background()); got != "" {
				t.Errorf("WebsiteURL() = %q, want empty", got)
			}
		})
	}
}
