#!/bin/sh
set -eu

# /run/lifeos-codex-source is a host bind mount, read-only on the owner's
# server. Copying only the credentials and optional configuration to tmpfs
# gives Codex a writable runtime directory without persisting a host profile
# (its sessions and history) in an image or Docker volume.
source_dir=/run/lifeos-codex-source
if [ ! -f "$source_dir/auth.json" ]; then
  echo "missing Codex authentication at $source_dir/auth.json" >&2
  exit 78
fi
mkdir -p "$CODEX_HOME"
cp "$source_dir/auth.json" "$CODEX_HOME/auth.json"
if [ -f "$source_dir/config.toml" ]; then
  cp "$source_dir/config.toml" "$CODEX_HOME/config.toml"
fi

# Codex refreshes its sign-in in the tmpfs copy, and a refresh can use up
# the refresh token the mounted copy still holds, so that a restart would
# start signed out. With LIFE_OS_CODEX_AUTH_WRITE_BACK=on and the mount
# writable (the release bundle's compose.yaml, where the Codex home is Life
# OS's own), a changed auth.json goes back to the mount within
# LIFE_OS_CODEX_AUTH_WRITE_BACK_SECONDS (60). Only auth.json; only while the
# mounted copy is still the one this container started from or last wrote
# (a new sign-in there wins, until a restart reads it); and only a file that
# still holds a refresh token. Off by default: the owner's server mounts
# their own Codex home read-only.
if [ "${LIFE_OS_CODEX_AUTH_WRITE_BACK:-off}" = on ]; then
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
fi
exec /processor --serve
