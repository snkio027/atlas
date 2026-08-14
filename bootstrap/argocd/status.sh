# shellcheck shell=bash

[[ -n ${_ATLAS_ARGOCD_STATUS_LOADED:-} ]] && return 0
readonly _ATLAS_ARGOCD_STATUS_LOADED=1

argocd::_workload_ready() {
  local kind=$1 name=$2 replicas
  replicas=$(runtime::kubectl get "${kind}/${name}" --namespace argocd \
    --ignore-not-found \
    --output 'jsonpath={.spec.replicas}{"/"}{.status.readyReplicas}' 2> /dev/null) || return 1
  [[ $replicas =~ ^([1-9][0-9]*)/([1-9][0-9]*)$ ]] || return 1
  [[ ${BASH_REMATCH[1]} == "${BASH_REMATCH[2]}" ]]
}

argocd::_control_plane_ready() {
  local deployment
  for deployment in atlas-argocd-server atlas-argocd-repo-server atlas-argocd-redis; do
    argocd::_workload_ready deployment "$deployment" || return 1
  done
  argocd::_workload_ready statefulset atlas-argocd-application-controller
}

argocd::_application_status() {
  local name=$1 record state sync health
  record=$(runtime::kubectl get application "$name" --namespace argocd \
    --ignore-not-found \
    --output 'jsonpath={.metadata.name}{"\t"}{.status.sync.status}{"\t"}{.status.health.status}' 2> /dev/null) || return 1
  if [[ -z $record ]]; then
    printf 'ABSENT\n'
    return 0
  fi
  [[ $record == *$'\t'*$'\t'* ]] || return 1
  state=${record#*$'\t'}
  sync=${state%%$'\t'*}
  health=${state#*$'\t'}
  printf '%s/%s\n' "${sync:-Unknown}" "${health:-Unknown}"
}

argocd::inspect_status() {
  local namespace root_state adoption_state
  namespace=$(runtime::kubectl get namespace argocd --ignore-not-found --output name 2> /dev/null) || {
    printf 'argocd\tUNAVAILABLE\targocd\n'
    printf 'root\tUNAVAILABLE\t%s\n' "$(config::get ATLAS_ROOT_NAME)"
    printf 'adoption\tUNAVAILABLE\targocd-self\n'
    return 2
  }
  if [[ -z $namespace ]]; then
    printf 'argocd\tABSENT\targocd\n'
    printf 'root\tABSENT\t%s\n' "$(config::get ATLAS_ROOT_NAME)"
    printf 'adoption\tABSENT\targocd-self\n'
    return 0
  fi
  if argocd::_control_plane_ready; then
    printf 'argocd\tREADY\targocd\n'
  else
    printf 'argocd\tDEGRADED\targocd\n'
  fi
  root_state=$(argocd::_application_status "$(config::get ATLAS_ROOT_NAME)") || root_state=UNAVAILABLE
  printf 'root\t%s\t%s\n' "$root_state" "$(config::get ATLAS_ROOT_NAME)"
  adoption_state=$(argocd::_application_status argocd-self) || adoption_state=UNAVAILABLE
  printf 'adoption\t%s\targocd-self\n' "$adoption_state"
}
