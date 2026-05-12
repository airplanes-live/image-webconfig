package wifi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	wexec "github.com/airplanes-live/image/webconfig/internal/exec"
)

func intPtr(n int) *int { return &n }

// === splitNMCLIFields ===

func TestSplitNMCLIFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		max  int
		want []string
	}{
		{"plain three fields", "a:b:c", 3, []string{"a", "b", "c"}},
		{"escaped colon in SSID", `*:78:My\:Net`, 3, []string{"*", "78", "My:Net"}},
		{"escaped backslash", `*:78:My\\Net`, 3, []string{"*", "78", `My\Net`}},
		{"trailing field captures rest", "a:b:c:d", 3, []string{"a", "b", "c:d"}}, // SSID with stray colon
		{"empty SSID (hidden)", "*:78:", 3, []string{"*", "78", ""}},
		{"empty IN-USE", ":60:Net", 3, []string{"", "60", "Net"}},
		{"fewer fields than max", "a:b", 3, []string{"a", "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitNMCLIFields(c.in, c.max)
			if len(got) != len(c.want) {
				t.Fatalf("len = %d (%q), want %d (%q)", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("field %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

// === sanitizeSSID ===

func TestSanitizeSSID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"MyHome", "MyHome"},
		{"With Space", "With Space"},
		{"Café", "Café"},                       // UTF-8 multibyte kept verbatim
		{"emoji 🎵", "emoji 🎵"},                 // four-byte UTF-8 kept
		{"foo\x01bar", "foobar"},               // C0 control stripped
		{"foo\x7fbar", "foobar"},               // DEL stripped
		{"\nfoo\tbar\r\n", "foobar"},           // whitespace controls stripped
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeSSID(c.in); got != c.want {
			t.Errorf("sanitizeSSID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// === Probe integration ===

// cannedNMCLI dispatches on the argv tail:
//   - "...dev status"           → statusOut, statusErr
//   - "...dev wifi list ifname *" → listOut, listErr
//   - anything else              → unexpected
type cannedNMCLI struct {
	statusOut string
	statusErr error
	listOut   string
	listErr   error
}

func (c cannedNMCLI) runner() wexec.CommandRunner {
	return func(ctx context.Context, argv []string) (wexec.Result, error) {
		// argv[0] is the binary path; we look at the tail to discriminate.
		joined := strings.Join(argv[1:], " ")
		switch {
		case strings.HasSuffix(joined, "dev status"):
			return wexec.Result{Stdout: []byte(c.statusOut)}, c.statusErr
		case strings.Contains(joined, "dev wifi list ifname"):
			return wexec.Result{Stdout: []byte(c.listOut)}, c.listErr
		}
		return wexec.Result{}, errors.New("unexpected argv: " + joined)
	}
}

func TestProbe_NMCLIErrors_ReturnsNil(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{statusErr: errors.New("nmcli: not found")}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	if got := r.Probe(context.Background()); got != nil {
		t.Errorf("expected nil on nmcli error, got %+v", got)
	}
}

func TestProbe_NoWiFiDevice_ReturnsNil(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "eth0:ethernet:connected\nlo:loopback:unmanaged\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	if got := r.Probe(context.Background()); got != nil {
		t.Errorf("expected nil when no wifi device, got %+v", got)
	}
}

func TestProbe_WiFiPresent_Disconnected(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:disconnected\neth0:ethernet:connected\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got == nil {
		t.Fatal("expected non-nil for wifi-present-but-disconnected")
	}
	if got.Connected || got.Iface != "wlan0" {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestProbe_WiFiPresent_Unavailable(t *testing.T) {
	t.Parallel()
	// rfkill / radio off — nmcli reports state "unavailable", not "connected".
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:unavailable\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got == nil || got.Connected {
		t.Errorf("rfkill should render as Connected:false, got %+v", got)
	}
}

func TestProbe_ConnectedWithSignal(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   "*:78:MyHome\n :60:OtherNet\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got == nil || !got.Connected {
		t.Fatalf("expected connected, got %+v", got)
	}
	if got.SSID != "MyHome" {
		t.Errorf("SSID = %q, want MyHome", got.SSID)
	}
	if got.SignalPct == nil || *got.SignalPct != 78 {
		t.Errorf("SignalPct = %v, want 78", got.SignalPct)
	}
}

func TestProbe_ConnectedNoActiveRow(t *testing.T) {
	t.Parallel()
	// status says connected but list has no IN-USE='*' row — surprising
	// but possible during race. Still render as Connected:true with no
	// signal data.
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   " :60:OtherNet\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got == nil || !got.Connected {
		t.Fatalf("expected connected, got %+v", got)
	}
	if got.SignalPct != nil {
		t.Errorf("SignalPct should be nil, got %v", *got.SignalPct)
	}
	if got.SSID != "" {
		t.Errorf("SSID should be empty when no in-use row, got %q", got.SSID)
	}
}

func TestProbe_EscapedColonInSSID(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   `*:78:My\:Net` + "\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got.SSID != "My:Net" {
		t.Errorf("SSID = %q, want My:Net", got.SSID)
	}
}

func TestProbe_EscapedBackslashInSSID(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   `*:78:My\\Net` + "\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got.SSID != `My\Net` {
		t.Errorf("SSID = %q, want My\\Net", got.SSID)
	}
}

func TestProbe_UTF8SSID(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   "*:78:Café\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got.SSID != "Café" {
		t.Errorf("SSID = %q, want Café (UTF-8 preserved)", got.SSID)
	}
}

func TestProbe_HiddenSSID(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   "*:78:\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got.SSID != "" {
		t.Errorf("SSID = %q, want empty (hidden)", got.SSID)
	}
	if got.SignalPct == nil || *got.SignalPct != 78 {
		t.Errorf("SignalPct = %v, want 78", got.SignalPct)
	}
}

func TestProbe_MalformedSignal_KeepsConnected(t *testing.T) {
	t.Parallel()
	// Codex feedback: an unparseable signal must NOT flip Connected→false.
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   "*:bogus:Net\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if !got.Connected {
		t.Error("Connected should stay true with malformed signal")
	}
	if got.SignalPct != nil {
		t.Errorf("SignalPct should be nil on parse failure, got %v", *got.SignalPct)
	}
	if got.SSID != "Net" {
		t.Errorf("SSID = %q, want Net", got.SSID)
	}
}

func TestProbe_OutOfRangeSignal_DropsValue(t *testing.T) {
	t.Parallel()
	c := cannedNMCLI{
		statusOut: "wlan0:wifi:connected\n",
		listOut:   "*:150:Net\n",
	}
	r := NewSignalReader("/usr/bin/nmcli", c.runner())
	got := r.Probe(context.Background())
	if got.SignalPct != nil {
		t.Errorf("out-of-range signal should be dropped, got %v", *got.SignalPct)
	}
}

func TestProbe_SignalBoundaries(t *testing.T) {
	t.Parallel()
	cases := []int{0, 39, 40, 59, 60, 100}
	for _, pct := range cases {
		c := cannedNMCLI{
			statusOut: "wlan0:wifi:connected\n",
			listOut:   "*:" + itoa(pct) + ":Net\n",
		}
		r := NewSignalReader("/usr/bin/nmcli", c.runner())
		got := r.Probe(context.Background())
		if got == nil || got.SignalPct == nil || *got.SignalPct != pct {
			t.Errorf("signal=%d: got %v", pct, got)
		}
	}
}

func TestProbe_ContextCancelled(t *testing.T) {
	t.Parallel()
	slow := func(ctx context.Context, _ []string) (wexec.Result, error) {
		<-ctx.Done()
		return wexec.Result{}, ctx.Err()
	}
	r := &SignalReader{nmcliBinary: "/usr/bin/nmcli", runner: slow, timeout: 50 * time.Millisecond}
	start := time.Now()
	got := r.Probe(context.Background())
	if got != nil {
		t.Errorf("expected nil on probe timeout, got %+v", got)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("probe should bound at ~50ms, took %v", time.Since(start))
	}
}

// itoa: tiny strconv-free helper so test cases stay one-liners.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
