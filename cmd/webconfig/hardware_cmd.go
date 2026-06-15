// hardware_cmd backs the `airplanes-webconfig --hardware` invocation
// used by the tty1 console dashboard and SSH MOTD (both via render-status).
// The Go side runs the same probe as the web tile, then projects the
// aggregated Health into a single ASCII-only line: "<severity>\t<summary>".
//
// ASCII-only because render-status runs under LC_ALL=C and its compact
// truncate helper is byte-oriented — Unicode bullets and degree symbols
// would either render as mojibake or get cut mid-multibyte. The web tile
// reads the same Health through /api/status JSON and keeps the rich glyphs.
package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/hardware"
)

// stripTempSuffixRE matches the " · NN°C" tail that hardware.Summarize
// appends to a healthy summary when temperature was probed. The existing
// "System" row in render-status already shows CPU temperature, so the
// Hardware row repeats nothing useful on a green state.
var stripTempSuffixRE = regexp.MustCompile(` · \d+°C$`)

func stripTempSuffix(s string) string {
	return stripTempSuffixRE.ReplaceAllString(s, "")
}

// asciiSafe folds the Unicode characters hardware.Summarize emits into
// the ASCII subset render-status can render and truncate safely.
//
//	"·" → "*"   (separator between findings)
//	"°" → ""    ("56°C" → "56C")
func asciiSafe(s string) string {
	s = strings.ReplaceAll(s, "·", "*")
	s = strings.ReplaceAll(s, "°", "")
	return s
}

// formatCLILine projects a *hardware.Snapshot into the wire format
// render-status consumes: "<severity>\t<summary>". On healthy state the
// trailing temp blurb is dropped — the System row owns CPU temperature
// on the dashboard, so repeating it on the Hardware row is noise. We
// gate the strip on Severity == "ok" rather than a TempProbed flag
// (Health doesn't carry one), which preserves today's behaviour: a
// warn/err summary keeps any embedded temp finding because that temp
// is the finding, not the trailing healthy marker.
func formatCLILine(snap *hardware.Snapshot) string {
	h := snap.Health
	summary := h.Summary
	if h.Severity == "ok" {
		summary = stripTempSuffix(summary)
	}
	return h.Severity + "\t" + asciiSafe(summary)
}

// runHardwareCmd is the body of `--hardware`. probe is injected so unit
// tests don't need to construct a real *hardware.Reader. Exit code 0 on
// success regardless of severity — the severity token is encoded in
// stdout. Exit code 1 only when the probe is structurally broken
// (returned nil), so render-status's `if cmd 2>/dev/null` shortcut still
// works for the normal warn/err case.
func runHardwareCmd(stdout, stderr io.Writer, probe func(context.Context) *hardware.Snapshot, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	snap := probe(ctx)
	if snap == nil {
		fmt.Fprintln(stderr, "hardware: probe returned no result")
		return 1
	}
	fmt.Fprintln(stdout, formatCLILine(snap))
	return 0
}
