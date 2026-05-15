package pihealth

import (
	"bytes"
	"strconv"
	"strings"
)

// parseThrottled extracts the bitmask from vcgencmd get_throttled's
// `throttled=0x50000` output. Returns ok=false on unparseable input.
func parseThrottled(stdout string) (uint32, bool) {
	s := strings.TrimSpace(stdout)
	const prefix = "throttled="
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return 0, false
	}
	value := s[idx+len(prefix):]
	if cut := strings.IndexAny(value, " \t\r\n"); cut >= 0 {
		value = value[:cut]
	}
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		value = value[2:]
	}
	bits, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(bits), true
}

// parseTimedatectlShow scans `timedatectl show` output for the
// NTPSynchronized key. Returns (synced, found). The legacy NTP=yes line is
// intentionally NOT a fallback — it indicates the timesyncd service is
// enabled, not that the clock is synchronized.
func parseTimedatectlShow(stdout string) (synced bool, found bool) {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		const key = "NTPSynchronized="
		if !strings.HasPrefix(line, key) {
			continue
		}
		v := strings.TrimSpace(line[len(key):])
		switch strings.ToLower(v) {
		case "yes", "true", "1":
			return true, true
		case "no", "false", "0":
			return false, true
		}
		return false, true
	}
	return false, false
}

// parseMeminfo reads MemTotal + MemAvailable from /proc/meminfo and
// returns MemAvailable as a percentage of MemTotal. ok=false if either
// field is missing or unparseable.
func parseMeminfo(b []byte) (availPct float64, ok bool) {
	var total, avail int64
	var haveTotal, haveAvail bool
	for _, raw := range bytes.Split(b, []byte("\n")) {
		line := string(bytes.TrimSpace(raw))
		const tk = "MemTotal:"
		const ak = "MemAvailable:"
		switch {
		case strings.HasPrefix(line, tk):
			if v, ok := parseKBValue(line[len(tk):]); ok {
				total, haveTotal = v, true
			}
		case strings.HasPrefix(line, ak):
			if v, ok := parseKBValue(line[len(ak):]); ok {
				avail, haveAvail = v, true
			}
		}
		if haveTotal && haveAvail {
			break
		}
	}
	if !haveTotal || !haveAvail || total <= 0 {
		return 0, false
	}
	return float64(avail) / float64(total) * 100, true
}

// parseKBValue parses `  4000000 kB` into 4000000. The unit suffix and
// surrounding whitespace are tolerated.
func parseKBValue(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if cut := strings.IndexAny(s, " \t"); cut >= 0 {
		s = s[:cut]
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseUptime returns the first whitespace-delimited field of /proc/uptime
// as seconds since boot.
func parseUptime(b []byte) (float64, bool) {
	s := strings.TrimSpace(string(b))
	if cut := strings.IndexAny(s, " \t"); cut >= 0 {
		s = s[:cut]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseSysfsTempMilliC parses /sys/class/thermal/thermal_zone0/temp
// (millidegrees Celsius) into a float Celsius value.
func parseSysfsTempMilliC(b []byte) (float64, bool) {
	s := strings.TrimSpace(string(b))
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return float64(v) / 1000, true
}

// isRaspberryPi inspects the device-tree model contents. The kernel
// publishes a null-terminated string like `Raspberry Pi 4 Model B Rev 1.4\0`.
func isRaspberryPi(modelFileContents []byte) bool {
	trimmed := bytes.TrimRight(modelFileContents, "\x00\n\r\t ")
	return bytes.HasPrefix(trimmed, []byte("Raspberry Pi"))
}

// parsePSUMaxCurrent extracts the milliamp rating from `vcgencmd
// get_config psu_max_current` output. Pi 5 firmware reports the
// USB-PD-negotiated capability (e.g. `psu_max_current=5000`); earlier
// Pis don't expose this key and `vcgencmd` either returns nothing or a
// zero value — both are treated as "not probed" via ok=false.
func parsePSUMaxCurrent(stdout string) (int, bool) {
	s := strings.TrimSpace(stdout)
	const prefix = "psu_max_current="
	idx := strings.Index(s, prefix)
	if idx < 0 {
		return 0, false
	}
	value := s[idx+len(prefix):]
	if cut := strings.IndexAny(value, " \t\r\n"); cut >= 0 {
		value = value[:cut]
	}
	v, err := strconv.Atoi(value)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// expectedPSUMaxCurrentMA returns the manufacturer-recommended PSU
// rating in milliamps for the Pi family detected from the device-tree
// model string. Returns 0 when the family is unknown or the model is
// too old to have a documented expectation.
//
// Values track the official "minimum power supply" guidance:
//   - Pi 5:    5000 mA (for full peripheral budget; 3000 mA boots in
//              USB-current-limited mode)
//   - Pi 4 /
//     CM4:    3000 mA
//   - Pi 3 /
//     Zero 2: 2500 mA
func expectedPSUMaxCurrentMA(modelFileContents []byte) int {
	trimmed := strings.TrimRight(string(modelFileContents), "\x00\n\r\t ")
	switch {
	case strings.HasPrefix(trimmed, "Raspberry Pi 5"),
		strings.HasPrefix(trimmed, "Raspberry Pi Compute Module 5"):
		return 5000
	case strings.HasPrefix(trimmed, "Raspberry Pi 4"),
		strings.HasPrefix(trimmed, "Raspberry Pi Compute Module 4"):
		return 3000
	case strings.HasPrefix(trimmed, "Raspberry Pi 3"),
		strings.HasPrefix(trimmed, "Raspberry Pi Zero 2"):
		return 2500
	}
	return 0
}
