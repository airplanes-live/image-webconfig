#!/usr/bin/env bash
# Shared helpers for install.sh. The image build clones this repo in
# pi-gen stage-05 and runs install.sh --build-mode against the staging
# rootfs; that is the only consumer of these helpers. No curl-pipe-bash
# bootstrap path exists — the repo is always on disk when install.sh runs.

AIRPLANES_WEBCONFIG_REPO="${AIRPLANES_WEBCONFIG_REPO:-https://github.com/airplanes-live/image-webconfig.git}"
AIRPLANES_WEBCONFIG_RELEASES_API="${AIRPLANES_WEBCONFIG_RELEASES_API:-https://api.github.com/repos/airplanes-live/image-webconfig/releases}"
AIRPLANES_WEBCONFIG_DOWNLOAD_BASE="${AIRPLANES_WEBCONFIG_DOWNLOAD_BASE:-https://github.com/airplanes-live/image-webconfig/releases/download}"

# ---------------------------------------------------------------------------
# Mode parsing and path helpers
# ---------------------------------------------------------------------------
#
# install.sh has a single mode (build). --build-mode is accepted so the
# pi-gen stage-05 invocation reads explicitly; any other argument is
# ignored.

airplanes_webconfig_parse_mode_args() {
    local arg
    for arg in "$@"; do
        case "$arg" in
            --build-mode) ;;
        esac
    done
}

airplanes_webconfig_target_root() {
    printf '%s' "${ROOTFS_DIR:?ROOTFS_DIR must be set in build mode}"
}

# ---------------------------------------------------------------------------
# Arch detection
# ---------------------------------------------------------------------------
#
# Build mode honors pi-gen's ${ARCH} enum (arm64, armhf).

airplanes_webconfig_detect_arch() {
    case "${ARCH:-}" in
        arm64|armhf) printf '%s' "${ARCH}"; return 0 ;;
        "") echo "ERROR: ARCH must be set by pi-gen in build mode" >&2; return 1 ;;
        *) echo "ERROR: unsupported build-mode ARCH='${ARCH}' (expected arm64 or armhf)" >&2; return 1 ;;
    esac
}

# ---------------------------------------------------------------------------
# Channel resolution
# ---------------------------------------------------------------------------
#
# Build mode reads AIRPLANES_WEBCONFIG_BRANCH directly. The caller (pi-gen
# stage-05) pinned the desired release; do not consult /etc/airplanes/release-channel
# because stage 06 writes that file AFTER stage 05 runs.

airplanes_webconfig_resolve_channel() {
    if [[ -z "${AIRPLANES_WEBCONFIG_BRANCH:-}" ]]; then
        echo "ERROR: AIRPLANES_WEBCONFIG_BRANCH must be set in build mode (concrete tag for stable, branch for dev)" >&2
        return 1
    fi
    printf '%s' "${AIRPLANES_WEBCONFIG_BRANCH}"
}

# Resolves a channel name into a concrete release tag.
# - "stable" => latest v[MAJOR].[MINOR].[PATCH] tag via git ls-remote.
# - "dev"    => the moving "dev-latest" tag (rewritten on each push to dev
#               by the release workflow, points at the most recent dev build).
# - anything else (concrete tag/branch/sha) => echoed back unchanged.
#
# Using a single rewritable tag avoids JSON-parsing the GitHub Releases API
# on a feeder, and keeps the resolver path identical for stable and dev:
# both are answered by git ls-remote.
airplanes_webconfig_resolve_tag() {
    local channel="$1"
    case "$channel" in
        stable)
            airplanes_webconfig_resolve_latest_stable_tag
            ;;
        dev)
            airplanes_webconfig_resolve_dev_latest_tag
            ;;
        *)
            printf '%s' "$channel"
            ;;
    esac
}

# Strict semver match: vMAJOR.MINOR.PATCH, no leading zeroes, no prereleases.
# Echoes the highest matching tag. Inlined from feed's airplanes_resolve_latest_stable_tag.
airplanes_webconfig_resolve_latest_stable_tag() {
    local refs latest=""
    if ! refs="$(GIT_TERMINAL_PROMPT=0 git ls-remote --tags --refs "$AIRPLANES_WEBCONFIG_REPO" 2>/dev/null)"; then
        echo "ERROR: could not query release tags from $AIRPLANES_WEBCONFIG_REPO (network/DNS/TLS failure)" >&2
        return 2
    fi
    if [[ -z "$refs" ]]; then
        echo "ERROR: stable channel selected but no v[MAJOR].[MINOR].[PATCH] tags exist at $AIRPLANES_WEBCONFIG_REPO" >&2
        return 1
    fi
    local _sha _refname _tag
    while IFS=$'\t' read -r _sha _refname; do
        _tag="${_refname#refs/tags/}"
        if [[ "$_tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
            if [[ -z "$latest" ]]; then
                latest="$_tag"
            else
                latest="$(printf '%s\n%s\n' "$latest" "$_tag" | sort -V | tail -n 1)"
            fi
        fi
    done <<< "$refs"
    if [[ -z "$latest" ]]; then
        echo "ERROR: no v[MAJOR].[MINOR].[PATCH] tags at $AIRPLANES_WEBCONFIG_REPO" >&2
        return 1
    fi
    printf '%s' "$latest"
}

airplanes_webconfig_resolve_dev_latest_tag() {
    local refs
    if ! refs="$(GIT_TERMINAL_PROMPT=0 git ls-remote --tags --refs "$AIRPLANES_WEBCONFIG_REPO" refs/tags/dev-latest 2>/dev/null)"; then
        echo "ERROR: could not query dev-latest tag at $AIRPLANES_WEBCONFIG_REPO" >&2
        return 2
    fi
    if [[ -z "$refs" ]]; then
        echo "ERROR: dev channel selected but no 'dev-latest' tag exists at $AIRPLANES_WEBCONFIG_REPO" >&2
        return 1
    fi
    printf '%s' "dev-latest"
}

# ---------------------------------------------------------------------------
# Download + verify
# ---------------------------------------------------------------------------

airplanes_webconfig_download_release() {
    local tag="$1" arch="$2" dest_dir="$3"
    local base="${AIRPLANES_WEBCONFIG_DOWNLOAD_BASE}/${tag}"
    local binary_name="airplanes-webconfig-${arch}"

    install -d -m 755 "$dest_dir"

    local f
    for f in "$binary_name" "rootfs.tar.gz" "manifest.json" "SHA256SUMS"; do
        if ! curl -fsSL --max-time 120 -o "$dest_dir/$f" "$base/$f"; then
            echo "ERROR: download failed: $base/$f" >&2
            return 1
        fi
    done

    # Filter SHA256SUMS to exactly the four assets we just downloaded and
    # check those without --ignore-missing. A SHA256SUMS that omits the
    # selected binary or rootfs would otherwise pass on the lines that
    # happen to be present, leaving the omitted asset unverified.
    local filtered="$dest_dir/SHA256SUMS.expected"
    {
        grep -E "  ${binary_name}\$" "$dest_dir/SHA256SUMS"
        grep -E "  rootfs\.tar\.gz\$" "$dest_dir/SHA256SUMS"
        grep -E "  manifest\.json\$" "$dest_dir/SHA256SUMS"
    } > "$filtered" || true
    local expected_lines
    expected_lines="$(wc -l < "$filtered")"
    if [[ "$expected_lines" -ne 3 ]]; then
        echo "ERROR: SHA256SUMS missing one of $binary_name / rootfs.tar.gz / manifest.json" >&2
        cat "$dest_dir/SHA256SUMS" >&2
        return 1
    fi

    if ! ( cd "$dest_dir" && sha256sum -c SHA256SUMS.expected ); then
        echo "ERROR: SHA256 verification failed in $dest_dir" >&2
        return 1
    fi
}

airplanes_webconfig_verify_manifest_sha() {
    local manifest="$1" expected_sha="$2"
    local got
    # python3 is in the base Debian/RPiOS install (cloud-init, apt-listchanges,
    # debconf depend on it). Use json.loads rather than line-format-fragile
    # awk so a release pipeline that compacts the JSON onto one line keeps
    # working at runtime.
    got="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("commit_sha",""))' "$manifest" 2>/dev/null || true)"
    if [[ -z "$got" ]]; then
        echo "ERROR: manifest.json missing commit_sha field (path: $manifest)" >&2
        return 1
    fi
    if [[ "$got" != "$expected_sha" ]]; then
        echo "ERROR: manifest.json commit_sha=$got does not match expected=$expected_sha" >&2
        echo "       The release binary was built from a different source than the cloned repo." >&2
        return 1
    fi
}

# Verifies that the downloaded manifest's "version" field matches the tag we
# asked the release server for. Defends against a moved tag (dev-latest force-
# pushed mid-update) returning a manifest whose recorded version doesn't match
# the tag we resolved seconds earlier.
airplanes_webconfig_verify_manifest_version() {
    local manifest="$1" expected_tag="$2"
    local got
    got="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("version",""))' "$manifest" 2>/dev/null || true)"
    if [[ -z "$got" ]]; then
        echo "ERROR: manifest.json missing version field (path: $manifest)" >&2
        return 1
    fi
    if [[ "$got" != "$expected_tag" ]]; then
        echo "ERROR: manifest.json version=$got does not match resolved tag=$expected_tag" >&2
        echo "       A release tag may have moved between resolution and download." >&2
        return 1
    fi
}

# ---------------------------------------------------------------------------
# Install
# ---------------------------------------------------------------------------

airplanes_webconfig_extract_rootfs() {
    local tarball="$1" target_root="$2"
    # --no-overwrite-dir preserves directory permissions on existing dirs
    # like /etc/airplanes/webconfig (0700 airplanes-webconfig) and
    # /var/lib/airplanes-webconfig (0700 airplanes-webconfig). The tarball
    # ships files inside those dirs; tar would otherwise reset the dir mode.
    if ! tar -C "$target_root" --no-overwrite-dir -xzf "$tarball"; then
        echo "ERROR: rootfs extraction failed" >&2
        return 1
    fi
}

airplanes_webconfig_install_binary() {
    local src="$1" target_root="$2"
    local dest="$target_root/usr/local/bin/airplanes-webconfig"

    install -d -m 755 "$(dirname "$dest")"
    install -m 0755 "$src" "$dest"
}

airplanes_webconfig_install_manifest() {
    local src="$1" target_root="$2"
    local dest_dir="$target_root/etc/airplanes"
    install -d -m 0755 "$dest_dir"
    install -m 0644 "$src" "$dest_dir/webconfig-release.json"
}

# Ensures the upgrade-state directory exists with the right ownership and
# mode. The runtime overlay's update path writes an upgrade-state marker
# here; /api/status/upgrade reads it. The directory must be root-owned so
# the unprivileged service account cannot forge the marker.
#
# Idempotent: the rootfs tarball also ships this directory via
# files/var/lib/airplanes-webconfig-upgrade/.gitkeep, so this call is
# usually a no-op after extract_rootfs.
airplanes_webconfig_ensure_upgrade_state_dir() {
    local target_root="$1"
    local dir="${target_root}/var/lib/airplanes-webconfig-upgrade"
    # No explicit -o/-g: install.sh runs as root in production
    # (pi-gen chroot), so the dir is root:root by virtue of the caller's
    # identity — same
    # convention as install_manifest / install_binary above. Adding
    # -o 0 -g 0 here would break the test suite, which runs install.sh
    # as a non-root user (CI runner) against tmpfs ROOTFS_DIRs.
    install -d -m 0755 "$dir"
}
