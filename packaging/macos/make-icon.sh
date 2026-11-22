#!/bin/sh
# Rebuilds the icon of the app the installer adds to Applications on macOS,
# from the WebUI's 512-pixel logo. Run it on a Mac after the logo changes and
# commit the result.
set -eu

root="$(cd "$(dirname "$0")/../.." && pwd)"
src="$root/web/frontend/public/web-app-manifest-512x512.png"
out="$root/internal/install/macapp/AppIcon.icns"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
iconset="$tmp/AppIcon.iconset"
mkdir "$iconset"

for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$src" --out "$iconset/icon_${size}x${size}.png" >/dev/null
done
for size in 16 32 128 256; do
    double=$((size * 2))
    sips -z "$double" "$double" "$src" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done

mkdir -p "$(dirname "$out")"
iconutil -c icns "$iconset" -o "$out"
