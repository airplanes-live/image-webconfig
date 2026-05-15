#!/usr/bin/env bats
#
# End-to-end smoke for install.sh --build-mode and --runtime against a
# synthesized release payload served from a local file:// URL.
#
# What's covered:
#   - install.sh resolves channel, downloads all four assets, verifies
#     SHA256SUMS, extracts rootfs.tar.gz, installs the binary, writes
#     the manifest, copies the installer into the rootfs.
#   - build-mode rejects a manifest whose commit_sha does not match the
#     cloned source's HEAD.
#   - runtime mode preserves the previous binary as .prev for rollback.

setup() {
    REPO_ROOT="$(cd "$BATS_TEST_DIRNAME/../.." && pwd)"
    [ -f "$REPO_ROOT/install.sh" ] || skip "install.sh missing"

    WORK="$(mktemp -d)"

    # 1. Make a fake binary, rootfs tarball, manifest, and SHA256SUMS that
    #    look like a real GH release.
    PAYLOAD="$WORK/payload"
    install -d -m 755 "$PAYLOAD"

    # Fake binaries (shell scripts pretending to be ELFs — install.sh only
    # checksums them, it doesn't try to exec them in this test).
    printf 'fake-arm64-binary\n'  > "$PAYLOAD/airplanes-webconfig-arm64"
    printf 'fake-armhf-binary\n' > "$PAYLOAD/airplanes-webconfig-armhf"

    # Fake rootfs.tar.gz that ships a sentinel file under /usr/local/bin
    # so we can prove it extracted.
    FAKEFS="$WORK/fake-rootfs"
    install -d -m 755 "$FAKEFS/usr/local/bin"
    printf 'sentinel\n' > "$FAKEFS/usr/local/bin/sentinel.txt"
    tar -C "$FAKEFS" --owner=0 --group=0 --numeric-owner \
        -czf "$PAYLOAD/rootfs.tar.gz" .

    # Commit-SHA we'll inject into manifest.json — for build-mode tests we
    # set this to match the cloned source HEAD; for the failure test we
    # set it to something else.
    EXPECTED_SHA="$(git -C "$REPO_ROOT" rev-parse HEAD)"
    cat > "$PAYLOAD/manifest.json" <<JSON
{
  "version": "v9.9.9",
  "kind": "stable",
  "commit_sha": "$EXPECTED_SHA",
  "build_date": "2026-01-01T00:00:00Z",
  "arches": ["arm64", "armhf"]
}
JSON

    ( cd "$PAYLOAD" && sha256sum \
        airplanes-webconfig-arm64 \
        airplanes-webconfig-armhf \
        rootfs.tar.gz \
        manifest.json \
        > SHA256SUMS )

    # 2. Synthesize a release-tags fake remote so resolve_latest_stable_tag
    #    finds v9.9.9 if anything calls it. Most tests pin
    #    AIRPLANES_WEBCONFIG_BRANCH directly, so this is defensive.
    FAKE_REMOTE="$WORK/fake-remote.git"
    git init -q --bare "$FAKE_REMOTE"
    SEED="$WORK/seed"
    git init -q "$SEED"
    (
        cd "$SEED"
        git config user.email t@example.com
        git config user.name test
        git commit --allow-empty -q -m c1
        git tag v9.9.9
        git remote add origin "$FAKE_REMOTE"
        git push -q origin --tags
    )

    ROOTFS_DIR="$WORK/rootfs"
    install -d -m 755 "$ROOTFS_DIR"

    export AIRPLANES_WEBCONFIG_REPO="$FAKE_REMOTE"
    export AIRPLANES_WEBCONFIG_DOWNLOAD_BASE="file://$WORK"
    # Symlink so that file://$WORK/v9.9.9/* resolves to our payload.
    ln -s "$PAYLOAD" "$WORK/v9.9.9"
    export ROOTFS_DIR
    unset AIRPLANES_ROOT
}

teardown() {
    rm -rf "$WORK"
}

@test "install.sh --build-mode lays binary, rootfs, manifest, installer" {
    export AIRPLANES_WEBCONFIG_BRANCH=v9.9.9
    ARCH=arm64 run bash "$REPO_ROOT/install.sh" --build-mode
    [ "$status" -eq 0 ] || { echo "$output"; false; }

    [ -f "$ROOTFS_DIR/usr/local/bin/airplanes-webconfig" ]
    grep -q fake-arm64-binary "$ROOTFS_DIR/usr/local/bin/airplanes-webconfig"

    [ -f "$ROOTFS_DIR/usr/local/bin/sentinel.txt" ]
    [ -f "$ROOTFS_DIR/etc/airplanes/webconfig-release.json" ]
    grep -q "v9.9.9" "$ROOTFS_DIR/etc/airplanes/webconfig-release.json"

    [ -x "$ROOTFS_DIR/usr/local/share/airplanes-webconfig/install.sh" ]
    [ -x "$ROOTFS_DIR/usr/local/share/airplanes-webconfig/update.sh" ]
    [ -f "$ROOTFS_DIR/usr/local/share/airplanes-webconfig/scripts/lib/install-common.sh" ]
}

@test "install.sh --build-mode honors ARCH=armhf" {
    export AIRPLANES_WEBCONFIG_BRANCH=v9.9.9
    ARCH=armhf run bash "$REPO_ROOT/install.sh" --build-mode
    [ "$status" -eq 0 ] || { echo "$output"; false; }
    grep -q fake-armhf-binary "$ROOTFS_DIR/usr/local/bin/airplanes-webconfig"
}

@test "install.sh --build-mode rejects mismatched manifest commit_sha" {
    # Overwrite manifest.json with a wrong SHA and re-sign the SHA256SUMS.
    cat > "$PAYLOAD/manifest.json" <<JSON
{"version":"v9.9.9","commit_sha":"0000000000000000","kind":"stable","build_date":"2026-01-01T00:00:00Z","arches":["arm64"]}
JSON
    ( cd "$PAYLOAD" && sha256sum \
        airplanes-webconfig-arm64 \
        airplanes-webconfig-armhf \
        rootfs.tar.gz \
        manifest.json \
        > SHA256SUMS )

    export AIRPLANES_WEBCONFIG_BRANCH=v9.9.9
    ARCH=arm64 run bash "$REPO_ROOT/install.sh" --build-mode
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "commit_sha"
}

@test "install.sh --build-mode rejects tampered binary (sha256 mismatch)" {
    # Mutate the binary without updating SHA256SUMS.
    printf 'tampered\n' > "$PAYLOAD/airplanes-webconfig-arm64"

    export AIRPLANES_WEBCONFIG_BRANCH=v9.9.9
    ARCH=arm64 run bash "$REPO_ROOT/install.sh" --build-mode
    [ "$status" -ne 0 ]
    echo "$output" | grep -qE "SHA256|sha256"
}

@test "install.sh --runtime preserves previous binary as .prev" {
    # Seed a pretend-running install at AIRPLANES_ROOT.
    AIRPLANES_ROOT="$WORK/sysroot"
    install -d -m 755 "$AIRPLANES_ROOT/usr/local/bin" "$AIRPLANES_ROOT/etc/airplanes"
    printf 'previous-binary\n' > "$AIRPLANES_ROOT/usr/local/bin/airplanes-webconfig"
    chmod 0755 "$AIRPLANES_ROOT/usr/local/bin/airplanes-webconfig"
    echo stable > "$AIRPLANES_ROOT/etc/airplanes/release-channel"

    # Stub `uname -m` so detect_arch returns aarch64 (→ arm64) on this
    # x86 dev box. We prepend the stub to PATH so the install.sh subshell
    # picks it up before /usr/bin/uname.
    STUB_DIR="$WORK/stubs"
    install -d "$STUB_DIR"
    cat > "$STUB_DIR/uname" <<'STUB'
#!/bin/bash
if [[ "$1" == "-m" ]]; then echo aarch64; exit 0; fi
exec /usr/bin/uname "$@"
STUB
    chmod +x "$STUB_DIR/uname"

    unset AIRPLANES_WEBCONFIG_BRANCH
    unset ROOTFS_DIR
    export AIRPLANES_ROOT
    PATH="$STUB_DIR:$PATH" run bash "$REPO_ROOT/install.sh" --runtime
    if [ "$status" -ne 0 ]; then
        printf '%s\n' "$output"
        return 1
    fi

    [ -f "$AIRPLANES_ROOT/usr/local/bin/airplanes-webconfig.prev" ]
    grep -q "previous-binary" "$AIRPLANES_ROOT/usr/local/bin/airplanes-webconfig.prev"
    grep -q "fake-arm64-binary" "$AIRPLANES_ROOT/usr/local/bin/airplanes-webconfig"
}

@test "install.sh refuses armv6l (Pi Zero W1) clearly" {
    STUB_DIR="$WORK/stubs"
    install -d "$STUB_DIR"
    cat > "$STUB_DIR/uname" <<'STUB'
#!/bin/bash
if [[ "$1" == "-m" ]]; then echo armv6l; exit 0; fi
exec /usr/bin/uname "$@"
STUB
    chmod +x "$STUB_DIR/uname"

    AIRPLANES_ROOT="$WORK/sysroot2"
    install -d -m 755 "$AIRPLANES_ROOT/etc/airplanes"
    echo stable > "$AIRPLANES_ROOT/etc/airplanes/release-channel"

    unset AIRPLANES_WEBCONFIG_BRANCH
    unset ROOTFS_DIR
    export AIRPLANES_ROOT
    PATH="$STUB_DIR:$PATH" run bash "$REPO_ROOT/install.sh" --runtime
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "armv6l is not supported"
}
