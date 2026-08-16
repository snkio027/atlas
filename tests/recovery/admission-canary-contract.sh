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

fixture=$(yq ea 'select(.kind == "ConfigMap")' "$first_bundle")
[[ $(yq '.metadata.namespace' <<< "$fixture") == kube-system ]] || test::fail "canary fixture is outside kube-system"
[[ $(yq '.data.sentinel' <<< "$fixture") == admission-escape-canary ]] || test::fail "canary fixture is not inert"

policy=$(yq ea 'select(.kind == "ValidatingAdmissionPolicy")' "$first_bundle")
[[ $(yq '.spec.failurePolicy' <<< "$policy") == Fail ]] || test::fail "canary Policy is not fail closed"
operations=$(yq -r '.spec.matchConstraints.resourceRules[0].operations | sort | join(",")' <<< "$policy")
[[ $operations == CREATE,DELETE,UPDATE ]] || test::fail "canary Policy mutation inventory is incomplete"
[[ $(yq -r '.spec.matchConstraints.resourceRules[0].resources | join(",")' <<< "$policy") == configmaps ]] ||
  test::fail "canary Policy matches a non-ConfigMap resource"
target_expression=$(yq -r \
  '.spec.matchConditions[] | select(.name == "canonical-canary-target") | .expression' \
  <<< "$policy")
[[ $target_expression == *"request.namespace == 'kube-system'"* ]] ||
  test::fail "canary Policy does not confine the fixture Namespace"
[[ $target_expression == *"request.operation == 'DELETE'"* ]] ||
  test::fail "canary Policy does not distinguish DELETE oldObject"
[[ $target_expression == *"oldObject.metadata.name == 'atlas-bootstrap-admission-escape-canary'"* ]] ||
  test::fail "canary Policy does not validate the deleted object name"
[[ $target_expression == *"object.metadata.name == 'atlas-bootstrap-admission-escape-canary'"* ]] ||
  test::fail "canary Policy does not validate the created or updated object name"
principal_expression=$(yq -r \
  '.spec.matchConditions[] | select(.name == "ordinary-principal") | .expression' \
  <<< "$policy")
[[ $principal_expression == "request.userInfo.username != '${recovery_operator}'" ]] ||
  test::fail "canary Policy exception is not the exact Recovery Operator"
[[ $(yq -r '.spec.validations[0].expression' <<< "$policy") == false ]] ||
  test::fail "canary Policy does not deny matched ordinary principals"
[[ $(yq -r '.spec.validations[0].reason' <<< "$policy") == Forbidden ]] ||
  test::fail "canary Policy does not use the intended denial reason"

binding=$(yq ea 'select(.kind == "ValidatingAdmissionPolicyBinding")' "$first_bundle")
[[ $(yq -r '.spec.policyName' <<< "$binding") == atlas-bootstrap-admission-escape-canary ]] ||
  test::fail "canary Binding references the wrong Policy"
actions=$(yq -r '.spec.validationActions | sort | join(",")' <<< "$binding")
[[ $actions == Audit,Deny ]] || test::fail "canary Binding is not the exact Audit+Deny set"
raw_actions=$(yq -o=json -I=0 '.spec.validationActions' <<< "$binding")
[[ $raw_actions == '["Audit","Deny"]' ]] || test::fail "canary Binding is not in canonical action order"
test::pass "admission canary definitions are deterministic, isolated, and fail closed"

role=$(yq ea 'select(.kind == "ClusterRole")' "$first_bundle")
role_binding=$(yq ea 'select(.kind == "ClusterRoleBinding")' "$first_bundle")
[[ $(yq '.subjects | length' <<< "$role_binding") -eq 1 ]] ||
  test::fail "Escape binding does not have exactly one subject"
[[ $(yq -r '.subjects[0].kind' <<< "$role_binding") == User ]] ||
  test::fail "Escape binding uses a group or service account"
[[ $(yq -r '.subjects[0].name' <<< "$role_binding") == "$recovery_operator" ]] ||
  test::fail "Escape binding subject drifted"
[[ $(yq -r '.roleRef.name' <<< "$role_binding") == atlas-bootstrap-break-glass-escape ]] ||
  test::fail "Escape binding references the wrong role"

role_verbs=$(yq -r '.rules[].verbs[]' <<< "$role" | sort -u | paste -sd, -)
[[ $role_verbs == get,patch,update ]] ||
  test::fail "Escape role grants verbs beyond canary read and Binding patch/update"
role_resources=$(yq -r '.rules[].resources[]' <<< "$role" | sort -u | paste -sd, -)
[[ $role_resources == configmaps,namespaces,validatingadmissionpolicies,validatingadmissionpolicybindings ]] ||
  test::fail "Escape role grants a resource outside its canary inspection boundary"
mutation_rule=$(yq '.rules[] | select(.verbs | any_c(. == "patch" or . == "update"))' <<< "$role")
[[ $(yq -r '.resources | join(",")' <<< "$mutation_rule") == validatingadmissionpolicybindings ]] ||
  test::fail "Escape mutation is not confined to the canary Binding kind"
[[ $(yq -r '.resourceNames | join(",")' <<< "$mutation_rule") == atlas-bootstrap-admission-escape-canary ]] ||
  test::fail "Escape mutation is not confined to the exact canary Binding"
! grep -Eq '(^|[[:space:]])\*([[:space:]]|$)' <<< "$role" || test::fail "Escape role contains a wildcard"
test::pass "Escape RBAC grants only exact-user canary read and Binding suspend/restore authority"

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
assert_username_rejected atlas:recovery-authorizer:12345678-1234-1234-1234-123456789abc:g1
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
