#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly executor=$ATLAS_TEST_ROOT/$probe_root/personal-local-target-materialization
readonly fake_source=$ATLAS_TEST_ROOT/tests/gitops/fixtures/argo-authorization-probe/fake-personal-local-materialization-kubectl
readonly expected_commit=${1:-}
if command -v aqua > /dev/null 2>&1; then
  real_yq=$(aqua which yq)
else
  real_yq=$(command -v yq)
fi
readonly real_yq
real_shasum=$(command -v shasum)
readonly real_shasum
real_ln=$(command -v ln)
readonly real_ln
real_mv=$(command -v mv)
readonly real_mv
readonly certificate_sentinel=SYNTHETIC_CLIENT_CERTIFICATE_SENTINEL_B2
readonly private_key_sentinel=SYNTHETIC_CLIENT_PRIVATE_KEY_SENTINEL_B2
readonly expected_waiver_sha=c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a
readonly expected_plan_sha=b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] || test::fail "expected contract commit must be supplied"
[[ -x $executor && -x $fake_source ]] || test::fail "B2 executor or fake kubectl is not executable"
[[ $real_yq == /* && -x $real_yq ]] || test::fail "locked yq executable could not be resolved"
env -i PATH="$PATH" LC_ALL=C "$real_yq" --version > /dev/null 2>&1 ||
  test::fail "locked yq executable is unavailable in a sanitized environment"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-materialization-contract.XXXXXX")
chmod 0700 "$test_workspace"
test_workspace=$(cd "$test_workspace" && pwd -P)
synthetic_ca_key=${test_workspace}/synthetic-ca.key
synthetic_ca_cert=${test_workspace}/synthetic-ca.crt
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=synthetic-atlas-b2-ca' \
  -keyout "$synthetic_ca_key" -out "$synthetic_ca_cert" > /dev/null 2>&1
chmod 0600 "$synthetic_ca_key" "$synthetic_ca_cert"
synthetic_ca_data=$(openssl base64 -A -in "$synthetic_ca_cert")
readonly synthetic_ca_key synthetic_ca_cert synthetic_ca_data
cleanup() {
  if [[ ${ATLAS_TEST_KEEP_TMP:-false} == true ]]; then
    printf 'retained synthetic workspace: %s\n' "$test_workspace" >&2
  else
    rm -rf "$test_workspace"
  fi
}
trap cleanup EXIT

timestamp_offset() {
  local offset=$1 epoch
  epoch=$(($(date -u +%s) + offset))
  if date -u -r "$epoch" '+%Y-%m-%dT%H:%M:%SZ' 2> /dev/null; then
    return 0
  fi
  date -u -d "@${epoch}" '+%Y-%m-%dT%H:%M:%SZ'
}

canonical_sha() {
  local projection
  projection=$(yq -o=json -I=0 'sort_keys(..)' "$1") || return 1
  printf '%s' "$projection" | shasum -a 256 | awk '{print $1}'
}

rewrite_json() {
  local document=$1 expression=$2 temporary projection
  temporary=${document}.rewrite
  projection=$(yq -o=json -I=0 "${expression} | sort_keys(..)" "$document")
  printf '%s' "$projection" > "$temporary"
  chmod 0600 "$temporary"
  mv "$temporary" "$document"
}

case_root=
case_bin=
case_tmp=
case_receipts=
case_output_dir=
case_kubeconfig=
case_kubectl=
case_gate=
case_gate_sha=
case_session_id=
case_session_dir=
case_output=
case_log=
case_access_log=
case_projection_log=
case_stdout=
case_stderr=
declare -a case_environment=()

write_tool_wrapper() {
  local destination=$1 tool=$2
  # shellcheck disable=SC2016 # The generated wrapper expands these values at execution time.
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    'capture_required=false' \
    'for argument in "$@"; do' \
    '  if [[ $argument == "$ATLAS_SYNTHETIC_KUBECONFIG" ]]; then' \
    '    printf "%s:%s\\n" "$(basename "$0")" "$argument" >> "$ATLAS_KUBECONFIG_READ_LOG"' \
    '    capture_required=true' \
    '  fi' \
    'done' \
    "if [[ \$capture_required == false ]]; then exec \"${tool}\" \"\$@\"; fi" \
    'capture=$(mktemp "$ATLAS_TOOL_WRAPPER_TMP/tool-output.XXXXXX")' \
    "if \"${tool}\" \"\$@\" > \"\$capture\"; then status=0; else status=\$?; fi" \
    'printf "### %s\\n" "$(basename "$0")" >> "$ATLAS_TOOL_PROJECTION_LOG"' \
    'cat "$capture" >> "$ATLAS_TOOL_PROJECTION_LOG"' \
    'printf "\\n" >> "$ATLAS_TOOL_PROJECTION_LOG"' \
    'cat "$capture"' \
    'rm -f "$capture"' \
    'exit "$status"' > "$destination"
  chmod 0700 "$destination"
}

write_filesystem_wrapper() {
  local destination=$1
  # shellcheck disable=SC2016 # The generated wrapper expands only synthetic fault controls.
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'set -Eeuo pipefail' \
    'case $(basename "$0") in' \
    '  ln)' \
    '    "$ATLAS_REAL_LN" "$@"' \
    '    if [[ ${ATLAS_FAKE_MATERIALIZATION_WAIT_FOR_GATE_EXPIRY:-false} == true ]]; then' \
    '      : > "$ATLAS_FAKE_MATERIALIZATION_TERMINAL_FAULT_MARKER"' \
    '      while [[ $(date -u +%s) -lt $ATLAS_FAKE_MATERIALIZATION_GATE_EXPIRES_EPOCH ]]; do sleep 0.1; done' \
    '    fi' \
    '    ;;' \
    '  mv)' \
    '    source_path=${1:-}' \
    '    destination_path=${!#}' \
    '    if [[ ${ATLAS_FAKE_MATERIALIZATION_FAIL_TERMINAL_PUBLISH:-false} == true &&' \
    '      $source_path == */terminal.materialized.json && $destination_path == */terminal.json &&' \
    '      ! -e $ATLAS_FAKE_MATERIALIZATION_TERMINAL_FAULT_MARKER ]]; then' \
    '      : > "$ATLAS_FAKE_MATERIALIZATION_TERMINAL_FAULT_MARKER"' \
    '      exit 73' \
    '    fi' \
    '    exec "$ATLAS_REAL_MV" "$@"' \
    '    ;;' \
    'esac' > "$destination"
  chmod 0700 "$destination"
}

make_case() {
  local name=$1 issued expires suffix gate_projection
  case_root=${test_workspace}/${name}
  case_bin=${case_root}/bin
  case_tmp=${case_root}/tmp
  case_receipts=${case_root}/receipts
  case_output_dir=${case_root}/output
  mkdir -p "$case_bin" "$case_tmp" "$case_receipts" "$case_output_dir"
  chmod 0700 "$case_root" "$case_bin" "$case_tmp" "$case_receipts" "$case_output_dir"
  case_kubeconfig=${case_root}/kubeconfig
  case_kubectl=${case_bin}/kubectl
  case_gate=${case_root}/owner-gate.json
  case_output=${case_output_dir}/materialization-evidence.json
  case_log=${case_root}/fake-kubectl.log
  case_access_log=${case_root}/kubeconfig-access.log
  case_projection_log=${case_root}/tool-projections.log
  case_stdout=${case_root}/stdout
  case_stderr=${case_root}/stderr
  : > "$case_log"
  : > "$case_access_log"
  : > "$case_projection_log"
  chmod 0600 "$case_log" "$case_access_log" "$case_projection_log"

  CA_DATA=$synthetic_ca_data CERTIFICATE=$certificate_sentinel PRIVATE_KEY=$private_key_sentinel \
    yq -n -o=json -I=0 \
    '{"apiVersion":"v1","kind":"Config","clusters":[{"name":"synthetic-cluster","cluster":
      {"server":"https://127.0.0.1:6443","certificate-authority-data":strenv(CA_DATA)}}],
      "contexts":[{"name":"kind-atlas-synthetic","context":{"cluster":"synthetic-cluster","user":"synthetic-owner"}}],
      "current-context":"kind-atlas-synthetic","users":[{"name":"synthetic-owner","user":
      {"client-certificate-data":strenv(CERTIFICATE),"client-key-data":strenv(PRIVATE_KEY)}}]} | sort_keys(..)' > "$case_kubeconfig"
  chmod 0600 "$case_kubeconfig"

  cp "$fake_source" "$case_kubectl"
  chmod 0700 "$case_kubectl"
  write_tool_wrapper "$case_bin/yq" "$real_yq"
  write_tool_wrapper "$case_bin/shasum" "$real_shasum"
  write_filesystem_wrapper "$case_bin/ln"
  write_filesystem_wrapper "$case_bin/mv"

  issued=$(timestamp_offset -60)
  expires=$(timestamp_offset 900)
  suffix=$(printf '%s' "$name" | shasum -a 256 | awk '{print substr($1,1,32)}')
  case_session_id=personal-local-materialization-20260830T000000Z-${suffix}
  case_session_dir=${case_receipts}/${case_session_id}
  gate_projection=$(COMMIT=$expected_commit RECEIPTS=$case_receipts SESSION=$case_session_id ISSUED=$issued EXPIRES=$expires \
    KUBECONFIG_PATH=$case_kubeconfig KUBECTL_PATH=$case_kubectl yq -n -o=json -I=0 \
    '{"schemaVersion":1,
      "gateID":"atlas.argocd.authorization-personal-local-target-materialization-owner-gate/v1",
      "operation":"PERSONAL_LOCAL_TARGET_MATERIALIZATION","decision":"APPROVED",
      "rolloutProfile":"PERSONAL_LOCAL","profileID":"atlas.argocd.authorization-probe-profile/personal-local/v2",
      "contractGitCommit":strenv(COMMIT),"authorityBaseline":"165fb2a31068e3de2ac1064dbf8f95966ff8aad1",
      "repositoryURL":"https://github.com/snkio027/atlas.git",
      "waiverDecisionSHA256":"c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a",
      "materializationPlanID":"atlas.argocd.authorization-personal-local-target-materialization-plan/v1",
      "materializationPlanSHA256":"b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc",
      "materializationEvidenceSchemaID":"atlas.argocd.authorization-personal-local-target-materialization-evidence/v1",
      "environmentName":"test","clusterName":"atlas-synthetic","kubeContext":"kind-atlas-synthetic",
      "kubeconfigPath":strenv(KUBECONFIG_PATH),"kubectlPath":strenv(KUBECTL_PATH),
      "kubectlVersion":"1.36.3","kubernetesVersion":"1.36.1",
      "sessionReceiptRoot":strenv(RECEIPTS),"sessionID":strenv(SESSION),
      "issuedAt":strenv(ISSUED),"expiresAt":strenv(EXPIRES)} | sort_keys(..)')
  printf '%s' "$gate_projection" > "$case_gate"
  chmod 0600 "$case_gate"
  case_gate_sha=$(canonical_sha "$case_gate")
  case_environment=(
    "PATH=${case_bin}:$PATH"
    "TMPDIR=${case_tmp}"
    "LC_ALL=C"
    "ATLAS_REAL_YQ=${real_yq}"
    "ATLAS_REAL_SHASUM=${real_shasum}"
    "ATLAS_REAL_LN=${real_ln}"
    "ATLAS_REAL_MV=${real_mv}"
    "ATLAS_SYNTHETIC_KUBECONFIG=${case_kubeconfig}"
    "ATLAS_KUBECONFIG_READ_LOG=${case_access_log}"
    "ATLAS_TOOL_WRAPPER_TMP=${case_tmp}"
    "ATLAS_TOOL_PROJECTION_LOG=${case_projection_log}"
    "ATLAS_FAKE_MATERIALIZATION_LOG=${case_log}"
    "ATLAS_FAKE_MATERIALIZATION_KUBECONFIG=${case_kubeconfig}"
    "ATLAS_FAKE_MATERIALIZATION_CONTEXT=kind-atlas-synthetic"
  )
}

run_case() {
  local gate_sha=${1:-$case_gate_sha}
  shift || true
  if env -i "${case_environment[@]}" "$@" "$executor" run \
    --owner-gate "$case_gate" \
    --expected-owner-gate-sha "$gate_sha" \
    --expected-commit "$expected_commit" \
    --output "$case_output" > "$case_stdout" 2> "$case_stderr"; then
    return 0
  else
    return $?
  fi
}

validate_case() {
  if env -i PATH="$case_bin:$PATH" TMPDIR="$case_tmp" LC_ALL=C \
    ATLAS_REAL_YQ="$real_yq" ATLAS_REAL_SHASUM="$real_shasum" \
    ATLAS_SYNTHETIC_KUBECONFIG="$case_kubeconfig" ATLAS_KUBECONFIG_READ_LOG="$case_access_log" \
    ATLAS_TOOL_WRAPPER_TMP="$case_tmp" ATLAS_TOOL_PROJECTION_LOG="$case_projection_log" \
    "$executor" validate --owner-gate "$case_gate" \
    --expected-owner-gate-sha "$case_gate_sha" --expected-commit "$expected_commit" \
    --evidence "$case_output" > "$case_stdout" 2> "$case_stderr"; then
    return 0
  else
    return $?
  fi
}

assert_synthetic_gate_fixture() {
  local gate=$1 expected_sha=$2 expected_runtime_commit=$3 canonical actual_sha issued expires now
  local gate_schema=${probe_root}/personal-local-target-materialization-owner-gate-v1.schema.json

  [[ $gate == /* && -f $gate && ! -L $gate && -O $gate ]] ||
    test::fail "synthetic Gate fixture custody drifted"
  canonical=$(yq -o=json -I=0 'sort_keys(..)' "$gate") ||
    test::fail "synthetic Gate fixture is not parseable"
  cmp -s "$gate" <(printf '%s' "$canonical") ||
    test::fail "synthetic Gate fixture is not canonical"
  [[ $(yq -r 'keys | .[]' "$gate" | sort) == "$(yq -r '.required[]' "$gate_schema" | sort)" ]] ||
    test::fail "synthetic Gate fixture key projection drifted"
  yq -e '(.schemaVersion | tag) == "!!int" and
    ([.gateID,.operation,.decision,.rolloutProfile,.profileID,.contractGitCommit,
      .authorityBaseline,.repositoryURL,.waiverDecisionSHA256,.materializationPlanID,
      .materializationPlanSHA256,.materializationEvidenceSchemaID,.environmentName,
      .clusterName,.kubeContext,.kubeconfigPath,.kubectlPath,.kubectlVersion,
      .kubernetesVersion,.sessionReceiptRoot,.sessionID,.issuedAt,.expiresAt] |
      all_c(.[]; tag == "!!str"))' "$gate" > /dev/null ||
    test::fail "synthetic Gate fixture field types drifted"
  actual_sha=$(canonical_sha "$gate")
  [[ $actual_sha == "$expected_sha" ]] || test::fail "synthetic Gate fixture SHA drifted"
  [[ $(canonical_sha "${probe_root}/personal-local-profile-v2.json") == "$expected_waiver_sha" &&
  $(canonical_sha "${probe_root}/personal-local-target-materialization-plan.json") == "$expected_plan_sha" ]] ||
    test::fail "synthetic Gate authority document SHA drifted"
  [[ $(yq -r '.contractGitCommit' "$gate") == "$expected_runtime_commit" &&
  $(yq -r '.authorityBaseline' "$gate") == 165fb2a31068e3de2ac1064dbf8f95966ff8aad1 &&
  $(yq -r '.waiverDecisionSHA256' "$gate") == "$expected_waiver_sha" &&
  $(yq -r '.materializationPlanSHA256' "$gate") == "$expected_plan_sha" ]] ||
    test::fail "synthetic Gate Git or decision authority drifted"
  [[ $(yq -r '.gateID' "$gate") == atlas.argocd.authorization-personal-local-target-materialization-owner-gate/v1 &&
  $(yq -r '.operation' "$gate") == PERSONAL_LOCAL_TARGET_MATERIALIZATION &&
  $(yq -r '.decision' "$gate") == APPROVED &&
  $(yq -r '.rolloutProfile' "$gate") == PERSONAL_LOCAL &&
  $(yq -r '.profileID' "$gate") == atlas.argocd.authorization-probe-profile/personal-local/v2 &&
  $(yq -r '.repositoryURL' "$gate") == https://github.com/snkio027/atlas.git &&
  $(yq -r '.materializationPlanID' "$gate") == atlas.argocd.authorization-personal-local-target-materialization-plan/v1 &&
  $(yq -r '.materializationEvidenceSchemaID' "$gate") == atlas.argocd.authorization-personal-local-target-materialization-evidence/v1 ]] ||
    test::fail "synthetic Gate fixed semantics drifted"
  issued=$(yq -r '.issuedAt | to_unix' "$gate")
  expires=$(yq -r '.expiresAt | to_unix' "$gate")
  now=$(date -u +%s)
  [[ $issued =~ ^[0-9]+$ && $expires =~ ^[0-9]+$ && $issued -lt $now && $now -lt $expires ]] ||
    test::fail "synthetic Gate validity window drifted"
}

assert_no_credential_escape() {
  local file
  while IFS= read -r file; do
    [[ $file == "$case_kubeconfig" || $file == "${case_kubeconfig}."* ]] && continue
    if rg -F -e "$certificate_sentinel" -e "$private_key_sentinel" "$file" > /dev/null 2>&1; then
      test::fail "synthetic credential escaped kubeconfig bytes: ${file}"
    fi
  done < <(find "$case_root" -type f -print)
}

assert_blocked_terminal() {
  local classification=$1
  if [[ ! -f ${case_session_dir}/claim.json || ! -f ${case_session_dir}/terminal.json || -e $case_output ||
    $(yq -r '.state' "${case_session_dir}/terminal.json") != BLOCKED ||
    $(yq -r '.failureClassification' "${case_session_dir}/terminal.json") != "$classification" ]]; then
    [[ ! -f ${case_session_dir}/terminal.json ]] || yq '.' "${case_session_dir}/terminal.json" >&2
    test::fail "claimed failure did not produce the expected BLOCKED terminal: ${classification}"
  fi
  assert_no_credential_escape
}

assert_finalization_failure_closed() {
  local marker=$1 terminal_sha files
  assert_blocked_terminal RESULT_INVALID
  files=$(find "$case_session_dir" -maxdepth 1 -type f -exec basename {} \; | sort)
  [[ -e $marker && ! -e $case_output && $files == $'claim.json\nterminal.json' ]] ||
    test::fail "terminal finalization failure left a published or staged artifact"
  terminal_sha=$(canonical_sha "${case_session_dir}/terminal.json")
  : > "$case_log"
  : > "$case_access_log"
  expect_run_blocked finalization-replay "$case_gate_sha"
  [[ ! -s $case_log && ! -s $case_access_log && ! -e $case_output &&
    $(canonical_sha "${case_session_dir}/terminal.json") == "$terminal_sha" ]] ||
    test::fail "terminal finalization failure did not permanently consume the session"
}

assert_preclaim_blocked() {
  local name=$1
  [[ ! -e $case_session_dir && ! -e $case_output && ! -s $case_log && ! -s $case_access_log ]] ||
    test::fail "pre-claim rejection consumed authority or invoked a tool: ${name}"
  assert_no_credential_escape
}

expect_run_blocked() {
  local name=$1
  shift
  if run_case "$@"; then
    test::fail "blocked run unexpectedly succeeded: ${name}"
  else
    status=$?
  fi
  [[ $status -eq 24 && $(< "$case_stdout") == '' && $(< "$case_stderr") == PERSONAL_LOCAL_BLOCKED:* ]] ||
    test::fail "blocked run returned an invalid status or output: ${name}"
}

# One complete synthetic ceremony and offline validation.
make_case success
assert_synthetic_gate_fixture "$case_gate" "$case_gate_sha" "$expected_commit"
if ! run_case; then
  printf 'synthetic stdout:\n%s\nsynthetic stderr:\n%s\n' "$(< "$case_stdout")" "$(< "$case_stderr")" >&2
  test::fail "synthetic Materialization ceremony failed"
fi
[[ $(< "$case_stdout") == TARGET_MATERIALIZED && ! -s $case_stderr ]] ||
  test::fail "successful executor output drifted"
[[ $(< "$case_log") == $'VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' ]] ||
  test::fail "synthetic request inventory or order drifted"
[[ $(yq -r '.result' "$case_output") == TARGET_MATERIALIZED &&
$(yq -r '.completeness.expectedRequests' "$case_output") -eq 2 &&
$(yq -r '.completeness.executedRequests' "$case_output") -eq 2 &&
$(yq -r '.completeness.skippedRequests' "$case_output") -eq 0 &&
$(yq -r '.completeness.collectionReads' "$case_output") -eq 0 &&
$(yq -r '.completeness.secretReads' "$case_output") -eq 0 &&
$(yq -r '.completeness.argoAPICalls' "$case_output") -eq 0 &&
$(yq -r '.completeness.kubernetesMutations' "$case_output") -eq 0 &&
$(yq -r '.completeness.gitOpsMutations' "$case_output") -eq 0 &&
$(yq -r '.completeness.runtimeMutations' "$case_output") -eq 0 &&
$(yq -r '.completeness.unexpectedRequests' "$case_output") -eq 0 ]] ||
  test::fail "successful Evidence completeness drifted"
success_evidence_sha=$(canonical_sha "$case_output")
success_claim_sha=$(canonical_sha "${case_session_dir}/claim.json")
[[ $(yq -r '.materializationSessionClaimSHA256' "$case_output") == "$success_claim_sha" &&
$(yq -r '.state' "${case_session_dir}/terminal.json") == MATERIALIZED &&
$(yq -r '.materializationEvidenceSHA256' "${case_session_dir}/terminal.json") == "$success_evidence_sha" ]] ||
  test::fail "successful claim, Evidence, and terminal hashes do not close"
assert_no_credential_escape

success_terminal_sha=$(canonical_sha "${case_session_dir}/terminal.json")
success_log=$(< "$case_log")
mv "$case_kubeconfig" "${case_kubeconfig}.unavailable"
mv "$case_kubectl" "${case_kubectl}.unavailable"
: > "$case_access_log"
validate_case || test::fail "offline provenance validator required live authority"
[[ $(< "$case_stdout") == TARGET_MATERIALIZED && ! -s $case_stderr && ! -s $case_access_log &&
$(< "$case_log") == "$success_log" ]] || test::fail "offline validator accessed credentials, kubectl, or Kubernetes"
assert_no_credential_escape

# Replays cannot invoke a tool or overwrite a successful terminal.
: > "$case_log"
: > "$case_access_log"
expect_run_blocked replay "$case_gate_sha"
[[ ! -s $case_log && ! -s $case_access_log &&
  $(canonical_sha "${case_session_dir}/terminal.json") == "$success_terminal_sha" ]] ||
  test::fail "replay invoked authority or overwrote a MATERIALIZED terminal"

# Pre-claim Gate and authority failures consume no credential or tool authority.
make_case wrong-expected-gate-sha
expect_run_blocked wrong-expected-gate-sha "$(printf '0%.0s' {1..64})"
assert_preclaim_blocked wrong-expected-gate-sha

for specification in \
  'not-authorized|.decision = "NOT_AUTHORIZED"' \
  'wrong-operation|.operation = "LIVE_READ_ONLY_PREFLIGHT"' \
  'wrong-profile|.profileID = "atlas.argocd.authorization-probe-profile/personal-local/other"' \
  'v1-profile|.profileID = "atlas.argocd.authorization-probe-profile/personal-local/v1"' \
  'schema-invalid-gate-type|.clusterName = false' \
  'wrong-commit|.contractGitCommit = "0000000000000000000000000000000000000000"' \
  'wrong-waiver|.waiverDecisionSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'wrong-plan|.materializationPlanSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"'; do
  name=${specification%%|*}
  expression=${specification#*|}
  make_case "$name"
  rewrite_json "$case_gate" "$expression"
  case_gate_sha=$(canonical_sha "$case_gate")
  expect_run_blocked "$name" "$case_gate_sha"
  assert_preclaim_blocked "$name"
done

make_case expired-gate
EXPIRED=$(timestamp_offset -5) rewrite_json "$case_gate" '.expiresAt = strenv(EXPIRED)'
case_gate_sha=$(canonical_sha "$case_gate")
expect_run_blocked expired-gate "$case_gate_sha"
assert_preclaim_blocked expired-gate

make_case future-gate
FUTURE=$(timestamp_offset 60) rewrite_json "$case_gate" '.issuedAt = strenv(FUTURE)'
case_gate_sha=$(canonical_sha "$case_gate")
expect_run_blocked future-gate "$case_gate_sha"
assert_preclaim_blocked future-gate

make_case invalid-session-root
mkdir "${case_root}/unsafe-receipts"
chmod 0755 "${case_root}/unsafe-receipts"
UNSAFE_ROOT=${case_root}/unsafe-receipts rewrite_json "$case_gate" '.sessionReceiptRoot = strenv(UNSAFE_ROOT)'
case_gate_sha=$(canonical_sha "$case_gate")
expect_run_blocked invalid-session-root "$case_gate_sha"
assert_preclaim_blocked invalid-session-root

make_case ambient-kubeconfig
case_environment+=("KUBECONFIG=${case_root}/ambient")
expect_run_blocked ambient-kubeconfig "$case_gate_sha"
assert_preclaim_blocked ambient-kubeconfig

make_case ambient-argocd-token
case_environment+=("ARGOCD_AUTH_TOKEN=SYNTHETIC_ARGO_TOKEN_SENTINEL_B2")
expect_run_blocked ambient-argocd-token "$case_gate_sha"
assert_preclaim_blocked ambient-argocd-token

make_case unsafe-gate-mode
chmod 0644 "$case_gate"
expect_run_blocked unsafe-gate-mode "$case_gate_sha"
assert_preclaim_blocked unsafe-gate-mode

make_case gate-symlink
mv "$case_gate" "${case_gate}.real"
ln -s "${case_gate}.real" "$case_gate"
expect_run_blocked gate-symlink "$case_gate_sha"
assert_preclaim_blocked gate-symlink

make_case unsafe-output-parent
chmod 0755 "$case_output_dir"
expect_run_blocked unsafe-output-parent "$case_gate_sha"
assert_preclaim_blocked unsafe-output-parent

make_case existing-output
printf '%s' PRESERVE > "$case_output"
existing_output_sha=$(shasum -a 256 "$case_output" | awk '{print $1}')
expect_run_blocked existing-output "$case_gate_sha"
[[ ! -e $case_session_dir && ! -s $case_log && ! -s $case_access_log &&
  $(shasum -a 256 "$case_output" | awk '{print $1}') == "$existing_output_sha" ]] ||
  test::fail "existing Evidence output was accessed or replaced"

make_case output-symlink
outside_output=${case_root}/outside-output
printf '%s' PRESERVE > "$outside_output"
outside_output_sha=$(shasum -a 256 "$outside_output" | awk '{print $1}')
ln -s "$outside_output" "$case_output"
expect_run_blocked output-symlink "$case_gate_sha"
[[ ! -e $case_session_dir && ! -s $case_log && ! -s $case_access_log &&
  $(shasum -a 256 "$outside_output" | awk '{print $1}') == "$outside_output_sha" ]] ||
  test::fail "symlink Evidence output crossed its approved boundary"

make_case preexisting-session
mkdir -m 0700 "$case_session_dir"
expect_run_blocked preexisting-session "$case_gate_sha"
[[ ! -e ${case_session_dir}/claim.json && ! -e ${case_session_dir}/terminal.json && ! -s $case_log && ! -s $case_access_log ]] ||
  test::fail "pre-existing session was modified or invoked authority"

# The repository executable is itself part of authority and drift fails before claim.
make_case repository-authority-drift
clone_root=${test_workspace}/repository-authority-clone
git clone --quiet --no-hardlinks "$ATLAS_TEST_ROOT" "$clone_root"
printf '\n' >> "$clone_root/$probe_root/personal-local-target-materialization"
clone_executor=$clone_root/$probe_root/personal-local-target-materialization
if env -i "${case_environment[@]}" "$clone_executor" run --owner-gate "$case_gate" \
  --expected-owner-gate-sha "$case_gate_sha" --expected-commit "$expected_commit" \
  --output "$case_output" > "$case_stdout" 2> "$case_stderr"; then
  test::fail "repository authority drift was accepted"
else
  status=$?
fi
[[ $status -eq 24 ]] || test::fail "repository authority drift returned the wrong status"
assert_preclaim_blocked repository-authority-drift

# Claimed local-authority and kubeconfig-shape failures must leave one BLOCKED terminal.
make_case kubeconfig-symlink
mv "$case_kubeconfig" "${case_kubeconfig}.real"
ln -s "${case_kubeconfig}.real" "$case_kubeconfig"
expect_run_blocked kubeconfig-symlink "$case_gate_sha"
assert_blocked_terminal CUSTODY_DRIFTED

make_case unsafe-kubeconfig-mode
chmod 0644 "$case_kubeconfig"
expect_run_blocked unsafe-kubeconfig-mode "$case_gate_sha"
assert_blocked_terminal CUSTODY_DRIFTED

for specification in \
  'duplicate-context|.contexts += [.contexts[0]]' \
  'missing-context|.contexts = []' \
  'duplicate-cluster|.clusters += [.clusters[0]]' \
  'missing-cluster|.clusters = []' \
  'duplicate-user|.users += [.users[0]]' \
  'missing-user|.users = []' \
  'extra-credential-key|.users[0].user.token = "SYNTHETIC_TOKEN"' \
  'exec-auth|.users[0].user.exec = {"command":"false"}' \
  'token-auth|.users[0].user = {"token":"SYNTHETIC_TOKEN"}' \
  'external-ca|del(.clusters[0].cluster."certificate-authority-data") | .clusters[0].cluster."certificate-authority" = "/tmp/forbidden-ca"' \
  'insecure-tls|.clusters[0].cluster."insecure-skip-tls-verify" = true' \
  'insecure-tls-string|.clusters[0].cluster."insecure-skip-tls-verify" = "false"' \
  'invalid-ca|.clusters[0].cluster."certificate-authority-data" = "NOT_BASE64"' \
  'proxy-transport|.clusters[0].cluster."proxy-url" = "https://proxy.invalid"'; do
  name=${specification%%|*}
  expression=${specification#*|}
  make_case "$name"
  rewrite_json "$case_kubeconfig" "$expression"
  expect_run_blocked "$name" "$case_gate_sha"
  assert_blocked_terminal CREDENTIAL_SHAPE_INVALID
  [[ $(< "$case_log") == VERSION ]] || test::fail "kubeconfig shape failure expanded the request surface: ${name}"
done

make_case unsafe-kubectl
chmod g+w "$case_kubectl"
expect_run_blocked unsafe-kubectl "$case_gate_sha"
assert_blocked_terminal TOOL_DRIFTED
[[ ! -s $case_log ]] || test::fail "unsafe kubectl was executed"

make_case kubectl-symlink
mv "$case_kubectl" "${case_kubectl}.real"
ln -s "${case_kubectl}.real" "$case_kubectl"
expect_run_blocked kubectl-symlink "$case_gate_sha"
assert_blocked_terminal TOOL_DRIFTED
[[ ! -s $case_log ]] || test::fail "symlink kubectl was executed"

make_case kubectl-version-mismatch
expect_run_blocked kubectl-version-mismatch "$case_gate_sha" ATLAS_FAKE_MATERIALIZATION_CLIENT_VERSION=v1.36.2
assert_blocked_terminal TOOL_DRIFTED
[[ $(< "$case_log") == VERSION ]] || test::fail "client version failure expanded the request surface"

# Every request error is terminal and never triggers a diagnostic read.
for specification in \
  'version-stderr|ATLAS_FAKE_MATERIALIZATION_STDERR=GET_VERSION|VERSION\nGET_VERSION' \
  'version-nonzero|ATLAS_FAKE_MATERIALIZATION_NONZERO=GET_VERSION|VERSION\nGET_VERSION' \
  'version-malformed|ATLAS_FAKE_MATERIALIZATION_MALFORMED=GET_VERSION|VERSION\nGET_VERSION' \
  'server-version-drift|ATLAS_FAKE_MATERIALIZATION_SERVER_VERSION=v1.36.2|VERSION\nGET_VERSION' \
  'namespace-stderr|ATLAS_FAKE_MATERIALIZATION_STDERR=GET_KUBE_SYSTEM|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-nonzero|ATLAS_FAKE_MATERIALIZATION_NONZERO=GET_KUBE_SYSTEM|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-wrong-api|ATLAS_FAKE_MATERIALIZATION_NAMESPACE_API_VERSION=v2|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-wrong-kind|ATLAS_FAKE_MATERIALIZATION_NAMESPACE_KIND=ConfigMap|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-wrong-name|ATLAS_FAKE_MATERIALIZATION_NAMESPACE_NAME=other|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-missing-uid|ATLAS_FAKE_MATERIALIZATION_NAMESPACE_UID_MISSING=true|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-malformed-uid|ATLAS_FAKE_MATERIALIZATION_NAMESPACE_UID=bad|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' \
  'namespace-malformed|ATLAS_FAKE_MATERIALIZATION_MALFORMED=GET_KUBE_SYSTEM|VERSION\nGET_VERSION\nGET_KUBE_SYSTEM'; do
  name=${specification%%|*}
  remainder=${specification#*|}
  injected=${remainder%%|*}
  expected_log=${remainder#*|}
  make_case "$name"
  expect_run_blocked "$name" "$case_gate_sha" "$injected"
  assert_blocked_terminal REQUEST_FAILED
  [[ $(< "$case_log") == "$(printf '%b' "$expected_log")" ]] ||
    test::fail "request failure expanded or reordered reads: ${name}"
done

# Final custody changes after the exact reads fail before successful Evidence.
make_case kubeconfig-toctou
expect_run_blocked kubeconfig-toctou "$case_gate_sha" ATLAS_FAKE_MATERIALIZATION_DRIFT_KUBECONFIG=true
assert_blocked_terminal CUSTODY_DRIFTED
[[ $(< "$case_log") == $'VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' ]] || test::fail "kubeconfig TOCTOU changed request count"

make_case kubectl-toctou
expect_run_blocked kubectl-toctou "$case_gate_sha" ATLAS_FAKE_MATERIALIZATION_DRIFT_KUBECTL=true
assert_blocked_terminal TOOL_DRIFTED
[[ $(< "$case_log") == $'VERSION\nGET_VERSION\nGET_KUBE_SYSTEM' ]] || test::fail "kubectl TOCTOU changed request count"

# MATERIALIZED is validated before publication and is the final semantic commit.
make_case gate-expiry-before-terminal
EXPIRY=$(timestamp_offset 15) rewrite_json "$case_gate" '.expiresAt = strenv(EXPIRY)'
case_gate_sha=$(canonical_sha "$case_gate")
terminal_fault_marker=${case_root}/evidence-output-committed
gate_expires_epoch=$(yq -r '.expiresAt | to_unix' "$case_gate")
expect_run_blocked gate-expiry-before-terminal "$case_gate_sha" \
  ATLAS_FAKE_MATERIALIZATION_WAIT_FOR_GATE_EXPIRY=true \
  "ATLAS_FAKE_MATERIALIZATION_GATE_EXPIRES_EPOCH=${gate_expires_epoch}" \
  "ATLAS_FAKE_MATERIALIZATION_TERMINAL_FAULT_MARKER=${terminal_fault_marker}"
assert_finalization_failure_closed "$terminal_fault_marker"

make_case terminal-publication-failure
terminal_fault_marker=${case_root}/terminal-publication-failed
expect_run_blocked terminal-publication-failure "$case_gate_sha" \
  ATLAS_FAKE_MATERIALIZATION_FAIL_TERMINAL_PUBLISH=true \
  "ATLAS_FAKE_MATERIALIZATION_TERMINAL_FAULT_MARKER=${terminal_fault_marker}"
assert_finalization_failure_closed "$terminal_fault_marker"

# An interrupted claimed session is terminal and cannot be retried.
make_case interrupted-session
interrupt_marker=${case_root}/request-held
env -i "${case_environment[@]}" ATLAS_FAKE_MATERIALIZATION_HOLD_MARKER="$interrupt_marker" \
  "$executor" run --owner-gate "$case_gate" --expected-owner-gate-sha "$case_gate_sha" \
  --expected-commit "$expected_commit" --output "$case_output" > "$case_stdout" 2> "$case_stderr" &
executor_pid=$!
for _ in {1..100}; do
  [[ -e $interrupt_marker ]] && break
  sleep 0.1
done
[[ -e $interrupt_marker ]] || test::fail "interruption fixture did not reach the first request"
kill -TERM "$executor_pid"
: > "${interrupt_marker}.release"
if wait "$executor_pid"; then
  test::fail "interrupted session unexpectedly succeeded"
else
  status=$?
fi
[[ $status -eq 24 ]] || test::fail "interrupted session returned the wrong status"
assert_blocked_terminal SESSION_INTERRUPTED
interrupted_terminal_sha=$(canonical_sha "${case_session_dir}/terminal.json")
: > "$case_log"
expect_run_blocked interrupted-replay "$case_gate_sha"
[[ ! -s $case_log && $(canonical_sha "${case_session_dir}/terminal.json") == "$interrupted_terminal_sha" ]] ||
  test::fail "BLOCKED session was reused or overwritten"

# Fake backend rejects any operation outside the frozen command allowlist.
make_case fake-unexpected-request
if env -i "${case_environment[@]}" "$case_kubectl" get pods > /dev/null 2>&1; then
  test::fail "fake kubectl accepted an unexpected operation"
fi
[[ $(< "$case_log") == UNEXPECTED:* ]] || test::fail "unexpected fake request was not recorded"

# Offline Evidence tampering stays blocked even when the terminal hash is resealed.
make_case evidence-base
run_case || test::fail "Evidence negative base ceremony failed"
cp "$case_output" "${case_output}.original"
cp "${case_session_dir}/terminal.json" "${case_session_dir}/terminal.original.json"
for specification in \
  'extra-field|.unexpected = "value"' \
  'missing-field|del(.apiServerURL)' \
  'schema-invalid-version-type|.schemaVersion = "1"' \
  'schema-invalid-completeness-type|.completeness.expectedRequests = "2"' \
  'wrong-assurance|.assurance.productionRecovery = "AUTHORIZED"' \
  'wrong-request-inventory|.kubernetesReads[1].path = "/api/v1/namespaces"' \
  'wrong-gate-sha|.materializationOwnerGateSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'wrong-claim-sha|.materializationSessionClaimSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'wrong-profile|.profileID = "atlas.argocd.authorization-probe-profile/personal-local/v1"' \
  'wrong-plan|.materializationPlanSHA256 = "0000000000000000000000000000000000000000000000000000000000000000"' \
  'reverse-time|.startedAt = .completedAt' \
  'credential-leakage|.clusterName = "token=synthetic"' \
  'path-leakage|.kubeContext = "/tmp/kubeconfig=synthetic"' \
  'partial-result|.result = "PARTIAL"'; do
  name=${specification%%|*}
  expression=${specification#*|}
  cp "${case_output}.original" "$case_output"
  cp "${case_session_dir}/terminal.original.json" "${case_session_dir}/terminal.json"
  chmod 0600 "$case_output" "${case_session_dir}/terminal.json"
  rewrite_json "$case_output" "$expression"
  tampered_sha=$(canonical_sha "$case_output")
  TAMPERED_SHA=$tampered_sha rewrite_json "${case_session_dir}/terminal.json" \
    '.materializationEvidenceSHA256 = strenv(TAMPERED_SHA)'
  if validate_case; then
    test::fail "tampered Evidence was accepted: ${name}"
  else
    status=$?
  fi
  [[ $status -eq 24 ]] || test::fail "tampered Evidence returned wrong status: ${name}"
done

# Evidence without a MATERIALIZED terminal is never reusable.
cp "${case_output}.original" "$case_output"
rm -f "${case_session_dir}/terminal.json"
if validate_case; then
  test::fail "orphan Evidence without terminal was accepted"
else
  status=$?
fi
[[ $status -eq 24 ]] || test::fail "orphan Evidence returned wrong status"

[[ $(canonical_sha "$probe_root/personal-local-profile-v2.json") == "$expected_waiver_sha" &&
$(canonical_sha "$probe_root/personal-local-target-materialization-plan.json") == "$expected_plan_sha" ]] ||
  test::fail "B2 altered frozen Profile v2 or Plan authority"

test::pass "ADR-0005 PERSONAL_LOCAL target Materialization executor and offline validator"
