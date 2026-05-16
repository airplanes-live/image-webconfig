#!/bin/bash
# webconfig-self-update.sh — invoked as root via the sudoers-pinned
# `systemd-run --unit=airplanes-webconfig-update` line. The transient unit
# lets the HTTP request that triggered the update return cleanly before
# this script runs systemctl restart on airplanes-webconfig.service.
#
# Steps:
#   1. Scrub the environment — never trust env from the webconfig caller.
#   2. Back up the live unit file alongside the binary so a unit-file
#      change in the new release can be rolled back together with the
#      binary if /health fails after the restart.
#   3. Run /usr/local/share/airplanes-webconfig/update.sh, which locks,
#      downloads and verifies the release, atomic-renames the binary
#      (saving the previous as .prev), and extracts the rootfs payload.
#   4. systemctl daemon-reload + restart airplanes-webconfig.service.
#   5. Poll /health for up to ~10s. On success, drop the .prev markers.
#      On failure, restore .prev for both the binary and the unit, then
#      restart, and exit non-zero so the SSE log stream the SPA is reading
#      shows the rollback.
#
# Why not `set -e`: a failed daemon-reload or restart under `set -e`
# would abort before the rollback block runs, leaving the new binary in
# place and the service down. The script handles errors explicitly so
# every failure path routes through rollback.

set -uo pipefail

# Reset the environment. The HTTP-facing process is a non-root service
# account; we do not trust any AIRPLANES_WEBCONFIG_* env it might have
# exported through the sudo grant. We also re-exec under `env -i` for a
# hard guarantee — sudoers' env_reset gives us a clean slate but a global
# `Defaults env_keep` (if some operator adds one) could leak through.
if [ -z "${AIRPLANES_WEBCONFIG_SELF_UPDATE_REEXEC:-}" ]; then
    exec /usr/bin/env -i \
        PATH=/usr/sbin:/usr/bin:/sbin:/bin \
        LC_ALL=C \
        AIRPLANES_WEBCONFIG_SELF_UPDATE_REEXEC=1 \
        /bin/bash "$0" "$@"
fi

INSTALLER=/usr/local/share/airplanes-webconfig/update.sh
BIN=/usr/local/bin/airplanes-webconfig
UNIT_FILE=/etc/systemd/system/airplanes-webconfig.service
MANIFEST=/etc/airplanes/webconfig-release.json
SERVICE=airplanes-webconfig.service
HEALTH_URL=http://127.0.0.1:8080/health
HEALTH_MAX_ATTEMPTS=10
HEALTH_SLEEP_SECONDS=1

# Restores the previous binary, unit file, and manifest (if backed up) and
# restarts the service so the rollback takes effect, then exits with the
# supplied code. The manifest backs up too so /health and the SPA report
# the actually-running version after a rollback, not the failed release's.
rollback_and_exit() {
    local rc="$1"
    local restored=0
    if [ -f "${BIN}.prev" ]; then
        mv -f "${BIN}.prev" "$BIN" && restored=1
    fi
    if [ -f "${UNIT_FILE}.prev" ]; then
        mv -f "${UNIT_FILE}.prev" "$UNIT_FILE" && restored=1
    fi
    if [ -f "${MANIFEST}.prev" ]; then
        mv -f "${MANIFEST}.prev" "$MANIFEST" && restored=1
    fi
    if [ "$restored" -eq 1 ]; then
        systemctl daemon-reload || true
        systemctl restart "$SERVICE" || true
    fi
    exit "$rc"
}

if [ ! -x "$INSTALLER" ]; then
    echo "ERROR: $INSTALLER missing or not executable" >&2
    exit 1
fi

# Back up the unit file and manifest so we can roll them back together
# with the binary. The installer rolls back the binary on its own
# failures; the helper additionally restores the unit (rewritten by
# rootfs extraction) and the manifest (rewritten by install.sh) so
# /health and the SPA both report the rolled-back release rather than
# the failed one after a /health-exhaustion rollback.
if [ -f "$UNIT_FILE" ]; then
    cp -a "$UNIT_FILE" "${UNIT_FILE}.prev"
fi
if [ -f "$MANIFEST" ]; then
    cp -a "$MANIFEST" "${MANIFEST}.prev"
fi

echo "webconfig-self-update: invoking $INSTALLER"
"$INSTALLER"
rc=$?
if [ "$rc" -ne 0 ]; then
    echo "webconfig-self-update: installer failed (rc=$rc)" >&2
    # The installer's own rollback may have run; we additionally restore
    # the unit file we backed up.
    if [ -f "${BIN}.prev" ]; then
        echo "webconfig-self-update: restoring ${BIN}.prev"
        mv -f "${BIN}.prev" "$BIN"
    fi
    if [ -f "${UNIT_FILE}.prev" ]; then
        mv -f "${UNIT_FILE}.prev" "$UNIT_FILE"
        systemctl daemon-reload || true
    fi
    if [ -f "${MANIFEST}.prev" ]; then
        mv -f "${MANIFEST}.prev" "$MANIFEST"
    fi
    exit "$rc"
fi

echo "webconfig-self-update: daemon-reload + restart $SERVICE"
if ! systemctl daemon-reload; then
    echo "webconfig-self-update: daemon-reload failed; rolling back" >&2
    rollback_and_exit 1
fi
if ! systemctl restart "$SERVICE"; then
    echo "webconfig-self-update: restart failed; rolling back" >&2
    rollback_and_exit 1
fi

attempt=0
while [ "$attempt" -lt "$HEALTH_MAX_ATTEMPTS" ]; do
    attempt=$((attempt + 1))
    sleep "$HEALTH_SLEEP_SECONDS"
    if curl -fsS -o /dev/null --max-time 2 "$HEALTH_URL"; then
        rm -f "${BIN}.prev" "${UNIT_FILE}.prev" "${MANIFEST}.prev"
        echo "webconfig-self-update: /health OK after restart (attempt=$attempt)"
        exit 0
    fi
    echo "webconfig-self-update: /health probe failed (attempt=$attempt/$HEALTH_MAX_ATTEMPTS)"
done

echo "webconfig-self-update: health probe exhausted; rolling back" >&2
rollback_and_exit 1
