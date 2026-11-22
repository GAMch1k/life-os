#!/usr/bin/env bash

set -euo pipefail

# The registry, next to this script in the postgres-provision image.
config_file=${LIFE_OS_DATABASE_CONFIG:-/opt/lifeos/postgres/service-databases.conf}

if [[ ! -r "$config_file" ]]; then
  printf 'database config is not readable: %s\n' "$config_file" >&2
  exit 1
fi

printf 'Database registry: %s\n' "$config_file"

: "${POSTGRES_USER:?POSTGRES_USER is required}"
: "${POSTGRES_DB:?POSTGRES_DB is required}"

psql_admin=(
  psql
  --username "$POSTGRES_USER"
  --dbname "$POSTGRES_DB"
  --set ON_ERROR_STOP=1
  --no-psqlrc
)

# shellcheck source=infra/postgres/password-check.sh
. "$(dirname "${BASH_SOURCE[0]}")/password-check.sh"

# Whether a login exists: 1 or 0.
role_exists() {
  "${psql_admin[@]}" --quiet --tuples-only --no-align --set=role_name="$1" <<'SQL'
SELECT count(*) FROM pg_roles WHERE rolname = :'role_name';
SQL
}

# The admin's password (LIFE_OS_POSTGRES_PASSWORD, which compose.yaml passes
# as PGPASSWORD). PostgreSQL keeps the one it was first started with, so a
# weak one is refused only on a new install, before provisioning made any
# service's login, when starting over loses nothing; after that it is a
# warning at every start.
if [[ -n "${PGPASSWORD:-}" ]] && [[ -n "$(weak_password "$PGPASSWORD")" ]]; then
  registry_roles=$(awk -F '|' '!/^[[:space:]]*(#|$)/ { printf "%s%s", sep, $3; sep = "," }' "$config_file")
  provisioned=$("${psql_admin[@]}" --quiet --tuples-only --no-align --set=roles="$registry_roles" <<'SQL'
SELECT count(*) FROM pg_roles WHERE rolname = ANY (string_to_array(:'roles', ','));
SQL
)
  if [[ "$provisioned" == 0 ]]; then
    printf 'LIFE_OS_POSTGRES_PASSWORD %s. PostgreSQL keeps the admin password it first started with, so on this new install: set a new one in .env (openssl rand -hex 24), remove the new, empty database volume (docker compose down, then docker volume rm lifeos-postgres-data), and start again (docs/install/manual.md#database-passwords).\n' \
      "$(weak_password "$PGPASSWORD")" >&2
    exit 1
  fi
  printf 'warning: LIFE_OS_POSTGRES_PASSWORD %s. Change the admin password in PostgreSQL first, then in .env (docs/install/manual.md#database-passwords).\n' \
    "$(weak_password "$PGPASSWORD")" >&2
fi

provision_service() {
  local service=$1
  local database=$2
  local role=$3
  local password_env=$4
  local password=${!password_env:-}

  if [[ ! "$service" =~ ^[a-z][a-z0-9_-]*$ ]]; then
    printf 'invalid service name in %s: %s\n' "$config_file" "$service" >&2
    exit 1
  fi

  if [[ ! "$database" =~ ^[a-z][a-z0-9_]*$ ]] || [[ ! "$role" =~ ^[a-z][a-z0-9_]*$ ]]; then
    printf 'invalid database or role identifier for service %s\n' "$service" >&2
    exit 1
  fi

  if [[ ! "$password_env" =~ ^LIFE_OS_[A-Z0-9_]+_DB_PASSWORD$ ]] || [[ -z "$password" ]]; then
    printf 'set non-empty %s in the local .env file for service %s (compose.yaml passes it to postgres-provision)\n' "$password_env" "$service" >&2
    exit 1
  fi
  check_password "$password_env" "$password" "$role" "$(role_exists "$role")"

  printf 'Provisioning %s (%s owned by %s)\n' "$service" "$database" "$role"

  "${psql_admin[@]}" \
    --set=role_name="$role" \
    --set=role_password="$password" <<'SQL'
SELECT format(
  'CREATE ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',
  :'role_name',
  :'role_password'
)
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = :'role_name')
\gexec

SELECT format(
  'ALTER ROLE %I WITH LOGIN PASSWORD %L NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS',
  :'role_name',
  :'role_password'
)
\gexec
SQL

  "${psql_admin[@]}" \
    --set=database_name="$database" \
    --set=role_name="$role" <<'SQL'
SELECT format('CREATE DATABASE %I OWNER %I ENCODING %L TEMPLATE template0', :'database_name', :'role_name', 'UTF8')
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = :'database_name')
\gexec

SELECT format('ALTER DATABASE %I OWNER TO %I', :'database_name', :'role_name')
\gexec
SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', :'database_name')
\gexec
SELECT DISTINCT format('REVOKE ALL ON DATABASE %I FROM %I', d.datname, grantee.rolname)
FROM pg_database AS d
CROSS JOIN LATERAL aclexplode(d.datacl) AS privilege
JOIN pg_roles AS grantee ON grantee.oid = privilege.grantee
WHERE d.datname = :'database_name'
  AND grantee.rolname <> :'role_name'
\gexec
SELECT format('GRANT CONNECT, TEMPORARY ON DATABASE %I TO %I', :'database_name', :'role_name')
\gexec
SQL
}

"${psql_admin[@]}" <<SQL
REVOKE CONNECT ON DATABASE "$POSTGRES_DB" FROM PUBLIC;
REVOKE CONNECT ON DATABASE template1 FROM PUBLIC;
SQL

while IFS='|' read -r service database role password_env extra; do
  [[ -z "$service" || "$service" == \#* ]] && continue
  if [[ -n "${extra:-}" || -z "$database" || -z "$role" || -z "$password_env" ]]; then
    printf 'invalid database config row for service %s; expected service|database|role|password_env\n' "$service" >&2
    exit 1
  fi
  provision_service "$service" "$database" "$role" "$password_env"
done < "$config_file"

# The backup service's read-only login connects to every database above. The
# loop revoked database grants from every role but the owner, so the backup
# role is reconciled after it, which also grants it a newly added database.
backup_role_script=${LIFE_OS_BACKUP_ROLE_SCRIPT:-/opt/lifeos/postgres/provision-backup-role.sh}
if [[ -r "$backup_role_script" ]]; then
  LIFE_OS_DATABASE_CONFIG="$config_file" bash "$backup_role_script"
else
  printf 'backup role script is not readable: %s; skipping the backup role\n' "$backup_role_script" >&2
fi
