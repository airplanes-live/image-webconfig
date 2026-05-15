#!/usr/bin/env bash
# Shared helpers for install.sh, update.sh, and the on-device self-update
# entrypoint. All callers source this file; no curl-pipe-bash bootstrap path
# exists for image-webconfig (the repo is always on disk when these run —
# either via the pi-gen clone in stage-05 or via the rootfs tarball that
# an earlier install laid down at /usr/local/share/airplanes-webconfig/).

AIRPLANES_ROOT="${AIRPLANES_ROOT:-/}"
AIRPLANES_WEBCONFIG_REPO="${AIRPLANES_WEBCONFIG_REPO:-https://github.com/airplanes-live/image-webconfig.git}"
AIRPLANES_WEBCONFIG_RELEASES_API="${AIRPLANES_WEBCONFIG_RELEASES_API:-https://api.github.com/repos/airplanes-live/image-webconfig/releases}"
AIRPLANES_WEBCONFIG_DOWNLOAD_BASE="${AIRPLANES_WEBCONFIG_DOWNLOAD_BASE:-https://github.com/airplanes-live/image-webconfig/releases/download}"

# ---------------------------------------------------------------------------
# Mode detection and path helpers
# ---------------------------------------------------------------------------

airplanes_webconfig_is_build_mode() {
    [[ "${AIRPLANES_BUILD_MODE:-0}" == "1" \
        || "${AIRPLANES_BUILD_MODE:-}" == "true" \
        || "${AIRPLANES_BUILD_MODE:-}" == "yes" ]]
}

airplanes_webconfig_parse_mode_args() {
    local arg
    for arg in "$@"; do
        case "$arg" in
            --build-mode)
                AIRPLANES_BUILD_MODE=1
                export AIRPLANES_BUILD_MODE
                ;;
            --runtime)
                AIRPLANES_BUILD_MODE=0
                export AIRPLANES_BUILD_MODE
                ;;
        esac
    done
}

airplanes_webconfig_target_root() {
    if airplanes_webconfig_is_build_mode; then
        printf '%s' "${ROOTFS_DIR:?ROOTFS_DIR must be set in build mode}"
        return 0
    fi
    # AIRPLANES_ROOT="/" → ${AIRPLANES_ROOT%/} → "" (empty target_root, callers
    # treat empty as /). Anything else gets its trailing slash stripped so the
    # caller can confidently $TARGET_ROOT/usr/local/bin/... without "//".
    printf '%s' "${AIRPLANES_ROOT%/}"
}

# ---------------------------------------------------------------------------
# Arch detection
# ---------------------------------------------------------------------------
#
# Build mode honors pi-gen's ${ARCH} enum (arm64, armhf, etc.).
# Runtime mode reads uname -m. armv6l is rejected — the published binaries
# are GOARM=7 and would crash with SIGILL on a Pi Zero W1.

airplanes_webconfig_detect_arch() {
    if airplanes_webconfig_is_build_mode; then
        case "${ARCH:-}" in
            arm64|armhf) printf '%s' "${ARCH}"; return 0 ;;
            "") echo "ERROR: ARCH must be set by pi-gen in build mode" >&2; return 1 ;;
            *) echo "ERROR: unsupported build-mode ARCH='${ARCH}' (expected arm64 or armhf)" >&2; return 1 ;;
        esac
    fi

    case "$(uname -m)" in
        aarch64)        printf '%s' "arm64" ;;
        armv7l)         printf '%s' "armhf" ;;
        armv6l)
            echo "ERROR: armv6l is not supported." >&2
            echo "       Published binaries target GOARM=7. The Pi Zero W1 cannot run them." >&2
            echo "       Pi Zero 2 W is aarch64 (arm64) and is supported." >&2
            return 1
            ;;
        *)
            echo "ERROR: unsupported architecture '$(uname -m)'" >&2
            return 1
            ;;
    esac
}

# ---------------------------------------------------------------------------
# Channel resolution
# ---------------------------------------------------------------------------
#
# Build mode reads AIRPLANES_WEBCONFIG_BRANCH directly. The caller (pi-gen
# stage-05) pinned the desired release; do not consult /etc/airplanes/release-channel
# because stage 06 writes that file AFTER stage 05 runs.
#
# Runtime mode reads /etc/airplanes/release-channel. Allowlist: stable, dev, main
# (main is a legacy alias for stable, kept consistent with feed's contract).

airplanes_webconfig_resolve_channel() {
    if airplanes_webconfig_is_build_mode; then
        if [[ -z "${AIRPLANES_WEBCONFIG_BRANCH:-}" ]]; then
            echo "ERROR: AIRPLANES_WEBCONFIG_BRANCH must be set in build mode (concrete tag for stable, branch for dev)" >&2
            return 1
        fi
        printf '%s' "${AIRPLANES_WEBCONFIG_BRANCH}"
        return 0
    fi

    local channel_file="${AIRPLANES_ROOT%/}/etc/airplanes/release-channel"
    if [[ ! -r "$channel_file" ]]; then
        printf '%s' "stable"
        return 0
    fi
    local channel
    channel="$(head -n1 "$channel_file" | tr -d '[:space:]')"
    case "$channel" in
        stable|main) printf '%s' "stable" ;;
        dev)         printf '%s' "dev" ;;
        *)
            echo "ERROR: $channel_file contains '$channel' (expected: stable, dev, main)" >&2
            return 1
            ;;
    esac
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

    if ! ( cd "$dest_dir" && sha256sum -c --ignore-missing SHA256SUMS ); then
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

    if airplanes_webconfig_is_build_mode; then
        install -m 0755 "$src" "$dest"
        return 0
    fi

    # Runtime: keep the previous binary as .prev for rollback; the in-place
    # rename two-step avoids ETXTBSY when overwriting a running ELF.
    if [[ -x "$dest" ]]; then
        cp -a "$dest" "${dest}.prev"
    fi
    cp "$src" "${dest}.new"
    chmod 0755 "${dest}.new"
    mv -f "${dest}.new" "$dest"
}

airplanes_webconfig_install_manifest() {
    local src="$1" target_root="$2"
    local dest_dir="$target_root/etc/airplanes"
    install -d -m 0755 "$dest_dir"
    install -m 0644 "$src" "$dest_dir/webconfig-release.json"
}
