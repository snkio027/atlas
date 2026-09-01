#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly root=gitops/platform/management/protection-foundation/definitions/argo-hardening
readonly action_inventory=$root/argo-freeze-action-inventory.json
readonly authority_inventory=$root/argo-authority-inventory.json
readonly hardening_values=$root/argocd-values-hardening.yaml
readonly current_values=gitops/platform/management/argocd-self/values.yaml
readonly chart=vendor/charts/argo-cd-10.3.3
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-argo-hardening.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

render=$(kubectl kustomize "$root")
comparison_render=$(kubectl kustomize "$root")
cmp -s <(printf '%s\n' "$render") <(printf '%s\n' "$comparison_render") ||
  test::fail "Argo hardening projection is not deterministic"
[[ $(yq ea '[.] | length' <<< "$render") -eq 1 &&
$(yq ea -r 'select(.) | .kind' <<< "$render") == Application ]] ||
  test::fail "Argo hardening entrypoint redeclares a Chart or Kustomize-owned child resource"

for environment in local-orbstack prod; do
  environment_render=$(kubectl kustomize "gitops/platform/applications/overlays/${environment}")
  if [[ $environment == local-orbstack ]]; then
    normalized_environment_render=$(yq '
      (select(.kind == "Application" and .metadata.name == "argocd-self") |
        .spec.sources[1].path) =
          "gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-self-base-overlay"
    ' <<< "$environment_render")
    normalized_environment_json=$(yq -o=json -I=0 'sort_keys(..)' <<< "$normalized_environment_render")
    reviewed_hardening_json=$(yq -o=json -I=0 'sort_keys(..)' <<< "$render")
    [[ $normalized_environment_json == "$reviewed_hardening_json" ]] ||
      test::fail "local-orbstack changes Argo hardening outside the approved observation owner path"
  else
    [[ $environment_render == "$render" ]] ||
      test::fail "${environment} does not render the reviewed Argo hardening projection"
  fi
done
[[ $(yq '.configs.cm.create == false' "$hardening_values") == true ]] ||
  test::fail "Argo hardening values attempt to duplicate the Kustomize-owned argocd-cm"
argocd_version=$(awk -F= '$1 == "ARGOCD_VERSION" {print $2}' versions.lock)
[[ $(yq -r '.argocdVersion' "$action_inventory") == "$argocd_version" &&
$(yq -r '.argocdVersion' "$authority_inventory") == "$argocd_version" ]] ||
  test::fail "Argo authority inventory differs from versions.lock"
[[ $(yq -r '.fineGrainedApplicationInheritanceDisabled' "$action_inventory") == true &&
$(yq -r '.unknownClassification' "$action_inventory") == AUTHORITY_DRIFTED &&
$(yq -r '.unknownResourceActionOrInheritance' "$authority_inventory") == AUTHORITY_DRIFTED ]] ||
  test::fail "unknown Argo authority does not fail closed"

expected_resources=(
  accounts applications applicationsets certificates clusters exec extensions gpgkeys logs projects repositories
)
mapfile -t actual_resources < <(yq -r '.resources[].name' "$action_inventory" | sort)
[[ ${actual_resources[*]} == "${expected_resources[*]}" ]] ||
  test::fail "Argo v3.5.1 Action Inventory resource surface drifted"

assert_actions() {
  local resource=$1 field=$2 expected=$3 actual
  actual=$(RESOURCE=$resource FIELD=$field yq -o=json -I=0 \
    '.resources[] | select(.name == strenv(RESOURCE)) | .[strenv(FIELD)]' "$action_inventory")
  [[ $actual == "$expected" ]] || test::fail "Argo ${resource} ${field} actions drifted"
}

assert_actions applications readOnly '["get"]'
assert_actions applications sideEffecting '["create","update","update/*","delete","delete/*","sync","override","action/*"]'
assert_actions applicationsets readOnly '["get"]'
assert_actions applicationsets sideEffecting '["create","update","delete"]'
for resource in clusters projects repositories; do
  assert_actions "$resource" readOnly '["get"]'
  assert_actions "$resource" sideEffecting '["create","update","delete"]'
done
assert_actions accounts readOnly '["get"]'
assert_actions accounts sideEffecting '["update"]'
for resource in certificates gpgkeys; do
  assert_actions "$resource" readOnly '["get"]'
  assert_actions "$resource" sideEffecting '["create","delete"]'
done
assert_actions logs readOnly '["get"]'
assert_actions logs sideEffecting '[]'
assert_actions exec readOnly '[]'
assert_actions exec sideEffecting '["create"]'
assert_actions extensions readOnly '[]'
assert_actions extensions sideEffecting '["invoke"]'

classify_action() {
  local resource=$1 action=$2 candidate pattern
  while IFS= read -r candidate; do
    [[ $candidate == "$action" ]] && {
      printf 'READ_ONLY\n'
      return
    }
  done < <(RESOURCE=$resource yq -r '.resources[] | select(.name == strenv(RESOURCE)) | .readOnly[]' "$action_inventory")
  while IFS= read -r pattern; do
    if [[ $pattern == */\* && $action == "${pattern%\*}"* || $pattern == "$action" ]]; then
      printf 'SIDE_EFFECTING\n'
      return
    fi
  done < <(RESOURCE=$resource yq -r '.resources[] | select(.name == strenv(RESOURCE)) | .sideEffecting[]' "$action_inventory")
  printf 'AUTHORITY_DRIFTED\n'
}

classify_authority_context() {
  local version=$1 inheritance_disabled=$2
  [[ $version == "$argocd_version" && $inheritance_disabled == true ]] || {
    printf 'AUTHORITY_DRIFTED\n'
    return
  }
  printf 'MATCH\n'
}

policy_fragments_are_reviewed() {
  local config_map=$1 actual_fragments expected_fragments
  actual_fragments=$(yq -r '
    .data | keys | .[] |
    select(. == "policy.csv" or test("^policy\\..*\\.csv$"))
  ' <<< "$config_map" | sort) || return 1
  expected_fragments=$(yq -r '.reviewedPolicyFragments[]' "$authority_inventory" | sort) || return 1
  [[ $actual_fragments == "$expected_fragments" ]]
}

[[ $(classify_action applications get) == READ_ONLY &&
$(classify_action applications action/restart) == SIDE_EFFECTING &&
$(classify_action applications future-action) == AUTHORITY_DRIFTED &&
$(classify_action future-resource get) == AUTHORITY_DRIFTED ]] ||
  test::fail "Argo Action Inventory classifier is not failure closed"
[[ $(classify_authority_context "$argocd_version" true) == MATCH &&
$(classify_authority_context 3.5.2 true) == AUTHORITY_DRIFTED &&
$(classify_authority_context "$argocd_version" false) == AUTHORITY_DRIFTED ]] ||
  test::fail "Argo version or inheritance drift does not fail closed"

candidate_app=$(NAME=argocd-self yq ea -o=json -I=0 \
  'select(.kind == "Application" and .metadata.name == strenv(NAME))' <<< "$render")
live_app=$(kubectl kustomize gitops/platform/applications/base | NAME=argocd-self yq ea -o=json -I=0 \
  'select(.kind == "Application" and .metadata.name == strenv(NAME))')

[[ -n $candidate_app && -n $live_app ]] || test::fail "argocd-self owner projection is missing"
[[ $(yq ea '[select(.kind == "Application")] | length' <<< "$render") -eq 1 ]] ||
  test::fail "Argo hardening does not retain a single argocd-self Application"
approved_app_delta='del(.spec.sources[0].helm.valueFiles) |
  del(.spec.sources[1].path) |
  del(.spec.ignoreDifferences) |
  del(.spec.syncPolicy.syncOptions) |
  sort_keys(..)'
live_app_remainder=$(yq -o=json -I=0 "$approved_app_delta" <<< "$live_app")
candidate_app_remainder=$(yq -o=json -I=0 "$approved_app_delta" <<< "$candidate_app")
[[ $candidate_app_remainder == "$live_app_remainder" ]] ||
  test::fail "argocd-self candidate changes fields outside the approved ownership increment"
[[ $(yq -r '.metadata.annotations."argocd.argoproj.io/sync-wave"' <<< "$candidate_app") == -90 &&
$(yq -r '.metadata.annotations."argocd.argoproj.io/sync-options"' <<< "$candidate_app") == Prune=confirm ]] ||
  test::fail "argocd-self candidate dropped its Tier-1 ordering or prune gate"
[[ $(yq '.spec.sources[0].helm.valueFiles | length' <<< "$candidate_app") -eq 2 &&
$(yq -r '.spec.sources[0].helm.valueFiles[0]' <<< "$candidate_app") == "\$values/gitops/platform/management/argocd-self/values.yaml" &&
$(yq -r '.spec.sources[0].helm.valueFiles[1]' <<< "$candidate_app") == "\$values/gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-values-hardening.yaml" &&
$(yq -r '.spec.sources[1].path' <<< "$candidate_app") == gitops/platform/management/protection-foundation/definitions/argo-hardening/argocd-self-base-overlay ]] ||
  test::fail "argocd-self candidate does not delegate changes to the existing owners"

sync_options=$(yq -o=json -I=0 '.spec.syncPolicy.syncOptions | sort' <<< "$candidate_app")
[[ $sync_options == '["ApplyOutOfSyncOnly=true","FailOnSharedResource=true","RespectIgnoreDifferences=true","ServerSideApply=true"]' ]] ||
  test::fail "argocd-self candidate does not fail on shared resources"

[[ $(yq '.spec.ignoreDifferences | length' <<< "$candidate_app") -eq 1 &&
$(yq -r '.spec.ignoreDifferences[0].group' <<< "$candidate_app") == "" &&
$(yq -r '.spec.ignoreDifferences[0].kind' <<< "$candidate_app") == ConfigMap &&
$(yq -r '.spec.ignoreDifferences[0].namespace' <<< "$candidate_app") == argocd &&
$(yq -r '.spec.ignoreDifferences[0].name' <<< "$candidate_app") == argocd-rbac-cm &&
$(yq -r '.spec.ignoreDifferences[0].jsonPointers[0]' <<< "$candidate_app") == /data/policy.atlas-recovery-freeze.csv ]] ||
  test::fail "argocd-self candidate does not own the exact recovery guard difference"
[[ $(yq '[.spec.syncPolicy.syncOptions[] | select(. == "RespectIgnoreDifferences=true")] | length' <<< "$candidate_app") -eq 1 ]] ||
  test::fail "argocd-self candidate omits RespectIgnoreDifferences"

live_cm=$(kubectl kustomize gitops/platform/management/argocd-self/base | NAME=argocd-cm \
  yq ea -o=json -I=0 'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))')
candidate_cm=$(kubectl kustomize "$root/argocd-self-base-overlay" | NAME=argocd-cm \
  yq ea -o=json -I=0 'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))')
[[ $(yq -r '.data."admin.enabled"' <<< "$candidate_cm") == false &&
$(yq -r '.data."users.anonymous.enabled"' <<< "$candidate_cm") == false ]] ||
  test::fail "Argo built-in admin or anonymous access remains enabled"
approved_cm_delta='del(.data."admin.enabled") |
  del(.data."users.anonymous.enabled") |
  sort_keys(..)'
[[ $(yq -o=json -I=0 "$approved_cm_delta" <<< "$candidate_cm") == "$(yq -o=json -I=0 "$approved_cm_delta" <<< "$live_cm")" ]] ||
  test::fail "argocd-cm overlay changes fields outside the approved hardening keys"

live_chart="$test_workspace/live-chart.yaml"
candidate_chart="$test_workspace/candidate-chart.yaml"
comparison_chart="$test_workspace/candidate-chart-comparison.yaml"
helm template atlas-argocd "$chart" --namespace argocd --include-crds \
  --values "$current_values" > "$live_chart"
helm template atlas-argocd "$chart" --namespace argocd --include-crds \
  --values "$current_values" --values "$hardening_values" > "$candidate_chart"
helm template atlas-argocd "$chart" --namespace argocd --include-crds \
  --values "$current_values" --values "$hardening_values" > "$comparison_chart"
cmp -s "$candidate_chart" "$comparison_chart" ||
  test::fail "hydrated Argo Chart projection is not deterministic"
normalize_chart() {
  yq ea -o=json -I=0 '
    del((select(.kind == "ConfigMap" and
      .metadata.name == "argocd-cmd-params-cm")).data."server.rbac.disableApplicationFineGrainedRBACInheritance") |
    del((select(.kind == "ConfigMap" and .metadata.name == "argocd-rbac-cm")).data."policy.csv") |
    del((select(.kind == "ConfigMap" and .metadata.name == "argocd-rbac-cm")).data."policy.default") |
    del(.spec.template.metadata.annotations."checksum/cmd-params") |
    sort_keys(..)
  ' "$1"
}
normalize_chart "$live_chart" > "$test_workspace/live-chart.normalized.json"
normalize_chart "$candidate_chart" > "$test_workspace/candidate-chart.normalized.json"
cmp -s "$test_workspace/live-chart.normalized.json" "$test_workspace/candidate-chart.normalized.json" ||
  test::fail "Chart hardening changes fields outside approved ConfigMaps and derived checksums"

params_cm=$(NAME=argocd-cmd-params-cm yq ea -o=json -I=0 \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' "$candidate_chart")
rbac_cm=$(NAME=argocd-rbac-cm yq ea -o=json -I=0 \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' "$candidate_chart")
[[ $(yq -r '.data."server.insecure"' <<< "$params_cm") == true &&
$(yq -r '.data."server.rbac.disableApplicationFineGrainedRBACInheritance"' <<< "$params_cm") == true ]] ||
  test::fail "Argo fine-grained inheritance mode is not explicit"
[[ $(yq -r '.data."policy.default"' <<< "$rbac_cm") == role:atlas-authenticated-readonly &&
$(yq -r '.data."policy.matchMode"' <<< "$rbac_cm") == glob &&
$(yq '.data | has("policy.atlas-recovery-freeze.csv")' <<< "$rbac_cm") == false ]] ||
  test::fail "Argo default policy or normal guard ownership projection drifted"
[[ $(yq -r '.unknownPolicyFragment' "$authority_inventory") == AUTHORITY_DRIFTED ]] ||
  test::fail "unknown Argo policy fragments do not fail closed"
policy_fragments_are_reviewed "$rbac_cm" ||
  test::fail "hydrated Argo policy fragments differ from the reviewed authority inventory"

side_effecting_fragment=$(yq '.data."policy.unreviewed.csv" =
  "p, role:unreviewed, applications, sync, */*, allow\n"' <<< "$rbac_cm") ||
  test::fail "could not construct the side-effecting policy fragment counterexample"
if policy_fragments_are_reviewed "$side_effecting_fragment"; then
  test::fail "an unreviewed side-effecting Argo policy fragment was accepted"
fi
group_mapping_fragment=$(yq '.data."policy.unreviewed.csv" =
  "g, unreviewed-user, role:unreviewed\n"' <<< "$rbac_cm") ||
  test::fail "could not construct the inherited-role policy fragment counterexample"
if policy_fragments_are_reviewed "$group_mapping_fragment"; then
  test::fail "an unreviewed inherited-role Argo policy fragment was accepted"
fi
while IFS= read -r policy_line; do
  [[ $policy_line == p,\ role:atlas-authenticated-readonly,\ *,\ get,\ *,\ allow ]] ||
    test::fail "default Argo role contains a non-read-only grant: ${policy_line}"
done < <(yq -r '.data."policy.csv"' <<< "$rbac_cm" | sed '/^[[:space:]]*$/d')

combined="$test_workspace/combined-owner-render.yaml"
candidate_kustomize="$test_workspace/candidate-kustomize.yaml"
comparison_kustomize="$test_workspace/candidate-kustomize-comparison.yaml"
kubectl kustomize "$root/argocd-self-base-overlay" > "$candidate_kustomize"
kubectl kustomize "$root/argocd-self-base-overlay" > "$comparison_kustomize"
cmp -s "$candidate_kustomize" "$comparison_kustomize" ||
  test::fail "hydrated Argo Kustomize projection is not deterministic"
{
  cat "$candidate_chart"
  cat "$candidate_kustomize"
} > "$combined"
identities="$test_workspace/combined-owner-identities.tsv"
yq ea -r '[.apiVersion, .kind, (.metadata.namespace // "cluster"), .metadata.name] | @tsv' \
  "$combined" | grep -v '^---$' > "$identities"
[[ $(wc -l < "$identities" | tr -d ' ') -eq $(sort -u "$identities" | wc -l | tr -d ' ') ]] ||
  test::fail "Argo hardening owner projection contains a duplicate resource identity"

[[ $(yq -r '.activationStage' "$authority_inventory") == ADR_0003_PHASE_1B_CHANGE_1 &&
$(yq -r '.builtInAdminEnabled' "$authority_inventory") == false &&
$(yq -r '.anonymousEnabled' "$authority_inventory") == false &&
$(yq -r '.policyDefault' "$authority_inventory") == role:atlas-authenticated-readonly &&
$(yq -r '.policyMatchMode' "$authority_inventory") == glob &&
$(yq -o=json -I=0 '.policyScopes' "$authority_inventory") == '["groups"]' ]] ||
  test::fail "active Argo authority control settings drifted"

[[ $(yq -r '.data."admin.enabled"' <<< "$candidate_cm") == false &&
$(yq -r '.data."users.anonymous.enabled"' <<< "$candidate_cm") == false &&
$(yq -r '.data."policy.default"' <<< "$rbac_cm") == "$(yq -r '.policyDefault' "$authority_inventory")" &&
$(yq -r '.data."policy.matchMode"' <<< "$rbac_cm") == "$(yq -r '.policyMatchMode' "$authority_inventory")" &&
$(yq -r '.data.scopes' <<< "$rbac_cm") == '[groups]' ]] ||
  test::fail "hydrated Argo authorization differs from the active inventory"

policy_inventory="$test_workspace/policy-inventory.tsv"
if ! awk -F, '
  function trim(value) {
    sub(/^[[:space:]]+/, "", value)
    sub(/[[:space:]]+$/, "", value)
    return value
  }
  /^[[:space:]]*$/ { next }
  {
    if (NF != 6) exit 1
    for (field = 1; field <= 6; field++) $field = trim($field)
    print $1 "\t" $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6
  }
' <<< "$(yq -r '.data."policy.csv"' <<< "$rbac_cm")" > "$policy_inventory"; then
  test::fail "hydrated Argo policy.csv is malformed"
fi
[[ $(wc -l < "$policy_inventory" | tr -d ' ') -eq 9 &&
$(sort -u "$policy_inventory" | wc -l | tr -d ' ') -eq 9 ]] ||
  test::fail "hydrated Argo policy.csv is incomplete or duplicated"

expected_policy_inventory="$test_workspace/expected-policy-inventory.tsv"
# shellcheck disable=SC2016 # yq expression, not a shell expansion
yq -r '.globalRoles[] as $role | $role.permissions[] |
  ["p", $role.name, .resource, .action, .object, .effect] | @tsv' \
  "$authority_inventory" | sort > "$expected_policy_inventory"
sort "$policy_inventory" > "$test_workspace/policy-inventory.sorted.tsv"
cmp -s "$test_workspace/policy-inventory.sorted.tsv" "$expected_policy_inventory" ||
  test::fail "hydrated policy.csv differs from the reviewed authority inventory"

while IFS=$'\t' read -r policy_type role resource action object effect; do
  [[ $policy_type == p && $role == role:atlas-authenticated-readonly &&
    $resource != '*' && $action != '*' && $effect == allow && -n $object ]] ||
    test::fail "default authority contains wildcard or non-allow policy: ${role} ${resource}/${action}"
  [[ $(classify_action "$resource" "$action") == READ_ONLY ]] ||
    test::fail "default authority contains side-effecting or unknown action: ${resource}/${action}"
done < "$policy_inventory"

# shellcheck disable=SC2016 # yq expression, not a shell expansion
while IFS=$'\t' read -r role resource action classification; do
  [[ $(classify_action "$resource" "$action") == "$classification" ]] ||
    test::fail "authority permission classification drifted: ${role} ${resource}/${action}"
done < <(yq -r '.globalRoles[] as $role | $role.permissions[] |
  [$role.name, .resource, .action, .classification] | @tsv' "$authority_inventory")

[[ $(yq '.localAccounts | length' "$authority_inventory") -eq 0 &&
$(yq '.ssoSubjects | length' "$authority_inventory") -eq 0 &&
$(yq '.inheritedRoleMappings | length' "$authority_inventory") -eq 0 &&
$(yq '.directPolicySubjects | length' "$authority_inventory") -eq 0 &&
$(yq '.retainedSideEffectingRoles | length' "$authority_inventory") -eq 0 &&
$(yq '.recoveryDenyCoverage.requiredRoles | length' "$authority_inventory") -eq 0 &&
$(yq -r '.recoveryDenyCoverage.unclassifiedOrUncovered' "$authority_inventory") == AUTHORITY_DRIFTED ]] ||
  test::fail "ordinary explicit authority or Recovery deny coverage drifted"
[[ $(yq -r '.data | keys | .[] | select(test("^accounts\\."))' <<< "$candidate_cm" | wc -l | tr -d ' ') -eq 0 &&
$(awk -F'\t' '$1 == "g" { count += 1 } END { print count + 0 }' "$policy_inventory") -eq 0 ]] ||
  test::fail "unreviewed local account or inherited SSO authority is active"
! grep -Fq 'atlas-phase1a-fixture' "$authority_inventory" "$policy_inventory" ||
  test::fail "non-production fixture role is represented as live Argo authority"

projects_render="$test_workspace/projects.yaml"
{
  cat bootstrap/argocd/atlas-bootstrap-project.yaml
  printf '%s\n' '---'
  kubectl kustomize gitops/platform/management/projects
} > "$projects_render"
actual_projects="$test_workspace/actual-projects.tsv"
expected_projects="$test_workspace/expected-projects.tsv"
yq -r 'select(.kind == "AppProject") |
  .metadata.name + "\t" + ((.spec.roles // []) | @json)' "$projects_render" |
  grep -v '^---$' | sort > "$actual_projects"
yq -r '.appProjects[] | [.name, (.roles | @json)] | @tsv' "$authority_inventory" |
  sort > "$expected_projects"
cmp -s "$actual_projects" "$expected_projects" ||
  test::fail "AppProject role inventory differs from all canonical AppProjects"

applicationset_deployment=$(NAME=atlas-argocd-applicationset-controller yq ea -o=json -I=0 \
  'select(.kind == "Deployment" and .metadata.name == strenv(NAME))' "$candidate_chart")
[[ -n $applicationset_deployment && $(yq '.spec.replicas' <<< "$applicationset_deployment") -eq 0 &&
$(yq '.applicationSet.replicas' "$current_values") -eq 0 ]] ||
  test::fail "ApplicationSet Controller is not pinned to zero replicas"

test::pass "Phase 1B Change 1 Argo authorization hardening and active authority inventory"
