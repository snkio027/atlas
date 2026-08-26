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

runtime::_path_owner() {
  local path=$1 value
  if value=$(stat -f '%u' "$path" 2> /dev/null); then
    printf '%s\n' "$value"
  else
    stat -c '%u' "$path"
  fi
}

runtime::_path_mode() {
  local path=$1 value
  if value=$(stat -f '%Lp' "$path" 2> /dev/null); then
    printf '%s\n' "$value"
  else
    stat -c '%a' "$path"
  fi
}

runtime::_path_has_extended_acl() {
  local path=$1 listing permissions
  if [[ $(uname -s) == Darwin ]]; then
    listing=$(LC_ALL=C ls -lde "$path") || return 2
    [[ $listing == *$'\n'* ]]
    return
  fi
  listing=$(LC_ALL=C ls -ld "$path") || return 2
  permissions=${listing%%[[:space:]]*}
  [[ $permissions == *+* ]]
}

runtime::_assert_no_extended_acl() {
  local path=$1 label=$2 acl_status
  if runtime::_path_has_extended_acl "$path"; then
    runtime::die "${label} must not have an extended ACL: ${path}"
    return 1
  else
    acl_status=$?
    ((acl_status == 1)) || {
      runtime::die "${label} ACL state is unavailable: ${path}"
      return 1
    }
  fi
}

runtime::path_identity() {
  local path=$1 value
  if value=$(stat -f '%d:%i:%u' "$path" 2> /dev/null); then
    printf '%s\n' "$value"
  else
    stat -c '%d:%i:%u' "$path"
  fi
}

runtime::_canonical_directory() {
  local path=$1
  (cd "$path" 2> /dev/null && pwd -P)
}

runtime::assert_private_directory() {
  local path=$1 label=$2 canonical owner mode
  [[ $path == /* ]] || {
    runtime::die "${label} must use an absolute path: ${path}"
    return 1
  }
  [[ -d $path && ! -L $path ]] || {
    runtime::die "${label} must be a non-symlink directory: ${path}"
    return 1
  }
  canonical=$(runtime::_canonical_directory "$path") || {
    runtime::die "unable to canonicalize ${label}: ${path}"
    return 1
  }
  [[ $canonical == "$path" ]] || {
    runtime::die "${label} resolves outside its approved path: ${path} -> ${canonical}"
    return 1
  }
  owner=$(runtime::_path_owner "$path") || {
    runtime::die "unable to inspect ${label} owner: ${path}"
    return 1
  }
  [[ $owner == "$(id -u)" ]] || {
    runtime::die "${label} must be owned by the current user: ${path}"
    return 1
  }
  mode=$(runtime::_path_mode "$path") || {
    runtime::die "unable to inspect ${label} mode: ${path}"
    return 1
  }
  [[ $mode == 700 ]] || {
    runtime::die "${label} must have mode 0700: ${path}"
    return 1
  }
  runtime::_assert_no_extended_acl "$path" "$label"
}

runtime::ensure_private_directory() {
  local path=$1 label=$2
  [[ ! -L $path ]] || {
    runtime::die "${label} must not be a symlink: ${path}"
    return 1
  }
  if [[ ! -e $path ]]; then
    mkdir -m 0700 "$path" || {
      runtime::die "unable to create ${label}: ${path}"
      return 1
    }
  fi
  runtime::assert_private_directory "$path" "$label"
}

runtime::assert_private_file() {
  local path=$1 label=$2 owner mode
  [[ -f $path && ! -L $path ]] || {
    runtime::die "${label} must be a non-symlink regular file: ${path}"
    return 1
  }
  owner=$(runtime::_path_owner "$path") || {
    runtime::die "unable to inspect ${label} owner: ${path}"
    return 1
  }
  [[ $owner == "$(id -u)" ]] || {
    runtime::die "${label} must be owned by the current user: ${path}"
    return 1
  }
  mode=$(runtime::_path_mode "$path") || {
    runtime::die "unable to inspect ${label} mode: ${path}"
    return 1
  }
  [[ $mode == 600 ]] || {
    runtime::die "${label} must have mode 0600: ${path}"
    return 1
  }
  runtime::_assert_no_extended_acl "$path" "$label"
}

runtime::_assert_atomic_destination() {
  local destination=$1
  [[ ! -L $destination ]] || {
    runtime::die "atomic output destination must not be a symlink: ${destination}"
    return 1
  }
  [[ -e $destination ]] || return 0
  runtime::assert_private_file "$destination" "atomic output destination"
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
  local release_status=0
  lock::release || release_status=$?
  if ((release_status != 0)); then
    runtime::error "bootstrap lifecycle lock release failed: exit=${release_status}"
  fi
  if ((status != 0)); then
    runtime::error "phase=${ATLAS_CURRENT_PHASE} exit=${status}"
    return "$status"
  fi
  return "$release_status"
}

runtime::atomic_capture() {
  local destination=$1
  shift
  local directory directory_identity current_identity temporary
  directory=$(dirname "$destination")
  runtime::ensure_private_directory "$directory" "atomic output directory" || return 1
  runtime::_assert_atomic_destination "$destination" || return 1
  directory_identity=$(runtime::path_identity "$directory") || {
    runtime::die "unable to bind atomic output directory identity: ${directory}"
    return 1
  }
  temporary=$(mktemp "${directory}/.atlas.XXXXXX") || return 1
  if "$@" > "$temporary"; then
    runtime::assert_private_directory "$directory" "atomic output directory" || return 1
    current_identity=$(runtime::path_identity "$directory") || return 1
    [[ $current_identity == "$directory_identity" ]] || {
      runtime::die "atomic output directory identity changed during capture: ${directory}"
      return 1
    }
    runtime::assert_private_file "$temporary" "atomic temporary output" || return 1
    if ! runtime::_assert_atomic_destination "$destination"; then
      rm -f "$temporary"
      return 1
    fi
    chmod 0600 "$temporary" || return 1
    mv "$temporary" "$destination" || return 1
    runtime::assert_private_file "$destination" "atomic output" || return 1
    runtime::assert_private_directory "$directory" "atomic output directory" || return 1
    current_identity=$(runtime::path_identity "$directory") || return 1
    [[ $current_identity == "$directory_identity" ]] || {
      runtime::die "atomic output directory identity changed after commit: ${directory}"
      return 1
    }
  else
    local status=$?
    runtime::assert_private_directory "$directory" "atomic output directory" || return 1
    current_identity=$(runtime::path_identity "$directory") || return 1
    if [[ $current_identity == "$directory_identity" ]]; then
      runtime::assert_private_file "$temporary" "atomic temporary output" || return 1
      rm -f "$temporary"
    else
      runtime::error "atomic output directory identity changed after capture failure: ${directory}"
      return 1
    fi
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
