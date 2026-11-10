#!/usr/bin/env bash

set -euo pipefail

# The registry, next to this script in the postgres-provision image.
config_file=${LIFE_OS_DATABASE_CONFIG:-/opt/lifeos/postgres/service-databases.conf}
# The server: PGHOST in the postgres-provision image, else this host.
host=${PGHOST:-127.0.0.1}
declare -a services databases roles password_variables

while IFS='|' read -r service database role password_env extra; do
  [[ -z "$service" || "$service" == \#* ]] && continue
  if [[ -n "${extra:-}" || -z "$database" || -z "$role" || -z "$password_env" ]]; then
    printf 'invalid database config row for service %s\n' "$service" >&2
    exit 1
  fi
  services+=("$service")
  databases+=("$database")
  roles+=("$role")
  password_variables+=("$password_env")
done < "$config_file"

for i in "${!services[@]}"; do
  password=${!password_variables[$i]:-}
  if [[ -z "$password" ]]; then
    printf 'missing password for %s (%s)\n' "${services[$i]}" "${password_variables[$i]}" >&2
    exit 1
  fi

  PGPASSWORD=$password psql \
    --host "$host" \
    --username "${roles[$i]}" \
    --dbname "${databases[$i]}" \
    --no-psqlrc \
    --tuples-only \
    --no-align \
    --command 'SELECT 1' | grep -qx 1

  for j in "${!databases[@]}"; do
    [[ $i == "$j" ]] && continue
    if PGPASSWORD=$password psql \
      --host "$host" \
      --username "${roles[$i]}" \
      --dbname "${databases[$j]}" \
      --no-psqlrc \
      --command 'SELECT 1' >/dev/null 2>&1; then
      printf '%s unexpectedly accessed %s\n' "${roles[$i]}" "${databases[$j]}" >&2
      exit 1
    fi
  done

  for protected_database in "$POSTGRES_DB" template1; do
    if PGPASSWORD=$password psql \
      --host "$host" \
      --username "${roles[$i]}" \
      --dbname "$protected_database" \
      --no-psqlrc \
      --command 'SELECT 1' >/dev/null 2>&1; then
      printf '%s unexpectedly accessed %s\n' "${roles[$i]}" "$protected_database" >&2
      exit 1
    fi
  done
done

printf 'database isolation verified for %d services\n' "${#services[@]}"

