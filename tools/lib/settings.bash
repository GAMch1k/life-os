# shellcheck shell=bash
# Settings for the tools, from the deployment's .env (docs: SETUP.md, step 4).
# Source it after changing to the repository root:
#
#   . tools/lib/settings.bash
#   host=$(setting LIFE_OS_PUBLIC_HOSTNAME)

# setting <name>: the value from the environment, else from .env as Compose
# reads it (the last line wins; surrounding quotes go, and ~ and $HOME
# become the home directory). Empty when neither sets it.
setting() {
  local name=$1 value=''
  if [[ -n "${!name:-}" ]]; then
    printf '%s' "${!name}"
    return
  fi
  [[ ! -f .env ]] || value=$(sed -n "s/^$name=//p" .env | tail -n 1)
  value=${value#[\"\']}; value=${value%[\"\']}
  value=${value/#\~/${HOME:-}}
  # shellcheck disable=SC2016 # the literal text ${HOME} and $HOME
  value=${value//'${HOME}'/${HOME:-}}
  # shellcheck disable=SC2016
  value=${value//'$HOME'/${HOME:-}}
  printf '%s' "$value"
}

# url_host <url>: the host of an http(s) URL, lowercase, without its port.
url_host() {
  local host=${1#*://}
  host=${host%%[/?#]*}
  host=${host##*@}
  host=${host%:*}
  printf '%s' "${host,,}"
}
