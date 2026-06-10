// airplanes-webconfig is the airplanes.live feeder image's web admin UI.
// Binds 127.0.0.1:8080 by default; lighttpd reverse-proxies / and /api/* on
// :80.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	"github.com/airplanes-live/image-webconfig/internal/claimstatus"
	wexec "github.com/airplanes-live/image-webconfig/internal/exec"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
	"github.com/airplanes-live/image-webconfig/internal/hardware"
	"github.com/airplanes-live/image-webconfig/internal/identity"
	"github.com/airplanes-live/image-webconfig/internal/logs"
	"github.com/airplanes-live/image-webconfig/internal/schemacache"
	"github.com/airplanes-live/image-webconfig/internal/server"
	"github.com/airplanes-live/image-webconfig/internal/status"
	"github.com/airplanes-live/image-webconfig/internal/wifi"
)

// version and commitSha are stamped by the release pipeline via
// -ldflags "-X main.version=<tag> -X main.commitSha=<sha>". The runtime
// version surfaced on /health and /api/status is composed by resolveVersion:
// for stable releases the tag alone is unique enough (v0.1.2 differs from
// v0.1.3), but the moving "dev-latest" tag is reused across consecutive dev
// builds, so we append a short SHA suffix (semver +build metadata) so
// /health byte-changes between two dev builds carrying the same tag.
var (
	version   = "dev"
	commitSha = ""
)

// resolveVersion returns the runtime version string by composing the package
// vars stamped at link time.
func resolveVersion() string {
	return composeVersion(version, commitSha)
}

// composeVersion is the pure form of resolveVersion: given a tag and a commit
// SHA, return the runtime version string. When commitSha is empty it returns
// the tag as-is (local `go build`); otherwise it appends a 7-char short SHA
// using semver +build metadata. Whitespace in commitSha is trimmed so a stray
// newline in the ldflag never leaks into /health or log output.
func composeVersion(tag, sha string) string {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return tag
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return tag + "+" + sha
}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on")
	hashPath := flag.String("password-hash",
		"/etc/airplanes/webconfig/password.hash",
		"path to argon2id PHC password hash")
	sessionTTL := flag.Duration("session-ttl", 24*time.Hour,
		"sliding session TTL")
	sessionsPath := flag.String("sessions-path", "",
		"file to mirror session-token hashes to so logins survive a service restart (empty = in-memory only; the shipped unit passes a path under /run so a reboot still logs everyone out)")
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
	hardwareMode := flag.Bool("hardware", false,
		"emit a one-line hardware-health summary and exit (for render-status / MOTD)")
	hardwareTimeout := flag.Duration("hardware-timeout", 2*time.Second,
		"probe timeout for --hardware (must be < the shell timeout wrapping the call)")
	validateSudoers := flag.Bool("validate-sudoers", false,
		"verify every DefaultPrivilegedArgv() shape is authorized by /etc/sudoers.d/010_* and exit (exit 0 on parity, 1 on mismatch)")
	flag.Parse()

	if *hardwareMode {
		hw := hardware.NewReader(hardware.DefaultPaths(), hardware.DefaultThresholds(), nil, nil)
		os.Exit(runHardwareCmd(os.Stdout, os.Stderr, hw.Probe, *hardwareTimeout))
	}

	if *validateSudoers {
		if err := server.ValidatePrivilegedArgvParity(
			server.DefaultPrivilegedArgv(),
			server.DefaultSudoersPaths()...,
		); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
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
		// Degraded mode: /api/config returns 503, but /api/orchestrator/*,
		// /api/log/*, /api/reboot, /api/poweroff, and the auth endpoints
		// stay alive so the operator can still recover via the dashboard.
		log.Printf("schema: boot fetch failed (degraded mode, /api/config unavailable): %v", err)
	}
	loadCancel()

	// Account-claim status: read-only, unprivileged `apl-feed claim status
	// --json` behind a single-flight, identity-keyed cache.
	claimStatusProber := claimstatus.Prober{Runner: wexec.RealRunner, Argv: claimstatus.DefaultArgv}
	claimStatusCache := claimstatus.NewCache(claimStatusProber.Probe, nil)

	effectiveVersion := resolveVersion()
	srv := &http.Server{
		Addr: *listen,
		Handler: server.New(server.Deps{
			Version:      effectiveVersion,
			Store:        auth.NewPasswordStore(*hashPath),
			Sessions:     auth.NewPersistentSessions(*sessionTTL, *sessionsPath),
			Lockout:      auth.NewLockout(*lockoutThreshold, *lockoutWindow, *lockoutDuration),
			Guard:        guard,
			Argon2Params: params,
			Identity:     identity.NewReader(identity.DefaultPaths()),
			FeedEnv:      feedenv.New(),
			Status: status.NewReader(effectiveVersion, status.DefaultPaths(), nil,
				status.WithHardware(hardware.NewReader(
					hardware.DefaultPaths(),
					hardware.DefaultThresholds(),
					nil, // RealRunner
					nil, // statfs-backed DiskProber
				)),
				status.WithWifi(wifi.NewSignalReader("/usr/bin/nmcli", nil)),
			),
			Logs:        logs.NewStreamer(nil),
			Schema:      cache,
			ClaimStatus: claimStatusCache,
			Privileged:  priv,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14,
	}

	// SIGHUP refreshes the schema cache. The update orchestrator's
	// transient unit fires this after its feed leg so a new
	// feed-env-keys.sh can take effect without bouncing the whole
	// process (sessions survive a restart when --sessions-path is set,
	// but a reload is still cheaper than a restart).
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
		log.Printf("airplanes-webconfig %s listening on %s", effectiveVersion, *listen)
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
