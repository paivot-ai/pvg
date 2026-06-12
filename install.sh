#!/bin/sh
# Paivot bootstrap: install the channel-pinned pvg binary, then hand off to
# `pvg setup` for the rest of the toolchain (nd, vlt, skills, plugins).
#
#   curl -fsSL https://raw.githubusercontent.com/paivot-ai/pvg/main/install.sh | sh
#
# Idempotent: safe to re-run. Honors GITHUB_TOKEN for all GitHub fetches.
set -eu

CHANNEL_MANIFEST_URL="https://raw.githubusercontent.com/paivot-ai/paivot-graph/main/channel/stable.json"
PVG_REPO="paivot-ai/pvg"

log() { printf '%s\n' "$*"; }
fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# fetch URL DEST -- download with curl or wget, sending GITHUB_TOKEN if set.
fetch() {
    url=$1
    dest=$2
    if command -v curl >/dev/null 2>&1; then
        if [ -n "${GITHUB_TOKEN:-}" ]; then
            curl -fsSL -H "Authorization: Bearer ${GITHUB_TOKEN}" -o "$dest" "$url"
        else
            curl -fsSL -o "$dest" "$url"
        fi
    elif command -v wget >/dev/null 2>&1; then
        if [ -n "${GITHUB_TOKEN:-}" ]; then
            wget -q --header="Authorization: Bearer ${GITHUB_TOKEN}" -O "$dest" "$url"
        else
            wget -q -O "$dest" "$url"
        fi
    else
        fail "need curl or wget"
    fi
}

# --- platform detection ---------------------------------------------------
case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported OS: $(uname -s) (linux and darwin only)" ;;
esac
case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
esac

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT INT TERM

# --- resolve the pinned pvg version ---------------------------------------
tag=""
if fetch "$CHANNEL_MANIFEST_URL" "$tmpdir/stable.json" 2>/dev/null; then
    # Extract tools.pvg.version without a JSON parser: flatten whitespace,
    # isolate the pvg object, then pull its version field.
    tag=$(tr -d ' \t\n\r' <"$tmpdir/stable.json" \
        | sed -n 's/.*"pvg":{\([^}]*\)}.*/\1/p' \
        | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
fi
if [ -z "$tag" ]; then
    log "channel manifest unavailable; falling back to the latest pvg release"
    fetch "https://api.github.com/repos/${PVG_REPO}/releases/latest" "$tmpdir/latest.json" \
        || fail "cannot reach the channel manifest or the GitHub API"
    tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmpdir/latest.json" | head -n 1)
fi
[ -n "$tag" ] || fail "could not determine a pvg version"
version=${tag#v}

# --- skip the download when pvg is already at the pinned version ----------
existing=$(command -v pvg 2>/dev/null || true)
if [ -n "$existing" ]; then
    installed=$("$existing" version 2>/dev/null | sed -n 's/^pvg[[:space:]]*v\{0,1\}\([0-9][^ ]*\).*/\1/p')
    if [ "$installed" = "$version" ]; then
        log "pvg $tag already installed at $existing"
        exec "$existing" setup
    fi
fi

# --- download and verify ---------------------------------------------------
asset="pvg_${version}_${os}_${arch}.tar.gz"
base="https://github.com/${PVG_REPO}/releases/download/${tag}"
log "downloading pvg $tag ($os/$arch)"
fetch "$base/$asset" "$tmpdir/$asset" || fail "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmpdir/checksums.txt" || fail "download failed: $base/checksums.txt"

expected=$(awk -v a="$asset" '$2 == a { print $1 }' "$tmpdir/checksums.txt")
[ -n "$expected" ] || fail "no checksum entry for $asset"
if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "$tmpdir/$asset" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$tmpdir/$asset" | awk '{ print $1 }')
else
    fail "need sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || fail "SHA256 mismatch for $asset: expected $expected, got $actual"

tar xzf "$tmpdir/$asset" -C "$tmpdir" pvg
[ -f "$tmpdir/pvg" ] || fail "pvg binary not found in $asset"
chmod 0755 "$tmpdir/pvg"

# --- choose the install dir (hybrid rule) ----------------------------------
# In place when already installed; else /usr/local/bin when writable or
# passwordless sudo works; else ~/.local/bin.
sudo_ok() { command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; }
writable() { [ -d "$1" ] && [ -w "$1" ]; }

use_sudo=0
if [ -n "$existing" ]; then
    dest_dir=$(dirname "$existing")
    if ! writable "$dest_dir"; then
        sudo_ok || fail "$dest_dir is not writable and passwordless sudo is unavailable"
        use_sudo=1
    fi
elif writable /usr/local/bin; then
    dest_dir=/usr/local/bin
elif sudo_ok; then
    dest_dir=/usr/local/bin
    use_sudo=1
else
    dest_dir="$HOME/.local/bin"
    mkdir -p "$dest_dir"
fi

# Stage inside the destination dir so the final rename is atomic.
staged="$dest_dir/.pvg.install-$$"
if [ "$use_sudo" = 1 ]; then
    sudo -n mkdir -p "$dest_dir"
    sudo -n cp "$tmpdir/pvg" "$staged"
    sudo -n chmod 0755 "$staged"
    sudo -n mv -f "$staged" "$dest_dir/pvg"
else
    cp "$tmpdir/pvg" "$staged"
    mv -f "$staged" "$dest_dir/pvg"
fi
log "pvg $tag installed to $dest_dir/pvg"

# --- hand off to pvg setup --------------------------------------------------
exec "$dest_dir/pvg" setup
