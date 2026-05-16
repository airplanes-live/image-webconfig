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
#   * happy path — install + restart + cleanup
#   * /health probe exhaustion → rollback of binary + unit + manifest
#   * systemctl restart failure → rollback
#   * systemctl daemon-reload failure → rollback
#   * installer (install.sh) failure before binary swap → unit rollback only
#   * env -i scrub — caller env cannot redirect downloads
#   * concurrent-update guard — second invocation while a lock is held → exit 75

# shellcheck source=lib/release_fixture.bash
load lib/release_fixture.bash

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

    # Test wrapper update.sh: helper hardcodes this path. The wrapper mirrors
    # production update.sh's flock + install.sh chain (so test case 7's
    # concurrency guard exercises the same code path a real on-device update
    # would), then exports the file:// URLs that the helper's env -i would
    # otherwise scrub before exec'ing install.sh.
    cat > /usr/local/share/airplanes-webconfig/update.sh <<'WRAPPER'
#!/bin/bash
set -euo pipefail
LOCK_DIR="${AIRPLANES_WEBCONFIG_LOCK_DIR:-/run/airplanes}"
LOCK_FILE="$LOCK_DIR/webconfig-update.lock"
install -d -m 0755 "$LOCK_DIR"
exec 9>"$LOCK_FILE"
if ! flock -n 9; then
    echo "ERROR: another webconfig update is in progress (lock held: $LOCK_FILE)" >&2
    exit 75
fi
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
}

run_helper() {
    bash /usr/local/lib/airplanes-webconfig/webconfig-self-update.sh
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
}

@test "rollback when /health probe exhausts: binary, unit, manifest restored" {
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
}

@test "rollback when systemctl restart fails on first call" {
    printf 'restart-fails-first' > /tmp/airplanes-test/systemctl-mode

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'restart failed; rolling back'

    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json

    # No /health probe is reached when restart fails (the probe loop only
    # runs after a successful restart). The stub records nothing for /health.
}

@test "rollback when systemctl daemon-reload fails" {
    printf 'daemon-reload-fails' > /tmp/airplanes-test/systemctl-mode

    run run_helper
    [ "$status" -ne 0 ]
    echo "$output" | grep -q 'daemon-reload failed; rolling back'

    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    grep -q 'test baseline v0.1.0' /etc/systemd/system/airplanes-webconfig.service
    grep -q '"version":"v0.1.0"' /etc/airplanes/webconfig-release.json
}

@test "rollback unit + manifest when installer fails before binary swap" {
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
}

@test "exits 75 when another update is already running" {
    # Hold the lock on a DIFFERENT FD (8) than the wrapper uses (9). The
    # wrapper's `exec 9>"$LOCK_FILE"` would close an inherited FD 9 and
    # implicitly release a lock held there; holding on FD 8 keeps the
    # bats shell's OFD distinct so the wrapper's flock(LOCK_NB) actually
    # contends with it.
    install -d -m 0755 /run/airplanes
    exec 8>/run/airplanes/webconfig-update.lock
    flock -n 8

    run run_helper
    [ "$status" -eq 75 ]

    # Release the lock.
    flock -u 8
    exec 8>&-

    # Binary untouched; installer never reached the swap.
    grep -q 'version=v0.1.0 baseline binary' /usr/local/bin/airplanes-webconfig
    [ ! -f /usr/local/bin/airplanes-webconfig.prev ]

    # No `systemctl restart` — the main path's restart only runs after a
    # successful installer. (The helper does call `systemctl daemon-reload`
    # during the unit-rollback path even on installer-fail; that's a wasted
    # no-op but not a correctness bug, so we only assert no `restart`.)
    ! grep -q '^systemctl restart' /tmp/airplanes-test/systemctl-calls.log
}
