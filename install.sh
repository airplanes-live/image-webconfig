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

# Drop a copy of install.sh + its lib into the rootfs so subsequent on-device
# updates have the installer they need without re-cloning the repo.
INSTALL_LIB_DEST="${TARGET_ROOT:-/}/usr/local/share/airplanes-webconfig"
install -d -m 0755 "$INSTALL_LIB_DEST" "$INSTALL_LIB_DEST/scripts/lib"
install -m 0755 "$SCRIPT_DIR/install.sh" "$INSTALL_LIB_DEST/install.sh"
install -m 0755 "$SCRIPT_DIR/update.sh" "$INSTALL_LIB_DEST/update.sh"
install -m 0644 "$SCRIPT_DIR/scripts/lib/install-common.sh" "$INSTALL_LIB_DEST/scripts/lib/install-common.sh"

echo "image-webconfig install: done"
