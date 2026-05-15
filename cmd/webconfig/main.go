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

	"github.com/airplanes-live/image-webconfig/internal/auth"
	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
	"github.com/airplanes-live/image-webconfig/internal/identity"
	"github.com/airplanes-live/image-webconfig/internal/logs"
	"github.com/airplanes-live/image-webconfig/internal/pihealth"
	"github.com/airplanes-live/image-webconfig/internal/schemacache"
	"github.com/airplanes-live/image-webconfig/internal/server"
	"github.com/airplanes-live/image-webconfig/internal/status"
	"github.com/airplanes-live/image-webconfig/internal/wifi"
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
	piHealthMode := flag.Bool("pi-health", false,
		"emit a one-line pi-health summary and exit (for render-status / MOTD)")
	piHealthTimeout := flag.Duration("pi-health-timeout", 2*time.Second,
		"probe timeout for --pi-health (must be < the shell timeout wrapping the call)")
	flag.Parse()

	if *piHealthMode {
		ph := pihealth.NewReader(pihealth.DefaultPaths(), pihealth.DefaultThresholds(), nil, nil)
		os.Exit(runPiHealthCmd(os.Stdout, os.Stderr, ph.Probe, *piHealthTimeout))
	}

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

	priv := server.DefaultPrivilegedArgv()
	cache := schemacache.New(priv.SchemaFeed, wexec.RealRunner)
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := cache.Load(loadCtx); err != nil {
		// Degraded mode: /api/config returns 503, but /api/update,
		// /api/log/*, /api/reboot, /api/poweroff, and the auth endpoints
		// stay alive so the operator can still recover via the dashboard.
		log.Printf("schema: boot fetch failed (degraded mode, /api/config unavailable): %v", err)
	}
	loadCancel()

	srv := &http.Server{
		Addr: *listen,
		Handler: server.New(server.Deps{
			Version:      version,
			Store:        auth.NewPasswordStore(*hashPath),
			Sessions:     auth.NewSessions(*sessionTTL),
			Lockout:      auth.NewLockout(*lockoutThreshold, *lockoutWindow, *lockoutDuration),
			Guard:        guard,
			Argon2Params: params,
			Identity:     identity.NewReader(identity.DefaultPaths()),
			FeedEnv:      feedenv.New(),
			Status: status.NewReader(version, status.DefaultPaths(), nil,
				status.WithPiHealth(pihealth.NewReader(
					pihealth.DefaultPaths(),
					pihealth.DefaultThresholds(),
					nil, // RealRunner
					nil, // statfs-backed DiskProber
				)),
				status.WithWifi(wifi.NewSignalReader("/usr/bin/nmcli", nil)),
			),
			Logs:       logs.NewStreamer(nil),
			Schema:     cache,
			Privileged: priv,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14,
	}

	// SIGHUP refreshes the schema cache. The airplanes-update transient
	// unit fires this after a feed update so a new feed-env-keys.sh
	// can take effect without bouncing the webconfig session table
	// (sessions are in-memory; a restart logs every operator out).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			log.Print("schema: SIGHUP — reloading from apl-feed schema --json")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := cache.Load(ctx); err != nil {
				log.Printf("schema: SIGHUP reload failed (keeping previous cache): %v", err)
			} else {
				log.Print("schema: SIGHUP reload OK")
			}
			cancel()
		}
	}()

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
