#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
# shellcheck source=bootstrap/recovery/audit-config.sh
source "$(dirname "${BASH_SOURCE[0]}")/../../bootstrap/recovery/audit-config.sh"
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

phase0::audit_level() {
  local verb=$1 group=$2 resource=$3 namespace=$4 name=$5
  AUDIT_VERB=$verb \
    AUDIT_GROUP=$group \
    AUDIT_RESOURCE=$resource \
    AUDIT_NAMESPACE=$namespace \
    AUDIT_NAME=$name \
    yq '
      [.rules[] |
        select(((.verbs // []) | length) == 0 or
          (.verbs | any_c(. == strenv(AUDIT_VERB)))) |
        select(((.namespaces // []) | length) == 0 or
          (.namespaces | any_c(. == strenv(AUDIT_NAMESPACE)))) |
        select(((.resources // []) | length) == 0 or
          (.resources | any_c(
            .group == strenv(AUDIT_GROUP) and
            (((.resources // []) | length) == 0 or
              (.resources | any_c(. == strenv(AUDIT_RESOURCE)))) and
            (((.resourceNames // []) | length) == 0 or
              (.resourceNames | any_c(. == strenv(AUDIT_NAME)))))))] |
      .[0].level
    ' clusters/kind/recovery-audit-policy.yaml
}

phase0::assert_audit_level() {
  local expected=$1 label=$2
  shift 2
  [[ $(phase0::audit_level "$@") == "$expected" ]] || test::fail "unexpected audit level: ${label}"
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

phase0::assert_audit_level Metadata "Secret CREATE" create "" secrets kube-system ""
phase0::assert_audit_level Metadata "ServiceAccount token CREATE" create "" serviceaccounts/token kube-system ""
phase0::assert_audit_level Metadata "TokenReview CREATE" create authentication.k8s.io tokenreviews "" ""
phase0::assert_audit_level Metadata "CSR CREATE" create certificates.k8s.io certificatesigningrequests "" ""
phase0::assert_audit_level Metadata "named Fence CREATE" create "" configmaps kube-system atlas-bootstrap-operation-fence-canary
phase0::assert_audit_level Metadata "collection Fence CREATE" create "" configmaps kube-system ""
phase0::assert_audit_level RequestResponse "admission canary UPDATE" update "" configmaps kube-system atlas-bootstrap-admission-escape-canary
phase0::assert_audit_level RequestResponse "named Fence UPDATE" update "" configmaps kube-system atlas-bootstrap-operation-fence-canary
phase0::assert_audit_level Metadata "unrelated ConfigMap UPDATE" update "" configmaps kube-system unrelated
phase0::assert_audit_level RequestResponse "RBAC CREATE" create rbac.authorization.k8s.io rolebindings kube-system ""
phase0::assert_audit_level RequestResponse "Argo Application CREATE" create argoproj.io applications argocd ""
[[ $(yq '.rules[-1].level' clusters/kind/recovery-audit-policy.yaml) == Metadata ]] || test::fail "audit policy lacks a metadata-only catch-all"
test::pass "audit policy first-match semantics protect sensitive request bodies"

utf8_locale=$(locale -a | awk 'tolower($0) ~ /utf-?8/ && found == "" { found = $0 } END { print found }')
[[ -n $utf8_locale ]] || test::fail "no UTF-8 locale is available for control-byte tests"
unsafe_byte_sequences=(
  $'\x01'
  $'\x1f'
  $'\x7f'
  $'\x80'
  $'\x9f'
  $'\xc0\x80'
  $'\xc2\x80'
  $'\xc2\x9f'
  $'\xe0\x80\x80'
  $'\xef\xbf\xbe'
  $'\xef\xbf\xbf'
  $'\xf5\x80\x80\x80'
)
path_rejected_suffixes=(
  $'\x01'
  $'\x1f'
  $'\x7f'
  $'\xc2\x80'
  $'\xc2\x9f'
  $'\xef\xbf\xbe'
  $'\xef\xbf\xbf'
)
for test_locale in C "$utf8_locale"; do
  for sequence in "${unsafe_byte_sequences[@]}"; do
    if LC_ALL="$test_locale" audit::_path_bytes_are_safe "$sequence"; then
      test::fail "unsafe path bytes were accepted under ${test_locale}"
    fi
  done
  for sequence in $'\xc4\x80' $'\xf0\x9f\x98\x80'; do
    LC_ALL="$test_locale" audit::_path_bytes_are_safe "$sequence" || test::fail "valid UTF-8 bytes were rejected under ${test_locale}"
  done

  for suffix in "${path_rejected_suffixes[@]}"; do
    rejected_directory="${test_workspace}/audit-rejected-${test_locale}-${suffix}"
    # APFS rejects YAML noncharacters at mkdir; filesystems that permit them
    # exercise the same CLI boundary with an existing real directory.
    mkdir "$rejected_directory" 2> /dev/null || true
    if rejected_output=$(LC_ALL="$test_locale" "$recovery_cli" phase0 audit-config --audit-dir "$rejected_directory" 2>&1); then
      test::fail "non-YAML-printable audit destination was accepted under ${test_locale}"
    fi
    grep -Fq 'valid YAML-printable UTF-8 without C0 or C1 control characters' <<< "$rejected_output" || test::fail "path rejection used the wrong failure boundary under ${test_locale}"
  done

  for suffix in $'\xc4\x80' $'\xf0\x9f\x98\x80'; do
    unicode_directory="${test_workspace}/audit-unicode-${test_locale}-${suffix}"
    unicode_render="${test_workspace}/kind-unicode-${test_locale}-${suffix}.yaml"
    mkdir "$unicode_directory"
    LC_ALL="$test_locale" "$recovery_cli" phase0 audit-config --audit-dir "$unicode_directory" > "$unicode_render" || test::fail "valid Unicode audit destination was rejected under ${test_locale}"
    yq '.' "$unicode_render" > /dev/null || test::fail "rendered Unicode path is not valid YAML under ${test_locale}"
  done
done
test::pass "YAML-printable UTF-8 path validation is locale independent"

test::assert_not_found 'bootstrap/recovery|atlas-recovery|recovery-audit-policy' bootstrap/atlas bootstrap/argocd bootstrap/cluster bootstrap/host bootstrap/lib bootstrap/registry
test::assert_not_found 'recovery-audit-policy' gitops
test::assert_not_found '(kubectl|kind)[[:space:]]+(apply|create|delete|patch|replace)' bootstrap/recovery
test::assert_not_found '(admission|canary)-(suspend|restore)|execute|resume|close' bootstrap/recovery
test::pass "Phase-0 foundation is isolated, definition-only, and non-mutating"
