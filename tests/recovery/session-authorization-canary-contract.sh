#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-session-authorization-canary.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

recovery_cli=./bootstrap/recovery/atlas-recovery
namespace_uid=12345678-1234-1234-1234-123456789abc
recovery_operator="atlas:break-glass:${namespace_uid}:g7"
session_authorizer="atlas:session-authz:${namespace_uid}:g4"
first_bundle="${test_workspace}/first.yaml"
second_bundle="${test_workspace}/second.yaml"

render_bundle() {
  "$recovery_cli" phase0 session-authorization-canary-manifests \
    --recovery-operator "$recovery_operator" \
    --session-authorizer "$session_authorizer"
}

render_bundle > "$first_bundle"
render_bundle > "$second_bundle"
cmp -s "$first_bundle" "$second_bundle" || test::fail "Session Authorization canary rendering is not deterministic"

document_count=$(yq ea '[.] | length' "$first_bundle")
((document_count == 12)) || test::fail "Phase-0 Definition Closure bundle does not contain exactly twelve resources"

assert_resource_projection() {
  local kind=$1 name=$2 expected_sha=$3 actual_sha
  actual_sha=$(
    RESOURCE_KIND=$kind RESOURCE_NAME=$name yq ea -o=json -I=0 \
      'select(.kind == strenv(RESOURCE_KIND) and .metadata.name == strenv(RESOURCE_NAME)) | sort_keys(..)' \
      "$first_bundle" |
      shasum -a 256 |
      awk '{print $1}'
  )
  [[ $actual_sha == "$expected_sha" ]] ||
    test::fail "${kind}/${name} complete projection drifted: expected ${expected_sha}, got ${actual_sha}"
}

assert_resource_projection Role atlas-bootstrap-recovery-canary \
  5a234d0d0e2afb86789fef5beed1a7342284d93ddcc889ed59025a599932f8fc
assert_resource_projection Role atlas-bootstrap-recovery-authorizer-canary \
  bad62a23878e69e0b15682a35663135bbbaff549a3362fd2da21ce2068b8dd89
assert_resource_projection RoleBinding atlas-bootstrap-recovery-authorizer-canary \
  dfcd2c71ebba51ac645fa5959fe2490c6eaebc18060a6d26f491825386867ee4
assert_resource_projection ValidatingAdmissionPolicy atlas-bootstrap-recovery-fence-authorization-canary \
  0d0985284dd35de5bc5811555274ece243ad48b7658ad4e228394f380a0c75f8
assert_resource_projection ValidatingAdmissionPolicyBinding atlas-bootstrap-recovery-fence-authorization-canary \
  efb401217069eb264239bf58996c2e13f60045743f4216ea4e78c05fb65d25fc
assert_resource_projection ValidatingAdmissionPolicy atlas-bootstrap-recovery-binding-shape-authorization-canary \
  0013994cb990e2296c35b022f3553401ee90c62950441e2d68f9e7ada0e52941
assert_resource_projection ValidatingAdmissionPolicyBinding atlas-bootstrap-recovery-binding-shape-authorization-canary \
  3d09d0dcc836ccd65195a853b51105044da9e4dbd056e95cdf9decf4b5f103a2
assert_resource_projection ValidatingAdmissionPolicy atlas-bootstrap-recovery-permission-authorization-canary \
  7f002e6f7776c557cecd847f307932120401f3b0226d382f32e1e432e75a2c96
assert_resource_projection ValidatingAdmissionPolicyBinding atlas-bootstrap-recovery-permission-authorization-canary \
  2877b4638b8453a809e0a0bee591cffcd888f72de4e5295fe58bdfd4c0cf98f7
assert_resource_projection ConfigMap atlas-bootstrap-recovery-guard-canary \
  ad3974aa428d29a7b882fde39f480527da09603fa5e73e6cb5e003d35f7dfc02
assert_resource_projection ValidatingAdmissionPolicy atlas-bootstrap-recovery-guard-authorization-canary \
  bcc7c6cab5c85a423dafe096a845edd4234531ac1224c87f862387751c7b939a
assert_resource_projection ValidatingAdmissionPolicyBinding atlas-bootstrap-recovery-guard-authorization-canary \
  07674f6d9d9c8fb0409d7ce9f048ebb60221cdf1dcb39748473620be4a6af774
test::pass "all Phase-0 Session Authorization definitions have exact complete projections"

permission_role=$(RESOURCE_NAME=atlas-bootstrap-recovery-canary yq ea \
  'select(.kind == "Role" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.metadata.name' <<< "$permission_role") == atlas-bootstrap-recovery-canary &&
$(yq '.metadata.namespace' <<< "$permission_role") == kube-system ]] ||
  test::fail "canary permission Role is not namespace confined"
expected_fixture_rule='{"apiGroups":[""],"resources":["configmaps"],"resourceNames":["atlas-bootstrap-admission-escape-canary"],"verbs":["get"]}'
expected_guard_rule='{"apiGroups":[""],"resources":["configmaps"],"resourceNames":["atlas-bootstrap-recovery-guard-canary"],"verbs":["get","patch","update"]}'
[[ $(yq -o=json -I=0 '.rules[0]' <<< "$permission_role") == "$expected_fixture_rule" ]] ||
  test::fail "canary permission Role does not grant only the exact fixture read"
[[ $(yq -o=json -I=0 '.rules[1]' <<< "$permission_role") == "$expected_guard_rule" ]] ||
  test::fail "canary permission Role does not confine guard mutation to the exact fixture"
[[ $(yq '.rules | length' <<< "$permission_role") -eq 2 ]] ||
  test::fail "canary permission Role contains an additional rule"

authorizer_role=$(RESOURCE_NAME=atlas-bootstrap-recovery-authorizer-canary yq ea \
  'select(.kind == "Role" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.metadata.namespace' <<< "$authorizer_role") == kube-system ]] ||
  test::fail "Session Authorizer Role is not namespace confined"
[[ $(yq '.rules | length' <<< "$authorizer_role") -eq 4 ]] ||
  test::fail "Session Authorizer Role does not have exactly four rules"
expected_fence_create='{"apiGroups":[""],"resources":["configmaps"],"verbs":["create"]}'
expected_fence_lifecycle='{"apiGroups":[""],"resources":["configmaps"],"resourceNames":["atlas-bootstrap-operation-fence-canary"],"verbs":["get","delete"]}'
expected_binding_lifecycle='{"apiGroups":["rbac.authorization.k8s.io"],"resources":["rolebindings"],"verbs":["create","delete"]}'
expected_exact_bind='{"apiGroups":["rbac.authorization.k8s.io"],"resources":["roles"],"resourceNames":["atlas-bootstrap-recovery-canary"],"verbs":["get","bind"]}'
[[ $(yq -o=json -I=0 '.rules[0]' <<< "$authorizer_role") == "$expected_fence_create" ]] ||
  test::fail "Session Authorizer Fence CREATE rule drifted"
[[ $(yq -o=json -I=0 '.rules[1]' <<< "$authorizer_role") == "$expected_fence_lifecycle" ]] ||
  test::fail "Session Authorizer Fence lifecycle rule drifted"
[[ $(yq -o=json -I=0 '.rules[2]' <<< "$authorizer_role") == "$expected_binding_lifecycle" ]] ||
  test::fail "Session Authorizer Binding lifecycle rule drifted"
[[ $(yq -o=json -I=0 '.rules[3]' <<< "$authorizer_role") == "$expected_exact_bind" ]] ||
  test::fail "Session Authorizer exact bind rule drifted"

authorizer_binding=$(RESOURCE_NAME=atlas-bootstrap-recovery-authorizer-canary yq ea \
  'select(.kind == "RoleBinding" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.metadata.namespace' <<< "$authorizer_binding") == kube-system ]] ||
  test::fail "Session Authorizer RoleBinding is not namespace confined"
expected_role_ref='{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":"atlas-bootstrap-recovery-authorizer-canary"}'
[[ $(yq -o=json -I=0 '.roleRef' <<< "$authorizer_binding") == "$expected_role_ref" ]] ||
  test::fail "Session Authorizer RoleBinding roleRef drifted"
expected_subject="[{\"apiGroup\":\"rbac.authorization.k8s.io\",\"kind\":\"User\",\"name\":\"${session_authorizer}\"}]"
[[ $(yq -o=json -I=0 '.subjects' <<< "$authorizer_binding") == "$expected_subject" ]] ||
  test::fail "Session Authorizer RoleBinding subject drifted"

cluster_rbac_count=$(yq ea '[select(.kind == "ClusterRole" or .kind == "ClusterRoleBinding")] | length' "$first_bundle")
((cluster_rbac_count == 0)) || test::fail "canary bundle grants cluster-scoped RBAC"
standing_permission_binding_count=$(yq ea \
  '[select(.kind == "RoleBinding" and .roleRef.name == "atlas-bootstrap-recovery-canary")] | length' \
  "$first_bundle")
((standing_permission_binding_count == 0)) || test::fail "fixture read is granted without a temporary Fence-bound RoleBinding"
! grep -Eq '(^|[[:space:]])\*([[:space:]]|$)' <<< "${permission_role}${authorizer_role}" ||
  test::fail "Session Authorization canary RBAC contains a wildcard"
test::pass "fixture read and Session Authorizer lifecycle authority are namespace split and least privilege"

for policy_name in \
  atlas-bootstrap-recovery-fence-authorization-canary \
  atlas-bootstrap-recovery-binding-shape-authorization-canary \
  atlas-bootstrap-recovery-permission-authorization-canary \
  atlas-bootstrap-recovery-guard-authorization-canary; do
  policy=$(RESOURCE_NAME=$policy_name yq ea \
    'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME))' \
    "$first_bundle")
  binding=$(RESOURCE_NAME=$policy_name yq ea \
    'select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(RESOURCE_NAME))' \
    "$first_bundle")
  [[ $(yq '.spec.failurePolicy' <<< "$policy") == Fail ]] || test::fail "${policy_name} is not fail closed"
  [[ $(yq '.spec.matchConstraints.resourceRules | length' <<< "$policy") -eq 1 ]] ||
    test::fail "${policy_name} does not have exactly one resource rule"
  [[ $(yq '.spec.matchConditions | length' <<< "$policy") -eq 1 ]] ||
    test::fail "${policy_name} does not have exactly one match condition"
  expected_binding_actions='["Audit","Deny"]'
  [[ $(yq -o=json -I=0 '.spec.validationActions' <<< "$binding") == "$expected_binding_actions" ]] ||
    test::fail "${policy_name} Binding is not canonical Audit+Deny"
done
policy_count=$(yq ea '[select(.kind == "ValidatingAdmissionPolicy")] | length' "$first_bundle")
binding_count=$(yq ea '[select(.kind == "ValidatingAdmissionPolicyBinding")] | length' "$first_bundle")
((policy_count == 4 && binding_count == 4)) || test::fail "four-control Policy/Binding closure is incomplete"
parameterized_policy_count=$(yq ea \
  '[select(.kind == "ValidatingAdmissionPolicy" and (.spec | has("paramKind")))] | length' \
  "$first_bundle")
parameterized_binding_count=$(yq ea \
  '[select(.kind == "ValidatingAdmissionPolicyBinding" and (.spec | has("paramRef")))] | length' \
  "$first_bundle")
((parameterized_policy_count == 1 && parameterized_binding_count == 1)) ||
  test::fail "only Permission authorization may be Fence parameterized"
test::pass "all four authorization controls share Fail and canonical Audit+Deny enforcement"

fence_policy=$(RESOURCE_NAME=atlas-bootstrap-recovery-fence-authorization-canary yq ea \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.spec | has("paramKind")' <<< "$fence_policy") == false ]] ||
  test::fail "Fence authorization incorrectly depends on a parameter"
[[ $(yq -o=json -I=0 '.spec.matchConstraints.resourceRules[0]' <<< "$fence_policy") == '{"apiGroups":[""],"apiVersions":["v1"],"operations":["CREATE","UPDATE","DELETE"],"resources":["configmaps"],"scope":"Namespaced"}' ]] ||
  test::fail "Fence authorization resource rule drifted"
fence_match=$(yq -r '.spec.matchConditions[0].expression' <<< "$fence_policy")
[[ $fence_match == *"request.userInfo.username == '${session_authorizer}'"* ]] ||
  test::fail "Fence authorization does not select every Session Authorizer request"
[[ $fence_match == *"atlas-bootstrap-operation-fence-canary"* ]] ||
  test::fail "Fence authorization does not protect the canonical canary Fence"

shape_policy=$(RESOURCE_NAME=atlas-bootstrap-recovery-binding-shape-authorization-canary yq ea \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.spec | has("paramKind")' <<< "$shape_policy") == false ]] ||
  test::fail "Binding Shape authorization incorrectly depends on a parameter"
shape_match=$(RESOURCE_NAME=atlas-bootstrap-recovery-binding-shape-authorization-canary yq ea -r \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME)) | .spec.matchConditions[0].expression' \
  "$first_bundle")
expected_shape_match=$'request.userInfo.username == \'atlas:session-authz:12345678-1234-1234-1234-123456789abc:g4\' || (request.namespace == \'kube-system\' &&\n  ((request.operation != \'CREATE\' &&\n    (oldObject.metadata.name.startsWith(\'atlas-bg-canary-\') ||\n      (has(oldObject.metadata.labels) &&\n        \'atlas.io/recovery-session\' in oldObject.metadata.labels))) ||\n  (request.operation != \'DELETE\' &&\n    (object.metadata.name.startsWith(\'atlas-bg-canary-\') ||\n      (has(object.metadata.labels) &&\n        \'atlas.io/recovery-session\' in object.metadata.labels)))))'
[[ $shape_match == "$expected_shape_match" ]] ||
  test::fail "Binding Shape authorization does not select the exact old/new target union"

permission_policy=$(RESOURCE_NAME=atlas-bootstrap-recovery-permission-authorization-canary yq ea \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq -o=json -I=0 '.spec.paramKind' <<< "$permission_policy") == '{"apiVersion":"v1","kind":"ConfigMap"}' ]] ||
  test::fail "Permission authorization does not use a native ConfigMap parameter"
[[ $(yq -r '.spec.validations[0].expression' <<< "$permission_policy") == 'params != null' ]] ||
  test::fail "Permission authorization does not explicitly require the canary Fence parameter"
[[ $(yq -r '.spec.matchConditions[0].expression' <<< "$permission_policy") == "request.namespace == 'kube-system'" ]] ||
  test::fail "Permission authorization overrides the Binding old/new objectSelector"
permission_binding=$(RESOURCE_NAME=atlas-bootstrap-recovery-permission-authorization-canary yq ea \
  'select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == strenv(RESOURCE_NAME))' \
  "$first_bundle")
expected_param_ref='{"name":"atlas-bootstrap-operation-fence-canary","namespace":"kube-system","parameterNotFoundAction":"Deny"}'
[[ $(yq -o=json -I=0 '.spec.paramRef' <<< "$permission_binding") == "$expected_param_ref" ]] ||
  test::fail "Permission authorization does not fail closed on a missing canary Fence"
expected_selector='{"matchLabels":{"atlas.io/recovery-scope":"canary"},"matchExpressions":[{"key":"atlas.io/recovery-session","operator":"Exists"}]}'
[[ $(yq -o=json -I=0 '.spec.matchResources.objectSelector' <<< "$permission_binding") == "$expected_selector" ]] ||
  test::fail "Permission authorization object selector drifted"
test::pass "UPDATE label removal remains selected through oldObject and fails closed"

permission_expressions=$(yq -r '.spec.validations[].expression' <<< "$permission_policy")
for required_lineage in \
  authorizerPrincipal recoveryPrincipal sessionID planSHA256 clusterFingerprintSHA256 knownGoodRevision \
  recovery-fence-uid atlas-bootstrap-recovery-canary atlas-bg-canary-; do
  grep -Fq "$required_lineage" <<< "$permission_expressions" ||
    test::fail "Permission authorization omits Fence lineage: ${required_lineage}"
done
fence_instance_count=$(yq ea \
  '[select(.kind == "ConfigMap" and (.metadata.name == "atlas-bootstrap-operation-fence-canary" or .metadata.name == "atlas-bootstrap-operation-fence"))] | length' \
  "$first_bundle")
((fence_instance_count == 0)) || test::fail "definition bundle creates a canary or production Fence"
test::pass "Fence, Binding Shape, and Permission controls compose and missing Fence fails closed"

guard_fixture=$(RESOURCE_NAME=atlas-bootstrap-recovery-guard-canary yq ea \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.metadata.namespace' <<< "$guard_fixture") == kube-system &&
$(yq -o=json -I=0 '.data' <<< "$guard_fixture") == '{"sentinel":"recovery-guard-canary"}' ]] ||
  test::fail "Guard fixture is not inert and namespace confined"
guard_policy=$(RESOURCE_NAME=atlas-bootstrap-recovery-guard-authorization-canary yq ea \
  'select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME))' "$first_bundle")
[[ $(yq '.spec | has("paramKind")' <<< "$guard_policy") == false ]] ||
  test::fail "Guard authorization incorrectly depends on the Fence parameter"
guard_expressions=$(yq -r '.spec.matchConditions[].expression, .spec.validations[].expression' <<< "$guard_policy")
guard_match=$(yq -r '.spec.matchConditions[0].expression' <<< "$guard_policy")
for guard_contract in \
  "$recovery_operator" atlas-bootstrap-recovery-guard-canary policy.atlas-recovery-freeze.csv \
  'request.operation != '\''DELETE'\''' 'object.metadata.labels == oldObject.metadata.labels' \
  "(('policy.atlas-recovery-freeze.csv' in object.data) ? 2 : 1)" \
  "request.operation == 'CREATE' && has(object.data)" \
  "('policy.atlas-recovery-freeze.csv' in oldObject.data) !="; do
  grep -Fq "$guard_contract" <<< "$guard_expressions" ||
    test::fail "Guard authorization omits contract: ${guard_contract}"
done
! grep -Fq 'GuardValue' <<< "$guard_expressions" ||
  test::fail "Guard authorization still conflates absent keys with empty values"
test::pass "Guard authorization protects exact add/remove projection and denies destructive drift"

guard_data_transition_allowed() {
  local old_data=$1 new_data=$2
  local old_has_guard new_has_guard old_size new_size
  local guard_key='policy.atlas-recovery-freeze.csv'
  local guard_value='p, role:atlas-recovery-guard-canary, applications, *, */*, deny'

  old_has_guard=$(GUARD_KEY=$guard_key yq 'has(strenv(GUARD_KEY))' <<< "$old_data")
  new_has_guard=$(GUARD_KEY=$guard_key yq 'has(strenv(GUARD_KEY))' <<< "$new_data")
  old_size=$(yq 'length' <<< "$old_data")
  new_size=$(yq 'length' <<< "$new_data")

  [[ $(yq -r '.sentinel' <<< "$old_data") == recovery-guard-canary &&
  $(yq -r '.sentinel' <<< "$new_data") == recovery-guard-canary ]] || return 1
  [[ $old_size -eq 1 && $old_has_guard == false ||
    $old_size -eq 2 && $old_has_guard == true ]] || return 1
  [[ $new_size -eq 1 && $new_has_guard == false ||
    $new_size -eq 2 && $new_has_guard == true ]] || return 1
  [[ $old_has_guard != "$new_has_guard" ]] || return 1
  [[ $new_has_guard == false ||
    $(GUARD_KEY=$guard_key yq -r '.[strenv(GUARD_KEY)]' <<< "$new_data") == "$guard_value" ]]
}

guard_absent='{"sentinel":"recovery-guard-canary"}'
guard_present='{"sentinel":"recovery-guard-canary","policy.atlas-recovery-freeze.csv":"p, role:atlas-recovery-guard-canary, applications, *, */*, deny"}'
guard_replaced='{"sentinel":"recovery-guard-canary","unexpected":"replacement"}'
guard_data_transition_allowed "$guard_absent" "$guard_present" ||
  test::fail "exact Guard addition is rejected by the static projection model"
guard_data_transition_allowed "$guard_present" "$guard_absent" ||
  test::fail "exact Guard removal is rejected by the static projection model"
if guard_data_transition_allowed "$guard_present" "$guard_replaced"; then
  test::fail "Guard removal plus arbitrary data insertion is accepted"
fi
if guard_data_transition_allowed "$guard_replaced" "$guard_present"; then
  test::fail "Guard addition plus arbitrary data replacement is accepted"
fi
test::pass "Guard add/remove cannot replace another data key"

# Static routing matrix. Full projection hashes above bind these assertions to
# the reviewed CEL. Required CI performs server-side type checking; request
# probes remain part of the separately authorized disposable-cluster drill.
[[ $fence_match == *"request.userInfo.username == '${session_authorizer}'"* &&
  $fence_match == *"object.metadata.name == 'atlas-bootstrap-operation-fence-canary'"* &&
  $fence_match == *"oldObject.metadata.name == 'atlas-bootstrap-operation-fence-canary'"* ]] ||
  test::fail "Fence routing does not cover the authorizer and canonical old/new target"
[[ $shape_match == *"oldObject.metadata.labels"* &&
  $shape_match == *"object.metadata.labels"* &&
  $shape_match == *"atlas.io/recovery-session"* ]] ||
  test::fail "Binding Shape routing does not cover session-label add/remove"
[[ $(yq '.spec.paramRef.parameterNotFoundAction' <<< "$permission_binding") == Deny &&
$(yq '.spec.matchResources.objectSelector.matchExpressions[0].operator' <<< "$permission_binding") == Exists ]] ||
  test::fail "Permission routing does not deny a selected Binding when the Fence is absent"
[[ $guard_match == *"request.operation != 'CREATE'"* &&
  $guard_match == *"oldObject.data"* &&
  $guard_match == *"request.operation != 'DELETE'"* &&
  $guard_match == *"object.data"* &&
  $guard_match == *"policy.atlas-recovery-freeze.csv"* ]] ||
  test::fail "Guard routing does not cover key add, change, and removal through old/new objects"
[[ $(yq -r '.spec.validations[1].expression' <<< "$guard_policy") == "request.operation != 'DELETE'" ]] ||
  test::fail "Guard routing permits fixture deletion while the guard is selected"
test::pass "four-control static routing truth table is complete and fail closed"

assert_render_rejected() {
  local recovery=$1 authorizer=$2
  if "$recovery_cli" phase0 session-authorization-canary-manifests \
    --recovery-operator "$recovery" \
    --session-authorizer "$authorizer" > /dev/null 2>&1; then
    test::fail "invalid principal pair was accepted: ${recovery} / ${authorizer}"
  fi
}

assert_render_rejected atlas:break-glass:not-a-uid:g1 "$session_authorizer"
assert_render_rejected "$recovery_operator" atlas:session-authz:not-a-uid:g1
assert_render_rejected "$recovery_operator" atlas:session-authz:aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa:g4
assert_render_rejected "$recovery_operator" atlas:session-authz:12345678-1234-1234-1234-123456789abc:g0
assert_render_rejected "$recovery_operator" $'atlas:session-authz:12345678-1234-1234-1234-123456789abc:g4\nsecond-user'
if "$recovery_cli" phase0 session-authorization-canary-manifests \
  --recovery-operator "$recovery_operator" > /dev/null 2>&1; then
  test::fail "missing Session Authorizer was accepted"
fi
if "$recovery_cli" phase0 session-authorization-canary-manifests \
  --recovery-operator "$recovery_operator" \
  --session-authorizer "$session_authorizer" \
  --session-authorizer "$session_authorizer" > /dev/null 2>&1; then
  test::fail "duplicate Session Authorizer option was accepted"
fi
! grep -Fq '__ATLAS_' "$first_bundle" || test::fail "unresolved principal placeholder reached rendered YAML"
test::pass "Session Authorization principal inputs are exact, independent, and injection resistant"

test::assert_not_found 'session-authorization-canary|recovery-authorizer-canary' gitops bootstrap/atlas
test::assert_not_found '(^|[;&|[:space:]])(kubectl|kind|docker)[[:space:]]' \
  bootstrap/recovery/session-authorization-canary
if find bootstrap/recovery/session-authorization-canary -name kustomization.yaml -print -quit | grep -q .; then
  test::fail "Session Authorization canary definitions are reachable through Kustomize"
fi
production_fence_count=$(yq ea \
  '[select((.metadata.name == "atlas-bootstrap-operation-fence") or (.spec.paramRef.name == "atlas-bootstrap-operation-fence"))] | length' \
  "$first_bundle")
((production_fence_count == 0)) || test::fail "canary bundle selects the production Fence"
production_role_ref_count=$(yq ea \
  '[select(.roleRef.name == "atlas-bootstrap-recovery" or (.rules[]?.resourceNames[]? == "atlas-bootstrap-recovery"))] | length' \
  "$first_bundle")
((production_role_ref_count == 0)) || test::fail "canary bundle selects a production Recovery Role"
! grep -Fq 'atlas-bootstrap-evidence-protection' "$first_bundle" ||
  test::fail "canary bundle contains production evidence protection"
for control_name in \
  atlas-bootstrap-recovery-fence-authorization-canary \
  atlas-bootstrap-recovery-binding-shape-authorization-canary \
  atlas-bootstrap-recovery-permission-authorization-canary \
  atlas-bootstrap-recovery-guard-authorization-canary; do
  grep -Fq "$control_name" docs/adr/0003-bootstrap-break-glass-recovery.md ||
    test::fail "canary control is not mapped by ADR-0003: ${control_name}"
done
test::pass "Session Authorization definitions remain render-only and production-isolated"
