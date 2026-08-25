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

# shellcheck source=bootstrap/recovery/principal-identity.sh
source bootstrap/recovery/principal-identity.sh
# shellcheck source=bootstrap/recovery/admission-canary/render.sh
source bootstrap/recovery/admission-canary/render.sh
# shellcheck source=bootstrap/recovery/session-authorization-canary/render.sh
source bootstrap/recovery/session-authorization-canary/render.sh
# shellcheck source=bootstrap/recovery/canary-session.sh
source bootstrap/recovery/canary-session.sh
# shellcheck source=bootstrap/recovery/canary-ceremony.sh
source bootstrap/recovery/canary-ceremony.sh

recovery_cli=./bootstrap/recovery/atlas-recovery

typecheck_destination="${test_workspace}/typecheck-live.json"
typecheck_destination_mode=''
(
  phase0_session::admin() {
    printf '%s\n' '{"metadata":{"generation":1},"status":{"observedGeneration":1,"typeChecking":{"expressionWarnings":[]}}}'
  }
  phase0_ceremony::_wait_policy_typecheck atlas-bootstrap-test-policy "$typecheck_destination"
)
if [[ $(uname -s) == Darwin ]]; then
  typecheck_destination_mode=$(stat -f '%Lp' "$typecheck_destination")
else
  typecheck_destination_mode=$(stat -c '%a' "$typecheck_destination")
fi
[[ -f $typecheck_destination && ! -L $typecheck_destination &&
  $typecheck_destination_mode == 400 &&
  $(yq -r '.status.typeChecking.expressionWarnings | length' "$typecheck_destination") == 0 ]] ||
  test::fail "VAP type-check evidence was not written to the requested destination"

unexpected_exit_record="${test_workspace}/unexpected-exit-record"
unexpected_exit_journal="${test_workspace}/unexpected-exit-journal.jsonl"
unexpected_exit_stderr="${test_workspace}/unexpected-exit-stderr"
: > "$unexpected_exit_journal"
if (
  # Both functions are invoked indirectly by the installed EXIT trap.
  # shellcheck disable=SC2329
  phase0_session::journal_append() {
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" > "$unexpected_exit_record"
  }
  # shellcheck disable=SC2329
  phase0_session::release_lock() {
    printf 'released\n' >> "$unexpected_exit_record"
  }
  ATLAS_PHASE0_OPERATION[journal_file]=$unexpected_exit_journal
  phase0_session::arm_unexpected_exit_guard
  phase0_session::install_unexpected_exit_trap
  printf '%s\n' "$ATLAS_PHASE0_CONTRACT_UNBOUND"
) 2> "$unexpected_exit_stderr"; then
  test::fail "unexpected nounset exit returned success"
fi
[[ $(< "$unexpected_exit_record") == $'RESULT\tFAILED_RETAINED\tunexpected shell exit status=1; runtime state and lock retained for human review' ]] ||
  test::fail "unexpected shell exit did not append the retained-state terminal record"
grep -Fq 'ATLAS_PHASE0_CONTRACT_UNBOUND: unbound variable' "$unexpected_exit_stderr" ||
  test::fail "unexpected-exit contract did not exercise Bash nounset"

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
ATLAS_PHASE0_OPERATION[authorizer_principal]=atlas:session-authz:00000000-0000-0000-0000-000000000000:g2
ATLAS_PHASE0_OPERATION[recovery_principal]=atlas:break-glass:00000000-0000-0000-0000-000000000000:g3
ATLAS_PHASE0_OPERATION[previous_authorizer_principal]=atlas:session-authz:00000000-0000-0000-0000-000000000000:g1
ATLAS_PHASE0_OPERATION[previous_recovery_principal]=atlas:break-glass:00000000-0000-0000-0000-000000000000:g2
ATLAS_PHASE0_OPERATION[prepared_at]=2026-08-17T00:00:00Z
ATLAS_PHASE0_OPERATION[plan_sha]=$(printf 'b%.0s' {1..64})
ATLAS_PHASE0_OPERATION[action_id]=phase0-test
ATLAS_PHASE0_OPERATION[actor]=501:test
ATLAS_PHASE0_OPERATION[api_server]=https://127.0.0.1:6443
ATLAS_PHASE0_OPERATION[namespace_uid]=00000000-0000-0000-0000-000000000000
ATLAS_PHASE0_OPERATION[repository_url]=https://github.com/snkio027/atlas.git
ATLAS_PHASE0_OPERATION[environment_name]=local-orbstack
ATLAS_PHASE0_OPERATION[git_commit]=$(printf 'c%.0s' {1..40})
ATLAS_PHASE0_OPERATION[git_tree]=$(printf 'd%.0s' {1..40})
ATLAS_PHASE0_OPERATION[creation_plan_sha]=$(printf 'e%.0s' {1..64})
ATLAS_PHASE0_OPERATION[creation_journal_tip]=$(printf 'f%.0s' {1..64})
ATLAS_PHASE0_OPERATION[admission_bundle_sha]=$(printf '1%.0s' {1..64})
ATLAS_PHASE0_OPERATION[session_bundle_sha]=$(printf '2%.0s' {1..64})
ATLAS_PHASE0_OPERATION[versions_sha]=$(printf '3%.0s' {1..64})
ATLAS_PHASE0_OPERATION[audit_policy_sha]=$(printf '4%.0s' {1..64})
ATLAS_PHASE0_OPERATION[admin_kubeconfig_sha]=$(printf '5%.0s' {1..64})
ATLAS_PHASE0_OPERATION[ca_data]=dGVzdA==
ATLAS_PHASE0_OPERATION[ca_spki_sha]=$(printf '6%.0s' {1..64})
ATLAS_PHASE0_TARGET[known_good_revision]=$(printf 'c%.0s' {1..40})
ATLAS_PHASE0_TARGET[cluster_name]=atlas-recovery-drill-20260817t000000z-0123abcd
ATLAS_PHASE0_TARGET[context]='kind-atlas-recovery-drill-20260817t000000z-0123abcd'
ATLAS_PHASE0_TARGET[admin_kubeconfig]=/encrypted/admin.kubeconfig
ATLAS_PHASE0_TARGET[audit_directory]=/encrypted/audit
ATLAS_PHASE0_TARGET[creation_evidence]=/encrypted/creation/session
ATLAS_PHASE0_TARGET[credential_directory]=/encrypted/credentials
ATLAS_PHASE0_TARGET[storage_assertion]=encrypted-owner-controlled
ATLAS_PHASE0_TARGET[KUBERNETES_VERSION]=1.36.1
ATLAS_PHASE0_TARGET[environment_name]=local-orbstack
ATLAS_PHASE0_TARGET[recovery_generation]=3
ATLAS_PHASE0_TARGET[previous_recovery_generation]=2
ATLAS_PHASE0_TARGET[authorizer_generation]=2
ATLAS_PHASE0_TARGET[previous_authorizer_generation]=1

missing_fence_log="${session}/authorization/rejected-permission-missing-fence.log"
missing_fence_journal="${test_workspace}/missing-fence-journal"
(
  phase0_session::journal_append() {
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$missing_fence_journal"
  }
  # Invoked indirectly by _expect_rejected.
  # shellcheck disable=SC2329
  missing_fence_denial() {
    printf '%s\n' \
      "failed to configure binding: no params found for policy binding with \`Deny\` parameterNotFoundAction" >&2
    return 1
  }
  phase0_ceremony::_expect_rejected permission-missing-fence \
    "no params found for policy binding with \`Deny\` parameterNotFoundAction" \
    missing_fence_denial
)
grep -Fq "no params found for policy binding with \`Deny\` parameterNotFoundAction" \
  "$missing_fence_log" ||
  test::fail "current Kubernetes missing-Fence denial was not accepted"
grep -Fqx $'PROBE\tREJECTED\tpermission-missing-fence' "$missing_fence_journal" ||
  test::fail "accepted missing-Fence denial was not journaled"
if (
  phase0_session::journal_append() { :; }
  # Invoked indirectly by _expect_rejected.
  # shellcheck disable=SC2329
  unrelated_denial() {
    printf '%s\n' 'forbidden by an unrelated authorization layer' >&2
    return 1
  }
  phase0_ceremony::_expect_rejected permission-missing-fence-unrelated \
    "no params found for policy binding with \`Deny\` parameterNotFoundAction" \
    unrelated_denial
) > /dev/null 2>&1; then
  test::fail "missing-Fence classifier accepted an unrelated denial"
fi

invalid_credential_calls="${test_workspace}/invalid-credential-calls"
(
  : > "$invalid_credential_calls"
  openssl() { printf 'openssl\n' >> "$invalid_credential_calls"; }
  phase0_session::admin() { printf 'kubectl\n' >> "$invalid_credential_calls"; }
  if phase0_ceremony::_issue_principal authorizer \
    atlas:session-authz:00000000-0000-0000-0000-000000000000:g1000000 \
    invalid-csr "${test_workspace}/invalid-credentials" /invalid/ca.crt > /dev/null 2>&1; then
    test::fail "credential issuance accepted a 65-byte Session Authorizer identity"
  fi
)
[[ ! -s $invalid_credential_calls ]] ||
  test::fail "invalid principal reached credential-producing OpenSSL or kubectl"

test_ca_key="${test_workspace}/test-ca.key"
test_ca_certificate="${test_workspace}/test-ca.crt"
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 \
  -out "$test_ca_key" > /dev/null 2>&1
openssl req -x509 -sha256 -key "$test_ca_key" -subj /CN=atlas-phase0-test-ca \
  -out "$test_ca_certificate"
test_ca_data=$(base64 < "$test_ca_certificate" | tr -d '\n')
expected_ca_spki=$(openssl x509 -in "$test_ca_certificate" -pubkey -noout |
  openssl pkey -pubin -outform DER |
  shasum -a 256 | awk '{print $1}')
[[ $(phase0_session::_ca_spki_sha256 "$test_ca_data") == "$expected_ca_spki" ]] ||
  test::fail "API server CA SPKI binding drifted"

verify_admin_target() (
  local mock_username=$1 mock_groups=$2 arguments
  phase0_session::_ca_spki_sha256() {
    printf '6%.0s' {1..64}
  }
  phase0_session::admin() {
    local IFS=' '
    arguments=$*
    case "$arguments" in
      'config current-context') printf '%s\n' "${ATLAS_PHASE0_TARGET[context]}" ;;
      'auth whoami -o json')
        printf '{"status":{"userInfo":{"username":"%s","groups":%s}}}\n' "$mock_username" "$mock_groups"
        ;;
      'config view --raw --minify -o json')
        printf '%s\n' '{"clusters":[{"cluster":{"server":"https://127.0.0.1:6443","certificate-authority-data":"dGVzdA=="}}]}'
        ;;
      'get namespace kube-system -o jsonpath={.metadata.uid}')
        printf '%s\n' 00000000-0000-0000-0000-000000000000
        ;;
      'get nodes -o json')
        printf '{"items":[{"metadata":{"name":"%s-control-plane"}}]}\n' "${ATLAS_PHASE0_TARGET[cluster_name]}"
        ;;
      "get node ${ATLAS_PHASE0_TARGET[cluster_name]}-control-plane -o json")
        printf '%s\n' '{"status":{"conditions":[{"type":"Ready","status":"True"}]}}'
        ;;
      'version -o json') printf '%s\n' '{"serverVersion":{"gitVersion":"v1.36.1"}}' ;;
      *) return 1 ;;
    esac
  }
  phase0_session::_admin_target
)

verify_admin_target kubernetes-admin \
  '["system:authenticated","kubeadm:cluster-admins"]'
for rejected_identity in \
  'kubernetes-admin|["system:authenticated","system:masters"]' \
  'kubernetes-admin|["kubeadm:cluster-admins","system:authenticated","unexpected-group"]' \
  'kubernetes-super-admin|["kubeadm:cluster-admins","system:authenticated"]'; do
  if verify_admin_target "${rejected_identity%%|*}" "${rejected_identity#*|}" > /dev/null 2>&1; then
    test::fail "admin target accepted a non-canonical Kind authority: ${rejected_identity}"
  fi
done

(
  principal_plan=''
  principal_identity::session_authorizer() {
    if [[ $2 == 1 ]]; then
      printf 'invalid-previous-authorizer\n'
    else
      printf 'atlas:session-authz:%s:g%s\n' "$1" "$2"
    fi
  }
  if principal_plan=$(principal_identity::plan \
    00000000-0000-0000-0000-000000000000 3 2 2 1 2> /dev/null); then
    test::fail "principal plan accepted an invalid previous generation"
  fi
  [[ -z $principal_plan ]] ||
    test::fail "partial principal plan escaped validation"
)

verify_live_target_revalidation() {
  local scenario=$1 expected=$2 namespace_uid=00000000-0000-0000-0000-000000000000
  local principal_plan recovery_principal authorizer_principal
  local previous_recovery_principal previous_authorizer_principal
  local -A ATLAS_PHASE0_OPERATION=()
  ATLAS_PHASE0_OPERATION[namespace_uid]=$namespace_uid
  ATLAS_PHASE0_OPERATION[api_server]=https://127.0.0.1:6443
  ATLAS_PHASE0_OPERATION[ca_data]=approved-ca
  ATLAS_PHASE0_OPERATION[ca_spki_sha]=$(printf '6%.0s' {1..64})
  ATLAS_PHASE0_OPERATION[repository_url]=https://github.com/snkio027/atlas.git
  ATLAS_PHASE0_OPERATION[environment_name]=local-orbstack
  principal_plan=$(principal_identity::plan "$namespace_uid" 3 2 2 1)
  IFS=$'\t' read -r recovery_principal authorizer_principal \
    previous_recovery_principal previous_authorizer_principal <<< "$principal_plan"
  ATLAS_PHASE0_OPERATION[recovery_principal]=$recovery_principal
  ATLAS_PHASE0_OPERATION[authorizer_principal]=$authorizer_principal
  ATLAS_PHASE0_OPERATION[previous_recovery_principal]=$previous_recovery_principal
  ATLAS_PHASE0_OPERATION[previous_authorizer_principal]=$previous_authorizer_principal
  ATLAS_PHASE0_OPERATION[target_fingerprint]=$(phase0_session::_target_fingerprint)
  [[ $scenario != principal ]] ||
    ATLAS_PHASE0_OPERATION[previous_authorizer_principal]="atlas:session-authz:${namespace_uid}:g3"
  phase0_session::_admin_target() {
    case "$scenario" in
      unavailable) return 1 ;;
      endpoint) ATLAS_PHASE0_OPERATION[api_server]=https://127.0.0.1:7443 ;;
      ca)
        ATLAS_PHASE0_OPERATION[ca_data]=drifted-ca
        ATLAS_PHASE0_OPERATION[ca_spki_sha]=$(printf '7%.0s' {1..64})
        ;;
      uid) ATLAS_PHASE0_OPERATION[namespace_uid]=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa ;;
      exact | principal) ;;
      *) return 1 ;;
    esac
  }
  if phase0_session::_revalidate_live_target > /dev/null 2>&1; then
    [[ $expected == accept ]] || test::fail "live target drift was accepted: ${scenario}"
  else
    [[ $expected == reject ]] || test::fail "exact live target was rejected"
  fi
}

verify_live_target_revalidation exact accept
for live_drift in endpoint ca uid unavailable principal; do
  verify_live_target_revalidation "$live_drift" reject
done

expected_target_fingerprint=$(printf 'apiServerURL=%s\nkubeSystemNamespaceUID=%s\napiServerCASPKISHA256=%s\nrepositoryURL=%s\nenvironmentName=%s\n' \
  "${ATLAS_PHASE0_OPERATION[api_server]}" "${ATLAS_PHASE0_OPERATION[namespace_uid]}" \
  "${ATLAS_PHASE0_OPERATION[ca_spki_sha]}" "${ATLAS_PHASE0_OPERATION[repository_url]}" \
  "${ATLAS_PHASE0_OPERATION[environment_name]}" | shasum -a 256 | awk '{print $1}')
[[ $(phase0_session::_target_fingerprint) == "$expected_target_fingerprint" ]] ||
  test::fail "target fingerprint is not the named ADR-0003 authority tuple"

verify_git_authority_revalidation() {
  local scenario=$1 expected=$2
  local -A ATLAS_PHASE0_OPERATION=()
  ATLAS_PHASE0_OPERATION[git_commit]=$(printf 'c%.0s' {1..40})
  ATLAS_PHASE0_OPERATION[git_tree]=$(printf 'd%.0s' {1..40})
  ATLAS_PHASE0_OPERATION[repository_url]=https://github.com/snkio027/atlas.git
  ATLAS_PHASE0_OPERATION[environment_name]=local-orbstack
  phase0_session::_git_authority() {
    case "$scenario" in
      exact)
        printf '%s\t%s\thttps://github.com/snkio027/atlas.git\tlocal-orbstack\n' \
          "${ATLAS_PHASE0_OPERATION[git_commit]}" "${ATLAS_PHASE0_OPERATION[git_tree]}"
        ;;
      repository)
        printf '%s\t%s\thttps://github.com/foreign/atlas.git\tlocal-orbstack\n' \
          "${ATLAS_PHASE0_OPERATION[git_commit]}" "${ATLAS_PHASE0_OPERATION[git_tree]}"
        ;;
      environment)
        printf '%s\t%s\thttps://github.com/snkio027/atlas.git\ttest\n' \
          "${ATLAS_PHASE0_OPERATION[git_commit]}" "${ATLAS_PHASE0_OPERATION[git_tree]}"
        ;;
      unavailable) return 1 ;;
      *) return 1 ;;
    esac
  }
  if phase0_session::_revalidate_git_authority > /dev/null 2>&1; then
    [[ $expected == accept ]] || test::fail "Git authority drift was accepted: ${scenario}"
  else
    [[ $expected == reject ]] || test::fail "exact Git authority was rejected"
  fi
}

verify_git_authority_revalidation exact accept
for git_drift in repository environment unavailable; do
  verify_git_authority_revalidation "$git_drift" reject
done

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
$(yq -r '.apiServerCASPKISHA256' "$plan") == "${ATLAS_PHASE0_OPERATION[ca_spki_sha]}" &&
$(yq -r '.repositoryURL' "$plan") == https://github.com/snkio027/atlas.git &&
$(yq -r '.environmentName' "$plan") == local-orbstack &&
$(yq -r '.recoveryGeneration' "$plan") == 3 &&
$(yq -r '.previousRecoveryGeneration' "$plan") == 2 &&
$(yq -r '.authorizerGeneration' "$plan") == 2 &&
$(yq -r '.previousAuthorizerGeneration' "$plan") == 1 &&
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

binding_projection_a="${session}/authorization/admission-binding-projection-a.json"
binding_projection_b="${session}/authorization/admission-binding-projection-b.json"
RESOURCE_NAME=atlas-bootstrap-admission-escape-canary yq ea -o=json -I=0 '
  select(.kind == "ValidatingAdmissionPolicyBinding" and .metadata.name == env(RESOURCE_NAME))
' "$approved_admission" > "$binding_projection_a"
yq -o=json -I=0 '
  .metadata.uid = "00000000-0000-0000-0000-000000000001" |
  .metadata.resourceVersion = "100" |
  .spec.validationActions = ["Deny", "Audit"]
' "$binding_projection_a" > "$binding_projection_b"
[[ $(phase0_ceremony::_normalized_definition_sha256 "$binding_projection_a") == "$(phase0_ceremony::_normalized_definition_sha256 "$binding_projection_b")" ]] ||
  test::fail "normalized definition hash does not preserve server metadata and set semantics"
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
    case "$phase" in
      admission) bundle=$approved_admission ;;
      static) bundle=$approved_static ;;
      activation) bundle=$approved_activation ;;
      *) return 1 ;;
    esac
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

# The scenario owns local authority maps inside an intentional subshell; the
# surrounding test authority must remain unchanged.
# shellcheck disable=SC2030,SC2031
exercise_admission_escape_mock() (
  local scenario=$1 scenario_root policy_live binding_live mock_binding_state=enforced
  local patch_calls=0 fixture_calls=0 propagation_pending='' resource destination actions dry_run
  local -A ATLAS_PHASE0_OPERATION=()
  local -A ATLAS_PHASE0_TARGET=()
  scenario_root="${test_workspace}/admission-escape-${scenario}"
  mkdir -m 0700 "$scenario_root" "$scenario_root/authorization"
  policy_live="${scenario_root}/policy-live.json"
  binding_live="${scenario_root}/binding-live.json"
  KIND=ValidatingAdmissionPolicy NAME=atlas-bootstrap-admission-escape-canary yq ea -o=json -I=0 '
    select(.kind == env(KIND) and .metadata.name == env(NAME)) |
    .metadata.uid = "00000000-0000-0000-0000-000000000101" |
    .metadata.resourceVersion = "101" |
    .metadata.generation = 1 |
    .status = {"observedGeneration":1,"typeChecking":{"expressionWarnings":[]}}
  ' "$approved_admission" > "$policy_live"
  KIND=ValidatingAdmissionPolicyBinding NAME=atlas-bootstrap-admission-escape-canary yq ea -o=json -I=0 '
    select(.kind == env(KIND) and .metadata.name == env(NAME)) |
    .metadata.uid = "00000000-0000-0000-0000-000000000102" |
    .metadata.resourceVersion = "201"
  ' "$approved_admission" > "$binding_live"
  if [[ $scenario == drift ]]; then
    yq -i '.metadata.labels."atlas.io/test-drift" = "present"' "$binding_live"
  fi

  ATLAS_PHASE0_OPERATION[evidence_session]=$scenario_root
  ATLAS_PHASE0_OPERATION[admission_bundle]=$approved_admission
  ATLAS_PHASE0_OPERATION[admission_bundle_sha]=$(phase0_session::_sha256 "$approved_admission")
  ATLAS_PHASE0_OPERATION[recovery_kubeconfig]='recovery-current-g3.kubeconfig'
  ATLAS_PHASE0_TARGET[admin_kubeconfig]=admin.kubeconfig
  phase0_session::journal_append() { :; }
  phase0_ceremony::_record_object() {
    resource=$2
    destination=$4
    case "$resource" in
      validatingadmissionpolicy) cp "$policy_live" "$destination" ;;
      validatingadmissionpolicybinding)
        case "$mock_binding_state" in
          enforced) cp "$binding_live" "$destination" ;;
          suspended)
            yq -o=json -I=0 \
              '.metadata.resourceVersion = "202" | .spec.validationActions = ["Audit"]' \
              "$binding_live" > "$destination"
            ;;
          *) return 1 ;;
        esac
        ;;
      *) return 1 ;;
    esac
    chmod 0400 "$destination"
  }
  phase0_ceremony::_binding_patch() {
    actions=$3
    patch_calls=$((patch_calls + 1))
    case "$actions" in
      '["Audit"]')
        [[ $mock_binding_state == enforced ]] || return 1
        mock_binding_state=suspended
        propagation_pending=suspend
        ;;
      '["Audit","Deny"]')
        [[ $mock_binding_state == suspended ]] || return 1
        mock_binding_state=enforced
        propagation_pending=restore
        ;;
      *) return 1 ;;
    esac
  }
  phase0_ceremony::_patch_fixture() {
    dry_run=${4:-false}
    fixture_calls=$((fixture_calls + 1))
    if [[ $dry_run == true && $propagation_pending == suspend ]]; then
      propagation_pending=''
      printf 'Atlas admission escape canary mutation requires the exact Recovery Operator\n' >&2
      return 1
    fi
    if [[ $dry_run == true && $propagation_pending == restore ]]; then
      propagation_pending=''
      return 0
    fi
    if [[ $mock_binding_state == enforced ]]; then
      printf 'Atlas admission escape canary mutation requires the exact Recovery Operator\n' >&2
      return 1
    fi
  }
  sleep() { :; }
  phase0_ceremony::_wait_policy_typecheck() {
    cp "$policy_live" "$2"
    chmod 0400 "$2"
  }

  if [[ $scenario == drift ]]; then
    if phase0_ceremony::_admission_escape_drill > /dev/null 2>&1; then
      test::fail "admission escape accepted a pre-suspend Binding drift"
    fi
    [[ $patch_calls == 0 && $fixture_calls == 0 && $mock_binding_state == enforced ]] ||
      test::fail "pre-suspend Binding drift reached a fixture or Binding mutation"
    return 0
  fi

  phase0_ceremony::_admission_escape_drill
  [[ $patch_calls == 2 && $fixture_calls == 7 && $mock_binding_state == enforced &&
    -z $propagation_pending ]] ||
    test::fail "admission escape did not wait for exact suspend and restore propagation"
  [[ $(yq -r '.metadata.uid' "$scenario_root/authorization/admission-policy-restored.json") == 00000000-0000-0000-0000-000000000101 &&
  $(yq -r '.metadata.uid' "$scenario_root/authorization/admission-binding-restored.json") == 00000000-0000-0000-0000-000000000102 &&
  $(yq -r '.status.typeChecking.expressionWarnings | length' \
    "$scenario_root/authorization/admission-policy-restored-typecheck.json") == 0 ]] ||
    test::fail "admission restore evidence omitted UID or type-check continuity"
)

exercise_admission_escape_mock exact
exercise_admission_escape_mock drift

audit_delta="${session}/audit/current-session-test.jsonl"
# The local authority maps in the isolated escape scenarios cannot modify the
# surrounding session map used by this audit fixture.
# shellcheck disable=SC2031
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

permission_namespaces="${session}/authorization/permission-namespaces-test.txt"
complete_inventory="${session}/authorization/complete-permissions.txt"
incomplete_inventory="${session}/authorization/incomplete-permissions.txt"
printf '%s\n' kube-system > "$permission_namespaces"
(
  ATLAS_PHASE0_OPERATION[permission_namespaces]=$permission_namespaces
  phase0_session::principal() {
    printf '%s\n' 'deployments.apps   []   []   [get list watch]'
  }
  phase0_ceremony::_permission_inventory principal.kubeconfig "$complete_inventory"
)
[[ $(< "$complete_inventory") == $'kube-system\tdeployments.apps   []   []   [get list watch]' &&
! -e ${complete_inventory}.tmp && ! -e ${complete_inventory}.stderr.tmp ]] ||
  test::fail "complete permission inventory was not captured atomically"
if (
  ATLAS_PHASE0_OPERATION[permission_namespaces]=$permission_namespaces
  phase0_session::principal() {
    printf '%s\n' 'deployments.apps   []   []   [get list watch]'
    printf '%s\n' 'Warning: the list may be incomplete: simulated evaluator gap' >&2
  }
  phase0_ceremony::_permission_inventory principal.kubeconfig "$incomplete_inventory"
) > /dev/null 2>&1; then
  test::fail "permission inventory accepted safe stdout with an incomplete warning"
fi
[[ ! -e $incomplete_inventory && ! -e ${incomplete_inventory}.tmp && ! -e ${incomplete_inventory}.stderr.tmp ]] ||
  test::fail "rejected incomplete permission inventory left ambiguous evidence"

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
ATLAS_PHASE0_OPERATION[recovery_kubeconfig]='recovery-current-g3.kubeconfig'
ATLAS_PHASE0_OPERATION[previous_recovery_kubeconfig]='recovery-previous-g2.kubeconfig'
ATLAS_PHASE0_OPERATION[authorizer_kubeconfig]='authorizer-current-g2.kubeconfig'
ATLAS_PHASE0_OPERATION[previous_authorizer_kubeconfig]='authorizer-previous-g1.kubeconfig'
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
    if [[ $kubeconfig == recovery-current-g3.kubeconfig && $command == patch ]]; then
      printf 'yes\n'
      return
    fi
    if [[ $kubeconfig == authorizer-current-g2.kubeconfig && $command == create ]]; then
      printf 'yes\n'
      return
    fi
    if [[ $allow_old == true && $kubeconfig == *-previous-g*.kubeconfig ]]; then
      printf 'yes\n'
      return
    fi
    printf 'no\n'
    return 1
  }
  phase0_ceremony::_verify_active_permissions
)
verify_active_permissions false
[[ $(grep -c -- '-previous-g' "$permission_calls") == 14 ]] ||
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

git_revalidation_calls="${test_workspace}/git-revalidation-calls"
(
  : > "$git_revalidation_calls"
  phase0_session::revalidate_target_paths() { printf 'paths\n' >> "$git_revalidation_calls"; }
  phase0_session::_revalidate_git_authority() {
    printf 'git-authority\n' >> "$git_revalidation_calls"
    return 23
  }
  # shellcheck disable=SC2329 # Any later authority access is a test failure.
  phase0_session::assert_file() { printf 'file\n' >> "$git_revalidation_calls"; }
  # shellcheck disable=SC2329 # Any later cluster access is a test failure.
  phase0_session::_admin_target() { printf 'kubectl\n' >> "$git_revalidation_calls"; }
  # shellcheck disable=SC2329 # Credential production must remain unreachable.
  openssl() { printf 'openssl\n' >> "$git_revalidation_calls"; }
  if phase0_session::revalidate > /dev/null 2>&1; then
    test::fail "Git authority revalidation failure returned success"
  fi
)
[[ $(< "$git_revalidation_calls") == $'paths\ngit-authority' ]] ||
  test::fail "repository or environment drift reached credentials or Kubernetes after the Human Gate"

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
phase0_session::arm_unexpected_exit_guard() { printf 'guard-arm\n' >> "$order"; }
phase0_session::prepare() { printf 'prepare\n' >> "$order"; }
phase0_session::human_gate() { printf 'gate\n' >> "$order"; }
phase0_session::revalidate() {
  printf 'revalidate-denied\n' >> "$order"
  return 23
}
phase0_session::journal_append() { printf 'journal-%s-%s\n' "$1" "$2" >> "$order"; }
phase0_session::release_lock() { printf 'unlock\n' >> "$order"; }
phase0_session::disarm_unexpected_exit_guard() { printf 'guard-disarm\n' >> "$order"; }
phase0_ceremony::_run() { test::fail "mutation ran after revalidation failed"; }
if phase0_ceremony::run a b c d e f g h i 3 2 2 1; then
  test::fail "revalidation failure returned success"
else
  [[ $? == 23 ]] || test::fail "revalidation failure exit code changed"
fi
[[ $(< "$order") == $'resolve\npreflight\nlock\nguard-arm\nprepare\ngate\nrevalidate-denied\njournal-PREMUTATION-DENIED\nunlock\nguard-disarm' ]] ||
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
