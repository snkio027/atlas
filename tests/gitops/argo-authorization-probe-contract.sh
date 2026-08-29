#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly contract=$probe_root/probe-contract.json
readonly target_schema=$probe_root/target.schema.json
readonly evidence_schema=$probe_root/evidence.schema.json
readonly matrix=$probe_root/probe-matrix.json
readonly action_inventory=gitops/platform/management/protection-foundation/definitions/argo-hardening/argo-freeze-action-inventory.json
readonly authority_inventory=gitops/platform/management/protection-foundation/definitions/argo-hardening/argo-authority-inventory.json
readonly fixture_root=tests/gitops/fixtures/argo-authorization-probe
readonly valid_target=$fixture_root/valid-target.json
readonly valid_evidence=$fixture_root/valid-evidence.json
readonly cases=$fixture_root/classification-cases.json
readonly expected_contract_commit=${1:-}
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-argo-authorization-probe.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

[[ $expected_contract_commit =~ ^[0-9a-f]{40}$ ]] ||
  test::fail "expected contract commit must be supplied by the caller"

probe_git() {
  env -i PATH="$PATH" LC_ALL=C git --no-replace-objects \
    -c core.fsmonitor=false -c core.ignoreStat=false "$@"
}

repository_root=$(cd "$ATLAS_TEST_ROOT" && pwd -P) || test::fail "could not resolve repository root"
actual_repository_root=$(probe_git -C "$repository_root" rev-parse --show-toplevel) ||
  test::fail "could not resolve Git repository authority"
runtime_contract_commit=$(probe_git -C "$repository_root" rev-parse --verify 'HEAD^{commit}') ||
  test::fail "could not resolve runtime Git commit"
[[ $actual_repository_root == "$repository_root" && $runtime_contract_commit == "$expected_contract_commit" ]] ||
  test::fail "runtime checkout differs from the caller-approved contract commit"

approved_checkout=$test_workspace/approved-checkout
mkdir -p "$approved_checkout"
probe_git -C "$repository_root" archive --format=tar "$expected_contract_commit" |
  tar -xf - -C "$approved_checkout" || test::fail "could not materialize the approved Git hydration source"

for json_file in "$contract" "$target_schema" "$evidence_schema" "$matrix" \
  "$valid_target" "$valid_evidence" "$cases"; do
  yq -e '.' "$json_file" > /dev/null || test::fail "invalid probe contract JSON: ${json_file}"
done

mapfile -t actual_contract_files < <(
  for contract_file in "$probe_root"/*; do
    [[ -f $contract_file ]] && basename "$contract_file"
  done | sort
)
expected_contract_files=(
  README.md
  evidence.schema.json
  personal-local-evidence.schema.json
  personal-local-owner-gate.schema.json
  personal-local-preflight
  personal-local-profile.json
  personal-local-target.schema.json
  probe-contract.json
  probe-matrix.json
  target.schema.json
)
[[ ${actual_contract_files[*]} == "${expected_contract_files[*]}" ]] ||
  test::fail "Probe Contract file inventory drifted"

argocd_version=$(awk -F= '$1 == "ARGOCD_VERSION" {print $2}' versions.lock)
argocd_image=$(awk -F= '$1 == "ARGOCD_IMAGE" {print $2}' versions.lock)
argocd_digest=${argocd_image##*@}
[[ $(yq -r '.authorityBaseline' "$contract") == 5a8bd5f5b9d547f82bcd6a86c2038c3805e6752f &&
$(yq -r '.executionState' "$contract") == REPOSITORY_ONLY &&
$(yq -r '.activationStage' "$contract") == ADR_0003_PHASE_1B_PROBE_CONTRACT &&
$(yq -r '.identityDecisionState' "$contract") == IDENTITY_UNAVAILABLE &&
$(yq -r '.argocdClient.version' "$contract") == "$argocd_version" &&
$(yq -r '.requiredServerVersion' "$contract") == "$argocd_version" &&
$(yq -r '.argocdClient.image' "$contract") == "$argocd_image" &&
$(yq -r '.argocdClient.digest' "$contract") == "$argocd_digest" &&
$(yq -r '.argocdClient.versionsLockKey' "$contract") == ARGOCD_IMAGE &&
$(yq -r '.argocdClient.ambientExecutableAllowed' "$contract") == false &&
$(yq -r '.requiredFineGrainedApplicationInheritanceDisabled' "$contract") == true &&
$(yq -r '.fineGrainedApplicationInheritanceDisabled' "$authority_inventory") == true ]] ||
  test::fail "Probe Contract client or authority baseline differs from the locked repository authority"

[[ $(yq -r '.transport.tlsRequired' "$contract") == true &&
$(yq -r '.transport.serverCertificatePinRequired' "$contract") == true &&
$(yq -r '.transport.tlsServerNameRequired' "$contract") == true &&
$(yq -r '.transport.allowInsecure' "$contract") == false &&
$(yq -r '.transport.allowPlaintext' "$contract") == false &&
$(yq -r '.transport.allowCore' "$contract") == false ]] ||
  test::fail "Probe transport does not require pinned TLS or forbid core mode"

expected_ambient=(ALL_PROXY ARGOCD_AUTH_TOKEN ARGOCD_CONTEXT ARGOCD_OPTS ARGOCD_SERVER ARGOCD_SERVER_CRT HTTPS_PROXY HTTP_PROXY KUBECONFIG)
mapfile -t actual_ambient < <(yq -r '.forbiddenAmbientEnvironment[]' "$contract" | sort)
[[ ${actual_ambient[*]} == "${expected_ambient[*]}" ]] ||
  test::fail "ambient target or credential environment is not fully rejected"

expected_operations=(argocd.authorization argocd.client-version argocd.server-version argocd.subject http.anonymous-session-info)
mapfile -t actual_operations < <(yq -r '.allowedOperations[].name' "$contract" | sort)
[[ ${actual_operations[*]} == "${expected_operations[*]}" ]] ||
  test::fail "no-side-effect operation allowlist drifted"
authorization_argv=$(yq -o=json -I=0 \
  '.allowedOperations[] | select(.name == "argocd.authorization") | .argv' "$contract")
[[ $authorization_argv == '["account","can-i","<action>","<resource>","<object>"]' ]] ||
  test::fail "authorization probe is not constrained to account can-i"
for forbidden_flag in --core --insecure --plaintext; do
  [[ $(FLAG=$forbidden_flag yq '[.forbiddenArgocdFlags[] | select(. == strenv(FLAG))] | length' "$contract") -eq 1 ]] ||
    test::fail "forbidden Argo flag missing: ${forbidden_flag}"
done
for forbidden_command in 'app sync' 'app create' 'app delete' 'app patch' 'account generate-token' 'admin'; do
  [[ $(COMMAND=$forbidden_command yq '[.forbiddenArgocdCommands[] | select(. == strenv(COMMAND))] | length' "$contract") -eq 1 ]] ||
    test::fail "forbidden Argo operation missing: ${forbidden_command}"
done

[[ $(yq -o=json -I=0 '.kubernetesReadContract.verbs' "$contract") == '["get"]' &&
$(yq -o=json -I=0 '.kubernetesReadContract.nonResourceURLs' "$contract") == '["/version"]' &&
$(yq '[.kubernetesReadContract.objects[] | select(.kind == "Secret")] | length' "$contract") -eq 0 &&
$(yq '[.kubernetesReadContract.forbiddenResources[] | select(. == "secrets")] | length' "$contract") -eq 1 ]] ||
  test::fail "Kubernetes read contract is not exact-object GET only or permits Secrets"
expected_forbidden_verbs=(create delete deletecollection list patch update watch)
mapfile -t actual_forbidden_verbs < <(yq -r '.kubernetesReadContract.forbiddenVerbs[]' "$contract" | sort)
[[ ${actual_forbidden_verbs[*]} == "${expected_forbidden_verbs[*]}" ]] ||
  test::fail "Kubernetes mutation, list, or watch prohibition drifted"
[[ $(yq '.kubernetesReadContract.objects | length' "$contract") -eq 13 &&
$(yq '[.kubernetesReadContract.objects[] | [.apiVersion, .kind, .namespace, .name] | @tsv] | unique | length' "$contract") -eq 13 &&
$(yq '[.kubernetesReadContract.objects[] | select(.apiVersion == "v1" and .kind == "Namespace" and .namespace == "" and .name == "kube-system")] | length' "$contract") -eq 1 ]] ||
  test::fail "Kubernetes read object inventory is incomplete or duplicated"

expected_workloads=(
  Deployment/atlas-argocd-applicationset-controller
  Deployment/atlas-argocd-redis
  Deployment/atlas-argocd-repo-server
  Deployment/atlas-argocd-server
  StatefulSet/atlas-argocd-application-controller
)
mapfile -t actual_workloads < <(yq -r '.kubernetesReadContract.objects[] |
  select(.kind == "Deployment" or .kind == "StatefulSet") | .kind + "/" + .name' "$contract" | sort)
[[ ${actual_workloads[*]} == "${expected_workloads[*]}" ]] ||
  test::fail "Argo workload read inventory drifted"

derive_matrix() {
  local classification resource action decision status object query_action fine_grained probe_mode
  for classification in readOnly sideEffecting; do
    if [[ $classification == readOnly ]]; then
      decision=ALLOW
      status=READY
    else
      decision=DENY
      status=DENIED
    fi
    # shellcheck disable=SC2016 # yq expression, not a shell expansion
    while IFS=$'\t' read -r resource action; do
      [[ -n $resource && -n $action ]] || continue
      if [[ $classification == readOnly ]]; then status=READY; else status=DENIED; fi
      object=$(RESOURCE=$resource yq -r '.resourceObjects[strenv(RESOURCE)]' "$contract") || return 1
      [[ $object != null && -n $object ]] || return 1
      query_action=$action
      probe_mode=ARGO_ACCOUNT_CAN_I
      if [[ $action == */\* ]]; then
        fine_grained=true
        query_action=null
        status=UNSUPPORTED
        probe_mode=UNSUPPORTED
      else
        fine_grained=false
      fi
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
        "$resource" "$action" "$query_action" "$object" "$decision" "$status" "$probe_mode" "$fine_grained"
    done < <(CLASSIFICATION=$classification yq -r '.resources[] as $resource |
      $resource[strenv(CLASSIFICATION)][] | [$resource.name, .] | @tsv' "$action_inventory")
  done | sort
}

expected_matrix=$test_workspace/expected-matrix.tsv
actual_matrix=$test_workspace/actual-matrix.tsv
derive_matrix > "$expected_matrix" || test::fail "could not derive probe matrix from Action Inventory"
# shellcheck disable=SC2016 # yq variable, not a shell expansion
yq -r '.defaultProbeMode as $default | .entries[] | [.resource, .actionPattern, (.queryAction // "null"), .object,
  .expectedDecision, .expectedStatus, (.probeMode // $default), .requiresFineGrainedCapability] | @tsv' "$matrix" |
  sort > "$actual_matrix"
cmp -s "$actual_matrix" "$expected_matrix" ||
  test::fail "probe matrix differs from the complete Argo v3.5.1 Action Inventory"
[[ $(wc -l < "$actual_matrix" | tr -d ' ') -eq 36 &&
$(sort -u "$actual_matrix" | wc -l | tr -d ' ') -eq 36 &&
$(yq '.completeness.authorizationMatrixEntries' "$contract") -eq 36 &&
$(yq '[.entries[] | select(.expectedStatus == "UNSUPPORTED" and .queryAction == null and .probeMode == "UNSUPPORTED")] | length' "$matrix") -eq 3 &&
$(yq '[.entries[] | select(.expectedStatus != "UNSUPPORTED")] | length' "$matrix") -eq 33 ]] ||
  test::fail "probe matrix is incomplete or duplicated"

schema_keys() { yq -r '.required[]' "$1" | sort; }
document_keys() { yq -r 'keys | .[]' "$1" | sort; }
sha256_text() { shasum -a 256 | awk '{print $1}'; }

seal_target_document() {
  local input=$1 output=$2 projection sha
  projection=$(yq -o=json -I=0 'del(.approvedTargetDocumentSHA256) | sort_keys(..)' "$input") || return 1
  sha=$(printf '%s' "$projection" | sha256_text) || return 1
  APPROVED_SHA=$sha yq '.approvedTargetDocumentSHA256 = strenv(APPROVED_SHA)' "$input" > "$output"
}

bind_target_to_commit() {
  local input=$1 commit=$2 output=$3 draft=$test_workspace/target-binding-draft.json
  EXPECTED_COMMIT=$commit yq '.contractGitCommit = strenv(EXPECTED_COMMIT)' "$input" > "$draft" || return 1
  seal_target_document "$draft" "$output"
}

bind_evidence_to_target() {
  local input=$1 target=$2 output=$3 commit approved_sha
  commit=$(yq -r '.contractGitCommit' "$target") || return 1
  approved_sha=$(yq -r '.approvedTargetDocumentSHA256' "$target") || return 1
  EXPECTED_COMMIT=$commit APPROVED_SHA=$approved_sha yq \
    '.contractGitCommit = strenv(EXPECTED_COMMIT) |
    .target.approvedTargetDocumentSHA256 = strenv(APPROVED_SHA)' "$input" > "$output"
}

desired_object_projection() {
  yq -o=json -I=0 \
    '.desiredObjects | sort_by(.apiVersion, .kind, .namespace, .name) |
    (.. | select(tag == "!!map")) |= sort_keys(.)' "$1"
}

hydrate_desired_objects() {
  local source_root=$1 target=$2 combined=$test_workspace/desired-hydration.yaml
  local object_json api kind namespace name projection count sha
  (
    cd "$source_root" || exit 1
    {
      printf '%s\n' 'apiVersion: v1' 'kind: Namespace' 'metadata:' '  name: kube-system' '---'
      kubectl kustomize gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-self-base-overlay
      printf '%s\n' '---'
      kubectl kustomize gitops/platform/management/protection-foundation/definitions/argo-hardening
      printf '%s\n' '---'
      kubectl kustomize gitops/platform/management/projects
      printf '%s\n' '---'
      yq '.' bootstrap/argocd/atlas-bootstrap-project.yaml
      printf '%s\n' '---'
      helm template atlas-argocd vendor/charts/argo-cd-10.3.3 --namespace argocd --include-crds \
        --values gitops/platform/management/argocd-self/values.yaml \
        --values gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-values-hardening.yaml
    }
  ) > "$combined" || return 1
  while IFS= read -r object_json; do
    api=$(yq -r '.apiVersion' <<< "$object_json") || return 1
    kind=$(yq -r '.kind' <<< "$object_json") || return 1
    namespace=$(yq -r '.namespace' <<< "$object_json") || return 1
    name=$(yq -r '.name' <<< "$object_json") || return 1
    count=$(API=$api KIND=$kind NS=$namespace NAME=$name yq ea \
      '[select(.apiVersion == strenv(API) and .kind == strenv(KIND) and
      (.metadata.namespace // "") == strenv(NS) and .metadata.name == strenv(NAME))] | length' \
      "$combined" | tail -1) || return 1
    [[ $count -eq 1 ]] || return 1
    projection=$(API=$api KIND=$kind NS=$namespace NAME=$name yq ea -o=json -I=0 \
      'select(.apiVersion == strenv(API) and .kind == strenv(KIND) and
      (.metadata.namespace // "") == strenv(NS) and .metadata.name == strenv(NAME)) |
      del(.metadata.creationTimestamp,.metadata.resourceVersion,.metadata.uid,
      .metadata.generation,.metadata.managedFields,.status) | sort_keys(..)' "$combined") || return 1
    sha=$(printf '%s' "$projection" | sha256_text) || return 1
    printf '%s\t%s\t%s\t%s\t%s\n' "$api" "$kind" "$namespace" "$name" "$sha"
  done < <(yq -o=json -I=0 '.desiredObjects[]' "$target")
}

validate_target() {
  local target=$1 expected_commit=$2 payload fingerprint environment cluster kube_context api_server
  local server_address server_port tls_server_name desired_projection approved_projection
  local desired_sha approved_sha object_json target_identities contract_identities
  yq -e '.' "$target" > /dev/null || return 1
  [[ $(document_keys "$target") == "$(schema_keys "$target_schema")" ]] || return 1
  [[ $(yq -r '.schemaVersion' "$target") == 1 &&
  $(yq -r '.contractGitCommit' "$target") == "$expected_commit" &&
  $(yq -r '.authorityBaseline' "$target") == "$(yq -r '.authorityBaseline' "$contract")" &&
  $(yq -r '.repositoryURL' "$target") == https://github.com/snkio027/atlas.git &&
  $(yq -r '.argocdClientVersion' "$target") == "$argocd_version" &&
  $(yq -r '.argocdClientImage' "$target") == "$argocd_image" ]] || return 1
  environment=$(yq -r '.environmentName' "$target") || return 1
  cluster=$(yq -r '.clusterName' "$target") || return 1
  kube_context=$(yq -r '.kubeContext' "$target") || return 1
  api_server=$(yq -r '.apiServerURL' "$target") || return 1
  server_address=$(yq -r '.argocdServerAddress' "$target") || return 1
  tls_server_name=$(yq -r '.argocdTLSServerName' "$target") || return 1
  [[ $server_address =~ ^([A-Za-z0-9.-]+):([0-9]{1,5})$ ]] || return 1
  server_port=${BASH_REMATCH[2]}
  ((10#$server_port >= 1 && 10#$server_port <= 65535)) || return 1
  [[ $environment =~ ^[a-z][a-z0-9-]{0,31}$ &&
    $cluster =~ ^[a-z0-9][a-z0-9.-]{0,62}$ &&
    -n $kube_context && ${#kube_context} -le 253 && $kube_context != *$'\n'* &&
    $api_server =~ ^https://[^[:space:]]+$ &&
    $tls_server_name =~ ^[A-Za-z0-9.-]+$ &&
    $(yq -r '.contractGitCommit' "$target") =~ ^[0-9a-f]{40}$ &&
    $(yq -r '.kubeconfigPath' "$target") == /* &&
    $(yq -r '.kubeconfigSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.apiServerURL' "$target") == https://* &&
    $(yq -r '.kubeSystemNamespaceUID' "$target") =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ &&
    $(yq -r '.apiServerCASPKISHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.argocdServerCertificateSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.identityDecisionSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.expectedSubjectSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.expectedIssuerSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.expectedClaimsSHA256' "$target") =~ ^[0-9a-f]{64}$ &&
    $(yq -r '.credentialReferenceSHA256' "$target") =~ ^[0-9a-f]{64}$ ]] || return 1
  case $(yq -r '.identityCategory' "$target") in
    EXISTING_IDENTITY | TEMPORARY_PROBE_IDENTITY) ;;
    *) return 1 ;;
  esac
  printf -v payload 'authorityBaseline=%s\nenvironmentName=%s\nclusterName=%s\nkubeContext=%s\nkubeconfigSHA256=%s\napiServerURL=%s\nkubeSystemNamespaceUID=%s\napiServerCASPKISHA256=%s\nrepositoryURL=%s\nargocdServerAddress=%s\nargocdTLSServerName=%s\nargocdServerCertificateSHA256=%s\n' \
    "$(yq -r '.authorityBaseline' "$target")" "$(yq -r '.environmentName' "$target")" \
    "$(yq -r '.clusterName' "$target")" "$(yq -r '.kubeContext' "$target")" \
    "$(yq -r '.kubeconfigSHA256' "$target")" "$(yq -r '.apiServerURL' "$target")" \
    "$(yq -r '.kubeSystemNamespaceUID' "$target")" "$(yq -r '.apiServerCASPKISHA256' "$target")" \
    "$(yq -r '.repositoryURL' "$target")" "$(yq -r '.argocdServerAddress' "$target")" \
    "$(yq -r '.argocdTLSServerName' "$target")" "$(yq -r '.argocdServerCertificateSHA256' "$target")"
  fingerprint=$(printf '%s' "$payload" | shasum -a 256 | awk '{print $1}') || return 1
  [[ $fingerprint == "$(yq -r '.targetFingerprintSHA256' "$target")" ]] || return 1
  [[ $(yq '.desiredObjects | length' "$target") -eq 13 ]] || return 1
  while IFS= read -r object_json; do
    [[ $(yq -o=json -I=0 'keys | sort' <<< "$object_json") == '["apiVersion","desiredProjectionSHA256","kind","name","namespace"]' &&
    $(yq -r '.desiredProjectionSHA256' <<< "$object_json") =~ ^[0-9a-f]{64}$ ]] || return 1
  done < <(yq -o=json -I=0 '.desiredObjects[]' "$target")
  [[ $(yq '[.desiredObjects[] | [.apiVersion,.kind,.namespace,.name] | @tsv] | unique | length' "$target") -eq 13 ]] || return 1
  target_identities=$(yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name] | @tsv' "$target" | sort) || return 1
  contract_identities=$(yq -r '.kubernetesReadContract.objects[] | [.apiVersion,.kind,.namespace,.name] | @tsv' "$contract" | sort) || return 1
  [[ $target_identities == "$contract_identities" ]] || return 1
  desired_projection=$(desired_object_projection "$target") || return 1
  desired_sha=$(printf '%s' "$desired_projection" | sha256_text) || return 1
  [[ $desired_sha == "$(yq -r '.desiredProjectionSHA256' "$target")" ]] || return 1
  approved_projection=$(yq -o=json -I=0 'del(.approvedTargetDocumentSHA256) | sort_keys(..)' "$target") || return 1
  approved_sha=$(printf '%s' "$approved_projection" | sha256_text) || return 1
  [[ $approved_sha == "$(yq -r '.approvedTargetDocumentSHA256' "$target")" ]]
}

bound_target=$test_workspace/valid-target.json
bound_evidence=$test_workspace/valid-evidence.json
bind_target_to_commit "$valid_target" "$expected_contract_commit" "$bound_target" ||
  test::fail "could not bind target fixture to the caller-approved commit"
bind_evidence_to_target "$valid_evidence" "$bound_target" "$bound_evidence" ||
  test::fail "could not bind evidence fixture to the approved target"
validate_target "$bound_target" "$expected_contract_commit" || test::fail "valid target-bound fixture was rejected"
expected_desired_objects=$test_workspace/expected-desired-objects.tsv
hydrated_desired_objects=$test_workspace/hydrated-desired-objects.tsv
yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256] | @tsv' "$bound_target" |
  sort > "$expected_desired_objects"
hydrate_desired_objects "$approved_checkout" "$bound_target" | sort > "$hydrated_desired_objects" ||
  test::fail "could not hydrate the reviewed Git desired projection"
cmp -s "$expected_desired_objects" "$hydrated_desired_objects" ||
  test::fail "reviewed desired projection differs from exact Git hydration"
for mutation in authority-baseline commit-resealed http-api relative-kubeconfig client-version client-image bad-environment bad-cluster bad-port server-name-drift identity-decision subject issuer claims credential desired-object missing-object approved-document extra-field; do
  mutated_target=$test_workspace/target-${mutation}.json
  case $mutation in
    authority-baseline) yq '.authorityBaseline = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' "$bound_target" > "$mutated_target" ;;
    commit-resealed)
      commit_draft=$test_workspace/target-commit-resealed-draft.json
      yq '.contractGitCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' "$bound_target" > "$commit_draft"
      seal_target_document "$commit_draft" "$mutated_target"
      ;;
    http-api) yq '.apiServerURL = "http://127.0.0.1:6443"' "$bound_target" > "$mutated_target" ;;
    relative-kubeconfig) yq '.kubeconfigPath = "fixture.kubeconfig"' "$bound_target" > "$mutated_target" ;;
    client-version) yq '.argocdClientVersion = "3.5.2"' "$bound_target" > "$mutated_target" ;;
    client-image) yq '.argocdClientImage = "ambient"' "$bound_target" > "$mutated_target" ;;
    bad-environment) yq '.environmentName = "TEST"' "$bound_target" > "$mutated_target" ;;
    bad-cluster) yq '.clusterName = "bad_cluster"' "$bound_target" > "$mutated_target" ;;
    bad-port) yq '.argocdServerAddress = "argocd.fixture.invalid:65536"' "$bound_target" > "$mutated_target" ;;
    server-name-drift) yq '.argocdTLSServerName = "other.fixture.invalid"' "$bound_target" > "$mutated_target" ;;
    identity-decision) yq '.identityDecisionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    subject) yq '.expectedSubjectSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    issuer) yq '.expectedIssuerSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    claims) yq '.expectedClaimsSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    credential) yq '.credentialReferenceSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    desired-object) yq '.desiredObjects[0].desiredProjectionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    missing-object) yq 'del(.desiredObjects[-1])' "$bound_target" > "$mutated_target" ;;
    approved-document) yq '.approvedTargetDocumentSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_target" > "$mutated_target" ;;
    extra-field) yq '.insecure = true' "$bound_target" > "$mutated_target" ;;
  esac
  if validate_target "$mutated_target" "$expected_contract_commit"; then test::fail "unsafe target fixture was accepted: ${mutation}"; fi
done

contains_sensitive_output() {
  local output=$1 pattern
  while IFS= read -r pattern; do
    if [[ $pattern == '(?i)'* ]]; then
      pattern=${pattern#'(?i)'}
      grep -Eiq -- "$pattern" <<< "$output" && return 0
    else
      grep -Eq -- "$pattern" <<< "$output" && return 0
    fi
  done < <(yq -r '.redaction.forbiddenOutputPatterns[]' "$contract")
  return 1
}

classify_case() {
  local case_json=$1 kind stdout stderr stdout_json stderr_json actual expected expected_count executed skipped
  kind=$(yq -r '.kind' <<< "$case_json") || return 1
  case $kind in
    canI)
      [[ $(yq -r '.knownAction' <<< "$case_json") == true ]] || {
        printf 'DRIFTED\n'
        return
      }
      [[ $(yq -r '.fineGrainedCapability' <<< "$case_json") == SUPPORTED ]] || {
        printf 'UNSUPPORTED\n'
        return
      }
      stdout=$(yq -r '.stdout' <<< "$case_json") || return 1
      stderr=$(yq -r '.stderr' <<< "$case_json") || return 1
      stdout_json=$(yq -o=json -I=0 '.stdout' <<< "$case_json") || return 1
      stderr_json=$(yq -o=json -I=0 '.stderr' <<< "$case_json") || return 1
      if contains_sensitive_output "${stdout}"$'\n'"${stderr}" ||
        [[ $(yq -r '.transport' <<< "$case_json") != OK ||
        $(yq -r '.grpcStatus' <<< "$case_json") != OK ||
        $(yq -r '.exitCode' <<< "$case_json") -ne 0 || $stderr_json != '""' ]]; then
        printf 'INVALID\n'
        return
      fi
      case $stdout_json in '"yes\n"') actual=ALLOW ;; '"no\n"') actual=DENY ;; *)
        printf 'INVALID\n'
        return
        ;;
      esac
      expected=$(yq -r '.expectedDecision' <<< "$case_json") || return 1
      [[ $actual == "$expected" ]] || {
        printf 'DRIFTED\n'
        return
      }
      if [[ $actual == ALLOW ]]; then printf 'READY\n'; else printf 'DENIED\n'; fi
      ;;
    target | version | inheritance)
      if [[ $(yq -r '.matches' <<< "$case_json") == true ]]; then printf 'READY\n'; else printf 'DRIFTED\n'; fi
      ;;
    policyFragments)
      if [[ $(yq -o=json -I=0 '.expected | sort' <<< "$case_json") == "$(yq -o=json -I=0 '.actual | sort' <<< "$case_json")" ]]; then printf 'READY\n'; else printf 'DRIFTED\n'; fi
      ;;
    json)
      if yq -e '.' <<< "$(yq -r '.payload' <<< "$case_json")" > /dev/null 2>&1; then printf 'READY\n'; else printf 'INVALID\n'; fi
      ;;
    completeness)
      expected_count=$(yq -r '.expected' <<< "$case_json") || return 1
      executed=$(yq -r '.executed' <<< "$case_json") || return 1
      skipped=$(yq -r '.skipped' <<< "$case_json") || return 1
      if [[ $expected_count -eq "$executed" && $skipped -eq 0 ]]; then printf 'READY\n'; else printf 'INVALID\n'; fi
      ;;
    state)
      if [[ $(yq -r '.before' <<< "$case_json") == "$(yq -r '.after' <<< "$case_json")" ]]; then printf 'READY\n'; else printf 'DRIFTED\n'; fi
      ;;
    output)
      stdout=$(yq -r '.stdout' <<< "$case_json") || return 1
      stderr=$(yq -r '.stderr' <<< "$case_json") || return 1
      if contains_sensitive_output "${stdout}"$'\n'"${stderr}"; then printf 'INVALID\n'; else printf 'READY\n'; fi
      ;;
    adminProof)
      if [[ $(yq -r '.credentialAvailable' <<< "$case_json") == false ]]; then
        printf 'ADMIN_PROOF_UNAVAILABLE\n'
      else
        printf 'INVALID\n'
      fi
      ;;
    *) return 1 ;;
  esac
}

[[ $(yq '.cases | length' "$cases") -ge 19 ]] || test::fail "negative classifier fixture inventory is incomplete"
while IFS= read -r case_json; do
  case_name=$(yq -r '.name' <<< "$case_json") || test::fail "classifier case has no name"
  expected_status=$(yq -r '.expectedStatus' <<< "$case_json") || test::fail "classifier case has no expected status"
  actual_status=$(classify_case "$case_json") || test::fail "classifier case could not be evaluated: ${case_name}"
  [[ $actual_status == "$expected_status" ]] ||
    test::fail "classifier case ${case_name} returned ${actual_status}, expected ${expected_status}"
done < <(yq -o=json -I=0 '.cases[]' "$cases")

validate_evidence() {
  local evidence=$1 target=$2 key lower_key forbidden_key expected_records actual_records
  local identity_check_projection expected_identity_checks started_at completed_at sha_path sha_value
  local expected_objects actual_objects expected_decision actual_decision status exit_code stdout_sha stderr_sha
  yq -e '.' "$evidence" > /dev/null || return 1
  [[ $(document_keys "$evidence") == "$(schema_keys "$evidence_schema")" ]] || return 1
  [[ $(yq -o=json -I=0 '.target | keys | sort' "$evidence") == '["apiServerCASPKISHA256","approvedTargetDocumentSHA256","argocdServerCertificateSHA256","clusterName","desiredProjectionSHA256","environmentName","targetFingerprintSHA256"]' &&
  $(yq -o=json -I=0 '.client | keys | sort' "$evidence") == '["digest","image","version"]' &&
  $(yq -o=json -I=0 '.server | keys | sort' "$evidence") == '["certificateSHA256","tlsServerName","version"]' &&
  $(yq -o=json -I=0 '.identity | keys | sort' "$evidence") == '["category","claimsSHA256","credentialReferenceSHA256","identityDecisionSHA256","issuerSHA256","subjectSHA256"]' &&
  $(yq -o=json -I=0 '.liveProjection | keys | sort' "$evidence") == '["desiredProjectionSHA256","liveAfterSHA256","liveBeforeSHA256","objects","reviewedPolicyFragments","status"]' &&
  $(yq -o=json -I=0 '.completeness | keys | sort' "$evidence") == '["ambiguous","executed","expected","identityExecuted","identityExpected","identityUnavailable","mutations","skipped","unsupported"]' ]] || return 1
  while IFS= read -r probe_record; do
    [[ $(yq -o=json -I=0 'keys | sort' <<< "$probe_record") == '["actionPattern","actualDecision","exitCode","expectedDecision","object","queryAction","resource","status","stderrSHA256","stdoutSHA256"]' ]] || return 1
  done < <(yq -o=json -I=0 '.authorizationProbes[]' "$evidence")
  while IFS= read -r identity_record; do
    [[ $(yq -o=json -I=0 'keys | sort' <<< "$identity_record") == '["execution","name","reason","status"]' ]] || return 1
  done < <(yq -o=json -I=0 '.identityChecks[]' "$evidence")
  while IFS= read -r object_record; do
    [[ $(yq -o=json -I=0 'keys | sort' <<< "$object_record") == '["apiVersion","desiredProjectionSHA256","kind","liveAfterProjectionSHA256","liveBeforeProjectionSHA256","name","namespace"]' ]] || return 1
  done < <(yq -o=json -I=0 '.liveProjection.objects[]' "$evidence")
  started_at=$(yq -r '.startedAt' "$evidence") || return 1
  completed_at=$(yq -r '.completedAt' "$evidence") || return 1
  [[ $(yq -r '.contractGitCommit' "$evidence") =~ ^[0-9a-f]{40}$ &&
  $(yq -r '.sessionID' "$evidence") =~ ^argo-authz-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{32}$ &&
  $started_at =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ &&
  $completed_at =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ &&
  $started_at < "$completed_at" &&
  $(yq -r '.target.environmentName' "$evidence") =~ ^[a-z][a-z0-9-]{0,31}$ &&
  $(yq -r '.target.clusterName' "$evidence") =~ ^[a-z0-9][a-z0-9.-]{0,62}$ &&
  $(yq -r '.server.tlsServerName' "$evidence") =~ ^[A-Za-z0-9.-]+$ ]] || return 1
  case $(yq -r '.identity.category' "$evidence") in
    EXISTING_IDENTITY | TEMPORARY_PROBE_IDENTITY) ;;
    *) return 1 ;;
  esac
  for sha_path in \
    .target.targetFingerprintSHA256 \
    .target.apiServerCASPKISHA256 \
    .target.argocdServerCertificateSHA256 \
    .target.approvedTargetDocumentSHA256 \
    .target.desiredProjectionSHA256 \
    .server.certificateSHA256 \
    .identity.identityDecisionSHA256 \
    .identity.credentialReferenceSHA256 \
    .identity.subjectSHA256 \
    .identity.issuerSHA256 \
    .identity.claimsSHA256 \
    .liveProjection.desiredProjectionSHA256 \
    .liveProjection.liveBeforeSHA256 \
    .liveProjection.liveAfterSHA256; do
    sha_value=$(yq -r "$sha_path" "$evidence") || return 1
    [[ $sha_value =~ ^[0-9a-f]{64}$ ]] || return 1
  done
  [[ $(yq -r '.schemaVersion' "$evidence") == 1 &&
  $(yq -r '.contractID' "$evidence") == "$(yq -r '.contractID' "$contract")" &&
  $(yq -r '.authorityBaseline' "$evidence") == "$(yq -r '.authorityBaseline' "$contract")" &&
  $(yq -r '.client.version' "$evidence") == "$argocd_version" &&
  $(yq -r '.client.image' "$evidence") == "$argocd_image" &&
  $(yq -r '.client.digest' "$evidence") == "$argocd_digest" &&
  $(yq -r '.server.version' "$evidence") == "$argocd_version" &&
  $(yq -r '.server.certificateSHA256' "$evidence") == "$(yq -r '.target.argocdServerCertificateSHA256' "$evidence")" &&
  $(yq -r '.server.tlsServerName' "$evidence") == "$(yq -r '.argocdTLSServerName' "$target")" &&
  $(yq -r '.result' "$evidence") == UNSUPPORTED ]] || return 1
  [[ $(yq -r '.contractGitCommit' "$evidence") == "$(yq -r '.contractGitCommit' "$target")" &&
  $(yq -r '.target.environmentName' "$evidence") == "$(yq -r '.environmentName' "$target")" &&
  $(yq -r '.target.clusterName' "$evidence") == "$(yq -r '.clusterName' "$target")" &&
  $(yq -r '.target.targetFingerprintSHA256' "$evidence") == "$(yq -r '.targetFingerprintSHA256' "$target")" &&
  $(yq -r '.target.apiServerCASPKISHA256' "$evidence") == "$(yq -r '.apiServerCASPKISHA256' "$target")" &&
  $(yq -r '.target.argocdServerCertificateSHA256' "$evidence") == "$(yq -r '.argocdServerCertificateSHA256' "$target")" &&
  $(yq -r '.target.approvedTargetDocumentSHA256' "$evidence") == "$(yq -r '.approvedTargetDocumentSHA256' "$target")" &&
  $(yq -r '.target.desiredProjectionSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
  $(yq -r '.identity.category' "$evidence") == "$(yq -r '.identityCategory' "$target")" &&
  $(yq -r '.identity.identityDecisionSHA256' "$evidence") == "$(yq -r '.identityDecisionSHA256' "$target")" &&
  $(yq -r '.identity.credentialReferenceSHA256' "$evidence") == "$(yq -r '.credentialReferenceSHA256' "$target")" &&
  $(yq -r '.identity.subjectSHA256' "$evidence") == "$(yq -r '.expectedSubjectSHA256' "$target")" &&
  $(yq -r '.identity.issuerSHA256' "$evidence") == "$(yq -r '.expectedIssuerSHA256' "$target")" &&
  $(yq -r '.identity.claimsSHA256' "$evidence") == "$(yq -r '.expectedClaimsSHA256' "$target")" ]] || return 1
  [[ $(yq -r '.liveProjection.desiredProjectionSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
  $(yq -r '.liveProjection.liveBeforeSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
  $(yq -r '.liveProjection.liveAfterSHA256' "$evidence") == "$(yq -r '.desiredProjectionSHA256' "$target")" &&
  $(yq -r '.liveProjection.status' "$evidence") == MATCH &&
  $(yq -o=json -I=0 '.liveProjection.reviewedPolicyFragments' "$evidence") == '["policy.csv"]' ]] || return 1
  [[ $(yq '.liveProjection.objects | length' "$evidence") -eq 13 &&
  $(yq '[.liveProjection.objects[] | [.apiVersion,.kind,.namespace,.name] | @tsv] | unique | length' "$evidence") -eq 13 ]] || return 1
  expected_objects=$test_workspace/evidence-expected-objects.tsv
  actual_objects=$test_workspace/evidence-actual-objects.tsv
  yq -r '.desiredObjects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256,
    .desiredProjectionSHA256,.desiredProjectionSHA256] | @tsv' "$target" | sort > "$expected_objects"
  yq -r '.liveProjection.objects[] | [.apiVersion,.kind,.namespace,.name,.desiredProjectionSHA256,
    .liveBeforeProjectionSHA256,.liveAfterProjectionSHA256] | @tsv' "$evidence" | sort > "$actual_objects"
  cmp -s "$actual_objects" "$expected_objects" || return 1
  [[ $(yq '.authorizationProbes | length' "$evidence") -eq 36 &&
  $(yq '.completeness.expected' "$evidence") -eq 36 &&
  $(yq '.completeness.executed' "$evidence") -eq 33 &&
  $(yq '.completeness.unsupported' "$evidence") -eq 3 &&
  $(yq '.completeness.skipped' "$evidence") -eq 0 &&
  $(yq '.completeness.ambiguous' "$evidence") -eq 0 &&
  $(yq '.completeness.mutations' "$evidence") -eq 0 &&
  $(yq '.completeness.identityExpected' "$evidence") -eq 3 &&
  $(yq '.completeness.identityExecuted' "$evidence") -eq 2 &&
  $(yq '.completeness.identityUnavailable' "$evidence") -eq 1 ]] || return 1
  expected_records=$test_workspace/evidence-expected.tsv
  actual_records=$test_workspace/evidence-actual.tsv
  yq -r '.entries[] | [.resource, .actionPattern, (.queryAction // "null"), .object, .expectedDecision, .expectedStatus] | @tsv' "$matrix" | sort > "$expected_records"
  yq -r '.authorizationProbes[] | [.resource, .actionPattern, (.queryAction // "null"), .object, .expectedDecision, .status] | @tsv' "$evidence" | sort > "$actual_records"
  cmp -s "$actual_records" "$expected_records" || return 1
  [[ $(sort -u "$actual_records" | wc -l | tr -d ' ') -eq 36 ]] || return 1
  while IFS=$'\t' read -r expected_decision actual_decision status exit_code stdout_sha stderr_sha; do
    if [[ $status == UNSUPPORTED ]]; then
      [[ $expected_decision == DENY && $actual_decision == UNPROVEN && $exit_code == null &&
        $stdout_sha == e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 &&
        $stderr_sha == e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 ]] || return 1
    else
      [[ $expected_decision == "$actual_decision" && $exit_code -eq 0 &&
        $stderr_sha == e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855 ]] || return 1
      if [[ $actual_decision == ALLOW ]]; then
        [[ $status == READY && $stdout_sha == 5040625b1fb6fa4af07226683f6e6003b29e5e70b16f8cfb24be7a752393f0ee ]] || return 1
      else
        [[ $status == DENIED && $stdout_sha == 564739ea8fa5926d4fa5c9734fed462061960a22e6b8d5c06e94969d97891bf2 ]] || return 1
      fi
    fi
  done < <(yq -r '.authorizationProbes[] | [.expectedDecision, .actualDecision, .status, (.exitCode // "null"), .stdoutSHA256, .stderrSHA256] | @tsv' "$evidence")
  identity_check_projection=$(yq -r \
    '.identityChecks[] | [.name, .status, .reason, .execution] | @tsv' "$evidence" | sort) || return 1
  expected_identity_checks=$'anonymous\tDENIED\tUNAUTHENTICATED\tEXECUTED\nauthenticated-subject\tREADY\tAUTHENTICATED\tEXECUTED\nbuilt-in-admin\tADMIN_PROOF_UNAVAILABLE\tCREDENTIAL_UNAVAILABLE\tNOT_EXECUTED'
  [[ $identity_check_projection == "$expected_identity_checks" ]] || return 1
  while IFS= read -r key; do
    lower_key=${key,,}
    while IFS= read -r forbidden_key; do
      [[ $lower_key != "${forbidden_key,,}" ]] || return 1
    done < <(yq -r '.redaction.forbiddenFieldNames[]' "$contract")
  done < <(yq -r '.. | select(tag == "!!map") | keys | .[]' "$evidence")
  ! grep -Eq '"/(Users|home|private|fixture)/' "$evidence" || return 1
}

validate_evidence "$bound_evidence" "$bound_target" || test::fail "valid failure-closed evidence fixture was rejected"
for mutation in missing-probe changed-state stable-drift missing-live-object replaced-live-object per-object-drift commit tls-server-name identity-decision credential subject issuer claims approved-document bad-session time-reversal malformed-hash extra-secret-field nested-extra-field probe-extra-field host-path; do
  mutated_evidence=$test_workspace/evidence-${mutation}.json
  case $mutation in
    missing-probe) yq 'del(.authorizationProbes[-1]) | .completeness.executed = 32' "$bound_evidence" > "$mutated_evidence" ;;
    changed-state) yq '.liveProjection.liveAfterSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    stable-drift) yq '.liveProjection.desiredProjectionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" |
      .liveProjection.liveBeforeSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" |
      .liveProjection.liveAfterSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    missing-live-object) yq 'del(.liveProjection.objects[-1])' "$bound_evidence" > "$mutated_evidence" ;;
    replaced-live-object) yq '.liveProjection.objects[0].name = "replacement"' "$bound_evidence" > "$mutated_evidence" ;;
    per-object-drift) yq '.liveProjection.objects[0].liveBeforeProjectionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" |
      .liveProjection.objects[0].liveAfterProjectionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    commit) yq '.contractGitCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"' "$bound_evidence" > "$mutated_evidence" ;;
    tls-server-name) yq '.server.tlsServerName = "other.fixture.invalid"' "$bound_evidence" > "$mutated_evidence" ;;
    identity-decision) yq '.identity.identityDecisionSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    credential) yq '.identity.credentialReferenceSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    subject) yq '.identity.subjectSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    issuer) yq '.identity.issuerSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    claims) yq '.identity.claimsSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    approved-document) yq '.target.approvedTargetDocumentSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "$bound_evidence" > "$mutated_evidence" ;;
    bad-session) yq '.sessionID = "argo-authz-invalid"' "$bound_evidence" > "$mutated_evidence" ;;
    time-reversal) yq '.completedAt = "2026-08-26T23:59:59Z"' "$bound_evidence" > "$mutated_evidence" ;;
    malformed-hash) yq '.identity.claimsSHA256 = "not-a-sha"' "$bound_evidence" > "$mutated_evidence" ;;
    extra-secret-field) yq '.token = "ATLAS_FIXTURE_NOT_A_CREDENTIAL"' "$bound_evidence" > "$mutated_evidence" ;;
    nested-extra-field) yq '.client.source = "ambient"' "$bound_evidence" > "$mutated_evidence" ;;
    probe-extra-field) yq '.authorizationProbes[0].diagnostic = "ignored"' "$bound_evidence" > "$mutated_evidence" ;;
    host-path) yq '.target.hostPath = "/fixture/not-a-real-host/evidence"' "$bound_evidence" > "$mutated_evidence" ;;
  esac
  if validate_evidence "$mutated_evidence" "$bound_target"; then test::fail "invalid evidence fixture was accepted: ${mutation}"; fi
done

[[ $(yq -r '.additionalProperties' "$target_schema") == false &&
$(yq -r '.additionalProperties' "$evidence_schema") == false &&
$(yq -r '.properties.authorizationProbes.minItems' "$evidence_schema") -eq 36 &&
$(yq -r '.properties.authorizationProbes.maxItems' "$evidence_schema") -eq 36 &&
$(yq -o=json -I=0 '.classification.recordStatuses' "$contract") == '["READY","DENIED","DRIFTED","UNSUPPORTED","ADMIN_PROOF_UNAVAILABLE","INVALID"]' &&
$(yq -o=json -I=0 '.classification.overallExitCodes' "$contract") == '{"READY":0,"DRIFTED":20,"UNSUPPORTED":21,"INVALID":22}' ]] ||
  test::fail "Probe status, exit, or schema completeness contract drifted"

forbidden_live_client_expression='(^|[;&|][[:space:]]*)argocd[[:space:]]|kubectl[[:space:]]+(get|create|apply|patch|delete|auth|version|config|exec|port-forward)[[:space:]]'
if rg -n "$forbidden_live_client_expression" "${BASH_SOURCE[0]}"; then
  test::fail "repository-only Probe Contract test invokes a live client"
fi

test::pass "repository-only target-bound Argo authorization Probe Contract"
