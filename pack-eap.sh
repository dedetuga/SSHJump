#!/usr/bin/env bash
# Manual .eap packager for the sshjump ACAP.
#
# An .eap is a gzip-compressed tar of the app files at the archive root, plus
# the metadata the AXIS OS installer reads (manifest.json is authoritative on
# AXIS OS 10+; package.conf/param.conf are included for compatibility).
#
# Usage: ./pack-eap.sh <aarch64|armv7hf> <path-to-prebuilt-binary>
set -euo pepipe 2>/dev/null || set -euo pipefail

ARCH="${1:?arch: aarch64 or armv7hf}"
BIN="${2:?path to prebuilt sshjump binary for this arch}"
APPNAME="sshjump"
FRIENDLY="SSH Jump"
VER="1.0.0"
IFS=. read -r MAJ MIN MIC <<<"$VER"

ROOT="$(cd "$(dirname "$0")" && pwd)"
APPDIR="$ROOT/app"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

# --- stage the files -------------------------------------------------------
cp "$APPDIR/manifest.json" "$STAGE/manifest.json"
cp "$BIN"                  "$STAGE/$APPNAME"
chmod 0755 "$STAGE/$APPNAME"
cp -r "$APPDIR/html"       "$STAGE/html"

# LICENSE (full application + third-party licenses).
if [ -f "$APPDIR/LICENSE" ]; then
  cp "$APPDIR/LICENSE" "$STAGE/LICENSE"
else
  echo "SSH Jump ACAP — provided as-is. No warranty." > "$STAGE/LICENSE"
fi

# param.conf: no runtime parameters.
: > "$STAGE/param.conf"

# package.conf: legacy metadata mirror of manifest.json.
cat > "$STAGE/package.conf" <<EOF
PACKAGENAME="$FRIENDLY"
MENUNAME="$FRIENDLY"
APPTYPE="$ARCH"
APPNAME="$APPNAME"
APPID=""
LICENSENAME="Available"
LICENSEPAGE="none"
VENDOR="Filipe Brigido 2026"
REQEMBDEVVERSION="3.0"
APPMAJORVERSION="$MAJ"
APPMINORVERSION="$MIN"
APPMICROVERSION="$MIC"
APPGRP="sdk"
APPUSR="sdk"
APPOPTS=""
OTHERFILES="html LICENSE"
SETTINGSPAGEFILE="index.html"
SETTINGSPAGETEXT=""
VENDORHOMEPAGELINK=""
PREUPGRADESCRIPT=""
POSTINSTALLSCRIPT=""
STARTMODE="once"
HTTPCGIPATHS=""
EOF
cp "$STAGE/package.conf" "$STAGE/package.conf.orig"

# --- build the archive -----------------------------------------------------
OUT="$ROOT/${APPNAME}_${MAJ}_${MIN}_${MIC}_${ARCH}.eap"
# reproducible-ish, files at root, gzip
tar --numeric-owner --owner=0 --group=0 \
    -C "$STAGE" -czf "$OUT" \
    manifest.json package.conf package.conf.orig param.conf LICENSE "$APPNAME" html

echo "built: $OUT"
tar tzf "$OUT" | sed 's/^/  /'
