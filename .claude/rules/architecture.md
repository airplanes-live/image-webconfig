# Architecture rules

Patterns that look like incidental code style but are actually invariants. Violating them ships a broken release, breaks an upgrade path on already-flashed feeders, opens a security boundary, or makes `/health` lie about which version is running.

## Release payload contract

Every published release carries exactly five assets:

- `airplanes-webconfig-arm64`
- `airplanes-webconfig-armhf`
- `rootfs.tar.gz` — `files/` tarred with deterministic ownership (`--owner=0 --group=0 --numeric-owner`), sorted (`--sort=name`), pinned mtime
- `manifest.json` — `{version, kind, commit_sha, build_date, arches}`
- `SHA256SUMS` — covers all four of the above

The set is a contract. Build-mode install (`install.sh --build-mode`) and the on-device self-update helper both expect all five and fail loudly if any is missing or its SHA256 doesn't match.

**Don't ship a binary without a matching rootfs.tar.gz.** The systemd unit, sudoers, and helper scripts must version with the binary (see "sudoers/argv parity" below). Shipping only a new ELF leaves the policy stale.

## Sudoers / argv parity

`internal/server/server.go`'s `DefaultPrivilegedArgv()` hard-codes every `/usr/bin/sudo -n …` argv shape the binary invokes. Each shape must appear byte-for-byte in `files/etc/sudoers.d/010_airplanes-webconfig` (base privileges) or `files/etc/sudoers.d/011_airplanes-webconfig-update` (self-update grant — placeholder until the helper lands).

The Go test `TestDefaultPrivilegedArgv_SudoersParity` enforces this from the binary side; the `visudo` CI job verifies the sudoers files parse. Both ship in the same release tarball, so a new argv requires a paired sudoers change in the same PR.

**Don't**:

- Don't let `010`/`011` live in `airplanes-live/image` while the argv lives here. Parity has to be enforceable in one repo.
- Don't add a new argv shape without adding a sudoers line for it AND a parity-test case.
- Don't broaden a sudoers entry to a wildcard (e.g. `apl-feed apply *`) to "fix" a parity failure. Pin the exact argv.

## Install-mode contract

`install.sh` has two modes:

- `--build-mode` (or `AIRPLANES_BUILD_MODE=1`): runs from a cloned source tree at pi-gen's build host. `ROOTFS_DIR` is the pi-gen staging rootfs; `ARCH` is set to `arm64` or `armhf`; `AIRPLANES_WEBCONFIG_BRANCH` is the concrete tag (stable) or branch (dev) the image config pinned. **Does not read `/etc/airplanes/release-channel`** — that file is written by pi-gen `stage 06`, which runs AFTER `stage 05`. Cross-checks `manifest.json.commit_sha` against `git rev-parse HEAD` of the cloned tree and hard-fails on mismatch.
- `--runtime`: runs on a booted feeder, invoked by the sudoers-pinned self-update helper. Reads `/etc/airplanes/release-channel`, resolves `stable` (highest semver tag) or `dev` (`dev-latest` moving tag), saves the previous binary as `.prev` for rollback, atomic-renames in the new one, extracts the rootfs payload, writes the manifest. Caller restarts the service and probes `/health`.

**Don't**:

- Don't read `/etc/airplanes/release-channel` from build mode. It's not there yet.
- Don't write directly over `/usr/local/bin/airplanes-webconfig` while the service is running — `ETXTBSY`. Use the two-step rename through `.new`.
- Don't skip the `manifest.json.commit_sha` cross-check in build mode. A baked binary that doesn't match the baked rootfs payload is a release-pipeline bug and a silent one when shipped.

## Channel resolution

Allowlist: `stable`, `dev`, `main`. `main` is a legacy alias for `stable` — kept consistent with feed's contract so a feeder migrating between feed and webconfig update mechanisms has one rule. Anything else aborts with a clear error rather than silently falling through.

- `stable` → `airplanes_webconfig_resolve_latest_stable_tag()` → `git ls-remote --tags --refs` filtered by strict semver regex `^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$` (no leading zeroes, no prereleases) → highest via `sort -V`.
- `dev` → `airplanes_webconfig_resolve_dev_latest_tag()` → the moving `dev-latest` tag that the release workflow force-pushes on every dev build. Single rewritable tag avoids JSON-parsing the Releases API on a feeder.

**Don't add a third channel** unless there's a real reason. Each channel multiplies test surface and operator confusion.

## Manifest cross-check rationale

In build mode, pi-gen clones the repo at some ref and runs `install.sh`. The script downloads the matching release. Both the cloned source AND the released binary should describe the same commit. The manifest cross-check exists because:

- A race window exists between a push to `dev` and the release workflow completing — a dev image build that lands in that window would clone a newer HEAD than the release at `dev-latest`.
- A misconfigured image config could pin `AIRPLANES_WEBCONFIG_BRANCH=v0.2.0` while a buggy pipeline tagged a binary built from `main` HEAD as `v0.2.0`.

The cross-check turns either of those into a build failure rather than a silently-mismatched image.

## Filter-repo history note

`airplanes-live/image-webconfig` was seeded by `git filter-repo --subdirectory-filter webconfig` over a clone of `airplanes-live/image`. **No tags were pushed during seeding** — image had a `dev-latest` tag whose history now lives only in `airplanes-live/image`, and image's semver tags don't apply to this repo's release namespace. The release namespace here starts fresh at `v0.1.0`.
