#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-admission-canary.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

recovery_cli=./bootstrap/recovery/atlas-recovery
recovery_operator=atlas:break-glass:12345678-1234-1234-1234-123456789abc:g7
first_bundle="${test_workspace}/first.yaml"
second_bundle="${test_workspace}/second.yaml"

"$recovery_cli" phase0 admission-canary-manifests \
  --recovery-operator "$recovery_operator" > "$first_bundle"
"$recovery_cli" phase0 admission-canary-manifests \
  --recovery-operator "$recovery_operator" > "$second_bundle"
cmp -s "$first_bundle" "$second_bundle" || test::fail "admission canary rendering is not deterministic"

document_count=$(yq ea '[.] | length' "$first_bundle")
((document_count == 5)) || test::fail "admission canary bundle does not contain exactly five resources"

assert_one_resource() {
  local kind=$1 name=$2 count
  count=$(RESOURCE_KIND=$kind RESOURCE_NAME=$name yq ea \
    '[select(.kind == strenv(RESOURCE_KIND) and .metadata.name == strenv(RESOURCE_NAME))] | length' \
    "$first_bundle")
  ((count == 1)) || test::fail "expected exactly one ${kind}/${name}"
}

assert_one_resource ConfigMap atlas-bootstrap-admission-escape-canary
assert_one_resource ClusterRole atlas-bootstrap-break-glass-escape
assert_one_resource ClusterRoleBinding atlas-bootstrap-break-glass-escape
assert_one_resource ValidatingAdmissionPolicy atlas-bootstrap-admission-escape-canary
assert_one_resource ValidatingAdmissionPolicyBinding atlas-bootstrap-admission-escape-canary

assert_resource_projection() {
  local kind=$1 expected_sha=$2 actual_sha

  actual_sha=$(
    RESOURCE_KIND=$kind yq ea -o=json -I=0 \
      'select(.kind == strenv(RESOURCE_KIND)) | sort_keys(..)' \
      "$first_bundle" |
      shasum -a 256 |
      awk '{print $1}'
  )
  [[ $actual_sha == "$expected_sha" ]] ||
    test::fail "${kind} complete projection drifted: expected ${expected_sha}, got ${actual_sha}"
}

# These hashes cover every rendered field, including rule multiplicity, CEL,
# metadata, roleRef, and subjects. Readable assertions below document the
# security boundary and make failures actionable.
assert_resource_projection ConfigMap 1de0f296ea0d8e970b9ca3b9665dc9c647afd7da1a0d7e334a6a531727fc8cdc
assert_resource_projection ClusterRole 18ec7e9d447c07be101df0454e06ed202eb93eb6d00b287c66a3d985a0e24e97
assert_resource_projection ClusterRoleBinding d74343d40b7c631b10ebcdfa02f4afab33a15df8df3d7af32c153f33d53ceeec
assert_resource_projection ValidatingAdmissionPolicy a6b7f1a4b1a84c834f232b9d89fc1f149b95f046f36628ce0cdd1ef2586ac7ae
assert_resource_projection ValidatingAdmissionPolicyBinding 88a357b9e5d456fd137c385f1ddd4aa7f4b2e71be057c62ca6191adb233336eb

fixture=$(yq ea 'select(.kind == "ConfigMap")' "$first_bundle")
[[ $(yq '.metadata.namespace' <<< "$fixture") == kube-system ]] || test::fail "canary fixture is outside kube-system"
[[ $(yq '.data.sentinel' <<< "$fixture") == admission-escape-canary ]] || test::fail "canary fixture is not inert"

policy=$(yq ea 'select(.kind == "ValidatingAdmissionPolicy")' "$first_bundle")
[[ $(yq '.spec.failurePolicy' <<< "$policy") == Fail ]] || test::fail "canary Policy is not fail closed"
[[ $(yq '.spec.matchConstraints.resourceRules | length' <<< "$policy") -eq 1 ]] ||
  test::fail "canary Policy does not have exactly one resource rule"
policy_rule=$(yq -o=json -I=0 '.spec.matchConstraints.resourceRules[0]' <<< "$policy")
expected_policy_rule='{"apiGroups":[""],"apiVersions":["v1"],"operations":["CREATE","UPDATE","DELETE"],"resources":["configmaps"],"scope":"Namespaced"}'
[[ $policy_rule == "$expected_policy_rule" ]] || test::fail "canary Policy resource rule drifted"
[[ $(yq '.spec.matchConditions | length' <<< "$policy") -eq 2 ]] ||
  test::fail "canary Policy does not have exactly two match conditions"
condition_names=$(yq -o=json -I=0 '[.spec.matchConditions[].name]' <<< "$policy")
[[ $condition_names == '["canonical-canary-target","ordinary-principal"]' ]] ||
  test::fail "canary Policy match condition names or order drifted"
target_expression=$(yq ea -r \
  'select(.kind == "ValidatingAdmissionPolicy") | .spec.matchConditions[0].expression' \
  "$first_bundle")
expected_target_expression=$'request.namespace == \'kube-system\' && (request.operation == \'DELETE\'\n  ? oldObject.metadata.name == \'atlas-bootstrap-admission-escape-canary\'\n  : object.metadata.name == \'atlas-bootstrap-admission-escape-canary\')'
[[ $target_expression == "$expected_target_expression" ]] ||
  test::fail "canary Policy target expression drifted"
principal_expression=$(yq -r '.spec.matchConditions[1].expression' <<< "$policy")
[[ $principal_expression == "request.userInfo.username != '${recovery_operator}'" ]] ||
  test::fail "canary Policy exception is not the exact Recovery Operator"
[[ $(yq '.spec.validations | length' <<< "$policy") -eq 1 ]] ||
  test::fail "canary Policy does not have exactly one validation"
validation=$(yq -o=json -I=0 '.spec.validations[0]' <<< "$policy")
expected_validation='{"expression":"false","message":"Atlas admission escape canary mutation requires the exact Recovery Operator","reason":"Forbidden"}'
[[ $validation == "$expected_validation" ]] || test::fail "canary Policy validation drifted"
[[ $(yq -r '.spec.validations[0].expression' <<< "$policy") == false ]] ||
  test::fail "canary Policy does not deny matched ordinary principals"

binding=$(yq ea 'select(.kind == "ValidatingAdmissionPolicyBinding")' "$first_bundle")
binding_spec=$(yq -o=json -I=0 '.spec' <<< "$binding")
expected_binding_spec='{"policyName":"atlas-bootstrap-admission-escape-canary","validationActions":["Audit","Deny"]}'
[[ $binding_spec == "$expected_binding_spec" ]] || test::fail "canary Binding spec drifted"
test::pass "admission canary definitions are deterministic, isolated, and fail closed"

role=$(yq ea 'select(.kind == "ClusterRole")' "$first_bundle")
role_binding=$(yq ea 'select(.kind == "ClusterRoleBinding")' "$first_bundle")
[[ $(yq '.rules | length' <<< "$role") -eq 3 ]] || test::fail "Escape role does not have exactly three rules"
expected_vap_rule='{"apiGroups":["admissionregistration.k8s.io"],"resources":["validatingadmissionpolicies"],"resourceNames":["atlas-bootstrap-admission-escape-canary"],"verbs":["get"]}'
expected_binding_rule='{"apiGroups":["admissionregistration.k8s.io"],"resources":["validatingadmissionpolicybindings"],"resourceNames":["atlas-bootstrap-admission-escape-canary"],"verbs":["get","patch","update"]}'
expected_namespace_rule='{"apiGroups":[""],"resources":["namespaces"],"resourceNames":["kube-system"],"verbs":["get"]}'
[[ $(yq -o=json -I=0 '.rules[0]' <<< "$role") == "$expected_vap_rule" ]] ||
  test::fail "Escape VAP read rule drifted"
[[ $(yq -o=json -I=0 '.rules[1]' <<< "$role") == "$expected_binding_rule" ]] ||
  test::fail "Escape Binding authority rule drifted"
[[ $(yq -o=json -I=0 '.rules[2]' <<< "$role") == "$expected_namespace_rule" ]] ||
  test::fail "Escape Namespace read rule drifted"
configmap_rule_count=$(yq '[.rules[] | select(.apiGroups == [""] and (.resources | any_c(. == "configmaps")))] | length' <<< "$role")
((configmap_rule_count == 0)) || test::fail "Escape role grants ConfigMap access outside a namespace RoleBinding"
! grep -Eq '(^|[[:space:]])\*([[:space:]]|$)' <<< "$role" || test::fail "Escape role contains a wildcard"
role_ref=$(yq -o=json -I=0 '.roleRef' <<< "$role_binding")
expected_role_ref='{"apiGroup":"rbac.authorization.k8s.io","kind":"ClusterRole","name":"atlas-bootstrap-break-glass-escape"}'
[[ $role_ref == "$expected_role_ref" ]] || test::fail "Escape binding roleRef drifted"
subjects=$(yq -o=json -I=0 '.subjects' <<< "$role_binding")
expected_subjects="[{\"apiGroup\":\"rbac.authorization.k8s.io\",\"kind\":\"User\",\"name\":\"${recovery_operator}\"}]"
[[ $subjects == "$expected_subjects" ]] || test::fail "Escape binding subject projection drifted"
test::pass "Escape RBAC is exact-user, canary-only, and grants no ConfigMap access in any Namespace"

assert_username_rejected() {
  local username=$1
  if "$recovery_cli" phase0 admission-canary-manifests \
    --recovery-operator "$username" > /dev/null 2>&1; then
    test::fail "invalid Recovery Operator username was accepted: ${username}"
  fi
}

assert_username_rejected atlas:break-glass:not-a-uid:g1
assert_username_rejected atlas:break-glass:12345678-1234-1234-1234-123456789abc:g0
assert_username_rejected atlas:break-glass:12345678-1234-1234-1234-123456789abC:g1
assert_username_rejected atlas:break-glass:12345678-1234-1234-1234-123456789abc:g1:group
assert_username_rejected atlas:session-authz:12345678-1234-1234-1234-123456789abc:g1
assert_username_rejected $'atlas:break-glass:12345678-1234-1234-1234-123456789abc:g1\nsecond-user'

if "$recovery_cli" phase0 admission-canary-manifests > /dev/null 2>&1; then
  test::fail "missing Recovery Operator username was accepted"
fi
if "$recovery_cli" phase0 admission-canary-manifests \
  --recovery-operator "$recovery_operator" \
  --recovery-operator "$recovery_operator" > /dev/null 2>&1; then
  test::fail "duplicate Recovery Operator option was accepted"
fi
! grep -Fq '__ATLAS_RECOVERY_OPERATOR_USERNAME__' "$first_bundle" ||
  test::fail "unresolved principal placeholder reached rendered YAML"
test::pass "Recovery Operator rendering input is exact and injection resistant"

test::assert_not_found 'admission-escape-canary|admission-canary' gitops
test::assert_not_found 'admission-canary|atlas-bootstrap-break-glass-escape' bootstrap/atlas
test::assert_not_found '(kubectl|kind|docker)[[:space:]]' bootstrap/recovery/admission-canary
if find bootstrap/recovery/admission-canary -name kustomization.yaml -print -quit | grep -q .; then
  test::fail "admission canary definitions are reachable through Kustomize"
fi
! grep -Fq 'atlas-bootstrap-evidence-protection' "$first_bundle" ||
  test::fail "canary bundle contains production evidence protection"
! grep -Fq 'atlas-bootstrap-operation-fence' "$first_bundle" ||
  test::fail "canary bundle contains a production Operation Fence"
test::pass "canary definitions remain unreachable, render-only, and production-inert"
