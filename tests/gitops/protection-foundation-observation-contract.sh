#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"

readonly expected_commit=${1:?expected immutable candidate commit is required}
readonly preflight_main=5d10bb3965efc7128c3b930308465b3c38b1530b
readonly expected_profile_sha=c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a
readonly expected_plan_sha=b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc
readonly target_uid=6c172134-40de-4a43-b5d2-63529fc3feb0
readonly bootstrap_principal="atlas:bootstrap:${target_uid}:g1"
readonly recovery_principal="atlas:break-glass:${target_uid}:g1"
readonly authorizer_principal="atlas:session-authz:${target_uid}:g1"
readonly argocd_principal=system:serviceaccount:argocd:atlas-argocd-application-controller
readonly activation=gitops/platform/management/protection-foundation/activation/personal-local-observing
readonly definitions=gitops/platform/management/protection-foundation/definitions

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] || test::fail "candidate commit is invalid"
git cat-file -e "${expected_commit}^{commit}" 2> /dev/null ||
  test::fail "candidate commit is unavailable"
mapfile -t candidate_parents < <(
  env -i PATH="$PATH" LC_ALL=C git --no-replace-objects \
    -c core.fsmonitor=false -c core.ignoreStat=false \
    cat-file commit "$expected_commit" | awk '$1 == "parent" { print $2 }'
)
case ${#candidate_parents[@]} in
  1)
    [[ ${candidate_parents[0]} == "$preflight_main" ]] ||
      test::fail "candidate is not a direct child of the READY preflight main"
    ;;
  2)
    [[ ${candidate_parents[0]} == "$preflight_main" &&
      ${candidate_parents[1]} =~ ^[0-9a-f]{40}$ &&
      ${candidate_parents[1]} != "$preflight_main" ]] ||
      test::fail "PR merge ref is not based directly on the READY preflight main"
    ;;
  *)
    test::fail "candidate commit has an unsupported parent projection"
    ;;
esac

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-observation-candidate.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

archive="$test_workspace/candidate.tar"
tree="$test_workspace/tree"
mkdir "$tree"
tree=$(cd "$tree" && pwd -P)
git archive --format=tar "$expected_commit" > "$archive"
tar -xf "$archive" -C "$tree"

authority="$tree/$activation/authority.json"
observing="$test_workspace/observing.yaml"
raw_observing="$test_workspace/raw-observing.yaml"
expected_observing="$test_workspace/expected-observing.yaml"
local_application="$test_workspace/local-application.yaml"
prod_application="$test_workspace/prod-application.yaml"

(cd "$tree" && kubectl kustomize "$activation/argocd-self-base-overlay") > "$observing"
(cd "$tree" && kubectl kustomize "$definitions/admission/overlays/observing") > "$raw_observing"
(cd "$tree" && kubectl kustomize gitops/platform/applications/overlays/local-orbstack) > "$local_application"
(cd "$tree" && kubectl kustomize gitops/platform/applications/overlays/prod) > "$prod_application"
(cd "$tree" && kubectl kustomize "$activation/application-overlay") > /dev/null

canonical_json_sha() {
  local file=$1 canonical
  canonical=$(jq -cS . "$file") || return 1
  printf '%s' "$canonical" | shasum -a 256 | awk '{print $1}'
}

canonical_yaml() {
  local file=$1
  yq ea -o=json -I=0 'sort_keys(..)' "$file" | jq -cs '
    sort_by([.apiVersion, .kind, (.metadata.namespace // "cluster"), .metadata.name])
  '
}

admission_only() {
  local file=$1
  yq ea '
    select(.kind == "ValidatingAdmissionPolicy" or
      .kind == "ValidatingAdmissionPolicyBinding")
  ' "$file"
}

validate_observing() {
  local file=$1
  [[ $(yq ea '[select(.kind == "ValidatingAdmissionPolicy")] | length' "$file") -eq 5 &&
  $(yq ea '[select(.kind == "ValidatingAdmissionPolicyBinding")] | length' "$file") -eq 5 ]] || return 1
  [[ $(yq ea '[select(.kind == "ValidatingAdmissionPolicy" and .spec.failurePolicy == "Fail")] | length' "$file") -eq 5 ]] || return 1
  [[ $(yq ea '[select(.kind == "ValidatingAdmissionPolicyBinding" and
    (.spec.validationActions | length) == 1 and
    .spec.validationActions[0] == "Audit")] | length' "$file") -eq 5 ]] || return 1
  [[ $(yq ea '[select(.kind == "ValidatingAdmissionPolicyBinding" and
    (.spec.validationActions[] == "Deny" or .spec.validationActions[] == "Warn"))] | length' "$file") -eq 0 ]] || return 1
  [[ $(NAME=atlas-bootstrap-recovery-permission-authorization yq ea '[select(
    .kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(NAME) and
    .spec.paramRef.parameterNotFoundAction == "Allow")] | length' "$file") -eq 1 ]] || return 1
  [[ $(yq ea '[select((.kind == "ValidatingAdmissionPolicy" or
    .kind == "ValidatingAdmissionPolicyBinding") and
    .metadata.labels."atlas.io/definition-state" == "observing")] | length' "$file") -eq 10 ]] || return 1
}

validate_target_principals() {
  local file=$1 content values
  content=$(admission_only "$file") || return 1
  ! grep -Eq '00000000-0000-0000-0000-000000000000|atlas-phase1a-fixture' <<< "$content" || return 1
  for principal in "$bootstrap_principal" "$recovery_principal" "$authorizer_principal" "$argocd_principal"; do
    grep -Fq "$principal" <<< "$content" || return 1
  done
  values=$(grep -Eo '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' <<< "$content" | sort -u)
  [[ $values == "$target_uid" ]] || return 1
  [[ $(grep -Eo 'atlas:bootstrap:[0-9a-f-]+:g[0-9]+' <<< "$content" | sort -u) == "$bootstrap_principal" ]] || return 1
  [[ $(grep -Eo 'atlas:break-glass:[0-9a-f-]+:g[0-9]+' <<< "$content" | sort -u) == "$recovery_principal" ]] || return 1
  [[ $(grep -Eo 'atlas:session-authz:[0-9a-f-]+:g[0-9]+' <<< "$content" | sort -u) == "$authorizer_principal" ]] || return 1
}

validate_observing "$observing" || test::fail "reachable admission projection is not Audit-only"
validate_target_principals "$observing" || test::fail "reachable admission principals are not target-bound"

LC_ALL=C sed \
  -e "s|atlas:bootstrap:00000000-0000-0000-0000-000000000000:g1|${bootstrap_principal}|g" \
  -e "s|atlas:break-glass:00000000-0000-0000-0000-000000000000:g1|${recovery_principal}|g" \
  -e "s|atlas:session-authz:00000000-0000-0000-0000-000000000000:g1|${authorizer_principal}|g" \
  -e 's|p, role:atlas-phase1a-fixture, \*, \*, \*, deny||g' \
  -e 's|atlas.io/definition-state: uninstalled|atlas.io/definition-state: observing|g' \
  "$raw_observing" > "$expected_observing"
admission_only "$observing" > "$test_workspace/actual-admission.yaml"
[[ $(canonical_yaml "$expected_observing") == "$(canonical_yaml "$test_workspace/actual-admission.yaml")" ]] ||
  test::fail "activation differs from the reviewed observing projection plus exact inputs"

enforced="$test_workspace/enforced.yaml"
(cd "$tree" && kubectl kustomize "$definitions/admission/overlays/enforced") > "$enforced"
if validate_observing "$enforced"; then
  test::fail "Audit-only validator accepted the enforced projection"
fi
[[ $(NAME=atlas-bootstrap-recovery-permission-authorization yq ea -r '
  select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(NAME)) |
  .spec.paramRef.parameterNotFoundAction
' "$enforced") == Deny ]] || test::fail "enforced Permission Binding lost missing-Fence Deny"
principal_drift="$test_workspace/principal-drift.yaml"
LC_ALL=C sed "s|${recovery_principal}|atlas:break-glass:${target_uid}:g2|g" "$observing" > "$principal_drift"
if validate_target_principals "$principal_drift"; then
  test::fail "principal validator accepted a generation drift"
fi

profile="$tree/$definitions/argo-hardening/probe-contract/personal-local-profile-v2.json"
plan="$tree/$definitions/argo-hardening/probe-contract/personal-local-target-materialization-plan.json"
[[ $(canonical_json_sha "$profile") == "$expected_profile_sha" &&
$(canonical_json_sha "$plan") == "$expected_plan_sha" ]] ||
  test::fail "B1/B2/B3 frozen authority hashes drifted"

jq -e \
  --arg preflight "$preflight_main" \
  --arg uid "$target_uid" \
  --arg bootstrap "$bootstrap_principal" \
  --arg recovery "$recovery_principal" \
  --arg authorizer "$authorizer_principal" \
  --arg argocd "$argocd_principal" \
  --arg profile "$expected_profile_sha" \
  '
    .schemaVersion == 1 and
    .activationStage == "ADR_0003_PHASE_1B_ADMISSION_OBSERVATION_CANDIDATE" and
    .rolloutProfile == "PERSONAL_LOCAL" and
    .environmentName == "personal-local" and .clusterName == "atlas-test" and
    .preflightMainCommit == $preflight and .candidateBaseCommit == $preflight and
    .profileV2SHA256 == $profile and
    .kubeSystemNamespaceUID == $uid and
    .principalGenerations == {bootstrap:1,recovery:1,sessionAuthorizer:1} and
    .principals == {bootstrap:$bootstrap,recovery:$recovery,
      sessionAuthorizer:$authorizer,argocdController:$argocd} and
    .argoRecoveryGuard.requiredRoles == [] and
    .argoRecoveryGuard.canonicalDenyFragment == "" and
    .validationActions == ["Audit"] and
    .runtimeAuthorization == "NOT_AUTHORIZED"
  ' "$authority" > /dev/null || test::fail "activation authority record drifted"

inventory="$tree/$definitions/argo-hardening/argo-authority-inventory.json"
[[ $(jq -c '.retainedSideEffectingRoles' "$inventory") == '[]' &&
$(jq -c '.recoveryDenyCoverage.requiredRoles' "$inventory") == '[]' ]] ||
  test::fail "empty recovery Guard projection disagrees with Argo authority inventory"

local_source=$(NAME=argocd-self yq ea -r '
  select(.kind == "Application" and .metadata.name == strenv(NAME)) |
  .spec.sources[1].path
' "$local_application")
prod_source=$(NAME=argocd-self yq ea -r '
  select(.kind == "Application" and .metadata.name == strenv(NAME)) |
  .spec.sources[1].path
' "$prod_application")
[[ $local_source == "$activation/argocd-self-base-overlay" ]] ||
  test::fail "local argocd-self does not own the observing projection"
[[ $prod_source == "$definitions/argo-hardening/argocd-self-base-overlay" ]] ||
  test::fail "production overlay unexpectedly activates PERSONAL_LOCAL observation"

collect_graph() {
  local start=$1
  # shellcheck disable=SC2016 # Expanded by the isolated child shell.
  env ATLAS_TEST_ROOT="$tree" bash -c '
    source "$ATLAS_TEST_ROOT/tests/gitops/lib/reachability-graph.sh"
    gitops_reachability::collect_paths "$1"
  ' atlas-observation-reachability "$start"
}

local_graph=$(collect_graph gitops/root/overlays/local-orbstack) ||
  test::fail "local control graph traversal failed"
prod_graph=$(collect_graph gitops/root/overlays/prod) ||
  test::fail "production control graph traversal failed"
grep -Fqx "$tree/$definitions/admission/overlays/observing" <<< "$local_graph" ||
  test::fail "observing overlay is not reachable from the local Root"
for forbidden in \
  "$definitions/admission/overlays/enforced" \
  "$definitions/rbac/escape" \
  "$definitions/rbac/session" \
  "$definitions/signal"; do
  ! grep -Fqx "$tree/$forbidden" <<< "$local_graph" ||
    test::fail "local Root reaches forbidden projection: ${forbidden}"
done
! grep -Fq '/applicationset-recovery-contract.json' <<< "$local_graph" ||
  test::fail "local Root reaches ApplicationSet recovery machinery"
! grep -Fqx "$tree/$definitions/admission/overlays/observing" <<< "$prod_graph" ||
  test::fail "production Root reaches PERSONAL_LOCAL observation"

[[ $(yq ea '[select(.kind == "Application")] | length' "$local_application") -eq 1 ]] ||
  test::fail "candidate creates another Application owner"
[[ $(yq ea '[select(.kind == "Role" or .kind == "RoleBinding" or
  .kind == "ClusterRole" or .kind == "ClusterRoleBinding")] | length' "$observing") -eq 0 ]] ||
  test::fail "candidate activates Recovery RBAC"
[[ $(yq ea '[select(.kind == "ApplicationSet")] | length' "$observing") -eq 0 ]] ||
  test::fail "candidate activates an ApplicationSet"
[[ $(yq ea '[select(.kind == "ConfigMap" and
  (.metadata.name == "atlas-bootstrap-adoption-signal" or
   .metadata.name == "atlas-bootstrap-operation-fence" or
   .metadata.name == "atlas-bootstrap-adoption-receipt"))] | length' "$observing") -eq 0 ]] ||
  test::fail "candidate creates Signal, Fence, or Receipt"

desired="$test_workspace/candidate-desired.yaml"
chart_path=$(awk -F= '$1 == "ARGOCD_CHART_PATH" {print $2}' "$tree/versions.lock")
{
  cat "$local_application"
  cat "$observing"
  (cd "$tree" && helm template atlas-argocd "$chart_path" \
    --namespace argocd --include-crds \
    --values gitops/platform/management/argocd-self/values.yaml \
    --values "$definitions/argo-hardening/argocd-values-hardening.yaml")
} > "$desired"
desired_canonical="$test_workspace/candidate-desired.json"
canonical_yaml "$desired" > "$desired_canonical"
jq -e '
  group_by([.apiVersion, .kind, (.metadata.namespace // "cluster"), .metadata.name]) |
  all(length == 1)
' "$desired_canonical" > /dev/null || test::fail "candidate desired tree contains duplicate ownership"
candidate_desired_tree_sha=$(tr -d '\n' < "$desired_canonical" | shasum -a 256 | awk '{print $1}')
[[ $candidate_desired_tree_sha =~ ^[0-9a-f]{64}$ ]] ||
  test::fail "candidate desired tree hash is invalid"

printf 'CANDIDATE_DESIRED_TREE_SHA256=%s\n' "$candidate_desired_tree_sha"
test::pass "PERSONAL_LOCAL Admission observation candidate is target-bound and Audit-only"
