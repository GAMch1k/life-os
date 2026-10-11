#!/usr/bin/env bash

# Provisions lifeos_backup, the backup service's login (docs/services/backup.md):
# it reads every table (pg_read_all_data), connects to every database in the
# registry, and may create databases (CREATEDB, for restore checks into
# scratch databases). It owns nothing and cannot change service data.
#
# Idempotent. provision-service-databases.sh runs it last, on a fresh volume
# and on every reapply, because that script revokes database grants from
# every role but the owner. On a running server, apply it on its own:
#
#   docker compose exec -T -e LIFE_OS_BACKUP_DB_PASSWORD postgres \
#     bash /opt/lifeos/postgres/provision-backup-role.sh
#
# With LIFE_OS_BACKUP_DB_PASSWORD set, the role is created if missing and its
# password always set. Without it, an existing role keeps its password and
# only its attributes and grants are reconciled; a missing role is skipped.

set -euo pipefail

# The registry: the copy in the directory-mounted /opt/lifeos/postgres when
# there is one, because a single-file bind mount (/etc/lifeos/...) keeps the
# file it was created with after Git replaces it, until the container is
# recreated. The single file is the fallback where the directory is not
# mounted.
default_config=/etc/lifeos/service-databases.conf
if [[ -r /opt/lifeos/postgres/service-databases.conf ]]; then
  default_config=/opt/lifeos/postgres/service-databases.conf
fi
config_file=${LIFE_OS_DATABASE_CONFIG:-$default_config}
role=lifeos_backup
password=${LIFE_OS_BACKUP_DB_PASSWORD:-}

if [[ ! -r "$config_file" ]]; then
  printf 'database config is not readable: %s\n' "$config_file" >&2
  exit 1
fi

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"

psql_admin=(
  psql
  --username "$POSTGRES_USER"
  --dbname "$POSTGRES_DB"
  --set ON_ERROR_STOP=1
  --no-psqlrc
  --quiet
)

printf 'Database registry for %s: %s\n' "$role" "$config_file"

role_exists=$("${psql_admin[@]}" --tuples-only --no-align --set=role_name="$role" <<'SQL'
SELECT count(*) FROM pg_roles WHERE rolname = :'role_name';
SQL
)

if [[ -n "$password" ]]; then
  printf 'Provisioning %s\n' "$role"
  "${psql_admin[@]}" --set=role_name="$role" --set=role_password="$password" <<'SQL'
SELECT format(
  'CREATE ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER CREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS',
  :'role_name',
  :'role_password'
)
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'role_name')
\gexec

SELECT format(
  'ALTER ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER CREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS',
  :'role_name',
  :'role_password'
)
\gexec
SQL
elif [[ "$role_exists" == 1 ]]; then
  printf 'Reconciling %s (LIFE_OS_BACKUP_DB_PASSWORD is not set: its password is kept)\n' "$role"
  "${psql_admin[@]}" --set=role_name="$role" <<'SQL'
SELECT format('ALTER ROLE %I WITH LOGIN NOSUPERUSER CREATEDB NOCREATEROLE INHERIT NOREPLICATION NOBYPASSRLS', :'role_name')
\gexec
SQL
else
  printf 'Skipping %s: set LIFE_OS_BACKUP_DB_PASSWORD in the local .env file to provision it\n' "$role" >&2
  exit 0
fi

"${psql_admin[@]}" --set=role_name="$role" <<'SQL'
SET client_min_messages = warning;
SELECT format('GRANT pg_read_all_data TO %I', :'role_name')
\gexec
SQL

while IFS='|' read -r service database _role _password_env _extra; do
  [[ -z "$service" || "$service" == \#* ]] && continue
  if [[ ! "$database" =~ ^[a-z][a-z0-9_]*$ ]]; then
    printf 'invalid database name for service %s\n' "$service" >&2
    exit 1
  fi
  "${psql_admin[@]}" --set=database_name="$database" --set=role_name="$role" <<'SQL'
SELECT format('GRANT CONNECT ON DATABASE %I TO %I', :'database_name', :'role_name')
WHERE EXISTS (SELECT FROM pg_database WHERE datname = :'database_name')
\gexec
SQL
done < "$config_file"
