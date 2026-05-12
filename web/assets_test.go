package web

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestAssetsHaveNoExternalFetch asserts every shipped asset references
// only same-origin or data: resources. The webconfig is reached during
// AP fallback when the device has no internet; an inadvertent CDN
// reference would manifest as a hung or broken page on flashing day.
//
// The CSP header (default-src 'self'; script-src 'self'; style-src 'self';
// img-src 'self' data:) enforces this at runtime, but a build-time
// check keeps surprise references out of the repo before a PR ever
// reaches a test image.
//
// Scope: fetch-bearing surfaces only.
//   - HTML: src=, href=
//   - CSS:  url(...), @import
//   - JS:   no scan; new fetches go through getJSON/postJSON which use
//     same-origin paths by construction.
//
// XML namespace URIs (xmlns="http://www.w3.org/2000/svg" etc.) are
// explicitly allowed — the browser does not fetch them.
func TestAssetsHaveNoExternalFetch(t *testing.T) {
	t.Parallel()

	htmlAttr := regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*"([^"]+)"`)
	cssURL := regexp.MustCompile(`url\(\s*["']?([^"')]+)`)
	cssImport := regexp.MustCompile(`@import\s+(?:url\()?["']?([^"')\s;]+)`)

	err := fs.WalkDir(FS, "assets", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(FS, p)
		if err != nil {
			return err
		}
		text := string(body)
		ext := strings.ToLower(path.Ext(p))

		var matches [][]string
		switch ext {
		case ".html", ".htm":
			matches = append(matches, htmlAttr.FindAllStringSubmatch(text, -1)...)
		case ".svg":
			// SVG src= / href= would actually fetch — check those, but
			// xmlns attributes are skipped because the regex requires
			// the bare attribute names src or href.
			matches = append(matches, htmlAttr.FindAllStringSubmatch(text, -1)...)
		case ".css":
			matches = append(matches, cssURL.FindAllStringSubmatch(text, -1)...)
			matches = append(matches, cssImport.FindAllStringSubmatch(text, -1)...)
		default:
			return nil
		}

		for _, m := range matches {
			ref := strings.TrimSpace(m[1])
			if ref == "" || strings.HasPrefix(ref, "#") {
				continue
			}
			if strings.HasPrefix(ref, "data:") {
				continue
			}
			if strings.HasPrefix(ref, "/") {
				continue // root-relative same-origin
			}
			if !strings.Contains(ref, "://") {
				continue // bare relative path
			}
			t.Errorf("%s: external fetch reference %q (only /, data:, or bare relative paths allowed)", p, ref)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
