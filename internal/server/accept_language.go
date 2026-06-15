package server

import (
	"sort"
	"strconv"
	"strings"
)

// tempUnitFromAcceptLanguage resolves the CPU-temperature display unit
// ("F" or "C") from a browser's Accept-Language header, mirroring the
// airplanes.live website's locale_units.display_temperature_unit: the
// region tag is the signal, not the language. Any "*-us" regional tag
// (en-US, es-US, …) selects Fahrenheit; the first other regional tag
// (en-GB, de-DE, …) selects Celsius; bare language tags ("en", "de")
// are not a strong enough signal and are skipped. Defaults to "C" and
// never panics — a malformed header just falls through to Celsius.
//
// Tags are considered highest-q first (stable for equal q, so earlier
// header position wins ties). Entries with q<=0 ("not acceptable" per
// RFC 9110) are ignored.
func tempUnitFromAcceptLanguage(header string) string {
	type weighted struct {
		tag string
		q   float64
		pos int
	}
	var tags []weighted
	for i, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag := part
		q := 1.0
		if semi := strings.IndexByte(part, ';'); semi >= 0 {
			tag = strings.TrimSpace(part[:semi])
			for _, param := range strings.Split(part[semi+1:], ";") {
				param = strings.TrimSpace(param)
				if v, ok := strings.CutPrefix(param, "q="); ok {
					if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
						q = parsed
					}
				}
			}
		}
		if tag == "" || q <= 0 {
			continue
		}
		tags = append(tags, weighted{tag: tag, q: q, pos: i})
	}
	sort.SliceStable(tags, func(a, b int) bool { return tags[a].q > tags[b].q })

	for _, t := range tags {
		lower := strings.ToLower(t.tag)
		if lower == "*" {
			continue
		}
		if strings.HasSuffix(lower, "-us") {
			return "F"
		}
		if strings.Contains(lower, "-") {
			return "C"
		}
		// Bare language tag — keep scanning for a regional signal.
	}
	return "C"
}
