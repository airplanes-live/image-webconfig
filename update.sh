#!/usr/bin/env bash
# update.sh — thin on-device update entrypoint.
#
# Called by the sudoers-pinned self-update helper at
# /usr/local/lib/airplanes-webconfig/webconfig-self-update.sh. The helper
# is the canonical entry point and owns the upgrade flock at
# /run/airplanes/webconfig-update.lock for the entire upgrade protocol
# (state read/write, backups, installer, restart, /health, rollback).
# update.sh runs WITHIN that lock and therefore does not take its own.
#
# Direct invocation (operator triage from a root shell, build-mode-style
# manual runs) bypasses the helper's lock and is the operator's
# responsibility — concurrent direct runs are not serialised.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bash "$SCRIPT_DIR/install.sh" --runtime
