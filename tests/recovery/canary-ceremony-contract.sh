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
ATLAS_PHASE0_OPERATION[authorizer_principal]=atlas:recovery-authorizer:00000000-0000-0000-0000-000000000000:g1
ATLAS_PHASE0_OPERATION[recovery_principal]=atlas:break-glass:00000000-0000-0000-0000-000000000000:g1
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
ATLAS_PHASE0_TARGET[known_good_revision]=$(printf 'c%.0s' {1..40})
ATLAS_PHASE0_TARGET[cluster_name]=atlas-recovery-drill-20260817t000000z-0123abcd
ATLAS_PHASE0_TARGET[context]=kind-atlas-recovery-drill-20260817t000000z-0123abcd
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
phase0_session::journal_append() {
  printf 'journal\t%s\t%s\n' "$1" "$2" >> "$cleanup_calls"
}
ATLAS_PHASE0_TARGET[admin_kubeconfig]=admin.kubeconfig
ATLAS_PHASE0_TARGET[cluster_name]=atlas-recovery-drill-20260817t000000z-0123abcd
phase0_ceremony::_cleanup_cluster_resources
[[ $(grep -c '^delete' "$cleanup_calls") == 17 ]] || test::fail "cleanup did not delete every installed canary object"
grep -Fq $'get\tget role atlas-bootstrap-recovery-canary --ignore-not-found -o name -n kube-system' "$cleanup_calls" ||
  test::fail "cleanup verification escaped the namespaced Role boundary"
grep -Fq '/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-recovery-fence-authorization-canary' "$cleanup_calls" ||
  test::fail "cleanup omitted the Fence Policy"
awk '{print $2 "/" $3}' "$absence_calls" | grep -Fv 'configmap/atlas-bootstrap-operation-fence-canary' | sort > "${test_workspace}/absence-inventory"
awk -F $'\t' '$1 != "delete" && $1 != "get" && $1 != "journal" {print $1 "/" $2}' "$cleanup_calls" | sort > "${test_workspace}/cleanup-inventory"
cmp -s "${test_workspace}/absence-inventory" "${test_workspace}/cleanup-inventory" ||
  test::fail "preflight and cleanup definition inventories differ"

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

order="${test_workspace}/order"
: > "$order"
phase0_ceremony::_issue_credentials() { printf 'credentials\n' >> "$order"; }
phase0_ceremony::_install_definitions() { printf 'definitions\n' >> "$order"; }
phase0_ceremony::_admission_escape_drill() { printf 'escape\n' >> "$order"; }
phase0_ceremony::_session_authorization_drill() { printf 'controls\n' >> "$order"; }
phase0_ceremony::_cleanup_cluster_resources() { printf 'resources-clean\n' >> "$order"; }
phase0_ceremony::_cleanup_credentials() { printf 'credentials-clean\n' >> "$order"; }
phase0_ceremony::_finalize_evidence() { printf 'evidence\n' >> "$order"; }
phase0_ceremony::_run > /dev/null
[[ $(< "$order") == $'credentials\ndefinitions\nescape\ncontrols\nresources-clean\ncredentials-clean\nevidence' ]] ||
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
