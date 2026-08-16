#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-phase0-canary-ceremony.XXXXXX")
trap 'rm -rf -- "$test_workspace"' EXIT

readonly ATLAS_RECOVERY_ROOT_DIR=$ATLAS_TEST_ROOT

recovery::die() {
  printf '%s\n' "$*" >&2
  return 1
}

# shellcheck source=bootstrap/recovery/admission-canary/render.sh
source bootstrap/recovery/admission-canary/render.sh
# shellcheck source=bootstrap/recovery/session-authorization-canary/render.sh
source bootstrap/recovery/session-authorization-canary/render.sh
# shellcheck source=bootstrap/recovery/canary-session.sh
source bootstrap/recovery/canary-session.sh
# shellcheck source=bootstrap/recovery/canary-ceremony.sh
source bootstrap/recovery/canary-ceremony.sh

recovery_cli=./bootstrap/recovery/atlas-recovery

"$recovery_cli" --help | grep -Fq 'phase0 canary-drill'
if "$recovery_cli" phase0 canary-drill --unknown value > /dev/null 2>&1; then
  test::fail "canary drill accepted an unknown option"
fi
if "$recovery_cli" phase0 canary-drill \
  --cluster-name one --cluster-name two > /dev/null 2>&1; then
  test::fail "canary drill accepted a duplicate option"
fi
if "$recovery_cli" phase0 canary-drill --cluster-name > /dev/null 2>&1; then
  test::fail "canary drill accepted a missing option value"
fi

calls="${test_workspace}/calls"
: > "$calls"
phase0_session::admin() {
  local IFS=' '
  printf '%s\n' "$*" >> "$calls"
}
phase0_session::_assert_runtime_absent
[[ $(wc -l < "$calls" | tr -d ' ') == 18 ]] || test::fail "runtime absence check did not cover all canary objects"
grep -Fqx 'get configmap atlas-bootstrap-admission-escape-canary --ignore-not-found -o name -n kube-system' "$calls" ||
  test::fail "runtime absence check omitted the admission fixture namespace"
grep -Fqx 'get validatingadmissionpolicy atlas-bootstrap-recovery-guard-authorization-canary --ignore-not-found -o name' "$calls" ||
  test::fail "runtime absence check omitted a four-control Policy"
grep -Fqx 'get configmap atlas-bootstrap-operation-fence-canary --ignore-not-found -o name -n kube-system' "$calls" ||
  test::fail "runtime absence check omitted the canonical Fence"
absence_calls="${test_workspace}/absence-calls"
cp "$calls" "$absence_calls"

: > "$calls"
phase0_session::_kubectl() {
  local kubeconfig=$1
  shift
  local IFS=' '
  printf '%s\t%s\n' "$kubeconfig" "$*" >> "$calls"
  printf '{}\n'
}
phase0_ceremony::_record_object principal.kubeconfig configmap fixture "${test_workspace}/fixture.json"
phase0_ceremony::_record_object principal.kubeconfig validatingadmissionpolicy policy "${test_workspace}/policy.json"
grep -Fqx $'principal.kubeconfig\tget configmap fixture -o json -n kube-system' "$calls" ||
  test::fail "object evidence read escaped kube-system"
grep -Fqx $'principal.kubeconfig\tget validatingadmissionpolicy policy -o json' "$calls" ||
  test::fail "cluster-scoped evidence read gained a namespace"

audit_source="${test_workspace}/audit.log"
audit_snapshot="${test_workspace}/audit-snapshot.log"
printf '%s\n' '{"kind":"Event","requestURI":"/readyz"}' > "$audit_source"
chmod 0600 "$audit_source"
ATLAS_PHASE0_TARGET[audit_log]=$audit_source
phase0_ceremony::_snapshot_audit_log "$audit_snapshot"
[[ $(< "$audit_snapshot") == '{"kind":"Event","requestURI":"/readyz"}' ]] ||
  test::fail "audit snapshot changed a complete event"
if [[ $(uname -s) == Darwin ]]; then
  audit_snapshot_mode=$(stat -f '%Lp' "$audit_snapshot")
else
  audit_snapshot_mode=$(stat -c '%a' "$audit_snapshot")
fi
[[ $audit_snapshot_mode == 400 ]] ||
  test::fail "audit snapshot is not read-only"
printf '%s\n' '{"kind":"Event"' > "$audit_source"
sleep() { :; }
if phase0_ceremony::_snapshot_audit_log "${test_workspace}/invalid-audit.log" > /dev/null 2>&1; then
  test::fail "audit snapshot accepted a malformed event"
fi
unset -f sleep

test_snapshot="${test_workspace}/snapshot.json"
printf '{"metadata":{"uid":"00000000-0000-0000-0000-000000000001","resourceVersion":"42"}}\n' > "$test_snapshot"
chmod 0400 "$test_snapshot"
session="${test_workspace}/evidence"
mkdir -m 0700 "$session"
mkdir -m 0700 "$session/authorization" "$session/audit" "$session/postflight"
ATLAS_PHASE0_OPERATION[evidence_session]=$session
: > "$calls"
phase0_ceremony::_delete_with_preconditions principal.kubeconfig \
  /api/v1/namespaces/kube-system/configmaps/example "$test_snapshot" example
delete_options="${session}/postflight/delete-example.json"
[[ $(< "$delete_options") == '{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":"00000000-0000-0000-0000-000000000001","resourceVersion":"42"}}' ]] ||
  test::fail "delete options did not bind UID and resourceVersion"
grep -Fqx $'principal.kubeconfig\tdelete --raw /api/v1/namespaces/kube-system/configmaps/example -f '"$delete_options" "$calls" ||
  test::fail "exact delete did not send the reviewed DeleteOptions body"

ATLAS_PHASE0_OPERATION[session_id]=0123456789abcdef0123456789abcdef
ATLAS_PHASE0_OPERATION[operation_id]=abcdef0123456789abcdef0123456789
ATLAS_PHASE0_OPERATION[target_fingerprint]=$(printf 'a%.0s' {1..64})
ATLAS_PHASE0_OPERATION[authorizer_principal]=atlas:recovery-authorizer:00000000-0000-0000-0000-000000000000:g2
ATLAS_PHASE0_OPERATION[recovery_principal]=atlas:break-glass:00000000-0000-0000-0000-000000000000:g2
ATLAS_PHASE0_OPERATION[previous_authorizer_principal]=atlas:recovery-authorizer:00000000-0000-0000-0000-000000000000:g1
ATLAS_PHASE0_OPERATION[previous_recovery_principal]=atlas:break-glass:00000000-0000-0000-0000-000000000000:g1
ATLAS_PHASE0_OPERATION[prepared_at]=2026-08-17T00:00:00Z
ATLAS_PHASE0_OPERATION[plan_sha]=$(printf 'b%.0s' {1..64})
ATLAS_PHASE0_OPERATION[action_id]=phase0-test
ATLAS_PHASE0_OPERATION[actor]=501:test
ATLAS_PHASE0_OPERATION[api_server]=https://127.0.0.1:6443
ATLAS_PHASE0_OPERATION[namespace_uid]=00000000-0000-0000-0000-000000000000
ATLAS_PHASE0_OPERATION[git_tree]=$(printf 'd%.0s' {1..40})
ATLAS_PHASE0_OPERATION[creation_plan_sha]=$(printf 'e%.0s' {1..64})
ATLAS_PHASE0_OPERATION[creation_journal_tip]=$(printf 'f%.0s' {1..64})
ATLAS_PHASE0_OPERATION[admission_bundle_sha]=$(printf '1%.0s' {1..64})
ATLAS_PHASE0_OPERATION[session_bundle_sha]=$(printf '2%.0s' {1..64})
ATLAS_PHASE0_OPERATION[versions_sha]=$(printf '3%.0s' {1..64})
ATLAS_PHASE0_OPERATION[audit_policy_sha]=$(printf '4%.0s' {1..64})
ATLAS_PHASE0_OPERATION[admin_kubeconfig_sha]=$(printf '5%.0s' {1..64})
ATLAS_PHASE0_OPERATION[ca_data]=dGVzdA==
ATLAS_PHASE0_TARGET[known_good_revision]=$(printf 'c%.0s' {1..40})
ATLAS_PHASE0_TARGET[cluster_name]=atlas-recovery-drill-20260817t000000z-0123abcd
ATLAS_PHASE0_TARGET[context]='kind-atlas-recovery-drill-20260817t000000z-0123abcd'
ATLAS_PHASE0_TARGET[admin_kubeconfig]=/encrypted/admin.kubeconfig
ATLAS_PHASE0_TARGET[audit_directory]=/encrypted/audit
ATLAS_PHASE0_TARGET[creation_evidence]=/encrypted/creation/session
ATLAS_PHASE0_TARGET[credential_directory]=/encrypted/credentials
ATLAS_PHASE0_TARGET[storage_assertion]=encrypted-owner-controlled
plan="${session}/plan.json"
phase0_session::_write_plan "$plan"
[[ $(yq -r '.adminKubeconfig' "$plan") == /encrypted/admin.kubeconfig &&
$(yq -r '.auditDirectory' "$plan") == /encrypted/audit &&
$(yq -r '.creationEvidence' "$plan") == /encrypted/creation/session &&
$(yq -r '.evidenceSession' "$plan") == "$session" &&
$(yq -r '.credentialDirectory' "$plan") == /encrypted/credentials &&
$(yq -r '.credentialCustody' "$plan") == separate-principal-subdirectories-single-operator-drill &&
$(yq -r '.recoveryPrincipal' "$plan") == "${ATLAS_PHASE0_OPERATION[recovery_principal]}" &&
$(yq -r '.previousRecoveryPrincipal' "$plan") == "${ATLAS_PHASE0_OPERATION[previous_recovery_principal]}" &&
$(yq -r '.namespaceUID' "$plan") == 00000000-0000-0000-0000-000000000000 ]] ||
  test::fail "runtime plan does not bind every managed path and cluster identity"
fence="${session}/authorization/fence.yaml"
binding="${session}/authorization/binding.yaml"
phase0_ceremony::_write_fence "$fence"
phase0_ceremony::_write_permission_binding "$binding" \
  00000000-0000-0000-0000-000000000001 "${ATLAS_PHASE0_OPERATION[plan_sha]}"
[[ $(yq -r '.metadata.namespace' "$fence") == kube-system &&
$(yq -r '.immutable' "$fence") == true &&
$(yq -r '.data | length' "$fence") == 11 &&
$(yq -r '.data.sessionID' "$fence") == "${ATLAS_PHASE0_OPERATION[session_id]}" ]] ||
  test::fail "Fence projection is not exact"
[[ $(yq -r '.metadata.name' "$binding") == "atlas-bg-canary-${ATLAS_PHASE0_OPERATION[session_id]}" &&
$(yq -r '.metadata.annotations."atlas.io/recovery-fence-uid"' "$binding") == 00000000-0000-0000-0000-000000000001 &&
$(yq -r '.subjects | length' "$binding") == 1 &&
$(yq -r '.subjects[0].name' "$binding") == "${ATLAS_PHASE0_OPERATION[recovery_principal]}" ]] ||
  test::fail "temporary permission Binding projection is not exact"

approved_admission="${session}/authorization/approved-admission.yaml"
approved_session="${session}/authorization/approved-session.yaml"
approved_static="${session}/authorization/approved-session-static.yaml"
approved_activation="${session}/authorization/approved-authorizer-activation.yaml"
admission_canary::render_manifests "${ATLAS_PHASE0_OPERATION[recovery_principal]}" > "$approved_admission"
session_canary::render_manifests "${ATLAS_PHASE0_OPERATION[recovery_principal]}" \
  "${ATLAS_PHASE0_OPERATION[authorizer_principal]}" > "$approved_session"
chmod 0400 "$approved_admission" "$approved_session"
yq ea 'select(.kind != "RoleBinding" or .metadata.name != "atlas-bootstrap-recovery-authorizer-canary")' \
  "$approved_session" > "$approved_static"
yq ea 'select(.kind == "RoleBinding" and .metadata.name == "atlas-bootstrap-recovery-authorizer-canary")' \
  "$approved_session" > "$approved_activation"
chmod 0400 "$approved_static" "$approved_activation"
ATLAS_PHASE0_OPERATION[admission_bundle]=$approved_admission
ATLAS_PHASE0_OPERATION[session_bundle]=$approved_session
ATLAS_PHASE0_OPERATION[session_static_bundle]=$approved_static
ATLAS_PHASE0_OPERATION[authorizer_activation_bundle]=$approved_activation
projection_drift=''
phase0_session::admin() {
  local command=$1 resource=$2 name=$3 phase namespace inventory_resource kind inventory_name label bundle
  [[ $command == get ]] || return 1
  while IFS=$'\t' read -r phase namespace inventory_resource kind inventory_name label; do
    [[ $resource == "$inventory_resource" && $name == "$inventory_name" ]] || continue
    if [[ $phase == admission ]]; then bundle=$approved_admission; else bundle=$approved_session; fi
    if [[ $projection_drift == "$label" ]]; then
      KIND=$kind NAMESPACE=$namespace NAME=$inventory_name yq ea -o=json -I=0 \
        'select(.kind == env(KIND) and .metadata.name == env(NAME) and
          (.metadata.namespace // "cluster") == env(NAMESPACE)) |
          .metadata.labels."atlas.io/test-drift" = "present"' "$bundle"
    else
      KIND=$kind NAMESPACE=$namespace NAME=$inventory_name yq ea -o=json -I=0 \
        'select(.kind == env(KIND) and .metadata.name == env(NAME) and
          (.metadata.namespace // "cluster") == env(NAMESPACE))' "$bundle"
    fi
    return 0
  done < <(phase0_ceremony::_definition_inventory)
  return 1
}
phase0_ceremony::_verify_live_definitions static
phase0_ceremony::_verify_live_definitions full
[[ $(wc -l < "${session}/authorization/static-live-projections.sha256" | tr -d ' ') == 16 ]] ||
  test::fail "static live projection check did not cover exactly 16 definitions"
[[ $(wc -l < "${session}/authorization/full-live-projections.sha256" | tr -d ' ') == 17 ]] ||
  test::fail "activated live projection check did not cover exactly 17 definitions"
projection_drift=guard-fixture
if phase0_ceremony::_verify_live_definitions full > /dev/null 2>&1; then
  test::fail "live projection verification accepted a drifted definition"
fi
projection_drift=''

audit_delta="${session}/audit/current-session-test.jsonl"
printf '%s\n' \
  "{\"kind\":\"Event\",\"requestURI\":\"/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/rolebindings/atlas-bg-canary-${ATLAS_PHASE0_OPERATION[session_id]}\",\"user\":{\"username\":\"${ATLAS_PHASE0_OPERATION[authorizer_principal]}\"},\"responseStatus\":{\"code\":201},\"requestObject\":{\"metadata\":{\"labels\":{\"atlas.io/recovery-session\":\"${ATLAS_PHASE0_OPERATION[session_id]}\"}}}}" \
  "{\"kind\":\"Event\",\"requestURI\":\"/api/v1/namespaces/kube-system/configmaps/rejected\",\"user\":{\"username\":\"${ATLAS_PHASE0_OPERATION[authorizer_principal]}\"},\"responseStatus\":{\"code\":403},\"annotations\":{\"validation.policy.admission.k8s.io/fence\":\"denied\"}}" \
  "{\"kind\":\"Event\",\"requestURI\":\"/api/v1/namespaces/kube-system/configmaps/atlas-bootstrap-recovery-guard-canary\",\"user\":{\"username\":\"${ATLAS_PHASE0_OPERATION[recovery_principal]}\"},\"responseStatus\":{\"code\":200}}" \
  "{\"kind\":\"Event\",\"requestURI\":\"/api/v1/namespaces/kube-system/configmaps/atlas-bootstrap-recovery-guard-canary\",\"user\":{\"username\":\"${ATLAS_PHASE0_OPERATION[recovery_principal]}\"},\"responseStatus\":{\"code\":403},\"annotations\":{\"validation.policy.admission.k8s.io/guard\":\"denied\"}}" > "$audit_delta"
phase0_ceremony::_verify_audit_delta "$audit_delta"
: > "${session}/audit/empty-delta.jsonl"
if phase0_ceremony::_verify_audit_delta "${session}/audit/empty-delta.jsonl" > /dev/null 2>&1; then
  test::fail "stale-only audit evidence satisfied the current session"
fi
printf '%s\n' '{"kind":"Event","requestURI":"/readyz","user":{"username":"kubernetes-admin"},"responseStatus":{"code":200}}' > "$audit_source"
(
  phase0_session::_path_identity() { printf '1:2\n'; }
  phase0_session::journal_append() { :; }
  phase0_ceremony::_capture_audit_boundary
)
[[ $(yq -r '.lineCount' "${session}/audit/pre-mutation-boundary.json") == 1 &&
$(yq -r '.sourceIdentity' "${session}/audit/pre-mutation-boundary.json") == 1:2 ]] ||
  test::fail "pre-mutation audit boundary omitted its offset or source identity"

read_only_inventory="${session}/authorization/read-only-permissions.txt"
unsafe_inventory="${session}/authorization/unsafe-permissions.txt"
declare -a unsafe_permissions=(
  $'kube-system\tpods   []   []   [get create]'
  $'kube-system\tdeployments.apps   []   []   [*]'
  $'kube-system\tdeployments.apps   []   []   [get proxy]'
  $'kube-system\tdeployments.apps   []   []   [get future-verb]'
  $'kube-system\tdeployments.apps   []   []   [get'
)
printf '%s\n' $'kube-system\tselfsubjectaccessreviews.authorization.k8s.io   []   []   [create]' \
  $'kube-system\t                                              [/api]   []   [get]' \
  $'kube-system\tdeployments.apps   []   []   [get list watch]' > "$read_only_inventory"
phase0_ceremony::_assert_permission_inventory_non_mutating "$read_only_inventory"
for unsafe_permission in "${unsafe_permissions[@]}"; do
  printf '%s\n' "$unsafe_permission" > "$unsafe_inventory"
  if phase0_ceremony::_assert_permission_inventory_non_mutating "$unsafe_inventory" > /dev/null 2>&1; then
    test::fail "effective-permission baseline accepted an unknown, mutating, or malformed grant: ${unsafe_permission}"
  fi
done

cleanup_calls="${test_workspace}/cleanup-calls"
: > "$cleanup_calls"
phase0_ceremony::_snapshot_for_delete() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$cleanup_calls"
  printf '%s\n' "$test_snapshot"
}
phase0_ceremony::_delete_with_preconditions() {
  printf 'delete\t%s\t%s\t%s\n' "$2" "$3" "$4" >> "$cleanup_calls"
}
phase0_session::admin() {
  local IFS=' '
  printf 'get\t%s\n' "$*" >> "$cleanup_calls"
}
phase0_session::principal() {
  local kubeconfig=$1
  shift
  local IFS=' '
  printf 'principal\t%s\t%s\n' "$kubeconfig" "$*" >> "$cleanup_calls"
  printf 'no\n'
  return 1
}
phase0_session::journal_append() {
  printf 'journal\t%s\t%s\n' "$1" "$2" >> "$cleanup_calls"
}
ATLAS_PHASE0_TARGET[admin_kubeconfig]=admin.kubeconfig
ATLAS_PHASE0_TARGET[cluster_name]=atlas-recovery-drill-20260817t000000z-0123abcd
ATLAS_PHASE0_OPERATION[authorizer_kubeconfig]=authorizer.kubeconfig
phase0_ceremony::_cleanup_cluster_resources
[[ $(grep -c '^delete' "$cleanup_calls") == 17 ]] || test::fail "cleanup did not delete every installed canary object"
first_delete=$(grep '^delete' "$cleanup_calls" | sed -n '1p')
[[ $first_delete == *'/rolebindings/atlas-bootstrap-recovery-authorizer-canary'* ]] ||
  test::fail "cleanup did not revoke the Session Authorizer first"
authorizer_delete_line=$(grep -n '/rolebindings/atlas-bootstrap-recovery-authorizer-canary' "$cleanup_calls" | sed -n '1s/:.*//p')
authorizer_probe_line=$(grep -n $'^principal\tauthorizer.kubeconfig\t' "$cleanup_calls" | sed -n '1s/:.*//p')
second_delete_line=$(grep -n '^delete' "$cleanup_calls" | sed -n '2s/:.*//p')
((authorizer_delete_line < authorizer_probe_line && authorizer_probe_line < second_delete_line)) ||
  test::fail "cleanup touched protected controls before verifying Authorizer revocation"
grep -Fq $'get\tget role atlas-bootstrap-recovery-canary --ignore-not-found -o name -n kube-system' "$cleanup_calls" ||
  test::fail "cleanup verification escaped the namespaced Role boundary"
grep -Fq '/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-recovery-fence-authorization-canary' "$cleanup_calls" ||
  test::fail "cleanup omitted the Fence Policy"
awk '{print $2 "/" $3}' "$absence_calls" | grep -Fv 'configmap/atlas-bootstrap-operation-fence-canary' | sort > "${test_workspace}/absence-inventory"
awk -F $'\t' '$1 != "delete" && $1 != "get" && $1 != "journal" && $1 != "principal" {print $1 "/" $2}' "$cleanup_calls" | sort > "${test_workspace}/cleanup-inventory"
cmp -s "${test_workspace}/absence-inventory" "${test_workspace}/cleanup-inventory" ||
  test::fail "preflight and cleanup definition inventories differ"

cleanup_failure_calls="${test_workspace}/cleanup-failure-calls"
(
  : > "$cleanup_failure_calls"
  phase0_ceremony::_snapshot_for_delete() {
    printf 'snapshot\t%s\n' "$2" >> "$cleanup_failure_calls"
    printf '%s\n' "$test_snapshot"
  }
  phase0_ceremony::_delete_with_preconditions() {
    printf 'delete\t%s\n' "$2" >> "$cleanup_failure_calls"
  }
  phase0_session::admin() { :; }
  phase0_session::principal() {
    printf 'revocation-probe\n' >> "$cleanup_failure_calls"
    printf 'yes\n'
  }
  phase0_session::journal_append() { :; }
  if phase0_ceremony::_cleanup_cluster_resources > /dev/null 2>&1; then
    test::fail "cleanup accepted an Authorizer grant after RoleBinding deletion"
  fi
)
[[ $(grep -c '^delete' "$cleanup_failure_calls") == 1 &&
$(grep -c '^snapshot' "$cleanup_failure_calls") == 1 ]] ||
  test::fail "cleanup continued into protected controls after revocation verification failed"

probe_expected_denial() {
  printf 'expected policy denial\n' >&2
  return 1
}
probe_unexpected_failure() {
  printf 'network unavailable\n' >&2
  return 1
}
phase0_ceremony::_expect_rejected diagnostic-contract 'expected policy denial' probe_expected_denial
if phase0_ceremony::_expect_rejected wrong-diagnostic 'expected policy denial' probe_unexpected_failure > /dev/null 2>&1; then
  test::fail "negative probe accepted an unrelated failure"
fi

install_order="${test_workspace}/install-order"
run_install_stage() (
  local failure_stage=$1 file scope
  : > "$install_order"
  phase0_session::journal_append() { printf 'journal\t%s\t%s\n' "$1" "$2" >> "$install_order"; }
  phase0_session::admin() {
    file=${4:-}
    printf 'create\t%s\n' "$file" >> "$install_order"
    [[ $failure_stage != admission || $file != "$approved_admission" ]] || return 41
    [[ $failure_stage != static || $file != "$approved_static" ]] || return 42
    [[ $failure_stage != activation || $file != "$approved_activation" ]] || return 43
  }
  phase0_ceremony::_wait_policy_typecheck() {
    printf 'typecheck\t%s\n' "$1" >> "$install_order"
    [[ $failure_stage != typecheck ]] || return 44
    printf '{"spec":{}}\n' > "$2"
  }
  phase0_ceremony::_verify_live_definitions() {
    scope=$1
    printf 'projection\t%s\n' "$scope" >> "$install_order"
    [[ $failure_stage != static-projection || $scope != static ]] || return 45
    [[ $failure_stage != full-projection || $scope != full ]] || return 46
  }
  phase0_session::principal() {
    printf 'principal\t%s\n' "$1" >> "$install_order"
    printf 'yes\n'
  }
  phase0_ceremony::_install_definitions
)
run_install_stage none
static_projection_line=$(grep -n $'^projection\tstatic$' "$install_order" | cut -d: -f1)
activation_line=$(grep -n $'^create\t'"$approved_activation"'$' "$install_order" | cut -d: -f1)
full_projection_line=$(grep -n $'^projection\tfull$' "$install_order" | cut -d: -f1)
((static_projection_line < activation_line && activation_line < full_projection_line)) ||
  test::fail "Session Authorizer was not activated after static projection verification"
[[ $(grep -c '^typecheck' "$install_order") == 5 ]] ||
  test::fail "definition installation did not type-check all five Policies"
for failure_stage in admission static typecheck static-projection; do
  if run_install_stage "$failure_stage" > /dev/null 2>&1; then
    test::fail "injected definition failure returned success: ${failure_stage}"
  fi
  if grep -Fqx $'create\t'"$approved_activation" "$install_order"; then
    test::fail "Authorizer activated after pre-activation failure: ${failure_stage}"
  fi
done
if run_install_stage activation > /dev/null 2>&1; then
  test::fail "injected Authorizer activation failure returned success"
fi
if run_install_stage full-projection > /dev/null 2>&1; then
  test::fail "injected activated-projection failure returned success"
fi

permission_calls="${test_workspace}/permission-calls"
ATLAS_PHASE0_OPERATION[recovery_kubeconfig]='recovery-g2.kubeconfig'
ATLAS_PHASE0_OPERATION[previous_recovery_kubeconfig]='recovery-g1.kubeconfig'
ATLAS_PHASE0_OPERATION[authorizer_kubeconfig]='authorizer-g2.kubeconfig'
ATLAS_PHASE0_OPERATION[previous_authorizer_kubeconfig]='authorizer-g1.kubeconfig'
printf 'baseline\n' > "${session}/authorization/previous_recovery-permissions-before.txt"
printf 'baseline\n' > "${session}/authorization/previous_authorizer-permissions-before.txt"
verify_active_permissions() (
  local allow_old=$1 kubeconfig command resource
  : > "$permission_calls"
  phase0_session::journal_append() { :; }
  phase0_ceremony::_permission_inventory() {
    printf 'baseline\n' > "$2"
    chmod 0400 "$2"
  }
  phase0_session::principal() {
    kubeconfig=$1
    command=$4
    resource=${5:-}
    printf '%s\t%s\t%s\n' "$kubeconfig" "$command" "$resource" >> "$permission_calls"
    if [[ $kubeconfig == recovery-g2.kubeconfig && $command == patch ]]; then
      printf 'yes\n'
      return
    fi
    if [[ $kubeconfig == authorizer-g2.kubeconfig && $command == create ]]; then
      printf 'yes\n'
      return
    fi
    if [[ $allow_old == true && $kubeconfig == *-g1.kubeconfig ]]; then
      printf 'yes\n'
      return
    fi
    printf 'no\n'
    return 1
  }
  phase0_ceremony::_verify_active_permissions
)
verify_active_permissions false
[[ $(grep -c -- '-g1.kubeconfig' "$permission_calls") == 14 ]] ||
  test::fail "old-generation denial did not cover the complete mutation matrix"
if verify_active_permissions true > /dev/null 2>&1; then
  test::fail "active permission verification accepted an old-generation mutation grant"
fi

path_revalidation_calls="${test_workspace}/path-revalidation-calls"
(
  : > "$path_revalidation_calls"
  phase0_session::revalidate_target_paths() {
    printf 'paths\n' >> "$path_revalidation_calls"
    return 23
  }
  phase0_session::_git_authority() { printf 'git\n' >> "$path_revalidation_calls"; }
  # shellcheck disable=SC2329 # A call is a test failure; absence is the contract.
  phase0_session::_kubectl() { printf 'kubectl\n' >> "$path_revalidation_calls"; }
  # shellcheck disable=SC2329 # A call is a test failure; absence is the contract.
  openssl() { printf 'openssl\n' >> "$path_revalidation_calls"; }
  if phase0_session::revalidate > /dev/null 2>&1; then
    test::fail "path revalidation failure returned success"
  fi
)
[[ $(< "$path_revalidation_calls") == paths ]] ||
  test::fail "path drift reached Git, OpenSSL, or kubectl after the Human Gate"

order="${test_workspace}/order"
: > "$order"
phase0_ceremony::_capture_audit_boundary() { printf 'audit-boundary\n' >> "$order"; }
phase0_ceremony::_issue_credentials() { printf 'credentials\n' >> "$order"; }
phase0_ceremony::_install_definitions() { printf 'definitions\n' >> "$order"; }
phase0_ceremony::_verify_active_permissions() { printf 'permissions-active\n' >> "$order"; }
phase0_ceremony::_admission_escape_drill() { printf 'escape\n' >> "$order"; }
phase0_ceremony::_session_authorization_drill() { printf 'controls\n' >> "$order"; }
phase0_ceremony::_cleanup_cluster_resources() { printf 'resources-clean\n' >> "$order"; }
phase0_ceremony::_cleanup_credentials() { printf 'credentials-clean\n' >> "$order"; }
phase0_ceremony::_finalize_evidence() { printf 'evidence\n' >> "$order"; }
phase0_ceremony::_run > /dev/null
[[ $(< "$order") == $'audit-boundary\ncredentials\ndefinitions\npermissions-active\nescape\ncontrols\nresources-clean\ncredentials-clean\nevidence' ]] ||
  test::fail "runtime ceremony order changed"

: > "$order"
phase0_session::resolve_target() { printf 'resolve\n' >> "$order"; }
phase0_session::_tool_preflight() { printf 'preflight\n' >> "$order"; }
phase0_session::acquire_lock() { printf 'lock\n' >> "$order"; }
phase0_session::prepare() { printf 'prepare\n' >> "$order"; }
phase0_session::human_gate() { printf 'gate\n' >> "$order"; }
phase0_session::revalidate() {
  printf 'revalidate-denied\n' >> "$order"
  return 23
}
phase0_session::journal_append() { printf 'journal-%s-%s\n' "$1" "$2" >> "$order"; }
phase0_session::release_lock() { printf 'unlock\n' >> "$order"; }
phase0_ceremony::_run() { test::fail "mutation ran after revalidation failed"; }
if phase0_ceremony::run a b c d e f g h i; then
  test::fail "revalidation failure returned success"
else
  [[ $? == 23 ]] || test::fail "revalidation failure exit code changed"
fi
[[ $(< "$order") == $'resolve\npreflight\nlock\nprepare\ngate\nrevalidate-denied\njournal-PREMUTATION-DENIED\nunlock' ]] ||
  test::fail "pre-mutation denial did not release the lifecycle lock"

for required_probe in shape-malformed-binding permission-missing-fence guard-data-replacement \
  'CREDENTIALS REVOKED' typeChecking; do
  grep -Fq "$required_probe" bootstrap/recovery/canary-ceremony.sh ||
    test::fail "runtime ceremony omitted required probe: ${required_probe}"
done
test::assert_not_found 'canary-session|phase0_ceremony|canary-drill' bootstrap/atlas gitops
test::assert_not_found '(kind create|kind delete|docker |receipt|atlas-bootstrap-adoption)' \
  bootstrap/recovery/canary-session.sh bootstrap/recovery/canary-ceremony.sh

[[ $(awk -F= '$1 == "OPENSSL_VERSION" {print $2}' versions.lock) == 3.6.3 ]] ||
  test::fail "OpenSSL runtime authority is not locked"
[[ $(awk -F= '$1 == "YQ_VERSION" {print $2}' versions.lock) == 4.53.3 ]] ||
  test::fail "yq runtime authority is not locked"

test::pass "Phase-0 canary runtime ceremony contract"
