#!/usr/bin/env bash

if [[ ${_ATLAS_TEST_VAP_SERVER_KIND_INVENTORY_LOADED:-} == true ]]; then
  return 0
fi
readonly _ATLAS_TEST_VAP_SERVER_KIND_INVENTORY_LOADED=true

vap_server_inventory::_capture() {
  local runner=$1 inventory

  if ! inventory=$("$runner" get clusters --quiet); then
    return 1
  fi
  printf '%s\n' "$inventory"
}

vap_server_inventory::_require_absent() {
  local stage=$1 runner=$2 cluster_name=$3 inventory

  if ! inventory=$(vap_server_inventory::_capture "$runner"); then
    printf 'unable to enumerate Kind clusters during %s\n' "$stage" >&2
    return 1
  fi
  if grep -Fqx "$cluster_name" <<< "$inventory"; then
    printf 'Kind cluster is present during %s: %s\n' "$stage" "$cluster_name" >&2
    return 1
  fi
}

vap_server_inventory::creation_target_absent() {
  vap_server_inventory::_require_absent creation-preflight "$@"
}

vap_server_inventory::deleted_target_absent() {
  vap_server_inventory::_require_absent post-delete "$@"
}
