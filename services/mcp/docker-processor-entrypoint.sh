#!/bin/sh
set -eu

# A run must never bill the Anthropic API or reach another endpoint by
# accident; the processor strips these from every run too. An API key the
# owner chose in setup arrives as LIFE_OS_ANTHROPIC_API_KEY, and the processor
# hands it to the Claude Code runs alone.
unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL

# The provider as the processor reads it: case and surrounding spaces do not
# matter.
provider=$(printf '%s' "${LIFE_OS_AI_PROVIDER:-codex}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')

# The image has only the CLIs its LIFE_OS_AI_PROVIDERS build argument named
# (the published image: Codex alone; ./setup.sh claude adds Claude Code on the
# owner's server): refuse a provider whose CLI it lacks, rather than fail
# every run.
require_cli() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "LIFE_OS_AI_PROVIDER=$2, but this image has no $1 CLI. Release files: run ./setup.sh $2. Source build: add $2 to LIFE_OS_AI_PROVIDERS in .env and rebuild (docker compose build processor)" >&2
    exit 78
  fi
}

# Codex refreshes its sign-in in the tmpfs copy, and a refresh can use up
# the refresh token the mounted copy still holds, so that a restart would
# start signed out. With LIFE_OS_CODEX_AUTH_WRITE_BACK=on and the mount
# writable (the release bundle's compose.yaml, where the Codex home is Life
# OS's own), a changed auth.json goes back to the mount within
# LIFE_OS_CODEX_AUTH_WRITE_BACK_SECONDS (60). Only auth.json; only while the
# mounted copy is still the one this container started from or last wrote
# (a new sign-in there wins, until a restart reads it); and only a file that
# still holds a refresh token. Off by default: the owner's server mounts
# their own Codex home read-only. Codex only: Claude Code's token comes from
# the environment and is never refreshed.
codex_auth_write_back() {
  # A probe file, as test -w does not see a read-only mount.
  if (: >"$source_dir/.lifeos-write-probe") 2>/dev/null && rm -f "$source_dir/.lifeos-write-probe"; then
    (
      synced=$(sha256sum <"$source_dir/auth.json")
      stopped=
      while :; do
        sleep "${LIFE_OS_CODEX_AUTH_WRITE_BACK_SECONDS:-60}"
        [ -z "$stopped" ] || continue
        current=$(sha256sum <"$CODEX_HOME/auth.json" 2>/dev/null) || continue
        [ "$current" != "$synced" ] || continue
        grep -q '"refresh_token"' "$CODEX_HOME/auth.json" || continue
        if [ "$(sha256sum <"$source_dir/auth.json" 2>/dev/null || true)" != "$synced" ]; then
          echo "codex sign-in: $source_dir/auth.json changed outside Life OS; keeping it, and no longer writing back until a restart" >&2
          stopped=1
          continue
        fi
        if (umask 077 && cp "$CODEX_HOME/auth.json" "$source_dir/.auth.json.lifeos") &&
          mv -f "$source_dir/.auth.json.lifeos" "$source_dir/auth.json"; then
          synced=$current
          echo "codex sign-in: wrote the refreshed auth.json back to $source_dir" >&2
        else
          echo "codex sign-in: could not write auth.json back to $source_dir; no longer trying until a restart" >&2
          stopped=1
        fi
      done
    ) &
  else
    echo "codex sign-in: LIFE_OS_CODEX_AUTH_WRITE_BACK=on, but $source_dir is read-only; a refreshed sign-in stays in this container" >&2
  fi
}

case "$provider" in
codex)
  require_cli "${LIFE_OS_CODEX_COMMAND:-codex}" codex
  # /run/lifeos-codex-source is a host bind mount, read-only on the owner's
  # server. Copying only the credentials and optional configuration to tmpfs
  # gives Codex a writable runtime directory without persisting a host profile
  # (its sessions and history) in an image or Docker volume.
  source_dir=/run/lifeos-codex-source
  if [ ! -f "$source_dir/auth.json" ]; then
    echo "missing Codex authentication at $source_dir/auth.json: set LIFE_OS_CODEX_HOME_HOST_PATH in .env to your Codex home (or use LIFE_OS_AI_PROVIDER=claude)" >&2
    exit 78
  fi
  mkdir -p "$CODEX_HOME"
  cp "$source_dir/auth.json" "$CODEX_HOME/auth.json"
  if [ -f "$source_dir/config.toml" ]; then
    cp "$source_dir/config.toml" "$CODEX_HOME/config.toml"
  fi
  if [ "${LIFE_OS_CODEX_AUTH_WRITE_BACK:-off}" = on ]; then
    codex_auth_write_back
  fi
  ;;
claude)
  require_cli "${LIFE_OS_CLAUDE_COMMAND:-claude}" claude
  # One sign-in, as the owner chose in setup: the Claude plan's long-lived
  # OAuth token (claude setup-token), or an Anthropic API key, billed per use.
  # Never both. Claude Code keeps its state in a tmpfs home.
  if [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -n "${LIFE_OS_ANTHROPIC_API_KEY:-}" ]; then
    echo "both LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN (your Claude plan) and LIFE_OS_ANTHROPIC_API_KEY (an Anthropic API key) are set in .env: keep only the one you chose (./setup.sh claude)" >&2
    exit 78
  fi
  if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && [ -z "${LIFE_OS_ANTHROPIC_API_KEY:-}" ]; then
    echo "missing Claude Code's sign-in: run ./setup.sh claude, or set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN (the output of claude setup-token) or LIFE_OS_ANTHROPIC_API_KEY in .env" >&2
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
