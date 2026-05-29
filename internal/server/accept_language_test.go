package server

import "testing"

func TestTempUnitFromAcceptLanguage(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"empty defaults C", "", "C"},
		{"en-US plain", "en-US", "F"},
		{"en-US with fallback", "en-US,en;q=0.9", "F"},
		{"es-US regional", "es-US", "F"},
		{"uppercase EN-US", "EN-US", "F"},
		{"de-DE", "de-DE,de;q=0.9,en;q=0.8", "C"},
		{"en-GB", "en-GB", "C"},
		{"bare en", "en", "C"},
		{"bare de then regional us wins over bare", "de,en-US", "F"},
		{"first regional on equal q wins", "en-GB,en-US", "C"},
		{"higher q regional wins", "en-GB;q=0.5,en-US;q=0.9", "F"},
		{"q=0 excluded falls through", "en-US;q=0", "C"},
		{"q=0 us excluded, next regional decides", "en-US;q=0,de-DE;q=0.5", "C"},
		{"malformed q treated as default", "en-US;q=abc", "F"},
		{"wildcard skipped", "*", "C"},
		{"wildcard then regional", "*,en-US", "F"},
		{"whitespace tolerated", "  en-US ; q=0.8 , de-DE ; q=0.9 ", "C"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tempUnitFromAcceptLanguage(tc.header); got != tc.want {
				t.Fatalf("tempUnitFromAcceptLanguage(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
