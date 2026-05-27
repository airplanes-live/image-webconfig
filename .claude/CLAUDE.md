# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Source for the `airplanes-webconfig` Go service and its on-device deployment artifacts. The webconfig serves the feeder's local web UI (lighttpd reverse-proxies `:80` → loopback `:8080`). It used to live under `airplanes-live/image` at `webconfig/`; it was extracted so the binary and UI ship as a versioned release that the runtime overlay installs and updates without reflashing the SD card.

`install.sh --build-mode` is the only install path: pi-gen `stage-airplanes/05-install-webconfig` in `airplanes-live/image` clones this repo at a config-pinned ref and runs it. `ARCH` and `ROOTFS_DIR` are set by pi-gen. The script downloads the matching GitHub Release, verifies SHA256, cross-checks `manifest.json.commit_sha` against the cloned source HEAD, and lays binary + `rootfs.tar.gz` payload into `ROOTFS_DIR`.

On-device updates are delivered through the runtime overlay (`airplanes-live/image`); webconfig has no in-product self-update path.

## Build

```
go build ./cmd/webconfig                 # native
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/webconfig   # cross
```

The release pipeline produces five assets per tag: `airplanes-webconfig-arm64`, `airplanes-webconfig-armhf`, `rootfs.tar.gz`, `manifest.json`, `SHA256SUMS`. The rootfs tarball ships everything under `files/`.

## Test & lint

CI (`.github/workflows/ci.yml`) runs on push to `main`/`dev` and on PRs:

| Job | What it does |
|---|---|
| `test` | `go vet ./...`, `go mod verify`, `go test ./...` |
| `cross-build` | Cross-compile matrix (arm64 + armhf), confirm the produced binary is the right arch |
| `shell-lint` | shellcheck (`-x`) + `bash -n` across `install.sh`, the lib, and the shipped helper scripts |
| `visudo` | `visudo -cf` over `files/etc/sudoers.d/*` (asserts the sudoers files parse) |
| `bats` | `bats test/bats/` — channel-resolver, arch-detect, manifest-sha cross-check, mode flags |

Release workflow (`.github/workflows/release.yml`): tag push or push-to-dev triggers a build matrix, assembles `rootfs.tar.gz` + `manifest.json` + `SHA256SUMS`, and publishes. Dev pushes force-move `dev-latest`.

## Architecture

### Tree

```
cmd/webconfig/                  binary entrypoint (--listen, --password-hash, --pi-health)
internal/
  server/                       HTTP mux, route registration, PrivilegedArgv contract
  feedenv/                      feed.env reader, key allowlist
  configspec/                   the writable-keys whitelist
  auth/                         argon2id PHC password store, session mgmt
  identity/                     feeder claim secret reveal
  wifi/                         signal-strength probing
  logs/                         journalctl streaming via SSE
  status/                       systemd unit + app tile status
  pihealth/                     CPU, memory, disk, temperature thresholds
  runtimestate/                 daemon state-file reader (/run/<service>/state)
  schemacache/                  feed schema cache
  exec/                         command runner abstraction
web/
  assets/                       SPA shell, app.js, style.css, icon.svg (embedded via web/embed.go)
files/                          rootfs payload installed by install.sh (tarred at release time)
  etc/sudoers.d/                010 (base privilege)
  etc/systemd/system/           airplanes-webconfig.service + reset oneshot
  usr/local/lib/airplanes-webconfig/  reset, identity-export.sh, identity-import.sh
  usr/local/lib/airplanes/      wifi-validators.sh, wifi-keyfile.sh (sourced by apl-wifi + airplanes-first-run)
  usr/local/bin/apl-wifi        privileged Wi-Fi management helper
install.sh                      build-mode entrypoint (image build only)
scripts/lib/install-common.sh   shared resolution + download + verify helpers
```

### Sudoers / argv parity invariant

`internal/server/server.go`'s `DefaultPrivilegedArgv()` hard-codes the `/usr/bin/sudo -n …` argv shapes that the binary uses. Each shape must appear verbatim in `files/etc/sudoers.d/010_airplanes-webconfig`. The Go test `TestDefaultPrivilegedArgv_SudoersParity` enforces this from the Go side; the `visudo` CI job enforces parseability from the policy side. Both must travel together in the release — that's why sudoers ships in the rootfs payload rather than being owned by `airplanes-live/image`.

### Release manifest

`manifest.json` records `version`, `kind` (`stable` or `dev`), `commit_sha`, `build_date`, `arches`. Build-mode hard-fails if `commit_sha` doesn't match the cloned source HEAD (prevents a baked image whose binary doesn't match the rootfs payload), and writes the manifest to `/etc/airplanes/webconfig-release.json` so `/health` can report which release is installed.

### Channel resolution

Build-mode reads `AIRPLANES_WEBCONFIG_BRANCH` from the pi-gen config (a concrete tag for stable, a branch name for dev) and resolves: a concrete `v[MAJOR].[MINOR].[PATCH]` tag passes through; `dev` → the moving `dev-latest` tag via `git ls-remote --tags --refs`. Build-mode does **not** read `/etc/airplanes/release-channel` — it's written by `stage 06`, after `stage 05` runs.

## Cross-repo coupling

- **`airplanes-live/image`** — pi-gen consumer. `stage-airplanes/05-install-webconfig/00-run.sh` clones this repo and invokes `install.sh --build-mode`. The image bakes a frozen release; on-device updates replace it.
- **`airplanes-live/feed`** — the webconfig writes feed.env via `sudo -n /usr/local/bin/apl-feed apply --json --lock-timeout 5`. That argv is pinned in `files/etc/sudoers.d/010_airplanes-webconfig` and must stay in sync with feed's `apl-feed` CLI. Validator parity between `web/assets/app.js` (JS validators, inside the `@validator-parity` block) and feed's `scripts/lib/configure-validators.sh` (bash) is enforced by `internal/clientvalidators/parity_test.go`, which executes the actual shipped JS in a Node subprocess and the actual shipped bash functions in a subprocess against a shared input-vector table. Wi-Fi validator parity against `files/usr/local/lib/airplanes/wifi-validators.sh` is enforced by the same test.
- **`/etc/airplanes/release-channel`** — same file feed reads. One device-wide channel knob.

## Rules

- `rules/architecture.md` — the release-payload contract, sudoers/argv parity invariant, install-mode contract, manifest cross-check rationale. Read before changing `install.sh`, the lib, or `DefaultPrivilegedArgv`.
- `rules/commit-guidelines.md` — conventional commits.
- `rules/pr-guidelines.md` — public-repo PR style.

## Local dev

```
go test ./...
bats test/bats/
shellcheck install.sh scripts/lib/install-common.sh files/usr/local/lib/airplanes-webconfig/*.sh files/usr/local/lib/airplanes/*.sh files/usr/local/bin/apl-wifi
```

To exercise `install.sh --build-mode` locally against a synthetic rootfs, point `ROOTFS_DIR` at a writable temp dir and set `AIRPLANES_WEBCONFIG_BRANCH` to a concrete tag that has a published release. The script will try to reach GitHub; offline dev is not supported.

### Local UI dev

`cmd/devserver` wires the SPA + JSON API against in-memory fakes for every system touchpoint (apl-feed, apl-wifi, systemctl, journalctl, /run/airplanes-* state, Pi-health, Wi-Fi). No Pi required.

```
go run ./cmd/devserver
# → http://127.0.0.1:8080 — first visit triggers password setup (≥12 chars)
```

Defaults serve `web/assets/` from disk when run from the repo root, so edits to `index.html` / `app.js` / `style.css` show up on the next browser refresh without rebuilding. Mutations (config save, Wi-Fi add/delete/activate, claim register) round-trip against backing temp files so the production `feedenv.Reader` / `status.Reader` / `identity.Reader` see a coherent view. State resets on every binary restart. The production binary `cmd/webconfig` is untouched — `internal/devfakes` is only imported by `cmd/devserver`.
