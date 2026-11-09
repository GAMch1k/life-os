#!/bin/sh
set -eu

# A run must never bill the Anthropic API or reach another endpoint instead of
# the owner's subscription; the processor strips these from every run too.
unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL

# The image installs only the CLIs its LIFE_OS_AI_PROVIDERS build argument
# named: refuse a provider whose CLI it lacks, rather than fail every run.
require_cli() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "LIFE_OS_AI_PROVIDER=$2, but this image has no $1 CLI: rebuild it with $2 in LIFE_OS_AI_PROVIDERS (docker compose build processor)" >&2
    exit 78
  fi
}

case "${LIFE_OS_AI_PROVIDER:-codex}" in
codex)
  require_cli "${LIFE_OS_CODEX_COMMAND:-codex}" codex
  # /run/lifeos-codex-source is a host bind mount and must remain read-only.
  # Copying only the credentials and optional configuration to tmpfs gives
  # Codex a writable runtime directory without persisting a host profile in an
  # image or Docker volume.
  if [ ! -f /run/lifeos-codex-source/auth.json ]; then
    echo "missing Codex authentication at /run/lifeos-codex-source/auth.json: set LIFE_OS_CODEX_HOME_HOST_PATH in .env to your Codex home (or use LIFE_OS_AI_PROVIDER=claude)" >&2
    exit 78
  fi
  mkdir -p "$CODEX_HOME"
  cp /run/lifeos-codex-source/auth.json "$CODEX_HOME/auth.json"
  if [ -f /run/lifeos-codex-source/config.toml ]; then
    cp /run/lifeos-codex-source/config.toml "$CODEX_HOME/config.toml"
  fi
  ;;
claude)
  require_cli "${LIFE_OS_CLAUDE_COMMAND:-claude}" claude
  # The subscription's long-lived OAuth token (claude setup-token) arrives in
  # the environment. Claude Code keeps its state in a tmpfs home.
  if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
    echo "missing the Claude subscription token: set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN in .env to the output of claude setup-token" >&2
    exit 78
  fi
  HOME=/tmp/lifeos-claude
  CLAUDE_CONFIG_DIR=$HOME/.claude
  export HOME CLAUDE_CONFIG_DIR
  mkdir -p "$CLAUDE_CONFIG_DIR"
  ;;
*)
  echo "LIFE_OS_AI_PROVIDER must be codex or claude" >&2
  exit 78
  ;;
esac
exec /processor --serve
