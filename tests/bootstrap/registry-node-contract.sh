#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-registry-contract.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

# shellcheck source=bootstrap/lib/runtime.sh
source bootstrap/lib/runtime.sh
# shellcheck source=bootstrap/registry/local.sh
source bootstrap/registry/local.sh
# shellcheck source=bootstrap/status/report.sh
source bootstrap/status/report.sh

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
    ARGOCD_VERSION) printf '3.5.1\n' ;;
    *) return 1 ;;
  esac
}

cluster::list_validated_kind_node_containers() {
  printf '%s\n' "$MOCK_NODES"
}

runtime::kind() {
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

registry_inspect_log="${test_workspace}/registry-inspect.log"
registry_operation_log="${test_workspace}/registry-operations.log"
canonical_projection=$(registry::_expected_container_projection)

reset_registry_mock() {
  ATLAS_TEST_PRESENCE_STATUS=0
  ATLAS_TEST_PRESENCE_OUTPUT=$MOCK_REGISTRY
  ATLAS_TEST_PROJECTION_STATUS=0
  ATLAS_TEST_REGISTRY_PROJECTION=$canonical_projection
  ATLAS_TEST_RUNTIME_STATUS=0
  ATLAS_TEST_RUNTIME_OUTPUT=true
  : > "$registry_inspect_log"
  : > "$registry_operation_log"
}

runtime::docker() {
  if [[ ${1:-} == network && ${2:-} == inspect && ${3:-} == kind ]]; then
    return 0
  fi
  if [[ ${1:-} == info ]]; then
    return 0
  fi
  if [[ ${1:-} == container && ${2:-} == ls ]]; then
    ((ATLAS_TEST_PRESENCE_STATUS == 0)) || return "$ATLAS_TEST_PRESENCE_STATUS"
    [[ -z $ATLAS_TEST_PRESENCE_OUTPUT ]] || printf '%s\n' "$ATLAS_TEST_PRESENCE_OUTPUT"
    return 0
  fi
  if [[ ${1:-} == inspect && ${2:-} == --format && ${4:-} == "$MOCK_REGISTRY" ]]; then
    if [[ $3 == '{{.State.Running}}' ]]; then
      ((ATLAS_TEST_RUNTIME_STATUS == 0)) || return "$ATLAS_TEST_RUNTIME_STATUS"
      printf '%s\n' "$ATLAS_TEST_RUNTIME_OUTPUT"
    else
      printf 'inspect\n' >> "$registry_inspect_log"
      ((ATLAS_TEST_PROJECTION_STATUS == 0)) || return "$ATLAS_TEST_PROJECTION_STATUS"
      printf '%s\n' "$ATLAS_TEST_REGISTRY_PROJECTION"
    fi
    return 0
  fi
  case "${1:-}" in
    run | start | rm)
      printf '%s\n' "$*" >> "$registry_operation_log"
      return 0
      ;;
  esac
  return 1
}

reset_registry_mock
[[ $(registry::_contract_state) == MATCH ]] || test::fail "canonical Registry projection did not match"
[[ $(wc -l < "$registry_inspect_log" | tr -d ' ') == 1 ]] || test::fail "Registry validation did not use one Docker inspect snapshot"

projection_with_field() {
  local projection=$1 field=$2 value=$3
  awk -v field="$field" -v value="$value" '
    index($0, field "=") == 1 {$0 = field "=" value}
    {print}
  ' <<< "$projection"
}

assert_projection_field_rejected() {
  local field=$1 value=$2 label=$3
  reset_registry_mock
  ATLAS_TEST_REGISTRY_PROJECTION=$(projection_with_field "$canonical_projection" "$field" "$value")
  if [[ $(registry::_contract_state) != DRIFTED ]]; then
    test::fail "Registry validation accepted ${label}"
  fi
  [[ $(wc -l < "$registry_inspect_log" | tr -d ' ') == 1 ]] || test::fail "${label} was not checked from one Docker inspect snapshot"
}

assert_projection_field_rejected image 'registry@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd' 'image drift'
assert_projection_field_rejected managed false 'ownership label drift'
assert_projection_field_rejected cluster foreign-cluster 'cluster label drift'
assert_projection_field_rejected networkMode bridge 'primary network drift'
assert_projection_field_rejected networkAttachmentCount 2 'extra network attachment'
assert_projection_field_rejected kindNetworkAttached false 'missing Kind network attachment'
assert_projection_field_rejected restart no 'restart policy drift'
assert_projection_field_rejected restartMaximumRetryCount 1 'restart retry drift'
assert_projection_field_rejected portBindingCount 2 'extra port binding'
assert_projection_field_rejected registryBinding '1|0.0.0.0|5001' 'Registry host binding drift'
assert_projection_field_rejected registryBinding '2|127.0.0.1|5001|127.0.0.1|5002' 'extra Registry target binding'
assert_projection_field_rejected mountCount 2 'extra mount'
assert_projection_field_rejected dataMount "bind|${MOCK_REGISTRY}-data|/var/lib/registry|true" 'data mount type drift'
assert_projection_field_rejected dataMount 'volume|foreign-data|/var/lib/registry|true' 'data volume drift'
assert_projection_field_rejected dataMount "volume|${MOCK_REGISTRY}-data|/tmp/registry|true" 'data mount target drift'
assert_projection_field_rejected dataMount "volume|${MOCK_REGISTRY}-data|/var/lib/registry|false" 'data mount access drift'
test::pass "Registry validation rejects every creation-contract drift from one inspect snapshot"

runtime::assert_docker_authority() {
  return 0
}
lock::assert_held() {
  return 0
}
registry::_configure_node() {
  printf 'configure %s\n' "$1" >> "$registry_operation_log"
}
registry::_preload_seed_images() {
  printf 'preload\n' >> "$registry_operation_log"
}

reset_registry_mock
ATLAS_TEST_REGISTRY_PROJECTION=$(projection_with_field "$canonical_projection" restart no)
if registry::ensure_local > /dev/null 2>&1; then
  test::fail "Registry reconciliation accepted a drifted existing container"
fi
[[ ! -s $registry_operation_log ]] || test::fail "Registry reconciliation continued after container drift"
test::pass "Registry drift stops reconciliation before runtime and node changes"

reset_registry_mock
ATLAS_TEST_PRESENCE_STATUS=42
if registry::ensure_local > /dev/null 2>&1; then
  test::fail "Registry reconciliation treated failed container enumeration as absence"
fi
[[ ! -s $registry_operation_log ]] || test::fail "failed container enumeration reached run, start, configure, or preload"
test::pass "Registry enumeration failure stops every mutation path"

reset_registry_mock
ATLAS_TEST_RUNTIME_STATUS=42
if registry::ensure_local > /dev/null 2>&1; then
  test::fail "Registry reconciliation treated failed runtime inspection as stopped"
fi
[[ ! -s $registry_operation_log ]] || test::fail "failed runtime inspection reached start or later mutations"
test::pass "Registry runtime inspection failure cannot start the container"

reset_registry_mock
ATLAS_TEST_PROJECTION_STATUS=42
projection_error="${test_workspace}/projection-error.log"
if registry_report=$(registry::inspect_status 2> "$projection_error"); then
  registry_status=0
else
  registry_status=$?
fi
((registry_status == 2)) || test::fail "failed Registry projection did not return UNAVAILABLE status"
[[ $registry_report == $'registry\tUNAVAILABLE\tatlas-registry-unit' ]] || test::fail "failed Registry projection was not reported as UNAVAILABLE"
grep -Fq 'unable to inspect Registry container contract' "$projection_error" || test::fail "failed Registry projection lacked a diagnostic"
complete_report=$'cluster\tREADY\tatlas-test\n'"${registry_report}"$'\nargocd\tREADY\targocd\nroot\tSynced/Healthy\tatlas-root\nargocd-self\tSynced/Healthy\targocd-self'
if status::check "$complete_report"; then
  check_status=0
else
  check_status=$?
fi
((check_status == 2)) || test::fail "status --check did not classify Registry inspection failure as unavailable"
test::pass "Registry projection failure produces status --check exit 2"

reset_registry_mock
ATLAS_TEST_REGISTRY_PROJECTION=$(sed '$d' <<< "$canonical_projection")
[[ $(registry::_contract_state 2> /dev/null) == UNAVAILABLE ]] || test::fail "malformed Registry projection was treated as drift"
ATLAS_TEST_RUNTIME_OUTPUT=unknown
[[ $(registry::_runtime_state 2> /dev/null) == UNAVAILABLE ]] || test::fail "unknown Registry runtime output was treated as stopped"
ATLAS_TEST_PRESENCE_OUTPUT=$'atlas-registry-unit\nunexpected'
[[ $(registry::_presence_state 2> /dev/null) == UNAVAILABLE ]] || test::fail "ambiguous Registry enumeration was treated as presence"
test::pass "malformed Docker inspection output fails unavailable"

reset_registry_mock
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
runtime::assert_docker_authority() {
  return 0
}

configure_log=''
registry::_configure_node() {
  configure_log+="$1"$'\n'
}

registry::ensure_local > /dev/null
[[ $configure_log == "${MOCK_NODES}"$'\n' ]] || test::fail "Registry trust was not configured on every validated Kubernetes node"
test::pass "Registry configuration uses only validated Kubernetes node containers"

test::assert_not_found 'kind[[:space:]]+get[[:space:]]+nodes' bootstrap/registry
