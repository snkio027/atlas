# shellcheck shell=bash

[[ -n ${_ATLAS_CONFIG_LOADED:-} ]] && return 0
readonly _ATLAS_CONFIG_LOADED=1

declare -gA ATLAS_CONFIG=()
declare -gA ATLAS_VERSIONS=()

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

config::_validate() {
  [[ ${ATLAS_VERSIONS[SCHEMA_VERSION]} == 1 ]] || {
    printf 'unsupported versions.lock schema: %s\n' "${ATLAS_VERSIONS[SCHEMA_VERSION]}" >&2
    return 1
  }
  [[ ${ATLAS_CONFIG[ATLAS_ENVIRONMENT]} =~ ^[a-z0-9][a-z0-9-]*$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_CLUSTER_NAME]} =~ ^[a-z0-9][a-z0-9-]*$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_KUBE_CONTEXT]} == "kind-${ATLAS_CONFIG[ATLAS_CLUSTER_NAME]}" ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_GIT_REPO_URL]} =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_GIT_REVISION]} =~ ^[A-Za-z0-9._/-]+$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_ROOT_PATH]} =~ ^gitops/root/overlays/[a-z0-9-]+$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_ROOT_NAME]} =~ ^[a-z0-9][a-z0-9-]*$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_REGISTRY_NAME]} =~ ^[a-z0-9][a-z0-9-]*$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_REGISTRY_HOST]} == localhost ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_REGISTRY_PORT]} =~ ^[1-9][0-9]{3,4}$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_KUBECTL_TIMEOUT]} =~ ^[1-9][0-9]*s$ ]] || return 1
  [[ ${ATLAS_CONFIG[ATLAS_READY_TIMEOUT]} =~ ^[1-9][0-9]*s$ ]] || return 1
  [[ ${ATLAS_VERSIONS[BASH_VERSION]} =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  [[ ${ATLAS_VERSIONS[KIND_NODE_IMAGE]} =~ @sha256:[0-9a-f]{64}$ ]] || return 1
  [[ ${ATLAS_VERSIONS[REGISTRY_IMAGE]} =~ @sha256:[0-9a-f]{64}$ ]] || return 1
  [[ ${ATLAS_VERSIONS[ARGOCD_IMAGE]} =~ @sha256:[0-9a-f]{64}$ ]] || return 1
  [[ ${ATLAS_VERSIONS[REDIS_IMAGE]} =~ @sha256:[0-9a-f]{64}$ ]] || return 1
  [[ ${ATLAS_VERSIONS[ARGOCD_CHART_SHA256]} =~ ^[0-9a-f]{64}$ ]] || return 1
  [[ ${ATLAS_VERSIONS[ARGOCD_CHART_FILE]} == "vendor/charts/argo-cd-${ATLAS_VERSIONS[ARGOCD_CHART_VERSION]}.tgz" ]] || return 1
  [[ ${ATLAS_VERSIONS[ARGOCD_CHART_PATH]} == "vendor/charts/argo-cd-${ATLAS_VERSIONS[ARGOCD_CHART_VERSION]}" ]] || return 1
  [[ ${ATLAS_VERSIONS[KIND_NODE_IMAGE]} == *":v${ATLAS_VERSIONS[KUBERNETES_VERSION]}@"* ]] || return 1
  [[ ${ATLAS_VERSIONS[REGISTRY_IMAGE]} == *":${ATLAS_VERSIONS[REGISTRY_VERSION]}@"* ]] || return 1
  [[ ${ATLAS_VERSIONS[ARGOCD_IMAGE]} == *":v${ATLAS_VERSIONS[ARGOCD_VERSION]}@"* ]] || return 1
}

config::load() {
  local root=$1 environment=$2
  ATLAS_CONFIG=()
  ATLAS_VERSIONS=()
  config::_load_file "${root}/env/${environment}.env" ATLAS_CONFIG ATLAS_CONFIG_KEYS || return 1
  config::_load_file "${root}/versions.lock" ATLAS_VERSIONS ATLAS_VERSION_KEYS || return 1
  config::_validate || {
    printf 'Atlas configuration validation failed\n' >&2
    return 1
  }
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
