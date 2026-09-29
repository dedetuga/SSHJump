#!/usr/bin/env bash
# Cross-compile the sshjump backend and package the .eap, with no Docker and no
# network (dependencies are vendored under app/vendor).
#
#   ./build.sh              # build both aarch64 and armv7hf
#   ./build.sh aarch64      # build one arch
#
# Requires: Go >= 1.21.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT/app"

build_one() {
  local arch="$1" goarch goarm=""
  case "$arch" in
    aarch64) goarch=arm64 ;;
    armv7hf) goarch=arm; goarm=7 ;;
    *) echo "unknown arch: $arch (use aarch64 or armv7hf)"; exit 1 ;;
  esac
  echo ">> building $arch"
  CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" GOARM="$goarm" GOFLAGS=-mod=vendor GOPROXY=off \
    go build -ldflags="-s -w" -o "/tmp/sshjump-$arch" .
  "$ROOT/pack-eap.sh" "$arch" "/tmp/sshjump-$arch"
}

if [ $# -ge 1 ]; then
  build_one "$1"
else
  build_one aarch64
  build_one armv7hf
fi
