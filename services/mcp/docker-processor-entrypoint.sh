#!/bin/sh
set -eu

# /run/lifeos-codex-source is a host bind mount and must remain read-only.
# Copying only the credentials and optional configuration to tmpfs gives Codex
# a writable runtime directory without persisting a host profile in an image or
# Docker volume.
if [ ! -f /run/lifeos-codex-source/auth.json ]; then
  echo "missing Codex authentication at /run/lifeos-codex-source/auth.json" >&2
  exit 78
fi
mkdir -p "$CODEX_HOME"
cp /run/lifeos-codex-source/auth.json "$CODEX_HOME/auth.json"
if [ -f /run/lifeos-codex-source/config.toml ]; then
  cp /run/lifeos-codex-source/config.toml "$CODEX_HOME/config.toml"
fi
exec /processor --serve
