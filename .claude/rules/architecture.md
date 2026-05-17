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

## Runtime dependencies on the Pi

The on-device updater needs `git` (used by `airplanes_webconfig_resolve_latest_stable_tag` / `airplanes_webconfig_resolve_dev_latest_tag` for `git ls-remote`), `curl`, `tar`, `sha256sum`, `flock`, and `python3` (one-liner JSON parse in `airplanes_webconfig_verify_manifest_*`). The image install path covers all of these (`git` and `python3` already needed elsewhere in the feed/cloud-init stack); a hand-installed webconfig on a stripped-down OS without `git` would fail tag resolution with what looks like a network error. If we ever drop `git` as a runtime dep, replace `git ls-remote` with a curl-against-the-releases-API path and refactor the resolver.

## GNU-only target

`install.sh`, `update.sh`, and `scripts/lib/install-common.sh` use `sort -V`, GNU `tar`, `flock`, and `sha256sum -c` — Linux/GNU only. The target is Raspberry Pi OS, so this is fine. Don't try to support these scripts on BSD/macOS; write Go for anything that needs cross-platform reach.

## Self-update privilege boundary (PR-3 contract)

When the on-device self-update helper invokes `update.sh` via the sudoers-pinned `systemd-run` line, it must:

- Run with `env -i` (or equivalent) so the running webconfig service (compromised attacker) cannot pass `AIRPLANES_WEBCONFIG_REPO=…` or `AIRPLANES_WEBCONFIG_DOWNLOAD_BASE=…` to point downloads at an attacker server.
- Set only the safe defaults inside the helper: `PATH=/usr/sbin:/usr/bin:/sbin:/bin`, no other AIRPLANES_* env.
- Reset the lock-file path to a hard-coded value (no `AIRPLANES_WEBCONFIG_LOCK_DIR` override from the caller's env).

The script files themselves still accept the env-var overrides because the bats tests rely on them — that's safe as long as the sudo-pinned helper scrubs the environment before invocation. The test suite covers the env-override path; the production privilege boundary is the helper's responsibility.

### Upgrade state machine

The helper writes a persistent marker at `/var/lib/airplanes-webconfig-upgrade/upgrade-state` so that a subsequent helper invocation can distinguish two indistinguishable on-disk states a `.prev` file alone cannot:

1. *Stale-from-cleanup-failure*: prior upgrade succeeded, happy-path `rm -f ${BIN}.prev` failed (ENOSPC). `.prev` reflects the binary BEFORE the prior successful upgrade — safe to overwrite.
2. *Real rollback-marker*: prior upgrade failed AND rollback also failed (`mv -f ${BIN}.prev $BIN` failed). `.prev` is the ONLY good copy — live `$BIN`/`$UNIT`/`$MANIFEST` is the failed-release contents.

Auto-cleanup at startup corrupts case 2; fail-closed on any `.prev` corrupts case 1's UX. The marker records which case produced the `.prev` files.

Three states:

- `clean` — written after `/health` OK + cleanup, OR after a pre-mutation installer failure (rc=75, download/SHA/preflight, partial-rootfs-no-binary-swap) where the helper restored its backups and live state was untouched.
- `in-progress` — written AFTER preflight + backups succeed, IMMEDIATELY before invoking the installer. The window in which the helper observes live state crossing the mutation boundary.
- `failed` — written on post-mutation install failures (the helper observed `${BIN}.prev` exists at installer exit, so the binary swap was attempted) OR after `rollback_and_exit` for any reason, even when each restore step succeeded. The narrow signal is `${BIN}.prev` existence after install.sh exit — `install.sh` only creates `.prev` at the atomic-swap step, so its presence after rc!=0 means live state may be inconsistent.

The state-write happens before `exit` at every termination path; see `webconfig-self-update.sh` for the exact write points.

`failed` is deliberately narrow. rc=75 (lock contention) and pre-mutation install failures route to `clean` so flaky-network upgrades don't wedge the device.

Migration rule: absent marker is NOT uniformly treated as `clean`. Absent + no `.prev` is `clean` (first flash). Absent + any `.prev` is fail-closed with a migration-recovery message — pre-state-machine feeders may silently carry rollback markers from a prior failed run; overwriting their `.prev` on the next upgrade would lose the only good copy.

Operator recovery for fail-closed devices, surfaced in the helper's log line:

```
echo clean | sudo tee /var/lib/airplanes-webconfig-upgrade/upgrade-state
```

No `--reset-upgrade-state` subcommand or production sudoers grant in v1 — the privilege surface stays minimal. The QEMU probe in `airplanes-live/image`'s boot-smoke exercises the recovery path via a test-only sudoers-pinned helper.

### Upgrade-state HTTP surface

`GET /api/status/upgrade` returns `{"state": "clean" | "in-progress" | "failed" | "unknown"}`. `unknown` covers every operationally-indistinct case the caller cannot triage from the SPA: missing file, empty, malformed, unparseable, read error. The SPA polls this after `POST /api/webconfig-update` to render "rolling back" or "wedged — manual recovery required" without grepping the SSE log.

`/health` stays plain-text `ok <version>` — the SPA captures it as raw text and treats any change as "update applied". JSON-ifying `/health` would misreport a rolled-back-with-`failed`-marker device as a successful upgrade because the version byte-changes after a partial extract. Upgrade state belongs on a dedicated status endpoint, not a health probe.

### Upgrade-state file ownership

Parent dir `/var/lib/airplanes-webconfig-upgrade/` is `0755 root:root`. File `/var/lib/airplanes-webconfig-upgrade/upgrade-state` is `0644 root:root`. The unprivileged `airplanes-webconfig` service account can read but cannot unlink/replace — intentionally NOT `/var/lib/airplanes-webconfig/`, which is `0700 airplanes-webconfig:airplanes-webconfig` and would let the account replace the marker regardless of file ownership.

The directory is provisioned in two places (idempotent):
- `install.sh` calls `airplanes_webconfig_ensure_upgrade_state_dir` (mode 0755) in both `--build-mode` and `--runtime` paths.
- The rootfs tarball includes `files/var/lib/airplanes-webconfig-upgrade/.gitkeep` so `tar --owner=0 --group=0` lays the dir down as `root:root` during extract.

## Accepted v1 limitations

These are known, deferred to a follow-up rather than fixed in the install/update pipeline:

- **TOFU on the release server.** Verification is SHA256 + HTTPS to `github.com`. A repo-admin compromise (or a sufficiently-deep GitHub compromise) lets an attacker serve an arbitrary `rootfs.tar.gz` + `airplanes-webconfig-arm64` and matching `SHA256SUMS`, and devices will install them. Mitigation in the roadmap: detached minisign signatures (the helper is structured to verify an extra `SHA256SUMS.minisig` next to `SHA256SUMS` once the signing key is provisioned in CI).
- **Atomic rootfs swap.** The runtime path extracts `rootfs.tar.gz` first, then atomic-swaps the binary, then writes the manifest. A power loss or ENOSPC mid-extraction can leave systemd units and helper scripts updated against the still-old binary. The binary's `.prev` rollback covers the most common failure (new binary doesn't start), but a partial tarball is not rolled back. Acceptable because (a) the binary is the one that breaks user-visible boot, and (b) the helper's `/health` probe + binary rollback recovers from the only failure that takes the UI down. A future hardening adds staged tar extract + bulk rename.
- **Operator discipline for tag pushes.** A `v[0-9]+.[0-9]+.[0-9]+` tag pushed without a successful release pipeline run will be picked up by `airplanes_webconfig_resolve_latest_stable_tag` and lead to a "release assets not found" failure on every device that resolves it. Don't push semver tags by hand — let the release workflow create them, or push from a state where you can confirm the workflow ran green before any device picks the tag up.
