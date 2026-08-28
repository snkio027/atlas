#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly profile=$probe_root/personal-local-profile.json
readonly target_schema=$probe_root/personal-local-target.schema.json
readonly evidence_schema=$probe_root/personal-local-evidence.schema.json
readonly production_contract=$probe_root/probe-contract.json
readonly authority_inventory=gitops/platform/management/protection-foundation/definitions/argo-hardening/argo-authority-inventory.json
readonly fixture_root=tests/gitops/fixtures/argo-authorization-probe
readonly target_fixture=$fixture_root/valid-personal-local-target.json
readonly evidence_fixture=$fixture_root/valid-personal-local-defined-evidence.json
readonly production_target=$fixture_root/valid-target.json
readonly expected_contract_commit=${1:-}
readonly fixture_gate_sha=dbb62b37538b65bb3c2f5c293d38efab3050a36b697674c42fb9d6407da074ee
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-profile.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

[[ $expected_contract_commit =~ ^[0-9a-f]{40}$ ]] ||
  test::fail "expected contract commit must be supplied by the caller"

sha256_text() { shasum -a 256 | awk '{print $1}'; }
schema_keys() { yq -r '.required[]' "$1" | sort; }
document_keys() { yq -r 'keys | .[]' "$1" | sort; }

for json_file in "$profile" "$target_schema" "$evidence_schema" \
  "$target_fixture" "$evidence_fixture"; do
  yq -e '.' "$json_file" > /dev/null || test::fail "invalid PERSONAL_LOCAL JSON: ${json_file}"
done

profile_sha=$(yq -o=json -I=0 'sort_keys(..)' "$profile" | sha256_text) ||
  test::fail "could not hash PERSONAL_LOCAL decision"
[[ $profile_sha == "$(yq -r '.waiverDecisionSHA256' "$target_fixture")" &&
$profile_sha == "$(yq -r '.target.waiverDecisionSHA256' "$evidence_fixture")" ]] ||
  test::fail "PERSONAL_LOCAL waiver is not bound to the canonical profile decision"

[[ $(yq -r '.authorityBaseline' "$profile") == 48867ada7a38e551098b5c698ddfefc899d9b09e &&
$(yq -r '.rolloutProfile' "$profile") == PERSONAL_LOCAL &&
$(yq -r '.selection.explicitSelectionRequired' "$profile") == true &&
$(yq -r '.selection.implicitFallbackAllowed' "$profile") == false &&
$(yq -r '.selection.productionProfileStatus' "$profile") == UNSUPPORTED &&
$(yq -r '.states.repositoryDefinition' "$profile") == PERSONAL_LOCAL_DEFINED &&
$(yq -r '.states.liveReadOnlyPreflight' "$profile") == PERSONAL_LOCAL_READY &&
$(yq -r '.states.admissionObserving' "$profile") == PERSONAL_LOCAL_OBSERVING &&
$(yq -r '.states.observationComplete' "$profile") == PERSONAL_LOCAL_OBSERVED &&
$(yq -r '.states.failure' "$profile") == PERSONAL_LOCAL_BLOCKED ]] ||
  test::fail "PERSONAL_LOCAL state or explicit-selection contract drifted"

[[ $(yq -o=json -I=0 '.classification.preflightResults' "$profile") == '["PERSONAL_LOCAL_DEFINED","PERSONAL_LOCAL_READY","PERSONAL_LOCAL_BLOCKED"]' &&
$(yq -o=json -I=0 '.classification.exitCodes' "$profile") == '{"PERSONAL_LOCAL_DEFINED":0,"PERSONAL_LOCAL_READY":0,"PERSONAL_LOCAL_BLOCKED":24}' &&
$(yq -r '.classification.unknownProfile' "$profile") == PERSONAL_LOCAL_BLOCKED &&
$(yq -r '.classification.syntheticReady' "$profile") == PERSONAL_LOCAL_BLOCKED &&
$(yq -r '.classification.inputMismatch' "$profile") == PERSONAL_LOCAL_BLOCKED &&
$(yq -r '.classification.unavailableRead' "$profile") == PERSONAL_LOCAL_BLOCKED &&
$(yq -r '.classification.partialResult' "$profile") == PERSONAL_LOCAL_BLOCKED ]] ||
  test::fail "PERSONAL_LOCAL result or exit-code classification drifted"

[[ $(yq -r '.repositoryFixture.executionMode' "$profile") == REPOSITORY_ONLY_SYNTHETIC &&
$(yq -r '.repositoryFixture.maximumResult' "$profile") == PERSONAL_LOCAL_DEFINED &&
$(yq -r '.repositoryFixture.ownerGateState' "$profile") == NOT_AUTHORIZED &&
$(yq -r '.repositoryFixture.ownerGateSHA256' "$profile") == "$fixture_gate_sha" &&
$(yq -r '.livePreflight.executionMode' "$profile") == LIVE_READ_ONLY_PREFLIGHT &&
$(yq -r '.livePreflight.ownerGateState' "$profile") == APPROVED &&
$(yq -r '.livePreflight.requiredObjectCount' "$profile") -eq 13 &&
$(yq -r '.livePreflight.requiredVerb' "$profile") == get &&
$(yq -r '.livePreflight.argoAPICallCount' "$profile") -eq 0 &&
$(yq -r '.livePreflight.mutationCount' "$profile") -eq 0 ]] ||
  test::fail "PERSONAL_LOCAL repository/live boundary drifted"

[[ $(yq -r '.waivers.baseArgoActions.count' "$profile") -eq 33 &&
$(yq -r '.waivers.baseArgoActions.execution' "$profile") == NOT_EXECUTED &&
$(yq -r '.waivers.baseArgoActions.result' "$profile") == RUNTIME_UNPROVEN &&
$(yq -r '.waivers.fineGrainedApplicationActions.count' "$profile") -eq 3 &&
$(yq -r '.waivers.fineGrainedApplicationActions.result' "$profile") == STATICALLY_CLOSED_RUNTIME_UNPROVEN &&
$(yq -r '.waivers.builtInAdmin.result' "$profile") == STATICALLY_CLOSED_RUNTIME_UNPROVEN &&
$(yq -r '.waivers.builtInAdmin.credentialRequired' "$profile") == false &&
$(yq -r '.waivers.builtInAdmin.loginExecuted' "$profile") == false &&
$(yq -r '.waivers.custody.model' "$profile") == SINGLE_OWNER_HUMAN_GATE &&
$(yq -r '.waivers.custody.productionIndependentCustodySatisfied' "$profile") == false &&
$(yq -r '.assurance.argoAPIAuthorization' "$profile") == RUNTIME_UNPROVEN &&
$(yq -r '.assurance.productionRecovery' "$profile") == NOT_AUTHORIZED ]] ||
  test::fail "PERSONAL_LOCAL waiver or assurance classification drifted"

expected_fine_grained=('action/*' 'delete/*' 'update/*')
mapfile -t actual_fine_grained < <(yq -r '.waivers.fineGrainedApplicationActions.patterns[]' "$profile" | sort)
[[ ${actual_fine_grained[*]} == "${expected_fine_grained[*]}" ]] ||
  test::fail "fine-grained waiver inventory drifted"

[[ $(yq -r '.observation.minimumMinutes' "$profile") -eq 30 &&
$(yq -r '.observation.maximumMinutes' "$profile") -eq 60 &&
$(yq -r '.observation.minimumCompleteReconciliationAuditCycles' "$profile") -eq 3 &&
$(yq -r '.observation.zeroEventsMeansSuccess' "$profile") == false &&
$(yq -r '.observation.insufficientEvidenceResult' "$profile") == PERSONAL_LOCAL_BLOCKED &&
$(yq -r '.observationTransition.candidateBaseMustEqualPreflightMain' "$profile") == true &&
$(yq -r '.observationTransition.postMergeRevalidationRequired' "$profile") == true &&
$(yq -r '.observationTransition.mismatchResult' "$profile") == PERSONAL_LOCAL_BLOCKED ]] ||
  test::fail "PERSONAL_LOCAL observation boundary drifted"

expected_git_authorities=(candidateBaseCommit candidateDesiredTree candidateHeadCommit postMergeDesiredTree postMergeMainCommit preflightMainCommit)
mapfile -t actual_git_authorities < <(yq -r '.observationTransition.requiredGitAuthorities[]' "$profile" | sort)
[[ ${actual_git_authorities[*]} == "${expected_git_authorities[*]}" ]] ||
  test::fail "observation Git-authority transition inventory drifted"

for prohibited in ambientKubeconfig argoAuthToken argoCore argoAPI secretRead collectionRead mutation admissionDeny recoveryRBAC signal receipt; do
  [[ $(KEY=$prohibited yq -r '.prohibitions[strenv(KEY)]' "$profile") == true ]] ||
    test::fail "PERSONAL_LOCAL prohibition is not fail-closed: ${prohibited}"
done

target_fingerprint() {
  local target=$1 payload
  printf -v payload 'rolloutProfile=%s\nauthorityBaseline=%s\ncontractGitCommit=%s\nenvironmentName=%s\nclusterName=%s\nkubeContext=%s\nkubeconfigSHA256=%s\napiServerURL=%s\nkubeSystemNamespaceUID=%s\napiServerCASPKISHA256=%s\nrepositoryURL=%s\nwaiverDecisionSHA256=%s\nownerGateState=%s\nownerGateSHA256=%s\n' \
    "$(yq -r '.rolloutProfile' "$target")" "$(yq -r '.authorityBaseline' "$target")" \
    "$(yq -r '.contractGitCommit' "$target")" "$(yq -r '.environmentName' "$target")" \
    "$(yq -r '.clusterName' "$target")" "$(yq -r '.kubeContext' "$target")" \
    "$(yq -r '.kubeconfigSHA256' "$target")" "$(yq -r '.apiServerURL' "$target")" \
    "$(yq -r '.kubeSystemNamespaceUID' "$target")" "$(yq -r '.apiServerCASPKISHA256' "$target")" \
    "$(yq -r '.repositoryURL' "$target")" "$(yq -r '.waiverDecisionSHA256' "$target")" \
    "$(yq -r '.ownerGateState' "$target")" "$(yq -r '.ownerGateSHA256' "$target")"
  printf '%s' "$payload" | sha256_text
}

seal_target() {
  local input=$1 output=$2 projection sha
  projection=$(yq -o=json -I=0 'del(.approvedTargetDocumentSHA256) | sort_keys(..)' "$input") || return 1
  sha=$(printf '%s' "$projection" | sha256_text) || return 1
  APPROVED_SHA=$sha yq '.approvedTargetDocumentSHA256 = strenv(APPROVED_SHA)' "$input" > "$output"
}

bind_target() {
  local input=$1 commit=$2 output=$3 draft=$test_workspace/target-draft.json fingerprint
  EXPECTED_COMMIT=$commit PROFILE_SHA=$profile_sha yq \
    '.contractGitCommit = strenv(EXPECTED_COMMIT) | .waiverDecisionSHA256 = strenv(PROFILE_SHA)' \
    "$input" > "$draft" || return 1
  fingerprint=$(target_fingerprint "$draft") || return 1
  TARGET_SHA=$fingerprint yq '.targetFingerprintSHA256 = strenv(TARGET_SHA)' "$draft" > "${draft}.fingerprinted" || return 1
  seal_target "${draft}.fingerprinted" "$output"
}

bind_evidence() {
  local input=$1 target=$2 output=$3
  EXPECTED_COMMIT=$(yq -r '.contractGitCommit' "$target") \
  TARGET_SHA=$(yq -r '.targetFingerprintSHA256' "$target") \
  PROFILE_SHA=$(yq -r '.waiverDecisionSHA256' "$target") \
  GATE_STATE=$(yq -r '.ownerGateState' "$target") \
  GATE_SHA=$(yq -r '.ownerGateSHA256' "$target") \
  APPROVED_SHA=$(yq -r '.approvedTargetDocumentSHA256' "$target") \
    yq '.contractGitCommit = strenv(EXPECTED_COMMIT) |
      .target.targetFingerprintSHA256 = strenv(TARGET_SHA) |
      .target.waiverDecisionSHA256 = strenv(PROFILE_SHA) |
      .target.ownerGateState = strenv(GATE_STATE) |
      .target.ownerGateSHA256 = strenv(GATE_SHA) |
      .target.approvedTargetDocumentSHA256 = strenv(APPROVED_SHA)' "$input" > "$output"
}

validate_target() {
  local target=$1 expected_commit=$2 expected_objects actual_objects projection sha
  yq -e '.' "$target" > /dev/null || return 1
  [[ $(document_keys "$target") == "$(schema_keys "$target_schema")" ]] || return 1
  [[ $(yq -r '.schemaVersion' "$target") == 1 &&
  $(yq -r '.rolloutProfile' "$target") == PERSONAL_LOCAL &&
  $(yq -r '.contractGitCommit' "$target") == "$expected_commit" &&
  $(yq -r '.authorityBaseline' "$target") == "$(yq -r '.authorityBaseline' "$profile")" &&
  $(yq -r '.waiverDecisionSHA256' "$target") == "$profile_sha" &&
  $(yq -r '.repositoryURL' "$target") == https://github.com/snkio027/atlas.git &&
  $(yq -r '.kubeconfigPath' "$target") == /* &&
  $(yq -r '.kubeconfigSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
  $(yq -r '.apiServerURL' "$target") =~ ^https://[^[:space:]]+$ &&
  $(yq -r '.kubeSystemNamespaceUID' "$target") =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ &&
  $(yq -r '.apiServerCASPKISHA256' "$target") =~ ^[0-9a-f]{64}$ &&
  $(yq -r '.ownerGateSHA256' "$target") =~ ^[0-9a-f]{64}$ ]] || return 1
  case $(yq -r '.ownerGateState' "$target") in
    NOT_AUTHORIZED) [[ $(yq -r '.ownerGateSHA256' "$target") == "$fixture_gate_sha" ]] || return 1 ;;
    APPROVED) [[ $(yq -r '.ownerGateSHA256' "$target") != "$fixture_gate_sha" ]] || return 1 ;;
    *) return 1 ;;
  esac
  [[ $(target_fingerprint "$target") == "$(yq -r '.targetFingerprintSHA256' "$target")" ]] || return 1
  [[ $(yq '.desiredObjects | length' "$target") -eq 13 &&
  $(yq '[.desiredObjects[] | [.apiVersion,.kind,.namespace,.name] | @tsv] | unique | length' "$target") -eq 13 ]] || return 1
  expected_objects=$(yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256] | @tsv' "$production_target" | sort) || return 1
  actual_objects=$(yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256] | @tsv' "$target" | sort) || return 1
  [[ $actual_objects == "$expected_objects" ]] || return 1
  projection=$(yq -o=json -I=0 '.desiredObjects | sort_by(.apiVersion,.kind,.namespace,.name) |
    (.. | select(tag == "!!map")) |= sort_keys(.)' "$target") || return 1
  sha=$(printf '%s' "$projection" | sha256_text) || return 1
  [[ $sha == "$(yq -r '.desiredProjectionSHA256' "$target")" ]] || return 1
  projection=$(yq -o=json -I=0 'del(.approvedTargetDocumentSHA256) | sort_keys(..)' "$target") || return 1
  sha=$(printf '%s' "$projection" | sha256_text) || return 1
  [[ $sha == "$(yq -r '.approvedTargetDocumentSHA256' "$target")" ]]
}

validate_evidence() {
  local evidence=$1 target=$2 expected_objects actual_objects expected_reads actual_reads
  yq -e '.' "$evidence" > /dev/null || return 1
  [[ $(document_keys "$evidence") == "$(schema_keys "$evidence_schema")" ]] || return 1
  [[ $(yq -r '.schemaVersion' "$evidence") == 1 &&
  $(yq -r '.contractID' "$evidence") == atlas.argocd.authorization-personal-local-preflight/v1 &&
  $(yq -r '.rolloutProfile' "$evidence") == PERSONAL_LOCAL &&
  $(yq -r '.contractGitCommit' "$evidence") == "$(yq -r '.contractGitCommit' "$target")" &&
  $(yq -r '.authorityBaseline' "$evidence") == "$(yq -r '.authorityBaseline' "$profile")" &&
  $(yq -r '.target.environmentName' "$evidence") == "$(yq -r '.environmentName' "$target")" &&
  $(yq -r '.target.clusterName' "$evidence") == "$(yq -r '.clusterName' "$target")" &&
  $(yq -r '.target.targetFingerprintSHA256' "$evidence") == "$(yq -r '.targetFingerprintSHA256' "$target")" &&
  $(yq -r '.target.apiServerCASPKISHA256' "$evidence") == "$(yq -r '.apiServerCASPKISHA256' "$target")" &&
  $(yq -r '.target.waiverDecisionSHA256' "$evidence") == "$(yq -r '.waiverDecisionSHA256' "$target")" &&
  $(yq -r '.target.ownerGateState' "$evidence") == "$(yq -r '.ownerGateState' "$target")" &&
  $(yq -r '.target.ownerGateSHA256' "$evidence") == "$(yq -r '.ownerGateSHA256' "$target")" &&
  $(yq -r '.target.approvedTargetDocumentSHA256' "$evidence") == "$(yq -r '.approvedTargetDocumentSHA256' "$target")" &&
  $(yq -r '.target.desiredProjectionSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" ]] || return 1
  [[ $(yq -r '.waiver.baseArgoActions' "$evidence") == RUNTIME_UNPROVEN &&
  $(yq -r '.waiver.fineGrainedApplicationActions' "$evidence") == STATICALLY_CLOSED_RUNTIME_UNPROVEN &&
  $(yq -r '.waiver.builtInAdmin' "$evidence") == STATICALLY_CLOSED_RUNTIME_UNPROVEN &&
  $(yq -r '.waiver.anonymousAccess' "$evidence") == STATICALLY_CLOSED_RUNTIME_UNPROVEN &&
  $(yq -r '.waiver.identity' "$evidence") == NOT_PROVIDED &&
  $(yq -r '.waiver.custody' "$evidence") == SINGLE_OWNER_HUMAN_GATE &&
  $(yq -r '.assurance.argoAPIAuthorization' "$evidence") == RUNTIME_UNPROVEN &&
  $(yq -r '.assurance.productionRecovery' "$evidence") == NOT_AUTHORIZED &&
  $(yq -o=json -I=0 '.liveProjection.reviewedPolicyFragments' "$evidence") == '["policy.csv"]' &&
  $(yq -r '.completeness.secretReads' "$evidence") -eq 0 &&
  $(yq -r '.completeness.collectionReads' "$evidence") -eq 0 &&
  $(yq -r '.completeness.argoAPICalls' "$evidence") -eq 0 &&
  $(yq -r '.completeness.mutations' "$evidence") -eq 0 ]] || return 1
  expected_objects=$(yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256] | @tsv' "$target" | sort) || return 1
  actual_objects=$(yq -r '.liveProjection.objects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256] | @tsv' "$evidence" | sort) || return 1
  [[ $actual_objects == "$expected_objects" ]] || return 1
  expected_reads=$(yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name,"get"] | @tsv' "$target" | sort) || return 1
  actual_reads=$(yq -r '.kubernetesReads[] | [.apiVersion,.kind,.namespace,.name,.verb] | @tsv' "$evidence" | sort) || return 1
  [[ $actual_reads == "$expected_reads" ]] || return 1
  case $(yq -r '.executionMode' "$evidence") in
    REPOSITORY_ONLY_SYNTHETIC)
      [[ $(yq -r '.result' "$evidence") == PERSONAL_LOCAL_DEFINED &&
      $(yq -r '.target.ownerGateState' "$evidence") == NOT_AUTHORIZED &&
      $(yq -r '.target.ownerGateSHA256' "$evidence") == "$fixture_gate_sha" &&
      $(yq -r '.liveProjection.status' "$evidence") == NOT_EXECUTED &&
      $(yq -r '.liveProjection.liveBeforeSHA256' "$evidence") == null &&
      $(yq -r '.liveProjection.liveAfterSHA256' "$evidence") == null &&
      $(yq '[.liveProjection.objects[] | select(.liveBeforeProjectionSHA256 != null or .liveAfterProjectionSHA256 != null)] | length' "$evidence") -eq 0 &&
      $(yq '[.kubernetesReads[] | select(.status != "NOT_EXECUTED")] | length' "$evidence") -eq 0 &&
      $(yq -r '.completeness.expectedReads' "$evidence") -eq 13 &&
      $(yq -r '.completeness.executedReads' "$evidence") -eq 0 &&
      $(yq -r '.completeness.skippedReads' "$evidence") -eq 13 ]]
      ;;
    LIVE_READ_ONLY_PREFLIGHT)
      [[ $(yq -r '.target.ownerGateState' "$evidence") == APPROVED &&
      $(yq -r '.target.ownerGateSHA256' "$evidence") != "$fixture_gate_sha" ]] || return 1
      if [[ $(yq -r '.result' "$evidence") == PERSONAL_LOCAL_READY ]]; then
        [[ $(yq -r '.liveProjection.status' "$evidence") == MATCH &&
        $(yq -r '.liveProjection.desiredProjectionSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
        $(yq -r '.liveProjection.liveBeforeSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
        $(yq -r '.liveProjection.liveAfterSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
        $(yq '[.liveProjection.objects[] | select(.desiredProjectionSHA256 != .liveBeforeProjectionSHA256 or .desiredProjectionSHA256 != .liveAfterProjectionSHA256)] | length' "$evidence") -eq 0 &&
        $(yq '[.kubernetesReads[] | select(.status != "READY")] | length' "$evidence") -eq 0 &&
        $(yq -r '.completeness.executedReads' "$evidence") -eq 13 &&
        $(yq -r '.completeness.skippedReads' "$evidence") -eq 0 ]]
      else
        [[ $(yq -r '.result' "$evidence") == PERSONAL_LOCAL_BLOCKED ]]
      fi
      ;;
    *) return 1 ;;
  esac
}

bound_target=$test_workspace/target.json
bound_evidence=$test_workspace/evidence.json
bind_target "$target_fixture" "$expected_contract_commit" "$bound_target" ||
  test::fail "could not bind PERSONAL_LOCAL target to the approved commit"
bind_evidence "$evidence_fixture" "$bound_target" "$bound_evidence" ||
  test::fail "could not bind PERSONAL_LOCAL Evidence to the target"
validate_target "$bound_target" "$expected_contract_commit" ||
  test::fail "valid PERSONAL_LOCAL definition Target was rejected"
validate_evidence "$bound_evidence" "$bound_target" ||
  test::fail "valid PERSONAL_LOCAL_DEFINED fixture was rejected"

[[ $(yq -r '.result' "$bound_evidence") == PERSONAL_LOCAL_DEFINED ]] ||
  test::fail "repository fixture exceeded PERSONAL_LOCAL_DEFINED"

for mutation in production-profile waiver gate-state gate-hash commit-resealed fingerprint desired-object missing-object extra-field; do
  mutated=$test_workspace/target-${mutation}.json
  case $mutation in
    production-profile) yq '.rolloutProfile = "PRODUCTION"' "$bound_target" > "$mutated" ;;
    waiver) yq '.waiverDecisionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated" ;;
    gate-state) yq '.ownerGateState = "APPROVED"' "$bound_target" > "$mutated" ;;
    gate-hash) yq '.ownerGateSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated" ;;
    commit-resealed)
      yq '.contractGitCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' "$bound_target" > "${mutated}.draft"
      fingerprint=$(target_fingerprint "${mutated}.draft")
      TARGET_SHA=$fingerprint yq '.targetFingerprintSHA256 = strenv(TARGET_SHA)' "${mutated}.draft" > "${mutated}.fingerprinted"
      seal_target "${mutated}.fingerprinted" "$mutated"
      ;;
    fingerprint) yq '.targetFingerprintSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated" ;;
    desired-object) yq '.desiredObjects[0].desiredProjectionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated" ;;
    missing-object) yq 'del(.desiredObjects[-1])' "$bound_target" > "$mutated" ;;
    extra-field) yq '.identity = "ambient"' "$bound_target" > "$mutated" ;;
  esac
  if validate_target "$mutated" "$expected_contract_commit"; then
    test::fail "unsafe PERSONAL_LOCAL Target was accepted: ${mutation}"
  fi
done

for mutation in synthetic-ready live-without-gate production-profile waiver gate commit target stable-drift missing-object replaced-object skipped-read mutation-count secret-read argo-api-call extra-field; do
  mutated=$test_workspace/evidence-${mutation}.json
  case $mutation in
    synthetic-ready) yq '.result = "PERSONAL_LOCAL_READY"' "$bound_evidence" > "$mutated" ;;
    live-without-gate) yq '.executionMode = "LIVE_READ_ONLY_PREFLIGHT" | .result = "PERSONAL_LOCAL_READY"' "$bound_evidence" > "$mutated" ;;
    production-profile) yq '.rolloutProfile = "PRODUCTION"' "$bound_evidence" > "$mutated" ;;
    waiver) yq '.target.waiverDecisionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated" ;;
    gate) yq '.target.ownerGateSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated" ;;
    commit) yq '.contractGitCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' "$bound_evidence" > "$mutated" ;;
    target) yq '.target.targetFingerprintSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated" ;;
    stable-drift) yq '.liveProjection.liveBeforeSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" |
      .liveProjection.liveAfterSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated" ;;
    missing-object) yq 'del(.liveProjection.objects[-1])' "$bound_evidence" > "$mutated" ;;
    replaced-object) yq '.liveProjection.objects[0].name = "replacement"' "$bound_evidence" > "$mutated" ;;
    skipped-read) yq 'del(.kubernetesReads[-1])' "$bound_evidence" > "$mutated" ;;
    mutation-count) yq '.completeness.mutations = 1' "$bound_evidence" > "$mutated" ;;
    secret-read) yq '.completeness.secretReads = 1' "$bound_evidence" > "$mutated" ;;
    argo-api-call) yq '.completeness.argoAPICalls = 1' "$bound_evidence" > "$mutated" ;;
    extra-field) yq '.credentialReferenceSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated" ;;
  esac
  if validate_evidence "$mutated" "$bound_target"; then
    test::fail "unsafe PERSONAL_LOCAL Evidence was accepted: ${mutation}"
  fi
done

[[ $(yq -r '.builtInAdminEnabled' "$authority_inventory") == false &&
$(yq -r '.anonymousEnabled' "$authority_inventory") == false &&
$(yq -o=json -I=0 '.reviewedPolicyFragments' "$authority_inventory") == '["policy.csv"]' &&
$(yq '.inheritedRoleMappings | length' "$authority_inventory") -eq 0 &&
$(yq '.ssoSubjects | length' "$authority_inventory") -eq 0 &&
$(yq '.localAccounts | length' "$authority_inventory") -eq 0 &&
$(yq '.retainedSideEffectingRoles | length' "$authority_inventory") -eq 0 &&
$(yq -r '.fineGrainedApplicationInheritanceDisabled' "$authority_inventory") == true ]] ||
  test::fail "live Git authority is not eligible for PERSONAL_LOCAL static closure"

[[ $(yq -r '.identityDecisionState' "$production_contract") == IDENTITY_UNAVAILABLE &&
$(yq -r '.classification.fineGrainedCanI' "$production_contract") == UNSUPPORTED &&
$(yq -r '.classification.adminWithoutApprovedCredential' "$production_contract") == ADMIN_PROOF_UNAVAILABLE ]] ||
  test::fail "Production Profile no longer remains independently unsupported"

forbidden_live_expression='kubectl[[:space:]]+(get|create|apply|patch|delete|auth|version|config|exec|port-forward)[[:space:]]|argocd[[:space:]]'
if rg -n "$forbidden_live_expression" "${BASH_SOURCE[0]}"; then
  test::fail "repository-only PERSONAL_LOCAL contract invokes a live client"
fi

test::pass "repository-only ADR-0003 PERSONAL_LOCAL rollout profile"
