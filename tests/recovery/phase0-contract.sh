#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-phase0-test.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

audit_directory="${test_workspace}/audit"
mkdir -m 0700 "$audit_directory"
audit_directory=$(cd "$audit_directory" && pwd -P)

recovery_cli=./bootstrap/recovery/atlas-recovery
"$recovery_cli" --help > /dev/null
"$recovery_cli" --version | grep -Eq '^atlas-recovery [0-9]+\.[0-9]+\.[0-9]+$'

phase0::kubeadm_arg() {
  local name=$1 patch=$2
  yq ".apiServer.extraArgs[] | select(.name == \"${name}\") | .value" <<< "$patch"
}

first_render="${test_workspace}/kind-first.yaml"
second_render="${test_workspace}/kind-second.yaml"
"$recovery_cli" phase0 audit-config --audit-dir "$audit_directory" > "$first_render"
"$recovery_cli" phase0 audit-config --audit-dir "$audit_directory" > "$second_render"
cmp -s "$first_render" "$second_render" || test::fail "Phase-0 Kind rendering is not deterministic"

[[ $(yq '.kind' "$first_render") == Cluster ]] || test::fail "rendered Phase-0 object is not a Kind cluster"
kubeadm_patch=$(yq '.nodes[0].kubeadmConfigPatches[0]' "$first_render")
[[ $(yq '.apiServer.extraArgs | type' <<< "$kubeadm_patch") == '!!seq' ]] || test::fail "kubeadm v1beta4 extraArgs is not a structured list"
audit_policy_arg=$(phase0::kubeadm_arg audit-policy-file "$kubeadm_patch")
audit_log_arg=$(phase0::kubeadm_arg audit-log-path "$kubeadm_patch")
[[ $audit_policy_arg == /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml ]] || test::fail "API server audit policy flag is missing"
[[ $audit_log_arg == /var/log/kubernetes/audit/kube-apiserver-audit.log ]] || test::fail "API server audit log flag is missing"

policy_mount=$(yq '.nodes[0].extraMounts[] | select(.containerPath == "/etc/kubernetes/policies/atlas-recovery-audit-policy.yaml")' "$first_render")
log_mount=$(yq '.nodes[0].extraMounts[] | select(.containerPath == "/var/log/kubernetes/audit")' "$first_render")
[[ $(yq '.readOnly' <<< "$policy_mount") == true ]] || test::fail "audit policy mount is not read-only"
[[ $(yq '.hostPath' <<< "$log_mount") == "$audit_directory" ]] || test::fail "external audit destination is not mounted"
[[ $(yq '.readOnly' <<< "$log_mount") == false ]] || test::fail "audit log mount is not writable"
test::pass "Phase-0 audited Kind rendering is deterministic and explicit"

if "$recovery_cli" phase0 audit-config --audit-dir relative/path > /dev/null 2>&1; then
  test::fail "relative audit destination was accepted"
fi
if "$recovery_cli" phase0 audit-config --audit-dir "$ATLAS_TEST_ROOT" > /dev/null 2>&1; then
  test::fail "repository audit destination was accepted"
fi
if "$recovery_cli" phase0 audit-config --audit-dir / > /dev/null 2>&1; then
  test::fail "filesystem root was accepted as the audit destination"
fi
ln -s "$audit_directory" "${test_workspace}/audit-link"
if "$recovery_cli" phase0 audit-config --audit-dir "${test_workspace}/audit-link" > /dev/null 2>&1; then
  test::fail "symlinked audit destination was accepted"
fi
if "$recovery_cli" phase0 audit-config > /dev/null 2>&1; then
  test::fail "missing audit destination was accepted"
fi
test::pass "Phase-0 audit destination fails closed"

[[ $(yq '.rules[0].level' clusters/kind/recovery-audit-policy.yaml) == Metadata ]] || test::fail "credential-bearing resources are not handled before body capture"
[[ $(yq '.rules[-1].level' clusters/kind/recovery-audit-policy.yaml) == Metadata ]] || test::fail "audit policy lacks a metadata-only catch-all"
grep -Fq 'secrets' clusters/kind/recovery-audit-policy.yaml || test::fail "Secret redaction boundary is absent"
grep -Fq 'tokenreviews' clusters/kind/recovery-audit-policy.yaml || test::fail "token review redaction boundary is absent"
grep -Fq 'atlas-bootstrap-operation-fence-canary' clusters/kind/recovery-audit-policy.yaml || test::fail "canary Fence audit coverage is absent"
captured_bodies=$(yq '.rules[] | select(.level == "RequestResponse") | .resources[].resources[]' clusters/kind/recovery-audit-policy.yaml)
if grep -Eq '^(secrets|tokenreviews|certificatesigningrequests)$' <<< "$captured_bodies"; then
  test::fail "audit policy captures credential-bearing request bodies"
fi
test::pass "audit policy captures recovery authority without credential bodies"

test::assert_not_found 'bootstrap/recovery|atlas-recovery|recovery-audit-policy' bootstrap/atlas bootstrap/argocd bootstrap/cluster bootstrap/host bootstrap/lib bootstrap/registry
test::assert_not_found 'recovery-audit-policy' gitops
test::assert_not_found '(kubectl|kind)[[:space:]]+(apply|create|delete|patch|replace)' bootstrap/recovery
test::assert_not_found '(admission|canary)-(suspend|restore)|execute|resume|close' bootstrap/recovery
test::pass "Phase-0 foundation is isolated, definition-only, and non-mutating"
