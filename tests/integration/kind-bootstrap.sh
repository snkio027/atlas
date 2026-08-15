#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

[[ ${ATLAS_INTEGRATION:-} == 1 ]] || {
  printf 'SKIP: set ATLAS_INTEGRATION=1 to run the mutating Kind Bootstrap test\n'
  exit 0
}

environment=${ATLAS_INTEGRATION_ENVIRONMENT:-test}
[[ $environment =~ ^[a-z0-9][a-z0-9-]*$ ]] || test::fail "invalid integration profile: ${environment}"
profile="env/${environment}.env"
[[ -f $profile && ! -L $profile ]] || test::fail "integration profile is missing or unsafe: ${profile}"

context=$(awk -F= '$1 == "ATLAS_KUBE_CONTEXT" {print $2}' "$profile")
root_name=$(awk -F= '$1 == "ATLAS_ROOT_NAME" {print $2}' "$profile")

./bootstrap/atlas apply --env "$environment" --approve-tier0
first_status=$(./bootstrap/atlas status --env "$environment")
server_uid=$(kubectl --context "$context" get deployment atlas-argocd-server --namespace argocd --output jsonpath='{.metadata.uid}')
controller_uid=$(kubectl --context "$context" get statefulset atlas-argocd-application-controller --namespace argocd --output jsonpath='{.metadata.uid}')
root_uid=$(kubectl --context "$context" get application "$root_name" --namespace argocd --output jsonpath='{.metadata.uid}')

./bootstrap/atlas apply --env "$environment" --approve-tier0
second_status=$(./bootstrap/atlas status --env "$environment")

[[ $(kubectl --context "$context" get deployment atlas-argocd-server --namespace argocd --output jsonpath='{.metadata.uid}') == "$server_uid" ]] || test::fail "second apply recreated the Argo CD server"
[[ $(kubectl --context "$context" get statefulset atlas-argocd-application-controller --namespace argocd --output jsonpath='{.metadata.uid}') == "$controller_uid" ]] || test::fail "second apply recreated the Application Controller"
[[ $(kubectl --context "$context" get application "$root_name" --namespace argocd --output jsonpath='{.metadata.uid}') == "$root_uid" ]] || test::fail "second apply recreated the External Root"
[[ $first_status == "$second_status" ]] || test::fail "second apply changed the reported Bootstrap state"

grep -Fq $'cluster\tREADY' <<< "$second_status" || test::fail "Kind cluster is not READY"
grep -Fq $'registry\tREADY' <<< "$second_status" || test::fail "local Registry is not READY"
grep -Fq $'argocd\tREADY' <<< "$second_status" || test::fail "Argo CD is not READY"
grep -Fq $'root\tSynced/Healthy' <<< "$second_status" || test::fail "External Root is not Synced/Healthy"
grep -Fq $'argocd-self\tSynced/Healthy' <<< "$second_status" || test::fail "argocd-self is not Synced/Healthy"
test::pass "two approved applies are idempotent after a Healthy GitOps handoff"
