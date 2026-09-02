#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly profile_v1=$probe_root/personal-local-profile-v1.json
readonly profile_v2=$probe_root/personal-local-profile-v2.json
readonly plan=$probe_root/personal-local-target-materialization-plan.json
readonly materialization_gate=$probe_root/personal-local-target-materialization-owner-gate-v1.schema.json
readonly materialization_evidence=$probe_root/personal-local-target-materialization-evidence-v1.schema.json
readonly materialization_claim=$probe_root/personal-local-target-materialization-claim-v1.schema.json
readonly materialization_terminal=$probe_root/personal-local-target-materialization-terminal-v1.schema.json
readonly target_v2=$probe_root/personal-local-target-v2.schema.json
readonly final_gate_v2=$probe_root/personal-local-owner-gate-v2.schema.json
readonly final_evidence_v2=$probe_root/personal-local-evidence-v2.schema.json
readonly expected_commit=${1:-}

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] ||
  test::fail "expected contract commit must be supplied by the caller"

sha256_text() { shasum -a 256 | awk '{print $1}'; }
canonical_json_sha() {
  local projection
  projection=$(yq -o=json -I=0 'sort_keys(..)' "$1") || return 1
  printf '%s' "$projection" | sha256_text
}
sorted_lines() { printf '%s\n' "$@" | sort; }
required_keys() { yq -r '.required[]' "$1" | sort; }
property_keys() { yq -r '.properties | keys | .[]' "$1" | sort; }
assert_exact_top_level_schema() {
  local schema=$1 missing
  missing=$(comm -23 <(required_keys "$schema") <(property_keys "$schema"))
  [[ $(yq -r '.type' "$schema") == object &&
  $(yq -r '.additionalProperties' "$schema") == false &&
  -z $missing ]] ||
    test::fail "schema does not define one exact top-level projection: ${schema}"
}
assert_authority_key_absent() {
  local document=$1 key=$2
  if KEY=$key yq -e '.. | select(tag == "!!map" and has(strenv(KEY)))' "$document" > /dev/null 2>&1; then
    test::fail "mutable implementation key entered canonical authority: ${key} in ${document}"
  fi
}

for document in "$profile_v1" "$profile_v2" "$plan" "$materialization_gate" \
  "$materialization_evidence" "$materialization_claim" "$materialization_terminal" \
  "$target_v2" "$final_gate_v2" "$final_evidence_v2"; do
  yq -e '.' "$document" > /dev/null || test::fail "invalid PERSONAL_LOCAL v2 JSON: ${document}"
done
for schema in "$materialization_gate" "$materialization_evidence" "$materialization_claim" \
  "$materialization_terminal" "$target_v2" "$final_gate_v2" "$final_evidence_v2"; do
  assert_exact_top_level_schema "$schema"
done

v1_sha=$(canonical_json_sha "$profile_v1")
v2_sha=$(canonical_json_sha "$profile_v2")
plan_sha=$(canonical_json_sha "$plan")
[[ $v1_sha == 34e42bc31933ecf63fa5d878b611c3119415c3503481c7863e5e1cb5a4eff949 &&
  $v2_sha == c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a &&
  $plan_sha == b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc &&
  $v1_sha != "$v2_sha" ]] || test::fail "versioned Profile or Plan authority hash drifted"

[[ $(yq -r '.profileID' "$profile_v1") == atlas.argocd.authorization-probe-profile/personal-local/v1 &&
$(yq -r '.profileID' "$profile_v2") == atlas.argocd.authorization-probe-profile/personal-local/v2 &&
$(yq -r '.selection.explicitProfileIDRequired' "$profile_v2") == true &&
$(yq -r '.selection.v1FallbackAllowed' "$profile_v2") == false &&
$(yq -r '.selection.dualAuthorizationAllowed' "$profile_v2") == false &&
$(yq -r '.selection.productionProfileStatus' "$profile_v2") == UNSUPPORTED &&
$(yq -r '.productionEligible' "$profile_v2") == false ]] ||
  test::fail "Profile version selection permits fallback or Production eligibility"
for authority_document in "$profile_v2" "$plan"; do
  assert_authority_key_absent "$authority_document" implementationState
  assert_authority_key_absent "$authority_document" executorImplemented
done

expected_states=(
  PERSONAL_LOCAL_BLOCKED
  PERSONAL_LOCAL_DEFINED
  PERSONAL_LOCAL_OBSERVED
  PERSONAL_LOCAL_OBSERVING
  PERSONAL_LOCAL_READY
  TARGET_MATERIALIZED
)
mapfile -t actual_states < <(yq -r '.states[]' "$profile_v2" | sort)
[[ ${actual_states[*]} == "${expected_states[*]}" ]] || test::fail "PERSONAL_LOCAL v2 state taxonomy drifted"
[[ $(yq -r '.gates.targetMaterialization.operation' "$profile_v2") == PERSONAL_LOCAL_TARGET_MATERIALIZATION &&
$(yq -r '.gates.readOnlyPreflight.operation' "$profile_v2") == PERSONAL_LOCAL_READ_ONLY_PREFLIGHT &&
$(yq -r '.gates.interchangeable' "$profile_v2") == false &&
$(yq -r '.executionModes.targetMaterialization' "$profile_v2") == LIVE_TARGET_MATERIALIZATION &&
$(yq -r '.executionModes.readOnlyPreflight' "$profile_v2") == LIVE_READ_ONLY_PREFLIGHT &&
$(yq -r '.executionModes.states' "$profile_v2") == false ]] ||
  test::fail "Gate, mode, and state namespaces are not distinct"

expected_operations=(
  VALIDATE_OWNER_GATE
  ACQUIRE_SESSION_CLAIM
  VALIDATE_KUBECONFIG_CUSTODY
  HASH_KUBECONFIG
  VALIDATE_KUBECTL_CUSTODY
  HASH_KUBECTL
  VALIDATE_KUBECTL_CLIENT_VERSION
  PROJECT_CONTEXT_CLUSTER_AND_USER
  PROJECT_CLUSTER_TLS_AUTHORITY
  VALIDATE_SELECTED_USER_KEY_SHAPE
  COMPUTE_CA_SPKI_SHA256
  GET_VERSION
  GET_KUBE_SYSTEM_NAMESPACE
  REVALIDATE_FILE_CUSTODY_AND_HASHES
  BUILD_AND_VALIDATE_EVIDENCE
  COMMIT_TERMINAL_RECEIPT
)
mapfile -t actual_operations < <(yq -r '.orderedOperations[]' "$plan")
[[ ${actual_operations[*]} == "${expected_operations[*]}" && ${#actual_operations[@]} -eq 16 &&
  $(yq -r '.planID' "$plan") == atlas.argocd.authorization-personal-local-target-materialization-plan/v1 &&
  $(yq -r '.operation' "$plan") == PERSONAL_LOCAL_TARGET_MATERIALIZATION ]] ||
  test::fail "canonical Materialization operation sequence drifted"

expected_requests=$'1\tGET\t/version\tNON_RESOURCE\n2\tGET\t/api/v1/namespaces/kube-system\tEXACT_OBJECT'
actual_requests=$(yq -r '.kubernetesRequests[] | [.ordinal,.method,.path,.resourceScope] | @tsv' "$plan")
[[ $actual_requests == "$expected_requests" &&
  $(yq -r '.completeness.expectedRequests' "$plan") -eq 2 &&
  $(yq -r '.completeness.collectionReads' "$plan") -eq 0 &&
  $(yq -r '.completeness.secretReads' "$plan") -eq 0 &&
  $(yq -r '.completeness.argoAPICalls' "$plan") -eq 0 &&
  $(yq -r '.completeness.kubernetesMutations' "$plan") -eq 0 &&
  $(yq -r '.completeness.gitOpsMutations' "$plan") -eq 0 &&
  $(yq -r '.completeness.runtimeMutations' "$plan") -eq 0 &&
  $(yq -r '.completeness.unexpectedRequests' "$plan") -eq 0 ]] ||
  test::fail "Materialization request surface exceeds two exact GET requests"

expected_gate_fields=(
  authorityBaseline clusterName contractGitCommit decision environmentName expiresAt gateID issuedAt
  kubeContext kubeconfigPath kubectlPath kubectlVersion kubernetesVersion materializationEvidenceSchemaID
  materializationPlanID materializationPlanSHA256 operation profileID repositoryURL rolloutProfile
  schemaVersion sessionID sessionReceiptRoot waiverDecisionSHA256
)
[[ $(required_keys "$materialization_gate") == "$(sorted_lines "${expected_gate_fields[@]}")" ]] ||
  test::fail "Materialization Owner Gate field set drifted"
[[ $(yq -o=json -I=0 '.properties.decision.enum' "$materialization_gate") == '["NOT_AUTHORIZED","APPROVED"]' ]] ||
  test::fail "Materialization Owner Gate decision enum drifted"
for discovered_field in kubeconfigSHA256 kubectlSHA256 apiServerURL apiServerCASPKISHA256 kubeSystemNamespaceUID; do
  [[ $(FIELD=$discovered_field yq '[.required[] | select(. == strenv(FIELD))] | length' "$materialization_gate") -eq 0 &&
  $(FIELD=$discovered_field yq '.properties | has(strenv(FIELD))' "$materialization_gate") == false ]] ||
    test::fail "Materialization Gate requires a value it exists to discover: ${discovered_field}"
done

expected_user_keys='["client-certificate-data","client-key-data"]'
[[ $(yq -r '.credential.mode' "$profile_v2") == STATIC_IN_KUBECONFIG &&
$(yq -r '.credential.authenticationShape' "$profile_v2") == X509_EMBEDDED_DATA &&
$(yq -o=json -I=0 '.credential.selectedUserExactKeys' "$profile_v2") == "$expected_user_keys" &&
$(yq -r '.credential.dynamicAuthenticationAllowed' "$profile_v2") == false &&
$(yq -r '.properties.credentialMode.const' "$materialization_evidence") == STATIC_IN_KUBECONFIG &&
$(yq -r '.properties.authenticationShape.const' "$materialization_evidence") == X509_EMBEDDED_DATA &&
$(yq -r '.properties.selectedUserShapeStatus.const' "$materialization_evidence") == EXACT_X509_EMBEDDED_DATA ]] ||
  test::fail "static embedded X.509 credential shape drifted"

credential_shape_valid() {
  local document=$1 keys
  keys=$(yq -o=json -I=0 '.user | keys | sort' "$document") || return 1
  [[ $keys == "$expected_user_keys" ]]
}
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-v2.XXXXXX")
cleanup() { rm -rf "$test_workspace"; }
trap cleanup EXIT
printf '%s\n' '{"user":{"client-certificate-data":"SYNTHETIC","client-key-data":"SYNTHETIC"}}' > "$test_workspace/valid-credential.json"
credential_shape_valid "$test_workspace/valid-credential.json" || test::fail "synthetic exact credential shape was rejected"
for extra_key in exec auth-provider token tokenFile username password client-certificate client-key interactiveMode impersonate-user impersonate-group; do
  EXTRA=$extra_key yq '.user[strenv(EXTRA)] = "SYNTHETIC"' "$test_workspace/valid-credential.json" > "$test_workspace/invalid-credential.json"
  if credential_shape_valid "$test_workspace/invalid-credential.json"; then
    test::fail "dynamic or projected credential key was accepted: ${extra_key}"
  fi
done

for forbidden_field in kubeconfigPath kubectlPath sessionReceiptRoot credentialValues token password privateKey clientCertificate authorizationHeader secretData; do
  if FIELD=$forbidden_field yq -e '.. | select(tag == "!!map") | select(has(strenv(FIELD)))' "$materialization_evidence" > /dev/null 2>&1; then
    test::fail "Materialization Evidence exposes forbidden field: ${forbidden_field}"
  fi
done
[[ $(yq -r '.properties.apiServerURL.pattern' "$materialization_evidence") == '^https://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:[0-9]{1,5})?$' ]] ||
  test::fail "Materialization Evidence API endpoint permits paths, query values, or userinfo"
kube_context_pattern=$(yq -r '.properties.kubeContext.pattern' "$materialization_evidence")
[[ kind-atlas-test =~ $kube_context_pattern ]] || test::fail "safe synthetic kube context was rejected"
for sensitive_value in 'token=synthetic' 'password:synthetic' 'cookie=synthetic' '/Users/owner/.kube/config'; do
  if [[ $sensitive_value =~ $kube_context_pattern ]]; then
    test::fail "shape-preserving sensitive Evidence string was accepted: ${sensitive_value}"
  fi
done
expected_materialization_evidence_fields=(
  apiServerCASPKISHA256 apiServerURL assurance authenticationShape authorityBaseline clusterName
  completeness completedAt contractGitCommit credentialMode environmentName evidenceID kubeContext
  kubeSystemNamespaceUID kubeconfigSHA256 kubectlSHA256 kubectlVersion kubernetesReads
  kubernetesVersion localAuthorityOperations materializationOwnerGateSHA256 materializationPlanID
  materializationPlanSHA256 materializationSessionClaimSHA256 profileID repositoryURL result
  rolloutProfile schemaVersion selectedUserShapeStatus sessionID startedAt waiverDecisionSHA256
)
[[ $(property_keys "$materialization_evidence") == "$(sorted_lines "${expected_materialization_evidence_fields[@]}")" &&
$(yq -r '.properties.result.const' "$materialization_evidence") == TARGET_MATERIALIZED &&
$(yq -r '.properties.assurance.properties.argoAPIAuthorization.const' "$materialization_evidence") == RUNTIME_UNPROVEN &&
$(yq -r '.properties.assurance.properties.productionRecovery.const' "$materialization_evidence") == NOT_AUTHORIZED ]] ||
  test::fail "Materialization Evidence exact projection or assurance drifted"

[[ $(yq -r '.properties.state.const' "$materialization_claim") == CLAIMED &&
$(yq -o=json -I=0 '.properties.state.enum' "$materialization_terminal") == '["MATERIALIZED","BLOCKED"]' &&
$(yq '.oneOf | length' "$materialization_terminal") -eq 2 &&
$(yq -r '.oneOf[0].required[0]' "$materialization_terminal") == materializationEvidenceSHA256 &&
$(yq -r '.oneOf[1].required[0]' "$materialization_terminal") == failureClassification ]] ||
  test::fail "create-once claim or terminal result contract drifted"

expected_claim_fields=(
  claimedAt contractGitCommit materializationOwnerGateSHA256 materializationPlanSHA256
  schemaVersion sessionID state waiverDecisionSHA256
)
[[ $(property_keys "$materialization_claim") == "$(sorted_lines "${expected_claim_fields[@]}")" ]] ||
  test::fail "claim schema includes unapproved replay fields"
expected_terminal_fields=(
  completedAt failureClassification materializationEvidenceSHA256
  materializationSessionClaimSHA256 schemaVersion sessionID state
)
[[ $(property_keys "$materialization_terminal") == "$(sorted_lines "${expected_terminal_fields[@]}")" ]] ||
  test::fail "terminal schema includes partial runtime material"

[[ $(yq -r '.properties.schemaVersion.const' "$target_v2") -eq 2 &&
$(yq -r '.properties.profileID.const' "$target_v2") == atlas.argocd.authorization-probe-profile/personal-local/v2 &&
$(yq '[.required[] | select(. == "targetMaterializationEvidenceSHA256")] | length' "$target_v2") -eq 1 &&
$(yq -r '.properties.gateID.const' "$final_gate_v2") == atlas.argocd.authorization-personal-local-owner-gate/v2 &&
$(yq '[.required[] | select(. == "targetMaterializationEvidenceSHA256")] | length' "$final_gate_v2") -eq 1 &&
$(yq -r '.properties.contractID.const' "$final_evidence_v2") == atlas.argocd.authorization-personal-local-preflight/v2 &&
$(yq -r '.properties.result.const' "$final_evidence_v2") == PERSONAL_LOCAL_READY ]] ||
  test::fail "Final Target, Gate, or Evidence is not explicitly v2 and materialization-bound"

for provenance_field in profileID targetMaterializationEvidenceSHA256 materializationOwnerGateSHA256 finalOwnerGateSHA256 approvedTargetDocumentSHA256; do
  [[ $(FIELD=$provenance_field yq '[.required[] | select(. == strenv(FIELD))] | length' "$final_evidence_v2") -eq 1 ]] ||
    test::fail "Final Evidence omits v2 provenance: ${provenance_field}"
done

expected_final_gate_fields=(
  apiServerCASPKISHA256 authorityBaseline contractGitCommit decision desiredProjectionSHA256 expiresAt
  gateID issuedAt kubeContext kubeconfigSHA256 operation ownerGateTargetProjectionSHA256 profileID
  readObjectCount readPlanSHA256 repositoryURL rolloutProfile schemaVersion sessionID snapshotCount
  targetMaterializationEvidenceSHA256 waiverDecisionSHA256
)
[[ $(property_keys "$final_gate_v2") == "$(sorted_lines "${expected_final_gate_fields[@]}")" ]] ||
  test::fail "Final Owner Gate v2 exact authority projection drifted"

v1_target_fields=$(required_keys "$probe_root/personal-local-target-v1.schema.json")
v2_target_legacy_fields=$(yq -r '.required[] | select(. != "profileID" and . != "targetMaterializationEvidenceSHA256")' "$target_v2" | sort)
[[ $v2_target_legacy_fields == "$v1_target_fields" ]] ||
  test::fail "Final Target v2 changed the historical Target projection instead of extending it"

expected_provenance_equalities='["FINAL_EVIDENCE_MATERIALIZATION_EVIDENCE_EQUALS_FINAL_TARGET","FINAL_EVIDENCE_MATERIALIZATION_EVIDENCE_EQUALS_FINAL_GATE","FINAL_EVIDENCE_MATERIALIZATION_GATE_EQUALS_MATERIALIZATION_EVIDENCE","FINAL_EVIDENCE_FINAL_GATE_EQUALS_FINAL_TARGET","FINAL_EVIDENCE_APPROVED_TARGET_EQUALS_FINAL_TARGET"]'

[[ $(yq -r '.properties.completeness.properties.plannedObjects.const' "$final_evidence_v2") -eq 13 &&
$(yq -r '.properties.completeness.properties.snapshotCount.const' "$final_evidence_v2") -eq 2 &&
$(yq -r '.properties.completeness.properties.expectedReads.const' "$final_evidence_v2") -eq 26 &&
$(yq -r '.properties.completeness.properties.executedReads.const' "$final_evidence_v2") -eq 26 &&
$(yq -r '.properties.completeness.properties.nonResourceReads.const' "$final_evidence_v2") -eq 1 &&
$(yq -r '.properties.completeness.properties.extraReads.const' "$final_evidence_v2") -eq 0 &&
$(yq -r '.properties.completeness.properties.SecretReads.const' "$final_evidence_v2") -eq 0 &&
$(yq -r '.properties.completeness.properties.collectionReads.const' "$final_evidence_v2") -eq 0 &&
$(yq -r '.properties.completeness.properties.ArgoAPICalls.const' "$final_evidence_v2") -eq 0 &&
$(yq -r '.properties.completeness.properties.mutations.const' "$final_evidence_v2") -eq 0 &&
$(yq -o=json -I=0 '.properties.nonResourceReads.const' "$final_evidence_v2") == '[{"method":"GET","path":"/version","status":"READY"}]' &&
$(yq -r '.finalPreflight.kubeSystemUIDUsesSnapshotReads' "$profile_v2") == true &&
$(yq -r '.finalPreflight.requiredAggregateEquality' "$profile_v2") == DESIRED_EQUALS_LIVE_BEFORE_EQUALS_LIVE_AFTER &&
$(yq -r '.finalPreflight.requiredPerObjectEquality' "$profile_v2") == DESIRED_EQUALS_LIVE_BEFORE_EQUALS_LIVE_AFTER &&
$(yq -o=json -I=0 '.finalPreflight.requiredProvenanceEqualities' "$profile_v2") == "$expected_provenance_equalities" ]] ||
  test::fail "Final preflight adds requests or weakens exact completeness"

freshness_allowed() {
  local completed_epoch=$1 issued_epoch=$2 delta
  delta=$((issued_epoch - completed_epoch))
  ((delta >= 0 && delta <= 900))
}
freshness_allowed 1000 1000 || test::fail "zero-second materialization freshness was rejected"
freshness_allowed 1000 1900 || test::fail "900-second materialization freshness was rejected"
if freshness_allowed 1001 1000; then test::fail "negative freshness was accepted"; fi
if freshness_allowed 1000 1901; then test::fail "901-second freshness was accepted"; fi
[[ $(yq -r '.finalPreflight.freshnessMinimumSeconds' "$profile_v2") -eq 0 &&
$(yq -r '.finalPreflight.freshnessMaximumSeconds' "$profile_v2") -eq 900 ]] ||
  test::fail "Profile v2 does not bind the zero-to-900-second freshness window"

for prohibited in ambientKubeconfig argoAuthToken argoCore argoAPI secretRead collectionRead kubernetesMutation gitOpsMutation runtimeMutation admissionActivation recoveryRBAC signal adoptionReceipt; do
  [[ $(KEY=$prohibited yq -r '.prohibitions[strenv(KEY)]' "$profile_v2") == true ]] ||
    test::fail "PERSONAL_LOCAL v2 prohibition is not fail-closed: ${prohibited}"
done

[[ ! -e $probe_root/personal-local-profile.json &&
  ! -e $probe_root/personal-local-target.schema.json &&
  ! -e $probe_root/personal-local-owner-gate.schema.json &&
  ! -e $probe_root/personal-local-evidence.schema.json ]] ||
  test::fail "ambiguous unversioned PERSONAL_LOCAL authority remains"

[[ -x $probe_root/personal-local-target-materialization ]] ||
  test::fail "the dedicated B2 Materialization executor is unavailable"
[[ -x $probe_root/personal-local-target-v2 &&
  -x $probe_root/personal-local-read-only-preflight ]] ||
  test::fail "the dedicated B3 Target or preflight executable is unavailable"

"$ATLAS_TEST_ROOT/tests/gitops/contract-primitives-contract.sh" "$expected_commit"

test::pass "ADR-0005 PERSONAL_LOCAL v2 repository-only contract surface"
