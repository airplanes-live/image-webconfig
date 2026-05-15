#!/bin/bash
# airplanes-system-upgrade.service entrypoint. Spawned by webconfig via
# `sudo systemd-run --unit=airplanes-system-upgrade --collect ...` when the
# user clicks "Update system packages". Runs as root inside a transient unit;
# `--collect` ensures the unit cleans itself up regardless of exit status.
#
# Pinned argv in /etc/sudoers.d/010_airplanes-webconfig — any change here
# must update the matching DefaultPrivilegedArgv in webconfig/internal/server.

set -euo pipefail

# Preflight before holding any locks or inhibitors. A near-full SD card is
# the failure mode most likely to brick an SSH-less feeder mid-upgrade, so
# fail fast with an actionable message instead of letting apt ENOSPC into a
# half-configured dpkg state.
ROOT_FREE_KB=$(df --output=avail / | tail -n1)
BOOT_FREE_KB=$(df --output=avail /boot/firmware | tail -n1)
if [[ "$ROOT_FREE_KB" -lt 512000 ]]; then
    echo "==> abort: only $((ROOT_FREE_KB / 1024)) MiB free on /, need >= 500 MiB"
    exit 1
fi
if [[ "$BOOT_FREE_KB" -lt 51200 ]]; then
    echo "==> abort: only $((BOOT_FREE_KB / 1024)) MiB free on /boot/firmware, need >= 50 MiB"
    exit 1
fi

# systemd-inhibit blocks shutdown/reboot while apt is touching dpkg.
# Defense in depth against a parallel `sudo reboot` from SSH; the HTTP-side
# 409 in /api/reboot covers the web-UI path.
exec systemd-inhibit \
    --what=shutdown \
    --who=airplanes-system-upgrade \
    --why="Applying OS package upgrades" \
    --mode=block \
    bash -c '
        set -euo pipefail
        export DEBIAN_FRONTEND=noninteractive
        export NEEDRESTART_MODE=a
        export APT_LISTCHANGES_FRONTEND=none

        echo "==> recovering any interrupted dpkg state"
        flock -w 300 /var/lib/dpkg/lock-frontend dpkg --configure -a

        echo "==> apt-get update"
        stdbuf -oL -eL apt-get --error-on=any \
            -o DPkg::Lock::Timeout=300 \
            update

        # --error-on=any is only honoured by apt-get update (treats sources
        # that emit warnings as errors); apt rejects the flag outright on
        # upgrade with exit 100, so it lives on update only.
        echo "==> apt-get upgrade"
        stdbuf -oL -eL apt-get -y \
            -o DPkg::Lock::Timeout=300 \
            -o Dpkg::Options::=--force-confold \
            -o Dpkg::Options::=--force-confdef \
            upgrade

        echo "==> done"
    '
