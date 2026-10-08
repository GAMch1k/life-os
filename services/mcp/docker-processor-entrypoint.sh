#!/bin/sh
set -eu

# /run/lifeos-codex-source is a host bind mount and must remain read-only.
# Copying to tmpfs gives Codex its normal writable runtime directory without
# persisting the authenticated session in an image or Docker volume.
if [ ! -f /run/lifeos-codex-source/auth.json ]; then
  echo "missing Codex authentication at /run/lifeos-codex-source/auth.json" >&2
  exit 78
fi
mkdir -p "$CODEX_HOME"
cp -a /run/lifeos-codex-source/. "$CODEX_HOME/"
exec /processor --serve
