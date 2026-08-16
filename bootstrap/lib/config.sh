# shellcheck shell=bash

[[ -n ${_ATLAS_CONFIG_LOADED:-} ]] && return 0
readonly _ATLAS_CONFIG_LOADED=1

declare -gA ATLAS_CONFIG=()
declare -gA ATLAS_VERSIONS=()
declare -g ATLAS_CONFIG_IS_RESOLVED=false

# shellcheck disable=SC2034 # Accessed by nameref in config::_load_file.
readonly -a ATLAS_CONFIG_KEYS=(
  ATLAS_ENVIRONMENT
  ATLAS_CLUSTER_NAME
  ATLAS_KUBE_CONTEXT
  ATLAS_KIND_CONFIG
  ATLAS_GIT_REPO_URL
  ATLAS_GIT_REVISION
  ATLAS_ROOT_PATH
  ATLAS_ROOT_NAME
  ATLAS_REGISTRY_NAME
  ATLAS_REGISTRY_HOST
  ATLAS_REGISTRY_PORT
  ATLAS_KUBECTL_TIMEOUT
  ATLAS_READY_TIMEOUT
)

# shellcheck disable=SC2034 # Accessed by nameref in config::_load_file.
readonly -a ATLAS_VERSION_KEYS=(
  SCHEMA_VERSION
  BASH_VERSION
  KIND_VERSION
  KUBECTL_VERSION
  OPENSSL_VERSION
  YQ_VERSION
  HELM_VERSION
  KUBERNETES_VERSION
  KIND_NODE_IMAGE
  REGISTRY_VERSION
  REGISTRY_IMAGE
  ARGOCD_VERSION
  ARGOCD_CHART_VERSION
  ARGOCD_CHART_FILE
  ARGOCD_CHART_PATH
  ARGOCD_CHART_SHA256
  ARGOCD_IMAGE
  REDIS_IMAGE
)

config::_key_allowed() {
  local candidate=$1
  shift
  local key
  for key in "$@"; do
    [[ $candidate == "$key" ]] && return 0
  done
  return 1
}

config::_load_file() {
  local file=$1 map_name=$2 keys_name=$3
  local -n output=$map_name
  local -n allowed_keys=$keys_name
  local line key value line_number=0

  [[ -f $file && ! -L $file ]] || {
    printf 'configuration file is missing or unsafe: %s\n' "$file" >&2
    return 1
  }

  while IFS= read -r line || [[ -n $line ]]; do
    ((line_number += 1))
    [[ $line =~ ^[[:space:]]*$ || $line =~ ^[[:space:]]*# ]] && continue
    [[ $line =~ ^([A-Z][A-Z0-9_]*)=([^[:space:]]+)$ ]] || {
      printf 'invalid configuration at %s:%d\n' "$file" "$line_number" >&2
      return 1
    }
    key=${BASH_REMATCH[1]}
    value=${BASH_REMATCH[2]}
    config::_key_allowed "$key" "${allowed_keys[@]}" || {
      printf 'unsupported key %s at %s:%d\n' "$key" "$file" "$line_number" >&2
      return 1
    }
    [[ -z ${output[$key]+present} ]] || {
      printf 'duplicate key %s at %s:%d\n' "$key" "$file" "$line_number" >&2
      return 1
    }
    # shellcheck disable=SC2004 # Associative-array key, not arithmetic intent.
    output[$key]=$value
  done < "$file"

  for key in "${allowed_keys[@]}"; do
    [[ -n ${output[$key]:-} ]] || {
      printf 'required key %s is missing from %s\n' "$key" "$file" >&2
      return 1
    }
  done
}

config::_error() {
  printf 'configuration validation failed: %s\n' "$*" >&2
  return 1
}

config::_equals() {
  local label=$1 actual=$2 expected=$3
  [[ $actual == "$expected" ]] || {
    config::_error "${label} must be ${expected}; found ${actual}"
    return 1
  }
}

config::_matches() {
  local label=$1 value=$2 pattern=$3
  [[ $value =~ $pattern ]] || {
    config::_error "${label} has an invalid value: ${value}"
    return 1
  }
}

config::_contains() {
  local label=$1 value=$2 fragment=$3
  [[ $value == *"$fragment"* ]] || {
    config::_error "${label} must contain ${fragment}"
    return 1
  }
}

config::_integer_in_range() {
  local label=$1 value=$2 minimum=$3 maximum=$4 number
  [[ $value =~ ^[1-9][0-9]*$ ]] || {
    config::_error "${label} must be an integer"
    return 1
  }
  ((${#value} <= ${#maximum})) || {
    config::_error "${label} must be between ${minimum} and ${maximum}"
    return 1
  }
  number=$((10#$value))
  ((number >= minimum && number <= maximum)) || {
    config::_error "${label} must be between ${minimum} and ${maximum}"
    return 1
  }
}

config::_timeout_in_range() {
  local key=$1 value=$2 maximum=$3
  [[ $value =~ ^[1-9][0-9]*s$ ]] || {
    config::_error "${key} must be a positive integer followed by s"
    return 1
  }
  config::_integer_in_range "$key" "${value%s}" 1 "$maximum"
}

config::_validate_repo_path() {
  local root=$1 relative=$2 expected_type=$3 label=$4
  local root_canonical candidate parent canonical

  [[ $relative != /* ]] || {
    config::_error "${label} must be relative to the repository root"
    return 1
  }
  root_canonical=$(cd "$root" && pwd -P) || {
    config::_error "repository root cannot be resolved: ${root}"
    return 1
  }
  candidate="${root_canonical}/${relative}"
  [[ ! -L $candidate ]] || {
    config::_error "${label} must not be a symlink: ${relative}"
    return 1
  }

  case "$expected_type" in
    file)
      [[ -f $candidate ]] || {
        config::_error "${label} file is missing: ${relative}"
        return 1
      }
      parent=$(cd "$(dirname "$candidate")" && pwd -P) || return 1
      canonical="${parent}/$(basename "$candidate")"
      ;;
    directory)
      [[ -d $candidate ]] || {
        config::_error "${label} directory is missing: ${relative}"
        return 1
      }
      canonical=$(cd "$candidate" && pwd -P) || return 1
      ;;
    *)
      config::_error "internal path type is unsupported: ${expected_type}"
      return 1
      ;;
  esac

  [[ $canonical == "${root_canonical}/"* ]] || {
    config::_error "${label} resolves outside the repository: ${relative}"
    return 1
  }
}

config::_manifest_field_matches() {
  local file=$1 field=$2 expected=$3 line value found=false
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line =~ ^[[:space:]-]*${field}:[[:space:]]*([^[:space:]#]+) ]]; then
      value=${BASH_REMATCH[1]}
      [[ $value == "$expected" ]] || {
        config::_error "${file#*/} has ${field}=${value}; expected ${expected}"
        return 1
      }
      found=true
    fi
  done < "$file"
  [[ $found == true ]] || {
    config::_error "${file#*/} does not define ${field}"
    return 1
  }
}

config::_manifest_repo_matches() {
  local file=$1 expected=$2 content url found=false
  content=$(< "$file")
  while [[ $content =~ (https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git) ]]; do
    url=${BASH_REMATCH[1]}
    [[ $url == "$expected" ]] || {
      config::_error "${file#*/} references ${url}; expected ${expected}"
      return 1
    }
    found=true
    content=${content#*"$url"}
  done
  [[ $found == true ]] || {
    config::_error "${file#*/} does not define a Git repository URL"
    return 1
  }
}

config::_validate_git_contract() {
  local root=$1 repo=$2 revision=$3 file
  local -a project_files=(
    bootstrap/argocd/atlas-bootstrap-project.yaml
    gitops/platform/management/projects/platform-project.yaml
    gitops/platform/management/projects/workload-project.yaml
  )
  local -a application_files=(
    gitops/root/base/project-bootstrap-app.yaml
    gitops/root/base/platform-control-app.yaml
    gitops/root/base/workload-control-app.yaml
    gitops/platform/applications/base/argocd-self-app.yaml
  )

  # Configuration loading stays dependency-free, so checked-in Git sources are
  # validated with the strict scalar forms used by these controlled manifests.
  for file in "${project_files[@]}"; do
    config::_validate_repo_path "$root" "$file" file "Git contract" || return 1
    config::_manifest_repo_matches "${root}/${file}" "$repo" || return 1
  done
  for file in "${application_files[@]}"; do
    config::_validate_repo_path "$root" "$file" file "Git contract" || return 1
    config::_manifest_field_matches "${root}/${file}" repoURL "$repo" || return 1
    config::_manifest_field_matches "${root}/${file}" targetRevision "$revision" || return 1
  done
}

config::_validate_profile() {
  local environment=$1
  local name_pattern='^[a-z0-9][a-z0-9-]*$'

  config::_equals ATLAS_ENVIRONMENT "${ATLAS_CONFIG[ATLAS_ENVIRONMENT]}" "$environment" || return 1
  config::_matches ATLAS_ENVIRONMENT "${ATLAS_CONFIG[ATLAS_ENVIRONMENT]}" "$name_pattern" || return 1
  config::_matches ATLAS_CLUSTER_NAME "${ATLAS_CONFIG[ATLAS_CLUSTER_NAME]}" "$name_pattern" || return 1
  config::_equals ATLAS_KUBE_CONTEXT "${ATLAS_CONFIG[ATLAS_KUBE_CONTEXT]}" "kind-${ATLAS_CONFIG[ATLAS_CLUSTER_NAME]}" || return 1
  config::_matches ATLAS_KIND_CONFIG "${ATLAS_CONFIG[ATLAS_KIND_CONFIG]}" '^clusters/kind/[A-Za-z0-9._-]+\.ya?ml$' || return 1
  config::_matches ATLAS_GIT_REPO_URL "${ATLAS_CONFIG[ATLAS_GIT_REPO_URL]}" '^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git$' || return 1
  config::_matches ATLAS_GIT_REVISION "${ATLAS_CONFIG[ATLAS_GIT_REVISION]}" '^[A-Za-z0-9][A-Za-z0-9._/-]*$' || return 1
  [[ ${ATLAS_CONFIG[ATLAS_GIT_REVISION]} != *..* ]] || {
    config::_error "ATLAS_GIT_REVISION must not contain path traversal"
    return 1
  }
  config::_matches ATLAS_ROOT_PATH "${ATLAS_CONFIG[ATLAS_ROOT_PATH]}" '^gitops/root/overlays/[a-z0-9][a-z0-9-]*$' || return 1
  config::_matches ATLAS_ROOT_NAME "${ATLAS_CONFIG[ATLAS_ROOT_NAME]}" "$name_pattern" || return 1
  config::_matches ATLAS_REGISTRY_NAME "${ATLAS_CONFIG[ATLAS_REGISTRY_NAME]}" "$name_pattern" || return 1
  config::_equals ATLAS_REGISTRY_HOST "${ATLAS_CONFIG[ATLAS_REGISTRY_HOST]}" localhost || return 1
  config::_integer_in_range ATLAS_REGISTRY_PORT "${ATLAS_CONFIG[ATLAS_REGISTRY_PORT]}" 1024 65535 || return 1
  config::_timeout_in_range ATLAS_KUBECTL_TIMEOUT "${ATLAS_CONFIG[ATLAS_KUBECTL_TIMEOUT]}" 300 || return 1
  config::_timeout_in_range ATLAS_READY_TIMEOUT "${ATLAS_CONFIG[ATLAS_READY_TIMEOUT]}" 1800 || return 1
}

config::_validate_versions() {
  local image_key
  local digest_pattern='@sha256:[0-9a-f]{64}$'

  config::_matches BASH_VERSION "${ATLAS_VERSIONS[BASH_VERSION]}" '^5\.[0-9]+\.[0-9]+$' || return 1
  config::_matches OPENSSL_VERSION "${ATLAS_VERSIONS[OPENSSL_VERSION]}" '^[0-9]+\.[0-9]+\.[0-9]+$' || return 1
  config::_matches YQ_VERSION "${ATLAS_VERSIONS[YQ_VERSION]}" '^[0-9]+\.[0-9]+\.[0-9]+$' || return 1
  for image_key in KIND_NODE_IMAGE REGISTRY_IMAGE ARGOCD_IMAGE REDIS_IMAGE; do
    config::_matches "$image_key" "${ATLAS_VERSIONS[$image_key]}" "$digest_pattern" || return 1
  done
  config::_matches ARGOCD_CHART_SHA256 "${ATLAS_VERSIONS[ARGOCD_CHART_SHA256]}" '^[0-9a-f]{64}$' || return 1
  config::_equals ARGOCD_CHART_FILE "${ATLAS_VERSIONS[ARGOCD_CHART_FILE]}" "vendor/charts/argo-cd-${ATLAS_VERSIONS[ARGOCD_CHART_VERSION]}.tgz" || return 1
  config::_equals ARGOCD_CHART_PATH "${ATLAS_VERSIONS[ARGOCD_CHART_PATH]}" "vendor/charts/argo-cd-${ATLAS_VERSIONS[ARGOCD_CHART_VERSION]}" || return 1
  config::_contains KIND_NODE_IMAGE "${ATLAS_VERSIONS[KIND_NODE_IMAGE]}" ":v${ATLAS_VERSIONS[KUBERNETES_VERSION]}@" || return 1
  config::_contains REGISTRY_IMAGE "${ATLAS_VERSIONS[REGISTRY_IMAGE]}" ":${ATLAS_VERSIONS[REGISTRY_VERSION]}@" || return 1
  config::_contains ARGOCD_IMAGE "${ATLAS_VERSIONS[ARGOCD_IMAGE]}" ":v${ATLAS_VERSIONS[ARGOCD_VERSION]}@" || return 1
}

config::_validate_paths() {
  local root=$1
  config::_validate_repo_path "$root" "${ATLAS_CONFIG[ATLAS_KIND_CONFIG]}" file ATLAS_KIND_CONFIG || return 1
  config::_validate_repo_path "$root" "${ATLAS_CONFIG[ATLAS_ROOT_PATH]}" directory ATLAS_ROOT_PATH || return 1
  config::_validate_repo_path "$root" "${ATLAS_VERSIONS[ARGOCD_CHART_FILE]}" file ARGOCD_CHART_FILE || return 1
  config::_validate_repo_path "$root" "${ATLAS_VERSIONS[ARGOCD_CHART_PATH]}" directory ARGOCD_CHART_PATH || return 1
  config::_validate_repo_path "$root" gitops/platform/management/argocd-self/values.yaml file argocd-self-values || return 1
}

config::_validate() {
  local root=$1 environment=$2
  config::_equals SCHEMA_VERSION "${ATLAS_VERSIONS[SCHEMA_VERSION]}" 1 || return 1
  config::_validate_profile "$environment" || return 1
  config::_validate_versions || return 1
  config::_validate_paths "$root" || return 1
  config::_validate_git_contract "$root" "${ATLAS_CONFIG[ATLAS_GIT_REPO_URL]}" "${ATLAS_CONFIG[ATLAS_GIT_REVISION]}" || return 1
}

config::load() {
  local root=$1 environment=$2
  [[ $ATLAS_CONFIG_IS_RESOLVED == false ]] || {
    config::_error "configuration is already resolved"
    return 1
  }
  [[ -d $root ]] || {
    config::_error "repository root is missing: ${root}"
    return 1
  }
  [[ $environment =~ ^[a-z0-9][a-z0-9-]*$ ]] || {
    config::_error "profile name is invalid: ${environment}"
    return 1
  }
  ATLAS_CONFIG=()
  ATLAS_VERSIONS=()
  config::_load_file "${root}/env/${environment}.env" ATLAS_CONFIG ATLAS_CONFIG_KEYS || return 1
  config::_load_file "${root}/versions.lock" ATLAS_VERSIONS ATLAS_VERSION_KEYS || return 1
  config::_validate "$root" "$environment" || return 1
  ATLAS_CONFIG_IS_RESOLVED=true
  readonly ATLAS_CONFIG_IS_RESOLVED
  readonly -A ATLAS_CONFIG ATLAS_VERSIONS
}

config::get() {
  local key=$1
  [[ -n ${ATLAS_CONFIG[$key]+present} ]] || return 1
  printf '%s\n' "${ATLAS_CONFIG[$key]}"
}

config::version() {
  local key=$1
  [[ -n ${ATLAS_VERSIONS[$key]+present} ]] || return 1
  printf '%s\n' "${ATLAS_VERSIONS[$key]}"
}
