#!/usr/bin/env bash
# Assemble a full release directory: cross-compiled binaries, rootfs tarball,
# manifest, and SHA256SUMS. Single source of truth used by:
#   * .github/workflows/release.yml — real publishes
#   * test fixtures that stage synthetic releases at file:// URLs
#
# Output is byte-deterministic given the same inputs (commit_sha, version,
# kind, tree contents). build_date is the only non-deterministic field
# unless --build-date is passed.
#
# Usage:
#   scripts/lib/build-release.sh --version <v> --output <dir> \
#       [--commit-sha <sha>]   default: git -C <source> rev-parse HEAD
#       [--kind stable|dev]    default: stable
#       [--arch arm64|armhf|all]   default: all
#       [--source <dir>]       default: repo root (script_dir/../..)
#       [--build-date <iso8601>]   default: current UTC

set -euo pipefail

VERSION=""
OUTPUT=""
COMMIT_SHA=""
KIND="stable"
ARCH_ARG="all"
SOURCE=""
BUILD_DATE=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)    VERSION="$2"; shift 2 ;;
        --output)     OUTPUT="$2"; shift 2 ;;
        --commit-sha) COMMIT_SHA="$2"; shift 2 ;;
        --kind)       KIND="$2"; shift 2 ;;
        --arch)       ARCH_ARG="$2"; shift 2 ;;
        --source)     SOURCE="$2"; shift 2 ;;
        --build-date) BUILD_DATE="$2"; shift 2 ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'
            exit 0
            ;;
        *)
            echo "ERROR: unknown argument: $1" >&2
            exit 2
            ;;
    esac
done

[[ -n "$VERSION" ]] || { echo "ERROR: --version is required" >&2; exit 2; }
[[ -n "$OUTPUT" ]]  || { echo "ERROR: --output is required" >&2; exit 2; }

case "$KIND" in
    stable|dev) ;;
    *) echo "ERROR: --kind must be stable or dev (got '$KIND')" >&2; exit 2 ;;
esac

case "$ARCH_ARG" in
    arm64)  ARCHES=(arm64) ;;
    armhf)  ARCHES=(armhf) ;;
    all)    ARCHES=(arm64 armhf) ;;
    *) echo "ERROR: --arch must be arm64, armhf, or all (got '$ARCH_ARG')" >&2; exit 2 ;;
esac

if [[ -z "$SOURCE" ]]; then
    SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
    SOURCE="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
fi
[[ -f "$SOURCE/go.mod" ]] || {
    echo "ERROR: --source '$SOURCE' does not look like an image-webconfig checkout (no go.mod)" >&2
    exit 2
}
[[ -d "$SOURCE/files" ]] || {
    echo "ERROR: --source '$SOURCE' has no files/ tree" >&2
    exit 2
}

command -v go >/dev/null || { echo "ERROR: go is not on PATH" >&2; exit 2; }
command -v sha256sum >/dev/null || { echo "ERROR: sha256sum is not on PATH" >&2; exit 2; }

if [[ -z "$COMMIT_SHA" ]]; then
    COMMIT_SHA="$(git -C "$SOURCE" rev-parse HEAD)"
fi
if [[ -z "$BUILD_DATE" ]]; then
    BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
fi

OUTPUT="$(mkdir -p "$OUTPUT" && cd -- "$OUTPUT" && pwd)"
STAGING="$OUTPUT/.staging"
rm -rf "$STAGING"
mkdir -p "$STAGING/files"

echo "build-release: version=$VERSION kind=$KIND commit=$COMMIT_SHA arches=${ARCHES[*]} output=$OUTPUT"

# 1. Cross-compile binaries.
for arch in "${ARCHES[@]}"; do
    case "$arch" in
        arm64) GOARCH=arm64; GOARM="" ;;
        armhf) GOARCH=arm;   GOARM=7 ;;
    esac
    echo "build-release: go build $arch"
    (
        cd "$SOURCE"
        GOOS=linux GOARCH="$GOARCH" GOARM="$GOARM" CGO_ENABLED=0 \
            go build -trimpath -buildvcs=false \
                -ldflags "-s -w -X main.version=$VERSION -X main.commitSha=$COMMIT_SHA" \
                -o "$OUTPUT/airplanes-webconfig-$arch" \
                ./cmd/webconfig
        file "$OUTPUT/airplanes-webconfig-$arch" || true
    )
done

# 2. Stage files/ tree into a writable copy (so chmod / extra-files don't
#    mutate the source tree — important for tests that build many releases
#    sequentially from one checkout).
echo "build-release: stage files/ tree"
cp -a "$SOURCE/files/." "$STAGING/files/"

# 3. Ship install.sh, update.sh, and the lib inside the rootfs payload so
#    extraction lands them at /usr/local/share/airplanes-webconfig/. The
#    on-device self-update helper invokes the installed copy, so each
#    release carries the installer needed to apply the NEXT release.
install -d -m 0755 "$STAGING/files/usr/local/share/airplanes-webconfig"
install -d -m 0755 "$STAGING/files/usr/local/share/airplanes-webconfig/scripts/lib"
install -m 0755 "$SOURCE/install.sh" "$STAGING/files/usr/local/share/airplanes-webconfig/install.sh"
install -m 0755 "$SOURCE/update.sh"  "$STAGING/files/usr/local/share/airplanes-webconfig/update.sh"
install -m 0644 "$SOURCE/scripts/lib/install-common.sh" \
    "$STAGING/files/usr/local/share/airplanes-webconfig/scripts/lib/install-common.sh"

# 4. sudo refuses to load a sudoers file whose mode is not 0440. Encode the
#    right mode in the tarball so on-device extraction lands it ready-to-use.
if compgen -G "$STAGING/files/etc/sudoers.d/*" >/dev/null; then
    chmod 0440 "$STAGING/files/etc/sudoers.d/"*
fi

# 5. Tar with deterministic owner/group/mtime/sort. --owner/--group/--numeric-owner
#    zero-pads filesystem metadata; --mtime pins timestamps; --sort=name makes
#    archive order independent of inode order from the cp -a above.
echo "build-release: tar rootfs.tar.gz"
tar -C "$STAGING/files" \
    --owner=0 --group=0 --numeric-owner \
    --mtime='2024-01-01 00:00:00 UTC' \
    --sort=name \
    -czf "$OUTPUT/rootfs.tar.gz" .

# 6. Manifest.
echo "build-release: manifest.json"
arches_json="["
for i in "${!ARCHES[@]}"; do
    [[ $i -gt 0 ]] && arches_json+=","
    arches_json+="\"${ARCHES[$i]}\""
done
arches_json+="]"
cat > "$OUTPUT/manifest.json" <<MANIFEST
{
  "version": "$VERSION",
  "kind": "$KIND",
  "commit_sha": "$COMMIT_SHA",
  "build_date": "$BUILD_DATE",
  "arches": $arches_json
}
MANIFEST

# 7. SHA256SUMS over the release-facing assets only (not the staging dir).
echo "build-release: SHA256SUMS"
(
    cd "$OUTPUT"
    {
        for arch in "${ARCHES[@]}"; do
            sha256sum "airplanes-webconfig-$arch"
        done
        sha256sum rootfs.tar.gz
        sha256sum manifest.json
    } > SHA256SUMS
    sha256sum -c SHA256SUMS >/dev/null
)

rm -rf "$STAGING"

echo "build-release: done"
ls -la "$OUTPUT"
