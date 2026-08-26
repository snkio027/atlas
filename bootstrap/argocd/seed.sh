# shellcheck shell=bash

[[ -n ${_ATLAS_ARGOCD_SEED_LOADED:-} ]] && return 0
readonly _ATLAS_ARGOCD_SEED_LOADED=1

argocd::_wait_seed() {
  local timeout deployment
  timeout=$(config::get ATLAS_READY_TIMEOUT) || return 1
  runtime::kubectl wait customresourcedefinition/applications.argoproj.io \
    --for condition=Established --timeout "$timeout" > /dev/null || return 1
  for deployment in atlas-argocd-server atlas-argocd-repo-server atlas-argocd-redis; do
    runtime::kubectl wait "deployment/${deployment}" --namespace argocd \
      --for condition=Available --timeout "$timeout" > /dev/null || return 1
  done
  runtime::kubectl rollout status statefulset/atlas-argocd-application-controller \
    --namespace argocd --timeout "$timeout" > /dev/null || return 1
}

argocd::install_seed() {
  local manifest="${ATLAS_STATE_DIR}/rendered/argocd-seed.yaml"
  [[ -s $manifest && ! -L $manifest ]] || {
    runtime::die "rendered Argo CD Seed is missing or unsafe: ${manifest}"
    return 1
  }

  if ! runtime::kubectl get namespace argocd > /dev/null 2>&1; then
    lock::assert_held || return 1
    runtime::kubectl create namespace argocd > /dev/null || return 1
  fi
  lock::assert_held || return 1
  runtime::kubectl apply --server-side --field-manager atlas-bootstrap \
    --filename "${ATLAS_ROOT_DIR}/gitops/platform/management/argocd-self/base/argocd-cm.yaml" > /dev/null || return 1
  lock::assert_held || return 1
  runtime::kubectl apply --server-side --field-manager atlas-bootstrap \
    --filename "$manifest" > /dev/null || return 1
  argocd::_wait_seed || return 1
}
