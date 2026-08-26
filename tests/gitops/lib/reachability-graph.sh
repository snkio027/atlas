#!/usr/bin/env bash

if [[ ${_ATLAS_GITOPS_REACHABILITY_GRAPH_LOADED:-} == true ]]; then
  return 0
fi
readonly _ATLAS_GITOPS_REACHABILITY_GRAPH_LOADED=true

gitops_reachability::_canonical() {
  local path=$1 parent leaf

  if [[ -d $path ]]; then
    (cd "$path" && pwd -P)
    return
  fi
  parent=$(dirname "$path")
  leaf=$(basename "$path")
  [[ -d $parent ]] || return 1
  parent=$(cd "$parent" && pwd -P) || return 1
  printf '%s/%s\n' "$parent" "$leaf"
}

gitops_reachability::_within_repository() {
  local path=$1
  [[ $path == "$ATLAS_TEST_ROOT" || $path == "$ATLAS_TEST_ROOT"/* ]]
}

gitops_reachability::_is_definition_path() {
  local path=$1 definition=$2
  [[ $path == "$definition" || $path == "$definition"/* ]]
}

gitops_reachability::_kustomization_file() {
  local directory=$1 candidate
  for candidate in kustomization.yaml kustomization.yml Kustomization; do
    if [[ -f $directory/$candidate ]]; then
      printf '%s\n' "$directory/$candidate"
      return 0
    fi
  done
  return 1
}

gitops_reachability::assert_definition_unreachable() {
  local start=$1 definition_relative=$2
  local definition start_path current kustomization entry target render source_path
  local -a queue=()
  local -A seen=()

  definition=$(gitops_reachability::_canonical "$ATLAS_TEST_ROOT/$definition_relative") || return 1
  start_path=$(gitops_reachability::_canonical "$ATLAS_TEST_ROOT/$start") || return 1
  gitops_reachability::_within_repository "$definition" || return 1
  gitops_reachability::_within_repository "$start_path" || return 1
  queue+=("$start_path")

  while ((${#queue[@]} > 0)); do
    current=${queue[0]}
    queue=("${queue[@]:1}")
    [[ -z ${seen[$current]:-} ]] || continue
    seen[$current]=true

    if gitops_reachability::_is_definition_path "$current" "$definition"; then
      printf 'Phase 1A definition path is reachable: %s -> %s\n' "$start" "${current#"$ATLAS_TEST_ROOT/"}" >&2
      return 1
    fi
    gitops_reachability::_within_repository "$current" || {
      printf 'control graph escapes the repository: %s\n' "$current" >&2
      return 1
    }
    [[ -d $current ]] || continue
    if ! kustomization=$(gitops_reachability::_kustomization_file "$current"); then
      continue
    fi

    while IFS= read -r entry; do
      [[ -n $entry ]] || continue
      [[ $entry != *://* && $entry != git::* ]] || {
        printf 'remote Kustomize edge is outside the local proof: %s\n' "$entry" >&2
        return 1
      }
      target=$(gitops_reachability::_canonical "$current/$entry") || {
        printf 'unresolvable Kustomize edge: %s -> %s\n' "$current" "$entry" >&2
        return 1
      }
      gitops_reachability::_within_repository "$target" || {
        printf 'Kustomize edge escapes the repository: %s\n' "$target" >&2
        return 1
      }
      if gitops_reachability::_is_definition_path "$target" "$definition"; then
        printf 'Phase 1A definition path is reachable through Kustomize: %s\n' "${target#"$ATLAS_TEST_ROOT/"}" >&2
        return 1
      fi
      [[ -d $target ]] && queue+=("$target")
    done < <(yq -r '.resources[]?, .bases[]?, .components[]?' "$kustomization")

    if ! render=$(kubectl kustomize "$current"); then
      printf 'unable to render reachable Kustomization: %s\n' "$current" >&2
      return 1
    fi
    while IFS= read -r source_path; do
      [[ -n $source_path ]] || continue
      [[ $source_path != *://* && $source_path != git::* ]] || {
        printf 'remote Application path is outside the local proof: %s\n' "$source_path" >&2
        return 1
      }
      target=$(gitops_reachability::_canonical "$ATLAS_TEST_ROOT/$source_path") || {
        printf 'unresolvable Application source path: %s\n' "$source_path" >&2
        return 1
      }
      gitops_reachability::_within_repository "$target" || {
        printf 'Application source path escapes the repository: %s\n' "$source_path" >&2
        return 1
      }
      if gitops_reachability::_is_definition_path "$target" "$definition"; then
        printf 'Phase 1A definition path is reachable through Application: %s\n' "$source_path" >&2
        return 1
      fi
      queue+=("$target")
    done < <(yq ea -r '
      select(.kind == "Application") |
      (.spec.source.path // ""), (.spec.sources[]?.path // "")
    ' <<< "$render")
  done
}
