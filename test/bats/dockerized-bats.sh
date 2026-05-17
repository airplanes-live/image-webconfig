#!/usr/bin/env bash
# Runs test/bats/test_self_update.bats inside a disposable debian:trixie-slim
# container. The helper under test (webconfig-self-update.sh) re-execs under
# `env -i` and operates on absolute production paths (/usr/local/bin/...,
# /etc/systemd/..., /etc/airplanes/..., http://127.0.0.1:8080/health). $PATH
# shims don't survive env -i and those paths cannot be relocated for a test,
# so isolation by container is the only way to exercise the helper without
# mutating the host filesystem.
#
# Usage:
#   bash test/bats/dockerized-bats.sh [bats-file]
#
# Defaults to test/bats/test_self_update.bats.

set -euo pipefail

REPO_ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
BATS_FILE="${1:-test/bats/test_self_update.bats}"

command -v docker >/dev/null || { echo "ERROR: docker is required" >&2; exit 1; }

# Inner script that runs inside the container. Kept as a heredoc so the
# whole harness stays in a single file alongside the bats.
docker run --rm \
    --volume "$REPO_ROOT:/repo:ro" \
    --workdir /repo \
    --env "AIRPLANES_BATS_INSIDE_CONTAINER=1" \
    --env "BATS_FILE=$BATS_FILE" \
    debian:trixie-slim \
    bash -e -o pipefail -c '
        set -eu

        export DEBIAN_FRONTEND=noninteractive
        apt-get update -qq
        apt-get install -y --no-install-recommends \
            bats curl git tar python3 sudo util-linux ca-certificates >/dev/null

        # The /usr/bin/flock from util-linux already covers the helper.

        # The repo is bind-mounted read-only from the host; container runs
        # as root, host owner is non-root. git refuses to operate on the
        # tree without an explicit safe.directory grant.
        git config --global --add safe.directory /repo

        # Install stubs over real curl + systemctl. Real curl moves to
        # /usr/local/bin/curl.real so the stub can delegate file:// fetches.
        mv /usr/bin/curl /usr/local/bin/curl.real
        cp /repo/test/bats/lib/stubs/curl.sh /usr/bin/curl
        chmod 0755 /usr/bin/curl
        # The base image is systemd-less, so /usr/bin/systemctl does not
        # exist; just lay the stub.
        cp /repo/test/bats/lib/stubs/systemctl.sh /usr/bin/systemctl
        chmod 0755 /usr/bin/systemctl

        # The container almost certainly runs on x86_64. install.sh rejects
        # any uname -m that is not aarch64 or armv7l. Stub uname so the
        # helper resolves to arm64 and the existing arm64 fake binary in
        # the synthetic release applies cleanly. The helper re-execs under
        # env -i with PATH=/usr/sbin:/usr/bin:/sbin:/bin, so the stub at
        # /usr/bin/uname is what install.sh sees.
        mv /usr/bin/uname /usr/local/bin/uname.real
        cat > /usr/bin/uname <<UNAME_STUB
#!/bin/bash
if [ "\${1:-}" = "-m" ]; then
    echo aarch64
    exit 0
fi
exec /usr/local/bin/uname.real "\$@"
UNAME_STUB
        chmod 0755 /usr/bin/uname

        # Ensure no proxies leak in (file:// fetches do not need them).
        unset http_proxy https_proxy HTTPS_PROXY HTTP_PROXY NO_PROXY no_proxy

        echo "==> running $BATS_FILE"
        exec bats "/repo/$BATS_FILE"
    '
