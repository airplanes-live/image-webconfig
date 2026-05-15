// pihealth_cmd backs the `airplanes-webconfig --pi-health` invocation
// used by the tty1 console dashboard and SSH MOTD (both via render-status).
// The Go side runs the same probe as the web tile, then projects the
// PiHealth payload into a single ASCII-only line: "<severity>\t<summary>".
//
// ASCII-only because render-status runs under LC_ALL=C and its compact
// truncate helper is byte-oriented — Unicode bullets and degree symbols
// would either render as mojibake or get cut mid-multibyte. The web tile
// reads the same data through /api/status JSON and keeps the rich glyphs.
package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/pihealth"
)

// stripTempSuffixRE matches the " · NN°C" tail that pihealth.classify
// appends to a healthy summary when temperature was probed. The existing
// "System" row in render-status already shows CPU temperature, so the
// Hardware row repeats nothing useful on a green state.
var stripTempSuffixRE = regexp.MustCompile(` · \d+°C$`)

func stripTempSuffix(s string) string {
	return stripTempSuffixRE.ReplaceAllString(s, "")
}

// asciiSafe folds the Unicode characters pihealth.classify emits into
// the ASCII subset render-status can render and truncate safely.
//
//	"·" → "*"   (separator between findings)
//	"°" → ""    ("56°C" → "56C")
func asciiSafe(s string) string {
	s = strings.ReplaceAll(s, "·", "*")
	s = strings.ReplaceAll(s, "°", "")
	return s
}

// formatCLILine projects a *pihealth.PiHealth into the wire format
// render-status consumes: "<severity>\t<summary>". On healthy state with
// temperature available the trailing temp blurb is dropped (the System
// row owns CPU temperature on the dashboard).
func formatCLILine(p *pihealth.PiHealth) string {
	summary := p.Summary
	if p.Severity == "ok" && p.TempProbed {
		summary = stripTempSuffix(summary)
	}
	return p.Severity + "\t" + asciiSafe(summary)
}

// runPiHealthCmd is the body of `--pi-health`. probe is injected so unit
// tests don't need to construct a real *pihealth.Reader. Exit code 0 on
// success regardless of severity — the severity token is encoded in
// stdout. Exit code 1 only when the probe is structurally broken
// (returned nil), so render-status's `if cmd 2>/dev/null` shortcut still
// works for the normal warn/err case.
func runPiHealthCmd(stdout, stderr io.Writer, probe func(context.Context) *pihealth.PiHealth, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result := probe(ctx)
	if result == nil {
		fmt.Fprintln(stderr, "pi-health: probe returned no result")
		return 1
	}
	fmt.Fprintln(stdout, formatCLILine(result))
	return 0
}
