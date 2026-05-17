#!/bin/bash
# release_fixture.bash — bats helpers that build a synthetic release at a
# file:// URL plus a bare git repo carrying the release tag. The release
# payload is structurally identical to what scripts/lib/build-release.sh
# produces, but the binaries are shell scripts (which never get exec'd in
# the tests that source this file) so no Go toolchain is required.
#
# Source from a bats setup() with REPO_ROOT pointing at the image-webconfig
# checkout. Helpers operate on caller-supplied directories so the same
# bats can stage multiple releases (e.g. v9.9.9 followed by v9.9.10 for
# A→B chain coverage).

# stage_release_fixture WORK VERSION
#
# Creates:
#   $WORK/$VERSION/airplanes-webconfig-arm64    fake binary, content includes the version
#   $WORK/$VERSION/airplanes-webconfig-armhf    fake binary
#   $WORK/$VERSION/rootfs.tar.gz                files/ tree from REPO_ROOT, sentinel marker,
#                                                installer copy at /usr/local/share/airplanes-webconfig/
#   $WORK/$VERSION/manifest.json                version, commit_sha (= REPO_ROOT HEAD), kind=stable
#   $WORK/$VERSION/SHA256SUMS                   covers all four
#
# Caller may set RELEASE_COMMIT_SHA before invoking to inject a specific
# commit sha into the manifest (used by tests that exercise the build-mode
# manifest cross-check). Default: REPO_ROOT's HEAD.
stage_release_fixture() {
    local work="$1"
    local version="$2"
    local payload="$work/$version"
    install -d -m 755 "$payload"

    printf 'fake-arm64-binary version=%s\n' "$version" > "$payload/airplanes-webconfig-arm64"
    printf 'fake-armhf-binary version=%s\n' "$version" > "$payload/airplanes-webconfig-armhf"

    local fakefs="$work/.fake-rootfs-$version"
    rm -rf "$fakefs"
    install -d -m 755 "$fakefs/usr/local/bin"
    install -d -m 755 "$fakefs/usr/local/lib/airplanes-webconfig"
    install -d -m 755 "$fakefs/usr/local/share/airplanes-webconfig/scripts/lib"
    install -d -m 755 "$fakefs/etc/systemd/system"
    printf 'sentinel version=%s\n' "$version" > "$fakefs/usr/local/bin/sentinel.txt"
    # The fake "new binary" the test wants installed at /usr/local/bin/airplanes-webconfig
    # after install.sh runs. install.sh installs the airplanes-webconfig-<arch> file from
    # the release dir, not from rootfs.tar.gz, so this marker is only for cases that
    # assert rootfs payload extraction overwrote /usr/local/bin/ contents. Keep separate.
    install -m 0755 "$REPO_ROOT/install.sh" "$fakefs/usr/local/share/airplanes-webconfig/install.sh"
    install -m 0755 "$REPO_ROOT/update.sh"  "$fakefs/usr/local/share/airplanes-webconfig/update.sh"
    install -m 0644 "$REPO_ROOT/scripts/lib/install-common.sh" \
        "$fakefs/usr/local/share/airplanes-webconfig/scripts/lib/install-common.sh"
    # The helper itself — so an upgrade carries forward the same self-update logic.
    install -m 0755 "$REPO_ROOT/files/usr/local/lib/airplanes-webconfig/webconfig-self-update.sh" \
        "$fakefs/usr/local/lib/airplanes-webconfig/webconfig-self-update.sh"
    # Ship a unit file that matches the production filename so the helper's
    # backup/restore path has something to operate on after rootfs extract.
    install -m 0644 "$REPO_ROOT/files/etc/systemd/system/airplanes-webconfig.service" \
        "$fakefs/etc/systemd/system/airplanes-webconfig.service"
    tar -C "$fakefs" --owner=0 --group=0 --numeric-owner --sort=name \
        --mtime='2024-01-01 00:00:00 UTC' \
        -czf "$payload/rootfs.tar.gz" .

    local commit_sha="${RELEASE_COMMIT_SHA:-$(git -C "$REPO_ROOT" rev-parse HEAD)}"
    cat > "$payload/manifest.json" <<JSON
{
  "version": "$version",
  "kind": "stable",
  "commit_sha": "$commit_sha",
  "build_date": "2024-01-01T00:00:00Z",
  "arches": ["arm64", "armhf"]
}
JSON

    (
        cd "$payload" || exit 1
        sha256sum \
            airplanes-webconfig-arm64 \
            airplanes-webconfig-armhf \
            rootfs.tar.gz \
            manifest.json \
            > SHA256SUMS
    )
}

# init_release_remote WORK TAG [TAG ...]
#
# Initializes a bare git repo at $WORK/image-webconfig.git carrying the given
# tags off a single empty commit. The repo is suitable as
# $AIRPLANES_WEBCONFIG_REPO for runtime tag resolution against file://.
init_release_remote() {
    local work="$1"
    shift
    local remote="$work/image-webconfig.git"
    rm -rf "$remote"
    git init -q --bare "$remote"
    local seed
    seed="$work/.seed-$$"
    rm -rf "$seed"
    git init -q "$seed"
    (
        cd "$seed" || exit 1
        git config user.email t@example.com
        git config user.name test
        git commit --allow-empty -q -m c1
        for tag in "$@"; do
            git tag "$tag"
        done
        git remote add origin "$remote"
        git push -q origin --tags
    )
    rm -rf "$seed"
}
