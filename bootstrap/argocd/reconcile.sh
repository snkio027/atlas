# shellcheck shell=bash

[[ -n ${_ATLAS_ARGOCD_LOADED:-} ]] && return 0
readonly _ATLAS_ARGOCD_LOADED=1

argocd::_render_root_stdout() {
  local template content
  template="${ATLAS_ROOT_DIR}/bootstrap/argocd/root-app.yaml.tpl"
  content=$(< "$template")
  content=${content//@@ROOT_NAME@@/$(config::get ATLAS_ROOT_NAME)}
  content=${content//@@REPO_URL@@/$(config::get ATLAS_GIT_REPO_URL)}
  content=${content//@@REVISION@@/$(config::get ATLAS_GIT_REVISION)}
  content=${content//@@ROOT_PATH@@/$(config::get ATLAS_ROOT_PATH)}
  [[ $content != *@@* ]] || {
    core::die "Root template contains unresolved tokens"
    return 1
  }
  printf '%s\n' "$content"
}

argocd::_render_seed_stdout() {
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
  core::phase render
  local chart expected actual output_dir
  chart="${ATLAS_ROOT_DIR}/$(config::version ARGOCD_CHART_FILE)"
  expected=$(config::version ARGOCD_CHART_SHA256)
  [[ -s $chart && ! -L $chart ]] || {
    core::die "locked Argo CD chart is missing: ${chart}"
    return 1
  }
  actual=$(core::sha256 "$chart")
  [[ $actual == "$expected" ]] || {
    core::die "Argo CD chart checksum mismatch"
    return 1
  }

  output_dir="${ATLAS_STATE_DIR}/rendered"
  core::atomic_capture "${output_dir}/argocd-seed.yaml" argocd::_render_seed_stdout
  core::atomic_capture "${output_dir}/root-app.yaml" argocd::_render_root_stdout
  argocd::_validate_rendered_seed "${output_dir}/argocd-seed.yaml" || {
    core::die "rendered Argo CD seed violates its contract"
    return 1
  }
  core::ok "rendered Bootstrap manifests: ${output_dir}"
}

argocd::_root_source_ready() {
  local root_path output
  root_path="${ATLAS_ROOT_DIR}/$(config::get ATLAS_ROOT_PATH)"
  output=$(kubectl kustomize "$root_path" 2> /dev/null) || return 1
  grep -Fq 'kind: Application' <<< "$output" || return 1
  grep -Fq 'name: project-bootstrap' <<< "$output" || return 1
  grep -Fq 'name: platform-control' <<< "$output" || return 1
  grep -Fq 'name: workload-control' <<< "$output" || return 1
}

argocd::_self_managed() {
  local state
  state=$(core::kubectl get application argocd-self --namespace argocd \
    --output 'jsonpath={.status.sync.status}{"/"}{.status.health.status}' 2> /dev/null) || return 1
  [[ $state == Synced/Healthy ]]
}

argocd::_wait_seed() {
  local timeout deployment
  timeout=$(config::get ATLAS_READY_TIMEOUT)
  core::kubectl wait customresourcedefinition/applications.argoproj.io \
    --for condition=Established --timeout "$timeout" > /dev/null
  for deployment in atlas-argocd-server atlas-argocd-repo-server atlas-argocd-redis; do
    core::kubectl wait "deployment/${deployment}" --namespace argocd \
      --for condition=Available --timeout "$timeout" > /dev/null
  done
  if core::kubectl get statefulset atlas-argocd-application-controller --namespace argocd > /dev/null 2>&1; then
    core::kubectl rollout status statefulset/atlas-argocd-application-controller \
      --namespace argocd --timeout "$timeout" > /dev/null
  fi
}

argocd::_apply_seed() {
  local manifest="${ATLAS_STATE_DIR}/rendered/argocd-seed.yaml"
  core::kubectl get namespace argocd > /dev/null 2>&1 || core::kubectl create namespace argocd > /dev/null
  core::kubectl apply --server-side --field-manager atlas-bootstrap \
    --filename "${ATLAS_ROOT_DIR}/gitops/platform/management/argocd-self/base/argocd-cm.yaml" > /dev/null
  core::kubectl apply --server-side --field-manager atlas-bootstrap \
    --filename "$manifest" > /dev/null
  argocd::_wait_seed
}

argocd::_apply_root_once() {
  local root_manifest="${ATLAS_STATE_DIR}/rendered/root-app.yaml"
  local root_name diff_status
  root_name=$(config::get ATLAS_ROOT_NAME)

  if core::kubectl get application "$root_name" --namespace argocd > /dev/null 2>&1; then
    if core::kubectl diff --filename "$root_manifest" > /dev/null; then
      core::info "External Root already matches its Git-defined contract"
      return 0
    else
      diff_status=$?
    fi
    if ((diff_status == 1)); then
      core::die "External Root drift detected; normal Bootstrap will not overwrite Tier-0"
      return 1
    fi
    core::die "External Root comparison failed"
  fi

  core::kubectl apply --filename "$root_manifest" > /dev/null
  core::kubectl annotate application "$root_name" --namespace argocd \
    argocd.argoproj.io/refresh=hard --overwrite > /dev/null
  core::ok "External Root instantiated: ${root_name}"
}

argocd::_root_ready() {
  local state
  state=$(core::kubectl get application "$(config::get ATLAS_ROOT_NAME)" --namespace argocd \
    --output 'jsonpath={.status.sync.status}{"/"}{.status.health.status}' 2> /dev/null) || return 1
  [[ $state == Synced/Healthy ]]
}

argocd::_handoff_ready() {
  argocd::_root_ready && argocd::_self_managed
}

argocd::reconcile() {
  local tier0_approved=$1 timeout_seconds
  core::phase argocd
  [[ $tier0_approved == true ]] || {
    core::die "Tier-0 approval is required: pass --approve-tier0"
    return 1
  }
  argocd::_root_source_ready || {
    core::die "Root Macro DAG is incomplete; refusing to instantiate Tier-0"
    return 1
  }
  argocd::render

  if argocd::_self_managed; then
    core::info "argocd-self is Healthy; Bootstrap will not reconcile the adopted control plane"
  else
    argocd::_apply_seed
  fi

  core::kubectl apply --filename "${ATLAS_ROOT_DIR}/bootstrap/argocd/atlas-bootstrap-project.yaml" > /dev/null
  argocd::_apply_root_once

  timeout_seconds=${ATLAS_CONFIG[ATLAS_READY_TIMEOUT]%s}
  core::wait_for "$timeout_seconds" 5 "External Root health and argocd-self adoption" argocd::_handoff_ready
  core::ok "GitOps control handoff is Healthy; Bootstrap authority has terminated"
}

argocd::status() {
  local namespace_state root_state adoption_state
  if ! core::kubectl get namespace argocd > /dev/null 2>&1; then
    printf 'argocd\tABSENT\targocd\n'
    printf 'root\tABSENT\t%s\n' "$(config::get ATLAS_ROOT_NAME)"
    printf 'adoption\tABSENT\targocd-self\n'
    return 0
  fi
  namespace_state=$(core::kubectl get deployment --namespace argocd \
    --output 'jsonpath={range .items[*]}{.status.availableReplicas}{"\n"}{end}' 2> /dev/null || true)
  if [[ -n $namespace_state ]] && ! grep -Fxq '0' <<< "$namespace_state"; then
    printf 'argocd\tREADY\targocd\n'
  else
    printf 'argocd\tDEGRADED\targocd\n'
  fi
  root_state=$(core::kubectl get application "$(config::get ATLAS_ROOT_NAME)" --namespace argocd \
    --output 'jsonpath={.status.sync.status}{"/"}{.status.health.status}' 2> /dev/null || true)
  [[ -n $root_state ]] || root_state=ABSENT
  printf 'root\t%s\t%s\n' "$root_state" "$(config::get ATLAS_ROOT_NAME)"
  adoption_state=$(core::kubectl get application argocd-self --namespace argocd \
    --output 'jsonpath={.status.sync.status}{"/"}{.status.health.status}' 2> /dev/null || true)
  [[ -n $adoption_state ]] || adoption_state=ABSENT
  printf 'adoption\t%s\targocd-self\n' "$adoption_state"
}
