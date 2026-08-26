#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly root=gitops/platform/management/protection-foundation/definitions/argo-hardening
readonly action_inventory=$root/argo-freeze-action-inventory.json
readonly authority_inventory=$root/argo-authority-inventory.json

render=$(kubectl kustomize "$root")
argocd_version=$(awk -F= '$1 == "ARGOCD_VERSION" {print $2}' versions.lock)
[[ $(yq -r '.argocdVersion' "$action_inventory") == "$argocd_version" &&
$(yq -r '.argocdVersion' "$authority_inventory") == "$argocd_version" ]] ||
  test::fail "Argo authority inventory differs from versions.lock"
[[ $(yq -r '.fineGrainedApplicationInheritanceDisabled' "$action_inventory") == true &&
$(yq -r '.unknownClassification' "$action_inventory") == AUTHORITY_DRIFTED &&
$(yq -r '.unknownResourceActionOrInheritance' "$authority_inventory") == AUTHORITY_DRIFTED ]] ||
  test::fail "unknown Argo authority does not fail closed"

expected_resources=(
  accounts applications applicationsets certificates clusters exec extensions gpgkeys logs projects repositories
)
mapfile -t actual_resources < <(yq -r '.resources[].name' "$action_inventory" | sort)
[[ ${actual_resources[*]} == "${expected_resources[*]}" ]] ||
  test::fail "Argo v3.5.1 Action Inventory resource surface drifted"

assert_actions() {
  local resource=$1 field=$2 expected=$3 actual
  actual=$(RESOURCE=$resource FIELD=$field yq -o=json -I=0 \
    '.resources[] | select(.name == strenv(RESOURCE)) | .[strenv(FIELD)]' "$action_inventory")
  [[ $actual == "$expected" ]] || test::fail "Argo ${resource} ${field} actions drifted"
}

assert_actions applications readOnly '["get"]'
assert_actions applications sideEffecting '["create","update","update/*","delete","delete/*","sync","override","action/*"]'
assert_actions applicationsets sideEffecting '["create","update","delete"]'
assert_actions exec readOnly '[]'
assert_actions exec sideEffecting '["create"]'
assert_actions extensions sideEffecting '["invoke"]'

classify_action() {
  local resource=$1 action=$2 candidate pattern
  while IFS= read -r candidate; do
    [[ $candidate == "$action" ]] && {
      printf 'READ_ONLY\n'
      return
    }
  done < <(RESOURCE=$resource yq -r '.resources[] | select(.name == strenv(RESOURCE)) | .readOnly[]' "$action_inventory")
  while IFS= read -r pattern; do
    if [[ $pattern == */\* && $action == "${pattern%\*}"* || $pattern == "$action" ]]; then
      printf 'SIDE_EFFECTING\n'
      return
    fi
  done < <(RESOURCE=$resource yq -r '.resources[] | select(.name == strenv(RESOURCE)) | .sideEffecting[]' "$action_inventory")
  printf 'AUTHORITY_DRIFTED\n'
}

classify_authority_context() {
  local version=$1 inheritance_disabled=$2
  [[ $version == "$argocd_version" && $inheritance_disabled == true ]] || {
    printf 'AUTHORITY_DRIFTED\n'
    return
  }
  printf 'MATCH\n'
}

[[ $(classify_action applications get) == READ_ONLY &&
$(classify_action applications action/restart) == SIDE_EFFECTING &&
$(classify_action applications future-action) == AUTHORITY_DRIFTED &&
$(classify_action future-resource get) == AUTHORITY_DRIFTED ]] ||
  test::fail "Argo Action Inventory classifier is not failure closed"
[[ $(classify_authority_context "$argocd_version" true) == MATCH &&
$(classify_authority_context 3.5.2 true) == AUTHORITY_DRIFTED &&
$(classify_authority_context "$argocd_version" false) == AUTHORITY_DRIFTED ]] ||
  test::fail "Argo version or inheritance drift does not fail closed"

argocd_cm=$(NAME=argocd-cm yq ea -o=json -I=0 \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' <<< "$render")
rbac_cm=$(NAME=argocd-rbac-cm yq ea -o=json -I=0 \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' <<< "$render")
params_cm=$(NAME=argocd-cmd-params-cm yq ea -o=json -I=0 \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' <<< "$render")
candidate_app=$(NAME=argocd-self yq ea -o=json -I=0 \
  'select(.kind == "Application" and .metadata.name == strenv(NAME))' <<< "$render")

[[ $(yq -r '.data."admin.enabled"' <<< "$argocd_cm") == false &&
$(yq -r '.data."users.anonymous.enabled"' <<< "$argocd_cm") == false ]] ||
  test::fail "Argo built-in admin or anonymous access remains enabled"
live_health=$(yq -r '.data."resource.customizations.health.argoproj.io_Application"' \
  gitops/platform/management/argocd-self/base/argocd-cm.yaml)
candidate_health=$(yq -r '.data."resource.customizations.health.argoproj.io_Application"' <<< "$argocd_cm")
[[ $candidate_health == "$live_health" ]] ||
  test::fail "Argo hardening candidate changed the shared Application Health capability"
[[ $(yq -r '.data."server.insecure"' <<< "$params_cm") == true &&
$(yq -r '.data."server.rbac.disableApplicationFineGrainedRBACInheritance"' <<< "$params_cm") == true ]] ||
  test::fail "Argo fine-grained inheritance mode is not explicit"
[[ $(yq -r '.data."policy.default"' <<< "$rbac_cm") == role:atlas-authenticated-readonly &&
$(yq -r '.data."policy.matchMode"' <<< "$rbac_cm") == glob &&
$(yq '.data | has("policy.atlas-recovery-freeze.csv")' <<< "$rbac_cm") == false ]] ||
  test::fail "Argo default policy or normal guard ownership projection drifted"

while IFS= read -r policy_line; do
  [[ $policy_line == p,\ role:atlas-authenticated-readonly,\ *,\ get,\ *,\ allow ]] ||
    test::fail "default Argo role contains a non-read-only grant: ${policy_line}"
done < <(yq -r '.data."policy.csv"' <<< "$rbac_cm" | sed '/^[[:space:]]*$/d')

[[ $(yq '.spec.ignoreDifferences | length' <<< "$candidate_app") -eq 1 &&
$(yq -r '.spec.ignoreDifferences[0].group' <<< "$candidate_app") == "" &&
$(yq -r '.spec.ignoreDifferences[0].kind' <<< "$candidate_app") == ConfigMap &&
$(yq -r '.spec.ignoreDifferences[0].namespace' <<< "$candidate_app") == argocd &&
$(yq -r '.spec.ignoreDifferences[0].name' <<< "$candidate_app") == argocd-rbac-cm &&
$(yq -r '.spec.ignoreDifferences[0].jsonPointers[0]' <<< "$candidate_app") == /data/policy.atlas-recovery-freeze.csv ]] ||
  test::fail "argocd-self candidate does not own the exact recovery guard difference"
[[ $(yq '[.spec.syncPolicy.syncOptions[] | select(. == "RespectIgnoreDifferences=true")] | length' <<< "$candidate_app") -eq 1 ]] ||
  test::fail "argocd-self candidate omits RespectIgnoreDifferences"

[[ $(yq -r '.builtInAdminEnabled' "$authority_inventory") == false &&
$(yq -r '.anonymousEnabled' "$authority_inventory") == false &&
$(yq -r '.policyDefault' "$authority_inventory") == role:atlas-authenticated-readonly &&
$(yq '.localAccounts | length' "$authority_inventory") -eq 0 &&
$(yq '.ssoSubjects | length' "$authority_inventory") -eq 0 &&
$(yq '.appProjectRoles | length' "$authority_inventory") -eq 0 ]] ||
  test::fail "Argo local, SSO, global, or AppProject authority inventory drifted"

grep -Fq 'p, role:atlas-phase1a-fixture, *, *, *, deny' \
  gitops/platform/management/protection-foundation/definitions/admission/base/recovery-guard-authorization.yaml ||
  test::fail "Guard authorization is not bound to the inventoried non-production fixture role"

test::pass "Argo v3.5.1 hardening, Action Inventory, and guard ownership candidate"
