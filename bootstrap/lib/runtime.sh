# shellcheck shell=bash

[[ -n ${_ATLAS_RUNTIME_LOADED:-} ]] && return 0
readonly _ATLAS_RUNTIME_LOADED=1

ATLAS_CURRENT_PHASE=bootstrap
readonly ATLAS_DOCKER_CONTEXT=orbstack

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

runtime::_orbstack_endpoint() {
  [[ -n ${HOME:-} && $HOME == /* ]] || {
    runtime::die "HOME must be an absolute path for OrbStack target resolution"
    return 1
  }
  printf 'unix://%s/.orbstack/run/docker.sock\n' "$HOME"
}

runtime::_reject_container_target_environment() {
  local names
  names=$(env | awk -F= '$1 ~ /^(DOCKER_|KIND_)/ {print $1}') || return 1
  [[ -z $names ]] || {
    runtime::die "inherited DOCKER_* and KIND_* environment variables are forbidden: ${names//$'\n'/,}"
    return 1
  }
}

runtime::docker() {
  env \
    -u DOCKER_API_VERSION \
    -u DOCKER_CERT_PATH \
    -u DOCKER_CONFIG \
    -u DOCKER_DEFAULT_PLATFORM \
    -u DOCKER_HOST \
    -u DOCKER_TLS_VERIFY \
    DOCKER_CONTEXT="$ATLAS_DOCKER_CONTEXT" \
    docker "$@"
}

runtime::kind() {
  env \
    -u DOCKER_API_VERSION \
    -u DOCKER_CERT_PATH \
    -u DOCKER_CONFIG \
    -u DOCKER_DEFAULT_PLATFORM \
    -u DOCKER_HOST \
    -u DOCKER_TLS_VERIFY \
    -u KIND_CLUSTER_NAME \
    -u KIND_DNS_SEARCH \
    -u KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER \
    -u KIND_EXPERIMENTAL_DOCKER_CONCURRENT_LOADS \
    -u KIND_EXPERIMENTAL_DOCKER_CONCURRENT_PULLS \
    -u KIND_EXPERIMENTAL_DOCKER_DNSSEARCH \
    -u KIND_EXPERIMENTAL_DOCKER_NETWORK \
    -u KIND_EXPERIMENTAL_PODMAN_NETWORK \
    -u KIND_EXPERIMENTAL_PROVIDER \
    DOCKER_CONTEXT="$ATLAS_DOCKER_CONTEXT" \
    KIND_EXPERIMENTAL_PROVIDER=docker \
    kind "$@"
}

runtime::assert_docker_authority() {
  local actual_context actual_endpoint expected_endpoint
  runtime::_reject_container_target_environment || return 1
  expected_endpoint=$(runtime::_orbstack_endpoint) || return 1
  actual_context=$(runtime::docker context show 2> /dev/null) || {
    runtime::die "unable to resolve the effective Docker context"
    return 1
  }
  actual_endpoint=$(runtime::docker context inspect "$ATLAS_DOCKER_CONTEXT" \
    --format '{{.Endpoints.docker.Host}}' 2> /dev/null) || {
    runtime::die "unable to inspect the OrbStack Docker context"
    return 1
  }
  [[ $actual_context == "$ATLAS_DOCKER_CONTEXT" ]] || {
    runtime::die "effective Docker context drift: expected=${ATLAS_DOCKER_CONTEXT} actual=${actual_context}"
    return 1
  }
  [[ $actual_endpoint == "$expected_endpoint" ]] || {
    runtime::die "OrbStack Docker endpoint drift: expected=${expected_endpoint} actual=${actual_endpoint}"
    return 1
  }
}

runtime::kind_cluster_exists() {
  local cluster=$1 clusters
  runtime::assert_docker_authority || return 2
  runtime::docker info > /dev/null 2>&1 || {
    runtime::error "Docker daemon is unavailable"
    return 2
  }
  clusters=$(runtime::kind get clusters --quiet 2>&1) || {
    runtime::error "Kind cluster discovery failed: ${clusters}"
    return 2
  }
  grep -Fqx -- "$cluster" <<< "$clusters"
}

runtime::docker_image_present() {
  runtime::docker image inspect "$1" > /dev/null 2>&1
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
