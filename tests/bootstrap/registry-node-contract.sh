#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

# shellcheck source=bootstrap/lib/runtime.sh
source bootstrap/lib/runtime.sh
# shellcheck source=bootstrap/registry/local.sh
source bootstrap/registry/local.sh

readonly MOCK_REGISTRY=atlas-registry-unit
readonly MOCK_CLUSTER=atlas-unit
readonly MOCK_REGISTRY_IMAGE='registry@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
readonly MOCK_ARGOCD_IMAGE='argocd@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
readonly MOCK_REDIS_IMAGE='redis@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
readonly MOCK_NODES=$'node-control\nnode-gateway\nnode-compute\nnode-data'

config::get() {
  case "$1" in
    ATLAS_REGISTRY_NAME) printf '%s\n' "$MOCK_REGISTRY" ;;
    ATLAS_REGISTRY_HOST) printf 'registry.local\n' ;;
    ATLAS_REGISTRY_PORT) printf '5001\n' ;;
    ATLAS_CLUSTER_NAME) printf '%s\n' "$MOCK_CLUSTER" ;;
    *) return 1 ;;
  esac
}

config::version() {
  case "$1" in
    REGISTRY_IMAGE) printf '%s\n' "$MOCK_REGISTRY_IMAGE" ;;
    ARGOCD_IMAGE) printf '%s\n' "$MOCK_ARGOCD_IMAGE" ;;
    REDIS_IMAGE) printf '%s\n' "$MOCK_REDIS_IMAGE" ;;
    *) return 1 ;;
  esac
}

cluster::list_validated_kind_node_containers() {
  printf '%s\n' "$MOCK_NODES"
}

kind() {
  test::fail "Registry code called raw Kind node enumeration: $*"
}

runtime::docker_image_present() {
  return 0
}

preload_log=''
registry::_load_node_image() {
  preload_log+="$1|$2"$'\n'
}

registry::_preload_seed_images
expected_preload=''
while IFS= read -r image; do
  while IFS= read -r node; do
    expected_preload+="${node}|${image}"$'\n'
  done <<< "$MOCK_NODES"
done <<< "${MOCK_ARGOCD_IMAGE}"$'\n'"${MOCK_REDIS_IMAGE}"
[[ $preload_log == "$expected_preload" ]] || test::fail "seed images were not preloaded onto every validated Kubernetes node"
test::pass "seed image preloading uses only validated Kubernetes node containers"

registry::_container_exists() {
  return 0
}
registry::_validate_container() {
  return 0
}
registry::_running() {
  return 0
}
registry::_health() {
  return 0
}
registry::_preload_seed_images() {
  return 0
}
runtime::wait_for() {
  shift 3
  "$@"
}
docker() {
  [[ $1 == network && $2 == inspect && $3 == kind ]] || return 1
}

configure_log=''
registry::_configure_node() {
  configure_log+="$1"$'\n'
}

registry::ensure_local > /dev/null
[[ $configure_log == "${MOCK_NODES}"$'\n' ]] || test::fail "Registry trust was not configured on every validated Kubernetes node"
test::pass "Registry configuration uses only validated Kubernetes node containers"

test::assert_not_found 'kind[[:space:]]+get[[:space:]]+nodes' bootstrap/registry
