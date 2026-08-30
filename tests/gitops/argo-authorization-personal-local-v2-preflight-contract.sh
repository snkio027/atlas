#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly materialization=$ATLAS_TEST_ROOT/$probe_root/personal-local-target-materialization
readonly target_builder=$ATLAS_TEST_ROOT/$probe_root/personal-local-target-v2
readonly preflight=$ATLAS_TEST_ROOT/$probe_root/personal-local-read-only-preflight
readonly fake_source=$ATLAS_TEST_ROOT/tests/gitops/fixtures/argo-authorization-probe/fake-personal-local-v2-kubectl
readonly renderer_source=$ATLAS_TEST_ROOT/tests/gitops/fixtures/argo-authorization-probe/fake-personal-local-v2-renderer-kubectl
readonly ln_source=$ATLAS_TEST_ROOT/tests/gitops/fixtures/argo-authorization-probe/fake-personal-local-v2-ln
readonly date_source=$ATLAS_TEST_ROOT/tests/gitops/fixtures/argo-authorization-probe/fake-personal-local-v2-date
readonly contract=$ATLAS_TEST_ROOT/$probe_root/probe-contract.json
readonly expected_commit=${1:-}
readonly waiver_sha=c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a
readonly plan_sha=b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc
readonly certificate_sentinel=SYNTHETIC_CLIENT_CERTIFICATE_SENTINEL_B3
readonly private_key_sentinel=SYNTHETIC_CLIENT_PRIVATE_KEY_SENTINEL_B3
real_date=$(command -v date)
readonly real_date
if command -v aqua > /dev/null 2>&1; then
  real_yq=$(aqua which yq)
  real_helm=$(aqua which helm)
  real_kubectl=$(aqua which kubectl)
else
  real_yq=$(command -v yq)
  real_helm=$(command -v helm)
  real_kubectl=$(command -v kubectl)
fi
readonly real_yq real_helm real_kubectl

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] || test::fail 'expected contract commit must be supplied'
[[ -x $materialization && -x $target_builder && -x $preflight && -x $fake_source && -x $renderer_source &&
  -x $ln_source && -x $date_source ]] ||
  test::fail 'B2/B3 executables or fake kubectl are unavailable'
[[ $real_yq == /* && -x $real_yq && $real_helm == /* && -x $real_helm ]] ||
  test::fail 'locked repository renderers are unavailable'
[[ $real_kubectl == /* && -x $real_kubectl ]] || test::fail 'locked renderer kubectl is unavailable'

workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-v2-preflight-contract.XXXXXX")
chmod 0700 "$workspace"
workspace=$(cd "$workspace" && pwd -P)
cleanup() {
  if [[ ${ATLAS_TEST_KEEP_TMP:-false} == true ]]; then
    printf 'retained synthetic workspace: %s\n' "$workspace" >&2
  else
    rm -rf "$workspace"
  fi
}
trap cleanup EXIT

canonical_sha() {
  local projection
  projection=$(yq -o=json -I=0 'sort_keys(..)' "$1") || return 1
  printf '%s' "$projection" | shasum -a 256 | awk '{print $1}'
}

timestamp_offset() {
  local epoch
  epoch=$(($(date -u +%s) + $1))
  if date -u -r "$epoch" '+%Y-%m-%dT%H:%M:%SZ' 2> /dev/null; then return 0; fi
  date -u -d "@${epoch}" '+%Y-%m-%dT%H:%M:%SZ'
}

timestamp_epoch() {
  local epoch=$1
  if date -u -r "$epoch" '+%Y-%m-%dT%H:%M:%SZ' 2> /dev/null; then return 0; fi
  date -u -d "@${epoch}" '+%Y-%m-%dT%H:%M:%SZ'
}

make_gate_variant() {
  local source=$1 destination=$2 issued_epoch=$3 expires_epoch=$4 decision=${5:-APPROVED} projection
  projection=$(ISSUED=$(timestamp_epoch "$issued_epoch") EXPIRES=$(timestamp_epoch "$expires_epoch") DECISION=$decision \
    yq -o=json -I=0 \
    '.issuedAt = strenv(ISSUED) | .expiresAt = strenv(EXPIRES) | .decision = strenv(DECISION) | sort_keys(..)' \
    "$source") || return 1
  printf '%s' "$projection" > "$destination"
  chmod 0600 "$destination"
}

rewrite_json() {
  local file=$1 expression=$2
  local temporary=${file}.rewrite
  yq -o=json -I=0 "$expression | sort_keys(..)" "$file" > "$temporary"
  chmod 0600 "$temporary"
  mv "$temporary" "$file"
}

write_yq_wrapper() {
  local destination=$1 tool=$2 version=$3
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    "if [[ \${1:-} == --version ]]; then printf '%s\\n' 'yq (https://github.com/mikefarah/yq/) version v${version}'; exit 0; fi" \
    "exec \"${tool}\" \"\$@\"" > "$destination"
  chmod 0700 "$destination"
}

write_helm_wrapper() {
  local destination=$1 tool=$2 version=$3
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    "if [[ \${1:-} == version && \${2:-} == --template ]]; then printf '%s' 'v${version}'; exit 0; fi" \
    "exec \"${tool}\" \"\$@\"" > "$destination"
  chmod 0700 "$destination"
}

write_second_version_drift_yq_wrapper() {
  local destination=$1 tool=$2 state_file=$3
  # shellcheck disable=SC2016 # The generated wrapper expands its own positional parameters.
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    'if [[ ${1:-} == --version ]]; then' \
    "  count=0; [[ ! -f \"${state_file}\" ]] || count=\$(< \"${state_file}\")" \
    "  count=\$((count + 1)); printf '%s' \"\$count\" > \"${state_file}\"" \
    "  version=4.53.3; ((count < 2)) || version=4.53.2" \
    "  printf 'yq (https://github.com/mikefarah/yq/) version v%s\\n' \"\$version\"; exit 0" \
    'fi' \
    "exec \"${tool}\" \"\$@\"" > "$destination"
  chmod 0700 "$destination"
}

raw_path() {
  local api=$1 kind=$2 namespace=$3 name=$4
  case "${api}|${kind}|${namespace}" in
    'v1|Namespace|') printf '/api/v1/namespaces/%s' "$name" ;;
    'v1|ConfigMap|argocd') printf '/api/v1/namespaces/argocd/configmaps/%s' "$name" ;;
    'argoproj.io/v1alpha1|Application|argocd') printf '/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/%s' "$name" ;;
    'argoproj.io/v1alpha1|AppProject|argocd') printf '/apis/argoproj.io/v1alpha1/namespaces/argocd/appprojects/%s' "$name" ;;
    'apps/v1|Deployment|argocd') printf '/apis/apps/v1/namespaces/argocd/deployments/%s' "$name" ;;
    'apps/v1|StatefulSet|argocd') printf '/apis/apps/v1/namespaces/argocd/statefulsets/%s' "$name" ;;
    *) return 1 ;;
  esac
}

hydrate_fake_objects() {
  local source_root=${workspace}/source combined=${workspace}/combined.yaml object api kind namespace name count projection path file
  mkdir -m 0700 "$source_root" "$object_dir"
  git archive "$expected_commit" | tar -x -C "$source_root"
  (
    cd "$source_root"
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
      "$real_helm" template atlas-argocd vendor/charts/argo-cd-10.3.3 --namespace argocd --include-crds \
        --values gitops/platform/management/argocd-self/values.yaml \
        --values gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-values-hardening.yaml
    }
  ) > "$combined"
  : > "$object_map"
  while IFS= read -r object; do
    api=$(yq -r '.apiVersion' <<< "$object")
    kind=$(yq -r '.kind' <<< "$object")
    namespace=$(yq -r '.namespace' <<< "$object")
    name=$(yq -r '.name' <<< "$object")
    count=$(API=$api KIND=$kind NS=$namespace NAME=$name yq ea \
      '[select(.apiVersion == strenv(API) and .kind == strenv(KIND) and
      (.metadata.namespace // "") == strenv(NS) and .metadata.name == strenv(NAME))] | length' "$combined" | tail -1)
    [[ $count -eq 1 ]] || test::fail "desired object is not unique: ${kind}/${name}"
    projection=$(API=$api KIND=$kind NS=$namespace NAME=$name yq ea -o=json -I=0 \
      'select(.apiVersion == strenv(API) and .kind == strenv(KIND) and
      (.metadata.namespace // "") == strenv(NS) and .metadata.name == strenv(NAME)) |
      del(.metadata.creationTimestamp,.metadata.resourceVersion,.metadata.uid,
      .metadata.generation,.metadata.managedFields,.status) | sort_keys(..)' "$combined")
    if [[ $kind == Namespace && $name == kube-system ]]; then
      projection=$(UID_VALUE=12345678-1234-1234-1234-123456789abc yq -o=json -I=0 \
        '.metadata.uid = strenv(UID_VALUE) | sort_keys(..)' <<< "$projection")
    fi
    path=$(raw_path "$api" "$kind" "$namespace" "$name")
    file=${object_dir}/$(printf '%s' "$path" | shasum -a 256 | awk '{print $1}').json
    printf '%s' "$projection" > "$file"
    chmod 0600 "$file"
    printf '%s\t%s\n' "$path" "$file" >> "$object_map"
  done < <(yq -o=json -I=0 '.kubernetesReadContract.objects | sort_by(.apiVersion,.kind,.namespace,.name)[]' "$contract")
}

assert_no_credential_escape() {
  local file
  while IFS= read -r file; do
    [[ $file == "$kubeconfig" || $file == "${kubeconfig}.unavailable" ]] && continue
    [[ $file == "${workspace}/source/"* ]] && continue
    if rg -F -e "$certificate_sentinel" -e "$private_key_sentinel" "$file" > /dev/null 2>&1; then
      test::fail "synthetic credential escaped kubeconfig bytes: ${file}"
    fi
  done < <(find "$workspace" -type f -print)
}

run_blocked() {
  local name=$1
  shift
  : > "$stdout"
  : > "$stderr"
  if "$@" > "$stdout" 2> "$stderr"; then
    test::fail "blocked operation unexpectedly succeeded: ${name}"
  else
    status=$?
  fi
  [[ $status -eq 24 && ! -s $stdout && $(< "$stderr") == PERSONAL_LOCAL_BLOCKED:* ]] ||
    test::fail "blocked operation returned invalid classification: ${name}"
}

validate_final_gate_direct() {
  local gate=$1 gate_sha=$2 pre_target=$3 evidence_sha=$4 require_current=$5
  # shellcheck disable=SC2016 # The isolated child receives values as positional parameters.
  env -i PATH="${tool_bin}:${renderer_bin}:${bin}:$PATH" TMPDIR="$tmp" LC_ALL=C \
    bash -c '
      set -Eeuo pipefail
      source "$1"
      target_v2_tmp=$(mktemp -d "${TMPDIR}/atlas-target-v2-gate-test.XXXXXX")
      chmod 0700 "$target_v2_tmp"
      target_v2::_validate_final_gate "$2" "$3" "$4" "$5" "$6" "$7" "$8"
    ' _ "$target_builder" "$gate" "$gate_sha" "$pre_target" "$materialization_evidence" \
    "$evidence_sha" "$expected_commit" "$require_current"
}

assert_kubeconfig_projection_rejected() {
  local name=$1 candidate=$2
  # shellcheck disable=SC2016 # The isolated child receives values as positional parameters.
  if env -i PATH="${tool_bin}:$PATH" TMPDIR="$tmp" LC_ALL=C \
    bash -c '
      set -Eeuo pipefail
      source "$1"
      preflight_v2_tmp=$(mktemp -d "${TMPDIR}/atlas-preflight-v2-kubeconfig-test.XXXXXX")
      chmod 0700 "$preflight_v2_tmp"
      preflight_v2::_project_kubeconfig "$2" kind-atlas-synthetic
    ' _ "$preflight" "$candidate" > "$stdout" 2> "$stderr"; then
    test::fail "unsafe kubeconfig projection was accepted: ${name}"
  fi
}

ca_key=${workspace}/ca.key
ca_cert=${workspace}/ca.crt
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=synthetic-atlas-b3-ca' \
  -keyout "$ca_key" -out "$ca_cert" > /dev/null 2>&1
chmod 0600 "$ca_key" "$ca_cert"
ca_data=$(openssl base64 -A -in "$ca_cert")

bin=${workspace}/bin
renderer_bin=${workspace}/renderer-bin
tool_bin=${workspace}/tool-bin
fault_bin=${workspace}/fault-bin
date_bin=${workspace}/date-bin
wrong_yq_bin=${workspace}/wrong-yq-bin
wrong_helm_bin=${workspace}/wrong-helm-bin
second_yq_bin=${workspace}/second-yq-bin
tmp=${workspace}/tmp
receipts=${workspace}/receipts
output_dir=${workspace}/output
state_dir=${workspace}/fake-state
object_dir=${workspace}/objects
mkdir -m 0700 "$bin" "$renderer_bin" "$tool_bin" "$fault_bin" "$date_bin" "$wrong_yq_bin" "$wrong_helm_bin" "$second_yq_bin" \
  "$tmp" "$receipts" "$output_dir" "$state_dir"
kubeconfig=${workspace}/kubeconfig
kubectl=${bin}/kubectl
renderer_kubectl=${renderer_bin}/kubectl
fault_ln=${fault_bin}/ln
fake_date=${date_bin}/date
object_map=${workspace}/object-map.tsv
log=${workspace}/fake.log
stdout=${workspace}/stdout
stderr=${workspace}/stderr
cp "$fake_source" "$kubectl"
chmod 0700 "$kubectl"
cp "$renderer_source" "$renderer_kubectl"
chmod 0700 "$renderer_kubectl"
write_yq_wrapper "$tool_bin/yq" "$real_yq" 4.53.3
write_helm_wrapper "$tool_bin/helm" "$real_helm" 4.2.3
write_yq_wrapper "$wrong_yq_bin/yq" "$real_yq" 4.53.2
write_helm_wrapper "$wrong_helm_bin/helm" "$real_helm" 4.2.2
second_yq_state=${workspace}/second-yq-state
write_second_version_drift_yq_wrapper "$second_yq_bin/yq" "$real_yq" "$second_yq_state"
cp "$ln_source" "$fault_ln"
chmod 0700 "$fault_ln"
cp "$date_source" "$fake_date"
chmod 0700 "$fake_date"
: > "$log"
chmod 0600 "$log"

CA_DATA=$ca_data CERT=$certificate_sentinel KEY=$private_key_sentinel yq -n -o=json -I=0 \
  '{"apiVersion":"v1","kind":"Config","clusters":[{"name":"synthetic-cluster","cluster":
  {"server":"https://127.0.0.1:6443","certificate-authority-data":strenv(CA_DATA)}}],
  "contexts":[{"name":"kind-atlas-synthetic","context":{"cluster":"synthetic-cluster","user":"synthetic-owner"}}],
  "current-context":"kind-atlas-synthetic","users":[{"name":"synthetic-owner","user":
  {"client-certificate-data":strenv(CERT),"client-key-data":strenv(KEY)}}]} | sort_keys(..)' > "$kubeconfig"
chmod 0600 "$kubeconfig"
hydrate_fake_objects
[[ $(canonical_sha "$probe_root/personal-local-profile-v2.json") == "$waiver_sha" &&
$(canonical_sha "$probe_root/personal-local-target-materialization-plan.json") == "$plan_sha" ]] ||
  test::fail 'frozen B1 authority hashes drifted'

base_env=(
  "PATH=${tool_bin}:${renderer_bin}:${bin}:$PATH"
  "TMPDIR=${tmp}" "LC_ALL=C" "ATLAS_REAL_KUBECTL=${real_kubectl}"
  "ATLAS_FAKE_V2_LOG=${log}" "ATLAS_FAKE_V2_STATE_DIR=${state_dir}"
  "ATLAS_FAKE_V2_OBJECT_MAP=${object_map}" "ATLAS_FAKE_V2_KUBECONFIG=${kubeconfig}"
  "ATLAS_FAKE_V2_CONTEXT=kind-atlas-synthetic"
)

# Complete synthetic B2 materialization.
materialization_gate=${workspace}/materialization-gate.json
materialization_evidence=${output_dir}/materialization-evidence.json
materialization_session=personal-local-materialization-20260831T000000Z-11111111111111111111111111111111
materialization_gate_projection=$(MG_ISSUED=$(timestamp_offset -60) MG_EXPIRES=$(timestamp_offset 1200) COMMIT=$expected_commit \
RECEIPTS=$receipts SESSION=$materialization_session KUBECONFIG_PATH=$kubeconfig KUBECTL_PATH=$kubectl \
  yq -n -o=json -I=0 \
  '{"schemaVersion":1,"gateID":"atlas.argocd.authorization-personal-local-target-materialization-owner-gate/v1",
  "operation":"PERSONAL_LOCAL_TARGET_MATERIALIZATION","decision":"APPROVED","rolloutProfile":"PERSONAL_LOCAL",
  "profileID":"atlas.argocd.authorization-probe-profile/personal-local/v2","contractGitCommit":strenv(COMMIT),
  "authorityBaseline":"165fb2a31068e3de2ac1064dbf8f95966ff8aad1","repositoryURL":"https://github.com/snkio027/atlas.git",
  "waiverDecisionSHA256":"c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a",
  "materializationPlanID":"atlas.argocd.authorization-personal-local-target-materialization-plan/v1",
  "materializationPlanSHA256":"b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc",
  "materializationEvidenceSchemaID":"atlas.argocd.authorization-personal-local-target-materialization-evidence/v1",
  "environmentName":"test","clusterName":"atlas-synthetic","kubeContext":"kind-atlas-synthetic",
  "kubeconfigPath":strenv(KUBECONFIG_PATH),"kubectlPath":strenv(KUBECTL_PATH),"kubectlVersion":"1.36.3",
  "kubernetesVersion":"1.36.1","sessionReceiptRoot":strenv(RECEIPTS),"sessionID":strenv(SESSION),
  "issuedAt":strenv(MG_ISSUED),"expiresAt":strenv(MG_EXPIRES)} | sort_keys(..)')
printf '%s' "$materialization_gate_projection" > "$materialization_gate"
chmod 0600 "$materialization_gate"
materialization_gate_sha=$(canonical_sha "$materialization_gate")
if env -i "${base_env[@]}" "$materialization" run --owner-gate "$materialization_gate" \
  --expected-owner-gate-sha "$materialization_gate_sha" --expected-commit "$expected_commit" \
  --output "$materialization_evidence" > "$stdout" 2> "$stderr"; then
  materialization_status=0
else
  materialization_status=$?
fi
if ((materialization_status != 0)); then
  printf 'synthetic B2 stdout=%s\nsynthetic B2 stderr=%s\n' "$(< "$stdout")" "$(< "$stderr")" >&2
  test::fail 'synthetic B2 materialization failed'
fi
[[ $(< "$stdout") == TARGET_MATERIALIZED && ! -s $stderr ]] || test::fail 'B2 success output drifted'

# The Git-to-Desired renderer TCB is exact and fails before target authority access.
for renderer_case in yq helm kubectl; do
  : > "$log"
  case $renderer_case in
    yq)
      run_blocked renderer-yq env -i "${base_env[@]}" \
        PATH="${wrong_yq_bin}:${tool_bin}:${renderer_bin}:${bin}:$PATH" "$target_builder" project \
        --materialization-owner-gate "$materialization_gate" \
        --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
        --materialization-evidence "$materialization_evidence" --expected-commit "$expected_commit"
      ;;
    helm)
      run_blocked renderer-helm env -i "${base_env[@]}" \
        PATH="${wrong_helm_bin}:${tool_bin}:${renderer_bin}:${bin}:$PATH" "$target_builder" project \
        --materialization-owner-gate "$materialization_gate" \
        --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
        --materialization-evidence "$materialization_evidence" --expected-commit "$expected_commit"
      ;;
    kubectl)
      run_blocked renderer-kubectl env -i "${base_env[@]}" ATLAS_FAKE_V2_RENDERER_VERSION=1.36.2 \
        "$target_builder" project --materialization-owner-gate "$materialization_gate" \
        --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
        --materialization-evidence "$materialization_evidence" --expected-commit "$expected_commit"
      ;;
  esac
  [[ ! -s $log ]] || test::fail "renderer drift accessed the target: ${renderer_case}"
done

# Offline project, independently constructed Final Gate, and sealed Final Target.
project=${workspace}/project.json
env -i "${base_env[@]}" "$target_builder" project --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" --materialization-evidence "$materialization_evidence" \
  --expected-commit "$expected_commit" > "$project" 2> "$stderr" || test::fail 'B3 Target projection failed'
[[ ! -s $stderr && $(yq -r '.artifactClass' "$project") == REVIEW_AID_ONLY ]] || test::fail 'B3 review aid drifted'
pre_target=${workspace}/pre-target.json
yq -o=json -I=0 '.targetPreGateProjection | sort_keys(..)' "$project" > "$pre_target"
chmod 0600 "$pre_target"
materialization_evidence_sha=$(canonical_sha "$materialization_evidence")
materialization_completed_epoch=$(yq -r '.completedAt | to_unix' "$materialization_evidence")

final_gate=${workspace}/final-gate.json
final_session=personal-local-preflight-20260831T000000Z-22222222222222222222222222222222
# shellcheck disable=SC2016 # yq reads the exported strenv/load values.
final_gate_projection=$(FG_ISSUED=$(timestamp_offset -1) FG_EXPIRES=$(timestamp_offset 900) SESSION=$final_session COMMIT=$expected_commit PROJECT=$project \
KUBE_CONTEXT=kind-atlas-synthetic KUBECONFIG_SHA=$(shasum -a 256 "$kubeconfig" | awk '{print $1}') \
CA_SHA=$(openssl x509 -in "$ca_cert" -pubkey -noout | openssl pkey -pubin -outform DER | shasum -a 256 | awk '{print $1}') \
  yq -n -o=json -I=0 \
  'load(strenv(PROJECT)) as $p | {"schemaVersion":2,
  "gateID":"atlas.argocd.authorization-personal-local-owner-gate/v2",
  "operation":"PERSONAL_LOCAL_READ_ONLY_PREFLIGHT","decision":"APPROVED","rolloutProfile":"PERSONAL_LOCAL",
  "profileID":"atlas.argocd.authorization-probe-profile/personal-local/v2","contractGitCommit":strenv(COMMIT),
  "authorityBaseline":"165fb2a31068e3de2ac1064dbf8f95966ff8aad1","repositoryURL":"https://github.com/snkio027/atlas.git",
  "waiverDecisionSHA256":"c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a",
  "targetMaterializationEvidenceSHA256":$p.targetMaterializationEvidenceSHA256,
  "ownerGateTargetProjectionSHA256":$p.ownerGateTargetProjectionSHA256,
  "desiredProjectionSHA256":$p.desiredProjectionSHA256,"readPlanSHA256":$p.readPlanSHA256,
  "readObjectCount":13,"snapshotCount":2,"kubeContext":strenv(KUBE_CONTEXT),
  "kubeconfigSHA256":strenv(KUBECONFIG_SHA),"apiServerCASPKISHA256":strenv(CA_SHA),
  "sessionID":strenv(SESSION),"issuedAt":strenv(FG_ISSUED),"expiresAt":strenv(FG_EXPIRES)} | sort_keys(..)')
printf '%s' "$final_gate_projection" > "$final_gate"
chmod 0600 "$final_gate"
final_gate_sha=$(canonical_sha "$final_gate")

# Final Gate freshness is exactly inclusive 0..900 seconds from Materialization completion.
for delta in 0 900; do
  gate_variant=${workspace}/gate-freshness-${delta}.json
  issued_epoch=$((materialization_completed_epoch + delta))
  make_gate_variant "$final_gate" "$gate_variant" "$issued_epoch" $((issued_epoch + 3600))
  validate_final_gate_direct "$gate_variant" "$(canonical_sha "$gate_variant")" "$pre_target" \
    "$materialization_evidence_sha" false || test::fail "valid Final Gate freshness was rejected: ${delta}"
done
for delta in -1 901; do
  gate_variant=${workspace}/gate-freshness-${delta}.json
  issued_epoch=$((materialization_completed_epoch + delta))
  make_gate_variant "$final_gate" "$gate_variant" "$issued_epoch" $((issued_epoch + 3600))
  if validate_final_gate_direct "$gate_variant" "$(canonical_sha "$gate_variant")" "$pre_target" \
    "$materialization_evidence_sha" false; then
    test::fail "invalid Final Gate freshness was accepted: ${delta}"
  fi
done
not_authorized_gate=${workspace}/gate-not-authorized.json
make_gate_variant "$final_gate" "$not_authorized_gate" "$(date -u +%s)" "$(($(date -u +%s) + 900))" NOT_AUTHORIZED
if validate_final_gate_direct "$not_authorized_gate" "$(canonical_sha "$not_authorized_gate")" "$pre_target" \
  "$materialization_evidence_sha" false; then
  test::fail 'NOT_AUTHORIZED Final Gate was accepted'
fi
if validate_final_gate_direct "$final_gate" 0000000000000000000000000000000000000000000000000000000000000000 \
  "$pre_target" "$materialization_evidence_sha" false; then
  test::fail 'wrong independently supplied Final Gate SHA was accepted'
fi

target=${workspace}/target.json
env -i "${base_env[@]}" "$target_builder" seal --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" --materialization-evidence "$materialization_evidence" \
  --final-owner-gate "$final_gate" --expected-final-owner-gate-sha "$final_gate_sha" \
  --expected-commit "$expected_commit" --output "$target" > "$stdout" 2> "$stderr" || test::fail 'B3 Target sealing failed'
[[ $(< "$stdout") == TARGET_SEALED && ! -s $stderr ]] || test::fail 'B3 Target success output drifted'

# Final preflight independently revalidates the renderer TCB after Target sealing.
: > "$log"
rm -f "$second_yq_state"
run_blocked renderer-revalidation env -i "${base_env[@]}" \
  PATH="${second_yq_bin}:${tool_bin}:${renderer_bin}:${bin}:$PATH" \
  "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-renderer-revalidation.json"
[[ ! -s $log && $(< "$second_yq_state") -eq 2 ]] ||
  test::fail 'Final preflight did not independently revalidate the renderer TCB'

# Expired and future-issued Gates seal historically but cannot authorize a live request.
for gate_time_case in expired future; do
  gate_variant=${workspace}/gate-${gate_time_case}.json
  target_variant=${workspace}/target-${gate_time_case}.json
  case $gate_time_case in
    expired)
      issued_epoch=$materialization_completed_epoch
      expires_epoch=$((materialization_completed_epoch + 1))
      ;;
    future)
      issued_epoch=$((materialization_completed_epoch + 900))
      expires_epoch=$((issued_epoch + 3600))
      ;;
  esac
  make_gate_variant "$final_gate" "$gate_variant" "$issued_epoch" "$expires_epoch"
  gate_variant_sha=$(canonical_sha "$gate_variant")
  env -i "${base_env[@]}" "$target_builder" seal --materialization-owner-gate "$materialization_gate" \
    --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
    --materialization-evidence "$materialization_evidence" --final-owner-gate "$gate_variant" \
    --expected-final-owner-gate-sha "$gate_variant_sha" --expected-commit "$expected_commit" \
    --output "$target_variant" > "$stdout" 2> "$stderr" || test::fail "${gate_time_case} Gate Target sealing failed"
  : > "$log"
  run_blocked "gate-${gate_time_case}" env -i "${base_env[@]}" "$preflight" run --target "$target_variant" \
    --materialization-owner-gate "$materialization_gate" \
    --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
    --materialization-evidence "$materialization_evidence" --final-owner-gate "$gate_variant" \
    --expected-final-owner-gate-sha "$gate_variant_sha" --expected-commit "$expected_commit" \
    --output "${output_dir}/blocked-${gate_time_case}.json"
  [[ ! -s $log && ! -e ${output_dir}/blocked-${gate_time_case}.json ]] ||
    test::fail "${gate_time_case} Gate accessed the target or published Evidence"
done

# Complete fake 27-request read-only preflight.
evidence=${output_dir}/preflight-evidence.json
: > "$log"
rm -f "$state_dir"/*
sleep 1
env -i "${base_env[@]}" "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" --materialization-evidence "$materialization_evidence" \
  --final-owner-gate "$final_gate" --expected-final-owner-gate-sha "$final_gate_sha" \
  --expected-commit "$expected_commit" --output "$evidence" > "$stdout" 2> "$stderr" || {
  printf 'stdout=%s\nstderr=%s\n' "$(< "$stdout")" "$(< "$stderr")" >&2
  test::fail 'synthetic B3 preflight failed'
}
[[ $(< "$stdout") == PERSONAL_LOCAL_READY && ! -s $stderr && $(yq -r '.result' "$evidence") == PERSONAL_LOCAL_READY ]] ||
  test::fail 'B3 success output or Evidence drifted'
[[ $(grep -c '^RAW:/version:' "$log") -eq 1 && $(grep -c '^RAW:/api/v1/namespaces/kube-system:' "$log") -eq 2 &&
$(grep -c '^RAW:' "$log") -eq 27 && $(grep -c '^CLIENT_VERSION$' "$log") -eq 1 &&
$(yq -r '.completeness.executedReads' "$evidence") -eq 26 ]] ||
  test::fail 'B2/B3 exact request count drifted'
while IFS=$'\t' read -r path file; do
  expected=2
  [[ $(grep -F -c "RAW:${path}:" "$log") -eq $expected ]] || test::fail "read count drifted: ${path}"
done < "$object_map"
assert_no_credential_escape

# Gate expiry between approved reads prevents every subsequent Kubernetes request.
: > "$log"
rm -f "$state_dir"/*
gate_expired_epoch=$(($(yq -r '.expiresAt | to_unix' "$final_gate") + 1))
run_blocked gate-expired-between-reads env -i "${base_env[@]}" \
  PATH="${date_bin}:${tool_bin}:${renderer_bin}:${bin}:$PATH" ATLAS_REAL_DATE="$real_date" \
  ATLAS_FAKE_V2_DATE_LOG="$log" ATLAS_FAKE_V2_DATE_EXPIRE_AFTER_RAW=2 \
  ATLAS_FAKE_V2_DATE_EXPIRED_EPOCH="$gate_expired_epoch" \
  "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-mid-expiry.json"
[[ $(grep -c '^RAW:' "$log") -eq 2 && ! -e ${output_dir}/blocked-mid-expiry.json ]] ||
  test::fail 'Gate expiry did not stop before the next approved read'

# B2 provenance remains mandatory and fails before any B3 target request.
materialization_claim=${receipts}/${materialization_session}/claim.json
materialization_terminal=${receipts}/${materialization_session}/terminal.json
: > "$log"
run_blocked materialization-gate-sha env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha 0000000000000000000000000000000000000000000000000000000000000000 \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-materialization-gate.json"
[[ ! -s $log ]] || test::fail 'Materialization Gate failure invoked target kubectl'

mv "$materialization_claim" "${materialization_claim}.unavailable"
: > "$log"
run_blocked missing-materialization-claim env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-materialization-claim.json"
[[ ! -s $log ]] || test::fail 'missing Materialization Claim invoked target kubectl'
mv "${materialization_claim}.unavailable" "$materialization_claim"

cp "$materialization_terminal" "${materialization_terminal}.valid"
rewrite_json "$materialization_terminal" \
  '.materializationEvidenceSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"'
: > "$log"
run_blocked materialization-terminal env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-materialization-terminal.json"
[[ ! -s $log ]] || test::fail 'Materialization terminal failure invoked target kubectl'
mv "${materialization_terminal}.valid" "$materialization_terminal"

# Offline validation remains possible after target authority files disappear.
mv "$kubeconfig" "${kubeconfig}.unavailable"
mv "$kubectl" "${kubectl}.unavailable"
offline_log_sha=$(shasum -a 256 "$log" | awk '{print $1}')
env -i PATH="${tool_bin}:${renderer_bin}:$PATH" TMPDIR="$tmp" LC_ALL=C ATLAS_REAL_KUBECTL="$real_kubectl" \
  "$preflight" validate --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" --evidence "$evidence" \
  > "$stdout" 2> "$stderr" || test::fail 'offline B3 Evidence validation required target authority'
[[ $(< "$stdout") == PERSONAL_LOCAL_READY && ! -s $stderr && $(shasum -a 256 "$log" | awk '{print $1}') == "$offline_log_sha" ]] ||
  test::fail 'offline B3 validator invoked target authority'
mv "${kubeconfig}.unavailable" "$kubeconfig"
mv "${kubectl}.unavailable" "$kubectl"

# Provenance and Final Target changes fail before a target request.
cp "$final_gate" "${final_gate}.tampered"
rewrite_json "${final_gate}.tampered" '.readPlanSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"'
chmod 0600 "${final_gate}.tampered"
: > "$log"
run_blocked final-gate-provenance env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "${final_gate}.tampered" \
  --expected-final-owner-gate-sha "$(canonical_sha "${final_gate}.tampered")" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-provenance.json"
[[ ! -s $log ]] || test::fail 'pre-live provenance failure invoked target kubectl'

cp "$target" "${target}.tampered"
rewrite_json "${target}.tampered" '.desiredObjects[0].name = "replaced"'
chmod 0600 "${target}.tampered"
: > "$log"
run_blocked target-tamper env -i "${base_env[@]}" "$preflight" run --target "${target}.tampered" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-target.json"
[[ ! -s $log ]] || test::fail 'Final Target failure invoked target kubectl'

for specification in \
  'gate-schema-version-type|.schemaVersion = "2"' \
  'gate-read-count-type|.readObjectCount = "13"'; do
  name=${specification%%|*}
  expression=${specification#*|}
  tampered=${workspace}/${name}.json
  cp "$final_gate" "$tampered"
  rewrite_json "$tampered" "$expression"
  chmod 0600 "$tampered"
  : > "$log"
  run_blocked "$name" env -i "${base_env[@]}" "$target_builder" seal \
    --materialization-owner-gate "$materialization_gate" \
    --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
    --materialization-evidence "$materialization_evidence" --final-owner-gate "$tampered" \
    --expected-final-owner-gate-sha "$(canonical_sha "$tampered")" --expected-commit "$expected_commit" \
    --output "${output_dir}/${name}.json"
  [[ ! -s $log ]] || test::fail "Schema-invalid Final Gate invoked target kubectl: ${name}"
done

# Local authority drift fails before the first live request.
chmod 0644 "$kubeconfig"
: > "$log"
run_blocked kubeconfig-mode env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-local.json"
[[ ! -s $log ]] || test::fail 'local custody failure invoked target kubectl'
chmod 0600 "$kubeconfig"

for kubeconfig_case in proxy-url insecure-tls credential-exec credential-token; do
  candidate=${workspace}/kubeconfig-${kubeconfig_case}.json
  cp "$kubeconfig" "$candidate"
  case $kubeconfig_case in
    proxy-url) rewrite_json "$candidate" '.clusters[0].cluster."proxy-url" = "https://proxy.invalid"' ;;
    insecure-tls) rewrite_json "$candidate" '.clusters[0].cluster."insecure-skip-tls-verify" = true' ;;
    credential-exec) rewrite_json "$candidate" '.users[0].user.exec = {"apiVersion":"client.authentication.k8s.io/v1","command":"forbidden"}' ;;
    credential-token) rewrite_json "$candidate" '.users[0].user.token = "forbidden"' ;;
  esac
  assert_kubeconfig_projection_rejected "$kubeconfig_case" "$candidate"
  rm -f "$candidate"
done

cp "$kubeconfig" "${kubeconfig}.valid"
printf '\n' >> "$kubeconfig"
: > "$log"
run_blocked kubeconfig-hash env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-kubeconfig-hash.json"
[[ ! -s $log ]] || test::fail 'wrong kubeconfig hash invoked target kubectl'
mv "${kubeconfig}.valid" "$kubeconfig"

cp "$kubectl" "${kubectl}.valid"
printf '\n# hash drift\n' >> "$kubectl"
: > "$log"
run_blocked kubectl-hash env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-kubectl-hash.json"
[[ ! -s $log ]] || test::fail 'wrong kubectl hash invoked target kubectl'
mv "${kubectl}.valid" "$kubectl"

# The materialized kube-system UID is proven independently in both exact snapshots.
namespace_path=/api/v1/namespaces/kube-system
for uid_phase in before after; do
  : > "$log"
  rm -f "$state_dir"/*
  uid_count=1
  [[ $uid_phase == after ]] && uid_count=2
  run_blocked "uid-${uid_phase}" env -i "${base_env[@]}" \
    ATLAS_FAKE_V2_WRONG_UID_AT="${namespace_path}:${uid_count}" \
    "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
    --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
    --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
    --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
    --output "${output_dir}/blocked-uid-${uid_phase}.json"
  [[ $(grep -F -c "RAW:${namespace_path}:" "$log") -eq $uid_count &&
  ! -e ${output_dir}/blocked-uid-${uid_phase}.json ]] ||
    test::fail "kube-system UID ${uid_phase} proof used extra reads or published Evidence"
done

# Version diagnostics and live desired drift fail closed without diagnostic reads.
: > "$log"
run_blocked version-drift env -i "${base_env[@]}" ATLAS_FAKE_V2_SERVER_VERSION=v1.36.2 "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-version.json"
[[ $(grep -c '^RAW:' "$log") -eq 1 ]] || test::fail 'version drift triggered diagnostic reads'

first_object=$(awk -F '\t' '$1 != "/api/v1/namespaces/kube-system" {print $2; exit}' "$object_map")
cp "$first_object" "${first_object}.original"
rewrite_json "$first_object" '.metadata.name = "wrong-name"'
: > "$log"
run_blocked object-identity env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-object.json"
[[ $(grep -c '^RAW:' "$log") -le 27 ]] || test::fail 'object failure exceeded exact request bound'
mv "${first_object}.original" "$first_object"

application_path=/apis/argoproj.io/v1alpha1/namespaces/argocd/applications/argocd-self
application_object=$(awk -F '\t' -v path="$application_path" '$1 == path {print $2}' "$object_map")
cp "$application_object" "${application_object}.original"
rewrite_json "$application_object" '.spec.project = "wrong-project"'
: > "$log"
rm -f "$state_dir"/*
run_blocked desired-field-drift env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-desired-field.json"
[[ ! -e ${output_dir}/blocked-desired-field.json && $(grep -c '^RAW:' "$log") -le 14 ]] ||
  test::fail 'desired-field drift did not fail within the first snapshot'
mv "${application_object}.original" "$application_object"

[[ $(yq '.spec.syncPolicy.syncOptions | length' "$application_object") -gt 1 ]] ||
  test::fail 'array-order fixture is unavailable'
cp "$application_object" "${application_object}.original"
rewrite_json "$application_object" '.spec.syncPolicy.syncOptions |= reverse'
: > "$log"
rm -f "$state_dir"/*
run_blocked array-order-drift env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-array-order.json"
[[ ! -e ${output_dir}/blocked-array-order.json && $(grep -c '^RAW:' "$log") -le 14 ]] ||
  test::fail 'array-order drift did not fail within the first snapshot'
mv "${application_object}.original" "$application_object"

replacement=${workspace}/after-drift-replacement.json
first_path=$application_path
cp "$application_object" "$replacement"
rewrite_json "$replacement" '.spec.project = "wrong-project"'
: > "$log"
rm -f "$state_dir"/*
run_blocked before-after-drift env -i "${base_env[@]}" \
  ATLAS_FAKE_V2_REPLACEMENT_AT="${first_path}:2" ATLAS_FAKE_V2_REPLACEMENT_FILE="$replacement" \
  "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-before-after.json"
[[ ! -e ${output_dir}/blocked-before-after.json && $(grep -F -c "RAW:${first_path}:" "$log") -eq 2 &&
$(grep -c '^RAW:' "$log") -le 27 ]] || test::fail 'BEFORE/AFTER drift did not fail closed'

# Post-read filesystem TOCTOU cannot publish READY Evidence.
last_path=$(tail -1 "$object_map" | cut -f1)
cp "$kubeconfig" "${kubeconfig}.valid"
: > "$log"
rm -f "$state_dir"/*
run_blocked kubeconfig-toctou env -i "${base_env[@]}" ATLAS_FAKE_V2_DRIFT_AFTER_REQUEST="${last_path}:2" \
  "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" --materialization-evidence "$materialization_evidence" \
  --final-owner-gate "$final_gate" --expected-final-owner-gate-sha "$final_gate_sha" \
  --expected-commit "$expected_commit" --output "${output_dir}/blocked-toctou.json"
[[ ! -e ${output_dir}/blocked-toctou.json && $(grep -c '^RAW:' "$log") -eq 27 ]] ||
  test::fail 'TOCTOU failure published Evidence or added a request'
mv "${kubeconfig}.valid" "$kubeconfig"

cp "$kubectl" "${kubectl}.valid"
: > "$log"
rm -f "$state_dir"/*
run_blocked kubectl-toctou env -i "${base_env[@]}" \
  ATLAS_FAKE_V2_DRIFT_KUBECTL_AFTER_REQUEST="${last_path}:2" \
  "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" \
  --output "${output_dir}/blocked-kubectl-toctou.json"
[[ ! -e ${output_dir}/blocked-kubectl-toctou.json && $(grep -c '^RAW:' "$log") -eq 27 ]] ||
  test::fail 'kubectl TOCTOU failure published Evidence or changed request surface'
mv "${kubectl}.valid" "$kubectl"

# Output custody is checked before live reads, and publication failure cannot create READY Evidence.
existing_output=${output_dir}/existing-evidence.json
printf '%s' 'occupied' > "$existing_output"
chmod 0600 "$existing_output"
: > "$log"
run_blocked existing-output env -i "${base_env[@]}" "$preflight" run --target "$target" \
  --materialization-owner-gate "$materialization_gate" --expected-materialization-owner-gate-sha "$materialization_gate_sha" \
  --materialization-evidence "$materialization_evidence" --final-owner-gate "$final_gate" \
  --expected-final-owner-gate-sha "$final_gate_sha" --expected-commit "$expected_commit" --output "$existing_output"
[[ ! -s $log && $(< "$existing_output") == occupied ]] || test::fail 'existing output was not rejected before reads'

ln_state=${workspace}/ln-state
: > "$log"
rm -f "$state_dir"/* "$ln_state"
publication_output=${output_dir}/blocked-publication.json
run_blocked evidence-publication env -i "${base_env[@]}" \
  PATH="${fault_bin}:${tool_bin}:${renderer_bin}:${bin}:$PATH" ATLAS_FAKE_V2_LN_STATE="$ln_state" ATLAS_FAKE_V2_LN_FAIL_AT=2 \
  "$preflight" run --target "$target" --materialization-owner-gate "$materialization_gate" \
  --expected-materialization-owner-gate-sha "$materialization_gate_sha" --materialization-evidence "$materialization_evidence" \
  --final-owner-gate "$final_gate" --expected-final-owner-gate-sha "$final_gate_sha" \
  --expected-commit "$expected_commit" --output "$publication_output"
[[ ! -e $publication_output && $(< "$ln_state") -eq 2 && $(grep -c '^RAW:' "$log") -eq 27 ]] ||
  test::fail 'Evidence publication failure committed READY or changed request surface'

# Offline validator rejects semantic, inventory, timing, and redaction tampering.
for specification in \
  'extra-field|.unexpected = "value"' \
  'schema-version-type|.schemaVersion = "2"' \
  'wrong-commit|.contractGitCommit = "0000000000000000000000000000000000000000"' \
  'wrong-materialization-sha|.targetMaterializationEvidenceSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'wrong-final-gate-sha|.finalOwnerGateSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'wrong-approved-target-sha|.approvedTargetDocumentSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'wrong-before|.liveProjection.liveBeforeSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'missing-read|.kubernetesReads = .kubernetesReads[:-1]' \
  'duplicate-read|.kubernetesReads += [.kubernetesReads[0]]' \
  'extra-read|.kubernetesReads += [{"phase":"AFTER","apiVersion":"v1","kind":"ConfigMap","namespace":"argocd","name":"extra","verb":"get","status":"READY"}]' \
  'read-count-type|.completeness.executedReads = "26"' \
  'reverse-time|.startedAt = .completedAt' \
  'path-leak|.target.environmentName = "/Users/example/.kube/config"' \
  'credential-leak|.target.environmentName = "token=synthetic"' \
  'wrong-assurance|.assurance.productionRecovery = "AUTHORIZED"' \
  'partial-result|.result = "PERSONAL_LOCAL_BLOCKED"'; do
  name=${specification%%|*}
  expression=${specification#*|}
  tampered=${workspace}/evidence-${name}.json
  cp "$evidence" "$tampered"
  rewrite_json "$tampered" "$expression"
  chmod 0600 "$tampered"
  run_blocked "evidence-${name}" env -i PATH="${tool_bin}:$PATH" TMPDIR="$tmp" LC_ALL=C "$preflight" validate \
    --target "$target" --materialization-owner-gate "$materialization_gate" \
    --expected-materialization-owner-gate-sha "$materialization_gate_sha" --materialization-evidence "$materialization_evidence" \
    --final-owner-gate "$final_gate" --expected-final-owner-gate-sha "$final_gate_sha" \
    --expected-commit "$expected_commit" --evidence "$tampered"
done

assert_no_credential_escape
printf '%s\n' 'PASS: PERSONAL_LOCAL v2 Target and final read-only preflight contract'
