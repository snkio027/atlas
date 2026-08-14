# shellcheck shell=bash

[[ -n ${_ATLAS_RUNTIME_LOADED:-} ]] && return 0
readonly _ATLAS_RUNTIME_LOADED=1

ATLAS_CURRENT_PHASE=bootstrap

runtime::_color() {
  [[ -t 2 && ${NO_COLOR:-} == "" ]]
}

runtime::_log() {
  local level=$1 color=$2
  shift 2
  if runtime::_color; then
    printf '\033[%sm[%s] %-5s\033[0m %s\n' "$color" "$(date '+%H:%M:%S')" "$level" "$*" >&2
  else
    printf '[%s] %-5s %s\n' "$(date '+%H:%M:%S')" "$level" "$*" >&2
  fi
}

runtime::info() { runtime::_log INFO 36 "$@"; }
runtime::ok() { runtime::_log OK 32 "$@"; }
runtime::warn() { runtime::_log WARN 33 "$@"; }
runtime::error() { runtime::_log ERROR 31 "$@"; }

runtime::die() {
  runtime::error "$*"
  return 1
}

runtime::phase() {
  ATLAS_CURRENT_PHASE=$1
  runtime::info "phase=${ATLAS_CURRENT_PHASE}"
}

runtime::command_exists() {
  command -v "$1" > /dev/null 2>&1
}

runtime::sha256() {
  shasum -a 256 "$1" | awk '{print $1}'
}

runtime::version_triplet() {
  local value=$1
  if [[ $value =~ ([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
    printf '%s.%s.%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
    return 0
  fi
  return 1
}

runtime::require_exact_version() {
  local tool=$1 actual=$2 expected=$3
  [[ $actual == "$expected" ]] || {
    runtime::die "${tool} version mismatch: expected=${expected} actual=${actual}"
    return 1
  }
}

runtime::on_exit() {
  local status=$1
  lock::release || true
  if ((status != 0)); then
    runtime::error "phase=${ATLAS_CURRENT_PHASE} exit=${status}"
  fi
}

runtime::atomic_capture() {
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

runtime::kubectl() {
  local context timeout
  context=$(config::get ATLAS_KUBE_CONTEXT)
  timeout=$(config::get ATLAS_KUBECTL_TIMEOUT)
  command kubectl --context "$context" --request-timeout "$timeout" "$@"
}

runtime::kind_cluster_exists() {
  local cluster=$1 clusters
  docker info > /dev/null 2>&1 || {
    runtime::error "Docker daemon is unavailable"
    return 2
  }
  clusters=$(kind get clusters --quiet 2>&1) || {
    runtime::error "Kind cluster discovery failed: ${clusters}"
    return 2
  }
  grep -Fqx -- "$cluster" <<< "$clusters"
}

runtime::docker_image_present() {
  docker image inspect "$1" > /dev/null 2>&1
}

runtime::wait_for() {
  local timeout_seconds=$1 interval_seconds=$2 description=$3
  shift 3
  local deadline=$((SECONDS + timeout_seconds))
  until "$@"; do
    if ((SECONDS >= deadline)); then
      runtime::die "timeout waiting for ${description}"
      return 1
    fi
    sleep "$interval_seconds"
  done
}
