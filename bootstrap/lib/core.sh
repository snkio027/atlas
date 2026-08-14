# shellcheck shell=bash

[[ -n ${_ATLAS_CORE_LOADED:-} ]] && return 0
readonly _ATLAS_CORE_LOADED=1

ATLAS_LOCK_DIR=
ATLAS_CURRENT_PHASE=bootstrap

core::_color() {
  [[ -t 2 && ${NO_COLOR:-} == "" ]]
}

core::_log() {
  local level=$1 color=$2
  shift 2
  if core::_color; then
    printf '\033[%sm[%s] %-5s\033[0m %s\n' "$color" "$(date '+%H:%M:%S')" "$level" "$*" >&2
  else
    printf '[%s] %-5s %s\n' "$(date '+%H:%M:%S')" "$level" "$*" >&2
  fi
}

core::info() { core::_log INFO 36 "$@"; }
core::ok() { core::_log OK 32 "$@"; }
core::warn() { core::_log WARN 33 "$@"; }
core::error() { core::_log ERROR 31 "$@"; }

core::die() {
  core::error "$*"
  return 1
}

core::phase() {
  ATLAS_CURRENT_PHASE=$1
  core::info "phase=${ATLAS_CURRENT_PHASE}"
}

core::command_exists() {
  command -v "$1" > /dev/null 2>&1
}

core::sha256() {
  shasum -a 256 "$1" | awk '{print $1}'
}

core::version_triplet() {
  local value=$1
  if [[ $value =~ ([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
    printf '%s.%s.%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
    return 0
  fi
  return 1
}

core::version_at_least() {
  local actual=$1 required=$2
  local -a left right
  IFS=. read -r -a left <<< "$actual"
  IFS=. read -r -a right <<< "$required"
  local index
  for index in 0 1 2; do
    ((10#${left[$index]:-0} > 10#${right[$index]:-0})) && return 0
    ((10#${left[$index]:-0} < 10#${right[$index]:-0})) && return 1
  done
  return 0
}

core::require_exact_version() {
  local tool=$1 actual=$2 expected=$3
  [[ $actual == "$expected" ]] || {
    core::die "${tool} version mismatch: expected=${expected} actual=${actual}"
    return 1
  }
}

core::acquire_lock() {
  local state_dir=$1
  local lock_dir="${state_dir}/bootstrap.lock"
  local owner_file="${lock_dir}/pid"
  local owner_pid

  mkdir -p "$state_dir"
  [[ ! -L $lock_dir ]] || {
    core::die "bootstrap lock path is a symlink: ${lock_dir}"
    return 1
  }

  if mkdir -m 0700 "$lock_dir" 2> /dev/null; then
    printf '%s\n' "$$" > "$owner_file"
    chmod 0600 "$owner_file"
    ATLAS_LOCK_DIR=$lock_dir
    return 0
  fi

  [[ -f $owner_file && ! -L $owner_file ]] || core::die "bootstrap lock is not recoverable: ${lock_dir}"
  read -r owner_pid < "$owner_file"
  [[ $owner_pid =~ ^[1-9][0-9]*$ ]] || core::die "bootstrap lock has an invalid owner: ${lock_dir}"
  if kill -0 "$owner_pid" 2> /dev/null; then
    core::die "another bootstrap process is running: pid=${owner_pid}"
    return 1
  fi

  core::warn "recovering stale bootstrap lock: pid=${owner_pid}"
  rm -f "$owner_file"
  rmdir "$lock_dir" || core::die "bootstrap lock contains unexpected files: ${lock_dir}"
  mkdir -m 0700 "$lock_dir"
  printf '%s\n' "$$" > "$owner_file"
  chmod 0600 "$owner_file"
  ATLAS_LOCK_DIR=$lock_dir
}

core::release_lock() {
  [[ -n ${ATLAS_LOCK_DIR:-} ]] || return 0
  [[ -d $ATLAS_LOCK_DIR && ! -L $ATLAS_LOCK_DIR ]] || return 0
  local owner_pid=
  read -r owner_pid < "${ATLAS_LOCK_DIR}/pid" 2> /dev/null || return 0
  [[ $owner_pid == "$$" ]] || return 0
  rm -f "${ATLAS_LOCK_DIR}/pid"
  rmdir "$ATLAS_LOCK_DIR"
  ATLAS_LOCK_DIR=
}

core::on_exit() {
  local status=$1
  core::release_lock || true
  if ((status != 0)); then
    core::error "phase=${ATLAS_CURRENT_PHASE} exit=${status}"
  fi
}

core::atomic_capture() {
  local destination=$1
  shift
  local directory temporary
  directory=$(dirname "$destination")
  mkdir -p "$directory"
  temporary=$(mktemp "${directory}/.atlas.XXXXXX")
  if "$@" > "$temporary"; then
    chmod 0600 "$temporary"
    mv "$temporary" "$destination"
    return 0
  else
    local status=$?
    rm -f "$temporary"
    return "$status"
  fi
}

core::kubectl() {
  local context timeout
  context=$(config::get ATLAS_KUBE_CONTEXT)
  timeout=$(config::get ATLAS_KUBECTL_TIMEOUT)
  command kubectl --context "$context" --request-timeout "$timeout" "$@"
}

core::kind_cluster_exists() {
  local cluster=$1 clusters
  docker info > /dev/null 2>&1 || {
    core::error "Docker daemon is unavailable"
    return 2
  }
  clusters=$(kind get clusters --quiet 2>&1) || {
    core::error "Kind cluster discovery failed: ${clusters}"
    return 2
  }
  grep -Fqx -- "$cluster" <<< "$clusters"
}

core::docker_image_present() {
  docker image inspect "$1" > /dev/null 2>&1
}

core::wait_for() {
  local timeout_seconds=$1 interval_seconds=$2 description=$3
  shift 3
  local deadline=$((SECONDS + timeout_seconds))
  until "$@"; do
    if ((SECONDS >= deadline)); then
      core::die "timeout waiting for ${description}"
      return 1
    fi
    sleep "$interval_seconds"
  done
}
