# shellcheck shell=bash

[[ -n ${_ATLAS_ARGOCD_HANDOFF_LOADED:-} ]] && return 0
readonly _ATLAS_ARGOCD_HANDOFF_LOADED=1

argocd::_root_source_ready() {
  local root_path output
  root_path="${ATLAS_ROOT_DIR}/$(config::get ATLAS_ROOT_PATH)"
  output=$(kubectl kustomize "$root_path" 2> /dev/null) || return 1
  grep -Fq 'kind: Application' <<< "$output" || return 1
  grep -Fq 'name: project-bootstrap' <<< "$output" || return 1
  grep -Fq 'name: platform-control' <<< "$output" || return 1
  grep -Fq 'name: workload-control' <<< "$output" || return 1
}

argocd::_argocd_self_state() {
  local crd record state
  crd=$(runtime::kubectl get customresourcedefinition applications.argoproj.io \
    --ignore-not-found --output name 2> /dev/null) || return 1
  if [[ -z $crd ]]; then
    printf 'ABSENT\n'
    return 0
  fi

  record=$(runtime::kubectl get application argocd-self --namespace argocd \
    --ignore-not-found \
    --output 'jsonpath={.metadata.name}{"\t"}{.status.sync.status}{"/"}{.status.health.status}' 2> /dev/null) || return 1
  if [[ -z $record ]]; then
    printf 'ABSENT\n'
    return 0
  fi
  [[ $record == *$'\t'* ]] || return 1
  state=${record#*$'\t'}
  if [[ $state == Synced/Healthy ]]; then
    printf 'HEALTHY\n'
  else
    printf 'PRESENT:%s\n' "${state:-Unknown/Unknown}"
  fi
}

argocd::_argocd_self_healthy() {
  [[ $(argocd::_argocd_self_state) == HEALTHY ]]
}

argocd::_ensure_seed_authority() {
  local argocd_self_state
  argocd_self_state=$(argocd::_argocd_self_state) || {
    runtime::die "unable to inspect argocd-self health state"
    return 1
  }

  case "$argocd_self_state" in
    ABSENT)
      argocd::install_seed
      ;;
    HEALTHY)
      runtime::info "argocd-self is Healthy; Bootstrap will not modify the Git-managed control plane"
      ;;
    PRESENT:*)
      runtime::die "argocd-self already exists but is not Healthy (${argocd_self_state#PRESENT:}); normal Bootstrap will not resume Seed authority"
      return 1
      ;;
    *)
      runtime::die "unknown argocd-self health state: ${argocd_self_state}"
      return 1
      ;;
  esac
}

argocd::instantiate_root() {
  local root_manifest="${ATLAS_STATE_DIR}/rendered/root-app.yaml"
  local root_name diff_status
  root_name=$(config::get ATLAS_ROOT_NAME)

  if runtime::kubectl get application "$root_name" --namespace argocd > /dev/null 2>&1; then
    if runtime::kubectl diff --filename "$root_manifest" > /dev/null; then
      runtime::info "External Root already matches its Git-defined contract"
      return 0
    else
      diff_status=$?
    fi
    if ((diff_status == 1)); then
      runtime::die "External Root drift detected; normal Bootstrap will not overwrite Tier-0"
      return 1
    fi
    runtime::die "External Root comparison failed"
    return 1
  fi

  runtime::kubectl apply --filename "$root_manifest" > /dev/null
  runtime::kubectl annotate application "$root_name" --namespace argocd \
    argocd.argoproj.io/refresh=hard --overwrite > /dev/null
  runtime::ok "External Root instantiated: ${root_name}"
}

argocd::_root_ready() {
  local state
  state=$(runtime::kubectl get application "$(config::get ATLAS_ROOT_NAME)" --namespace argocd \
    --output 'jsonpath={.status.sync.status}{"/"}{.status.health.status}' 2> /dev/null) || return 1
  [[ $state == Synced/Healthy ]]
}

argocd::_handoff_health_ready() {
  argocd::_root_ready && argocd::_argocd_self_healthy
}

argocd::wait_for_handoff_health() {
  local timeout_seconds
  timeout_seconds=$(config::get ATLAS_READY_TIMEOUT)
  timeout_seconds=${timeout_seconds%s}
  runtime::wait_for "$timeout_seconds" 5 "External Root and argocd-self health" argocd::_handoff_health_ready
}

argocd::handoff() {
  local tier0_approved=$1
  runtime::phase argocd-handoff
  # Keep the human gate at the mutating trust boundary as well as in the CLI.
  [[ $tier0_approved == true ]] || {
    runtime::die "Tier-0 approval is required: pass --approve-tier0"
    return 1
  }
  argocd::_root_source_ready || {
    runtime::die "Root Macro DAG is incomplete; refusing to instantiate Tier-0"
    return 1
  }

  argocd::render
  argocd::_ensure_seed_authority
  runtime::kubectl apply --filename "${ATLAS_ROOT_DIR}/bootstrap/argocd/atlas-bootstrap-project.yaml" > /dev/null
  argocd::instantiate_root
  argocd::wait_for_handoff_health
  runtime::ok "GitOps control handoff is Healthy; normal Bootstrap has stopped Seed mutation"
}
