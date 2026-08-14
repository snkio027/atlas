# shellcheck shell=bash

[[ -n ${_ATLAS_ARGOCD_RENDER_LOADED:-} ]] && return 0
readonly _ATLAS_ARGOCD_RENDER_LOADED=1

argocd::render_root() {
  local template content
  template="${ATLAS_ROOT_DIR}/bootstrap/argocd/root-app.yaml.tpl"
  content=$(< "$template")
  content=${content//@@ROOT_NAME@@/$(config::get ATLAS_ROOT_NAME)}
  content=${content//@@REPO_URL@@/$(config::get ATLAS_GIT_REPO_URL)}
  content=${content//@@REVISION@@/$(config::get ATLAS_GIT_REVISION)}
  content=${content//@@ROOT_PATH@@/$(config::get ATLAS_ROOT_PATH)}
  [[ $content != *@@* ]] || {
    runtime::die "Root template contains unresolved tokens"
    return 1
  }
  printf '%s\n' "$content"
}

argocd::render_seed() {
  local chart values
  chart="${ATLAS_ROOT_DIR}/$(config::version ARGOCD_CHART_FILE)"
  values="${ATLAS_ROOT_DIR}/gitops/platform/management/argocd-self/values.yaml"

  helm template atlas-argocd "$chart" \
    --namespace argocd \
    --include-crds \
    --values "$values"
}

argocd::_validate_rendered_seed() {
  local manifest=$1
  grep -Fq 'kind: CustomResourceDefinition' "$manifest" || return 1
  grep -Fq 'name: applications.argoproj.io' "$manifest" || return 1
  grep -Fq 'name: atlas-argocd-server' "$manifest" || return 1
  grep -Fq "$(config::version ARGOCD_IMAGE)" "$manifest" || return 1
  grep -Fq "$(config::version REDIS_IMAGE)" "$manifest" || return 1
  ! grep -Eq '^kind: (Application|AppProject)$' "$manifest" || return 1
}

argocd::render() {
  runtime::phase render
  local chart expected_sha actual_sha output_dir
  chart="${ATLAS_ROOT_DIR}/$(config::version ARGOCD_CHART_FILE)"
  expected_sha=$(config::version ARGOCD_CHART_SHA256)
  [[ -s $chart && ! -L $chart ]] || {
    runtime::die "locked Argo CD chart is missing: ${chart}"
    return 1
  }
  actual_sha=$(runtime::sha256 "$chart")
  [[ $actual_sha == "$expected_sha" ]] || {
    runtime::die "Argo CD chart checksum mismatch"
    return 1
  }

  output_dir="${ATLAS_STATE_DIR}/rendered"
  runtime::atomic_capture "${output_dir}/argocd-seed.yaml" argocd::render_seed
  runtime::atomic_capture "${output_dir}/root-app.yaml" argocd::render_root
  argocd::_validate_rendered_seed "${output_dir}/argocd-seed.yaml" || {
    runtime::die "rendered Argo CD seed violates its contract"
    return 1
  }
  runtime::ok "rendered Bootstrap manifests: ${output_dir}"
}
