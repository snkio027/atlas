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

gitops_reachability::_kustomization_edges() {
  local kustomization=$1 entry raw_edges

  raw_edges=$(yq -r '(
    .resources[]?,
    .bases[]?,
    .components[]?,
    .crds[]?,
    .generators[]?,
    .transformers[]?,
    .validators[]?,
    .configurations[]?,
    .patches[]?.path?,
    .patchesStrategicMerge[]?,
    .configMapGenerator[]?.envs[]?,
    .configMapGenerator[]?.files[]?,
    .secretGenerator[]?.envs[]?,
    .secretGenerator[]?.files[]?,
    .helmCharts[]?.valuesFile?,
    .helmCharts[]?.additionalValuesFiles[]?,
    .openapi.path?
  ) | select(. != null)
  ' "$kustomization") || return 1
  while IFS= read -r entry; do
    [[ -n $entry ]] || continue
    [[ $entry != *=* ]] || entry=${entry#*=}
    printf '%s\n' "$entry"
  done <<< "$raw_edges"
}

gitops_reachability::_application_edges() {
  local application=$1 source repo path value_file reference reference_count sources value_files

  sources=$(yq -o=json -I=0 '(.spec.source?, .spec.sources[]?) | select(. != null)' <<< "$application") || return 1
  while IFS= read -r source; do
    [[ -n $source ]] || continue
    repo=$(yq -r '.repoURL // ""' <<< "$source")
    [[ $repo == https://github.com/snkio027/atlas.git ]] || {
      printf 'Application source repository is outside the local proof: %s\n' "$repo" >&2
      return 1
    }
    path=$(yq -r '.path // ""' <<< "$source")
    if [[ -n $path ]]; then
      printf '%s\n' "$ATLAS_TEST_ROOT/$path"
    fi

    value_files=$(yq -r '.helm.valueFiles[]? | select(. != null)' <<< "$source") || return 1
    while IFS= read -r value_file; do
      [[ -n $value_file ]] || continue
      if [[ $value_file == \$*/* ]]; then
        reference=${value_file%%/*}
        reference=${reference#\$}
        reference_count=$(REF=$reference yq '
          [.spec.sources[]? |
            select(.ref == strenv(REF) and
              .repoURL == "https://github.com/snkio027/atlas.git")] | length
        ' <<< "$application")
        ((reference_count == 1)) || {
          printf 'Application Helm value reference is unresolved: %s\n' "$value_file" >&2
          return 1
        }
        printf '%s/%s\n' "$ATLAS_TEST_ROOT" "${value_file#*/}"
      else
        [[ -n $path ]] || {
          printf 'relative Helm value file has no source path: %s\n' "$value_file" >&2
          return 1
        }
        printf '%s/%s/%s\n' "$ATLAS_TEST_ROOT" "$path" "$value_file"
      fi
    done <<< "$value_files"
  done <<< "$sources"
}

gitops_reachability::collect_paths() {
  local start=$1 start_path current kustomization entry target render application edge
  local kustomization_edges applications application_edges
  local -a queue=()
  local -A seen=()

  start_path=$(gitops_reachability::_canonical "$ATLAS_TEST_ROOT/$start") || return 1
  gitops_reachability::_within_repository "$start_path" || return 1
  queue+=("$start_path")

  while ((${#queue[@]} > 0)); do
    current=${queue[0]}
    queue=("${queue[@]:1}")
    [[ -z ${seen[$current]:-} ]] || continue
    seen[$current]=true
    gitops_reachability::_within_repository "$current" || {
      printf 'control graph escapes the repository: %s\n' "$current" >&2
      return 1
    }
    printf '%s\n' "$current"
    [[ -d $current ]] || continue
    if ! kustomization=$(gitops_reachability::_kustomization_file "$current"); then
      continue
    fi
    printf '%s\n' "$kustomization"

    kustomization_edges=$(gitops_reachability::_kustomization_edges "$kustomization") || return 1
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
      queue+=("$target")
    done <<< "$kustomization_edges"

    if ! render=$(kubectl kustomize "$current"); then
      printf 'unable to render reachable Kustomization: %s\n' "$current" >&2
      return 1
    fi
    applications=$(yq ea -o=json -I=0 'select(.kind == "Application")' <<< "$render") || return 1
    while IFS= read -r application; do
      [[ -n $application ]] || continue
      application_edges=$(gitops_reachability::_application_edges "$application") || return 1
      while IFS= read -r edge; do
        [[ -n $edge ]] || continue
        [[ $edge != *://* && $edge != git::* ]] || {
          printf 'remote Application edge is outside the local proof: %s\n' "$edge" >&2
          return 1
        }
        target=$(gitops_reachability::_canonical "$edge") || {
          printf 'unresolvable Application edge: %s\n' "$edge" >&2
          return 1
        }
        gitops_reachability::_within_repository "$target" || {
          printf 'Application edge escapes the repository: %s\n' "$edge" >&2
          return 1
        }
        queue+=("$target")
      done <<< "$application_edges"
    done <<< "$applications"
  done
}

gitops_reachability::assert_definition_unreachable() {
  local start=$1 definition_relative=$2 definition graph current

  definition=$(gitops_reachability::_canonical "$ATLAS_TEST_ROOT/$definition_relative") || return 1
  gitops_reachability::_within_repository "$definition" || return 1
  graph=$(gitops_reachability::collect_paths "$start") || return 1

  while IFS= read -r current; do
    if gitops_reachability::_is_definition_path "$current" "$definition"; then
      printf 'definition path is reachable: %s -> %s\n' "$start" "${current#"$ATLAS_TEST_ROOT/"}" >&2
      return 1
    fi
  done <<< "$graph"
}
