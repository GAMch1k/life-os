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
#   ./setup.sh modules [on|off ID...]
#                               the optional modules: list, turn on, turn off
#   ./setup.sh setup-code       a one-time code for the first account
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
reset_2fa=0
keep_devices=0
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

# The comma-separated list in KEY (such as COMPOSE_PROFILES): as a pending
# change has it (env_values), else as .env has it.
list_current() {
  if [[ -v env_values[$1] ]]; then
    printf '%s\n' "${env_values[$1]}"
  else
    env_get "$1" || true
  fi
}

# Adds NAME to the comma-separated list in KEY.
list_add() {
  local key=$1 name=$2 current
  current=$(list_current "$key")
  if [[ ,$current, == *",$name,"* ]]; then
    env_values[$key]=$current
  else
    env_values[$key]=${current:+$current,}$name
  fi
}

# Removes NAME from the comma-separated list in KEY.
list_remove() {
  local key=$1 name=$2 item result=''
  local -a items=()
  IFS=, read -r -a items <<<"$(list_current "$key")"
  for item in "${items[@]}"; do
    [[ -z $item || $item == "$name" ]] || result+=${result:+,}$item
  done
  env_values[$key]=$result
}

list_has() {
  [[ ,$(list_current "$1"), == *",$2,"* ]]
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

# ---------- One Life OS per machine ----------

# Every Life OS is the Compose project lifeos (compose.yaml's name), with the
# same lifeos-* volumes and networks, so a machine runs one. other_install
# describes the containers of a lifeos project started from another
# directory, such as "37 containers of the Compose project lifeos, started
# from /srv/life-os"; nothing when there are none. It fails when Docker cannot
# list its containers.
other_install() {
  local listing line dir where=
  local -i count=0
  local -A dirs=()
  listing=$(docker ps -a --filter label=com.docker.compose.project=lifeos \
    --format '{{.Label "com.docker.compose.project.working_dir"}}|{{.Label "com.docker.compose.service"}}' 2>/dev/null) || return 1
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    dir=${line%%|*}
    # This directory, also when reached through a symbolic link, is this install.
    [[ -n $dir && $dir -ef . ]] && continue
    count+=1
    dirs[${dir:-an unknown directory}]=1
  done <<<"$listing"
  ((count)) || return 0
  for dir in "${!dirs[@]}"; do where+=${where:+, }$dir; done
  printf '%d container%s of the Compose project lifeos, started from %s\n' "$count" "$( ((count == 1)) || printf s)" "$where"
}

# Refuses a command that runs Compose here while another directory's Life OS
# runs on this machine: Compose would take that install over and recreate its
# containers with this directory's .env, passwords and all.
require_own_install() {
  local found
  found=$(other_install) \
    || die "Docker cannot list its containers, so this cannot check that no other Life OS runs on this machine. Is Docker running? (./setup.sh check)"
  [[ -z $found ]] || die "another Life OS runs on this machine: Docker has $found. Every Life OS is that same Compose project, with the same lifeos-* volumes, so ./setup.sh $1 here would take that install over: Compose would recreate its containers with this directory's .env and passwords. Nothing was changed. Manage that install from its own directory; one machine runs one Life OS. If you moved it here from there, remove its old containers first (docker ps -a --filter label=com.docker.compose.project=lifeos lists them; the data stays in the lifeos-* volumes), then run this again."
}

# Says so when another directory's Life OS runs on this machine.
warn_other_install() {
  local found
  found=$(other_install 2>/dev/null) || return 0
  [[ -z $found ]] || warn "another Life OS runs on this machine: Docker has $found. One machine runs one Life OS: up, update, and codex refuse to run here while it does."
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

# ---------- Modules ----------

# Life OS's core always runs: the Dashboard, Tasks, Calendar, Inbox, Food,
# Finance, notifications in Life OS, and backups. Every other part is an
# optional module (docs/install/modules.md), a Compose profile of its own
# name: it runs when COMPOSE_PROFILES in .env lists it. A first setup turns
# none on. Off, a module has no containers; its data (its database and
# volumes) stays, so turning it on again picks up where it was.

# BEGIN generated by tools/modules from modules/*.yaml: do not edit here
# The optional modules (modules/*.yaml), in the order setup.sh offers them:
# each is the Compose profile of its id, and runs when COMPOSE_PROFILES
# lists it. The core always runs.
# shellcheck disable=SC2034 # read where this is used
module_ids=(ai voice google-calendar banking cloudflare ntfy server-health)
declare -A module_name=(
  [ai]='AI processing'
  [voice]='Voice notes'
  [google-calendar]='Google Calendar'
  [banking]='Bank sync'
  [cloudflare]='Cloudflare Tunnel'
  [ntfy]='ntfy push'
  [server-health]='Server health'
)
declare -A module_summary=(
  [ai]='files your notes with OpenAI Codex or Claude Code (your own plan)'
  [voice]='record notes; Whisper transcribes them on this server (up to 2 GB of memory while it does)'
  [google-calendar]='two-way sync with Google Calendar (your own Google Cloud OAuth client)'
  [banking]='your bank account in Finance, read-only, through Enable Banking'
  [cloudflare]='reach Life OS from anywhere through Cloudflare Tunnel and Access'
  [ntfy]='push notifications through your own ntfy server'
  [server-health]='memory, disks, load, and containers of this server on the Status page'
)
declare -A module_caution=(
  [ai]='Your notes, and the tasks and events the AI looks at to understand them, are sent to OpenAI or Anthropic.'
  [server-health]='It reads the host'\''s Docker socket (through a read-only gateway), which lists every container on this server.'
)
declare -A module_requires=(
  [ntfy]='cloudflare'
)
declare -A module_setup=(
  [ai]='codex'
  [voice]='whisper'
  [google-calendar]='google'
  [banking]='banking'
  [cloudflare]='cloudflare'
  [ntfy]='ntfy'
)
declare -A module_services=(
  [ai]='mcp processor'
  [voice]='voice-migrate voice voice-worker whisper'
  [google-calendar]='google-calendar-migrate google-calendar'
  [banking]='banking-migrate banking'
  [cloudflare]='cloudflared'
  [ntfy]='ntfy'
  [server-health]='host-health host-health-docker'
)
# END generated by tools/modules

module_known() { [[ -v module_name[$1] ]]; }

# The optional modules that are on, in setup.sh's order, one a line.
modules_on() {
  local id
  for id in "${module_ids[@]}"; do
    if module_is_on "$id"; then printf '%s\n' "$id"; fi
  done
}

module_is_on() { list_has COMPOSE_PROFILES "$1"; }

# The modules on, as "ai, voice", or "none".
modules_on_text() {
  local list
  list=$(modules_on | paste -sd, - | sed 's/,/, /g')
  printf '%s\n' "${list:-none}"
}

require_module() {
  module_known "$1" || usage_error "there is no module '$1': the modules are ${module_ids[*]}"
}

# Turns module ID on in env_values (applied with env_update): after the
# modules it requires, which must be on.
module_enable() {
  local id=$1 required home
  require_module "$id"
  for required in ${module_requires[$id]:-}; do
    module_is_on "$required" \
      || die "the $id module needs the $required module: turn that on first (./setup.sh ${module_setup[$required]:-modules on $required})"
  done
  module_is_on "$id" && return 0
  list_add COMPOSE_PROFILES "$id"
  [[ -z ${module_caution[$id]:-} ]] || say "Note: ${module_caution[$id]}"
  # The AI sign-in's directory, which the processor mounts.
  if [[ $id == ai ]] && ! is_checkout; then
    home=$(env_get LIFE_OS_CODEX_HOME_HOST_PATH 2>/dev/null) || home=
    [[ $home != /* ]] || make_dir "$home" 700
  fi
  return 0
}

# Turns module ID off in env_values; refuses while a module that needs it
# is on.
module_disable() {
  local id=$1 other
  require_module "$id"
  for other in $(modules_on); do
    [[ $other == "$id" || " ${module_requires[$other]:-} " != *" $id "* ]] \
      || die "the $other module needs the $id module: turn $other off first (./setup.sh modules off $other $id)"
  done
  list_remove COMPOSE_PROFILES "$id"
  # Notifications must not publish to an ntfy that is not there.
  [[ $id != ntfy ]] || ! list_has LIFE_OS_NOTIFICATIONS_CHANNELS ntfy || list_remove LIFE_OS_NOTIFICATIONS_CHANNELS ntfy
}

# This install's containers, by service, one a line: those of the Compose
# project lifeos started from this directory.
install_services() {
  local listing line dir
  listing=$(docker ps -a --filter label=com.docker.compose.project=lifeos \
    --format '{{.Label "com.docker.compose.project.working_dir"}}|{{.Label "com.docker.compose.service"}}' 2>/dev/null) || return 1
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    dir=${line%%|*}
    [[ -n $dir && $dir -ef . ]] && printf '%s\n' "${line#*|}"
  done <<<"$listing" | sort -u
}

# Removes the containers of the modules that are off (a module's containers
# are the services its manifest lists). Volumes and databases stay. Only in
# the release files: a clone says what to run instead.
modules_remove_off() {
  local id service running
  local -a gone=()
  running=$(install_services) || return 0
  for id in "${module_ids[@]}"; do
    module_is_on "$id" && continue
    for service in ${module_services[$id]}; do
      grep -qxF "$service" <<<"$running" && gone+=("$service")
    done
  done
  ((${#gone[@]})) || return 0
  docker compose rm --stop --force "${gone[@]}" >/dev/null \
    || die "could not remove the containers of the modules that are off: ${gone[*]}"
  say "Removed the containers of the modules that are off: ${gone[*]}. Their data stays."
}

# How to apply a change of modules: in the release files, ./setup.sh up; in
# a clone, the Compose commands.
modules_apply_hint() {
  local -a started=() stopped=() services=()
  local id
  for id in "$@"; do
    if module_is_on "$id"; then
      read -r -a services <<<"${module_services[$id]}"
      started+=("${services[@]}")
    else
      read -r -a services <<<"${module_services[$id]}"
      stopped+=("${services[@]}")
    fi
  done
  if ! is_checkout; then
    say 'Apply it with: ./setup.sh up'
    return 0
  fi
  ((!${#started[@]})) || say "Start it with: docker compose up -d --no-build --no-deps ${started[*]} (build them first if needed), then recreate the dashboard and gateway"
  ((!${#stopped[@]})) || say "Remove its containers with: docker compose rm --stop --force ${stopped[*]}, then recreate the dashboard and gateway"
}

# Turns modules on as ./setup.sh modules on does: a module that needs
# setting up first is set up (the tunnel checks Access, ntfy needs its
# accounts, AI needs a sign-in); the others are switched on, and say what
# else they need.
module_turn_on() {
  local id=$1 choice
  require_module "$id"
  case $id in
    cloudflare | ntfy)
      "setup_${module_setup[$id]}"
      return
      ;;
    ai)
      if ! ai_ready; then
        ((!assume_yes)) || die 'AI processing needs a sign-in first: ./setup.sh codex (OpenAI Codex) or ./setup.sh claude (Claude Code)'
        if is_checkout && [[ $(ai_provider) == codex ]]; then
          die 'AI processing needs a sign-in first: in a clone, sign in with the Codex CLI on the server itself (SETUP.md, step 5), or use ./setup.sh claude'
        fi
        say 'AI processing needs a sign-in: OpenAI Codex (1, with your ChatGPT plan) or Claude Code (2, with your Claude plan or an Anthropic API key).'
        choice=1
        [[ $(ai_provider) != claude ]] || choice=2
        ask choice 'Use (1 or 2)' "$choice"
        case $choice in
          1 | codex) setup_codex ;;
          2 | claude) setup_claude ;;
          *) usage_error "answer 1 (Codex) or 2 (Claude Code), not '$choice'" ;;
        esac
        return
      fi
      ;;
  esac
  module_enable "$id"
  env_update
  say "${module_name[$id]} ($id) is on."
  case $id in
    google-calendar) is_set LIFE_OS_GOOGLE_CLIENT_ID || say 'Set it up: ./setup.sh google' ;;
    banking) is_set LIFE_OS_ENABLE_BANKING_APP_ID || say 'Set it up: ./setup.sh banking' ;;
  esac
}

cmd_modules() {
  local action=${1:-list} id
  require_env
  case $action in
    list)
      step 'Modules'
      status_line 'Core (always on)' 'Dashboard, Tasks, Calendar, Inbox, Food, Finance, notifications, backups'
      for id in "${module_ids[@]}"; do
        if module_is_on "$id"; then
          status_line "${module_name[$id]} ($id)" "on: $(module_detail "$id")"
        else
          status_line "${module_name[$id]} ($id)" "off: ${module_summary[$id]}"
        fi
      done
      say ''
      say 'Turn one on: ./setup.sh modules on <id>; off: ./setup.sh modules off <id>.'
      say "Off, a module's containers are removed; its data stays. Guide: $docs/modules.md"
      ;;
    on | off)
      shift
      (($#)) || usage_error "name the modules to turn $action, such as ./setup.sh modules $action voice"
      for id in "$@"; do require_module "$id"; done
      if [[ $action == on ]]; then
        for id in "$@"; do module_turn_on "$id"; done
        modules_apply_hint "$@"
      else
        # The containers go at once in the release files: first make sure
        # they are this directory's Life OS.
        local remove=0
        if ! is_checkout && command -v docker >/dev/null 2>&1; then
          require_own_install modules
          remove=1
        fi
        for id in "$@"; do
          if module_is_on "$id"; then
            module_disable "$id"
            env_update
            say "${module_name[$id]} ($id) is off. Its data stays: turning it on again picks it up."
          else
            say "${module_name[$id]} ($id) is already off."
          fi
        done
        if ((remove)); then
          modules_remove_off
          say 'Apply the rest (the Dashboard and gateway learn of it) with: ./setup.sh up'
        else
          modules_apply_hint "$@"
        fi
      fi
      ;;
    *) usage_error "unknown modules command '$action': use ./setup.sh modules, modules on <id>, or modules off <id>" ;;
  esac
}

# What a module that is on has set up, for status and modules (never a value).
module_detail() {
  case $1 in
    ai)
      if [[ $(ai_provider) == claude ]]; then
        printf 'Claude Code, %s' "$(claude_status)"
      elif codex_signed_in; then
        printf 'Codex, signed in'
      else
        printf 'Codex, not signed in: ./setup.sh codex'
      fi
      ;;
    voice)
      printf 'Whisper %s, language %s' "$(whisper_model)" "$(whisper_language)"
      whisper_language_known || printf ' (unknown: ./setup.sh whisper)'
      ;;
    google-calendar) on_off is_set LIFE_OS_GOOGLE_CLIENT_ID; is_set LIFE_OS_GOOGLE_CLIENT_ID || printf ': ./setup.sh google' ;;
    banking) on_off is_set LIFE_OS_ENABLE_BANKING_APP_ID; is_set LIFE_OS_ENABLE_BANKING_APP_ID || printf ': ./setup.sh banking' ;;
    cloudflare) on_off is_set LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN ;;
    ntfy) if list_has LIFE_OS_NOTIFICATIONS_CHANNELS ntfy; then printf 'set up, a notification channel'; else printf 'not a notification channel: ./setup.sh ntfy'; fi ;;
    *) printf '%s' "${module_summary[$1]}" ;;
  esac
}

# An .env from before modules has no LIFE_OS_MODULES_LISTED: every module
# ran. This turns on those the install used, so that the update changes
# nothing it relied on: the tunnel and ntfy as COMPOSE_PROFILES had them,
# voice notes and server health, which needed nothing set up, AI processing
# when it is signed in, and Google Calendar and bank sync when they are set
# up. The AI processor without a sign-in only restarted again and again: it
# stays off.
modules_migrate() {
  local id
  local -a off=()
  [[ -z $(env_get LIFE_OS_MODULES_LISTED 2>/dev/null || true) ]] || return 0
  step 'Optional modules'
  list_add COMPOSE_PROFILES voice
  list_add COMPOSE_PROFILES server-health
  ! ai_ready || list_add COMPOSE_PROFILES ai
  ! is_set LIFE_OS_GOOGLE_CLIENT_ID || list_add COMPOSE_PROFILES google-calendar
  ! is_set LIFE_OS_ENABLE_BANKING_APP_ID || list_add COMPOSE_PROFILES banking
  env_values[LIFE_OS_MODULES_LISTED]=yes
  env_update
  for id in "${module_ids[@]}"; do
    module_is_on "$id" || off+=("$id")
  done
  say 'Life OS now has optional modules: the core always runs, and each module runs only when it is on.'
  say "Kept on, as this install used them: $(modules_on_text)."
  ((!${#off[@]})) || say "Off: ${off[*]} (not set up here)."
  say "Turn one on or off with ./setup.sh modules (guide: $docs/modules.md)."
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
# bank sessions, the web push key pair, and the accounts' secret and keys
# (two-factor secrets are encrypted with LIFE_OS_AUTH_SECRET).
kept_keys() {
  password_keys
  printf '%s\n' LIFE_OS_GOOGLE_TOKEN_KEY LIFE_OS_BANKING_TOKEN_KEY \
    LIFE_OS_WEBPUSH_VAPID_PUBLIC_KEY LIFE_OS_WEBPUSH_VAPID_PRIVATE_KEY \
    LIFE_OS_AUTH_SECRET LIFE_OS_IDENTITY_SIGNING_KEY LIFE_OS_TRUSTED_KEYS
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
    die "a .env already exists here, so nothing was changed. To change one part, run ./setup.sh gateway, ./setup.sh modules, ./setup.sh <integration>, or edit .env. To start over, run ./setup.sh --force-new (it keeps a copy of the old .env and its database passwords)."
  fi
  [[ -f .env.example ]] || die ".env.example is missing here: download it from the same release as this script (see $docs/README.md)"
  if ((skip_checks)); then
    warn 'skipping the server checks (--skip-checks)'
  else
    run_checks
    warn_other_install
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
  # The core alone: every optional module is off until it is turned on
  # below or later (./setup.sh modules).
  env_values[COMPOSE_PROFILES]=
  env_values[LIFE_OS_MODULES_LISTED]=yes

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
  # The accounts' secret and keys, unless kept.
  account_keys_needed && generated+=1

  step 'Writing .env'
  make_dir "$backup_dir" 700
  # The AI sign-in's directory comes when AI processing is turned on.
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
    backups "$backup_dir"
  ((generated == 0)) || printf '  %-17s %s\n' passwords "$generated generated (never shown)"
  say 'Keep a copy of .env in a password manager or another safe place: backups do not include it.'

  setup_modules_now
  printf '  %-17s %s\n' modules "the core, and: $(modules_on_text)"

  step 'Next'
  if is_checkout; then
    say 'Build and start Life OS as SETUP.md describes.'
  else
    say 'Start Life OS:  ./setup.sh up'
  fi
}

# After a first setup: each optional module, and push to the phone app, now
# or later.
setup_modules_now() {
  local id
  local -a requested=()
  if ((assume_yes)); then
    # Without questions: the modules LIFE_OS_SETUP_MODULES names, and each
    # integration whose settings are in the environment (which turns its
    # module on).
    IFS=', ' read -r -a requested <<<"${LIFE_OS_SETUP_MODULES:-}"
    for id in "${requested[@]}"; do
      [[ -z $id ]] || require_module "$id"
    done
    for id in "${requested[@]}"; do
      [[ -z $id ]] || module_turn_on "$id"
    done
    [[ -z ${LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN:-}${LIFE_OS_ANTHROPIC_API_KEY:-} ]] || setup_claude
    [[ -z ${LIFE_OS_SETUP_WHISPER_MODEL:-}${LIFE_OS_WHISPER_LANGUAGE:-} ]] || setup_whisper
    [[ -z ${LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN:-} ]] || setup_cloudflare
    [[ -z ${LIFE_OS_GOOGLE_CLIENT_ID:-} ]] || setup_google
    [[ -z ${LIFE_OS_ENABLE_BANKING_APP_ID:-} ]] || setup_banking
    [[ -z ${LIFE_OS_NTFY_PUBLIC_URL:-} ]] || setup_ntfy
    [[ ${LIFE_OS_SETUP_WEBPUSH:-} != on ]] || setup_webpush
    return 0
  fi
  step 'Optional modules'
  say 'Life OS runs its core: the Dashboard, Tasks, Calendar, Inbox, Food, Finance,'
  say 'notifications, and backups. Each optional module can be turned on now, or later'
  say 'with ./setup.sh modules on <id>:'
  for id in "${module_ids[@]}"; do
    printf '  %-16s %s\n' "$id" "${module_summary[$id]}"
  done
  if confirm 'Turn some on now?' n; then
    for id in "${module_ids[@]}"; do
      # ntfy needs the tunnel: offered only once that is on.
      [[ -z ${module_requires[$id]:-} ]] || module_is_on "${module_requires[$id]}" || continue
      if confirm "Turn on ${module_name[$id]} ($id)?" n; then
        # A module with settings is set up at once; module_turn_on sets up
        # those that need it before they run.
        case $id in
          voice) setup_whisper ;;
          google-calendar) setup_google ;;
          banking) setup_banking ;;
          *) module_turn_on "$id" ;;
        esac
      fi
    done
  else
    say 'Later: ./setup.sh modules lists them; ./setup.sh modules on <id> turns one on.'
  fi
  if confirm 'Set up notifications on your phone through the Life OS app (web push) now?' n; then
    setup_webpush
  fi
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
  require_own_install codex
  home=$(env_get LIFE_OS_CODEX_HOME_HOST_PATH) || die 'LIFE_OS_CODEX_HOME_HOST_PATH is missing from .env'
  if [[ $home == /* ]]; then
    make_dir "$home" 700
  fi
  mount=$home:/codex-login
  # The processor's own image and user, with the sign-in written to the
  # directory the processor reads (read-only) at every start. The release's
  # image, not a local one with Claude Code (LIFE_OS_PROCESSOR_IMAGE).
  LIFE_OS_PROCESSOR_IMAGE='' docker compose run --rm --no-deps --entrypoint codex -e CODEX_HOME=/codex-login -v "$mount" \
    processor login --device-auth || die 'the Codex sign-in did not finish; run ./setup.sh codex to try again'
  LIFE_OS_PROCESSOR_IMAGE='' docker compose run --rm --no-deps -T --entrypoint codex -e CODEX_HOME=/codex-login -v "$mount" \
    processor login status || die 'Codex says it is not signed in; run ./setup.sh codex to try again'
  if ! module_is_on ai; then
    module_enable ai
    env_update
    say 'AI processing (the ai module) is on.'
  fi
  if [[ $(ai_provider) != codex ]]; then
    env_values[LIFE_OS_AI_PROVIDER]=codex
    # The processor runs the release's image again; ./setup.sh up removes
    # the local one with Claude Code.
    env_values[LIFE_OS_PROCESSOR_IMAGE]=
    env_update
    say 'AI processing now uses Codex instead of Claude Code (LIFE_OS_AI_PROVIDER=codex).'
    apply_hint processor
  elif [[ -n $(docker compose ps -q processor 2>/dev/null) ]]; then
    docker compose restart processor >/dev/null
    say 'Restarted the processor with the new sign-in.'
  else
    apply_hint mcp processor
  fi
  say 'AI processing is set up. The Status page shows the Codex allowance that is left.'
}

# Claude Code instead of Codex, signed in with the owner's choice: their
# Claude plan (the long-lived token claude setup-token prints on any
# computer) or an Anthropic API key, billed per use by Anthropic. Never both.
# On a release install it also adds Claude Code to the processor image on
# this server (claude_image_build), as the published images never contain
# it; a clone builds it from source (LIFE_OS_AI_PROVIDERS).
setup_claude() {
  local kind current token='' key='' model version providers rebuild=0
  intro 'AI processing (Claude Code)' ai-processing.md#claude-code-instead-of-codex
  require_env
  is_checkout || [[ -f compose.yaml ]] || die "compose.yaml is missing here: download it from the same release as this script"
  current=$(claude_sign_in)
  if [[ $(ai_provider) == claude && $current != none ]]; then
    change_existing 'AI processing with Claude Code' || return 0
  fi
  cat <<'EOF'
Instead of Codex, Life OS's AI can run Claude Code, Anthropic's AI agent, with
your own Claude plan or Anthropic API key:
  * Your notes, and the tasks and events the AI looks at to understand them,
    are sent to Anthropic. Leave this off if you don't want that.
  * Claude Code is Anthropic's software, and your use of it is under
    Anthropic's terms: https://code.claude.com/docs/en/legal-and-compliance
EOF
  if ! is_checkout; then
    cat <<'EOF'
  * Life OS's images do not include Claude Code. This step downloads it from
    Anthropic's official npm package (@anthropic-ai/claude-code, the version
    this release pins, checked against its sha512) and adds it, unmodified,
    to a copy of the processor image on this server: about 330 MB.
EOF
  fi
  if ((assume_yes)); then
    token=${LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN:-}
    key=${LIFE_OS_ANTHROPIC_API_KEY:-}
    [[ -z $token || -z $key ]] \
      || usage_error 'set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN (your Claude plan) or LIFE_OS_ANTHROPIC_API_KEY (an Anthropic API key), not both'
    if [[ -n $key ]]; then kind=api-key; else kind=plan; fi
  else
    confirm 'Use Claude Code for AI processing?' y || return 0
    cat <<'EOF'

How should Claude Code sign in?
  1  your Claude plan (Pro or Max): on any computer with Claude Code, run
     claude setup-token, sign in, and paste the token it prints. Each note
     uses a little of the plan's usage limits; no separate bill.
  2  an Anthropic API key (sk-ant-api...), from the Claude Console. API use
     is billed per use by Anthropic.
EOF
    kind=1
    [[ $current != api-key ]] || kind=2
    ask kind 'Sign in with (1 or 2)' "$kind"
    case $kind in
      1 | plan) kind=plan ;;
      2 | api | key | api-key) kind=api-key ;;
      *) usage_error "answer 1 (your Claude plan) or 2 (an Anthropic API key), not '$kind'" ;;
    esac
    if [[ $kind == plan ]]; then
      ask_secret token 'Token from claude setup-token (input is hidden)'
    else
      ask_secret key 'Anthropic API key (input is hidden)'
    fi
  fi
  if [[ $kind == plan ]]; then
    [[ $token != sk-ant-api* ]] \
      || usage_error 'that is an Anthropic API key, which bills the API per use: to use it, choose the API key (2; with --yes, set LIFE_OS_ANTHROPIC_API_KEY); for your plan, paste the token claude setup-token prints'
    [[ -n $token && $token =~ ^[A-Za-z0-9_-]+$ ]] \
      || usage_error 'the token is missing or has unexpected characters: paste what claude setup-token prints (or, with --yes, set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN or LIFE_OS_ANTHROPIC_API_KEY)'
  else
    [[ $key != sk-ant-oat* ]] \
      || usage_error "that is a Claude plan token from claude setup-token, not an API key: choose your Claude plan (1; with --yes, set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN)"
    [[ $key =~ ^sk-ant-api[A-Za-z0-9_-]+$ ]] \
      || usage_error 'that does not look like an Anthropic API key (sk-ant-api...): create one in the Claude Console'
  fi
  model=$(claude_model)
  if ((!assume_yes)); then
    cat <<EOF

Which Claude model should Life OS use? Enter keeps the current one ($model).
  sonnet  balanced, the default
  opus    the most capable; uses your plan's limits or API credit faster
  haiku   the fastest
  or a full model name, such as claude-sonnet-4-5
EOF
  fi
  ask model 'Model' "${LIFE_OS_CLAUDE_MODEL:-$model}"
  model=${model,,}
  valid_claude_model "$model" \
    || usage_error "unknown Claude model '$model': choose sonnet, opus, or haiku, or a full model name such as claude-sonnet-4-5"

  if is_checkout; then
    # A clone builds Claude Code into its processor image from source, when
    # LIFE_OS_AI_PROVIDERS names it.
    providers=$(env_get LIFE_OS_AI_PROVIDERS 2>/dev/null) || providers=
    providers=${providers:-codex}
    if [[ ,$providers, != *,claude,* ]]; then
      env_values[LIFE_OS_AI_PROVIDERS]=$providers,claude
      rebuild=1
    fi
  else
    version=$(env_get LIFE_OS_VERSION 2>/dev/null) || version=$release_version
    check_docker
    if claude_image_current "$version"; then
      say "Claude Code $(claude_code_pinned) is already in this server's processor image for $version ($(claude_image_tag "$version"))."
    else
      claude_image_build "$version"
    fi
    env_values[LIFE_OS_PROCESSOR_IMAGE]=$(claude_image_tag "$version")
  fi
  env_values[LIFE_OS_AI_PROVIDER]=claude
  env_values[LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN]=$token
  env_values[LIFE_OS_ANTHROPIC_API_KEY]=$key
  env_values[LIFE_OS_CLAUDE_MODEL]=$model
  module_enable ai
  env_update
  if [[ $kind == plan ]]; then
    say "AI processing uses Claude Code with your Claude plan, model $model. To go back to Codex: ./setup.sh codex."
  else
    say "AI processing uses Claude Code with your Anthropic API key (billed per use by Anthropic), model $model. To go back to Codex: ./setup.sh codex."
  fi
  ((!rebuild)) \
    || say "This clone's processor image gets Claude Code now (LIFE_OS_AI_PROVIDERS=$providers,claude): build it first with docker compose build processor."
  apply_hint mcp processor
}

# The Claude models setup accepts: an alias, or a full model name.
valid_claude_model() {
  [[ $1 == sonnet || $1 == opus || $1 == haiku || $1 =~ ^claude-[a-z0-9.-]+$ ]]
}

# The Claude model .env names (default sonnet).
claude_model() {
  local model
  model=$(env_get LIFE_OS_CLAUDE_MODEL 2>/dev/null) || model=
  printf '%s\n' "${model:-sonnet}"
}

# How Claude Code signs in, as .env says: plan, api-key, both (which the
# processor refuses), or none.
claude_sign_in() {
  if is_set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN && is_set LIFE_OS_ANTHROPIC_API_KEY; then
    printf 'both\n'
  elif is_set LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN; then
    printf 'plan\n'
  elif is_set LIFE_OS_ANTHROPIC_API_KEY; then
    printf 'api-key\n'
  else
    printf 'none\n'
  fi
}

# ---------- Claude Code on this server ----------

# Writes the files of the local Claude Code build (services/mcp/claude-code in
# the repository: its Dockerfile, install.sh, package.json, and
# package-lock.json, which pins the npm package's version and sha512) into the
# directory $1. tools/build-install-bundle writes them in here when it makes a
# release's setup.sh; a copy from the repository has none, and fails.
claude_build_files() {
  : __LIFE_OS_CLAUDE_CODE_FILES__
  return 1
}

# The Claude Code version this script's build pins, or nothing.
claude_code_pinned() {
  local dir
  dir=$(mktemp -d)
  if claude_build_files "$dir"; then
    sed -n 's/^ *"@anthropic-ai\/claude-code": *"\([^"]*\)".*/\1/p' "$dir/package.json"
  fi
  rm -rf "$dir"
}

# The local image with Claude Code of a Life OS version.
claude_image_tag() { printf 'lifeos-processor-claude:%s\n' "$1"; }

# The release's processor image of a version, as compose.yaml names it.
release_processor_image() {
  local registry=${LIFE_OS_IMAGE_REGISTRY:-}
  [[ -n $registry ]] || registry=$(env_get LIFE_OS_IMAGE_REGISTRY 2>/dev/null) || registry=
  printf '%s/life-os-processor:%s\n' "${registry:-ghcr.io/gamch1k}" "$1"
}

# What the local build starts FROM: the release image this server has, by its
# registry digest (exactly that image), else by name.
release_processor_base() {
  local image=$1 digest
  digest=$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$image" 2>/dev/null \
    | grep -m 1 -F "${image%:*}@") || digest=
  printf '%s\n' "${digest:-$image}"
}

# Builds lifeos-processor-claude:VERSION: the release's processor image of
# VERSION, as this server has it (else downloaded), with Claude Code's native
# binary added, from Anthropic's npm package at the version and integrity this
# script pins. Needs the network for npm (and the registry, for a missing
# release image).
claude_image_build() {
  local version=$1 release base tag dir pinned
  release=$(release_processor_image "$version")
  tag=$(claude_image_tag "$version")
  dir=$(mktemp -d)
  if ! claude_build_files "$dir"; then
    rm -rf "$dir"
    die "this setup.sh has no Claude Code build: it is a copy from the repository. Use the setup.sh of a release ($docs/README.md)"
  fi
  pinned=$(sed -n 's/^ *"@anthropic-ai\/claude-code": *"\([^"]*\)".*/\1/p' "$dir/package.json")
  step "Adding Claude Code $pinned to the processor image ($version)"
  say "Downloading Claude Code from Anthropic's npm package; this takes a minute."
  if ! docker image inspect "$release" >/dev/null 2>&1; then
    docker pull "$release" >/dev/null || {
      rm -rf "$dir"
      die "could not download $release; nothing was changed"
    }
  fi
  base=$(release_processor_base "$release")
  if ! docker build --tag "$tag" --build-arg "LIFE_OS_PROCESSOR_BASE=$base" \
    --label "life-os.processor-base=$base" --label "life-os.version=$version" \
    --label "life-os.claude-code=$pinned" "$dir"; then
    rm -rf "$dir"
    die "adding Claude Code to the processor image failed (above); nothing was changed. Run ./setup.sh claude to try again."
  fi
  rm -rf "$dir"
  say "Built $tag: the $version processor with Claude Code $pinned."
}

# Whether the local image of VERSION is there, built on the release image
# this server has now, with the Claude Code this script pins.
claude_image_current() {
  local tag built pinned
  tag=$(claude_image_tag "$1")
  built=$(docker image inspect --format '{{index .Config.Labels "life-os.processor-base"}}' "$tag" 2>/dev/null) || return 1
  pinned=$(docker image inspect --format '{{index .Config.Labels "life-os.claude-code"}}' "$tag" 2>/dev/null) || return 1
  [[ -n $built && $built == "$(release_processor_base "$(release_processor_image "$1")")" && $pinned == "$(claude_code_pinned)" ]]
}

# Whether this install runs Claude Code from a local image: the release
# files, with LIFE_OS_AI_PROVIDER=claude.
claude_local() {
  ! is_checkout && [[ $(ai_provider) == claude ]]
}

# Before up: with Claude Code, the processor must run the local image of the
# version in .env. A missing one is built again; one of another version, or
# none, is refused, as the processor would run without Claude Code or on
# another version than the rest of Life OS. With Codex, a leftover image
# setting goes.
claude_image_check_up() {
  local version image tag
  # The processor runs only with AI processing on.
  module_is_on ai || return 0
  version=$(env_get LIFE_OS_VERSION 2>/dev/null) || version=$release_version
  image=$(env_get LIFE_OS_PROCESSOR_IMAGE 2>/dev/null) || image=
  tag=$(claude_image_tag "$version")
  if ! claude_local; then
    if [[ -n $image ]]; then
      env_values[LIFE_OS_PROCESSOR_IMAGE]=
      env_update
      say 'AI processing uses Codex, so the processor runs the release image again (LIFE_OS_PROCESSOR_IMAGE cleared in .env).'
    fi
    return 0
  fi
  [[ -n $image ]] \
    || die "AI processing is set to Claude Code, but Claude Code is not on this server yet: run ./setup.sh claude (or ./setup.sh codex). Nothing was started."
  [[ $image == "$tag" ]] \
    || die "LIFE_OS_PROCESSOR_IMAGE in .env is $image, but Life OS is $version: the processor would run another version. Run ./setup.sh claude to add Claude Code to $version (or ./setup.sh codex). Nothing was started."
  if ! docker image inspect "$tag" >/dev/null 2>&1; then
    warn "$tag is missing on this server; building it again."
    claude_image_build "$version"
  fi
}

# Removes the local Claude Code images the processor no longer runs: those of
# other versions, and all of them once AI processing uses Codex. An image a
# container still uses stays.
claude_images_prune() {
  local keep image removed=0
  keep=$(env_get LIFE_OS_PROCESSOR_IMAGE 2>/dev/null) || keep=
  while IFS= read -r image; do
    [[ -n $image && $image != "$keep" && $image == lifeos-processor-claude:* ]] || continue
    docker image rm "$image" >/dev/null 2>&1 && removed=1
  done < <(docker image ls --format '{{.Repository}}:{{.Tag}}' lifeos-processor-claude 2>/dev/null || true)
  # Earlier builds of the same tag (an update of a moving tag such as preview).
  while IFS= read -r image; do
    [[ -n $image ]] || continue
    docker image rm "$image" >/dev/null 2>&1 && removed=1
  done < <(docker image ls --quiet --filter dangling=true --filter label=life-os.claude-code 2>/dev/null || true)
  ((!removed)) || say 'Removed the local Claude Code images the processor no longer runs.'
}

# The Claude Code sign-in and model, as status reports them (never a value).
claude_status() {
  case $(claude_sign_in) in
    plan) printf 'set up: your Claude plan, model %s' "$(claude_model)" ;;
    api-key) printf 'set up: Anthropic API key (billed per use), model %s' "$(claude_model)" ;;
    both) printf 'two sign-ins in .env; keep one: ./setup.sh claude' ;;
    *) printf 'not set up: ./setup.sh claude' ;;
  esac
}

# The local Claude Code image, as status reports it.
claude_image_status() {
  local version image tag pinned
  version=$(env_get LIFE_OS_VERSION 2>/dev/null) || version=$release_version
  image=$(env_get LIFE_OS_PROCESSOR_IMAGE 2>/dev/null) || image=
  tag=$(claude_image_tag "$version")
  if [[ -z $image ]]; then
    printf 'not on this server: ./setup.sh claude'
  elif [[ $image != "$tag" ]]; then
    printf '%s, not for %s: ./setup.sh claude' "$image" "$version"
  elif ! pinned=$(docker image inspect --format '{{index .Config.Labels "life-os.claude-code"}}' "$tag" 2>/dev/null); then
    printf '%s is missing: ./setup.sh up builds it again' "$tag"
  elif ! claude_image_current "$version"; then
    printf 'Claude Code %s, built on another %s image or Claude Code than this setup.sh has: ./setup.sh claude builds it again' "$pinned" "$version"
  else
    printf 'Claude Code %s in %s, matches %s' "$pinned" "$tag" "$version"
  fi
}

# The language codes whisper.cpp knows: services/voice/whisper/languages.txt
# in the repository, which the release does not ship (tools/test-setup checks
# that this copy is the same). Whisper itself refuses a code not in it.
whisper_languages='af am ar as az ba be bg bn bo br bs ca cs cy da de el en es et eu fa fi fo fr gl gu ha haw he hi hr ht hu hy id is it ja jw ka kk km kn ko la lb ln lo lt lv mg mi mk ml mn mr ms mt my ne nl nn no oc pa pl ps pt ro ru sa sd si sk sl sn so sq sr su sv sw ta te tg th tk tl tr tt uk ur uz vi yi yo yue zh'

# Whether a Whisper language is auto or one code whisper.cpp knows.
valid_whisper_language() {
  [[ $1 == auto ]] || [[ $1 =~ ^[a-z]+$ && " $whisper_languages " == *" $1 "* ]]
}

setup_whisper() {
  local model memory current language
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
  if ((!assume_yes)); then
    cat <<EOF

Whisper guesses the language of each voice note (auto), and short notes are
sometimes guessed wrong. If you speak one language only, name it: that is more
accurate, and a bit faster. Codes such as en, de, ru, uk, fr, es, it, or pl
(every one: $docs/voice.md#language).
EOF
  fi
  ask language 'Language (auto, or a code)' "${LIFE_OS_WHISPER_LANGUAGE:-$(whisper_language)}"
  language=${language,,}
  valid_whisper_language "$language" \
    || usage_error "unknown Whisper language '$language': use auto or one of whisper.cpp's codes, such as de, ru, or en (every one: $docs/voice.md#language)"
  env_values[LIFE_OS_WHISPER_MODEL_PATH]=/models/ggml-$model.bin
  env_values[LIFE_OS_WHISPER_MODEL_URL]=https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-$model.bin
  env_values[LIFE_OS_WHISPER_MEMORY]=$memory
  env_values[LIFE_OS_WHISPER_LANGUAGE]=$language
  module_enable voice
  env_update
  say "Voice notes are on. Whisper uses the $model model (memory cap $memory); it downloads it at its next start."
  if [[ $language == auto ]]; then
    say 'Language: auto (guessed for each voice note).'
  else
    say "Language: $language (every voice note is transcribed as $language)."
  fi
  apply_hint voice-migrate whisper voice voice-worker
}

setup_cloudflare() {
  local url host token result code location
  intro 'Cloudflare Tunnel and Access' access.md#cloudflare-tunnel-and-access
  require_env
  if is_set LIFE_OS_CLOUDFLARE_TUNNEL_TOKEN && module_is_on cloudflare; then
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
  module_enable cloudflare
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
  module_enable google-calendar
  env_update
  say 'Google Calendar is set up, and its module is on. Connect your account in Life OS: Calendar > Google Calendar > Connect Google.'
  apply_hint google-calendar-migrate google-calendar
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
  module_enable banking
  env_update
  say 'Bank sync is set up, and its module is on. Connect your bank in Life OS: Finance > Bank connection.'
  apply_hint banking-migrate banking
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
  module_is_on cloudflare \
    || die 'ntfy is reached only through the Cloudflare Tunnel: run ./setup.sh cloudflare first'
  if is_set LIFE_OS_NTFY_PHONE_PASSWORD_HASH && module_is_on ntfy; then
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
  module_enable ntfy
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

# ---------- Accounts ----------

# A new Ed25519 key pair as two lines of JSON: the private JWK (with its kid)
# for LIFE_OS_IDENTITY_SIGNING_KEY, and the public one for
# LIFE_OS_TRUSTED_KEYS. Needs openssl 1.1.1 or newer.
identity_key_pair() {
  local kid=$1 dir d x
  command -v openssl >/dev/null 2>&1 || die 'openssl is needed to make the sign-in keys'
  dir=$(mktemp -d)
  if ! openssl genpkey -algorithm ed25519 -outform DER -out "$dir/key.der" 2>/dev/null \
    || ! openssl pkey -inform DER -in "$dir/key.der" -pubout -outform DER -out "$dir/pub.der" 2>/dev/null; then
    rm -rf "$dir"
    die 'openssl could not make an Ed25519 key (it needs openssl 1.1.1 or newer)'
  fi
  # PKCS#8 of an Ed25519 key: the 32-byte seed after a 16-byte prefix;
  # SubjectPublicKeyInfo: the 32-byte key after a 12-byte prefix.
  if [[ $(head -c 16 "$dir/key.der" | od -An -v -tx1 | tr -d ' \n') != 302e020100300506032b657004220420 ]] \
    || [[ $(head -c 12 "$dir/pub.der" | od -An -v -tx1 | tr -d ' \n') != 302a300506032b6570032100 ]]; then
    rm -rf "$dir"
    die 'openssl wrote an Ed25519 key in an unexpected layout'
  fi
  d=$(tail -c 32 "$dir/key.der" | base64 | tr -d '\n=' | tr '+/' '-_')
  x=$(tail -c 32 "$dir/pub.der" | base64 | tr -d '\n=' | tr '+/' '-_')
  rm -rf "$dir"
  printf '{"kty":"OKP","crv":"Ed25519","kid":"%s","x":"%s","d":"%s"}\n' "$kid" "$x" "$d"
  printf '{"kty":"OKP","crv":"Ed25519","kid":"%s","x":"%s","use":"sig","alg":"EdDSA"}\n' "$kid" "$x"
}

# A new key id, such as auth-20261114-3fa2.
identity_kid() { printf 'auth-%s-%s\n' "$(date -u +%Y%m%d)" "$(random_hex 2)"; }

# The public JWK of a private one (drops "d").
public_jwk() {
  sed -E 's/,"d":"[^"]*"//; s/\}$/,"use":"sig","alg":"EdDSA"}/' <<<"$1"
}

# The value a key has: a pending change (env_values), else .env's.
current_value() {
  if [[ -v env_values[$1] ]]; then printf '%s\n' "${env_values[$1]}"; else env_get "$1" 2>/dev/null || true; fi
}

# Whether a setting has no value yet: empty, or still the example's
# replace-with-* placeholder. A value someone set is never replaced, even
# one in another format than setup.sh writes.
unset_value() { [[ -z $1 || $1 == replace-with-* ]]; }

# A new key pair in key_pair_private and key_pair_public, or the script
# stops: identity_key_pair runs in a subshell, where its die only ends the
# subshell, and a caller in an || or && list runs without set -e.
new_key_pair() {
  local pair
  pair=$(identity_key_pair "$(identity_kid)") || die 'could not make a signing key'
  key_pair_private=${pair%%$'\n'*}
  key_pair_public=${pair#*$'\n'}
  [[ $key_pair_private == '{"kty":"OKP"'*'"d":"'*'}' && $key_pair_public == '{"kty":"OKP"'*'}' && $key_pair_public != *'"d":'* ]] \
    || die 'could not make a signing key: openssl wrote something unexpected'
}

# A JWKS with one more key: the list in $1 (empty, or {"keys":[...]}) and
# the public JWK $2.
jwks_with() {
  local list=$1 key=$2 end=']}'
  if [[ -z $list || $list == '{"keys":[]}' ]]; then
    printf '{"keys":[%s]}' "$key"
  else
    printf '%s,%s]}' "${list%"$end"}" "$key"
  fi
}

# Sets the accounts' secret and keys that are missing (or still the
# example's placeholder): LIFE_OS_AUTH_SECRET, and the signing key with the
# trusted keys that list it. Values that are set stay as they are. Fails
# (returns 1) when nothing was missing.
account_keys_needed() {
  local -i made=0
  local signing trusted key_pair_private key_pair_public
  if unset_value "$(current_value LIFE_OS_AUTH_SECRET)"; then
    env_values[LIFE_OS_AUTH_SECRET]=$(random_hex 32)
    made=1
  fi
  signing=$(current_value LIFE_OS_IDENTITY_SIGNING_KEY)
  trusted=$(current_value LIFE_OS_TRUSTED_KEYS)
  unset_value "$trusted" && trusted=
  if unset_value "$signing"; then
    new_key_pair
    env_values[LIFE_OS_IDENTITY_SIGNING_KEY]=$key_pair_private
    env_values[LIFE_OS_TRUSTED_KEYS]=$(jwks_with "$trusted" "$key_pair_public")
    made=1
  elif [[ -z $trusted ]]; then
    [[ $signing == '{'*'"x":"'*'}' ]] \
      || die 'LIFE_OS_IDENTITY_SIGNING_KEY is not a JWK, so its public key cannot go into LIFE_OS_TRUSTED_KEYS: fix it in .env (docs/install/accounts.md#keys)'
    env_values[LIFE_OS_TRUSTED_KEYS]=$(jwks_with '' "$(public_jwk "$signing")")
    made=1
  fi
  ((made))
}

# account-keys: the accounts' settings an .env from before accounts lacks,
# made here: the auth_db password, the secret, the signing key with the
# trusted keys, and legacy access on, so that the install keeps working as
# it did. ./setup.sh update does this for a release install; this is for a
# clone of the repository, which update leaves alone. Never prints a value.
cmd_account_keys() {
  local key
  local -a added=()
  require_env
  is_set LIFE_OS_AUTH_DB_PASSWORD || env_values[LIFE_OS_AUTH_DB_PASSWORD]=$(random_hex 24)
  account_keys_needed || true
  env_get LIFE_OS_LEGACY_ACCESS >/dev/null 2>&1 || env_values[LIFE_OS_LEGACY_ACCESS]=on
  for key in LIFE_OS_INSECURE_LOGIN_HOSTS LIFE_OS_REQUIRE_CLOUDFLARE_ACCESS; do
    env_get "$key" >/dev/null 2>&1 || env_values[$key]=
  done
  for key in "${!env_values[@]}"; do added+=("$key"); done
  if ((${#added[@]} == 0)); then
    say 'The accounts are set up in .env already: nothing to add.'
    return 0
  fi
  env_update
  say "Added to .env: $(printf '%s\n' "${added[@]}" | sort | tr '\n' ' ')"
  say 'Keep a copy of .env with your backups: auth_db needs LIFE_OS_AUTH_SECRET.'
}

dashboard_running() {
  [[ -n $(docker compose ps -q --status running dashboard 2>/dev/null) ]]
}

# Runs one of the Dashboard's account commands (apps/dashboard/scripts/
# lifeos-auth.mjs) in its container, which has auth_db's address.
account_command() {
  docker compose exec -T dashboard node scripts/lifeos-auth.mjs "$@"
}

require_dashboard() {
  require_env
  command -v docker >/dev/null 2>&1 || die 'Docker is not installed here.'
  dashboard_running || die "the Dashboard is not running, so this cannot reach the accounts. Start Life OS first (./setup.sh up)."
}

# After up and update: while no account exists, a setup code for the first.
print_setup_code() {
  local out
  dashboard_running || return 0
  out=$(account_command setup-code --if-no-account 2>/dev/null) || return 0
  [[ -n $out ]] || return 0
  step 'The first account'
  printf '%s\n' "$out"
}

cmd_setup_code() {
  require_dashboard
  account_command setup-code
}

cmd_recover_owner() {
  require_dashboard
  local -a args=()
  ((!reset_2fa)) || args+=(--reset-2fa)
  account_command recover-owner "${args[@]}"
}

cmd_legacy_access() {
  require_dashboard
  case ${1:-} in
    '' | on | off) ;;
    *) usage_error "legacy-access takes on or off, not '$1'" ;;
  esac
  account_command legacy-access ${1:+"$1"}
}

cmd_sign_out_all() {
  local out
  local -a args=()
  require_dashboard
  ((!keep_devices)) || args+=(--keep-devices)
  if ((keep_devices)); then
    ((assume_yes)) || confirm 'Sign out every session, API token, and connected app of every account, yours too (paired phones and watches stay)?' n || die 'nothing was changed'
  else
    ((assume_yes)) || confirm 'Sign out every session, paired phone and watch, API token, and connected app of every account, yours too?' n || die 'nothing was changed'
  fi
  out=$(account_command sign-out-all "${args[@]}") || die "signing out failed: $out"
  printf '%s\n' "$out"
  # While legacy access is on, a request without a sign-in is the owner
  # anyway: offer to turn it off (with --yes, only say how).
  if [[ $out == *'Legacy access is on'* ]] && ((!assume_yes)); then
    if confirm 'Turn legacy access off too?' n; then
      account_command legacy-access off
    fi
  fi
  if module_is_on cloudflare; then
    say "Cloudflare Access keeps its own sessions: end them in Zero Trust as well (${docs%/install}/operations/cloudflare.md#sign-everyone-out)."
  fi
}

# rotate-keys [auth|all]: a new signing key. The services trust the new and
# the current key first, then the Dashboard signs with the new one; the
# key before the current one is dropped. Tokens live five minutes, so the
# overlap is enough. Service keys come with a later version (P21-07).
cmd_rotate_keys() {
  local target=${1:-auth} signing services key_pair_private key_pair_public
  require_env
  case $target in
    auth | all) ;;
    *) usage_error "rotate-keys rotates auth (or all), not '$target': services have no keys of their own yet" ;;
  esac
  signing=$(env_get LIFE_OS_IDENTITY_SIGNING_KEY 2>/dev/null || true)
  [[ $signing == '{'*'"x":"'*'}' ]] || die 'there is no signing key (a JWK) to rotate: ./setup.sh update adds one'
  new_key_pair
  env_values[LIFE_OS_IDENTITY_SIGNING_KEY]=$key_pair_private
  env_values[LIFE_OS_TRUSTED_KEYS]=$(jwks_with "$(jwks_with '' "$(public_jwk "$signing")")" "$key_pair_public")
  env_update
  say "A new signing key is in .env; the services trust it and the previous one."
  services=$(docker compose ps --services --status running 2>/dev/null | grep -vxE 'dashboard|gateway|postgres|whisper|ntfy|cloudflared|host-health-docker' | tr '\n' ' ' || true)
  if is_checkout; then
    say 'Apply it: first the services, then the Dashboard:'
    say "  docker compose up -d --no-build --no-deps ${services:-<every running service but the Dashboard>}"
    say '  docker compose up -d --no-build --no-deps dashboard'
    return 0
  fi
  step 'Restarting the services, then the Dashboard'
  # shellcheck disable=SC2086 # one name a word
  [[ -z $services ]] || docker compose up -d --no-deps $services
  docker compose up -d --no-deps dashboard
  say 'Done: identity tokens are signed with the new key.'
}

# ---------- Running ----------

# Waits until the gateway and the core services answer; prints what is
# still starting or failing.
# Prints the services (the arguments) whose /ready answers, from inside the
# Dashboard's container.
# shellcheck disable=SC2016 # JavaScript, run by node
ready_probe='Promise.all(process.argv.slice(1).map((s) => fetch(`http://${s}:8080/ready`, { signal: AbortSignal.timeout(4000) }).then((r) => (r.ok ? s : ""), () => ""))).then((names) => console.log(names.filter(Boolean).join(" ")))'

wait_until_ready() {
  local gateway deadline service state health exit_code ready processor_seen=0 processor_down=0 whisper_starting=0
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
    # The gateway and the Dashboard (the login page needs no sign-in), then
    # each core service from inside the Dashboard's container: their routes
    # at the gateway need a sign-in.
    if curl -fsS -o /dev/null -m 5 "$gateway/login" 2>/dev/null; then
      ready=" $(docker compose exec -T dashboard node -e "$ready_probe" "${domains[@]}" 2>/dev/null || true) "
      for service in "${domains[@]}"; do
        [[ $ready == *" $service "* ]] || waiting+=("$service")
      done
    else
      waiting+=(dashboard)
    fi
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
      # One-shot jobs: done once they exited with 0.
      postgres-provision | *-migrate) [[ $state != exited || $exit_code == 0 ]] || problems+=("$service (exit $exit_code)") ;;
      processor)
        processor_seen=1
        [[ $state == running ]] || processor_down=1
        ;;
      whisper) [[ $health == healthy ]] || whisper_starting=1 ;;
      *) [[ $state == running ]] || problems+=("$service ($state)") ;;
    esac
  done < <(docker compose ps --all --format '{{.Service}}|{{.State}}|{{.Health}}|{{.ExitCode}}' 2>/dev/null || true)
  # Without its sign-in, the processor stops at every start and Docker starts
  # it again, so ps may catch it running: AI is off either way.
  if ((processor_seen)) && ! ai_ready; then
    if [[ $(ai_provider) == claude ]]; then
      say "AI processing is off until Claude Code is set up (./setup.sh claude); notes wait in Inbox."
    else
      say 'AI processing is off until you sign in to Codex (./setup.sh codex); notes wait in Inbox.'
    fi
  elif ((processor_down)); then
    problems+=('processor (not running)')
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

# Whether that provider can run: Codex signed in, or Claude Code's one
# sign-in (the plan's token or an API key) set.
ai_ready() {
  if [[ $(ai_provider) == claude ]]; then
    [[ $(claude_sign_in) == plan || $(claude_sign_in) == api-key ]]
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
  if ! module_is_on ai; then
    next+=('./setup.sh codex      turn on AI processing of your notes')
    next+=('./setup.sh claude     or with Claude Code and a Claude plan instead')
  elif [[ $(ai_provider) == claude ]]; then
    ai_ready || next+=('./setup.sh claude     sign in to Claude Code: AI processing waits for it')
  elif ! ai_ready; then
    next+=('./setup.sh codex      sign in to Codex: AI processing waits for it')
    next+=('./setup.sh claude     or with Claude Code and a Claude plan instead')
  fi
  next+=('./setup.sh modules    the optional modules: voice notes, Google Calendar, bank sync, ...')
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
  require_own_install up
  modules_migrate
  ! module_is_on voice || check_whisper_language
  claude_image_check_up
  step "Starting Life OS $(env_get LIFE_OS_VERSION 2>/dev/null || printf '%s' "$release_version")"
  say "Modules: the core, and $(modules_on_text)."
  modules_remove_off
  docker compose up -d --pull missing
  claude_images_prune
  wait_until_ready || true
  print_access
  print_setup_code
  print_next_steps
}

# Adds the settings a newer .env.example has and .env lacks: new passwords
# and the accounts' keys generated, legacy access on (an install updated from
# before accounts keeps working as it did, docs/install/accounts.md), and
# everything else as the example has it.
add_new_settings() {
  local line key
  local -a added=() lines=()
  [[ -f .env.example ]] || return 0
  mapfile -t lines <.env.example
  for line in "${lines[@]}"; do
    [[ $line =~ ^(LIFE_OS_[A-Z0-9_]*)= ]] || continue
    key=${BASH_REMATCH[1]}
    env_get "$key" >/dev/null 2>&1 && continue
    [[ -v env_values[$key] ]] && continue
    case $key in
      *_PASSWORD) env_values[$key]=$(random_hex 24) ;;
      LIFE_OS_AUTH_SECRET | LIFE_OS_IDENTITY_SIGNING_KEY | LIFE_OS_TRUSTED_KEYS) account_keys_needed || true ;;
      LIFE_OS_LEGACY_ACCESS) env_values[$key]=on ;;
      *) env_values[$key]=$(env_get "$key" .env.example) ;;
    esac
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
  require_own_install update
  current=$(env_get LIFE_OS_VERSION 2>/dev/null || printf 'preview')
  target=${1:-$release_version}
  [[ $target =~ ^[A-Za-z0-9_][A-Za-z0-9_.-]*$ ]] || usage_error "'$target' is not a version"
  if [[ $target == "$current" ]]; then
    step "Updating the $current images"
  else
    step "Updating from $current to $target"
  fi
  modules_migrate
  add_new_settings

  ! module_is_on voice || check_whisper_language
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
  # The release's processor image too, not a local one with Claude Code.
  LIFE_OS_VERSION=$target LIFE_OS_PROCESSOR_IMAGE='' docker compose pull || die "could not download the $target images; nothing was changed"
  # With Claude Code, the processor needs it added to the new image too
  # (while AI processing is off, ./setup.sh up adds it once it is on).
  if claude_local; then
    if module_is_on ai; then
      claude_image_current "$target" || claude_image_build "$target"
    fi
    env_values[LIFE_OS_PROCESSOR_IMAGE]=$(claude_image_tag "$target")
  elif [[ -n $(env_get LIFE_OS_PROCESSOR_IMAGE 2>/dev/null || true) ]]; then
    env_values[LIFE_OS_PROCESSOR_IMAGE]=
  fi
  env_values[LIFE_OS_VERSION]=$target
  env_update
  step 'Restarting'
  modules_remove_off
  docker compose up -d --remove-orphans
  claude_images_prune
  wait_until_ready || true
  print_setup_code
  say ''
  say "Life OS $target is running. Read what changed: https://github.com/GAMch1k/life-os/releases"
}

cmd_status() {
  local gateway answer id
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
    warn_other_install
    step 'Containers'
    docker compose ps --all --format 'table {{.Service}}\t{{.State}}\t{{.Status}}' 2>/dev/null \
      || warn 'docker compose cannot read this project'
  fi
  step 'Modules (./setup.sh modules)'
  status_line 'Core' 'on (always)'
  for id in "${module_ids[@]}"; do
    if module_is_on "$id"; then
      status_line "${module_name[$id]} ($id)" "on: $(module_detail "$id")"
    else
      status_line "${module_name[$id]} ($id)" 'off'
    fi
  done
  if module_is_on ai && claude_local; then
    status_line 'Claude Code on this server' "$(claude_image_status)"
  fi
  [[ -n $(env_get LIFE_OS_MODULES_LISTED 2>/dev/null || true) ]] \
    || say 'This .env is from before modules: ./setup.sh up or update turns on the modules this install used.'
  step 'Set up'
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

# The Whisper language .env names: auto (the default) or a code such as de.
whisper_language() {
  local language
  language=$(env_get LIFE_OS_WHISPER_LANGUAGE 2>/dev/null) || language=
  printf '%s\n' "${language:-auto}"
}

whisper_language_known() {
  local language
  language=$(whisper_language)
  valid_whisper_language "${language,,}"
}

# A language whisper.cpp does not know (a hand-edited .env) stops Whisper at
# its start: up says so, rather than that Whisper is still starting.
check_whisper_language() {
  whisper_language_known \
    || warn "LIFE_OS_WHISPER_LANGUAGE in .env is '$(whisper_language)', which whisper.cpp does not know: Whisper will not start, and voice notes wait. Choose another with ./setup.sh whisper."
}

on_off() { if "$@"; then printf 'set up'; else printf 'not set up'; fi; }
status_line() { printf '  %-36s %s\n' "$1" "$2"; }

cmd_check() {
  preview_banner
  run_checks
  warn_other_install
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
  ./setup.sh status             what runs, and which modules are on and set up
  ./setup.sh check              only check this server
  ./setup.sh gateway            change where Life OS listens and its address

Accounts (docs/install/accounts.md):
  ./setup.sh account-keys       add the accounts' settings to an older .env
                                (a clone of the repository; update does it
                                for a release install)
  ./setup.sh setup-code         a one-time code for the first account, while
                                none exists (also printed by up and update)
  ./setup.sh recover-owner [--reset-2fa]
                                a one-time link to set the owner's password;
                                --reset-2fa also turns off their two-factor
  ./setup.sh legacy-access [on|off]
                                show or change legacy access (sign-in-free
                                access as the first account)
  ./setup.sh sign-out-all [--keep-devices]
                                sign every account out, everywhere: sessions,
                                devices (unless --keep-devices), API tokens,
                                and connected apps
  ./setup.sh rotate-keys [auth|all]
                                a new key for the services' identity tokens

Optional modules (the core always runs; none is on after a first setup):
  ./setup.sh modules            list them, on or off
  ./setup.sh modules on ID...   turn modules on (ai, voice, google-calendar,
                                banking, cloudflare, ntfy, server-health)
  ./setup.sh modules off ID...  turn modules off: their containers go, their
                                data stays

Set up a module (which turns it on), or another integration, any time:
  ./setup.sh codex              AI processing: sign in to OpenAI Codex
  ./setup.sh claude             AI processing: Claude Code instead of Codex,
                                with your Claude plan or an Anthropic API key
                                (downloads Claude Code onto this server)
  ./setup.sh whisper            choose the speech-to-text model and language
  ./setup.sh cloudflare         Cloudflare Tunnel (behind Cloudflare Access)
  ./setup.sh google             Google Calendar sync
  ./setup.sh banking            bank sync through Enable Banking
  ./setup.sh ntfy               push notifications through ntfy
  ./setup.sh webpush            push notifications to the Life OS phone app
  ./setup.sh feed               subscribe to a calendar feed (ICS)

Options:
  -y, --yes                no questions: defaults, or values from the
                           environment (see $docs/README.md#without-questions);
                           a first setup turns on LIFE_OS_SETUP_MODULES
  --force-new              write a new .env over an existing one; keeps a copy
                           and the database passwords
  --bind ADDRESS           where the gateway listens (default 127.0.0.1)
  --allow-public-bind      allow a public --bind address
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
    --reset-2fa) reset_2fa=1 ;;
    --keep-devices) keep_devices=1 ;;
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
  modules) cmd_modules "${args[@]}" ;;
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
  account-keys) cmd_account_keys ;;
  setup-code) cmd_setup_code ;;
  recover-owner) cmd_recover_owner ;;
  legacy-access) cmd_legacy_access "${args[@]}" ;;
  sign-out-all) cmd_sign_out_all ;;
  rotate-keys) cmd_rotate_keys "${args[@]}" ;;
  help) usage ;;
  *) usage_error "unknown command '$command'" ;;
esac
