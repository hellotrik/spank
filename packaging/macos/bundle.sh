#!/usr/bin/env bash
# Assembles Spank.app for macOS: binary + Info.plist + AppIcon.icns from doc/logo.png.
# Usage: bundle.sh <path-to-spank-binary> [version]
# Requires: macOS (iconutil, sips). Run from repo root or any cwd.
set -euo pipefail

BINARY="${1:?usage: bundle.sh <spank-binary> [version]}"
VERSION="${2:-dev}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
APP_NAME="Spank"
OUT_APP="${ROOT}/dist/${APP_NAME}.app"
CONTENTS="${OUT_APP}/Contents"
MACOS_DIR="${CONTENTS}/MacOS"
RESOURCES="${CONTENTS}/Resources"
LOGO="${ROOT}/doc/logo.png"

rm -rf "${OUT_APP}"
mkdir -p "${MACOS_DIR}" "${RESOURCES}"

cp -f "${BINARY}" "${MACOS_DIR}/spank"
chmod +x "${MACOS_DIR}/spank"

sed "s/VERSION_PLACEHOLDER/${VERSION}/g" "${SCRIPT_DIR}/Info.plist" > "${CONTENTS}/Info.plist"

if [[ -f "${LOGO}" ]] && command -v sips >/dev/null 2>&1 && command -v iconutil >/dev/null 2>&1; then
	ICONSET="${ROOT}/dist/AppIcon.iconset"
	rm -rf "${ICONSET}"
	mkdir -p "${ICONSET}"
	mk() { sips -z "$2" "$1" "${LOGO}" --out "$3" >/dev/null 2>&1 || sips -z "$2" "$1" "${LOGO}" --out "$3"; }
	# iconutil expects fixed filenames (1x / 2x ladder)
	mk 16 16 "${ICONSET}/icon_16x16.png"
	mk 32 32 "${ICONSET}/icon_16x16@2x.png"
	mk 32 32 "${ICONSET}/icon_32x32.png"
	mk 64 64 "${ICONSET}/icon_32x32@2x.png"
	mk 128 128 "${ICONSET}/icon_128x128.png"
	mk 256 256 "${ICONSET}/icon_128x128@2x.png"
	mk 256 256 "${ICONSET}/icon_256x256.png"
	mk 512 512 "${ICONSET}/icon_256x256@2x.png"
	mk 512 512 "${ICONSET}/icon_512x512.png"
	mk 1024 1024 "${ICONSET}/icon_512x512@2x.png"
	iconutil -c icns "${ICONSET}" -o "${RESOURCES}/AppIcon.icns"
	rm -rf "${ICONSET}"
	echo "packaging/macos: wrote ${RESOURCES}/AppIcon.icns from doc/logo.png"
else
	echo "packaging/macos: skip AppIcon.icns (need doc/logo.png + sips + iconutil)"
fi

echo "packaging/macos: built ${OUT_APP}"
