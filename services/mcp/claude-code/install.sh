#!/bin/sh
# Installs Claude Code's native binary into the directory $1, in a Node.js
# (Alpine) build stage. This directory is the one place that says which Claude
# Code the processor gets: the official npm package @anthropic-ai/claude-code at
# the version and sha512 integrity package-lock.json pins (package.json names
# the version). npm ci refuses a download whose integrity differs. The
# package's own postinstall (Anthropic's install.cjs) then puts the native
# binary for this platform (the musl build on Alpine), from its pinned platform
# package, in place of the package's bin. That binary, unmodified, is all that
# goes on into the processor image: it needs no Node.js and no npm.
#
# Used by services/mcp/Dockerfile (a source build whose LIFE_OS_AI_PROVIDERS
# names claude) and by ./setup.sh claude on a release install
# (services/mcp/claude-code/Dockerfile, which tools/build-install-bundle writes
# into the release's setup.sh). Life OS's published images never contain it.
set -eu

out=$1
cd "$(dirname "$0")"
version=$(sed -n 's/^ *"@anthropic-ai\/claude-code": *"\([^"]*\)".*/\1/p' package.json)
[ -n "$version" ] || { echo "install.sh: package.json names no @anthropic-ai/claude-code version" >&2; exit 1; }

npm ci --no-audit --no-fund --no-update-notifier --loglevel=warn

case $(uname -m) in
  x86_64) platform=linux-x64-musl ;;
  aarch64) platform=linux-arm64-musl ;;
  *) echo "install.sh: Claude Code has no Alpine build for $(uname -m)" >&2; exit 1 ;;
esac
binary=$(readlink -f node_modules/.bin/claude)
# The binary is the pinned platform package's own file, byte for byte.
cmp "$binary" "node_modules/@anthropic-ai/claude-code-$platform/claude"
home=$(mktemp -d)
reported=$(HOME=$home "$binary" --version)
rm -rf "$home"
case $reported in
  "$version" | "$version "*) ;;
  *) echo "install.sh: Claude Code reports '$reported', not the pinned $version" >&2; exit 1 ;;
esac
mkdir -p "$out"
cp "$binary" "$out/claude"
chmod 0755 "$out/claude"
echo "Claude Code $version ($platform) installed"
