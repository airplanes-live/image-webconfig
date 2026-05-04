// Package web exposes the embedded HTML/CSS/JS asset tree.
package web

import "embed"

//go:embed assets
var FS embed.FS
