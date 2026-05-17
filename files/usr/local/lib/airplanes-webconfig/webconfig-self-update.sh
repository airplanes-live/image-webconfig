#!/bin/bash
# webconfig-self-update.sh — invoked as root via the sudoers-pinned
# `systemd-run --unit=airplanes-webconfig-update` line. The transient unit
# lets the HTTP request that triggered the update return cleanly before
# this script runs systemctl restart on airplanes-webconfig.service.
#
# Steps:
#   1. Scrub the environment — never trust env from the webconfig caller.
#   2. Read /var/lib/airplanes-webconfig-upgrade/upgrade-state. Fail-closed
#      on `in-progress`, `failed`, malformed, or `absent` when any .prev
#      files exist (an unmarked feeder may carry stale or real rollback
#      markers — we cannot distinguish them, so triage is required).
#   3. Back up the live unit file and manifest alongside the binary so a
#      unit-file or manifest change in the new release can be rolled back
#      together with the binary if /health fails after the restart.
#   4. Mark the upgrade `in-progress` immediately before crossing the
#      live-state boundary (`/usr/local/share/airplanes-webconfig/update.sh`).
#   5. Run /usr/local/share/airplanes-webconfig/update.sh, which locks,
#      downloads and verifies the release, atomic-renames the binary
#      (saving the previous as .prev), and extracts the rootfs payload.
#      Distinguish pre-mutation failures (no ${BIN}.prev → mark clean,
#      live state untouched) from post-mutation failures (${BIN}.prev
#      exists → roll everything back, mark failed).
#   6. systemctl daemon-reload + restart airplanes-webconfig.service.
#   7. Poll /health for up to ~10s. On success, drop the .prev markers
#      and mark clean. On failure, restore .prev for the binary, unit,
#      and manifest, restart, mark failed, and exit non-zero so the SSE
#      log stream the SPA is reading shows the rollback.
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

# Upgrade state machine: a persistent marker that distinguishes stale
# .prev files (a successful upgrade whose happy-path cleanup failed) from
# real rollback markers (a failed upgrade whose live state may be corrupt
# and whose .prev files may be the only good copies).
#
# Parent dir is intentionally NEW and root-owned. /var/lib/airplanes-webconfig
# is 0700 airplanes-webconfig:airplanes-webconfig — the service account
# could unlink files there even when individual files are root-owned. A
# separate 0755 root:root parent keeps the marker out of the service
# account's reach.
STATE_DIR=/var/lib/airplanes-webconfig-upgrade
STATE_FILE=${STATE_DIR}/upgrade-state

MANIFEST_EXISTED=0

# Echoes one of: clean, in-progress, failed, absent, unknown. Treats an
# empty or whitespace-only marker as unknown (fail-closed on next read).
state_read() {
    if [ ! -e "$STATE_FILE" ]; then
        printf 'absent'
        return 0
    fi
    local line=""
    if ! line="$(head -n1 "$STATE_FILE" 2>/dev/null)"; then
        printf 'unknown'
        return 0
    fi
    # Strip leading + trailing whitespace.
    line="${line#"${line%%[![:space:]]*}"}"
    line="${line%"${line##*[![:space:]]}"}"
    case "$line" in
        clean|in-progress|failed) printf '%s' "$line" ;;
        *)                        printf 'unknown' ;;
    esac
}

# Atomic write: tempfile in same dir, fsync, mv -f, fsync parent. Returns
# 0 on success, non-zero on failure. Never aborts the caller — every
# caller decides whether marker-write failure is fatal for that path.
state_write() {
    local new_state="$1"
    local tmp=""
    if [ ! -d "$STATE_DIR" ]; then
        echo "WARN: $STATE_DIR missing; cannot persist upgrade state '$new_state'" >&2
        return 1
    fi
    if ! tmp="$(mktemp "${STATE_FILE}.XXXXXX" 2>/dev/null)"; then
        echo "WARN: mktemp in $STATE_DIR failed; cannot persist upgrade state '$new_state'" >&2
        return 1
    fi
    if ! printf '%s\n' "$new_state" > "$tmp"; then
        echo "WARN: write to $tmp failed; cannot persist upgrade state '$new_state'" >&2
        rm -f "$tmp"
        return 1
    fi
    chmod 0644 "$tmp" 2>/dev/null || true
    # sync -d caps fsync to data (not metadata); harmless if the kernel
    # or fs doesn't implement it — the mv below is still atomic at the
    # directory level on ext4.
    sync -d "$tmp" 2>/dev/null || true
    if ! mv -f "$tmp" "$STATE_FILE"; then
        echo "WARN: rename $tmp -> $STATE_FILE failed; upgrade state may be inconsistent" >&2
        rm -f "$tmp"
        return 1
    fi
    sync -d "$STATE_DIR" 2>/dev/null || true
    return 0
}

# Restore the helper's unit + manifest backups. Used by both pre-mutation
# rollback (installer rc!=0 with no ${BIN}.prev) and rc=75 lock-contention
# paths. Safe to call when no .prev exists. The mv is a no-op when live
# and .prev are byte-identical (e.g. rc=75 where install.sh never ran)
# and undoes a partial rootfs extract that overwrote the unit file when
# install.sh failed mid-extract.
#
# Returns 0 if every restore step succeeded (or had no work to do), 1 if
# any step failed. Caller decides whether to escalate to `failed` — for
# rc=75 (live state untouched) a failure is informational; for a pre-
# mutation install error (rootfs extract may have run) a failure means
# the live state is partially corrupt and the marker must reflect that.
restore_helper_backups() {
    local rc=0
    if [ -f "${UNIT_FILE}.prev" ]; then
        if ! mv -f "${UNIT_FILE}.prev" "$UNIT_FILE" 2>/dev/null; then
            echo "ERROR: restore_helper_backups: mv ${UNIT_FILE}.prev -> $UNIT_FILE failed" >&2
            rc=1
        fi
    fi
    if [ "$MANIFEST_EXISTED" -eq 1 ] && [ -f "${MANIFEST}.prev" ]; then
        if ! mv -f "${MANIFEST}.prev" "$MANIFEST" 2>/dev/null; then
            echo "ERROR: restore_helper_backups: mv ${MANIFEST}.prev -> $MANIFEST failed" >&2
            rc=1
        fi
    elif [ "$MANIFEST_EXISTED" -eq 0 ] && [ -f "$MANIFEST" ]; then
        if ! rm -f "$MANIFEST" 2>/dev/null; then
            echo "ERROR: restore_helper_backups: rm $MANIFEST failed" >&2
            rc=1
        fi
    fi
    return $rc
}

# Restores the previous binary, unit file, and manifest, then restarts the
# service so the rollback takes effect. Marks the upgrade `failed` (the
# next helper invocation must triage even after a clean rollback — the
# rootfs payload definitively touched live state). Exits with the
# supplied code (or 2 if any restore step itself failed).
rollback_and_exit() {
    local rc="$1"
    local restored=0
    local rollback_failed=0
    if [ -f "${BIN}.prev" ]; then
        if mv -f "${BIN}.prev" "$BIN"; then
            restored=1
        else
            echo "ERROR: rollback could not restore $BIN from .prev; manual recovery needed" >&2
            rollback_failed=1
        fi
    fi
    if [ -f "${UNIT_FILE}.prev" ]; then
        if mv -f "${UNIT_FILE}.prev" "$UNIT_FILE"; then
            restored=1
        else
            echo "ERROR: rollback could not restore $UNIT_FILE from .prev; manual recovery needed" >&2
            rollback_failed=1
        fi
    fi
    if [ "$MANIFEST_EXISTED" -eq 1 ] && [ -f "${MANIFEST}.prev" ]; then
        if mv -f "${MANIFEST}.prev" "$MANIFEST"; then
            restored=1
        else
            echo "ERROR: rollback could not restore $MANIFEST from .prev; device manifest reports failed release" >&2
            rollback_failed=1
        fi
    elif [ "$MANIFEST_EXISTED" -eq 0 ] && [ -f "$MANIFEST" ]; then
        if rm -f "$MANIFEST"; then
            restored=1
        else
            echo "ERROR: rollback could not remove failed-install $MANIFEST; device manifest reports failed release" >&2
            rollback_failed=1
        fi
    fi
    # Log the rollback summary BEFORE the restart — the restart kills the
    # webconfig service and with it the SSE log stream the SPA is reading,
    # so anything logged after the restart never reaches the user.
    if [ "$rollback_failed" -eq 1 ]; then
        echo "ERROR: rollback completed with errors; device may need manual intervention" >&2
    fi
    if [ "$restored" -eq 1 ]; then
        systemctl daemon-reload || true
        systemctl restart "$SERVICE" || true
    fi
    # `failed` regardless of whether each restore step succeeded — even a
    # clean rollback means this device went through a failed upgrade, and
    # the next helper invocation must fail-closed for triage.
    state_write failed || true
    if [ "$rollback_failed" -eq 1 ]; then
        exit 2
    fi
    exit "$rc"
}

# State machine entry — read the marker BEFORE preflight so a wedged
# device fails fast without touching anything else. Do NOT write
# `in-progress` here: that write happens after preflight succeeds.
case "$(state_read)" in
    clean)
        # Stable. Any .prev files on disk are leftover from a happy-path
        # cleanup whose `rm -f` failed. ${UNIT_FILE}.prev and
        # ${MANIFEST}.prev get overwritten by the helper's upcoming cp -a,
        # but ${BIN}.prev is created by install.sh (atomic-swap step) and
        # is NOT touched until then — a stale ${BIN}.prev would later be
        # mis-classified as a post-mutation rollback target on a pre-swap
        # install failure, restoring the older binary. Clean it up now.
        if [ -f "${BIN}.prev" ]; then
            if ! rm -f "${BIN}.prev"; then
                echo "WARN: could not remove stale ${BIN}.prev from a prior cleanup-rm-failure; refusing to proceed" >&2
                state_write failed || true
                exit 1
            fi
        fi
        ;;
    absent)
        # No marker file ever written. Two sub-cases:
        #   1) First flash / first helper invocation: no .prev files
        #      either. Proceed as clean.
        #   2) Pre-v0.1.3 feeder upgrading for the first time after this
        #      machinery shipped: the prior helper produced .prev files
        #      but no marker. We cannot tell whether they are stale
        #      (good live state) or a rollback target (live state
        #      corrupt). Fail-closed.
        if [ -f "${BIN}.prev" ] || [ -f "${UNIT_FILE}.prev" ] || [ -f "${MANIFEST}.prev" ]; then
            echo "ERROR: upgrade-state marker absent but .prev files exist on disk." >&2
            echo "       This feeder carried rollback markers before the state machine" >&2
            echo "       shipped. Cannot safely determine whether the .prev files are" >&2
            echo "       stale (good live state) or the only good copies (live state corrupt)." >&2
            echo "       Manual recovery — confirm $BIN runs, triage any .prev files, then:" >&2
            echo "         echo clean | sudo tee $STATE_FILE" >&2
            exit 1
        fi
        ;;
    in-progress)
        echo "ERROR: upgrade-state marker is 'in-progress'." >&2
        echo "       A prior helper invocation did not complete — likely a mid-upgrade" >&2
        echo "       reboot or process kill. Live state may be inconsistent with any" >&2
        echo "       .prev files that exist." >&2
        echo "       Manual recovery — confirm $BIN runs, triage any .prev files, then:" >&2
        echo "         echo clean | sudo tee $STATE_FILE" >&2
        exit 1
        ;;
    failed)
        echo "ERROR: upgrade-state marker is 'failed'." >&2
        echo "       A prior upgrade failed. .prev files (if present) may be the only" >&2
        echo "       good copies. Live state may carry rootfs files from the failed" >&2
        echo "       release." >&2
        echo "       Manual recovery — confirm $BIN runs, triage any .prev files, then:" >&2
        echo "         echo clean | sudo tee $STATE_FILE" >&2
        exit 1
        ;;
    *)
        echo "ERROR: upgrade-state marker at $STATE_FILE is malformed or unreadable." >&2
        echo "       Manual recovery — confirm $BIN runs, then:" >&2
        echo "         echo clean | sudo tee $STATE_FILE" >&2
        exit 1
        ;;
esac

if [ ! -x "$INSTALLER" ]; then
    echo "ERROR: $INSTALLER missing or not executable" >&2
    # No marker write — preflight failed BEFORE we touched any disk
    # state, so the existing `clean` marker is still accurate.
    exit 1
fi

# Back up the unit file and manifest so we can roll them back together
# with the binary. The installer rolls back the binary on its own
# failures; the helper additionally restores the unit (rewritten by
# rootfs extraction) and the manifest (rewritten by install.sh) so
# /health and the SPA both report the rolled-back release rather than
# the failed one after a /health-exhaustion rollback. A cp failure here
# may leave a partial .prev — mark `failed` so the next helper invocation
# triages before re-entering.
if [ -f "$UNIT_FILE" ]; then
    if ! cp -a "$UNIT_FILE" "${UNIT_FILE}.prev"; then
        echo "ERROR: cp -a $UNIT_FILE -> ${UNIT_FILE}.prev failed; refusing to start upgrade without rollback safety" >&2
        state_write failed || true
        exit 1
    fi
fi
if [ -f "$MANIFEST" ]; then
    if ! cp -a "$MANIFEST" "${MANIFEST}.prev"; then
        echo "ERROR: cp -a $MANIFEST -> ${MANIFEST}.prev failed; refusing to start upgrade without rollback safety" >&2
        rm -f "${UNIT_FILE}.prev"
        state_write failed || true
        exit 1
    fi
    MANIFEST_EXISTED=1
fi

# Mark `in-progress` immediately before crossing the live-state boundary.
# If the marker write itself fails we cannot proceed — the device would
# end up unable to distinguish later .prev files from a successful
# upgrade. Roll our backups back to keep the next run in the
# absent+no-.prev → clean path rather than absent+.prev → fail-closed.
if ! state_write in-progress; then
    echo "ERROR: cannot persist 'in-progress' upgrade state to $STATE_FILE; aborting before installer runs" >&2
    rm -f "${UNIT_FILE}.prev" "${MANIFEST}.prev" 2>/dev/null || true
    exit 1
fi

echo "webconfig-self-update: invoking $INSTALLER"
"$INSTALLER"
rc=$?

if [ "$rc" -ne 0 ]; then
    if [ "$rc" -eq 75 ]; then
        # Lock contention — wrapper update.sh exits 75 before invoking
        # install.sh, so live state is untouched. Restore our backups
        # (no-op against unchanged live state) and mark `clean` for a
        # benign retry-later. Even if the restore mv internally fails,
        # live state was never mutated, so .prev stragglers are harmless
        # and the next cp -a overwrites them.
        echo "webconfig-self-update: another update in progress (rc=75); retry later" >&2
        restore_helper_backups || true
        state_write clean || true
        exit 75
    fi
    if [ ! -f "${BIN}.prev" ]; then
        # Pre-mutation install failure: download / SHA / manifest-verify
        # rejected, or extract_rootfs failed before the atomic binary
        # swap. Restore unit + manifest (rootfs extract may have
        # partially overwritten them).
        echo "webconfig-self-update: installer failed (rc=$rc) before binary swap" >&2
        if restore_helper_backups; then
            # Live state is back to its pre-upgrade form, modulo any
            # partial rootfs files we cannot un-extract (documented v1
            # limit). Safe for the next helper invocation to retry.
            state_write clean || true
        else
            # The unit or manifest restore mv failed. Rootfs extract may
            # have touched live state and we couldn't undo it — the
            # device is partially corrupt and the next run must triage
            # before retrying.
            echo "webconfig-self-update: pre-mutation backup restore failed; marking 'failed'" >&2
            state_write failed || true
        fi
        exit "$rc"
    fi

    # Post-mutation install failure: $BIN was swapped (install.sh may
    # have rolled it back itself). Restore everything we backed up and
    # mark `failed` — the rootfs payload definitively touched live state.
    echo "webconfig-self-update: installer failed (rc=$rc) after binary swap; rolling back" >&2
    rollback_failed=0
    if [ -f "${BIN}.prev" ]; then
        echo "webconfig-self-update: restoring ${BIN}.prev"
        if ! mv -f "${BIN}.prev" "$BIN"; then
            echo "ERROR: could not restore $BIN from .prev; manual recovery needed" >&2
            rollback_failed=1
        fi
    fi
    if [ -f "${UNIT_FILE}.prev" ]; then
        if mv -f "${UNIT_FILE}.prev" "$UNIT_FILE"; then
            systemctl daemon-reload || true
        else
            echo "ERROR: could not restore $UNIT_FILE from .prev; manual recovery needed" >&2
            rollback_failed=1
        fi
    fi
    if [ "$MANIFEST_EXISTED" -eq 1 ] && [ -f "${MANIFEST}.prev" ]; then
        if ! mv -f "${MANIFEST}.prev" "$MANIFEST"; then
            echo "ERROR: could not restore $MANIFEST from .prev; device manifest reports failed release" >&2
            rollback_failed=1
        fi
    elif [ "$MANIFEST_EXISTED" -eq 0 ] && [ -f "$MANIFEST" ]; then
        if ! rm -f "$MANIFEST"; then
            echo "ERROR: could not remove failed-install $MANIFEST; device manifest reports failed release" >&2
            rollback_failed=1
        fi
    fi
    state_write failed || true
    if [ "$rollback_failed" -eq 1 ]; then
        echo "ERROR: installer-fail rollback completed with errors; device may need manual intervention" >&2
        exit 2
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
        if ! rm -f "${BIN}.prev" "${UNIT_FILE}.prev" "${MANIFEST}.prev"; then
            echo "WARN: failed to clean up .prev markers after successful upgrade; non-fatal, may require cleanup before the next update" >&2
        fi
        echo "webconfig-self-update: /health OK after restart (attempt=$attempt)"
        # Upgrade succeeded even if the .prev cleanup itself warned —
        # the state machine cares about live-state validity, not stale-
        # leftover cleanliness. Mark `clean` so the next upgrade proceeds.
        state_write clean || true
        exit 0
    fi
    echo "webconfig-self-update: /health probe failed (attempt=$attempt/$HEALTH_MAX_ATTEMPTS)"
done

echo "webconfig-self-update: health probe exhausted; rolling back" >&2
rollback_and_exit 1
