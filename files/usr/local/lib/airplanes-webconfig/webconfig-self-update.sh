#!/bin/bash
# webconfig-self-update.sh — invoked as root via the sudoers-pinned
# `systemd-run --unit=airplanes-webconfig-update` line. The transient unit
# lets the HTTP request that triggered the update return cleanly before
# this script runs systemctl restart on airplanes-webconfig.service.
#
# Steps:
#   1. Scrub the environment — never trust env from the webconfig caller.
#   2. Run /usr/local/share/airplanes-webconfig/update.sh, which locks,
#      downloads and verifies the release, atomic-renames the binary
#      (saving the previous as .prev), and extracts the rootfs payload.
#   3. systemctl daemon-reload + restart airplanes-webconfig.service.
#   4. Poll /health for up to ~10s. On success, drop the .prev marker.
#      On failure, restore .prev, restart, and exit non-zero so the SSE
#      log stream the SPA is reading shows the rollback.

set -euo pipefail

# Reset the environment. The HTTP-facing process is a non-root service
# account; we do not trust any AIRPLANES_WEBCONFIG_* env it might have
# exported through the sudo grant. PATH is set to systemd's default for
# system services so curl/git/tar/sha256sum/python3 resolve consistently.
while IFS='=' read -r _var _; do
    case "$_var" in
        AIRPLANES_*) unset "$_var" ;;
    esac
done < <(env)
unset _var
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
export LC_ALL=C

INSTALLER=/usr/local/share/airplanes-webconfig/update.sh
BIN=/usr/local/bin/airplanes-webconfig
SERVICE=airplanes-webconfig.service
HEALTH_URL=http://127.0.0.1:8080/health
HEALTH_MAX_ATTEMPTS=10
HEALTH_SLEEP_SECONDS=1

if [ ! -x "$INSTALLER" ]; then
    echo "ERROR: $INSTALLER missing or not executable" >&2
    exit 1
fi

echo "webconfig-self-update: invoking $INSTALLER"
if ! "$INSTALLER"; then
    rc=$?
    echo "webconfig-self-update: installer failed (rc=$rc)" >&2
    if [ -f "${BIN}.prev" ]; then
        echo "webconfig-self-update: restoring ${BIN}.prev"
        mv -f "${BIN}.prev" "$BIN"
    fi
    exit "$rc"
fi

echo "webconfig-self-update: daemon-reload + restart $SERVICE"
systemctl daemon-reload
systemctl restart "$SERVICE"

attempt=0
while [ "$attempt" -lt "$HEALTH_MAX_ATTEMPTS" ]; do
    attempt=$((attempt + 1))
    sleep "$HEALTH_SLEEP_SECONDS"
    if curl -fsS -o /dev/null --max-time 2 "$HEALTH_URL"; then
        rm -f "${BIN}.prev"
        echo "webconfig-self-update: /health OK after restart (attempt=$attempt)"
        exit 0
    fi
    echo "webconfig-self-update: /health probe failed (attempt=$attempt/$HEALTH_MAX_ATTEMPTS)"
done

# Restart did not produce a healthy service. Roll the binary back to the
# pre-update copy and restart again. The rootfs tarball changes (units,
# helper scripts, sudoers) are intentionally not rolled back here — the
# binary is what determines whether the service can boot; the rest can be
# fixed by the next successful update cycle.
echo "webconfig-self-update: health probe exhausted; rolling back binary" >&2
if [ -f "${BIN}.prev" ]; then
    mv -f "${BIN}.prev" "$BIN"
    systemctl restart "$SERVICE" || true
fi
exit 1
