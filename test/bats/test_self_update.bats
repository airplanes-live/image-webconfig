#!/usr/bin/env bats
#
# End-to-end smoke for files/usr/local/lib/airplanes-webconfig/webconfig-self-update.sh.
# The helper re-execs under env -i and operates on absolute production paths
# (/usr/local/bin/airplanes-webconfig, /etc/systemd/system/airplanes-webconfig.service,
# /etc/airplanes/webconfig-release.json, http://127.0.0.1:8080/health). $PATH
# shims do not survive env -i and the absolute paths cannot be relocated for
# a test, so this file must run inside a disposable container where overwriting
# those paths is safe.
#
# Runner: test/bats/dockerized-bats.sh (debian:trixie-slim).
#
# What's covered:
#   * happy path — install + restart + cleanup; marker advances clean→clean
#   * /health probe exhaustion → rollback of binary + unit + manifest; marker → failed
#   * systemctl restart failure → rollback; marker → failed
#   * systemctl daemon-reload failure → rollback; marker → failed
#   * installer (install.sh) failure before binary swap → unit rollback only; marker → clean
#   * env -i scrub — caller env cannot redirect downloads
#   * concurrent-update guard — second invocation while a lock is held → exit 75; marker → clean
#   * upgrade-state machine entry guards: clean / absent / in-progress / failed / malformed
#   * absent marker + stray .prev files → migration fail-closed
#   * post-mutation installer failure (${BIN}.prev present) → marker → failed
#   * pre-mutation installer failure (no ${BIN}.prev) → marker → clean
#   * happy-path .prev cleanup failure → marker still advances to clean
#   * cp-backup failure → marker → failed before exit
#   * rollback-time state_write failure → marker stays at in-progress, WARN logged
#   * state-file + parent-dir ownership/mode after a happy-path run

# shellcheck source=lib/release_fixture.bash
load lib/release_fixture.bash

# Production paths the state machine writes. Read-only constants — every
# test reaches the same on-disk locations.
STATE_DIR=/var/lib/airplanes-webconfig-upgrade
STATE_FILE=${STATE_DIR}/upgrade-state

setup() {
    # The bats mutate /usr/local/bin, /etc/systemd, /etc/airplanes, etc.
    # An arbitrary container (e.g. a developer's devcontainer) may also
    # carry /.dockerenv but is not safe to mutate. Require the explicit
    # opt-in marker that dockerized-bats.sh sets so only that harness can
    # run this file.
    if [ -z "${AIRPLANES_BATS_INSIDE_CONTAINER:-}" ]; then
        skip "test_self_update.bats mutates production paths; run via test/bats/dockerized-bats.sh"
    fi

    REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/../.." && pwd)"
    export REPO_ROOT

    # Stage the helper, install.sh, and lib at production paths. Idempotent —
    # cheap to redo per-test.
    install -d -m 0755 /usr/local/lib/airplanes-webconfig
    install -d -m 0755 /usr/local/share/airplanes-webconfig/scripts/lib
    install -m 0755 "$REPO_ROOT/files/usr/local/lib/airplanes-webconfig/webconfig-self-update.sh" \
        /usr/local/lib/airplanes-webconfig/webconfig-self-update.sh
    install -m 0755 "$REPO_ROOT/install.sh" /usr/local/share/airplanes-webconfig/install.sh
    install -m 0644 "$REPO_ROOT/scripts/lib/install-common.sh" \
        /usr/local/share/airplanes-webconfig/scripts/lib/install-common.sh

    install -d -m 0755 /etc/airplanes /etc/systemd/system /run/airplanes /var/test-releases


    # Reset control flags to defaults.
    rm -rf /tmp/airplanes-test
    install -d -m 0755 /tmp/airplanes-test
    printf 'ok'   > /tmp/airplanes-test/health-mode
    printf 'ok'   > /tmp/airplanes-test/systemctl-mode
    : > /tmp/airplanes-test/systemctl-calls.log
    : > /tmp/airplanes-test/systemctl-restart-count

    # Reset .prev markers that may have leaked from a prior failing test.
    rm -f /usr/local/bin/airplanes-webconfig.prev \
          /etc/systemd/system/airplanes-webconfig.service.prev \
          /etc/airplanes/webconfig-release.json.prev

    # Provision the upgrade-state directory and write a `clean` marker so
    # each test starts in a known-good state. Tests that need a different
    # starting marker overwrite or remove $STATE_FILE.
    install -d -m 0755 "$STATE_DIR"
    printf 'clean\n' > "$STATE_FILE"
    chmod 0644 "$STATE_FILE"

    # Stage the previous (currently-running) install at production paths.
    install -d -m 0755 /usr/local/bin
    printf 'version=v0.1.0 baseline binary\n' > /usr/local/bin/airplanes-webconfig
    chmod 0755 /usr/local/bin/airplanes-webconfig

    cat > /etc/systemd/system/airplanes-webconfig.service <<'UNIT'
[Unit]
Description=airplanes.live webconfig (test baseline v0.1.0)
[Service]
ExecStart=/usr/local/bin/airplanes-webconfig
UNIT

    cat > /etc/airplanes/webconfig-release.json <<'JSON'
{"version":"v0.1.0","kind":"stable","commit_sha":"baseline","build_date":"2024-01-01T00:00:00Z","arches":["arm64","armhf"]}
JSON

    # Stage the v9.9.9 release the helper will install.
    rm -rf /var/test-releases
    install -d -m 0755 /var/test-releases
    stage_release_fixture /var/test-releases v9.9.9
    init_release_remote   /var/test-releases v9.9.9

    # Test wrapper update.sh: helper hardcodes this path. The helper owns
    # the upgrade flock at /run/airplanes/webconfig-update.lock for the
    # whole protocol — production update.sh has no flock of its own and
    # neither does this test wrapper. The wrapper just exports the
    # file:// URLs that the helper's env -i would otherwise scrub, then
    # execs install.sh.
    cat > /usr/local/share/airplanes-webconfig/update.sh <<'WRAPPER'
#!/bin/bash
set -euo pipefail
export AIRPLANES_WEBCONFIG_REPO=file:///var/test-releases/image-webconfig.git
export AIRPLANES_WEBCONFIG_DOWNLOAD_BASE=file:///var/test-releases
export AIRPLANES_BUILD_MODE=
exec bash /usr/local/share/airplanes-webconfig/install.sh --runtime
WRAPPER
    chmod 0755 /usr/local/share/airplanes-webconfig/update.sh

    # Release-channel: stable. v9.9.9 is the only stable tag in the bare repo.
    printf 'stable' > /etc/airplanes/release-channel

    # The helper's flock is at /run/airplanes/webconfig-update.lock.
    rm -f /run/airplanes/webconfig-update.lock
}

teardown() {
    rm -f /usr/local/bin/airplanes-webconfig.prev \
          /etc/systemd/system/airplanes-webconfig.service.prev \
          /etc/airplanes/webconfig-release.json.prev \
          /run/airplanes/webconfig-update.lock
    rm -rf /tmp/airplanes-test/fault-injection
    rm -f /tmp/airplanes-test/mktemp-state-count
}

run_helper() {
    bash /usr/local/lib/airplanes-webconfig/webconfig-self-update.sh
}

# read_state echoes the current marker contents (stripped of whitespace)
# or 'absent' when the file is missing. Used by assertions across all
# tests so the read mirrors the helper's state_read semantics.
read_state() {
    if [ ! -e "$STATE_FILE" ]; then
        echo absent
        return
    fi
    head -n1 "$STATE_FILE" | tr -d '[:space:]'
}

@test "happy path: swap binary, restart, clean .prev, manifest reports new version" {
    run run_helper
    [ "$status" -eq 0 ] || { echo "$output"; false; }

    grep -q 'fake-arm64-binary version=v9.9.9' /usr/local/bin/airplanes-webconfig
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]
    [ ! -f /etc/systemd/system/airplanes-webconfig.service.prev ]
    [ ! -f /etc/airplanes/webconfig-release.json.prev ]

    grep -q '"version": "v9.9.9"' /etc/airplanes/webconfig-release.json

    grep -q '^systemctl daemon-reload$' /tmp/airplanes-test/systemctl-calls.log
    grep -q '^systemctl restart airplanes-webconfig.service$' /tmp/airplanes-test/systemctl-calls.log

    # State machine: clean → clean (transition via in-progress).
    [ "$(read_state)" = clean ]
}

@test "rollback when /health probe exhausts: binary, unit, manifest restored; marker failed" {
    printf 'fail' > /tmp/airplanes-test/health-mode

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'health probe exhausted'

    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json

    # Final state: rollback markers are consumed (mv'd back), not lingering.
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]
    [ ! -f /etc/systemd/system/airplanes-webconfig.service.prev ]
    [ ! -f /etc/airplanes/webconfig-release.json.prev ]

    # restart is called twice: once post-install, once during rollback.
    [ "$(grep -c '^systemctl restart airplanes-webconfig.service$' /tmp/airplanes-test/systemctl-calls.log)" -eq 2 ]

    # State machine: clean → in-progress → failed (rollback succeeded but
    # the device went through a failed upgrade — next helper run must triage).
    [ "$(read_state)" = failed ]
}

@test "rollback when systemctl restart fails on first call; marker failed" {
    printf 'restart-fails-first' > /tmp/airplanes-test/systemctl-mode

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'restart failed; rolling back'

    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json

    # No /health probe is reached when restart fails (the probe loop only
    # runs after a successful restart). The stub records nothing for /health.

    [ "$(read_state)" = failed ]
}

@test "rollback when systemctl daemon-reload fails; marker failed" {
    printf 'daemon-reload-fails' > /tmp/airplanes-test/systemctl-mode

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'daemon-reload failed; rolling back'

    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json

    [ "$(read_state)" = failed ]
}

@test "rollback unit + manifest when installer fails before binary swap; marker clean" {
    # Tamper with the release binary so install.sh's SHA256 check rejects it
    # BEFORE writing .prev. The helper still has its own unit + manifest
    # backups from before invoking the installer, and must restore them.
    printf 'tampered\n' > /var/test-releases/v9.9.9/airplanes-webconfig-arm64

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -qE 'installer failed|SHA256|sha256'

    # Binary unchanged from the baseline (install.sh never got to the swap).
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    # Unit and manifest backups consumed (helper mv'd them back).
    [ ! -f /etc/systemd/system/airplanes-webconfig.service.prev ]
    [ ! -f /etc/airplanes/webconfig-release.json.prev ]
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json

    # restart is NOT called when install.sh fails.
    ! grep -q '^systemctl restart' /tmp/airplanes-test/systemctl-calls.log

    # Pre-mutation failure: live state is back to its pre-upgrade form,
    # so the next helper invocation may safely proceed. clean → clean.
    [ "$(read_state)" = clean ]
}

@test "env -i scrubs caller AIRPLANES_* vars before invoking installer" {
    # Replace update.sh with a marker-writing stub. If env -i scrubs as
    # documented, the marker reads <unset> for both vars. If the helper
    # regressed and propagated the caller's env, the marker would carry the
    # caller-supplied URL — that would let an unprivileged caller redirect
    # downloads via sudo, breaking the privilege boundary.
    cat > /usr/local/share/airplanes-webconfig/update.sh <<'STUB'
#!/bin/bash
{
    echo "REPO=${AIRPLANES_WEBCONFIG_REPO:-<unset>}"
    echo "DOWNLOAD_BASE=${AIRPLANES_WEBCONFIG_DOWNLOAD_BASE:-<unset>}"
} > /tmp/airplanes-test/env-scrub.log
exit 0
STUB
    chmod 0755 /usr/local/share/airplanes-webconfig/update.sh

    AIRPLANES_WEBCONFIG_REPO=http://caller-supplied.example \
    AIRPLANES_WEBCONFIG_DOWNLOAD_BASE=http://caller-supplied.example \
        run run_helper

    [ "$status" -eq 0 ] || { echo "$output"; false; }
    grep -q '^REPO=<unset>$' /tmp/airplanes-test/env-scrub.log
    grep -q '^DOWNLOAD_BASE=<unset>$' /tmp/airplanes-test/env-scrub.log
}

@test "rollback when no pre-existing manifest removes the failed-release manifest" {
    # Simulate a feeder that never carried /etc/airplanes/webconfig-release.json
    # (e.g. a baseline image that predates the manifest). The helper must not
    # leave the just-written failed-release manifest in place after rollback —
    # the device would otherwise lie about its installed version.
    rm -f /etc/airplanes/webconfig-release.json
    printf 'fail' > /tmp/airplanes-test/health-mode

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'health probe exhausted'

    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    [ ! -f /etc/airplanes/webconfig-release.json ]
    [ ! -f /etc/airplanes/webconfig-release.json.prev ]

    [ "$(read_state)" = failed ]
}

@test "exits 75 when another update is already running; marker untouched" {
    # Hold the lock on a DIFFERENT fd (8) than the helper uses (9). The
    # helper's `exec 9>"$LOCK_FILE"` would close an inherited fd-9 (and
    # implicitly release a lock held there); keeping the bats shell's
    # OFD on fd 8 makes flock(LOCK_NB) inside the helper actually contend
    # against the bats shell's lock.
    install -d -m 0755 /run/airplanes
    exec 8>/run/airplanes/webconfig-update.lock
    flock -n 8

    run run_helper
    [ "$status" -eq 75 ]

    # Release the lock.
    flock -u 8
    exec 8>&-

    # Binary untouched; the helper exited at the flock check, before
    # state_read / backups / installer.
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]

    # No systemctl call at all — the helper exited before reaching either
    # the installer's restart or the rollback's daemon-reload.
    [ ! -s /tmp/airplanes-test/systemctl-calls.log ]

    # Marker is untouched at its setup() value (clean) — the helper exits
    # 75 BEFORE state_read/state_write. The losing invocation cannot race
    # the winner's marker writes, which is the whole point of #19.
    [ "$(read_state)" = clean ]
}

# --- State machine entry guard cases ---

@test "absent marker + no .prev files: proceeds as clean" {
    # Wipe the marker that setup() wrote. With no .prev files on disk, the
    # absent state is treated as a first-flash device — proceed normally.
    rm -f "$STATE_FILE"

    run run_helper
    [ "$status" -eq 0 ] || { echo "$output"; false; }

    grep -q 'fake-arm64-binary version=v9.9.9' /usr/local/bin/airplanes-webconfig
    [ "$(read_state)" = clean ]
}

@test "absent marker + .prev files present: fail-closed with migration message" {
    # Simulate a pre-state-machine feeder that carries .prev files from a
    # prior run we cannot reason about. Helper must refuse rather than
    # overwrite (cp -a $UNIT_FILE → $UNIT_FILE.prev could destroy the
    # only good copy on a wedged device).
    rm -f "$STATE_FILE"
    install -m 0755 /usr/local/bin/airplanes-webconfig /usr/local/bin/airplanes-webconfig.prev

    run run_helper
    [ "$status" -eq 1 ]
    echo "$output" | grep -q 'absent but .prev files exist'
    echo "$output" | grep -q "echo clean | sudo tee $STATE_FILE"

    # Helper did not touch anything: .prev remains, marker remains absent.
    [ -f /usr/local/bin/airplanes-webconfig.prev ]
    [ ! -e "$STATE_FILE" ]
    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
}

@test "existing 'clean' marker + stale .prev: helper overwrites them and proceeds" {
    # `clean` means the prior upgrade succeeded but its rm cleanup may have
    # failed — .prev files are stale leftovers, safe to overwrite.
    install -m 0755 /usr/local/bin/airplanes-webconfig /usr/local/bin/airplanes-webconfig.prev

    run run_helper
    [ "$status" -eq 0 ] || { echo "$output"; false; }

    # Helper completed cleanly; the happy-path rm cleared the .prev files
    # the bats seeded (plus any the install created).
    grep -q 'fake-arm64-binary version=v9.9.9' /usr/local/bin/airplanes-webconfig
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]
    [ "$(read_state)" = clean ]
}

@test "existing 'in-progress' marker: fail-closed with operator-recovery message" {
    printf 'in-progress\n' > "$STATE_FILE"

    run run_helper
    [ "$status" -eq 1 ]
    echo "$output" | grep -q "marker is 'in-progress'"
    echo "$output" | grep -q "echo clean | sudo tee $STATE_FILE"

    # Helper did not invoke the installer.
    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig

    # Marker untouched.
    [ "$(read_state)" = in-progress ]
}

@test "existing 'failed' marker: fail-closed with operator-recovery message" {
    printf 'failed\n' > "$STATE_FILE"

    run run_helper
    [ "$status" -eq 1 ]
    echo "$output" | grep -q "marker is 'failed'"
    echo "$output" | grep -q "echo clean | sudo tee $STATE_FILE"

    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    [ "$(read_state)" = failed ]
}

@test "malformed marker: fail-closed" {
    printf 'garbage\n' > "$STATE_FILE"

    run run_helper
    [ "$status" -eq 1 ]
    echo "$output" | grep -qE 'malformed or unreadable'

    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
}

@test "empty marker: fail-closed" {
    : > "$STATE_FILE"

    run run_helper
    [ "$status" -eq 1 ]
    echo "$output" | grep -qE 'malformed or unreadable'

    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
}

@test "installer-missing preflight does NOT advance marker past clean" {
    # Remove the wrapper so the helper's pre-preflight check fails. The
    # state machine writes `in-progress` AFTER preflight + backups, so a
    # missing installer must NOT leave the marker at in-progress for the
    # next run to fail-closed on.
    rm -f /usr/local/share/airplanes-webconfig/update.sh

    run run_helper
    [ "$status" -eq 1 ]
    echo "$output" | grep -qE 'missing or not executable'

    # State machine did not transition: marker is still clean.
    [ "$(read_state)" = clean ]
    # Helper did not even reach the cp-backup phase.
    [ ! -f /etc/systemd/system/airplanes-webconfig.service.prev ]
    [ ! -f /etc/airplanes/webconfig-release.json.prev ]
}

@test "cp-backup failure writes 'failed' marker before exit" {
    # Make /etc/systemd/system/ read-only so cp -a UNIT_FILE → UNIT_FILE.prev
    # fails. We expect the helper to mark the upgrade `failed` before
    # exiting so the next run triages instead of treating partial .prev
    # leftovers as the prior good state.
    #
    # In the test container the bats run as root and bypass DAC. Switch
    # to chattr +i if the host kernel supports it; otherwise stub `cp`.
    # Use a `cp` override via PATH because the helper's env -i sets
    # PATH=/usr/sbin:/usr/bin:/sbin:/bin and /usr/bin/cp is the first
    # match. We replace /usr/bin/cp with a stub that fails ONLY for the
    # unit-file backup pattern, preserving cp for install.sh internals.
    mv /usr/bin/cp /usr/local/bin/cp.real
    cat > /usr/bin/cp <<'STUB'
#!/bin/bash
for arg in "$@"; do
    case "$arg" in
        /etc/systemd/system/airplanes-webconfig.service.prev)
            echo "cp: simulated failure" >&2
            exit 1
            ;;
    esac
done
exec /usr/local/bin/cp.real "$@"
STUB
    chmod 0755 /usr/bin/cp

    run run_helper
    rc="$status"
    # Restore real cp before any further bats assertion uses cp internally.
    mv /usr/local/bin/cp.real /usr/bin/cp

    [ "$rc" -eq 1 ]
    echo "$output" | grep -q 'cp -a /etc/systemd/system/airplanes-webconfig.service'

    # Marker must be `failed` so the next helper invocation triages.
    [ "$(read_state)" = failed ]
    # No installer invocation happened.
    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
}

@test "happy-path cleanup rm failure still marks clean (upgrade succeeded)" {
    # Replace /usr/bin/rm with a stub that fails on /usr/local/bin/airplanes-webconfig.prev
    # to simulate ENOSPC during the post-/health cleanup. The upgrade
    # succeeded; the marker must reflect that even though the rm warned.
    mv /usr/bin/rm /usr/local/bin/rm.real
    cat > /usr/bin/rm <<'STUB'
#!/bin/bash
for arg in "$@"; do
    case "$arg" in
        /usr/local/bin/airplanes-webconfig.prev)
            echo "rm: simulated cleanup failure" >&2
            exit 1
            ;;
    esac
done
exec /usr/local/bin/rm.real "$@"
STUB
    chmod 0755 /usr/bin/rm

    run run_helper
    rc="$status"
    mv /usr/local/bin/rm.real /usr/bin/rm

    [ "$rc" -eq 0 ] || { echo "$output"; false; }
    echo "$output" | grep -q 'WARN: failed to clean up .prev markers'

    # Marker must be `clean` — the upgrade reached /health OK, which is
    # the contract the state machine cares about, not cleanup hygiene.
    [ "$(read_state)" = clean ]
}

@test "rollback-time state_write failure leaves marker at 'in-progress'" {
    # Stage a PATH-pinned mktemp stub that succeeds for the first
    # state_write call (in-progress, before installer) and fails for
    # every subsequent state_file mktemp. The /health-exhaust path then
    # invokes rollback_and_exit, whose `state_write failed` cannot
    # overwrite the in-progress marker — exactly the failure mode we
    # want to catch.
    #
    # This test deliberately bypasses the env -i re-exec by setting the
    # REEXEC sentinel before invoking the helper, so the PATH override
    # propagates through. The env-i scrub is covered separately by the
    # 'env -i scrubs caller AIRPLANES_* vars' test.
    install -d -m 0755 /tmp/airplanes-test/fault-injection
    cat > /tmp/airplanes-test/fault-injection/mktemp <<'STUB'
#!/bin/bash
is_state_file=0
for arg in "$@"; do
    case "$arg" in
        */airplanes-webconfig-upgrade/upgrade-state.*) is_state_file=1 ;;
    esac
done
if [ "$is_state_file" -eq 1 ]; then
    COUNT_FILE=/tmp/airplanes-test/mktemp-state-count
    if [ ! -f "$COUNT_FILE" ]; then echo 0 > "$COUNT_FILE"; fi
    count=$(cat "$COUNT_FILE")
    count=$((count + 1))
    echo "$count" > "$COUNT_FILE"
    if [ "$count" -le 1 ]; then
        exec /usr/bin/mktemp "$@"
    fi
    exit 1
fi
exec /usr/bin/mktemp "$@"
STUB
    chmod 0755 /tmp/airplanes-test/fault-injection/mktemp

    printf 'fail' > /tmp/airplanes-test/health-mode

    AIRPLANES_WEBCONFIG_SELF_UPDATE_REEXEC=1 \
    PATH=/tmp/airplanes-test/fault-injection:/usr/sbin:/usr/bin:/sbin:/bin \
        run bash /usr/local/lib/airplanes-webconfig/webconfig-self-update.sh

    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'health probe exhausted'
    echo "$output" | grep -qE 'WARN.*cannot persist upgrade state'

    # First state_write (in-progress) succeeded; second one (failed from
    # rollback_and_exit) did not. Marker still carries in-progress, which
    # fail-closes the next helper invocation — the contract.
    [ "$(read_state)" = in-progress ]
}

@test "state-file + parent-dir ownership and mode after happy path" {
    run run_helper
    [ "$status" -eq 0 ] || { echo "$output"; false; }

    # Parent dir: 0755 root:root. The helper's state_write only mv's into
    # this dir; never chmod's the dir itself. install.sh's
    # airplanes_webconfig_ensure_upgrade_state_dir provisioned it at 0755.
    parent_mode="$(stat -c '%a' "$STATE_DIR")"
    parent_owner="$(stat -c '%U:%G' "$STATE_DIR")"
    [ "$parent_mode" = "755" ] || { echo "parent_mode=$parent_mode"; false; }
    [ "$parent_owner" = "root:root" ] || { echo "parent_owner=$parent_owner"; false; }

    # State file: 0644 root:root. state_write applies chmod 0644 to the
    # tempfile before mv, so the mode survives the atomic rename.
    file_mode="$(stat -c '%a' "$STATE_FILE")"
    file_owner="$(stat -c '%U:%G' "$STATE_FILE")"
    [ "$file_mode" = "644" ] || { echo "file_mode=$file_mode"; false; }
    [ "$file_owner" = "root:root" ] || { echo "file_owner=$file_owner"; false; }
}

@test "stale \${BIN}.prev from a prior cleanup-rm-failure does not downgrade on pre-mutation install failure" {
    # Scenario: the prior upgrade succeeded (marker=clean) but the
    # happy-path `rm -f ${BIN}.prev` failed (ENOSPC at the time). A
    # stale ${BIN}.prev now lingers on disk pointing at an OLDER binary
    # than the current live one. On the next upgrade attempt, install.sh
    # fails before its atomic-swap step (e.g., SHA256 rejects the
    # downloaded binary). Without the entry-time cleanup, the helper's
    # post-mutation classifier (`${BIN}.prev exists`) would mistakenly
    # restore the stale .prev — a silent downgrade. With the cleanup,
    # the classifier correctly sees pre-mutation and leaves live alone.
    printf 'version=v0.0.9 stale-prev-from-prior-cleanup-rm-failure\n' \
        > /usr/local/bin/airplanes-webconfig.prev
    chmod 0755 /usr/local/bin/airplanes-webconfig.prev

    # Trigger pre-mutation install failure: SHA256 rejects the binary.
    printf 'tampered\n' > /var/test-releases/v9.9.9/airplanes-webconfig-arm64

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -qE 'installer failed.*before binary swap|SHA256|sha256'

    # Critical: the live binary must remain v0.1.0 baseline, NOT the
    # stale v0.0.9 from the cleanup-failure leftover.
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    ! grep -q 'stale-prev-from-prior-cleanup-rm-failure' /usr/local/bin/airplanes-webconfig
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]

    # Live state is back to its pre-upgrade form; next run can retry.
    [ "$(read_state)" = clean ]
}

@test "stale \${BIN}.prev rm failure at entry: marker advances to 'failed'" {
    # If the entry-time cleanup of a stale ${BIN}.prev fails (FS error,
    # read-only mount, etc.) the heuristic is broken and the helper must
    # abort. Marker → failed so the next run forces operator triage.
    install -m 0755 /usr/local/bin/airplanes-webconfig /usr/local/bin/airplanes-webconfig.prev

    # Stub /usr/bin/rm to fail on the binary .prev (the entry-time
    # cleanup target) so we exercise the failure branch without disk-
    # full setup. /usr/bin/rm.real preserves teardown rm calls.
    mv /usr/bin/rm /usr/local/bin/rm.real
    cat > /usr/bin/rm <<'STUB'
#!/bin/bash
for arg in "$@"; do
    case "$arg" in
        /usr/local/bin/airplanes-webconfig.prev)
            echo "rm: simulated FS failure" >&2
            exit 1
            ;;
    esac
done
exec /usr/local/bin/rm.real "$@"
STUB
    chmod 0755 /usr/bin/rm

    run run_helper
    rc="$status"
    mv /usr/local/bin/rm.real /usr/bin/rm

    [ "$rc" -eq 1 ]
    echo "$output" | grep -qE 'could not remove stale.*airplanes-webconfig\.prev'

    # Marker must be `failed` so the next helper invocation triages.
    [ "$(read_state)" = failed ]
    # Installer was NOT invoked (we aborted at entry).
    ! grep -q '^systemctl' /tmp/airplanes-test/systemctl-calls.log
}

@test "post-mutation installer failure rolls back binary, unit, manifest; marker failed" {
    # Stub the wrapper update.sh to mimic install.sh failing AFTER the
    # atomic binary swap: cp -a current binary → .prev (matches what
    # install_binary's two-step rename does), write a "new" binary,
    # write a failed-release manifest, then exit non-zero. This is the
    # branch covered by webconfig-self-update.sh's `${BIN}.prev` post-
    # mutation classifier — Codex's review of #18 noted the test gap.
    cat > /usr/local/share/airplanes-webconfig/update.sh <<'WRAPPER'
#!/bin/bash
set -euo pipefail
# The helper owns the upgrade flock — this wrapper runs inside it and
# does not need its own. See production update.sh.

# Simulate install_binary's atomic swap.
cp -a /usr/local/bin/airplanes-webconfig /usr/local/bin/airplanes-webconfig.prev
printf 'version=v9.9.9 post-mutation-fake\n' > /usr/local/bin/airplanes-webconfig
chmod 0755 /usr/local/bin/airplanes-webconfig
# Simulate install_manifest writing the failed-release version.
cat > /etc/airplanes/webconfig-release.json <<MANIFEST
{"version":"v9.9.9","kind":"stable","commit_sha":"fake","build_date":"2024-01-01T00:00:00Z","arches":["arm64","armhf"]}
MANIFEST

# Now fail — simulating e.g. a manifest cross-check that rejects after
# the live state has been touched.
exit 5
WRAPPER
    chmod 0755 /usr/local/share/airplanes-webconfig/update.sh

    run run_helper
    [ "$status" -eq 5 ]
    echo "$output" | grep -qE 'installer failed.*after binary swap; rolling back'
    echo "$output" | grep -q 'restoring /usr/local/bin/airplanes-webconfig.prev'

    # Live state rolled back to baseline.
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json
    [ ! -f /etc/airplanes/webconfig-release.json.prev ]
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    [ ! -f /etc/systemd/system/airplanes-webconfig.service.prev ]

    # Post-mutation failure → marker `failed` even though rollback
    # itself succeeded (the rootfs payload definitively touched live
    # state; next run must triage).
    [ "$(read_state)" = failed ]
}

@test "pre-mutation restore failure marks 'failed' instead of 'clean'" {
    # Codex review of #18: if rootfs extract touched the unit/manifest
    # and the helper's restore mv fails, the live state is partially
    # corrupt but the previous code wrote clean unconditionally. Fix:
    # bubble restore_helper_backups's status up to the marker write.
    #
    # Trigger pre-mutation install failure (SHA tampering) plus a
    # restore mv failure (stub /usr/bin/mv for the unit-file restore).
    printf 'tampered\n' > /var/test-releases/v9.9.9/airplanes-webconfig-arm64

    mv /usr/bin/mv /usr/local/bin/mv.real
    cat > /usr/bin/mv <<'STUB'
#!/bin/bash
# Fail only when restoring the unit-file .prev (the restore_helper_backups
# call). state_write's mv (to the upgrade-state file) and install.sh's
# atomic-rename of the binary are unaffected — both go through.
for arg in "$@"; do
    case "$arg" in
        /etc/systemd/system/airplanes-webconfig.service)
            # This is the destination of restore_helper_backups's mv.
            echo "mv: simulated restore failure" >&2
            exit 1
            ;;
    esac
done
exec /usr/local/bin/mv.real "$@"
STUB
    chmod 0755 /usr/bin/mv

    run run_helper
    rc="$status"
    mv /usr/local/bin/mv.real /usr/bin/mv

    [ "$rc" -ne 0 ]
    echo "$output" | grep -q 'installer failed.*before binary swap'
    echo "$output" | grep -q 'restore_helper_backups: mv'
    echo "$output" | grep -q 'pre-mutation backup restore failed'

    # Marker must be `failed` so the next helper invocation triages —
    # not `clean`, because we couldn't undo the rootfs touch.
    [ "$(read_state)" = failed ]
}
