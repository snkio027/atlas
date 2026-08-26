#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly fixture_root=tests/gitops/fixtures/applicationset-recovery
readonly contract=gitops/platform/management/protection-foundation/definitions/applicationset-recovery-contract.json

layer_rank() {
  case "$1" in
    foundation) printf '10\n' ;;
    management) printf '20\n' ;;
    operators) printf '30\n' ;;
    infrastructure-controllers) printf '40\n' ;;
    platform-services) printf '50\n' ;;
    workload-control) printf '60\n' ;;
    tenant-workloads) printf '70\n' ;;
    external-root) printf '80\n' ;;
    *) return 1 ;;
  esac
}

validate_fixture() {
  local file=$1 parent parent_name parent_layer parent_rank_value
  local generator_count child_count element_count max_rank=0 layer rank revision child_name
  local matching_child_count

  parent=$(yq ea -o=json -I=0 'select(.kind == "ApplicationSet")' "$file")
  [[ -n $parent && $(wc -l <<< "$parent" | tr -d ' ') -eq 1 ]] || return 1
  parent_name=$(yq -r '.metadata.name' <<< "$parent")
  parent_layer=$(yq -r '.metadata.annotations."atlas.io/resume-layer" // ""' <<< "$parent")
  parent_rank_value=$(layer_rank "$parent_layer") || return 1
  [[ $(yq -r '.spec.syncPolicy.applicationsSync // ""' <<< "$parent") == create-update ]] || return 1
  [[ $(yq '.spec.template.spec.syncPolicy.automated.enabled == false' <<< "$parent") == true ]] || return 1
  revision=$(yq -r '.spec.template.spec.source.targetRevision // ""' <<< "$parent")
  [[ $revision =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ $(yq '.spec | has("ignoreApplicationDifferences")' <<< "$parent") == false ]] || return 1

  generator_count=$(yq '.spec.generators | length' <<< "$parent")
  ((generator_count == 1)) || return 1
  [[ $(yq '.spec.generators[0] | keys | .[]' <<< "$parent") == list ]] || return 1
  element_count=$(yq '.spec.generators[0].list.elements | length' <<< "$parent")
  ((element_count > 0)) || return 1
  while IFS= read -r layer; do
    rank=$(layer_rank "$layer") || return 1
    ((rank > max_rank)) && max_rank=$rank
  done < <(yq -r '.spec.generators[0].list.elements[].resumeLayer' <<< "$parent")
  ((parent_rank_value == max_rank)) || return 1

  child_count=$(PARENT=$parent_name yq ea \
    '[select(.kind == "Application" and .metadata.labels."atlas.io/applicationset-owner" == strenv(PARENT))] | length' \
    "$file")
  ((child_count == element_count)) || return 1
  while IFS=$'\t' read -r child_name layer; do
    layer_rank "$layer" > /dev/null || return 1
    matching_child_count=$(PARENT=$parent_name CHILD=$child_name LAYER=$layer REVISION=$revision yq ea \
      '[select(.kind == "Application" and
        .metadata.labels."atlas.io/applicationset-owner" == strenv(PARENT) and
        .metadata.name == strenv(CHILD) and
        .metadata.labels."atlas.io/resume-layer" == strenv(LAYER) and
        .spec.source.targetRevision == strenv(REVISION) and
        .spec.syncPolicy.automated.enabled == false)] | length' "$file")
    ((matching_child_count == 1)) || return 1
  done < <(yq -r '.spec.generators[0].list.elements[] | [.name, .resumeLayer] | @tsv' <<< "$parent")
  enabled_false_count=$(PARENT=$parent_name yq ea \
    '[select(.kind == "Application" and
      .metadata.labels."atlas.io/applicationset-owner" == strenv(PARENT) and
      .spec.syncPolicy.automated.enabled == false)] | length' "$file")
  ((enabled_false_count == child_count)) || return 1
}

[[ $(yq -r '.activationState' "$contract") == UNINSTALLED &&
$(yq -r '.controllerBaselineReplicas' "$contract") -eq 0 &&
$(yq -r '.recoveryControllerPolicy' "$contract") == create-update &&
$(yq -r '.unresolvedLayerOrGenerator' "$contract") == FREEZE_UNAVAILABLE &&
$(yq -r '.ignoreApplicationDifferencesIsSufficient' "$contract") == false ]] ||
  test::fail "ApplicationSet recovery contract drifted"

for valid in valid-single-layer.yaml valid-cross-layer.yaml; do
  validate_fixture "$fixture_root/$valid" || test::fail "valid ApplicationSet fixture was rejected: ${valid}"
done

for invalid in \
  invalid-mutable-revision.yaml \
  invalid-parent-autosync.yaml \
  invalid-generator.yaml \
  invalid-cross-layer-gate.yaml \
  invalid-ignore-only.yaml; do
  if validate_fixture "$fixture_root/$invalid"; then
    test::fail "unsafe ApplicationSet fixture was accepted: ${invalid}"
  fi
done

[[ $(yq '.applicationSet.replicas' gitops/platform/management/argocd-self/values.yaml) -eq 0 ]] ||
  test::fail "live ApplicationSet Controller baseline is no longer zero"
test::assert_not_found '^kind:[[:space:]]+ApplicationSet$' gitops

test::pass "ApplicationSet recovery-safe projection and outermost layer contract"
