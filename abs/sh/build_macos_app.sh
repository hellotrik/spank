#!/usr/bin/env bash
# Build native spank binary and assemble dist/Spank.app (macOS only; needs iconutil + sips for .icns).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"
VERSION="${VERSION:-dev}"
mkdir -p dist
BIN="dist/spank_macos_app_staging"
go build -ldflags "-s -w -X main.version=${VERSION}" -o "${BIN}" .
bash packaging/macos/bundle.sh "${BIN}" "${VERSION}"
rm -f "${BIN}"
echo "abs/sh/build_macos_app.sh: done → dist/Spank.app"
