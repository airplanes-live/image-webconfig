#!/usr/bin/env bats
#
# Unit tests for scripts/lib/install-common.sh:
#   - airplanes_webconfig_resolve_latest_stable_tag against a local fake remote
#   - airplanes_webconfig_resolve_channel reads AIRPLANES_WEBCONFIG_BRANCH
#   - airplanes_webconfig_detect_arch honors the pi-gen ARCH enum
#   - airplanes_webconfig_parse_mode_args
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
    unset AIRPLANES_WEBCONFIG_BRANCH
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

@test "resolve_channel: reads AIRPLANES_WEBCONFIG_BRANCH" {
    AIRPLANES_WEBCONFIG_BRANCH="v9.9.9"
    run airplanes_webconfig_resolve_channel
    [ "$status" -eq 0 ]
    [ "$output" = "v9.9.9" ]
}

@test "resolve_channel: rejects unset AIRPLANES_WEBCONFIG_BRANCH" {
    unset AIRPLANES_WEBCONFIG_BRANCH
    run airplanes_webconfig_resolve_channel
    [ "$status" -ne 0 ]
}

# -- airplanes_webconfig_detect_arch ---------------------------------------

@test "detect_arch: honors ARCH=arm64" {
    ARCH=arm64
    run airplanes_webconfig_detect_arch
    [ "$status" -eq 0 ]
    [ "$output" = "arm64" ]
}

@test "detect_arch: honors ARCH=armhf" {
    ARCH=armhf
    run airplanes_webconfig_detect_arch
    [ "$status" -eq 0 ]
    [ "$output" = "armhf" ]
}

@test "detect_arch: rejects unset ARCH" {
    unset ARCH
    run airplanes_webconfig_detect_arch
    [ "$status" -ne 0 ]
}

@test "detect_arch: rejects unknown ARCH" {
    ARCH=mips
    run airplanes_webconfig_detect_arch
    [ "$status" -ne 0 ]
}

# -- mode-arg parsing ------------------------------------------------------

@test "parse_mode_args: --build-mode is accepted" {
    run airplanes_webconfig_parse_mode_args --build-mode
    [ "$status" -eq 0 ]
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
