#!/usr/bin/env bash
# update.sh — thin on-device update entrypoint.
#
# Called by the sudoers-pinned self-update helper (PR-3 introduces
# /usr/local/lib/airplanes-webconfig/webconfig-self-update.sh). Acquires
# the update lock, dispatches to install.sh --runtime. The caller handles
# systemctl daemon-reload + restart + post-restart health probe + rollback.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

LOCK_DIR="${AIRPLANES_WEBCONFIG_LOCK_DIR:-/run/airplanes}"
LOCK_FILE="$LOCK_DIR/webconfig-update.lock"

install -d -m 0755 "$LOCK_DIR"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
    echo "ERROR: another webconfig update is in progress (lock held: $LOCK_FILE)" >&2
    exit 75   # EX_TEMPFAIL
fi

bash "$SCRIPT_DIR/install.sh" --runtime
