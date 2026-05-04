// airplanes-webconfig is the airplanes.live feeder image's web admin UI.
// Binds 127.0.0.1:8080 by default; lighttpd reverse-proxies / and /api/* on
// :80.
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

	"github.com/airplanes-live/image/webconfig/internal/auth"
	"github.com/airplanes-live/image/webconfig/internal/server"
)

// version is overridden via -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on")
	hashPath := flag.String("password-hash",
		"/etc/airplanes/webconfig/password.hash",
		"path to argon2id PHC password hash")
	sessionTTL := flag.Duration("session-ttl", 24*time.Hour,
		"sliding session TTL")
	argonTime := flag.Uint("argon2-time", uint(auth.DefaultParams.TimeCost),
		"argon2id time cost")
	argonMem := flag.Uint("argon2-memory-kb", uint(auth.DefaultParams.MemoryKB),
		"argon2id memory in KiB")
	argonThreads := flag.Uint("argon2-threads", uint(auth.DefaultParams.Threads),
		"argon2id threads (parallelism)")
	argonConcurrency := flag.Int("argon2-concurrency", 2,
		"max simultaneous argon2id evaluations (>=1)")
	lockoutThreshold := flag.Int("lockout-threshold", 5,
		"failures before login is throttled")
	lockoutWindow := flag.Duration("lockout-window", time.Minute,
		"sliding window for failure counting")
	lockoutDuration := flag.Duration("lockout-duration", 15*time.Minute,
		"duration of an active lockout")
	flag.Parse()

	if *argonThreads > 255 {
		log.Fatalf("argon2-threads must be <= 255 (got %d)", *argonThreads)
	}
	params := auth.Params{
		TimeCost: uint32(*argonTime),
		MemoryKB: uint32(*argonMem),
		Threads:  uint8(*argonThreads),
		KeyLen:   auth.DefaultParams.KeyLen,
		SaltLen:  auth.DefaultParams.SaltLen,
	}
	if err := params.Validate(); err != nil {
		log.Fatalf("argon2 params: %v", err)
	}

	guard, err := auth.NewHashGuard(*argonConcurrency)
	if err != nil {
		log.Fatalf("hash guard: %v", err)
	}
	if *lockoutThreshold < 1 || *lockoutWindow <= 0 || *lockoutDuration <= 0 {
		log.Fatalf("lockout values must be positive")
	}

	srv := &http.Server{
		Addr: *listen,
		Handler: server.New(server.Deps{
			Version:      version,
			Store:        auth.NewPasswordStore(*hashPath),
			Sessions:     auth.NewSessions(*sessionTTL),
			Lockout:      auth.NewLockout(*lockoutThreshold, *lockoutWindow, *lockoutDuration),
			Guard:        guard,
			Argon2Params: params,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14,
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
