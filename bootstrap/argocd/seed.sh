# shellcheck shell=bash

[[ -n ${_ATLAS_ARGOCD_SEED_LOADED:-} ]] && return 0
readonly _ATLAS_ARGOCD_SEED_LOADED=1

argocd::_wait_seed() {
  local timeout deployment
  timeout=$(config::get ATLAS_READY_TIMEOUT)
  runtime::kubectl wait customresourcedefinition/applications.argoproj.io \
    --for condition=Established --timeout "$timeout" > /dev/null
  for deployment in atlas-argocd-server atlas-argocd-repo-server atlas-argocd-redis; do
    runtime::kubectl wait "deployment/${deployment}" --namespace argocd \
      --for condition=Available --timeout "$timeout" > /dev/null
  done
  runtime::kubectl rollout status statefulset/atlas-argocd-application-controller \
    --namespace argocd --timeout "$timeout" > /dev/null
}

argocd::install_seed() {
  local manifest="${ATLAS_STATE_DIR}/rendered/argocd-seed.yaml"
  [[ -s $manifest && ! -L $manifest ]] || {
    runtime::die "rendered Argo CD Seed is missing or unsafe: ${manifest}"
    return 1
  }

  runtime::kubectl get namespace argocd > /dev/null 2>&1 || runtime::kubectl create namespace argocd > /dev/null
  runtime::kubectl apply --server-side --field-manager atlas-bootstrap \
    --filename "${ATLAS_ROOT_DIR}/gitops/platform/management/argocd-self/base/argocd-cm.yaml" > /dev/null
  runtime::kubectl apply --server-side --field-manager atlas-bootstrap \
    --filename "$manifest" > /dev/null
  argocd::_wait_seed
}
