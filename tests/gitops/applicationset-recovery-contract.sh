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
  local parent_namespace generator_count child_count element_count max_rank=0 layer rank revision child_name
  local child child_spec template_spec labels element_names element_keys

  parent=$(yq ea -o=json -I=0 'select(.kind == "ApplicationSet")' "$file")
  [[ -n $parent && $(wc -l <<< "$parent" | tr -d ' ') -eq 1 ]] || return 1
  parent_name=$(yq -r '.metadata.name' <<< "$parent")
  parent_namespace=$(yq -r '.metadata.namespace' <<< "$parent")
  parent_layer=$(yq -r '.metadata.annotations."atlas.io/resume-layer" // ""' <<< "$parent")
  parent_rank_value=$(layer_rank "$parent_layer") || return 1
  [[ $(yq '.spec.goTemplate == true' <<< "$parent") == true ]] || return 1
  [[ $(yq -o=json -I=0 '.spec | keys | sort' <<< "$parent") == '["generators","goTemplate","syncPolicy","template"]' ]] || return 1
  [[ $(yq '.spec | has("templatePatch")' <<< "$parent") == false ]] || return 1
  [[ $(yq -r '.spec.syncPolicy.applicationsSync // ""' <<< "$parent") == create-update ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.syncPolicy | keys | sort' <<< "$parent") == '["applicationsSync"]' ]] || return 1
  [[ $(yq '.spec.template.spec.syncPolicy.automated.enabled == false' <<< "$parent") == true ]] || return 1
  [[ $(yq '.spec.template.spec | has("source") and (has("sources") | not)' <<< "$parent") == true ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template | keys | sort' <<< "$parent") == '["metadata","spec"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template.metadata | keys | sort' <<< "$parent") == '["labels","name"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template.spec | keys | sort' <<< "$parent") == '["destination","project","source","syncPolicy"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template.spec.source | keys | sort' <<< "$parent") == '["path","repoURL","targetRevision"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template.spec.destination | keys | sort' <<< "$parent") == '["namespace","server"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template.spec.syncPolicy | keys | sort' <<< "$parent") == '["automated"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.template.spec.syncPolicy.automated | keys | sort' <<< "$parent") == '["enabled"]' ]] || return 1
  revision=$(yq -r '.spec.template.spec.source.targetRevision // ""' <<< "$parent")
  [[ $revision =~ ^[0-9a-f]{40}$ ]] || return 1
  [[ $(yq '.spec | has("ignoreApplicationDifferences")' <<< "$parent") == false ]] || return 1
  [[ $(yq -r '.spec.template.metadata.name // ""' <<< "$parent") == '{{.name}}' ]] || return 1
  [[ $(yq -r '.spec.template.metadata.labels."atlas.io/applicationset-owner" // ""' <<< "$parent") == "$parent_name" ]] || return 1
  [[ $(yq -r '.spec.template.metadata.labels."atlas.io/resume-layer" // ""' <<< "$parent") == '{{.resumeLayer}}' ]] || return 1
  [[ $(yq '.spec.template.metadata.labels | length' <<< "$parent") -eq 2 ]] || return 1
  [[ $(yq -r '.spec.template.spec.project // ""' <<< "$parent") =~ ^(platform-project|workload-project)$ ]] || return 1
  [[ $(yq -r '.spec.template.spec.source.repoURL // ""' <<< "$parent") == https://github.com/snkio027/atlas.git ]] || return 1
  [[ $(yq -r '.spec.template.spec.source.path // ""' <<< "$parent") =~ ^[a-zA-Z0-9._/-]+$ ]] || return 1
  [[ $(yq -r '.spec.template.spec.source.path' <<< "$parent") != /* &&
  $(yq -r '.spec.template.spec.source.path' <<< "$parent") != *../* ]] || return 1
  [[ $(yq -r '.spec.template.spec.destination.server // ""' <<< "$parent") == https://kubernetes.default.svc ]] || return 1
  [[ $(yq -r '.spec.template.spec.destination.namespace // ""' <<< "$parent") =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || return 1
  template_spec=$(yq -o=json -I=0 '.spec.template.spec | sort_keys(..)' <<< "$parent")
  ! grep -Fq '{{' <<< "$template_spec" || return 1

  generator_count=$(yq '.spec.generators | length' <<< "$parent")
  ((generator_count == 1)) || return 1
  [[ $(yq -o=json -I=0 '.spec.generators[0] | keys | sort' <<< "$parent") == '["list"]' ]] || return 1
  [[ $(yq -o=json -I=0 '.spec.generators[0].list | keys | sort' <<< "$parent") == '["elements"]' ]] || return 1
  element_count=$(yq '.spec.generators[0].list.elements | length' <<< "$parent")
  ((element_count > 0)) || return 1
  while IFS= read -r element_keys; do
    [[ $element_keys == '["name","resumeLayer"]' ]] || return 1
  done < <(yq -r '.spec.generators[0].list.elements[] | (keys | sort | @json)' <<< "$parent")
  element_names=$(yq -r '.spec.generators[0].list.elements[].name' <<< "$parent")
  [[ $(wc -l <<< "$element_names" | tr -d ' ') -eq $(sort -u <<< "$element_names" | wc -l | tr -d ' ') ]] || return 1
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
    [[ $child_name =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || return 1
    layer_rank "$layer" > /dev/null || return 1
    child=$(PARENT=$parent_name CHILD=$child_name yq ea -o=json -I=0 \
      'select(.kind == "Application" and
        .metadata.labels."atlas.io/applicationset-owner" == strenv(PARENT) and
        .metadata.name == strenv(CHILD))' "$file")
    [[ -n $child && $(wc -l <<< "$child" | tr -d ' ') -eq 1 ]] || return 1
    [[ $(yq -r '.metadata.namespace' <<< "$child") == "$parent_namespace" ]] || return 1
    labels=$(yq -o=json -I=0 '.metadata.labels | sort_keys(..)' <<< "$child")
    [[ $labels == "{\"atlas.io/applicationset-owner\":\"${parent_name}\",\"atlas.io/resume-layer\":\"${layer}\"}" ]] || return 1
    child_spec=$(yq -o=json -I=0 '.spec | sort_keys(..)' <<< "$child")
    [[ $child_spec == "$template_spec" ]] || return 1
  done < <(yq -r '.spec.generators[0].list.elements[] | [.name, .resumeLayer] | @tsv' <<< "$parent")
}

[[ $(yq -r '.activationState' "$contract") == UNINSTALLED &&
$(yq -r '.controllerBaselineReplicas' "$contract") -eq 0 &&
$(yq -r '.recoveryControllerPolicy' "$contract") == create-update &&
$(yq -r '.requiredGeneratedIdentity' "$contract") == TEMPLATE_EXACT_NAME_OWNER_SOURCE_DESTINATION &&
$(yq -r '.requiredRepositoryURL' "$contract") == https://github.com/snkio027/atlas.git &&
$(yq -r '.requiredDestinationServer' "$contract") == https://kubernetes.default.svc &&
$(yq -r '.sourceMode' "$contract") == SINGLE_SOURCE_ONLY &&
$(yq -r '.templatePatch' "$contract") == FORBIDDEN &&
$(yq -r '.generatorTemplateOverride' "$contract") == FORBIDDEN &&
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
  invalid-ignore-only.yaml \
  invalid-template-repo.yaml \
  invalid-template-name.yaml \
  invalid-template-destination.yaml \
  invalid-multiple-sources.yaml \
  invalid-template-patch.yaml; do
  if validate_fixture "$fixture_root/$invalid"; then
    test::fail "unsafe ApplicationSet fixture was accepted: ${invalid}"
  fi
done

[[ $(yq '.applicationSet.replicas' gitops/platform/management/argocd-self/values.yaml) -eq 0 ]] ||
  test::fail "live ApplicationSet Controller baseline is no longer zero"
test::assert_not_found '^kind:[[:space:]]+ApplicationSet$' gitops

test::pass "ApplicationSet recovery-safe projection and outermost layer contract"
