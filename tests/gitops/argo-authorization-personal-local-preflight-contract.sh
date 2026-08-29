#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly preflight=$probe_root/personal-local-preflight
readonly profile=$probe_root/personal-local-profile.json
readonly target_fixture=tests/gitops/fixtures/argo-authorization-probe/valid-personal-local-target.json
readonly gate_fixture=tests/gitops/fixtures/argo-authorization-probe/valid-personal-local-owner-gate.json
fake_kubectl=$(pwd -P)/tests/gitops/fixtures/argo-authorization-probe/fake-personal-local-kubectl
readonly fake_kubectl
real_kubectl=$(command -v kubectl)
readonly real_kubectl
readonly expected_commit=${1:-}
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-preflight-contract.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] || test::fail "expected commit is required"
bash -n "$preflight" "$fake_kubectl"
shellcheck -x "$preflight" "$fake_kubectl"

sha256_text() { shasum -a 256 | awk '{print $1}'; }
sha256_file() { shasum -a 256 "$1" | awk '{print $1}'; }
canonical_json_sha() {
  local projection
  projection=$(yq -o=json -I=0 'sort_keys(..)' "$1") || return 1
  printf '%s' "$projection" | sha256_text
}

raw_path() {
  case "$1|$2|$3" in
    'v1|Namespace|') printf '/api/v1/namespaces/%s' "$4" ;;
    'v1|ConfigMap|argocd') printf '/api/v1/namespaces/argocd/configmaps/%s' "$4" ;;
    'argoproj.io/v1alpha1|Application|argocd') printf '/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/%s' "$4" ;;
    'argoproj.io/v1alpha1|AppProject|argocd') printf '/apis/argoproj.io/v1alpha1/namespaces/argocd/appprojects/%s' "$4" ;;
    'apps/v1|Deployment|argocd') printf '/apis/apps/v1/namespaces/argocd/deployments/%s' "$4" ;;
    'apps/v1|StatefulSet|argocd') printf '/apis/apps/v1/namespaces/argocd/statefulsets/%s' "$4" ;;
    *) return 1 ;;
  esac
}

read_plan() {
  local target=$1 object api kind namespace name path
  while IFS= read -r object; do
    api=$(yq -r '.apiVersion' <<< "$object")
    kind=$(yq -r '.kind' <<< "$object")
    namespace=$(yq -r '.namespace' <<< "$object")
    name=$(yq -r '.name' <<< "$object")
    path=$(raw_path "$api" "$kind" "$namespace" "$name")
    printf '%s\t%s\t%s\t%s\tget\t%s\n' "$api" "$kind" "$namespace" "$name" "$path"
  done < <(yq -o=json -I=0 '.desiredObjects[]' "$target") | sort
}

owner_gate_target_sha() {
  local projection
  projection=$(yq -o=json -I=0 \
    'del(.ownerGateState,.ownerGateSHA256,.ownerGateTargetProjectionSHA256,
      .targetFingerprintSHA256,.approvedTargetDocumentSHA256) | sort_keys(..)' "$1") || return 1
  printf '%s' "$projection" | sha256_text
}

target_fingerprint() {
  local target=$1 payload
  printf -v payload 'rolloutProfile=%s\nauthorityBaseline=%s\ncontractGitCommit=%s\nenvironmentName=%s\nclusterName=%s\nkubeContext=%s\nkubeconfigPath=%s\nkubeconfigSHA256=%s\nkubectlPath=%s\nkubectlSHA256=%s\nkubectlVersion=%s\nkubernetesVersion=%s\napiServerURL=%s\nkubeSystemNamespaceUID=%s\napiServerCASPKISHA256=%s\nrepositoryURL=%s\nwaiverDecisionSHA256=%s\nownerGateState=%s\nownerGateSHA256=%s\nownerGateTargetProjectionSHA256=%s\n' \
    "$(yq -r '.rolloutProfile' "$target")" "$(yq -r '.authorityBaseline' "$target")" \
    "$(yq -r '.contractGitCommit' "$target")" "$(yq -r '.environmentName' "$target")" \
    "$(yq -r '.clusterName' "$target")" "$(yq -r '.kubeContext' "$target")" \
    "$(yq -r '.kubeconfigPath' "$target")" "$(yq -r '.kubeconfigSHA256' "$target")" \
    "$(yq -r '.kubectlPath' "$target")" "$(yq -r '.kubectlSHA256' "$target")" \
    "$(yq -r '.kubectlVersion' "$target")" "$(yq -r '.kubernetesVersion' "$target")" \
    "$(yq -r '.apiServerURL' "$target")" "$(yq -r '.kubeSystemNamespaceUID' "$target")" \
    "$(yq -r '.apiServerCASPKISHA256' "$target")" "$(yq -r '.repositoryURL' "$target")" \
    "$(yq -r '.waiverDecisionSHA256' "$target")" "$(yq -r '.ownerGateState' "$target")" \
    "$(yq -r '.ownerGateSHA256' "$target")" "$(yq -r '.ownerGateTargetProjectionSHA256' "$target")"
  printf '%s' "$payload" | sha256_text
}

seal_target() {
  local input=$1 output=$2 projection sha
  projection=$(yq -o=json -I=0 'del(.approvedTargetDocumentSHA256) | sort_keys(..)' "$input")
  sha=$(printf '%s' "$projection" | sha256_text)
  APPROVED_SHA=$sha yq '.approvedTargetDocumentSHA256 = strenv(APPROVED_SHA)' "$input" > "$output"
}

render_desired_objects() {
  {
    printf '%s\n' 'apiVersion: v1' 'kind: Namespace' 'metadata:' '  name: kube-system' '---'
    "$real_kubectl" kustomize gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-self-base-overlay
    printf '%s\n' '---'
    "$real_kubectl" kustomize gitops/platform/management/protection-foundation/definitions/argo-hardening
    printf '%s\n' '---'
    "$real_kubectl" kustomize gitops/platform/management/projects
    printf '%s\n' '---'
    yq '.' bootstrap/argocd/atlas-bootstrap-project.yaml
    printf '%s\n' '---'
    helm template atlas-argocd vendor/charts/argo-cd-10.3.3 --namespace argocd --include-crds \
      --values gitops/platform/management/argocd-self/values.yaml \
      --values gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-values-hardening.yaml
  }
}

profile_sha=$(canonical_json_sha "$profile")
ca_key=$test_workspace/ca.key
ca_certificate=$test_workspace/ca.crt
wrong_ca_key=$test_workspace/wrong-ca.key
wrong_ca_certificate=$test_workspace/wrong-ca.crt
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$ca_key" > /dev/null 2>&1
openssl req -x509 -sha256 -key "$ca_key" -subj /CN=atlas-personal-local-test-ca \
  -days 1 -out "$ca_certificate" > /dev/null 2>&1
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$wrong_ca_key" > /dev/null 2>&1
openssl req -x509 -sha256 -key "$wrong_ca_key" -subj /CN=atlas-personal-local-wrong-ca \
  -days 1 -out "$wrong_ca_certificate" > /dev/null 2>&1
ca_data=$(openssl base64 -A -in "$ca_certificate")
wrong_ca_data=$(openssl base64 -A -in "$wrong_ca_certificate")
ca_spki_sha=$(
  openssl x509 -in "$ca_certificate" -pubkey -noout |
    openssl pkey -pubin -outform DER |
    sha256_text
)
kubeconfig=$test_workspace/kubeconfig
readonly token_sentinel=ATLAS_FIXTURE_TOKEN_MUST_NOT_BE_PROJECTED
readonly private_key_sentinel=ATLAS_FIXTURE_PRIVATE_KEY_MUST_NOT_BE_PROJECTED
printf '%s\n' \
  'apiVersion: v1' \
  'kind: Config' \
  'users:' \
  '  - name: fake-user' \
  '    user:' \
  "      token: ${token_sentinel}" \
  "      client-key-data: ${private_key_sentinel}" > "$kubeconfig"
chmod 0600 "$kubeconfig"
objects=$test_workspace/objects.yaml
render_desired_objects > "$objects"
read_log=$test_workspace/read.log
: > "$read_log"
config_view_log=$test_workspace/config-view.log
: > "$config_view_log"

target_draft=$test_workspace/target-draft.json
target_gate_projection=$test_workspace/target-gate-projection.json
target_gated=$test_workspace/target-gated.json
target=$test_workspace/target.json
gate=$test_workspace/owner-gate.json
PROFILE_SHA=$profile_sha COMMIT=$expected_commit KUBECONFIG=$kubeconfig \
  KUBECONFIG_SHA=$(sha256_file "$kubeconfig") KUBECTL=$fake_kubectl \
  KUBECTL_SHA=$(sha256_file "$fake_kubectl") CA_SHA=$ca_spki_sha yq \
  '.contractGitCommit = strenv(COMMIT) | .waiverDecisionSHA256 = strenv(PROFILE_SHA) |
  .ownerGateState = "APPROVED" | .kubeconfigPath = strenv(KUBECONFIG) |
  .kubeconfigSHA256 = strenv(KUBECONFIG_SHA) | .kubectlPath = strenv(KUBECTL) |
  .kubectlSHA256 = strenv(KUBECTL_SHA) |
  .apiServerCASPKISHA256 = strenv(CA_SHA)' "$target_fixture" > "$target_draft"
gate_target_sha=$(owner_gate_target_sha "$target_draft")
GATE_TARGET_SHA=$gate_target_sha yq \
  '.ownerGateTargetProjectionSHA256 = strenv(GATE_TARGET_SHA)' "$target_draft" > "$target_gate_projection"
plan=$test_workspace/read-plan.tsv
read_plan "$target_gate_projection" > "$plan"
plan_sha=$(printf '%s' "$(< "$plan")" | sha256_text)
COMMIT=$expected_commit PROFILE_SHA=$profile_sha GATE_TARGET_SHA=$gate_target_sha CA_SHA=$ca_spki_sha \
  PLAN_SHA=$plan_sha KUBECONFIG_SHA=$(sha256_file "$kubeconfig") yq -o=json -I=2 \
  '.decision = "APPROVED" | .contractGitCommit = strenv(COMMIT) |
  .waiverDecisionSHA256 = strenv(PROFILE_SHA) |
  .ownerGateTargetProjectionSHA256 = strenv(GATE_TARGET_SHA) |
  .readPlanSHA256 = strenv(PLAN_SHA) | .kubeconfigSHA256 = strenv(KUBECONFIG_SHA) |
  .apiServerCASPKISHA256 = strenv(CA_SHA)' \
  "$gate_fixture" > "$gate"
gate_sha=$(canonical_json_sha "$gate")
GATE_SHA=$gate_sha yq '.ownerGateSHA256 = strenv(GATE_SHA)' "$target_gate_projection" > "$target_gated"
fingerprint=$(target_fingerprint "$target_gated")
TARGET_SHA=$fingerprint yq '.targetFingerprintSHA256 = strenv(TARGET_SHA)' "$target_gated" > "${target_gated}.fingerprinted"
seal_target "${target_gated}.fingerprinted" "$target"

export ATLAS_FAKE_REAL_KUBECTL=$real_kubectl
export ATLAS_FAKE_OBJECTS=$objects
export ATLAS_FAKE_READ_PLAN=$plan
export ATLAS_FAKE_READ_LOG=$read_log
export ATLAS_FAKE_CONFIG_VIEW_LOG=$config_view_log
export ATLAS_FAKE_CONTEXT=kind-atlas-test
export ATLAS_FAKE_API_SERVER=https://127.0.0.1:6443
export ATLAS_FAKE_NAMESPACE_UID=00000000-0000-4000-8000-000000000001
export ATLAS_FAKE_CA_DATA=$ca_data

run_preflight() {
  local target_file=$1 gate_file=$2 expected_gate_sha=$3 output_file=$4 stdout_file=$5 stderr_file=$6 status
  if "$preflight" run --target "$target_file" --owner-gate "$gate_file" \
    --expected-owner-gate-sha "$expected_gate_sha" --expected-commit "$expected_commit" \
    --output "$output_file" > "$stdout_file" 2> "$stderr_file"; then
    status=0
  else
    status=$?
  fi
  return "$status"
}

evidence=$test_workspace/evidence.json
stdout_file=$test_workspace/run.stdout
stderr_file=$test_workspace/run.stderr
run_preflight "$target" "$gate" "$gate_sha" "$evidence" "$stdout_file" "$stderr_file" ||
  test::fail "fake-backed authoritative read-only preflight failed: $(< "$stderr_file")"
[[ $(< "$stdout_file") == PERSONAL_LOCAL_READY && ! -s $stderr_file &&
$(yq -r '.result' "$evidence") == PERSONAL_LOCAL_READY &&
$(yq -r '.liveProjection.status' "$evidence") == MATCH &&
$(yq -r '.completeness.executedReads' "$evidence") -eq 26 &&
$(yq -r '.completeness.mutations' "$evidence") -eq 0 ]] ||
  test::fail "successful preflight did not produce exact READY Evidence"

validator_stdout=$test_workspace/validator.stdout
validator_stderr=$test_workspace/validator.stderr
"$preflight" validate --target "$target" --owner-gate "$gate" \
  --expected-owner-gate-sha "$gate_sha" --expected-commit "$expected_commit" \
  --evidence "$evidence" > "$validator_stdout" 2> "$validator_stderr" ||
  test::fail "authoritative Evidence Validator rejected generated Evidence"
[[ $(< "$validator_stdout") == PERSONAL_LOCAL_READY && ! -s $validator_stderr ]] ||
  test::fail "Evidence Validator emitted an ambiguous result"
for output_file in "$stdout_file" "$stderr_file" "$validator_stdout" "$validator_stderr" "$evidence" "$config_view_log"; do
  if grep -Fq "$token_sentinel" "$output_file" || grep -Fq "$private_key_sentinel" "$output_file"; then
    test::fail "kubeconfig credential sentinel escaped its hash-bound file"
  fi
done
grep -Fq '.users' "$config_view_log" && test::fail "config projection requested a users field"
raw_config_queries=$(awk -F '\t' '$1 == "true" {print $2}' "$config_view_log" | sort -u)
[[ $raw_config_queries == 'jsonpath={.clusters[0].cluster.certificate-authority-data}' ]] ||
  test::fail "raw config access was not limited to the CA data projection"

assert_validator_rejects() {
  local name=$1 evidence_file=$2
  if "$preflight" validate --target "$target" --owner-gate "$gate" \
    --expected-owner-gate-sha "$gate_sha" --expected-commit "$expected_commit" \
    --evidence "$evidence_file" > /dev/null 2>&1; then
    test::fail "Evidence Validator accepted ${name}"
  fi
}

assert_sensitive_scanner_rejects() {
  local name=$1 evidence_file=$2
  if (
    # shellcheck source=/dev/null
    source "$preflight"
    # shellcheck disable=SC2034 # consumed by the sourced preflight cleanup trap
    preflight_tmp=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-sensitive-scan.XXXXXX")
    preflight::_assert_evidence_shape "$evidence_file" &&
      preflight::_assert_evidence_has_no_sensitive_content "$evidence_file"
  ); then
    test::fail "sensitive-content scanner accepted ${name}"
  fi
}

[[ $(grep -Fxc /version "$read_log") -eq 1 ]] || test::fail "Kubernetes /version was not read exactly once"
object_reads=$(grep -Fvx /version "$read_log")
[[ $(wc -l <<< "$object_reads" | tr -d ' ') -eq 26 && $(sort -u <<< "$object_reads" | wc -l | tr -d ' ') -eq 13 ]] ||
  test::fail "preflight did not execute two complete thirteen-object snapshots"
while IFS= read -r path; do
  [[ $(grep -Fxc "$path" <<< "$object_reads") -eq 2 ]] || test::fail "read plan path was not executed twice: ${path}"
done < <(awk -F '\t' '{print $6}' "$plan")

invalid_evidence=$test_workspace/evidence-invalid-zero-read-ready.json
yq '.liveProjection.status = "NOT_EXECUTED" | .liveProjection.liveBeforeSHA256 = null |
  .liveProjection.liveAfterSHA256 = null |
  (.liveProjection.objects[].liveBeforeProjectionSHA256 = null) |
  (.liveProjection.objects[].liveAfterProjectionSHA256 = null) |
  (.kubernetesReads[].status = "NOT_EXECUTED") |
  .completeness.executedReads = 0 | .completeness.skippedReads = 26' "$evidence" > "$invalid_evidence"
if "$preflight" validate --target "$target" --owner-gate "$gate" \
  --expected-owner-gate-sha "$gate_sha" --expected-commit "$expected_commit" \
  --evidence "$invalid_evidence" > /dev/null 2>&1; then
  test::fail "zero-read READY Evidence was accepted"
fi

for mutation in ca waiver fragments nested-sensitive reverse-time sensitive-cookie sensitive-kubeconfig sensitive-token; do
  mutated_evidence=$test_workspace/evidence-invalid-${mutation}.json
  case $mutation in
    ca)
      yq '.target.apiServerCASPKISHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' \
        "$evidence" > "$mutated_evidence"
      ;;
    waiver)
      yq '.waiver.baseArgoActions = "RUNTIME_PROVEN"' "$evidence" > "$mutated_evidence"
      ;;
    fragments)
      yq '.liveProjection.reviewedPolicyFragments += ["policy.unreviewed.csv"]' \
        "$evidence" > "$mutated_evidence"
      ;;
    nested-sensitive)
      yq '.liveProjection.objects[0].token = "ATLAS_FIXTURE_NOT_A_TOKEN"' \
        "$evidence" > "$mutated_evidence"
      ;;
    reverse-time)
      STARTED=$(yq -r '.startedAt' "$evidence") COMPLETED=$(yq -r '.completedAt' "$evidence") \
        yq '.startedAt = strenv(COMPLETED) | .completedAt = strenv(STARTED)' \
        "$evidence" > "$mutated_evidence"
      ;;
    sensitive-cookie)
      yq '.target.environmentName = "cookie=ATLAS_FIXTURE_NOT_A_COOKIE"' \
        "$evidence" > "$mutated_evidence"
      ;;
    sensitive-kubeconfig)
      yq '.target.environmentName = "kubeconfig:/fixture/not-a-real-host/config"' \
        "$evidence" > "$mutated_evidence"
      ;;
    sensitive-token)
      yq '.target.environmentName = "token:ATLAS_FIXTURE_NOT_A_TOKEN"' \
        "$evidence" > "$mutated_evidence"
      ;;
  esac
  assert_validator_rejects "$mutation mutation" "$mutated_evidence"
  case $mutation in
    sensitive-*) assert_sensitive_scanner_rejects "$mutation mutation" "$mutated_evidence" ;;
  esac
done

tampered_gate=$test_workspace/tampered-gate.json
tampered_target_draft=$test_workspace/tampered-target-draft.json
tampered_target=$test_workspace/tampered-target.json
yq '.sessionID = "personal-local-20260829T000001Z-00000000000000000000000000000002"' "$gate" > "$tampered_gate"
tampered_gate_sha=$(canonical_json_sha "$tampered_gate")
GATE_SHA=$tampered_gate_sha yq '.ownerGateSHA256 = strenv(GATE_SHA)' "$target" > "$tampered_target_draft"
fingerprint=$(target_fingerprint "$tampered_target_draft")
TARGET_SHA=$fingerprint yq '.targetFingerprintSHA256 = strenv(TARGET_SHA)' "$tampered_target_draft" > "${tampered_target_draft}.fingerprinted"
seal_target "${tampered_target_draft}.fingerprinted" "$tampered_target"
reads_before=$(wc -l < "$read_log" | tr -d ' ')
if run_preflight "$tampered_target" "$tampered_gate" "$gate_sha" "$test_workspace/tampered-evidence.json" \
  "$test_workspace/tampered.stdout" "$test_workspace/tampered.stderr"; then
  test::fail "self-declared Owner Gate SHA was accepted"
fi
[[ $(wc -l < "$read_log" | tr -d ' ') -eq "$reads_before" ]] ||
  test::fail "Owner Gate mismatch reached a Kubernetes read"

export ATLAS_FAKE_DRIFT_PATH=/api/v1/namespaces/argocd/configmaps/argocd-cm
if run_preflight "$target" "$gate" "$gate_sha" "$test_workspace/drift-evidence.json" \
  "$test_workspace/drift.stdout" "$test_workspace/drift.stderr"; then
  test::fail "drifted live projection produced READY Evidence"
fi
unset ATLAS_FAKE_DRIFT_PATH

export ATLAS_FAKE_IDENTITY_DRIFT_PATH=/api/v1/namespaces/argocd/configmaps/argocd-cm
if run_preflight "$target" "$gate" "$gate_sha" "$test_workspace/identity-drift-evidence.json" \
  "$test_workspace/identity-drift.stdout" "$test_workspace/identity-drift.stderr"; then
  test::fail "replaced live object identity produced READY Evidence"
fi
unset ATLAS_FAKE_IDENTITY_DRIFT_PATH

export ATLAS_FAKE_MISSING_PATH=/api/v1/namespaces/argocd/configmaps/argocd-cm
if run_preflight "$target" "$gate" "$gate_sha" "$test_workspace/missing-evidence.json" \
  "$test_workspace/missing.stdout" "$test_workspace/missing.stderr"; then
  test::fail "missing reviewed object produced READY Evidence"
fi
unset ATLAS_FAKE_MISSING_PATH

export ATLAS_FAKE_STDERR_PATH=/version
if run_preflight "$target" "$gate" "$gate_sha" "$test_workspace/stderr-evidence.json" \
  "$test_workspace/stderr.stdout" "$test_workspace/stderr.stderr"; then
  test::fail "Kubernetes diagnostic was ignored"
fi
unset ATLAS_FAKE_STDERR_PATH

reads_before=$(wc -l < "$read_log" | tr -d ' ')
export ATLAS_FAKE_CA_DATA=$wrong_ca_data
if run_preflight "$target" "$gate" "$gate_sha" "$test_workspace/wrong-ca-evidence.json" \
  "$test_workspace/wrong-ca.stdout" "$test_workspace/wrong-ca.stderr"; then
  test::fail "wrong API Server CA produced READY Evidence"
fi
[[ $(wc -l < "$read_log" | tr -d ' ') -eq "$reads_before" ]] ||
  test::fail "wrong API Server CA reached a Kubernetes object read"

ATLAS_FAKE_CA_DATA='' run_preflight "$target" "$gate" "$gate_sha" \
  "$test_workspace/missing-ca-evidence.json" "$test_workspace/missing-ca.stdout" \
  "$test_workspace/missing-ca.stderr" && test::fail "missing API Server CA produced READY Evidence"
[[ $(wc -l < "$read_log" | tr -d ' ') -eq "$reads_before" ]] ||
  test::fail "missing API Server CA reached a Kubernetes object read"

export ATLAS_FAKE_CA_DATA=$ca_data
export ATLAS_FAKE_INSECURE_TLS=true
if run_preflight "$target" "$gate" "$gate_sha" "$test_workspace/insecure-evidence.json" \
  "$test_workspace/insecure.stdout" "$test_workspace/insecure.stderr"; then
  test::fail "insecure kubeconfig produced READY Evidence"
fi
unset ATLAS_FAKE_INSECURE_TLS
[[ $(wc -l < "$read_log" | tr -d ' ') -eq "$reads_before" ]] ||
  test::fail "insecure kubeconfig reached a Kubernetes object read"

if KUBECONFIG=/tmp/ambient "$preflight" validate --target "$target" --owner-gate "$gate" \
  --expected-owner-gate-sha "$gate_sha" --expected-commit "$expected_commit" \
  --evidence "$evidence" > /dev/null 2>&1; then
  test::fail "ambient KUBECONFIG was accepted"
fi

test::pass "PERSONAL_LOCAL authoritative preflight, Owner Gate, and Evidence Validator"
