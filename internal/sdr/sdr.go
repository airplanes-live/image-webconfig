// Package sdr enumerates RTL2832U-family USB devices from sysfs so the
// webconfig can offer detected SDR serials in the configuration UI.
//
// Read-only and unprivileged: /sys/bus/usb/devices is world-readable and
// reading attribute files does not disturb a device that a consumer
// (readsb, dump978-fa) currently holds open. Matching is by (idVendor,
// idProduct) against librtlsdr's known-device table — vendor-only matching
// on 0bda would also catch Realtek Wi-Fi dongles and NICs.
package sdr

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DefaultSysfsRoot is where USB devices surface on the feeder.
const DefaultSysfsRoot = "/sys/bus/usb/devices"

// Device is one detected RTL-SDR. BusPath is the sysfs device-directory
// name (e.g. "1-1.2") — stable for a given physical port, useful to tell
// apart devices whose EEPROM serials collide. Duplicate marks serials that
// appear on more than one detected device; pinning by such a serial is
// ambiguous at the librtlsdr level.
type Device struct {
	Serial    string `json:"serial"`
	Product   string `json:"product"`
	BusPath   string `json:"bus_path"`
	Duplicate bool   `json:"duplicate"`
}

// knownIDs is librtlsdr's known_devices table (rtl-sdr by Steve Markgraf
// et al., GPL-2.0) as lowercase "vid:pid". The dominant real-world entries
// are 0bda:2832 / 0bda:2838; the rest are RTL2832U rebrands.
var knownIDs = map[string]struct{}{
	"0bda:2832": {}, // Generic RTL2832U
	"0bda:2838": {}, // Generic RTL2832U OEM
	"0413:6680": {}, // DigitalNow Quad DVB-T PCI-E card
	"0413:6f0f": {}, // Leadtek WinFast DTV Dongle mini D
	"0458:707f": {}, // Genius TVGo DVB-T03 (Ver. B)
	"0ccd:00a9": {}, // Terratec Cinergy T Stick Black (rev 1)
	"0ccd:00b3": {}, // Terratec NOXON DAB/DAB+ (rev 1)
	"0ccd:00b4": {}, // Terratec Deutschlandradio DAB Stick
	"0ccd:00b5": {}, // Terratec NOXON DAB Stick - Radio Energy
	"0ccd:00b7": {}, // Terratec Media Broadcast DAB Stick
	"0ccd:00b8": {}, // Terratec BR DAB Stick
	"0ccd:00b9": {}, // Terratec WDR DAB Stick
	"0ccd:00c0": {}, // Terratec MuellerVerlag DAB Stick
	"0ccd:00c6": {}, // Terratec Fraunhofer DAB Stick
	"0ccd:00d3": {}, // Terratec Cinergy T Stick RC (Rev.3)
	"0ccd:00d7": {}, // Terratec T Stick PLUS
	"0ccd:00e0": {}, // Terratec NOXON DAB/DAB+ (rev 2)
	"1554:5020": {}, // PixelView PV-DT235U(RN)
	"15f4:0131": {}, // Astrometa DVB-T/DVB-T2
	"15f4:0133": {}, // HanfTek DAB+FM+DVB-T
	"185b:0620": {}, // Compro Videomate U620F
	"185b:0650": {}, // Compro Videomate U650F
	"185b:0680": {}, // Compro Videomate U680F
	"1b80:d393": {}, // GIGABYTE GT-U7300
	"1b80:d394": {}, // DIKOM USB-DVBT HD
	"1b80:d395": {}, // Peak 102569AGPK
	"1b80:d397": {}, // KWorld KW-UB450-T USB DVB-T Pico TV
	"1b80:d398": {}, // Zaapa ZT-MINDVBZP
	"1b80:d39d": {}, // SVEON STV20 DVB-T USB & FM
	"1b80:d3a4": {}, // Twintech UT-40
	"1b80:d3a8": {}, // ASUS U3100MINI_PLUS_V2
	"1b80:d3af": {}, // SVEON STV27 DVB-T USB & FM
	"1b80:d3b0": {}, // SVEON STV21 DVB-T USB & FM
	"1d19:1101": {}, // Dexatek DK DVB-T (Logilink VG0002A)
	"1d19:1102": {}, // Dexatek DK DVB-T (MSI DigiVox mini II V3.0)
	"1d19:1103": {}, // Dexatek DK 5217 DVB-T
	"1d19:1104": {}, // MSI DigiVox Micro HD
	"1f4d:a803": {}, // Sweex DVB-T USB
	"1f4d:b803": {}, // GTek T803
	"1f4d:c803": {}, // Lifeview LV5TDeluxe
	"1f4d:d286": {}, // MyGica TD312
	"1f4d:d803": {}, // PROlectrix DV107669
}

// List enumerates detected RTL-SDR devices under root. A missing root,
// unreadable entries, or no matching hardware all yield an empty list —
// to the UI those cases are operationally identical. Devices with an
// empty or missing serial are skipped so they cannot collide with the
// empty "no pin" config value. The result is sorted by BusPath so
// repeated calls are stable for a given physical topology.
func List(root string) []Device {
	out := []Device{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		// Interface entries (e.g. "1-1:1.0") and hubs without the
		// attribute files fall out naturally: readAttr returns "".
		vid := readAttr(dir, "idVendor")
		pid := readAttr(dir, "idProduct")
		if vid == "" || pid == "" {
			continue
		}
		if _, ok := knownIDs[strings.ToLower(vid)+":"+strings.ToLower(pid)]; !ok {
			continue
		}
		serial := readAttr(dir, "serial")
		if serial == "" {
			continue
		}
		out = append(out, Device{
			Serial:  serial,
			Product: readAttr(dir, "product"),
			BusPath: e.Name(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BusPath < out[j].BusPath })
	counts := make(map[string]int, len(out))
	for _, d := range out {
		counts[d.Serial]++
	}
	for i := range out {
		out[i].Duplicate = counts[out[i].Serial] > 1
	}
	return out
}

// readAttr strips only the sysfs attribute's trailing newline — NOT all
// whitespace. A serial with embedded or edge whitespace must surface
// verbatim so the UI can flag it as unsupported, rather than offering a
// trimmed variant that no longer matches the device's actual EEPROM value.
func readAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\r\n")
}
