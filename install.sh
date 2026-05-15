#!/usr/bin/env bash
# install.sh — single install path for image build and on-device update.
#
# Modes:
#   --build-mode (or AIRPLANES_BUILD_MODE=1)
#       Invoked from pi-gen stage-05/00-run.sh. ROOTFS_DIR points at the
#       staging rootfs. ARCH is set by pi-gen. AIRPLANES_WEBCONFIG_BRANCH
#       is set by the image config (a concrete tag for stable, branch name
#       for dev). Downloads the matching GitHub release, verifies the
#       manifest's commit_sha equals git rev-parse HEAD of the cloned
#       source, installs the binary and rootfs payload into ROOTFS_DIR.
#       Does no systemctl, no user creation — those happen in the chroot
#       step inside the image build.
#
#   --runtime  (or AIRPLANES_BUILD_MODE not set)
#       Invoked by the on-device self-update helper. Reads
#       /etc/airplanes/release-channel to pick stable or dev. Downloads
#       the resolved release, verifies SHA256, keeps the existing binary
#       as .prev for rollback, atomic-rename-swaps in the new one,
#       extracts the rootfs payload, writes the manifest. Caller handles
#       systemctl daemon-reload + restart.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib/install-common.sh
source "$SCRIPT_DIR/scripts/lib/install-common.sh"

airplanes_webconfig_parse_mode_args "$@"

ARCH_NAME="$(airplanes_webconfig_detect_arch)"
CHANNEL="$(airplanes_webconfig_resolve_channel)"
TAG="$(airplanes_webconfig_resolve_tag "$CHANNEL")"

TARGET_ROOT="$(airplanes_webconfig_target_root)"
: "${TARGET_ROOT:=/}"

WORK_DIR="$(mktemp -d -t airplanes-webconfig-install.XXXXXXXX)"
trap 'rm -rf "$WORK_DIR"' EXIT

echo "image-webconfig install: mode=$(airplanes_webconfig_is_build_mode && echo build || echo runtime) arch=$ARCH_NAME channel=$CHANNEL tag=$TAG target_root=${TARGET_ROOT:-/}"

airplanes_webconfig_download_release "$TAG" "$ARCH_NAME" "$WORK_DIR"

# Cross-check manifest.version against the resolved tag so a tag moved
# between resolution and download (dev-latest force-push mid-update,
# stale-asset replay) is caught loudly.
airplanes_webconfig_verify_manifest_version "$WORK_DIR/manifest.json" "$TAG"

if airplanes_webconfig_is_build_mode; then
    # In build mode the caller cloned the source at AIRPLANES_WEBCONFIG_BRANCH.
    # The release manifest must point at the same commit, otherwise the baked
    # binary does not match the baked rootfs payload (helper scripts, systemd
    # units, sudoers). Hard-fail rather than silently ship a mismatch.
    EXPECTED_SHA="$(git -C "$SCRIPT_DIR" rev-parse HEAD)"
    airplanes_webconfig_verify_manifest_sha "$WORK_DIR/manifest.json" "$EXPECTED_SHA"
fi

airplanes_webconfig_extract_rootfs "$WORK_DIR/rootfs.tar.gz" "${TARGET_ROOT:-/}"
airplanes_webconfig_install_binary "$WORK_DIR/airplanes-webconfig-$ARCH_NAME" "${TARGET_ROOT:-/}"
airplanes_webconfig_install_manifest "$WORK_DIR/manifest.json" "${TARGET_ROOT:-/}"

# The release tarball already ships install.sh, update.sh, and
# scripts/lib/install-common.sh under /usr/local/share/airplanes-webconfig/.
# Subsequent on-device updates invoke that installed copy, not the original
# clone or the previous version's copy.

echo "image-webconfig install: done"
