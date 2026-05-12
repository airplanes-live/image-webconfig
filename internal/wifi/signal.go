package wifi

// signal.go probes the *current* WiFi-signal state from NetworkManager via
// `nmcli` reads. This is the read-only counterpart to apl-wifi: writes
// (config changes) still go through the privileged helper, but on-the-wire
// signal-strength reads need no privileges and shouldn't pay the apl-wifi
// fork+JSON cost on every /api/status poll.
//
// Two nmcli calls per probe:
//   1. `nmcli -t -f DEVICE,TYPE,STATE dev status` — find the wifi-type device
//      and learn whether it's connected. If no wifi device exists, the
//      probe returns nil so the dashboard hides the tile entirely.
//   2. `nmcli -t -f IN-USE,SIGNAL,SSID dev wifi list ifname <iface> --rescan no`
//      — only when the wifi device is connected. Parses the IN-USE='*'
//      row and returns its SSID + signal %. `--rescan no` must follow
//      the `dev wifi list` subcommand; nmcli 1.52+ rejects it as a
//      global flag with exit 2.
//
// Note: `nmcli dev wifi list` does NOT accept a DEVICE field, so the older
// "one-call DEVICE,IN-USE,SIGNAL,SSID" shape was unworkable. Hence the
// split.

import (
	"context"
	"strconv"
	"strings"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

// Signal is the JSON shape embedded in /api/status as `wifi`. Connected
// is the only mandatory field; the rest carry the meaningful values when
// the probe succeeded all the way through.
//
// SignalPct is a *int so we can distinguish "unknown" (nil — connection
// is up but we couldn't parse the strength) from a valid 0 (which an
// `int + omitempty` would silently drop from the JSON).
type Signal struct {
	Connected bool   `json:"connected"`
	Iface     string `json:"iface,omitempty"`
	SSID      string `json:"ssid,omitempty"`
	SignalPct *int   `json:"signal_pct,omitempty"`
}

// SignalReader runs the two-step probe via the injectable CommandRunner.
type SignalReader struct {
	nmcliBinary string
	runner      wexec.CommandRunner
	timeout     time.Duration
}

// NewSignalReader returns a Reader whose Probe() will call the given
// nmcli binary path through r. A nil r falls back to wexec.RealRunner.
// timeout=0 selects a 2 s default.
func NewSignalReader(nmcli string, r wexec.CommandRunner) *SignalReader {
	if r == nil {
		r = wexec.RealRunner
	}
	return &SignalReader{
		nmcliBinary: nmcli,
		runner:      r,
		timeout:     2 * time.Second,
	}
}

// Probe returns:
//   - nil when there is no WiFi hardware (or nmcli unavailable / failed).
//     The /api/status payload omits `wifi`; the frontend hides the tile.
//   - &Signal{Connected:false, Iface} when WiFi hardware exists but no
//     network is associated (radio off, no profile, scanning).
//   - &Signal{Connected:true, Iface, SSID, SignalPct} on an active
//     connection. SignalPct is nil when the strength row was missing or
//     unparseable — the interface is still connected; we just don't know.
//
// Never returns an error.
func (r *SignalReader) Probe(ctx context.Context) *Signal {
	iface, connected, ok := r.findWiFiInterface(ctx)
	if !ok {
		return nil
	}
	if !connected {
		return &Signal{Connected: false, Iface: iface}
	}
	out := &Signal{Connected: true, Iface: iface}
	r.populateActiveRow(ctx, iface, out)
	return out
}

// findWiFiInterface runs `nmcli -t -f DEVICE,TYPE,STATE dev status` and
// returns the first wifi-type device. `connected` is true iff its STATE
// is "connected". `ok` is false when the command errored or no wifi
// device exists.
func (r *SignalReader) findWiFiInterface(ctx context.Context) (iface string, connected, ok bool) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.nmcliBinary, "-t", "-f", "DEVICE,TYPE,STATE", "dev", "status"})
	if err != nil {
		return "", false, false
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		fields := splitNMCLIFields(line, 3)
		if len(fields) < 3 {
			continue
		}
		dev, typ, state := fields[0], fields[1], fields[2]
		if typ != "wifi" {
			continue
		}
		return dev, state == "connected", true
	}
	return "", false, false
}

// populateActiveRow runs `nmcli -t -f IN-USE,SIGNAL,SSID dev wifi list
// ifname <iface> --rescan no` and fills SSID + SignalPct on out from the
// row whose IN-USE column is "*". On any failure (exec error, no in-use
// row, malformed columns) out is left as-is (Connected stays true; we
// don't fabricate disconnected).
//
// `--rescan no` MUST follow `dev wifi list` — nmcli 1.52+ rejects it as
// a global flag with "Option '--rescan' is unknown" and exits 2. The
// flag exists to suppress the implicit rescan nmcli would otherwise
// trigger; without it nmcli's `--rescan auto` may stall the call.
func (r *SignalReader) populateActiveRow(ctx context.Context, iface string, out *Signal) {
	cctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	res, err := r.runner(cctx, []string{r.nmcliBinary, "-t", "-f", "IN-USE,SIGNAL,SSID", "dev", "wifi", "list", "ifname", iface, "--rescan", "no"})
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		if line == "" {
			continue
		}
		fields := splitNMCLIFields(line, 3)
		if len(fields) < 3 {
			continue
		}
		inUse, signal, ssid := fields[0], fields[1], fields[2]
		if inUse != "*" {
			continue
		}
		out.SSID = sanitizeSSID(ssid)
		if n, err := strconv.Atoi(strings.TrimSpace(signal)); err == nil && n >= 0 && n <= 100 {
			out.SignalPct = &n
		}
		return
	}
}

// splitNMCLIFields splits an nmcli `--terse` (-t) line on unescaped `:`
// separators, returning up to maxFields fields. nmcli escapes `:` as
// `\:` and `\` as `\\` inside any single field — so the splitter must
// recognise the escape and not split on those colons.
//
// maxFields=3 means we keep the first three fields; if the SSID legally
// contains unescaped colons (it shouldn't, but a misbehaving driver
// could) the rest is kept inside the third field.
func splitNMCLIFields(line string, maxFields int) []string {
	out := make([]string, 0, maxFields)
	var cur strings.Builder
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			cur.WriteByte(c)
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == ':' && len(out) < maxFields-1 {
			out = append(out, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	out = append(out, cur.String())
	return out
}

// sanitizeSSID strips C0 control bytes (0x00–0x1F) and DEL (0x7F).
// Multibyte UTF-8 sequences pass through untouched — the repo's
// `wifi-validators.sh` permits UTF-8 SSIDs and we don't want to display
// less than is stored.
func sanitizeSSID(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
