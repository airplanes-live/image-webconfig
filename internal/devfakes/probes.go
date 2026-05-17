package devfakes

import (
	"context"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/pihealth"
	"github.com/airplanes-live/image-webconfig/internal/wifi"
)

// PiHealthProbe implements status.PiHealthProbe with plausible
// jittered readings drawn from the clock. The simulated Pi is healthy:
// no undervoltage, no throttling, normal-range temp/mem/disk. Severity
// stays "ok" so the dashboard tile never flashes red unless a future
// dev-mode toggle inverts a flag manually.
type PiHealthProbe struct {
	state *State
}

// NewPiHealthProbe binds a probe to a state pointer. The state is
// unused today but kept threaded so a future "force throttling" toggle
// on State can be surfaced without a constructor signature change.
func NewPiHealthProbe(s *State) *PiHealthProbe { return &PiHealthProbe{state: s} }

// Probe returns a fresh PiHealth on every call. Values jitter inside
// safe ranges so the dashboard's 30s poll shows movement, but the
// severity never trips. The jitter source is the wall clock — no
// rand.Source state to manage and no flakiness across runs.
func (p *PiHealthProbe) Probe(_ context.Context) *pihealth.PiHealth {
	tick := time.Now().UnixNano() / int64(time.Millisecond)
	jitter := float64(tick%1000) / 1000.0 // 0.0..1.0
	return &pihealth.PiHealth{
		Severity:        "ok",
		Summary:         "dev simulator — all sub-probes synthetic",
		IsRaspberryPi:   true,
		ThrottleProbed:  true,
		TempProbed:      true,
		TimeProbed:      true,
		MemProbed:       true,
		DiskProbed:      true,
		PSUProbed:       true,
		CPUTempCelsius:  45.0 + 10*jitter,
		NTPSynchronized: true,
		UptimeSeconds:   time.Since(p.state.feedStart).Seconds(),
		MemoryAvailPct:  55.0 + 10*jitter,
		DiskFreePct:     65.0 + 5*jitter,
		PSUMaxCurrentMA: 5000,
		PSUExpectedMA:   5000,
	}
}

// WifiProbe implements status.WifiProbe by deriving the snapshot from
// the active network in State. Returns nil when no network is active,
// which makes the dashboard tile self-hide (matches the production
// "no WiFi hardware" path).
type WifiProbe struct {
	state *State
}

// NewWifiProbe binds a probe to a state pointer.
func NewWifiProbe(s *State) *WifiProbe { return &WifiProbe{state: s} }

// Probe returns a synthetic signal snapshot. The simulated machine
// always has a Wi-Fi radio, so we never return nil — production
// returns nil only on "no hardware". When no profile is active we
// emit {Connected: false, Iface: "wlan0"} so the dashboard tile
// renders the disconnected state instead of hiding altogether.
func (p *WifiProbe) Probe(_ context.Context) *wifi.Signal {
	active, _ := p.state.WifiStatus()
	if active == nil {
		return &wifi.Signal{Connected: false, Iface: "wlan0"}
	}
	tick := time.Now().UnixNano() / int64(time.Millisecond)
	pct := 60 + int(tick%25)
	return &wifi.Signal{
		Connected: true,
		Iface:     active.Device,
		SSID:      active.SSID,
		SignalPct: &pct,
	}
}
