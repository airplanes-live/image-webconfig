package hardware

import "testing"

func f64(v float64) *float64 { return &v }

func TestLocalizeTempUnit(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		rawC    *float64
		unit    string
		want    string
	}{
		{"healthy F", "healthy · 56°C", f64(56), "F", "healthy · 133°F"},
		{"healthy C unchanged", "healthy · 56°C", f64(56), "C", "healthy · 56°C"},
		{"nil raw unchanged", "healthy · 56°C", nil, "F", "healthy · 56°C"},
		{"unknown unit unchanged", "healthy · 56°C", f64(56), "", "healthy · 56°C"},
		{"converts from raw not token", "healthy · 58°C", f64(58.5), "F", "healthy · 137°F"},
		{"negative temp", "healthy · -5°C", f64(-5), "F", "healthy · 23°F"},
		{"warn blurb mid-string", "78°C · mem 5% free", f64(78), "F", "172°F · mem 5% free"},
		{"generic linux prefix", "generic Linux · healthy · 56°C", f64(56), "F", "generic Linux · healthy · 133°F"},
		{"decimal token captured whole", "healthy · 56.5°C", f64(56.5), "F", "healthy · 134°F"},
		{"no temp token unchanged", "throttling now · mem 5% free", f64(50), "F", "throttling now · mem 5% free"},
		{"two tokens fail closed", "56°C · 57°C", f64(56), "F", "56°C · 57°C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := LocalizeTempUnit(tc.summary, tc.rawC, tc.unit)
			if got != tc.want {
				t.Fatalf("LocalizeTempUnit(%q, %v, %q) = %q, want %q", tc.summary, tc.rawC, tc.unit, got, tc.want)
			}
		})
	}
}
