#!/usr/bin/env bash
# ClawEh One-Line Online Installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/PivotLLM/ClawEh/main/claw-online-install.sh | bash
#
# With optional arguments forwarded to `claw install`:
#   curl -fsSL https://raw.githubusercontent.com/PivotLLM/ClawEh/main/claw-online-install.sh | bash -s -- --host 0.0.0.0 --allowed-cidrs 192.168.1.0/24
#
# Environment variables:
#   CLAW_VERSION   Version/tag to install (e.g. 0.5.4); default: latest
#   CLAW_PACKAGE   Path to a local tar.gz archive (for offline/test use)
#
set -euo pipefail

REPO="PivotLLM/ClawEh"
BINARY="claw"
VERSION="${CLAW_VERSION:-latest}"

# --- detect OS ---
os="$(uname -s)"
case "$os" in
    Linux)  os="linux" ;;
    Darwin) os="darwin" ;;
    *)
        echo "Error: Unsupported OS '$os'. ClawEh supports Linux and macOS." >&2
        exit 1
        ;;
esac

# --- detect architecture ---
arch="$(uname -m)"
case "$arch" in
    x86_64 | amd64)  arch="amd64" ;;
    aarch64 | arm64) arch="arm64" ;;
    i386 | i686)
        if [ "$os" = "linux" ]; then
            arch="386"
        else
            echo "Error: 32-bit x86 is not supported on $os." >&2
            exit 1
        fi
        ;;
    *)
        echo "Error: Unsupported architecture '$arch' (supported: amd64, arm64, 386)." >&2
        exit 1
        ;;
esac

# --- choose downloader ---
if command -v curl >/dev/null 2>&1; then
    download() { curl -fSL --retry 3 -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
    download() { wget -O "$2" "$1"; }
else
    echo "Error: 'curl' or 'wget' is required to download ClawEh." >&2
    exit 1
fi

asset="claw-${os}-${arch}.tar.gz"
checksum_asset="${asset}.sha256"

if [ "$VERSION" = "latest" ]; then
    asset_url="https://github.com/${REPO}/releases/latest/download/${asset}"
    checksum_url="https://github.com/${REPO}/releases/latest/download/${checksum_asset}"
else
    clean_version="${VERSION#v}"
    asset_url="https://github.com/${REPO}/releases/download/${clean_version}/${asset}"
    checksum_url="https://github.com/${REPO}/releases/download/${clean_version}/${checksum_asset}"
fi

# --- setup temporary workspace ---
tmpdir="$(mktemp -d 2>/dev/null || mktemp -d -t claw-install)" || {
    echo "Error: Failed to create temporary directory." >&2
    exit 1
}
trap 'rm -rf "$tmpdir"' EXIT INT TERM

# --- download package and checksum ---
if [ -n "${CLAW_PACKAGE:-}" ] && [ -f "${CLAW_PACKAGE}" ]; then
    echo "==> Using specified package: ${CLAW_PACKAGE}"
    cp "${CLAW_PACKAGE}" "$tmpdir/$asset"
else
    echo "==> Downloading ClawEh for ${os}/${arch} (${VERSION})..."
    if ! download "$asset_url" "$tmpdir/$asset"; then
        echo "Error: Failed to download release asset from ${asset_url}" >&2
        echo "Please check available releases at https://github.com/${REPO}/releases" >&2
        exit 1
    fi

    # Verify SHA256 checksum if checksum file exists
    if download "$checksum_url" "$tmpdir/$checksum_asset" >/dev/null 2>&1; then
        echo "==> Verifying SHA256 checksum..."
        checksum_verified=0
        if command -v sha256sum >/dev/null 2>&1; then
            ( cd "$tmpdir" && ( sha256sum -c "$checksum_asset" --status 2>/dev/null || sha256sum -c "$checksum_asset" ) ) && checksum_verified=1
        elif command -v shasum >/dev/null 2>&1; then
            ( cd "$tmpdir" && shasum -a 256 -c "$checksum_asset" >/dev/null 2>&1 ) && checksum_verified=1
        else
            echo "Note: Neither sha256sum nor shasum is available; skipping checksum verification."
            checksum_verified=1
        fi

        if [ "$checksum_verified" -ne 1 ]; then
            echo "Error: SHA256 checksum verification failed!" >&2
            exit 1
        fi
    fi
fi

# --- extract package ---
echo "==> Extracting package..."
mkdir -p "$tmpdir/extracted"
tar -xzf "$tmpdir/$asset" -C "$tmpdir/extracted" || {
    echo "Error: Failed to extract ${asset}." >&2
    exit 1
}

# Locate claw binary in extracted directory
bin="$(find "$tmpdir/extracted" -type f \( -name "$BINARY" -o -name "${BINARY}-${os}-${arch}" \) ! -name "claw-auth" 2>/dev/null | head -n 1)"
if [ -z "$bin" ] || [ ! -f "$bin" ]; then
    echo "Error: '${BINARY}' binary not found inside extracted archive." >&2
    exit 1
fi
chmod +x "$bin"

# --- determine auto-confirm flags supported by the binary ---
install_args=()
if "$bin" install --help 2>&1 | grep -q -- "-y"; then
    install_args+=("-y")
elif "$bin" install --help 2>&1 | grep -q -- "--yes"; then
    install_args+=("--yes")
fi

# --- run installation ---
echo "==> Installing ClawEh service..."
install_log="$tmpdir/install.log"
if ! "$bin" install "${install_args[@]}" "$@" 2>&1 | tee "$install_log"; then
    echo "Error: Installation failed. Check output above for details." >&2
    exit 1
fi

# --- resolve web UI URL ---
web_url="$(grep -E "(Web interface is at:|Open:|Browse to:)" "$install_log" 2>/dev/null | grep -o 'http[s]*://[^ ]*' | tail -n 1 || true)"
if [ -z "$web_url" ]; then
    web_url="http://localhost:18790"
fi

# --- attempt to open web browser for setup wizard ---
open_browser() {
    local target_url="$1"

    # Headless Linux check: if no graphical display is available, skip browser commands
    if [ "$os" = "linux" ] && [ -z "${DISPLAY:-}" ] && [ -z "${WAYLAND_DISPLAY:-}" ]; then
        return 1
    fi

    if [ "$os" = "darwin" ] && command -v open >/dev/null 2>&1; then
        open "$target_url" >/dev/null 2>&1 && return 0
    elif command -v xdg-open >/dev/null 2>&1; then
        xdg-open "$target_url" >/dev/null 2>&1 && return 0
    elif command -v sensible-browser >/dev/null 2>&1; then
        sensible-browser "$target_url" >/dev/null 2>&1 && return 0
    elif command -v python3 >/dev/null 2>&1; then
        python3 -m webbrowser "$target_url" >/dev/null 2>&1 && return 0
    elif command -v python >/dev/null 2>&1; then
        python -m webbrowser "$target_url" >/dev/null 2>&1 && return 0
    fi
    return 1
}

echo ""
echo "==> Setup Wizard"
if open_browser "$web_url"; then
    echo "Opened setup wizard in your browser: ${web_url}"
else
    echo "Could not launch a web browser automatically (running in a terminal or headless session)."
    echo "Point your browser to the following URL to complete the setup wizard:"
    echo "  ${web_url}"
fi
