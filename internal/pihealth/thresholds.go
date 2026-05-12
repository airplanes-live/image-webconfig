package pihealth

// Thresholds are the warn/err cutoffs for each numeric sub-check. Held on
// pihealth.Reader (not as package globals) so concurrent tests with
// t.Parallel() can use distinct values without racing.
type Thresholds struct {
	// CPU temperature warn/err in Celsius. The Pi soft-throttles around
	// 80°C; warn earlier so operators can react before the firmware does.
	TempWarnC float64
	TempErrC  float64

	// Memory available % of MemTotal. Warn/err cutoffs.
	MemWarnPct float64
	MemErrPct  float64

	// Disk free % on the root mount point.
	DiskWarnPct float64
	DiskErrPct  float64

	// Grace window before NTP-not-synced escalates from warn to err.
	// First-boot Pis usually need ~30s to acquire NTP; 5 min is a
	// comfortable ceiling for legitimate sync delay.
	NTPGraceSeconds float64
}

func DefaultThresholds() Thresholds {
	return Thresholds{
		TempWarnC:       75,
		TempErrC:        80,
		MemWarnPct:      15,
		MemErrPct:       5,
		DiskWarnPct:     15,
		DiskErrPct:      5,
		NTPGraceSeconds: 300,
	}
}
