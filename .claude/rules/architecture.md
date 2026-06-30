# Architecture rules

Patterns that look like incidental code style but are actually invariants. Violating them ships a broken release, breaks an upgrade path on already-flashed feeders, opens a security boundary, or makes `/health` lie about which version is running.

## Release payload contract

Every published release carries exactly five assets:

- `airplanes-webconfig-arm64`
- `airplanes-webconfig-armhf`
- `rootfs.tar.gz` — `files/` tarred with deterministic ownership (`--owner=0 --group=0 --numeric-owner`), sorted (`--sort=name`), pinned mtime
- `manifest.json` — `{version, kind, commit_sha, build_date, arches}`
- `SHA256SUMS` — covers all four of the above

The set is a contract. Build-mode install (`install.sh --build-mode`) and the runtime overlay both expect all five and fail loudly if any is missing or its SHA256 doesn't match.

**Don't ship a binary without a matching rootfs.tar.gz.** The systemd unit, sudoers, and helper scripts must version with the binary (see "sudoers/argv parity" below). Shipping only a new ELF leaves the policy stale.

## Sudoers / argv parity

`internal/server/server.go`'s `DefaultPrivilegedArgv()` hard-codes every `/usr/bin/sudo -n …` argv shape the binary invokes. Each shape must appear byte-for-byte in `files/etc/sudoers.d/010_airplanes-webconfig`.

The Go test `TestDefaultPrivilegedArgv_SudoersParity` enforces this from the binary side; the `visudo` CI job verifies the sudoers file parses. Both ship in the same release tarball, so a new argv requires a paired sudoers change in the same PR.

**Don't**:

- Don't let `010` live in `airplanes-live/image` while the argv lives here. Parity has to be enforceable in one repo.
- Don't add a new argv shape without adding a sudoers line for it AND a parity-test case.
- Don't broaden a sudoers entry to a wildcard (e.g. `apl-feed apply *`) to "fix" a parity failure. Pin the exact argv.

## Install-mode contract

`install.sh` has a single mode: `--build-mode`. It runs from a cloned source tree at pi-gen's build host. `ROOTFS_DIR` is the pi-gen staging rootfs; `ARCH` is set to `arm64` or `armhf`; `AIRPLANES_WEBCONFIG_BRANCH` is the concrete tag (stable) or branch (dev) the image config pinned. It downloads the matching release, extracts the rootfs payload, installs the binary, and writes the manifest into `ROOTFS_DIR`. It does no `systemctl` and no user creation — those happen in the chroot step. It cross-checks `manifest.json.commit_sha` against `git rev-parse HEAD` of the cloned tree and hard-fails on mismatch.

On-device updates do not run `install.sh`. Webconfig is delivered and updated through the runtime overlay (`airplanes-live/image`); there is no in-product self-update path.

**Don't**:

- Don't read `/etc/airplanes/release-channel` from `install.sh`. In build mode that file isn't there yet (pi-gen `stage 06` writes it after `stage 05`).
- Don't skip the `manifest.json.commit_sha` cross-check. A baked binary that doesn't match the baked rootfs payload is a release-pipeline bug and a silent one when shipped.

## Channel resolution

`AIRPLANES_WEBCONFIG_BRANCH` is passed through `airplanes_webconfig_resolve_tag`:

- A concrete `v[MAJOR].[MINOR].[PATCH]` tag is echoed back unchanged.
- `dev` → `airplanes_webconfig_resolve_dev_latest_tag()` → the moving `dev-latest` tag that the release workflow force-pushes on every dev build.
- Anything else aborting cleanly is preferable to silently falling through.

`airplanes_webconfig_resolve_latest_stable_tag()` (`git ls-remote --tags --refs` filtered by strict semver regex `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`, highest via `sort -V`) remains available for resolving `stable`.

## Manifest cross-check rationale

In build mode, pi-gen clones the repo at some ref and runs `install.sh`. The script downloads the matching release. Both the cloned source AND the released binary should describe the same commit. The manifest cross-check exists because:

- A race window exists between a push to `dev` and the release workflow completing — a dev image build that lands in that window would clone a newer HEAD than the release at `dev-latest`.
- A misconfigured image config could pin `AIRPLANES_WEBCONFIG_BRANCH=v0.2.0` while a buggy pipeline tagged a binary built from `main` HEAD as `v0.2.0`.

The cross-check turns either of those into a build failure rather than a silently-mismatched image.

## Filter-repo history note

`airplanes-live/image-webconfig` was seeded by `git filter-repo --subdirectory-filter webconfig` over a clone of `airplanes-live/image`. **No tags were pushed during seeding** — image had a `dev-latest` tag whose history now lives only in `airplanes-live/image`, and image's semver tags don't apply to this repo's release namespace. The release namespace here starts fresh at `v0.1.0`.

## Build-host dependencies

`install.sh --build-mode` needs `git` (used by `airplanes_webconfig_resolve_*` for `git ls-remote`), `curl`, `tar`, `sha256sum`, and `python3` (one-liner JSON parse in `airplanes_webconfig_verify_manifest_*`). These run on the pi-gen build host, not on the feeder.

## GNU-only target

`install.sh` and `scripts/lib/install-common.sh` use `sort -V`, GNU `tar`, and `sha256sum -c` — Linux/GNU only. The build host is Linux, so this is fine. Don't try to support these scripts on BSD/macOS; write Go for anything that needs cross-platform reach.

## Updates are delivered through the runtime overlay

Webconfig has no in-product self-update path. Updates ship as part of the runtime overlay release in `airplanes-live/image`, which extracts the new binary + rootfs payload and restarts the service. `install.sh` runs only at image-build time.

### Upgrade-state HTTP surface

`GET /api/status/upgrade` returns `{"state": "clean" | "in-progress" | "failed" | "unknown"}`. `unknown` covers every operationally-indistinct case the caller cannot triage: missing file, empty, malformed, unparseable, read error. The marker is written by the overlay update path; a feeder that has never been upgraded — or one without the marker — reports `unknown`.

`/health` stays plain-text `ok <version>`: JSON-ifying it would misreport a rolled-back-with-`failed`-marker device as a successful upgrade because the version byte-changes after a partial extract. Upgrade state belongs on a dedicated status endpoint, not a health probe.

### Upgrade-state file ownership

Parent dir `/var/lib/airplanes/webconfig-upgrade/` is `0755 root:root`. File `/var/lib/airplanes/webconfig-upgrade/upgrade-state` is `0644 root:root`. The unprivileged `airplanes-webconfig` service account can read but cannot unlink/replace — intentionally NOT `/var/lib/airplanes/webconfig/`, which is `0700 airplanes-webconfig:airplanes-webconfig` and would let the account replace the marker regardless of file ownership.

The directory is provisioned two ways (idempotent): `install.sh --build-mode` calls `airplanes_webconfig_ensure_upgrade_state_dir` (mode 0755), and the rootfs tarball ships `files/var/lib/airplanes/webconfig-upgrade/.gitkeep` so `tar --owner=0 --group=0` lays the dir down as `root:root` during extract.

## Accepted v1 limitations

These are known, deferred to a follow-up:

- **TOFU on the release server.** Build-mode verification is SHA256 + HTTPS to `github.com`. A repo-admin compromise (or a sufficiently-deep GitHub compromise) lets an attacker serve an arbitrary `rootfs.tar.gz` + `airplanes-webconfig-arm64` and matching `SHA256SUMS`, and the build would bake them. Mitigation in the roadmap: detached minisign signatures over the release assets once the signing key is provisioned in CI.
- **Operator discipline for tag pushes.** A `v[0-9]+.[0-9]+.[0-9]+` tag pushed without a successful release pipeline run will be picked up by `airplanes_webconfig_resolve_latest_stable_tag` and lead to a "release assets not found" failure on any build that resolves it. Don't push semver tags by hand — let the release workflow create them.
