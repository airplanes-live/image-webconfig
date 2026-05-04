// Package web exposes the embedded HTML/CSS/JS asset tree. The actual SPA
// chrome lands in PR-2 (auth + dashboard); PR-1 ships a placeholder index
// that proves the lighttpd reverse-proxy reaches this binary.
package web

import "embed"

//go:embed assets
var FS embed.FS
