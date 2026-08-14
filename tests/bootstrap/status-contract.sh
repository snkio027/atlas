#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

# shellcheck source=bootstrap/lib/runtime.sh
source bootstrap/lib/runtime.sh
# shellcheck source=bootstrap/argocd/status.sh
source bootstrap/argocd/status.sh

config::get() {
  [[ $1 == ATLAS_ROOT_NAME ]] || return 1
  printf 'atlas-root\n'
}

MOCK_NAMESPACE_ERROR=false
MOCK_SERVER_REPLICAS=1/1
MOCK_REPO_REPLICAS=1/1
MOCK_REDIS_REPLICAS=1/1
MOCK_CONTROLLER_REPLICAS=1/1

runtime::kubectl() {
  local resource=${2:-}
  [[ $MOCK_NAMESPACE_ERROR == false ]] || return 1
  case "$resource" in
    namespace)
      printf 'namespace/argocd\n'
      ;;
    deployment/atlas-argocd-server)
      printf '%s' "$MOCK_SERVER_REPLICAS"
      ;;
    deployment/atlas-argocd-repo-server)
      printf '%s' "$MOCK_REPO_REPLICAS"
      ;;
    deployment/atlas-argocd-redis)
      printf '%s' "$MOCK_REDIS_REPLICAS"
      ;;
    statefulset/atlas-argocd-application-controller)
      printf '%s' "$MOCK_CONTROLLER_REPLICAS"
      ;;
    application)
      printf '%s\tSynced\tHealthy' "$3"
      ;;
    *)
      return 1
      ;;
  esac
}

status_output=$(argocd::inspect_status)
grep -Fq $'argocd\tREADY\targocd' <<< "$status_output" || test::fail "ready control plane was not reported READY"

assert_degraded() {
  local label=$1 output
  output=$(argocd::inspect_status)
  grep -Fq $'argocd\tDEGRADED\targocd' <<< "$output" || test::fail "${label} was falsely reported READY"
}

MOCK_SERVER_REPLICAS=1/
assert_degraded "a Deployment with no readyReplicas field"
MOCK_SERVER_REPLICAS=1/1

MOCK_REPO_REPLICAS=1/0
assert_degraded "a Deployment with zero ready replicas"
MOCK_REPO_REPLICAS=1/1

MOCK_REDIS_REPLICAS=0/0
assert_degraded "a zero-replica Deployment"
MOCK_REDIS_REPLICAS=1/1

MOCK_CONTROLLER_REPLICAS=
assert_degraded "a missing Application Controller StatefulSet"
MOCK_CONTROLLER_REPLICAS=1/1
test::pass "READY requires every Seed control-plane workload"

MOCK_NAMESPACE_ERROR=true
if unavailable_output=$(argocd::inspect_status); then
  test::fail "namespace API failure returned success"
else
  unavailable_status=$?
fi
((unavailable_status == 2)) || test::fail "namespace API failure did not return status 2"
grep -Fq $'argocd\tUNAVAILABLE\targocd' <<< "$unavailable_output" || test::fail "namespace API failure was not reported UNAVAILABLE"
test::pass "status distinguishes API errors from absence"
