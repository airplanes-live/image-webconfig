// airplanes-webconfig is the airplanes.live feeder image's web admin UI.
// It binds 127.0.0.1:8080 by default and is reverse-proxied by lighttpd on
// :80 for `/` and `/api/*`. This binary is the PR-1 plumbing skeleton —
// only `/health` and `/` are wired. Auth, config, and write paths land in
// later PRs.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/airplanes-live/image/webconfig/internal/server"
)

// version is overridden via -ldflags "-X main.version=<sha>" during build.
var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on (host:port)")
	flag.Parse()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(version),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14, // 16 KB; webconfig has no reason for larger headers
	}

	go func() {
		log.Printf("airplanes-webconfig %s listening on %s", version, *listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Print("shutting down")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
