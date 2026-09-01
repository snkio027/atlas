#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly definitions=gitops/platform/management/protection-foundation/definitions
readonly inventory_fixture=tests/gitops/fixtures/protection-foundation-inventory
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-protection-foundation.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

observing_a="$test_workspace/inventory-a.yaml"
observing_b="$test_workspace/inventory-b.yaml"
enforced="$test_workspace/enforced.yaml"
kubectl kustomize "$inventory_fixture" > "$observing_a"
kubectl kustomize "$inventory_fixture" > "$observing_b"
kubectl kustomize "$definitions/admission/overlays/enforced" > "$enforced"
cmp -s "$observing_a" "$observing_b" ||
  test::fail "Phase 1A test inventory rendering is not deterministic"

projection_names=(
  admission-observing
  admission-enforced
  rbac-escape
  rbac-session
  signal
  argo-hardening
  test-inventory
)
projection_paths=(
  "$definitions/admission/overlays/observing"
  "$definitions/admission/overlays/enforced"
  "$definitions/rbac/escape"
  "$definitions/rbac/session"
  "$definitions/signal"
  "$definitions/argo-hardening"
  "$inventory_fixture"
)
for index in "${!projection_names[@]}"; do
  projection=${projection_names[$index]}
  projection_path=${projection_paths[$index]}
  source_file="$test_workspace/${projection}.yaml"
  comparison="$test_workspace/${projection}.comparison.yaml"
  kubectl kustomize "$projection_path" > "$source_file"
  kubectl kustomize "$projection_path" > "$comparison"
  cmp -s "$source_file" "$comparison" ||
    test::fail "${projection} rendering is not deterministic"
  canonical="$test_workspace/${projection}.canonical.json"
  yq e -o=json -I=0 'sort_keys(..)' "$source_file" > "$canonical"
  expected_sha=$(awk -v projection="$projection" '$1 == projection { count += 1; value = $2 }
    END { if (count == 1) print value; else exit 1 }' \
    tests/gitops/fixtures/protection-foundation-projections.sha256)
  actual_sha=$(shasum -a 256 "$canonical" | awk '{print $1}')
  [[ $actual_sha == "$expected_sha" ]] ||
    test::fail "${projection} normalized projection hash drifted"
done

for entrypoint in \
  "$definitions/admission/base" \
  "$definitions/admission/overlays/observing" \
  "$definitions/admission/overlays/enforced" \
  "$definitions/rbac/escape" \
  "$definitions/rbac/session" \
  "$definitions/signal" \
  "$definitions/argo-hardening" \
  "$inventory_fixture"; do
  kubectl kustomize "$entrypoint" > /dev/null ||
    test::fail "Phase 1A Kustomize entrypoint does not render: ${entrypoint}"
done
[[ ! -e $definitions/kustomization.yaml && ! -e $definitions/rbac/kustomization.yaml ]] ||
  test::fail "Phase 1A contains a cross-stage aggregate entrypoint"

resource_count=$(yq ea '[.] | length' "$observing_a")
((resource_count == 27)) || test::fail "Phase 1A test inventory is not exactly 27 resources"

identities="$test_workspace/identities.tsv"
yq e -r '[.apiVersion, .kind, (.metadata.namespace // "cluster"), .metadata.name] | @tsv' \
  "$observing_a" | grep -v '^---$' > "$identities"
[[ $(wc -l < "$identities" | tr -d ' ') -eq 27 ]] ||
  test::fail "Phase 1A resource identity inventory is incomplete"
[[ $(sort -u "$identities" | wc -l | tr -d ' ') -eq 27 ]] ||
  test::fail "Phase 1A contains a duplicate GVK/namespace/name identity"

policy_count=$(yq ea '[select(.kind == "ValidatingAdmissionPolicy")] | length' "$observing_a")
binding_count=$(yq ea '[select(.kind == "ValidatingAdmissionPolicyBinding")] | length' "$observing_a")
((policy_count == 5 && binding_count == 5)) ||
  test::fail "Phase 1A must define evidence protection plus four authorization controls"

expected_controls=(
  atlas-bootstrap-evidence-protection
  atlas-bootstrap-recovery-binding-shape-authorization
  atlas-bootstrap-recovery-fence-authorization
  atlas-bootstrap-recovery-guard-authorization
  atlas-bootstrap-recovery-permission-authorization
)
for name in "${expected_controls[@]}"; do
  policy=$(NAME=$name yq ea -o=json -I=0 \
    'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME))' \
    "$observing_a")
  binding=$(NAME=$name yq ea -o=json -I=0 \
    'select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(NAME))' \
    "$observing_a")
  [[ -n $policy && -n $binding ]] || test::fail "missing production Admission control: ${name}"
  [[ $(yq -r '.spec.failurePolicy' <<< "$policy") == Fail ]] ||
    test::fail "Admission control is not fail closed: ${name}"
  [[ $(yq -o=json -I=0 '.spec.validationActions' <<< "$binding") == '["Audit"]' ]] ||
    test::fail "observing projection is not Audit-only: ${name}"
  enforced_binding=$(NAME=$name yq ea -o=json -I=0 \
    'select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(NAME))' \
    "$enforced")
  [[ $(yq -o=json -I=0 '.spec.validationActions' <<< "$enforced_binding") == '["Audit","Deny"]' ]] ||
    test::fail "enforced projection is not canonical Audit+Deny: ${name}"
done

parameterized_policies=$(yq ea \
  '[select(.kind == "ValidatingAdmissionPolicy" and (.spec | has("paramKind")))] | length' \
  "$observing_a")
parameterized_bindings=$(yq ea \
  '[select(.kind == "ValidatingAdmissionPolicyBinding" and (.spec | has("paramRef")))] | length' \
  "$observing_a")
((parameterized_policies == 1 && parameterized_bindings == 1)) ||
  test::fail "only Permission authorization may use the Fence parameter"

permission_policy=$(NAME=atlas-bootstrap-recovery-permission-authorization yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME))' "$observing_a")
permission_binding=$(NAME=atlas-bootstrap-recovery-permission-authorization yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(NAME))' "$observing_a")
enforced_permission_binding=$(NAME=atlas-bootstrap-recovery-permission-authorization yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(NAME))' "$enforced")
[[ $(yq -o=json -I=0 '.spec.paramKind' <<< "$permission_policy") == '{"apiVersion":"v1","kind":"ConfigMap"}' &&
$(yq -o=json -I=0 '.spec.paramRef' <<< "$permission_binding") == '{"name":"atlas-bootstrap-operation-fence","namespace":"kube-system","parameterNotFoundAction":"Allow"}' ]] ||
  test::fail "observing Permission authorization is not non-blocking on the missing Fence"
[[ $(yq -o=json -I=0 '.spec.paramRef' <<< "$enforced_permission_binding") == '{"name":"atlas-bootstrap-operation-fence","namespace":"kube-system","parameterNotFoundAction":"Deny"}' ]] ||
  test::fail "enforced Permission authorization does not fail closed on the missing Fence"
[[ $(yq -r '.spec.validations[0].expression' <<< "$permission_policy") == 'params != null' ]] ||
  test::fail "Permission authorization does not explicitly reject a missing parameter"

for no_parameter in \
  atlas-bootstrap-recovery-fence-authorization \
  atlas-bootstrap-recovery-binding-shape-authorization \
  atlas-bootstrap-recovery-guard-authorization; do
  value=$(NAME=$no_parameter yq ea \
    'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME)) | .spec | has("paramKind")' \
    "$observing_a")
  [[ $value == false ]] || test::fail "${no_parameter} unexpectedly depends on a parameter"
done

shape_policy=$(NAME=atlas-bootstrap-recovery-binding-shape-authorization yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME))' "$observing_a")
shape_match=$(yq -r '.spec.matchConditions[0].expression' <<< "$shape_policy")
for old_new_contract in oldObject.metadata.name object.metadata.name oldObject.metadata.labels object.metadata.labels; do
  grep -Fq "$old_new_contract" <<< "$shape_match" ||
    test::fail "Binding Shape old/new union omits ${old_new_contract}"
done

evidence_policy=$(NAME=atlas-bootstrap-evidence-protection yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME))' "$observing_a")
evidence_rules=$(yq -o=json -I=0 '.spec.matchConstraints.resourceRules' <<< "$evidence_policy")
for protected in \
  atlas-bootstrap-identity \
  atlas-bootstrap-adoption-signal \
  atlas-bootstrap-adoption-receipt \
  atlas-bootstrap-operation-fence \
  argocd kube-system; do
  grep -Fq "$protected" <<< "$evidence_policy" ||
    test::fail "Evidence protection omits ${protected}"
done
[[ $evidence_rules == *'"resources":["configmaps"]'* &&
  $evidence_rules == *'"resources":["namespaces"]'* &&
  $evidence_rules != *validatingadmissionpolic* ]] ||
  test::fail "Evidence protection resource boundary is invalid"
grep -Fq 'oldObject.metadata.name' <<< "$evidence_policy" ||
  test::fail "Evidence protection DELETE path does not use oldObject"
identity_validation=$(yq -r '.spec.validations[] | select(.message == "Bootstrap Identity v2 projection is invalid") | .expression' \
  <<< "$evidence_policy")
grep -Fq 'variables.target.data.size() == 4' <<< "$identity_validation" ||
  test::fail "Identity v2 does not enforce its exact four-key downgrade fence"
grep -Fq "k in ['schema', 'repositoryURL', 'kindConfigSHA256', 'clusterName']" \
  <<< "$identity_validation" ||
  test::fail "Identity v2 does not restrict the complete four-key map projection"

fence_policy=$(NAME=atlas-bootstrap-recovery-fence-authorization yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME))' "$observing_a")
fence_match=$(yq -r '.spec.matchConditions[0].expression' <<< "$fence_policy")
grep -Fq "atlas:session-authz:00000000-0000-0000-0000-000000000000:g1" <<< "$fence_match" ||
  test::fail "Fence authorization does not globally capture the Session Authorizer"
grep -Fq "atlas-bootstrap-operation-fence" <<< "$fence_match" ||
  test::fail "Fence authorization does not capture the canonical Fence for every subject"
! grep -Fq "atlas:bootstrap:00000000-0000-0000-0000-000000000000:g1" <<< "$fence_match" ||
  test::fail "Fence authorization globally captures non-Fence Bootstrap ConfigMaps"

guard_policy=$(NAME=atlas-bootstrap-recovery-guard-authorization yq ea -o=json -I=0 \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(NAME))' "$observing_a")
guard_contract=$(yq -r '.spec.matchConditions[].expression, .spec.validations[].expression' <<< "$guard_policy")
for required in \
  argocd-rbac-cm \
  policy.atlas-recovery-freeze.csv \
  'object.data.all(k, v' \
  'oldObject.data.all(k, v' \
  'has(object.metadata.annotations) == has(oldObject.metadata.annotations)' \
  'has(object.metadata.finalizers) == has(oldObject.metadata.finalizers)' \
  'has(object.metadata.ownerReferences) == has(oldObject.metadata.ownerReferences)' \
  "request.operation != 'DELETE'"; do
  grep -Fq "$required" <<< "$guard_contract" || test::fail "Guard contract omits ${required}"
done

rbac="$test_workspace/rbac.yaml"
{
  kubectl kustomize "$definitions/rbac/escape"
  printf '%s\n' '---'
  kubectl kustomize "$definitions/rbac/session"
} > "$rbac"
while IFS= read -r value; do
  [[ $value != '*' ]] || test::fail "Phase 1A RBAC contains a wildcard"
done < <(yq ea -r '
  select(.kind == "Role" or .kind == "ClusterRole") |
  .rules[] | .apiGroups[], .resources[], .verbs[]
' "$rbac")
[[ $(yq ea '[select(.kind == "RoleBinding" or .kind == "ClusterRoleBinding") | .subjects[] | select(.kind != "User")] | length' "$rbac") -eq 0 ]] ||
  test::fail "Phase 1A RBAC grants a non-User subject"
[[ $(yq ea '[select(.kind == "RoleBinding" or .kind == "ClusterRoleBinding") | .subjects[] | select(.name == "system:masters" or .name == "system:authenticated")] | length' "$rbac") -eq 0 ]] ||
  test::fail "Phase 1A RBAC grants authority through a system group"
[[ $(yq ea '[select(.kind == "RoleBinding" or .kind == "ClusterRoleBinding") | select(.roleRef.name == "atlas-bootstrap-recovery" or .roleRef.name == "atlas-bootstrap-recovery-cluster")] | length' "$rbac") -eq 0 ]] ||
  test::fail "static Recovery roles have standing Mutation authority"

list_contains() {
  local expected=$1
  grep -Fqx "$expected"
}

rule_allows() {
  local rule=$1 api_group=$2 resource=$3 resource_name=$4 verb=$5
  yq -r '.apiGroups[]' <<< "$rule" | list_contains "$api_group" || return 1
  yq -r '.resources[]' <<< "$rule" | list_contains "$resource" || return 1
  yq -r '.verbs[]' <<< "$rule" | list_contains "$verb" || return 1
  if [[ $(yq 'has("resourceNames")' <<< "$rule") == true ]]; then
    yq -r '.resourceNames[]' <<< "$rule" | list_contains "$resource_name" || return 1
  fi
}

effective_allows() {
  local principal=$1 namespace=$2 api_group=$3 resource=$4 resource_name=$5 verb=$6
  local binding_kind binding_namespace role_kind role_name role rule
  while IFS=$'\t' read -r binding_kind binding_namespace role_kind role_name; do
    [[ $binding_kind == ClusterRoleBinding ||
      $binding_kind == RoleBinding && $binding_namespace == "$namespace" ]] || continue
    role=$(KIND=$role_kind NAME=$role_name NAMESPACE=$binding_namespace yq e -o=json -I=0 '
      select(.kind == strenv(KIND) and .metadata.name == strenv(NAME) and
        (strenv(KIND) == "ClusterRole" or .metadata.namespace == strenv(NAMESPACE)))
    ' "$rbac")
    [[ -n $role ]] || continue
    while IFS= read -r rule; do
      if rule_allows "$rule" "$api_group" "$resource" "$resource_name" "$verb"; then
        return 0
      fi
    done < <(yq -o=json -I=0 '.rules[]' <<< "$role")
  done < <(USER=$principal yq e -r '
    select((.kind == "RoleBinding" or .kind == "ClusterRoleBinding") and
      (.subjects[]?.kind == "User" and .subjects[]?.name == strenv(USER))) |
    [.kind, (.metadata.namespace // "cluster"), .roleRef.kind, .roleRef.name] | @tsv
  ' "$rbac")
  return 1
}

matrix_count=0
while IFS=$'\t' read -r principal namespace api_group resource resource_name verb expected; do
  [[ $principal != \#* ]] || continue
  [[ -n $principal ]] || continue
  ((matrix_count += 1))
  [[ $api_group != core ]] || api_group=""
  if effective_allows "$principal" "$namespace" "$api_group" "$resource" "$resource_name" "$verb"; then
    actual=allow
  else
    actual=deny
  fi
  [[ $actual == "$expected" ]] ||
    test::fail "RBAC matrix drifted: ${principal} ${namespace} ${resource}/${resource_name} ${verb}"
done < tests/gitops/fixtures/protection-foundation-rbac-matrix.tsv
((matrix_count == 15)) || test::fail "RBAC positive/negative matrix is incomplete"

signal=$(NAME=atlas-bootstrap-adoption-signal yq ea -o=json -I=0 \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' "$observing_a")
[[ $(yq -r '.metadata.namespace' <<< "$signal") == argocd &&
$(yq -r '.immutable' <<< "$signal") == true &&
$(yq -r '.data.schema' <<< "$signal") == atlas.io/bootstrap-adoption-signal/v1 &&
$(yq -r '.data.repositoryURL' <<< "$signal") == https://github.com/snkio027/atlas.git &&
$(yq -r '.data.rootName' <<< "$signal") == atlas-root &&
$(yq -r '.metadata.annotations."argocd.argoproj.io/sync-options"' <<< "$signal") == Prune=confirm,Delete=false ]] ||
  test::fail "GitOps Adoption Signal projection drifted"

[[ $(yq ea '[select(.kind == "ConfigMap" and .metadata.name == "atlas-bootstrap-operation-fence")] | length' "$observing_a") -eq 0 ]] ||
  test::fail "Phase 1A creates an Operation Fence instance"
[[ $(yq ea '[select(.kind == "ConfigMap" and .metadata.name == "atlas-bootstrap-adoption-receipt")] | length' "$observing_a") -eq 0 ]] ||
  test::fail "Phase 1A creates an Adoption Receipt"
[[ $(yq ea '[select(.kind == "ApplicationSet")] | length' "$observing_a") -eq 0 ]] ||
  test::fail "Phase 1A creates an ApplicationSet"
[[ $(yq ea '[select(.kind == "Secret")] | length' "$observing_a") -eq 0 ]] ||
  test::fail "Phase 1A definition contains a Secret"

test::assert_not_found '__ATLAS_|BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY|client-key-data:|token:' "$definitions"
test::assert_not_found 'kind:[[:space:]]+Secret' "$definitions"

test::pass "Phase 1A protection, authorization, RBAC, and Signal definitions"
