# shellcheck shell=bash
# Sourced by provision-service-databases.sh and provision-backup-role.sh: a
# database password must not be .env.example's replace-with-… placeholder,
# which anyone can read, nor shorter than 32 characters (./setup.sh and
# tools/ci-env make 48). A new login is refused one. A login that exists
# already keeps the password it has, with a warning at every start, so that
# an install made before this check starts as it did until its passwords are
# replaced (docs/install/manual.md#database-passwords).

min_password_length=32

# weak_password <value>: why the value is weak, or nothing.
weak_password() {
  local value=$1
  if [[ $value == replace-with-* ]]; then
    printf "is still .env.example's placeholder"
  elif ((${#value} < min_password_length)); then
    printf 'is shorter than %d characters' "$min_password_length"
  fi
}

# check_password <variable> <value> <login> <exists: 1 or 0>: refuses
# (exit 1) a weak password for a new login, and warns for one that exists.
# Never prints the password.
check_password() {
  local variable=$1 value=$2 login=$3 exists=$4 why
  why=$(weak_password "$value")
  [[ -n $why ]] || return 0
  if [[ $exists == 1 ]]; then
    printf 'warning: %s %s. The login %s keeps working with it for now: replace it in .env with a new one (openssl rand -hex 24) and start Life OS again, and provisioning sets it (docs/install/manual.md#database-passwords).\n' \
      "$variable" "$why" "$login" >&2
  else
    printf '%s %s: set a new one in .env (openssl rand -hex 24) before the login %s is made with it (docs/install/manual.md#database-passwords).\n' \
      "$variable" "$why" "$login" >&2
    exit 1
  fi
}
