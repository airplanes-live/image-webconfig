package clientvalidators

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// displayRunnerRel is the node harness that exercises the client-only
// altitude display + locale helpers in app.js (see run_js_display.js).
const displayRunnerRel = "testdata/run_js_display.js"

type localeCase struct {
	Languages []string
	Imperial  bool
}

type altDisplayCase struct {
	Metres   string
	Imperial bool
	Output   string
}

// runJSDisplay invokes the display harness with the given cases and returns
// the JS results keyed for lookup. Mirrors runJSValidators' node-subprocess
// shape; kept separate so the validator-parity flow stays untouched.
func runJSDisplay(t *testing.T, locales []localeCase, alts []altDisplayCase) (map[string]bool, map[string]string) {
	t.Helper()

	type reqLocale struct {
		Languages []string `json:"languages"`
	}
	type reqAlt struct {
		Metres   string `json:"metres"`
		Imperial bool   `json:"imperial"`
	}
	req := struct {
		LocaleCases     []reqLocale `json:"localeCases"`
		AltDisplayCases []reqAlt    `json:"altDisplayCases"`
	}{}
	for _, c := range locales {
		req.LocaleCases = append(req.LocaleCases, reqLocale{Languages: c.Languages})
	}
	for _, c := range alts {
		req.AltDisplayCases = append(req.AltDisplayCases, reqAlt{Metres: c.Metres, Imperial: c.Imperial})
	}
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal display request: %v", err)
	}

	appJSPath, err := filepath.Abs(appJSRel)
	if err != nil {
		t.Fatalf("abs app.js: %v", err)
	}
	runnerPath, err := filepath.Abs(displayRunnerRel)
	if err != nil {
		t.Fatalf("abs runner: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", runnerPath, appJSPath)
	cmd.Stdin = strings.NewReader(string(body))
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("node display runner failed: %v; stderr: %s", err, stderr.String())
	}

	var resp struct {
		LocaleResults []struct {
			Languages []string `json:"languages"`
			Imperial  bool     `json:"imperial"`
		} `json:"localeResults"`
		AltDisplayResults []struct {
			Metres   string `json:"metres"`
			Imperial bool   `json:"imperial"`
			Output   string `json:"output"`
		} `json:"altDisplayResults"`
		Errors []string `json:"errors"`
	}
	if err := json.Unmarshal([]byte(stdout.String()), &resp); err != nil {
		t.Fatalf("decode node output: %v; raw: %q", err, stdout.String())
	}
	if len(resp.Errors) > 0 {
		t.Fatalf("display runner reported errors: %v", resp.Errors)
	}

	localeOut := make(map[string]bool, len(resp.LocaleResults))
	for _, r := range resp.LocaleResults {
		localeOut[strings.Join(r.Languages, ",")] = r.Imperial
	}
	altOut := make(map[string]string, len(resp.AltDisplayResults))
	for _, r := range resp.AltDisplayResults {
		altOut[altKey(r.Metres, r.Imperial)] = r.Output
	}
	return localeOut, altOut
}

func altKey(metres string, imperial bool) string {
	return fmt.Sprintf("%s|%t", metres, imperial)
}

// TestImperialLengthFromLanguages pins the *-us region rule the altitude
// field uses to pick its display unit, mirroring the server-side
// tempUnitFromAcceptLanguage contract.
func TestImperialLengthFromLanguages(t *testing.T) {
	cases := []localeCase{
		{Languages: []string{"en-US", "en"}, Imperial: true},
		{Languages: []string{"es-US"}, Imperial: true},
		{Languages: []string{"en-GB", "en"}, Imperial: false},
		{Languages: []string{"de-DE"}, Imperial: false},
		{Languages: []string{"en", "en-US"}, Imperial: true},  // bare tag skipped, then -us wins
		{Languages: []string{"en", "de-DE"}, Imperial: false}, // bare skipped, first regional is metric
		{Languages: []string{"en"}, Imperial: false},          // bare only → default metric
		{Languages: []string{"*"}, Imperial: false},           // wildcard ignored → default metric
		{Languages: []string{}, Imperial: false},              // empty → default metric
		{Languages: []string{"fr-FR", "en-US"}, Imperial: false}, // first regional (metric) wins
	}
	gotLocale, _ := runJSDisplay(t, cases, nil)
	for _, c := range cases {
		key := strings.Join(c.Languages, ",")
		if got := gotLocale[key]; got != c.Imperial {
			t.Errorf("imperialLengthFromLanguages(%v) = %t, want %t", c.Languages, got, c.Imperial)
		}
	}
}

// TestAltitudeDisplayValue pins how a canonical bare-metres value renders in
// the altitude field: metric always "<m>m"; imperial shows "<ft>ft" only
// when it round-trips exactly back to the stored metres, otherwise "<m>m".
func TestAltitudeDisplayValue(t *testing.T) {
	cases := []altDisplayCase{
		// Metric viewer: always metres, unit suffixed.
		{Metres: "3.6576", Imperial: false, Output: "3.6576m"},
		{Metres: "20", Imperial: false, Output: "20m"},
		{Metres: "", Imperial: false, Output: ""},
		// Imperial viewer, exact whole-feet conversions → feet.
		{Metres: "3.6576", Imperial: true, Output: "12ft"},   // 12 ft
		{Metres: "19.812", Imperial: true, Output: "65ft"},   // 65 ft
		{Metres: "121.92", Imperial: true, Output: "400ft"},  // 400 ft
		{Metres: "0", Imperial: true, Output: "0ft"},
		// Imperial viewer, value not a whole number of feet → stays metres.
		{Metres: "20", Imperial: true, Output: "20m"},   // 65.6168 ft, not exact
		{Metres: "42.5", Imperial: true, Output: "42.5m"},
		// Empty passthrough regardless of locale.
		{Metres: "", Imperial: true, Output: ""},
	}
	_, gotAlt := runJSDisplay(t, nil, cases)
	for _, c := range cases {
		if got := gotAlt[altKey(c.Metres, c.Imperial)]; got != c.Output {
			t.Errorf("altitudeDisplayValue(%q, %t) = %q, want %q", c.Metres, c.Imperial, got, c.Output)
		}
	}
}
