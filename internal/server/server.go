// Package server wires the airplanes-webconfig HTTP routes. PR-1 only
// implements the plumbing-test endpoints: GET / (embedded UI placeholder)
// and GET /health (200 ok). Auth, config, status, and write paths land in
// later PRs.
package server

import (
	"io/fs"
	"net/http"

	webassets "github.com/airplanes-live/image/webconfig/web"
)

// New returns the top-level HTTP handler. version is embedded at build time
// and surfaced via /health so plumbing tests can confirm which build is
// running behind the lighttpd reverse-proxy.
func New(version string) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("ok " + version + "\n"))
	})

	assets, err := fs.Sub(webassets.FS, "assets")
	if err != nil {
		// Compile-time guarantee — embed.FS always has the declared subdir.
		panic(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(assets)))

	return mux
}
