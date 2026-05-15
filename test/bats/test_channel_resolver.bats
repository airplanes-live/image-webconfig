#!/usr/bin/env bats
#
# Unit tests for scripts/lib/install-common.sh:
#   - airplanes_webconfig_resolve_latest_stable_tag against a local fake remote
#   - airplanes_webconfig_resolve_channel against synthetic release-channel files
#   - airplanes_webconfig_detect_arch in build-mode and runtime
#   - airplanes_webconfig_is_build_mode / parse_mode_args
#   - airplanes_webconfig_verify_manifest_sha

setup() {
    LIB="$BATS_TEST_DIRNAME/../../scripts/lib/install-common.sh"
    [ -f "$LIB" ] || skip "install-common.sh missing"

    WORK="$(mktemp -d)"
    FAKE_REMOTE="$WORK/fake-remote.git"

    # Build a bare repo with a handful of fake tags. We only need refs/tags/*,
    # not commit content, so empty commits are fine.
    git init -q --bare "$FAKE_REMOTE"
    WORKING="$WORK/seed"
    git init -q "$WORKING"
    (
        cd "$WORKING"
        git config user.email t@example.com
        git config user.name test
        git commit --allow-empty -q -m c1
        git tag v0.1.0
        git tag v0.2.0
        git tag v0.10.0
        git tag v1.0.0
        git tag v01.2.3            # invalid: leading zero, must be filtered
        git tag v0.1.0-rc.1        # invalid: prerelease, must be filtered
        git tag v0.1                # invalid: not three components
        git tag dev-latest
        git tag random
        git remote add origin "$FAKE_REMOTE"
        git push -q origin --tags
    )

    # Reset env so a stray export from the host shell doesn't leak in.
    unset AIRPLANES_BUILD_MODE
    unset AIRPLANES_WEBCONFIG_BRANCH
    unset AIRPLANES_ROOT
    unset ARCH

    AIRPLANES_WEBCONFIG_REPO="$FAKE_REMOTE"
    export AIRPLANES_WEBCONFIG_REPO

    # shellcheck source=/dev/null
    source "$LIB"
}

teardown() {
    rm -rf "$WORK"
}

# -- airplanes_webconfig_resolve_latest_stable_tag -------------------------

@test "resolve_latest_stable_tag: picks highest semver, filters invalid" {
    run airplanes_webconfig_resolve_latest_stable_tag
    [ "$status" -eq 0 ]
    [ "$output" = "v1.0.0" ]
}

@test "resolve_latest_stable_tag: returns 2 on network failure" {
    AIRPLANES_WEBCONFIG_REPO="/nonexistent-repo-$$"
    run airplanes_webconfig_resolve_latest_stable_tag
    [ "$status" -eq 2 ]
}

@test "resolve_latest_stable_tag: returns 1 when no semver tags exist" {
    EMPTY_REMOTE="$WORK/empty.git"
    git init -q --bare "$EMPTY_REMOTE"
    SEED2="$WORK/seed2"
    git init -q "$SEED2"
    (
        cd "$SEED2"
        git config user.email t@example.com
        git config user.name test
        git commit --allow-empty -q -m c1
        git tag random-tag
        git remote add origin "$EMPTY_REMOTE"
        git push -q origin --tags
    )
    AIRPLANES_WEBCONFIG_REPO="$EMPTY_REMOTE"
    run airplanes_webconfig_resolve_latest_stable_tag
    [ "$status" -eq 1 ]
}

@test "resolve_latest_stable_tag: picks v0.10.0 over v0.2.0 via sort -V" {
    EMPTY_REMOTE="$WORK/sortcheck.git"
    git init -q --bare "$EMPTY_REMOTE"
    SEED3="$WORK/seed3"
    git init -q "$SEED3"
    (
        cd "$SEED3"
        git config user.email t@example.com
        git config user.name test
        git commit --allow-empty -q -m c1
        git tag v0.2.0
        git tag v0.10.0
        git remote add origin "$EMPTY_REMOTE"
        git push -q origin --tags
    )
    AIRPLANES_WEBCONFIG_REPO="$EMPTY_REMOTE"
    run airplanes_webconfig_resolve_latest_stable_tag
    [ "$status" -eq 0 ]
    [ "$output" = "v0.10.0" ]
}

# -- airplanes_webconfig_resolve_dev_latest_tag ----------------------------

@test "resolve_dev_latest_tag: echoes 'dev-latest' if tag exists" {
    run airplanes_webconfig_resolve_dev_latest_tag
    [ "$status" -eq 0 ]
    [ "$output" = "dev-latest" ]
}

@test "resolve_dev_latest_tag: returns 1 if dev-latest is absent" {
    NO_DEV="$WORK/no-dev.git"
    git init -q --bare "$NO_DEV"
    SEED4="$WORK/seed4"
    git init -q "$SEED4"
    (
        cd "$SEED4"
        git config user.email t@example.com
        git config user.name test
        git commit --allow-empty -q -m c1
        git tag v0.1.0
        git remote add origin "$NO_DEV"
        git push -q origin --tags
    )
    AIRPLANES_WEBCONFIG_REPO="$NO_DEV"
    run airplanes_webconfig_resolve_dev_latest_tag
    [ "$status" -eq 1 ]
}

# -- airplanes_webconfig_resolve_channel -----------------------------------

@test "resolve_channel: build-mode reads AIRPLANES_WEBCONFIG_BRANCH" {
    AIRPLANES_BUILD_MODE=1
    AIRPLANES_WEBCONFIG_BRANCH="v9.9.9"
    run airplanes_webconfig_resolve_channel
    [ "$status" -eq 0 ]
    [ "$output" = "v9.9.9" ]
}

@test "resolve_channel: build-mode rejects unset AIRPLANES_WEBCONFIG_BRANCH" {
    AIRPLANES_BUILD_MODE=1
    unset AIRPLANES_WEBCONFIG_BRANCH
    run airplanes_webconfig_resolve_channel
    [ "$status" -ne 0 ]
}

@test "resolve_channel: runtime reads release-channel=stable" {
    install -d "$WORK/root/etc/airplanes"
    echo stable > "$WORK/root/etc/airplanes/release-channel"
    AIRPLANES_ROOT="$WORK/root"
    run airplanes_webconfig_resolve_channel
    [ "$status" -eq 0 ]
    [ "$output" = "stable" ]
}

@test "resolve_channel: runtime treats 'main' as stable alias" {
    install -d "$WORK/root/etc/airplanes"
    echo main > "$WORK/root/etc/airplanes/release-channel"
    AIRPLANES_ROOT="$WORK/root"
    run airplanes_webconfig_resolve_channel
    [ "$status" -eq 0 ]
    [ "$output" = "stable" ]
}

@test "resolve_channel: runtime reads release-channel=dev" {
    install -d "$WORK/root/etc/airplanes"
    echo dev > "$WORK/root/etc/airplanes/release-channel"
    AIRPLANES_ROOT="$WORK/root"
    run airplanes_webconfig_resolve_channel
    [ "$status" -eq 0 ]
    [ "$output" = "dev" ]
}

@test "resolve_channel: runtime rejects unknown channel" {
    install -d "$WORK/root/etc/airplanes"
    echo unstable > "$WORK/root/etc/airplanes/release-channel"
    AIRPLANES_ROOT="$WORK/root"
    run airplanes_webconfig_resolve_channel
    [ "$status" -ne 0 ]
}

@test "resolve_channel: runtime defaults to stable when file is missing" {
    AIRPLANES_ROOT="$WORK/empty-root"
    install -d "$AIRPLANES_ROOT/etc/airplanes"
    run airplanes_webconfig_resolve_channel
    [ "$status" -eq 0 ]
    [ "$output" = "stable" ]
}

# -- airplanes_webconfig_detect_arch ---------------------------------------

@test "detect_arch: build-mode honors ARCH=arm64" {
    AIRPLANES_BUILD_MODE=1
    ARCH=arm64
    run airplanes_webconfig_detect_arch
    [ "$status" -eq 0 ]
    [ "$output" = "arm64" ]
}

@test "detect_arch: build-mode honors ARCH=armhf" {
    AIRPLANES_BUILD_MODE=1
    ARCH=armhf
    run airplanes_webconfig_detect_arch
    [ "$status" -eq 0 ]
    [ "$output" = "armhf" ]
}

@test "detect_arch: build-mode rejects unset ARCH" {
    AIRPLANES_BUILD_MODE=1
    unset ARCH
    run airplanes_webconfig_detect_arch
    [ "$status" -ne 0 ]
}

@test "detect_arch: build-mode rejects unknown ARCH" {
    AIRPLANES_BUILD_MODE=1
    ARCH=mips
    run airplanes_webconfig_detect_arch
    [ "$status" -ne 0 ]
}

# -- mode-arg parsing ------------------------------------------------------

@test "parse_mode_args: --build-mode sets AIRPLANES_BUILD_MODE" {
    airplanes_webconfig_parse_mode_args --build-mode
    [ "$AIRPLANES_BUILD_MODE" = "1" ]
}

@test "parse_mode_args: --runtime clears AIRPLANES_BUILD_MODE" {
    AIRPLANES_BUILD_MODE=1
    airplanes_webconfig_parse_mode_args --runtime
    [ "$AIRPLANES_BUILD_MODE" = "0" ]
}

@test "is_build_mode: false by default" {
    unset AIRPLANES_BUILD_MODE
    run airplanes_webconfig_is_build_mode
    [ "$status" -ne 0 ]
}

@test "is_build_mode: true when env is '1'" {
    AIRPLANES_BUILD_MODE=1
    airplanes_webconfig_is_build_mode
}

@test "is_build_mode: true when env is 'true'" {
    AIRPLANES_BUILD_MODE=true
    airplanes_webconfig_is_build_mode
}

# -- manifest commit_sha verification --------------------------------------

@test "verify_manifest_sha: passes when matched" {
    cat > "$WORK/manifest.json" <<JSON
{"version":"v0.1.0","commit_sha":"abc123def456","arches":["arm64"]}
JSON
    airplanes_webconfig_verify_manifest_sha "$WORK/manifest.json" "abc123def456"
}

@test "verify_manifest_sha: fails on mismatch" {
    cat > "$WORK/manifest.json" <<JSON
{"version":"v0.1.0","commit_sha":"deadbeef","arches":["arm64"]}
JSON
    run airplanes_webconfig_verify_manifest_sha "$WORK/manifest.json" "abc123"
    [ "$status" -ne 0 ]
}

@test "verify_manifest_sha: fails when field missing" {
    cat > "$WORK/manifest.json" <<JSON
{"version":"v0.1.0","arches":["arm64"]}
JSON
    run airplanes_webconfig_verify_manifest_sha "$WORK/manifest.json" "abc"
    [ "$status" -ne 0 ]
}
