#!/usr/bin/env bash
# Life OS setup helper: writes the .env for the published images, then starts,
# checks, and updates Life OS. It runs from the directory that holds the
# release's compose.yaml, .env.example, and this script. Bash 4.4 or newer; see
# docs/install/README.md.
#
#   ./setup.sh [options]        first-time setup: checks, then a complete .env
#   ./setup.sh up               start Life OS and wait until it answers
#   ./setup.sh update [VERSION] back up, pull the new images, restart
#   ./setup.sh status           what runs, and what is set up (never values)
#   ./setup.sh help             every command and option
#
# It never prints a password, key, or token, and never overwrites an existing
# .env without --force-new (which keeps a backup and the database passwords).

set -euo pipefail

# The release this copy belongs to: the image version a release may write
# over the placeholder on the next line, else the LIFE_OS_VERSION the
# release's .env.example names, else the moving "preview" images.
release_version='__LIFE_OS_VERSION__'
case $release_version in __*) release_version= ;; esac

docs=https://github.com/GAMch1k/life-os/blob/master/docs/install
min_engine=25.0
min_compose=2.27

cd "$(dirname "${BASH_SOURCE[0]}")"
umask 077
if [[ -z $release_version && -f .env.example ]]; then
  release_version=$(sed -n "s/^LIFE_OS_VERSION=['\"]*\([^'\"]*\)['\"]*$/\1/p" .env.example | head -n 1)
fi
[[ $release_version =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || release_version=preview

assume_yes=0
force_new=0
skip_checks=0
skip_backup=0
skip_access_check=0
allow_public_bind=0
bind_flag=
port_flag=
public_url_flag=

# ---------- Output ----------

say() { printf '%s\n' "$*"; }
step() { printf '\n==> %s\n' "$*"; }
warn() { printf 'Warning: %s\n' "$*" >&2; }
die() {
  printf 'setup: %s\n' "$*" >&2
  exit 1
}
usage_error() {
  printf 'setup: %s\n' "$*" >&2
  printf "Run './setup.sh help' to see the commands and options.\n" >&2
  exit 2
}

preview_banner() {
  cat <<EOF
Life OS early preview ($release_version)
  * Life OS has no login of its own yet: whoever can open it can read and
    change everything in it.
  * By default it answers only on this server (127.0.0.1). Reach it over an
    SSH tunnel, a VPN, or Cloudflare Access, never straight from the internet.
  * Previews have rough edges and may change between releases. Back up before
    you update (./setup.sh update does it for you).
EOF
}

# ---------- Questions ----------

# ask VAR QUESTION [DEFAULT]: sets VAR to the answer, or to DEFAULT when the
# answer is empty or the run is non-interactive (--yes).
ask() {
  local -n ask_result=$1
  local question=$2 default=${3-} answer
  if ((assume_yes)); then
    ask_result=$default
    return
  fi
  [[ -z $default ]] || question+=" [$default]"
  read -r -p "$question: " answer || die 'no answer (end of input); for a run without questions, use --yes'
  # shellcheck disable=SC2034 # a name reference: sets the caller's variable
  ask_result=${answer:-$default}
}

# ask_secret VAR QUESTION: reads without echo.
ask_secret() {
  local -n secret_result=$1
  local answer
  read -r -s -p "$2: " answer || die 'no answer (end of input); for a run without questions, use --yes'
  printf '\n' >&2
  # shellcheck disable=SC2034 # a name reference: sets the caller's variable
  secret_result=$answer
}

# confirm QUESTION DEFAULT(y|n): yes or no; --yes takes the default.
confirm() {
  local question=$1 default=$2 answer hint='[y/N]'
  [[ $default != y ]] || hint='[Y/n]'
  if ((assume_yes)); then
    [[ $default == y ]]
    return
  fi
  while true; do
    read -r -p "$question $hint " answer || die 'no answer (end of input); for a run without questions, use --yes'
    case ${answer:-$default} in
      [Yy] | [Yy][Ee][Ss]) return 0 ;;
      [Nn] | [Nn][Oo]) return 1 ;;
    esac
  done
}

# ---------- Random values ----------

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$1"
  else
    od -An -v -N "$1" -tx1 /dev/urandom | tr -d ' \n'
    printf '\n'
  fi
}

random_base64() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 "$1"
  else
    head -c "$1" /dev/urandom | base64 | tr -d '\n'
    printf '\n'
  fi
}

# ---------- .env ----------

# The value of KEY in FILE (default .env), without its quotes. Fails when
# the file has no such uncommented line.
env_get() {
  local key=$1 file=${2:-.env} line value found=1
  [[ -f $file ]] || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line == "$key="* ]]; then
      value=${line#*=}
      found=0
    fi
  done <"$file"
  ((found == 0)) || return 1
  if [[ $value == \'*\' && ${#value} -ge 2 ]]; then
    value=${value:1:${#value}-2}
  elif [[ $value == \"*\" && ${#value} -ge 2 ]]; then
    value=${value:1:${#value}-2}
  fi
  printf '%s\n' "$value"
}

# A value as a .env line takes it: bare when it is plain, else in single
# quotes, which Compose reads literally (bcrypt hashes contain $). env_write
# has refused values that .env cannot hold.
env_format() {
  local value=$1
  if [[ $value =~ ^[A-Za-z0-9_./:@,+=%-]*$ ]]; then
    printf '%s' "$value"
  else
    printf "'%s'" "$value"
  fi
}

declare -A env_values=()

# Writes TARGET from SOURCE with env_values applied: a key's first uncommented
# line gets the new value, later duplicates are dropped, and keys SOURCE does
# not have are added at the end. The file is replaced at once, mode 600.
env_write() {
  local source=$1 target=$2 line key tmp
  local -A done_keys=()
  for key in "${!env_values[@]}"; do
    [[ ${env_values[$key]} != *$'\n'* && ${env_values[$key]} != *"'"* ]] \
      || die "the value for $key has a newline or a single quote, which .env cannot hold"
  done
  tmp=$(mktemp "$target.XXXXXX")
  {
    while IFS= read -r line || [[ -n $line ]]; do
      key=${line%%=*}
      if [[ $line == *=* && $key =~ ^[A-Z][A-Z0-9_]*$ && -v env_values[$key] ]]; then
        [[ -v done_keys[$key] ]] && continue
        printf '%s=%s\n' "$key" "$(env_format "${env_values[$key]}")"
        done_keys[$key]=1
      else
        printf '%s\n' "$line"
      fi
    done <"$source"
    local header_written=0
    if grep -q '^# ---------- Set by setup.sh ----------$' "$source"; then header_written=1; fi
    for key in $(printf '%s\n' "${!env_values[@]}" | sort); do
      [[ -v done_keys[$key] ]] && continue
      if ((!header_written)); then
        printf '\n# ---------- Set by setup.sh ----------\n'
        header_written=1
      fi
      printf '%s=%s\n' "$key" "$(env_format "${env_values[$key]}")"
    done
  } >"$tmp"
  chmod 600 "$tmp"
  mv -f "$tmp" "$target"
}

# Applies env_values to the existing .env.
env_update() {
  ((${#env_values[@]})) || return 0
  env_write .env .env
  env_values=()
}

require_env() {
  [[ -f .env ]] || die "there is no .env here yet; run ./setup.sh first"
}

# Adds NAME to the comma-separated list in KEY (such as COMPOSE_PROFILES).
list_add() {
  local key=$1 name=$2 current
  current=$(env_get "$key" || true)
  if [[ ,$current, == *",$name,"* ]]; then
    env_values[$key]=$current
  else
    env_values[$key]=${current:+$current,}$name
  fi
}

list_has() {
  local current
  current=$(env_get "$1" || true)
  [[ ,$current, == *",$2,"* ]]
}

# A value that a generated secret still is: set, and not a template example.
is_set() {
  local value
  value=$(env_get "$1" "${2:-.env}" 2>/dev/null) || return 1
  [[ -n $value && $value != replace-with-* ]]
}

# ---------- Where things are ----------

# A clone of the repository (developers, and the reference server) builds its
# images from source and is set up with SETUP.md; the release files only run
# images.
is_checkout() {
  [[ -e .git ]] || { [[ -f compose.yaml ]] && grep -Eq '^[[:space:]]+build:' compose.yaml; }
}

require_release_dir() {
  if is_checkout; then
    die "this is a clone of the repository, which builds its own images: start and update it as SETUP.md says (redeploy with 'docker compose up -d --no-build --no-deps <service>'). ./setup.sh $1 is for the release files (docs/install)."
  fi
  [[ -f compose.yaml ]] || die "compose.yaml is missing here: download it from the same release as this script (see $docs/README.md)"
}

gateway_bind() { env_get LIFE_OS_GATEWAY_BIND_ADDRESS 2>/dev/null || printf '127.0.0.1\n'; }
gateway_port() { env_get LIFE_OS_GATEWAY_PORT 2>/dev/null || printf '3000\n'; }

# The address this script itself uses to reach the gateway.
gateway_url() {
  local bind
  bind=$(gateway_bind)
  [[ $bind != 0.0.0.0 && -n $bind ]] || bind=127.0.0.1
  printf 'http://%s:%s\n' "$bind" "$(gateway_port)"
}

apply_hint() {
  if is_checkout; then
    say "Apply it with: docker compose up -d --no-build --no-deps $*"
  else
    say "Apply it with: ./setup.sh up"
  fi
}

# ---------- Checks ----------

# version_at_least HAVE WANT: whether HAVE (such as 2.29.1) is WANT or newer.
version_at_least() {
  local have=${1#v} want=$2 h w i
  local -a hs ws
  IFS=. read -r -a hs <<<"$have"
  IFS=. read -r -a ws <<<"$want"
  for ((i = 0; i < ${#ws[@]}; i++)); do
    h=${hs[i]:-0}
    h=${h%%[!0-9]*}
    w=${ws[i]}
    if ((10#${h:-0} > 10#$w)); then return 0; fi
    if ((10#${h:-0} < 10#$w)); then return 1; fi
  done
  return 0
}

check_docker() {
  local out engine compose
  command -v docker >/dev/null 2>&1 \
    || die 'Docker is not installed. Install Docker Engine with the Compose plugin (https://docs.docker.com/engine/install/), then run this again.'
  if ! out=$(docker info --format '{{.ServerVersion}}' 2>&1); then
    case $out in
      *[Pp]ermission\ denied*)
        die "$(id -un) cannot talk to Docker. Add yourself to the docker group (sudo usermod -aG docker $(id -un)), log out and back in, and run this again. Members of that group can control every container, so it is as powerful as root." ;;
      *) die "Docker is not running or cannot be reached: $out" ;;
    esac
  fi
  engine=$out
  compose=$(docker compose version --short 2>/dev/null) \
    || die "Docker Compose v2 is missing: install the docker-compose-plugin package ('docker compose', not the old 'docker-compose')."
  compose=${compose#v}
  version_at_least "$engine" "$min_engine" \
    || warn "Docker Engine $engine is older than $min_engine, the oldest that has every feature Life OS uses. Update Docker if 'up' fails."
  version_at_least "$compose" "$min_compose" \
    || warn "Docker Compose $compose is older than $min_compose, the oldest that has every feature Life OS uses. Update Docker if 'up' fails."
  say "Docker Engine $engine, Docker Compose $compose."
}

check_host() {
  local mem_kib free_kib root
  if [[ $(uname -m) != x86_64 ]]; then
    warn "this server is $(uname -m). The preview images are built for x86_64 (amd64) only; ARM images come later, so 'up' will not find images for this machine."
  fi
  if (($(id -u) == 0)); then
    warn 'you are running as root. It works, but the containers that use your files (backups, the AI sign-in, a bank key) then run as root too. A normal user in the docker group is better.'
  fi
  if [[ -r /proc/meminfo ]]; then
    mem_kib=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
    if ((mem_kib < 2 * 1024 * 1024)); then
      warn "this server has $((mem_kib / 1024)) MiB of memory. Life OS uses about 0.5 GB when idle, but Whisper may use up to 2 GB while it transcribes a voice note, and each AI run a few hundred MB. 2 GB is the least; 4 GB is comfortable. A smaller Whisper model helps: ./setup.sh whisper."
    fi
  fi
  free_kib=$(df -Pk . 2>/dev/null | awk 'NR == 2 {print $4}')
  if [[ -n $free_kib ]] && ((free_kib < 10 * 1024 * 1024)); then
    warn "only $((free_kib / 1024 / 1024)) GB are free here. The images take about 3 GB, the Whisper model 0.5 GB (small) to 1.6 GB, and your data and nightly backups grow from there. Keep at least 10 GB free."
  fi
  root=$(docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)
  if [[ -n $root && $root != "$PWD" ]]; then
    free_kib=$(df -Pk "$root" 2>/dev/null | awk 'NR == 2 {print $4}')
    if [[ -n $free_kib ]] && ((free_kib < 10 * 1024 * 1024)); then
      warn "only $((free_kib / 1024 / 1024)) GB are free for Docker ($root). The images alone take about 3 GB."
    fi
  fi
}

check_tools() {
  command -v openssl >/dev/null 2>&1 || [[ -r /dev/urandom ]] \
    || die 'neither openssl nor /dev/urandom is available to generate passwords'
  command -v curl >/dev/null 2>&1 \
    || warn "curl is not installed. 'up', 'status', and some set-up steps use it to check Life OS: install it (sudo apt install curl)."
}

check_port() {
  local port=$1
  command -v ss >/dev/null 2>&1 || return 0
  if [[ -n $(ss -Htln "sport = :$port" 2>/dev/null) ]]; then
    warn "something on this server already listens on port $port. If it is not Life OS, choose another one with --port <port>."
  fi
}

run_checks() {
  step 'Checking this server'
  check_tools
  check_docker
  check_host
}

# ---------- Gateway address ----------

# The class of an IPv4 address: loopback, private (LAN, VPN, Tailscale's
# 100.64.0.0/10), or public (everything else, 0.0.0.0 too).
address_class() {
  local a b c d
  [[ $1 =~ ^([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})\.([0-9]{1,3})$ ]] || {
    printf 'invalid\n'
    return
  }
  a=${BASH_REMATCH[1]} b=${BASH_REMATCH[2]} c=${BASH_REMATCH[3]} d=${BASH_REMATCH[4]}
  if ((a > 255 || b > 255 || c > 255 || d > 255)); then
    printf 'invalid\n'
  elif ((a == 127)); then
    printf 'loopback\n'
  elif ((a == 10 || (a == 172 && b >= 16 && b <= 31) || (a == 192 && b == 168) || (a == 100 && b >= 64 && b <= 127))); then
    printf 'private\n'
  else
    printf 'public\n'
  fi
}

# Validates a gateway address, and says what it means.
check_bind() {
  local bind=$1
  case $(address_class "$bind") in
    loopback) return 0 ;;
    invalid) usage_error "--bind needs an IPv4 address such as 127.0.0.1 or your VPN address, not '$bind'" ;;
    private)
      warn "Life OS will answer at $bind to every device that can reach that address. It has no login of its own: on a VPN, every peer is the owner. Never use a LAN address that other people share." ;;
    public)
      if ((!allow_public_bind)); then
        die "$bind is a public address (or every address). Life OS has no login yet, so it refuses to listen there: keep 127.0.0.1 and use Cloudflare Access, a VPN, or an SSH tunnel ($docs/access.md). If a firewall or a login in front really protects it, add --allow-public-bind."
      fi
      cat >&2 <<EOF

!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
!! Life OS will listen on $bind, a public address.
!! It has NO LOGIN: anyone who reaches it can read and change everything,
!! use your AI sign-in, and see your bank data. Only do this when a firewall
!! or a login in front of it keeps everyone else out.
!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

EOF
      ;;
  esac
}

check_port_number() {
  if [[ ! $1 =~ ^[0-9]+$ ]] || (($1 < 1 || $1 > 65535)); then
    usage_error "the port must be a number from 1 to 65535, not '$1'"
  fi
}

# Normalizes a public URL (http(s)://host[:port], no path) or fails.
normalize_url() {
  local url=${1%/}
  [[ $url =~ ^https?://[A-Za-z0-9.-]+(:[0-9]+)?$ ]] || return 1
  printf '%s\n' "$url"
}

# The host of a URL when it is a name the gateway must answer: not an IPv4
# address, not localhost.
url_hostname() {
  local host=${1#*://}
  host=${host%%:*}
  if [[ $host == localhost || $(address_class "$host") != invalid ]]; then
    return 0
  fi
  printf '%s\n' "$host"
}

# Puts the gateway settings into env_values: from the options, the answers,
# or what .env has.
gateway_values() {
  local bind port url default_url current_url
  bind=${bind_flag:-${LIFE_OS_GATEWAY_BIND_ADDRESS:-$(gateway_bind)}}
  port=${port_flag:-${LIFE_OS_GATEWAY_PORT:-$(gateway_port)}}
  check_bind "$bind"
  check_port_number "$port"
  [[ -e .env ]] || check_port "$port"
  if [[ $bind == 0.0.0.0 || $(address_class "$bind") == loopback ]]; then
    default_url=http://127.0.0.1:$port
  else
    default_url=http://$bind:$port
  fi
  current_url=$(env_get LIFE_OS_PUBLIC_URL 2>/dev/null || true)
  # Keep an https address already set (Cloudflare or a reverse proxy); an
  # http one follows the gateway's address.
  if [[ $current_url == https://* && $current_url != https://lifeos.example.com ]]; then
    default_url=$current_url
  fi
  [[ -z ${LIFE_OS_PUBLIC_URL:-} ]] || default_url=$LIFE_OS_PUBLIC_URL
  url=$public_url_flag
  if [[ -z $url ]]; then
    if ((!assume_yes)); then
      say ''
      say 'Life OS needs the address you will open it at. Keep the default if you will use an'
      say 'SSH tunnel or a VPN. Enter an https:// address only if a Cloudflare Tunnel or your'
      say 'own reverse proxy (with a login) will serve it, such as https://lifeos.example.com.'
    fi
    ask url 'Life OS address' "$default_url"
  fi
  url=$(normalize_url "$url") || usage_error "the address must look like http://127.0.0.1:3000 or https://lifeos.example.com (no path)"
  env_values[LIFE_OS_GATEWAY_BIND_ADDRESS]=$bind
  env_values[LIFE_OS_GATEWAY_PORT]=$port
  env_values[LIFE_OS_PUBLIC_URL]=$url
  env_values[LIFE_OS_PUBLIC_HOSTNAME]=$(url_hostname "$url")
}

# ---------- First-time setup ----------

detect_timezone() {
  local tz=
  if [[ -r /etc/timezone ]]; then tz=$(head -n 1 /etc/timezone | tr -d '[:space:]'); fi
  if [[ -z $tz ]] && command -v timedatectl >/dev/null 2>&1; then
    tz=$(timedatectl show -p Timezone --value 2>/dev/null || true)
  fi
  if [[ -z $tz && -L /etc/localtime ]]; then
    tz=$(readlink /etc/localtime)
    tz=${tz##*zoneinfo/}
  fi
  case $tz in '' | Etc/UTC | Etc/Universal | Universal | Zulu | Etc/Zulu) tz=UTC ;; esac
  valid_timezone "$tz" || tz=UTC
  printf '%s\n' "$tz"
}

valid_timezone() {
  [[ $1 =~ ^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+)*$ ]] || return 1
  [[ $1 == UTC || ! -d /usr/share/zoneinfo || -f /usr/share/zoneinfo/$1 ]]
}

# Every database login's password the stack needs: those .env.example lists,
# and those compose.yaml requires.
password_keys() {
  {
    sed -n 's/^\(LIFE_OS_[A-Z0-9_]*_PASSWORD\)=.*/\1/p' .env.example
    printf '%s\n' LIFE_OS_POSTGRES_PASSWORD
  } | sort -u
}

# Keys whose old values a new .env keeps (--force-new): the data depends on
# them. The database passwords, the keys that encrypt the stored Google and
# bank sessions, and the web push key pair.
kept_keys() {
  password_keys
  printf '%s\n' LIFE_OS_GOOGLE_TOKEN_KEY LIFE_OS_BANKING_TOKEN_KEY \
    LIFE_OS_WEBPUSH_VAPID_PUBLIC_KEY LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY
}

make_dir() {
  local dir=$1 mode=$2
  [[ -d $dir ]] || { mkdir -p -- "$(dirname -- "$dir")" && mkdir -m "$mode" -- "$dir"; }
}

cmd_init() {
  local backup='' key tz currency version backup_dir codex_home android_dir docker_sock gid
  local -i generated=0 kept=0
  preview_banner
  if [[ -e .env ]] && ((!force_new)); then
    die "a .env already exists here, so nothing was changed. To change one part, run ./setup.sh gateway, ./setup.sh <integration>, or edit .env. To start over, run ./setup.sh --force-new (it keeps a copy of the old .env and its database passwords)."
  fi
  [[ -f .env.example ]] || die ".env.example is missing here: download it from the same release as this script (see $docs/README.md)"
  if ((skip_checks)); then
    warn 'skipping the server checks (--skip-checks)'
  else
    run_checks
  fi
  if is_checkout; then
    say ''
    say 'This is a clone of the repository: .env is written for it, and SETUP.md describes the rest.'
  fi

  step 'Settings'
  ask tz 'Timezone (IANA name)' "${LIFE_OS_TIMEZONE:-$(detect_timezone)}"
  valid_timezone "$tz" || usage_error "'$tz' is not a timezone name such as Europe/Berlin or America/New_York"
  ask currency 'Currency of the Cash account' "${LIFE_OS_FINANCE_CURRENCY:-EUR}"
  currency=${currency^^}
  [[ $currency =~ ^[A-Z]{3}$ ]] || usage_error "the currency must be a three-letter code such as EUR or USD, not '$currency'"
  gateway_values

  version=${LIFE_OS_VERSION:-$release_version}
  [[ $version =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || usage_error "LIFE_OS_VERSION '$version' is not an image tag"
  env_values[LIFE_OS_VERSION]=$version
  [[ -z ${LIFE_OS_IMAGE_REGISTRY:-} ]] || env_values[LIFE_OS_IMAGE_REGISTRY]=$LIFE_OS_IMAGE_REGISTRY
  env_values[LIFE_OS_TIMEZONE]=$tz
  env_values[LIFE_OS_FINANCE_CURRENCY]=$currency
  env_values[LIFE_OS_POSTGRES_USER]=lifeos_admin

  # The host directories: in the release directory, next to this script; in
  # a clone, where SETUP.md puts them.
  if is_checkout; then
    backup_dir=${LIFE_OS_BACKUP_DIR:-$HOME/life-os.backups}
    codex_home=${LIFE_OS_CODEX_HOME_HOST_PATH:-$HOME/.codex}
    android_dir=$HOME/life-os.android
  else
    backup_dir=${LIFE_OS_BACKUP_DIR:-$PWD/backups}
    codex_home=${LIFE_OS_CODEX_HOME_HOST_PATH:-$PWD/codex-home}
  fi
  [[ $backup_dir == /* && $codex_home == /* ]] || usage_error 'LIFE_OS_BACKUP_DIR and LIFE_OS_CODEX_HOME_HOST_PATH must be absolute paths'
  env_values[LIFE_OS_BACKUP_DIR]=$backup_dir
  env_values[LIFE_OS_BACKUP_RUNTIME_UID]=$(id -u)
  env_values[LIFE_OS_CODEX_HOME_HOST_PATH]=$codex_home
  env_values[LIFE_OS_CODEX_RUNTIME_UID]=$(id -u)
  env_values[LIFE_OS_BANKING_RUNTIME_UID]=$(id -u)
  docker_sock=/var/run/docker.sock
  [[ ${DOCKER_HOST:-} != unix://* ]] || docker_sock=${DOCKER_HOST#unix://}
  if gid=$(stat -c %g "$docker_sock" 2>/dev/null); then
    env_values[LIFE_OS_DOCKER_GID]=$gid
  else
    warn "cannot read the group of $docker_sock: the Status page's server card may not see the containers (LIFE_OS_DOCKER_GID in .env)."
  fi
  # The template's example hosts must not stay: ntfy is set up on its own.
  env_values[LIFE_OS_NTFY_PUBLIC_URL]=

  # Passwords: kept from the old .env with --force-new, else new.
  if [[ -e .env ]]; then
    backup=.env.backup-$(date -u +%Y%m%dT%H%M%SZ)
    cp -p .env "$backup"
    chmod 600 "$backup"
  fi
  while IFS= read -r key; do
    if [[ -n $backup ]] && is_set "$key" "$backup"; then
      env_values[$key]=$(env_get "$key" "$backup")
      kept+=1
    elif [[ $key == *_PASSWORD ]]; then
      env_values[$key]=$(random_hex 24)
      generated+=1
    fi
  done < <(kept_keys)

  step 'Writing .env'
  make_dir "$backup_dir" 700
  make_dir "$codex_home" 700
  # Only the owner's server serves Android releases; the release's
  # compose.yaml has no such mount.
  if is_checkout; then
    make_dir "$android_dir" 755
  fi
  env_write .env.example .env
  env_values=()
  say "Wrote .env (mode 600: only $(id -un) can read it)."
  [[ -z $backup ]] || say "The old one is in $backup; $kept passwords and keys were kept from it, so the existing data still opens."
  printf '  %-17s %s\n' \
    version "$version" \
    address "$(env_get LIFE_OS_PUBLIC_URL)" \
    timezone "$tz" \
    currency "$currency" \
    backups "$backup_dir" \
    'AI sign-in' "$codex_home"
  ((generated == 0)) || printf '  %-17s %s\n' passwords "$generated generated (never shown)"
  say 'Keep a copy of .env in a password manager or another safe place: backups do not include it.'

  setup_integrations_now

  step 'Next'
  if is_checkout; then
    say 'Build and start Life OS as SETUP.md describes.'
  else
    say 'Start Life OS:  ./setup.sh up'
  fi
}

# After a first setup: each integration, now or later.
setup_integrations_now() {
  local name
  if ((assume_yes)); then
    # Without questions, an integration is set up when its settings are in
    # the environment.
    [[ -z ${LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN:-} ]] || setup_claude
    [[ -z ${LIFE_OS_SETUP_WHISPER_MODEL:-} ]] || setup_whisper
    [[ -z ${LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN:-} ]] || setup_cloudflare
    [[ -z ${LIFE_OS_GOOGLE_CLIENT_ID:-} ]] || setup_google
    [[ -z ${LIFE_OS_ENABLE_BANKING_APP_ID:-} ]] || setup_banking
    [[ -z ${LIFE_OS_NTFY_PUBLIC_URL:-} ]] || setup_ntfy
    [[ ${LIFE_OS_SETUP_WEBPUSH:-} != on ]] || setup_webpush
    return 0
  fi
  step 'Integrations'
  cat <<EOF
Life OS works without them. Each one can be set up now, or later on its own:
  codex       AI processing of your notes (OpenAI Codex, needs a ChatGPT plan)
  claude      or AI processing with Claude Code instead (needs a Claude plan)
  whisper     the speech-to-text model for voice notes (default: small)
  cloudflare  reach Life OS from anywhere through a Cloudflare Tunnel and Access
  google      sync with Google Calendar
  banking     read your bank account through Enable Banking
  ntfy        push notifications through your own ntfy server
  webpush     push notifications to the Life OS phone app
  feed        subscribe to a calendar feed (ICS), once Life OS is running
EOF
  confirm 'Set up some of them now?' n || {
    say 'Later: ./setup.sh <name>, for example ./setup.sh codex.'
    return 0
  }
  for name in codex claude whisper cloudflare google banking ntfy webpush; do
    if confirm "Set up $name now?" n; then
      "setup_$name"
    fi
  done
}

# ---------- Integrations ----------

intro() {
  step "$1"
  say "Guide: $docs/$2"
}

# Asks whether to change an integration that is already set up.
change_existing() {
  ((!assume_yes)) || return 0
  confirm "$1 is already set up. Change it?" n
}

setup_codex() {
  local home mount
  intro 'AI processing (Codex)' ai-processing.md
  if is_checkout; then
    die "in a clone of the repository, sign in with the Codex CLI on the server itself (SETUP.md, step 5)"
  fi
  require_env
  [[ -f compose.yaml ]] || die "compose.yaml is missing here: download it from the same release as this script"
  ((!assume_yes)) || die 'the Codex sign-in needs you at a terminal: run ./setup.sh codex'
  cat <<'EOF'
Life OS's AI reads each note you capture and files it as a task, an event, a
shopping item, or an expense. It runs OpenAI's Codex with your ChatGPT sign-in:
  * You need a ChatGPT plan that includes Codex. Each note uses a little of
    that plan's Codex allowance; no API key and no separate bill.
  * Your notes, and the tasks and events the AI looks at to understand them,
    are sent to OpenAI. Leave this off if you don't want that.
The sign-in works without a browser on this server: open the link it shows on
any device, sign in, and type the code it shows.
EOF
  confirm 'Sign in to Codex now?' y || return 0
  check_docker
  home=$(env_get LIFE_OS_CODEX_HOME_HOST_PATH) || die 'LIFE_OS_CODEX_HOME_HOST_PATH is missing from .env'
  if [[ $home == /* ]]; then
    make_dir "$home" 700
  fi
  mount=$home:/codex-login
  # The processor's own image and user, with the sign-in written to the
  # directory the processor reads (read-only) at every start.
  docker compose run --rm --no-deps --entrypoint codex -e CODEX_HOME=/codex-login -v "$mount" \
    processor login --device-auth || die 'the Codex sign-in did not finish; run ./setup.sh codex to try again'
  docker compose run --rm --no-deps -T --entrypoint codex -e CODEX_HOME=/codex-login -v "$mount" \
    processor login status || die 'Codex says it is not signed in; run ./setup.sh codex to try again'
  if [[ $(ai_provider) != codex ]]; then
    env_values[LIFE_OS_AI_PROVIDER]=codex
    env_update
    say 'AI processing now uses Codex instead of Claude Code (LIFE_OS_AI_PROVIDER=codex).'
    apply_hint processor
  elif [[ -n $(docker compose ps -q processor 2>/dev/null) ]]; then
    docker compose restart processor >/dev/null
    say 'Restarted the processor with the new sign-in.'
  fi
  say 'AI processing is on. The Status page shows the Codex allowance that is left.'
}

# Claude Code instead of Codex: the subscription's long-lived token, which
# claude setup-token prints on any computer, goes into .env. Never an API key:
# the runs use the subscription, not the Anthropic API.
setup_claude() {
  local token
  intro 'AI processing (Claude Code)' ai-processing.md
  require_env
  if [[ $(ai_provider) == claude ]] && is_set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN; then
    change_existing 'AI processing with Claude Code' || return 0
  fi
  cat <<'EOF'
Instead of Codex, Life OS's AI can run Anthropic's Claude Code with your
Claude subscription:
  * You need a Claude Pro or Max plan. Each note uses a little of that plan's
    usage limits; no API key and no separate bill.
  * Your notes, and the tasks and events the AI looks at to understand them,
    are sent to Anthropic. Leave this off if you don't want that.
Create the token on any computer with Claude Code: run claude setup-token,
sign in with your subscription, and paste the token it prints.
EOF
  if ((assume_yes)); then
    token=${LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN:-}
  else
    confirm 'Use Claude Code for AI processing?' y || return 0
    ask_secret token 'Token from claude setup-token (input is hidden)'
  fi
  [[ $token != sk-ant-api* ]] || usage_error 'that is an Anthropic API key, which bills the API: paste the subscription token that claude setup-token prints'
  [[ -n $token && $token =~ ^[A-Za-z0-9_-]+$ ]] || usage_error 'the token is missing or has unexpected characters: paste what claude setup-token prints'
  env_values[LIFE_OS_AI_PROVIDER]=claude
  env_values[LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN]=$token
  env_update
  say 'AI processing uses Claude Code. To go back to Codex: ./setup.sh codex.'
  apply_hint processor
}

setup_whisper() {
  local model memory current
  intro 'Voice: the Whisper model' voice.md
  require_env
  current=$(whisper_model)
  cat <<'EOF'
Voice notes are transcribed on this server by whisper.cpp. Bigger models are
more accurate, slower, and need more memory. All of them understand many
languages. Download size, and memory while transcribing (whisper.cpp's figures):
  tiny            75 MB    about 0.3 GB
  base           142 MB    about 0.4 GB
  small          466 MB    about 0.9 GB   (default)
  medium         1.5 GB    about 2.1 GB
  large-v3-turbo 1.6 GB    more than medium
EOF
  ask model 'Model' "${LIFE_OS_SETUP_WHISPER_MODEL:-$current}"
  case $model in
    tiny | base | small) memory=2g ;;
    medium | large-v3-turbo) memory=3g ;;
    *) usage_error "unknown Whisper model '$model': choose tiny, base, small, medium, or large-v3-turbo" ;;
  esac
  env_values[LIFE_OS_WHISPER_MODEL_PATH]=/models/ggml-$model.bin
  env_values[LIFE_OS_WHISPER_MODEL_URL]=https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-$model.bin
  env_values[LIFE_OS_WHISPER_MEMORY]=$memory
  env_update
  say "Whisper uses the $model model (memory cap $memory); it downloads it at its next start."
  apply_hint whisper
}

setup_cloudflare() {
  local url host token result code location
  intro 'Cloudflare Tunnel and Access' access.md#cloudflare-tunnel-and-access
  require_env
  if is_set LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN && list_has COMPOSE_PROFILES cloudflare; then
    change_existing 'The Cloudflare Tunnel' || return 0
  fi
  cat <<'EOF'
Before this step, in Cloudflare Zero Trust (the guide shows each click):
  1. Access: a self-hosted application for your Life OS hostname, with an
     Allow policy for your own email address.
  2. Networks > Tunnels: a tunnel, and a published application route from
     that hostname to HTTP gateway:80.
Access must protect the hostname before the tunnel runs: Life OS has no login.
EOF
  url=$(env_get LIFE_OS_PUBLIC_URL 2>/dev/null || true)
  [[ $url == https://* ]] || url=
  [[ -z ${LIFE_OS_PUBLIC_URL:-} || ${LIFE_OS_PUBLIC_URL:-} != https://* ]] || url=$LIFE_OS_PUBLIC_URL
  ask url 'Life OS address on Cloudflare (https://...)' "$url"
  if ! url=$(normalize_url "$url") || [[ $url != https://* ]]; then
    usage_error 'the address must be https://<hostname>, such as https://lifeos.example.com'
  fi
  host=$(url_hostname "$url")
  [[ -n $host ]] || usage_error 'the Cloudflare address needs a hostname, not an IP address'
  if ((assume_yes)); then
    token=${LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN:-}
  else
    ask_secret token 'Tunnel token (Networks > Tunnels > your tunnel > install command: the long value after --token; input is hidden)'
  fi
  token=${token#*--token }
  token=${token//[[:space:]]/}
  [[ $token =~ ^[A-Za-z0-9+/=_-]{40,}$ ]] || usage_error 'that does not look like a tunnel token (a long base64 value starting with eyJ)'

  if ((skip_access_check)); then
    warn 'not checking that Cloudflare Access protects the hostname (--skip-access-check)'
  else
    command -v curl >/dev/null 2>&1 || die 'curl is needed to check that Cloudflare Access protects the hostname'
    result=$(curl -sS -o /dev/null -m 20 -w '%{http_code} %{redirect_url}' "https://$host/" 2>/dev/null || true)
    code=${result%% *}
    location=${result#* }
    if [[ $code != 302 || $location != https://*.cloudflareaccess.com/* ]]; then
      env_values[LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN]=$token
      env_update
      die "https://$host/ is not protected by Cloudflare Access yet (an anonymous request must be redirected to <team>.cloudflareaccess.com; got '${code:-no answer}'). The token is saved, but the tunnel stays off. Set up Access as the guide says, then run ./setup.sh cloudflare again."
    fi
    say "Cloudflare Access protects https://$host/."
  fi
  env_values[LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN]=$token
  env_values[LIFE_OS_PUBLIC_URL]=$url
  env_values[LIFE_OS_PUBLIC_HOSTNAME]=$host
  list_add COMPOSE_PROFILES cloudflare
  env_update
  say "The tunnel starts with Life OS; it serves https://$host."
  apply_hint cloudflared gateway notifications
}

setup_google() {
  local id secret redirect
  intro 'Google Calendar' google-calendar.md
  require_env
  if is_set LIFE_OS_GOOGLE_CLIENT_ID; then
    change_existing 'Google Calendar' || return 0
  fi
  redirect=$(env_get LIFE_OS_GOOGLE_REDIRECT_URI 2>/dev/null) || redirect=$(env_get LIFE_OS_PUBLIC_URL)/calendar/google/callback
  cat <<EOF
You need an OAuth client of type "Web application" from your own Google Cloud
project, with the Google Calendar API on and the consent screen in production.
Its authorized redirect URI must be exactly:
  $redirect
EOF
  if [[ $redirect != https://* && $redirect != http://127.0.0.1[:/]* && $redirect != http://localhost[:/]* ]]; then
    warn 'Google accepts a plain http redirect URI only on 127.0.0.1 or localhost. The guide shows how to connect through an SSH tunnel instead.'
  fi
  if ((assume_yes)); then
    id=${LIFE_OS_GOOGLE_CLIENT_ID:-}
    secret=${LIFE_OS_GOOGLE_CLIENT_SECRET:-}
  else
    ask id 'Client ID'
    ask_secret secret 'Client secret (input is hidden)'
  fi
  [[ $id =~ ^[A-Za-z0-9._-]+\.apps\.googleusercontent\.com$ ]] || usage_error 'the client ID ends in .apps.googleusercontent.com'
  [[ -n $secret && $secret =~ ^[A-Za-z0-9_-]+$ ]] || usage_error 'the client secret is missing or has unexpected characters'
  env_values[LIFE_OS_GOOGLE_CLIENT_ID]=$id
  env_values[LIFE_OS_GOOGLE_CLIENT_SECRET]=$secret
  is_set LIFE_OS_GOOGLE_TOKEN_KEY || env_values[LIFE_OS_GOOGLE_TOKEN_KEY]=$(random_base64 32)
  env_update
  say 'Google Calendar is set up. Connect your account in Life OS: Calendar > Google Calendar > Connect Google.'
  apply_hint google-calendar
}

setup_banking() {
  local id key redirect
  intro 'Bank sync (Enable Banking)' bank-sync.md
  require_env
  if is_set LIFE_OS_ENABLE_BANKING_APP_ID; then
    change_existing 'Bank sync' || return 0
  fi
  redirect=$(env_get LIFE_OS_BANKING_REDIRECT_URI 2>/dev/null) || redirect=$(env_get LIFE_OS_PUBLIC_URL)/finance/bank/callback
  cat <<EOF
You need an Enable Banking application in production, with account
information only, and its private key file (.pem) on this server. Its
redirect URL must be exactly:
  $redirect
EOF
  if ((assume_yes)); then
    id=${LIFE_OS_ENABLE_BANKING_APP_ID:-}
    key=${LIFE_OS_ENABLE_BANKING_KEY_FILE:-}
  else
    ask id 'Application ID'
    ask key 'Path to its private key file (.pem)'
  fi
  [[ $id =~ ^[A-Za-z0-9-]{8,}$ ]] || usage_error 'the application ID is missing or has unexpected characters'
  [[ -n $key ]] || usage_error 'the private key file is missing'
  key=$(realpath -- "$key" 2>/dev/null) || usage_error 'the private key file does not exist'
  [[ -f $key && -r $key ]] || usage_error "cannot read the private key file $key"
  grep -q -- '-----BEGIN .*PRIVATE KEY-----' "$key" || usage_error "$key is not a PEM private key"
  [[ $(stat -c %u "$key") == "$(id -u)" ]] \
    || warn "$key is not yours: the bank service runs as $(id -un) and must be able to read it"
  [[ $(stat -c %a "$key") =~ ^[0-7]00$ ]] \
    || warn "others can read $key: make it private with chmod 600 '$key'"
  env_values[LIFE_OS_ENABLE_BANKING_APP_ID]=$id
  env_values[LIFE_OS_ENABLE_BANKING_KEY_FILE]=$key
  env_values[LIFE_OS_BANKING_RUNTIME_UID]=$(id -u)
  is_set LIFE_OS_BANKING_TOKEN_KEY || env_values[LIFE_OS_BANKING_TOKEN_KEY]=$(random_base64 32)
  env_update
  say 'Bank sync is set up. Connect your bank in Life OS: Finance > Bank connection.'
  apply_hint banking
}

# The ntfy image compose.yaml runs.
ntfy_image() {
  docker compose --profile ntfy config --images 2>/dev/null | grep -m 1 -E '(^|/)ntfy[:@]' || true
}

# The bcrypt hash of the password on stdin (ntfy's own tool, offline).
ntfy_hash() {
  local image=$1 password=$2 hash
  # shellcheck disable=SC2016 # the $ signs are bcrypt's, not variables
  hash=$(printf '%s\n%s\n' "$password" "$password" | docker run --rm -i --network none "$image" user hash 2>/dev/null \
    | grep -o -m 1 -E '\$2[aby]\$[0-9]{2}\$[./A-Za-z0-9]{53}') || true
  [[ -n $hash ]] || die "ntfy's image ($image) did not hash the password"
  printf '%s\n' "$hash"
}

setup_ntfy() {
  local url host password again image
  intro 'Push notifications with ntfy' notifications.md#ntfy
  require_env
  list_has COMPOSE_PROFILES cloudflare \
    || die 'ntfy is reached only through the Cloudflare Tunnel: run ./setup.sh cloudflare first'
  if is_set LIFE_OS_NTFY_PHONE_PASSWORD_HASH && list_has COMPOSE_PROFILES ntfy; then
    change_existing 'ntfy' || return 0
  fi
  cat <<'EOF'
ntfy gets a hostname of its own in the same tunnel, WITHOUT Cloudflare Access
(the ntfy app cannot log in through it): a published application route from
that hostname to HTTP ntfy:80. ntfy itself refuses everyone but two accounts:
Life OS, which may only post, and your phone, which may only read.
EOF
  url=$(env_get LIFE_OS_NTFY_PUBLIC_URL 2>/dev/null || true)
  [[ -z ${LIFE_OS_NTFY_PUBLIC_URL:-} ]] || url=$LIFE_OS_NTFY_PUBLIC_URL
  ask url 'ntfy address (https://...)' "$url"
  if ! url=$(normalize_url "$url") || [[ $url != https://* ]]; then
    usage_error 'the ntfy address must be https://<hostname>, such as https://ntfy.example.com'
  fi
  host=$(url_hostname "$url")
  [[ -n $host && $host != "$(env_get LIFE_OS_PUBLIC_HOSTNAME 2>/dev/null || true)" ]] \
    || usage_error "ntfy needs a hostname of its own, not Life OS's"
  if ((assume_yes)); then
    password=${LIFE_OS_SETUP_NTFY_PHONE_PASSWORD:-}
    [[ -n $password ]] || usage_error 'set LIFE_OS_SETUP_NTFY_PHONE_PASSWORD to the password the ntfy app will use'
  else
    say 'Choose the password the ntfy app on your phone will use (user lifeos-phone);'
    say 'keep it in your password manager. Only its hash is stored.'
    ask_secret password 'Phone password (input is hidden)'
    ask_secret again 'The same password again'
    [[ $password == "$again" ]] || die 'the two passwords differ; nothing was changed'
  fi
  ((${#password} >= 12)) || usage_error 'use a phone password of at least 12 characters'
  check_docker
  image=$(ntfy_image)
  [[ -n $image ]] || die 'compose.yaml has no ntfy service'
  say "Hashing with $image..."
  env_values[LIFE_OS_NTFY_PHONE_PASSWORD_HASH]=$(ntfy_hash "$image" "$password")
  is_set LIFE_OS_NTFY_PUBLISHER_PASSWORD_HASH \
    || env_values[LIFE_OS_NTFY_PUBLISHER_PASSWORD_HASH]=$(ntfy_hash "$image" "$(random_hex 24)")
  is_set LIFE_OS_NTFY_PUBLISHER_TOKEN \
    || env_values[LIFE_OS_NTFY_PUBLISHER_TOKEN]=tk_$(random_hex 15 | cut -c 1-29)
  env_values[LIFE_OS_NTFY_PUBLIC_URL]=$url
  list_add COMPOSE_PROFILES ntfy
  list_add LIFE_OS_NOTIFICATIONS_CHANNELS ntfy
  env_update
  say "ntfy is set up. In the ntfy app: add the user lifeos-phone for $url, then subscribe to the topic $(env_get LIFE_OS_NTFY_TOPIC 2>/dev/null || printf lifeos) on that server."
  apply_hint ntfy notifications cloudflared
}

# A new VAPID key pair as two lines, public then private: an uncompressed
# P-256 point and its 32-byte scalar, base64url without padding.
vapid_keys() {
  local dir der
  command -v openssl >/dev/null 2>&1 || die 'openssl is needed to make the web push keys'
  dir=$(mktemp -d)
  der=$dir/key.der
  openssl ecparam -name prime256v1 -genkey -noout -outform DER -out "$der" 2>/dev/null \
    || {
      rm -rf "$dir"
      die 'openssl could not make a P-256 key'
    }
  # SEC1 DER of a P-256 key: the scalar at byte 8, the point at byte 57.
  if [[ $(head -c 7 "$der" | od -An -v -tx1 | tr -d ' \n') != 30770201010420 ]] \
    || [[ $(tail -c +40 "$der" | head -c 17 | od -An -v -tx1 | tr -d ' \n') != a00a06082a8648ce3d030107a144034200 ]]; then
    rm -rf "$dir"
    die 'openssl wrote a key in an unexpected layout'
  fi
  tail -c +57 "$der" | head -c 65 | base64 | tr -d '\n=' | tr '+/' '-_'
  printf '\n'
  tail -c +8 "$der" | head -c 32 | base64 | tr -d '\n=' | tr '+/' '-_'
  printf '\n'
  rm -rf "$dir"
}

setup_webpush() {
  local url subject keys
  intro 'Push notifications to the phone app' notifications.md#the-life-os-phone-app
  require_env
  if is_set LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY && list_has LIFE_OS_NOTIFICATIONS_CHANNELS webpush; then
    change_existing 'Push to the phone app' || return 0
  fi
  cat <<'EOF'
The Life OS phone app shows Life OS's notifications as pushes. They travel,
end-to-end encrypted, through the phone's push service (Google's FCM).
EOF
  url=$(env_get LIFE_OS_PUBLIC_URL)
  subject=$(env_get LIFE_OS_WEBPUSH_SUBJECT 2>/dev/null || true)
  [[ -z ${LIFE_OS_WEBPUSH_SUBJECT:-} ]] || subject=$LIFE_OS_WEBPUSH_SUBJECT
  if [[ $url != https://* && -z $subject ]]; then
    say 'Push services want a way to reach the sender: an email address, since Life OS has no https address.'
    ask subject 'Your email address'
    [[ -z $subject || $subject == mailto:* ]] || subject=mailto:$subject
  fi
  if [[ -n $subject ]]; then
    [[ $subject =~ ^mailto:[^@[:space:]]+@[^@[:space:]]+$ || $subject =~ ^https://[^[:space:]]+$ ]] \
      || usage_error "the contact must be an email address or an https URL, not '$subject'"
    env_values[LIFE_OS_WEBPUSH_SUBJECT]=$subject
  elif [[ $url != https://* ]]; then
    usage_error 'without an https address, set LIFE_OS_WEBPUSH_SUBJECT to mailto:<your address>'
  fi
  if ! is_set LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY; then
    keys=$(vapid_keys)
    env_values[LIFE_OS_WEBPUSH_VAPID_PUBLIC_KEY]=${keys%%$'\n'*}
    env_values[LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY]=${keys#*$'\n'}
  fi
  list_add LIFE_OS_NOTIFICATIONS_CHANNELS webpush
  env_update
  say 'Push to the phone app is set up. Turn on notifications in the app (Settings).'
  apply_hint notifications
}

setup_feed() {
  local name url gateway body status tmp
  intro 'Calendar feeds (ICS)' calendar-feeds.md
  require_env
  command -v curl >/dev/null 2>&1 || die 'curl is needed to add a feed'
  gateway=$(gateway_url)
  curl -fsS -o /dev/null -m 10 "$gateway/api/integrations/calendar-feeds/ready" 2>/dev/null \
    || die "Life OS does not answer at $gateway; start it first (./setup.sh up)"
  if ((assume_yes)); then
    name=${LIFE_OS_SETUP_FEED_NAME:-}
    url=${LIFE_OS_SETUP_FEED_URL:-}
  else
    say 'A feed is an https:// or webcal:// address of an .ics calendar (holidays, sports, school, waste pickup...).'
    ask name 'Name in Life OS'
    ask url 'Feed address'
  fi
  [[ -n $name && ${#name} -le 100 && ! $name =~ [\"\\[:cntrl:]] ]] || usage_error 'the name is missing, too long, or has quotes or backslashes'
  [[ $url =~ ^(https|webcal)://[^[:space:]\"\\]+$ ]] || usage_error 'the feed address must start with https:// or webcal://'
  body=$(printf '{"name":"%s","url":"%s"}' "$name" "$url")
  tmp=$(mktemp)
  status=$(curl -sS -m 60 -o "$tmp" -w '%{http_code}' -X POST \
    -H 'content-type: application/json' -H 'X-Life-OS-Actor: dev:setup' \
    --data-binary "$body" "$gateway/api/integrations/calendar-feeds/v1/feeds" 2>/dev/null || true)
  if [[ $status == 201 ]]; then
    rm -f "$tmp"
    say "Subscribed to \"$name\". Life OS downloads it now and every night; see Calendar."
  else
    say "Life OS refused the feed (HTTP ${status:-no answer}):"
    cat "$tmp" >&2
    printf '\n' >&2
    rm -f "$tmp"
    exit 1
  fi
}

cmd_gateway() {
  require_env
  step 'Gateway address'
  gateway_values
  env_update
  say "Life OS: $(env_get LIFE_OS_PUBLIC_URL) (listening on $(gateway_bind):$(gateway_port))."
  apply_hint gateway notifications
}

# ---------- Running ----------

# Waits until the gateway and the core services answer; prints what is
# still starting or failing.
wait_until_ready() {
  local gateway deadline service state health exit_code processor_down=0 whisper_starting=0
  local -a problems=() domains=(tasks inbox calendar finance food) waiting
  gateway=$(gateway_url)
  deadline=$((SECONDS + ${LIFE_OS_SETUP_WAIT_SECONDS:-300}))
  if ! command -v curl >/dev/null 2>&1; then
    warn "curl is not installed, so this cannot wait for Life OS: check ./setup.sh status in a minute."
    return 0
  fi
  step "Waiting for Life OS to answer at $gateway"
  while true; do
    waiting=()
    curl -fsS -o /dev/null -m 5 "$gateway/" 2>/dev/null || waiting+=(dashboard)
    for service in "${domains[@]}"; do
      curl -fsS -o /dev/null -m 5 "$gateway/api/$service/ready" 2>/dev/null || waiting+=("$service")
    done
    ((${#waiting[@]})) || break
    if ((SECONDS >= deadline)); then
      warn "still not answering: ${waiting[*]}. See ./setup.sh status and docker compose logs <service>."
      return 1
    fi
    sleep 3
  done
  say 'Life OS answers.'
  while IFS='|' read -r service state health exit_code; do
    [[ -n $service ]] || continue
    case $service in
      *-migrate) [[ $state != exited || $exit_code == 0 ]] || problems+=("$service (exit $exit_code)") ;;
      processor) [[ $state == running ]] || processor_down=1 ;;
      whisper) [[ $health == healthy ]] || whisper_starting=1 ;;
      *) [[ $state == running ]] || problems+=("$service ($state)") ;;
    esac
  done < <(docker compose ps --all --format '{{.Service}}|{{.State}}|{{.Health}}|{{.ExitCode}}' 2>/dev/null || true)
  if ((processor_down)); then
    if ai_ready; then
      problems+=('processor (not running)')
    elif [[ $(ai_provider) == claude ]]; then
      say "AI processing is off until you set Claude Code's token (./setup.sh claude); notes wait in Inbox."
    else
      say 'AI processing is off until you sign in to Codex (./setup.sh codex); notes wait in Inbox.'
    fi
  fi
  ((!whisper_starting)) || say 'Whisper is still starting (it downloads its model the first time); voice notes wait until it is ready.'
  if ((${#problems[@]})); then
    warn "not running: ${problems[*]}. Look at docker compose logs <service>."
  fi
  return 0
}

codex_signed_in() {
  local home
  home=$(env_get LIFE_OS_CODEX_HOME_HOST_PATH 2>/dev/null) || return 1
  [[ $home == /* && -s $home/auth.json ]]
}

# The AI provider .env names: codex (the default) or claude.
ai_provider() {
  local provider
  provider=$(env_get LIFE_OS_AI_PROVIDER 2>/dev/null) || provider=
  printf '%s\n' "${provider:-codex}"
}

# Whether that provider can run: Codex signed in, or Claude Code's token set.
ai_ready() {
  if [[ $(ai_provider) == claude ]]; then
    is_set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN
  else
    codex_signed_in
  fi
}

print_access() {
  local url bind port
  url=$(env_get LIFE_OS_PUBLIC_URL)
  bind=$(gateway_bind)
  port=$(gateway_port)
  step 'Open Life OS'
  if [[ $url == https://* ]]; then
    say "  $url"
    say "  (and on this server: http://127.0.0.1:$port)"
  elif [[ $(address_class "$bind") == loopback ]]; then
    cat <<EOF
  It answers only on this server: http://127.0.0.1:$port
  From your computer, open an SSH tunnel and keep it running:
      ssh -N -L $port:127.0.0.1:$port $(id -un)@<this server>
  then open http://127.0.0.1:$port in your browser.
EOF
  else
    say "  $url  (every device that reaches $bind can open it)"
  fi
  say "For everyday use from your phone and laptop, use a VPN or Cloudflare Access: $docs/access.md"
}

print_next_steps() {
  local -a next=()
  if [[ $(ai_provider) == claude ]]; then
    ai_ready || next+=('./setup.sh claude     turn on AI processing with Claude Code')
  elif ! ai_ready; then
    next+=('./setup.sh codex      turn on AI processing of your notes')
    next+=('./setup.sh claude     or with Claude Code and a Claude plan instead')
  fi
  is_set LIFE_OS_GOOGLE_CLIENT_ID || next+=('./setup.sh google     sync with Google Calendar')
  next+=('./setup.sh feed       subscribe to a calendar feed (ICS)')
  next+=('./setup.sh status     what runs and what is set up')
  step 'Next steps (all optional)'
  printf '  %s\n' "${next[@]}"
  say "Everything else: $docs/README.md"
}

cmd_up() {
  require_release_dir up
  require_env
  preview_banner
  ((skip_checks)) || run_checks
  check_bind "$(gateway_bind)"
  step "Starting Life OS $(env_get LIFE_OS_VERSION 2>/dev/null || printf '%s' "$release_version")"
  docker compose up -d --pull missing
  wait_until_ready || true
  print_access
  print_next_steps
}

# Adds the settings a newer .env.example has and .env lacks: new passwords
# generated, everything else as the example has it.
add_new_settings() {
  local line key
  local -a added=() lines=()
  [[ -f .env.example ]] || return 0
  mapfile -t lines <.env.example
  for line in "${lines[@]}"; do
    [[ $line =~ ^(LIFE_OS_[A-Z0-9_]*)= ]] || continue
    key=${BASH_REMATCH[1]}
    env_get "$key" >/dev/null 2>&1 && continue
    if [[ $key == *_PASSWORD ]]; then
      env_values[$key]=$(random_hex 24)
    else
      env_values[$key]=$(env_get "$key" .env.example)
    fi
    added+=("$key")
  done
  if ((${#added[@]})); then
    env_update
    say "New settings added to .env: ${added[*]}"
  fi
}

cmd_update() {
  local target current
  require_release_dir update
  require_env
  preview_banner
  ((skip_checks)) || run_checks
  check_bind "$(gateway_bind)"
  current=$(env_get LIFE_OS_VERSION 2>/dev/null || printf 'preview')
  target=${1:-$release_version}
  [[ $target =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || usage_error "'$target' is not a version"
  if [[ $target == "$current" ]]; then
    step "Updating the $current images"
  else
    step "Updating from $current to $target"
  fi
  add_new_settings

  if ((skip_backup)); then
    warn 'not backing up first (--skip-backup)'
  else
    step 'Backing up first'
    [[ -n $(docker compose ps -q --status running backup 2>/dev/null) ]] \
      || die "Life OS is not running, so it cannot back up first. Start it with ./setup.sh up, or update without a backup: ./setup.sh update --skip-backup."
    docker compose exec -T backup /service now -actor dev:setup \
      || die 'the backup failed, so nothing was updated. The Status page says why; fix it, or update without a backup: ./setup.sh update --skip-backup.'
  fi

  step 'Downloading the new images'
  LIFE_OS_VERSION=$target docker compose pull || die "could not download the $target images; nothing was changed"
  env_values[LIFE_OS_VERSION]=$target
  env_update
  step 'Restarting'
  docker compose up -d --remove-orphans
  wait_until_ready || true
  say ''
  say "Life OS $target is running. Read what changed: https://github.com/GAMch1k/life-os/releases"
}

cmd_status() {
  local gateway answer
  require_env
  step "Life OS $(env_get LIFE_OS_VERSION 2>/dev/null || printf '(no version set)') in $PWD"
  gateway=$(gateway_url)
  if command -v curl >/dev/null 2>&1 && curl -fsS -o /dev/null -m 5 "$gateway/" 2>/dev/null; then
    answer='answers'
  else
    answer='does not answer'
  fi
  say "Gateway $gateway $answer. Address: $(env_get LIFE_OS_PUBLIC_URL 2>/dev/null || printf 'not set')."
  if command -v docker >/dev/null 2>&1; then
    step 'Containers'
    docker compose ps --all --format 'table {{.Service}}\t{{.State}}\t{{.Status}}' 2>/dev/null \
      || warn 'docker compose cannot read this project'
  fi
  step 'Set up'
  if [[ $(ai_provider) == claude ]]; then
    status_line 'AI processing (claude)' "$(on_off is_set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN)"
  else
    status_line 'AI processing (codex)' "$(codex_signed_in && printf 'signed in' || printf 'not signed in')"
  fi
  status_line 'Whisper model (whisper)' "$(whisper_model)"
  status_line 'Cloudflare Tunnel (cloudflare)' "$(on_off list_has COMPOSE_PROFILES cloudflare)"
  status_line 'Google Calendar (google)' "$(on_off is_set LIFE_OS_GOOGLE_CLIENT_ID)"
  status_line 'Bank sync (banking)' "$(on_off is_set LIFE_OS_ENABLE_BANKING_APP_ID)"
  status_line 'ntfy push (ntfy)' "$(on_off list_has COMPOSE_PROFILES ntfy)"
  status_line 'Phone app push (webpush)' "$(on_off list_has LIFE_OS_NOTIFICATIONS_CHANNELS webpush)"
  say "The Status page in Life OS ($gateway/status) has the details."
}

# The Whisper model .env names, such as small.
whisper_model() {
  local model
  model=$(env_get LIFE_OS_WHISPER_MODEL_PATH 2>/dev/null || printf 'ggml-small.bin')
  model=${model##*/}
  model=${model#ggml-}
  printf '%s\n' "${model%.bin}"
}

on_off() { if "$@"; then printf 'set up'; else printf 'not set up'; fi; }
status_line() { printf '  %-32s %s\n' "$1" "$2"; }

cmd_check() {
  preview_banner
  run_checks
  [[ ! -f .env ]] || check_bind "$(gateway_bind)"
  say ''
  say 'Checks done.'
}

usage() {
  cat <<EOF
Life OS setup helper ($release_version). Run it from the directory with the
release's compose.yaml and .env.example.

  ./setup.sh [options]          first-time setup: checks this server, then
                                writes .env with new passwords
  ./setup.sh up                 start Life OS and wait until it answers
  ./setup.sh update [VERSION]   back up, download newer images, restart
                                (default: this script's release, $release_version)
  ./setup.sh status             what runs, and which integrations are set up
  ./setup.sh check              only check this server
  ./setup.sh gateway            change where Life OS listens and its address

Integrations (each optional, any time):
  ./setup.sh codex              AI processing: sign in to OpenAI Codex
  ./setup.sh claude             AI processing: Claude Code instead of Codex
  ./setup.sh whisper            choose the speech-to-text model
  ./setup.sh cloudflare         Cloudflare Tunnel (behind Cloudflare Access)
  ./setup.sh google             Google Calendar sync
  ./setup.sh banking            bank sync through Enable Banking
  ./setup.sh ntfy               push notifications through ntfy
  ./setup.sh webpush            push notifications to the Life OS phone app
  ./setup.sh feed               subscribe to a calendar feed (ICS)

Options:
  -y, --yes                no questions: defaults, or values from the
                           environment (see $docs/README.md#without-questions)
  --force-new              write a new .env over an existing one; keeps a copy
                           and the database passwords
  --bind ADDRESS           where the gateway listens (default 127.0.0.1)
  --allow-public-bind      allow a public --bind address (Life OS has no login!)
  --port PORT              the gateway's port (default 3000)
  --public-url URL         the address you open Life OS at
  --skip-checks            don't check Docker, memory, and disk first
  --skip-backup            update without backing up first
  --skip-access-check      cloudflare: don't check that Access protects the hostname

Guide: $docs/README.md
EOF
}

# ---------- Main ----------

command=
declare -a args=()
need_value() { [[ -n ${2-} && ${2-} != -* ]] || usage_error "$1 needs a value"; }
while (($#)); do
  case $1 in
    -y | --yes) assume_yes=1 ;;
    --force-new) force_new=1 ;;
    --skip-checks) skip_checks=1 ;;
    --skip-backup) skip_backup=1 ;;
    --skip-access-check) skip_access_check=1 ;;
    --allow-public-bind) allow_public_bind=1 ;;
    --bind) need_value "$@" && bind_flag=$2 && shift ;;
    --bind=*) bind_flag=${1#*=} ;;
    --port) need_value "$@" && port_flag=$2 && shift ;;
    --port=*) port_flag=${1#*=} ;;
    --public-url) need_value "$@" && public_url_flag=$2 && shift ;;
    --public-url=*) public_url_flag=${1#*=} ;;
    -h | --help) command=help ;;
    -*) usage_error "unknown option $1" ;;
    *)
      if [[ -z $command ]]; then command=$1; else args+=("$1"); fi
      ;;
  esac
  shift
done

case ${command:-init} in
  init) cmd_init ;;
  up) cmd_up ;;
  update) cmd_update "${args[@]}" ;;
  status) cmd_status ;;
  check) cmd_check ;;
  gateway) cmd_gateway ;;
  codex) setup_codex ;;
  claude) setup_claude ;;
  whisper) setup_whisper ;;
  cloudflare) setup_cloudflare ;;
  google) setup_google ;;
  banking) setup_banking ;;
  ntfy) setup_ntfy ;;
  webpush) setup_webpush ;;
  feed) setup_feed ;;
  help) usage ;;
  *) usage_error "unknown command '$command'" ;;
esac
