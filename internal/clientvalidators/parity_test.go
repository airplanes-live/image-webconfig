package clientvalidators

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	feedValidatorsURL  = "https://raw.githubusercontent.com/airplanes-live/feed/dev/scripts/lib/configure-validators.sh"
	wifiValidatorsRel  = "../../files/usr/local/lib/airplanes/wifi-validators.sh"
	aplAggregatorRel   = "../../files/usr/local/bin/apl-aggregator"
	appJSRel           = "../../web/assets/app.js"
	altitudeFixtureRel = "../feedmeta/testdata/altitude-canonicalization.json"
	nodeRunnerRel      = "testdata/run_js_validators.js"

	fetchTimeout  = 15 * time.Second
	maxFetchBytes = 64 * 1024 // configure-validators.sh is ~5KB; defensive cap

	bashSourceFailExit = 99 // distinguishable from validator exit 0/1
)

// bashSource indicates which file the validator's bash twin lives in.
type bashSource int

const (
	sourceFeed       bashSource = iota // fetched from airplanes-live/feed/dev
	sourceWifi                         // in-repo files/usr/local/lib/airplanes/wifi-validators.sh
	sourceAggregator                   // extracted from files/usr/local/bin/apl-aggregator
)

// vector is one test input with per-side expected outcomes. ExpectedJSValid
// and ExpectedBashValid are typically equal; setting them independently
// documents an intentional divergence (e.g., MLAT_USER empty).
//
// Transform models the UI's payload normalisation: form inputs for some
// feed config fields are .trim()'d before POSTing to apl-feed apply, so
// the bash side at apply-time sees the trimmed value, not the raw form
// value. Wi-Fi vectors use identityString — SSID and PSK preserve
// whitespace verbatim.
type vector struct {
	Input             string
	ExpectedJSValid   bool
	ExpectedBashValid bool
	Transform         func(string) string
}

// vec is the common-case constructor: both sides expected to agree;
// payload trimmed by default (applies to all feed validators).
func vec(input string, valid bool) vector {
	return vector{
		Input:             input,
		ExpectedJSValid:   valid,
		ExpectedBashValid: valid,
		Transform:         strings.TrimSpace,
	}
}

// vecWifi: Wi-Fi vectors never trim.
func vecWifi(input string, valid bool) vector {
	return vector{
		Input:             input,
		ExpectedJSValid:   valid,
		ExpectedBashValid: valid,
		Transform:         identityString,
	}
}

// vecDiverge documents an intentional JS-vs-bash divergence.
func vecDiverge(input string, jsValid, bashValid bool, trim bool) vector {
	transform := identityString
	if trim {
		transform = strings.TrimSpace
	}
	return vector{
		Input:             input,
		ExpectedJSValid:   jsValid,
		ExpectedBashValid: bashValid,
		Transform:         transform,
	}
}

func identityString(s string) string { return s }

// validatorSpec describes one JS validator and its bash twin.
type validatorSpec struct {
	JSName   string
	BashName string
	Source   bashSource
	Vectors  []vector
}

// feedSpecs covers validators whose bash twin lives in
// feed/scripts/lib/configure-validators.sh.
func feedSpecs() []validatorSpec {
	bigMlatUser := strings.Repeat("a", 64)
	tooBigMlatUser := strings.Repeat("a", 65)
	bigSerial := strings.Repeat("a", 32)
	tooBigSerial := strings.Repeat("a", 33)
	return []validatorSpec{
		{
			JSName: "isValidLatitude", BashName: "valid_latitude", Source: sourceFeed,
			Vectors: []vector{
				vec("0", true),
				vec("42.5", true),
				vec("-42.5", true),
				vec("90", true),
				vec("-90", true),
				vec("90.0001", false),
				vec("-90.0001", false),
				vec("180", false),
				vec("abc", false),
				vec("", false),
				vec("+42", true),
				vec(".5", false),
				vec("42.", false),
				vec("1e1", false),
				vec(" 42 ", true), // payload trims to "42", both valid
			},
		},
		{
			JSName: "isValidLongitude", BashName: "valid_longitude", Source: sourceFeed,
			Vectors: []vector{
				vec("0", true),
				vec("180", true),
				vec("-180", true),
				vec("180.0001", false),
				vec("-180.0001", false),
				vec("xyz", false),
				vec("", false),
			},
		},
		{
			JSName: "isValidAltitude", BashName: "valid_altitude", Source: sourceFeed,
			Vectors: []vector{
				vec("", true),
				vec("0", true),
				vec("120m", true),
				vec("400ft", true),
				vec("33000ft", false),
				vec("10001", false),
				vec("-1001m", false),
				vec("garbage", false),
				vec("12m3", false),
			},
		},
		{
			JSName: "isValidMlatUser", BashName: "valid_mlat_user_strict", Source: sourceFeed,
			Vectors: []vector{
				// Intentional divergence: JS accepts empty (form sends
				// MLAT_USER="" → daemon picks Anonymous fallback).
				// Bash _strict rejects empty; callers handle empty themselves.
				vecDiverge("", true, false, true),
				vec("alice", true),
				vec("ALice_99-x", true),
				vec("alice!", false),
				vec(bigMlatUser, true),
				vec(tooBigMlatUser, false),
				vec("space here", false),
			},
		},
		{
			JSName: "isValidGain", BashName: "valid_gain", Source: sourceFeed,
			Vectors: []vector{
				vec("auto", true),
				vec("min", true),
				vec("max", true),
				vec("0", true),
				vec("60", true),
				vec("30.5", true),
				vec("61", false),
				vec("-1", false),
				vec("", false),
				vec("1e1", false),
				vec(".5", false),
			},
		},
		{
			JSName: "isValidReadsbSdrSerial", BashName: "valid_readsb_sdr_serial", Source: sourceFeed,
			Vectors: []vector{
				vec("", true), // both accept empty (single-SDR default)
				vec("1090", true),
				// All-numeric serials are accepted — readsb's index-vs-serial
				// ambiguity for small integers is a UI warning, not a
				// validation rule.
				vec("0", true),
				vec("00000001", true),
				vec("abc-def_123", true),
				vec("abc!", false),
				vec("has space", false),
				vec(bigSerial, true),
				vec(tooBigSerial, false),
			},
		},
		{
			JSName: "isValidDump978Serial", BashName: "valid_dump978_serial", Source: sourceFeed,
			Vectors: []vector{
				vec("", true), // both accept empty
				vec("978", true),
				vec("abc-def_123", true),
				vec("abc!", false),
				vec(bigSerial, true),
				vec(tooBigSerial, false),
			},
		},
		{
			JSName: "isValidDump978Gain", BashName: "valid_dump978_gain", Source: sourceFeed,
			Vectors: []vector{
				vec("42.1", true),
				vec("0", true),
				vec("60", true),
				vec("61", false),
				vec("auto", false), // dump978-fa rejects auto/min/max
				vec("", false),
			},
		},
	}
}

// wifiSpecs covers validators whose bash twin lives in the in-repo
// files/usr/local/lib/airplanes/wifi-validators.sh.
//
// NUL (0x00) is deliberately not tested: argv strings on Linux are C
// strings (execve null-terminates), so a Go test can't pass a NUL byte
// to a bash subprocess. Other C0 controls (0x01-0x1F, 0x7F) and bytes
// like 0x0A pass through fine; they exercise the same control-char gate.
func wifiSpecs() []validatorSpec {
	bigSSID := strings.Repeat("a", 32)
	tooBigSSID := strings.Repeat("a", 33)
	hex64 := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	psk63 := strings.Repeat("a", 63)
	pskTooLong := strings.Repeat("a", 65)
	return []validatorSpec{
		{
			JSName: "isValidWifiSSID", BashName: "apl_wifi_valid_ssid", Source: sourceWifi,
			Vectors: []vector{
				vecWifi("", false),
				vecWifi("home", true),
				vecWifi(bigSSID, true),
				vecWifi(tooBigSSID, false),
				vecWifi("with space", true),
				vecWifi(" leading-space", true),
				vecWifi("ñ", true),              // 2 bytes; well under 32
				vecWifi("with\nnewline", false), // control char rejected
				vecWifi("\x7f", false),          // DEL is a control char
			},
		},
		{
			JSName: "isValidWifiPSK", BashName: "apl_wifi_valid_psk", Source: sourceWifi,
			Vectors: []vector{
				vecWifi("", false),
				vecWifi("12345678", true), // 8 chars; ASCII
				vecWifi(psk63, true),
				vecWifi(hex64, true), // exactly 64 hex chars
				vecWifi(pskTooLong, false),
				vecWifi("short", false), // 5 chars, < 8 and not 64-hex
				vecWifi("with\nnewline", false),
			},
		},
		{
			JSName: "isValidWifiCountry", BashName: "apl_wifi_valid_country", Source: sourceWifi,
			Vectors: []vector{
				vecWifi("DE", true),
				vecWifi("US", true),
				vecWifi("de", false), // lowercase rejected
				vecWifi("DEU", false),
				vecWifi("D", false),
				vecWifi("", false),
			},
		},
		{
			JSName: "isValidWifiPriority", BashName: "apl_wifi_valid_priority", Source: sourceWifi,
			Vectors: []vector{
				vecWifi("0", true),
				vecWifi("1", true),
				vecWifi("999", true),
				vecWifi("1000", false),
				vecWifi("01", false), // leading zero rejected
				vecWifi("-1", false),
				vecWifi("", false),
			},
		},
	}
}

// vecAgg: aggregator vectors. The JS validators .trim() internally and the form
// posts trimmed values, so the bash twin sees the trimmed input — model that with
// TrimSpace, like the feed validators.
func vecAgg(input string, valid bool) vector {
	return vector{
		Input:             input,
		ExpectedJSValid:   valid,
		ExpectedBashValid: valid,
		Transform:         strings.TrimSpace,
	}
}

// aggregatorSpecs covers the third-party-aggregator field validators whose bash
// twin lives in the apl-aggregator helper (_valid_email / _valid_fr24_key /
// _valid_feeder_id).
func aggregatorSpecs() []validatorSpec {
	key40 := strings.Repeat("a", 40)
	key41 := strings.Repeat("a", 41)
	return []validatorSpec{
		{
			JSName: "isValidAggEmail", BashName: "_valid_email", Source: sourceAggregator,
			Vectors: []vector{
				vecAgg("a@b.co", true),
				vecAgg("alice@example.com", true),
				vecAgg(" a@b.co ", true), // trimmed both sides
				vecAgg("", false),
				vecAgg("noat", false),
				vecAgg("no@domain", false), // no dot after @
				vecAgg("a@b@c.com", false), // two @
				vecAgg("a b@c.com", false), // space inside
			},
		},
		{
			JSName: "isValidFr24Key", BashName: "_valid_fr24_key", Source: sourceAggregator,
			Vectors: []vector{
				vecAgg("ABC123", true), // 6 alnum
				vecAgg(key40, true),
				vecAgg("abcde", false), // 5, too short
				vecAgg(key41, false),   // 41, too long
				vecAgg("ABC-123", false),
				vecAgg("", false),
			},
		},
		{
			JSName: "isValidFeederId", BashName: "_valid_feeder_id", Source: sourceAggregator,
			Vectors: []vector{
				vecAgg("00000000-0000-4000-8000-000000000000", true),
				vecAgg("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", true),
				vecAgg("AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", true), // hex is case-insensitive
				vecAgg("not-a-uuid", false),
				vecAgg("", false),
				vecAgg("00000000-0000-4000-8000-00000000000", false),  // last group only 11
				vecAgg("gggggggg-0000-4000-8000-000000000000", false), // non-hex
			},
		},
	}
}

// extractAggregatorValidators pulls the three _valid_* functions out of the
// apl-aggregator helper into a temp, sourceable script. The helper itself can't
// be sourced wholesale — it runs `main "$@"` at the bottom — so callBashValidator
// (which sources its script) needs the functions isolated. Each function is a
// top-level `_valid_X() {` … line through a column-0 `}`.
func extractAggregatorValidators(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(aplAggregatorRel)
	if err != nil {
		t.Fatalf("read apl-aggregator: %v", err)
	}
	want := map[string]bool{"_valid_email": true, "_valid_fr24_key": true, "_valid_feeder_id": true}
	found := map[string]bool{}
	lines := strings.Split(string(raw), "\n")
	var out strings.Builder
	out.WriteString("#!/usr/bin/env bash\n")
	for i := 0; i < len(lines); i++ {
		name := strings.TrimSuffix(lines[i], "() {")
		if name == lines[i] || want[name] == false {
			continue
		}
		out.WriteString(lines[i] + "\n")
		for j := i + 1; j < len(lines); j++ {
			out.WriteString(lines[j] + "\n")
			if lines[j] == "}" {
				i = j
				break
			}
		}
		found[name] = true
	}
	for n := range want {
		if !found[n] {
			t.Fatalf("could not extract %s from %s", n, aplAggregatorRel)
		}
	}
	tmp, err := os.CreateTemp(t.TempDir(), "agg-validators-*.sh")
	if err != nil {
		t.Fatalf("create temp aggregator validators: %v", err)
	}
	if _, err := tmp.WriteString(out.String()); err != nil {
		tmp.Close()
		t.Fatalf("write temp aggregator validators: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp aggregator validators: %v", err)
	}
	return tmp.Name()
}

// altitudeCase mirrors one entry in altitude-canonicalization.json.
type altitudeCase struct {
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
	ExpectedOK     bool   `json:"expected_ok"`
	Note           string `json:"note,omitempty"`
}

func loadAltitudeFixture(t *testing.T) []altitudeCase {
	t.Helper()
	raw, err := os.ReadFile(altitudeFixtureRel)
	if err != nil {
		t.Fatalf("read altitude fixture %s: %v", altitudeFixtureRel, err)
	}
	var doc struct {
		Cases []altitudeCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode altitude fixture: %v", err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("altitude fixture: zero cases")
	}
	return doc.Cases
}

// fetchFeedValidators downloads feed's configure-validators.sh once. Returns
// the local temp path on success. On TESTING_OFFLINE=1 or network failure,
// returns ok=false; the caller skips feed-dependent subtests.
func fetchFeedValidators(t *testing.T) (string, bool) {
	t.Helper()
	if os.Getenv("TESTING_OFFLINE") == "1" {
		t.Logf("TESTING_OFFLINE=1; skipping feed-validators fetch")
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedValidatorsURL, nil)
	if err != nil {
		t.Logf("build feed-validators request: %v", err)
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("fetch feed-validators: %v", err)
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Logf("HTTP %d fetching feed-validators", resp.StatusCode)
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		t.Logf("read feed-validators body: %v", err)
		return "", false
	}
	if len(body) == 0 {
		t.Logf("empty feed-validators body")
		return "", false
	}
	tmp, err := os.CreateTemp("", "feed-validators-*.sh")
	if err != nil {
		t.Logf("create temp file: %v", err)
		return "", false
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		t.Logf("write temp file: %v", err)
		return "", false
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		t.Logf("close temp file: %v", err)
		return "", false
	}
	t.Cleanup(func() { _ = os.Remove(tmp.Name()) })
	return tmp.Name(), true
}

// jsResults collects the Node runner's output, keyed for fast lookup.
type jsResults struct {
	bools        map[string]bool      // key: validator|input → ok
	altitude     map[string]altResult // key: input → result
	runnerErrors []string
}

type altResult struct {
	output     string
	outputNull bool
	ok         bool
}

// runJSValidators batches every vector into one Node subprocess call.
func runJSValidators(t *testing.T, specs []validatorSpec, altitudeCases []altitudeCase) (jsResults, error) {
	t.Helper()
	type jsReqVec struct {
		Validator string `json:"validator"`
		Input     string `json:"input"`
	}
	type jsReqAlt struct {
		Input string `json:"input"`
	}
	type jsReq struct {
		Vectors       []jsReqVec `json:"vectors"`
		AltitudeCases []jsReqAlt `json:"altitudeCases"`
	}
	req := jsReq{}
	for _, s := range specs {
		for _, v := range s.Vectors {
			req.Vectors = append(req.Vectors, jsReqVec{Validator: s.JSName, Input: v.Input})
		}
	}
	for _, c := range altitudeCases {
		req.AltitudeCases = append(req.AltitudeCases, jsReqAlt{Input: c.Input})
	}
	body, err := json.Marshal(req)
	if err != nil {
		return jsResults{}, fmt.Errorf("marshal js request: %w", err)
	}
	appJSPath, err := filepath.Abs(appJSRel)
	if err != nil {
		return jsResults{}, fmt.Errorf("abs app.js: %w", err)
	}
	runnerPath, err := filepath.Abs(nodeRunnerRel)
	if err != nil {
		return jsResults{}, fmt.Errorf("abs runner: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", runnerPath, appJSPath)
	cmd.Stdin = strings.NewReader(string(body))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return jsResults{}, fmt.Errorf("node runner failed: %v; stderr: %s", err, stderr.String())
	}
	var resp struct {
		Results []struct {
			Validator string `json:"validator"`
			Input     string `json:"input"`
			OK        bool   `json:"ok"`
		} `json:"results"`
		AltitudeResults []struct {
			Input  string  `json:"input"`
			Output *string `json:"output"`
			OK     bool    `json:"ok"`
		} `json:"altitudeResults"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		return jsResults{}, fmt.Errorf("decode node output: %w; raw: %q", err, stdout.String())
	}
	results := jsResults{
		bools:        make(map[string]bool, len(resp.Results)),
		altitude:     make(map[string]altResult, len(resp.AltitudeResults)),
		runnerErrors: resp.Errors,
	}
	for _, r := range resp.Results {
		results.bools[r.Validator+"|"+r.Input] = r.OK
	}
	for _, r := range resp.AltitudeResults {
		ar := altResult{ok: r.OK}
		if r.Output != nil {
			ar.output = *r.Output
		} else {
			ar.outputNull = true
		}
		results.altitude[r.Input] = ar
	}
	return results, nil
}

// callBashValidator invokes one bash validator with one input. Returns
// (valid, err). err is non-nil for subprocess/sourcing failures, NOT for
// the validator simply rejecting (which is exit 1 → valid=false, err=nil).
func callBashValidator(t *testing.T, scriptPath, funcName, input string) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "--noprofile", "--norc", "-c",
		`source "$1" || exit `+fmt.Sprintf("%d", bashSourceFailExit)+`; "$2" "$3"`,
		"bash", scriptPath, funcName, input)
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LC_ALL=C",
		"HOME=" + t.TempDir(),
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		rc := exitErr.ExitCode()
		switch rc {
		case 0:
			return true, nil
		case 1:
			return false, nil
		case bashSourceFailExit:
			return false, fmt.Errorf("bash source failed for %s: %s", scriptPath, strings.TrimSpace(stderr.String()))
		default:
			return false, fmt.Errorf("bash %s(%q) unexpected exit %d: %s", funcName, input, rc, strings.TrimSpace(stderr.String()))
		}
	}
	return false, fmt.Errorf("bash subprocess: %v; stderr: %s", err, strings.TrimSpace(stderr.String()))
}

func TestValidatorParity(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not in PATH (%v); install Node to exercise client-validator parity", err)
	}
	wifiPath, err := filepath.Abs(wifiValidatorsRel)
	if err != nil {
		t.Fatalf("abs wifi-validators: %v", err)
	}
	if _, err := os.Stat(wifiPath); err != nil {
		t.Fatalf("wifi-validators.sh not found at %s: %v", wifiPath, err)
	}
	feedPath, feedReady := fetchFeedValidators(t)
	aggPath := extractAggregatorValidators(t)

	allSpecs := append([]validatorSpec{}, wifiSpecs()...)
	allSpecs = append(allSpecs, aggregatorSpecs()...)
	if feedReady {
		allSpecs = append(allSpecs, feedSpecs()...)
	}
	jsR, err := runJSValidators(t, allSpecs, nil)
	if err != nil {
		t.Fatalf("js runner: %v", err)
	}
	for _, e := range jsR.runnerErrors {
		t.Errorf("js runner error: %s", e)
	}

	runSpec := func(t *testing.T, spec validatorSpec) {
		var script string
		switch spec.Source {
		case sourceFeed:
			script = feedPath
		case sourceWifi:
			script = wifiPath
		case sourceAggregator:
			script = aggPath
		default:
			t.Fatalf("unknown bash source for %s", spec.JSName)
		}
		for _, v := range spec.Vectors {
			v := v
			t.Run(fmt.Sprintf("%s/%q", spec.JSName, v.Input), func(t *testing.T) {
				jsKey := spec.JSName + "|" + v.Input
				jsOK, present := jsR.bools[jsKey]
				if !present {
					t.Fatalf("JS result missing for %s", jsKey)
				}
				if jsOK != v.ExpectedJSValid {
					t.Errorf("JS %s(%q) = %v, want %v", spec.JSName, v.Input, jsOK, v.ExpectedJSValid)
				}
				bashInput := v.Input
				if v.Transform != nil {
					bashInput = v.Transform(v.Input)
				}
				bashOK, callErr := callBashValidator(t, script, spec.BashName, bashInput)
				if callErr != nil {
					t.Fatalf("bash %s(%q): %v", spec.BashName, bashInput, callErr)
				}
				if bashOK != v.ExpectedBashValid {
					t.Errorf("bash %s(%q) = %v, want %v", spec.BashName, bashInput, bashOK, v.ExpectedBashValid)
				}
			})
		}
	}

	t.Run("wifi", func(t *testing.T) {
		for _, spec := range wifiSpecs() {
			runSpec(t, spec)
		}
	})
	t.Run("aggregator", func(t *testing.T) {
		for _, spec := range aggregatorSpecs() {
			runSpec(t, spec)
		}
	})
	if feedReady {
		t.Run("feed", func(t *testing.T) {
			for _, spec := range feedSpecs() {
				runSpec(t, spec)
			}
		})
	} else {
		t.Log("feed cross-repo fetch skipped; remote validators not exercised this run")
	}
}

func TestAltitudeJSCanonicalization(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node not in PATH (%v); install Node to exercise altitude canonicalization parity", err)
	}
	cases := loadAltitudeFixture(t)
	jsR, err := runJSValidators(t, nil, cases)
	if err != nil {
		t.Fatalf("js runner: %v", err)
	}
	for _, e := range jsR.runnerErrors {
		t.Errorf("js runner error: %s", e)
	}
	for _, c := range cases {
		c := c
		t.Run(c.Input, func(t *testing.T) {
			r, ok := jsR.altitude[c.Input]
			if !ok {
				t.Fatalf("JS altitude result missing for %q", c.Input)
			}
			if r.ok != c.ExpectedOK {
				t.Errorf("JS altitudeToBareMetres(%q) ok = %v, want %v", c.Input, r.ok, c.ExpectedOK)
			}
			if c.ExpectedOK {
				if r.outputNull {
					t.Errorf("JS altitudeToBareMetres(%q) returned null, want %q", c.Input, c.ExpectedOutput)
				} else if r.output != c.ExpectedOutput {
					t.Errorf("JS altitudeToBareMetres(%q) = %q, want %q", c.Input, r.output, c.ExpectedOutput)
				}
			}
		})
	}
}
