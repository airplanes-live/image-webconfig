// airplanes-webconfig devserver: launches the SPA + JSON API against
// in-memory fakes for every system touchpoint (apl-feed, apl-wifi,
// systemctl, journalctl, /run/airplanes-* state files, Pi-health,
// Wi-Fi signal). Lets a developer iterate on the UI on any Linux or
// macOS box without flashing a Pi.
//
// State lives in a per-process state directory (auto-created in
// /tmp by default, removed on shutdown) and is reset on every
// restart. The binary refuses to start if a user-provided state
// directory is non-empty so a stray `--state-dir ~/projects/...`
// can never overwrite live data.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/airplanes-live/image-webconfig/internal/auth"
	"github.com/airplanes-live/image-webconfig/internal/claimstatus"
	"github.com/airplanes-live/image-webconfig/internal/devfakes"
	"github.com/airplanes-live/image-webconfig/internal/feedenv"
	"github.com/airplanes-live/image-webconfig/internal/identity"
	"github.com/airplanes-live/image-webconfig/internal/logs"
	"github.com/airplanes-live/image-webconfig/internal/schemacache"
	"github.com/airplanes-live/image-webconfig/internal/server"
	"github.com/airplanes-live/image-webconfig/internal/status"
)

// fastArgon2 keeps the setup/login round-trip imperceptible on a dev
// machine. Mirrors the parameters internal/server/server_test.go
// uses for the same reason; production cmd/webconfig keeps the full
// cost.
var fastArgon2 = auth.Params{TimeCost: 1, MemoryKB: 8, Threads: 1, KeyLen: 32, SaltLen: 16}

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on")
	assetsDir := flag.String("assets-dir", "",
		"directory containing index.html + app.js + style.css to serve at GET / and /static/* (default: autodetect ./web/assets, else use the embedded copy)")
	stateDir := flag.String("state-dir", "",
		"directory used to back the in-memory simulator's on-disk projection (default: a fresh /tmp dir, removed on shutdown). When set explicitly, the directory must be empty and is NOT removed on shutdown.")
	flag.Parse()

	resolvedState, ownState, err := resolveStateDir(*stateDir)
	if err != nil {
		log.Fatalf("state-dir: %v", err)
	}
	defer func() {
		if ownState {
			_ = os.RemoveAll(resolvedState)
		}
	}()

	state := devfakes.NewState(devfakes.DefaultPaths(resolvedState))
	if err := state.SyncAll(); err != nil {
		log.Fatalf("seed state: %v", err)
	}

	assetsFS, assetsLabel, err := resolveAssetsFS(*assetsDir)
	if err != nil {
		log.Fatalf("assets-dir: %v", err)
	}

	guard, err := auth.NewHashGuard(2)
	if err != nil {
		log.Fatalf("hash guard: %v", err)
	}

	priv := devfakes.StubPrivilegedArgv()

	// Schema is prepopulated — the fake apl-feed schema endpoint is
	// implemented but the server never reaches for it after the cache
	// is loaded, so we skip the indirection.
	writable := []string{
		"LATITUDE", "LONGITUDE", "ALTITUDE", "GEO_CONFIGURED",
		"MLAT_USER", "MLAT_ENABLED", "MLAT_PRIVATE",
		"REPORT_STATUS", "REMOTE_CONFIG_ENABLED",
		"GAIN", "UAT_INPUT", "DUMP978_SDR_SERIAL", "DUMP978_GAIN",
	}
	readable := append(append([]string{}, writable...), "INPUT", "INPUT_TYPE")
	schema := schemacache.NewPrepopulated(writable, readable)

	statusPaths := status.Paths{
		ManifestFile:       state.Paths.Manifest,
		AircraftJSONFile:   state.Paths.AircraftJSON,
		MlatStateFile:      state.Paths.MlatState,
		FeedStateFile:      state.Paths.FeedState,
		UAT978StateFile:    state.Paths.UAT978State,
		Dump978FAStateFile: state.Paths.Dump978FAState,
		RebootRequiredFile: filepath.Join(resolvedState, "reboot-required"),
		SystemctlBinary:    "/usr/bin/systemctl",
		IsActiveTimeout:    2 * time.Second,
	}
	idPaths := identity.Paths{
		FeederIDFile:    state.Paths.FeederID,
		ClaimSecretFile: state.Paths.ClaimSecret,
		ClaimPageURL:    "https://airplanes.live/feeder/claim",
	}

	statusReader := status.NewReader("dev", statusPaths, devfakes.Runner(state, priv),
		status.WithHardware(devfakes.NewHardwareProbe(state)),
		status.WithWifi(devfakes.NewWifiProbe(state)),
	)

	// Claim-status probe routed through the same fake runner; the dev-stub
	// argv lands in devfakes.claimStatusFeed, which reports a verdict
	// coherent with the simulated identity (unregistered → unclaimed once
	// the Register button materialises a secret).
	claimStatusProber := claimstatus.Prober{
		Runner: devfakes.Runner(state, priv),
		Argv:   []string{"dev-stub", "apl-feed", "claim", "status", "--json"},
	}
	claimStatusCache := claimstatus.NewCache(claimStatusProber.Probe, nil)

	handler := server.New(server.Deps{
		Version:          "dev",
		Store:            auth.NewPasswordStore(state.Paths.PasswordHash),
		Sessions:         auth.NewSessions(24 * time.Hour),
		Lockout:          auth.NewLockout(5, time.Minute, 15*time.Minute),
		Guard:            guard,
		Argon2Params:     fastArgon2,
		Identity:         identity.NewReader(idPaths),
		FeedEnv:          &feedenv.Reader{Path: state.Paths.FeedEnv},
		Status:           statusReader,
		ClaimStatus:      claimStatusCache,
		Logs:             logs.NewStreamer(devfakes.StreamRunner(state)),
		Schema:           schema,
		Runner:           devfakes.Runner(state, priv),
		StdinRunner:      devfakes.StdinRunner(state, priv),
		Privileged:       priv,
		UpgradeStatePath: state.Paths.UpgradeState,
		AssetsFS:         assetsFS,
	})

	srv := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		// No WriteTimeout — log SSE streams hold the response open for
		// up to logs.streamMaxLifetime; the per-write deadline in the
		// SSE handler is the real budget.
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 14,
	}

	go func() {
		log.Printf("airplanes-webconfig devserver listening on %s", *listen)
		log.Printf("  assets:    %s", assetsLabel)
		log.Printf("  state dir: %s%s", resolvedState, statePersistenceLabel(ownState))
		log.Print("  first visit triggers password setup (≥12 chars)")
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

// resolveStateDir returns the directory the simulator should project
// state into and whether the caller owns it (i.e. should rm -rf it on
// shutdown). When the user passes an explicit path, the directory must
// not exist or must be empty — otherwise the binary refuses to start
// rather than risk overwriting real data.
func resolveStateDir(flagVal string) (path string, owned bool, err error) {
	if flagVal == "" {
		dir, err := os.MkdirTemp("", "apl-devserver-state-*")
		if err != nil {
			return "", false, err
		}
		return dir, true, nil
	}
	abs, err := filepath.Abs(flagVal)
	if err != nil {
		return "", false, err
	}
	info, statErr := os.Stat(abs)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return "", false, err
		}
	case statErr != nil:
		return "", false, statErr
	case !info.IsDir():
		return "", false, fmt.Errorf("%s exists and is not a directory", abs)
	default:
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", false, err
		}
		if len(entries) > 0 {
			return "", false, fmt.Errorf("%s is not empty; refuse to overwrite. Use a fresh path or remove --state-dir", abs)
		}
	}
	return abs, false, nil
}

// resolveAssetsFS returns the fs.FS to hand to server.Deps.AssetsFS,
// the human-readable label printed at startup, and any resolution
// error. nil FS → use the embedded copy compiled into the binary.
func resolveAssetsFS(flagVal string) (fs.FS, string, error) {
	candidate := flagVal
	if candidate == "" {
		if _, err := os.Stat("web/assets/index.html"); err == nil {
			candidate = "web/assets"
		}
	}
	if candidate == "" {
		return nil, "embedded (rebuild needed for UI edits)", nil
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Stat(filepath.Join(abs, "index.html"))
	if err != nil {
		return nil, "", fmt.Errorf("assets dir %s: index.html not found (%w)", abs, err)
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("assets dir %s: index.html is a directory", abs)
	}
	return os.DirFS(abs), abs + " (live reload — edit + browser refresh)", nil
}

func statePersistenceLabel(owned bool) string {
	if owned {
		return " (auto-cleaned on shutdown)"
	}
	return " (preserved across runs)"
}
