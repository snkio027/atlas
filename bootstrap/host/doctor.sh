# shellcheck shell=bash

[[ -n ${_ATLAS_HOST_LOADED:-} ]] && return 0
readonly _ATLAS_HOST_LOADED=1

host::_tool_version() {
  local tool=$1 expected=$2 actual=
  case "$tool" in
    bash) actual=$(runtime::version_triplet "$BASH_VERSION") ;;
    kind) actual=$(runtime::kind version 2> /dev/null | awk '{print $2}' | sed 's/^v//') ;;
    kubectl) actual=$(kubectl version --client 2> /dev/null | awk '/Client Version:/ {sub(/^v/, "", $3); print $3; exit}') ;;
    helm)
      actual=$(helm version --template '{{.Version}}' 2> /dev/null)
      actual=${actual#v}
      actual=${actual%%+*}
      ;;
    *) return 2 ;;
  esac
  runtime::require_exact_version "$tool" "$actual" "$expected"
}

host::_file() {
  local file=$1
  [[ -f $file && ! -L $file && -s $file ]] || runtime::die "required file is missing, empty, or unsafe: ${file}"
}

host::_image() {
  local image=$1
  runtime::docker_image_present "$image" || runtime::die "locked image is not available locally: ${image}"
}

host::doctor() {
  runtime::phase doctor
  local failures=0 tool image_key root chart chart_path expected_sha actual_sha relative
  root=$ATLAS_ROOT_DIR

  if [[ $(uname -s) == Darwin ]]; then
    runtime::ok "host operating system: Darwin"
  else
    runtime::die "unsupported host operating system: $(uname -s)" || true
    ((failures += 1))
  fi
  if [[ $(uname -m) == arm64 ]]; then
    runtime::ok "host architecture: arm64"
  else
    runtime::die "unsupported host architecture: $(uname -m)" || true
    ((failures += 1))
  fi

  for tool in bash docker kind kubectl helm git shasum awk sed grep mktemp curl env; do
    if runtime::command_exists "$tool"; then
      runtime::ok "command available: ${tool}"
    else
      runtime::die "required command is missing: ${tool}" || true
      ((failures += 1))
    fi
  done

  if ((failures == 0)); then
    runtime::assert_docker_authority || ((failures += 1))
  fi

  if ((failures == 0)); then
    host::_tool_version bash "$(config::version BASH_VERSION)" || ((failures += 1))
    host::_tool_version kind "$(config::version KIND_VERSION)" || ((failures += 1))
    host::_tool_version kubectl "$(config::version KUBECTL_VERSION)" || ((failures += 1))
    host::_tool_version helm "$(config::version HELM_VERSION)" || ((failures += 1))
  fi

  if runtime::docker info > /dev/null 2>&1; then
    runtime::ok "Docker daemon is available"
  else
    runtime::die "Docker daemon is unavailable" || true
    ((failures += 1))
  fi

  for relative in \
    "$(config::get ATLAS_KIND_CONFIG)" \
    bootstrap/argocd/root-app.yaml.tpl \
    bootstrap/argocd/atlas-bootstrap-project.yaml \
    gitops/platform/applications/base/argocd-self-app.yaml \
    gitops/platform/management/argocd-self/base/argocd-cm.yaml \
    gitops/platform/management/argocd-self/values.yaml \
    "$(config::get ATLAS_ROOT_PATH)/kustomization.yaml"; do
    host::_file "${root}/${relative}" || ((failures += 1))
  done

  chart="${root}/$(config::version ARGOCD_CHART_FILE)"
  if host::_file "$chart"; then
    expected_sha=$(config::version ARGOCD_CHART_SHA256)
    actual_sha=$(runtime::sha256 "$chart")
    [[ $actual_sha == "$expected_sha" ]] || {
      runtime::die "Argo CD chart checksum mismatch: ${chart}" || true
      ((failures += 1))
    }
  else
    ((failures += 1))
  fi

  chart_path="${root}/$(config::version ARGOCD_CHART_PATH)"
  if host::_file "${chart_path}/Chart.yaml"; then
    grep -Fxq "version: $(config::version ARGOCD_CHART_VERSION)" "${chart_path}/Chart.yaml" || {
      runtime::die "vendored Argo CD chart version mismatch: ${chart_path}" || true
      ((failures += 1))
    }
    grep -Fxq "appVersion: v$(config::version ARGOCD_VERSION)" "${chart_path}/Chart.yaml" || {
      runtime::die "vendored Argo CD application version mismatch: ${chart_path}" || true
      ((failures += 1))
    }
  else
    ((failures += 1))
  fi

  for image_key in KIND_NODE_IMAGE REGISTRY_IMAGE ARGOCD_IMAGE REDIS_IMAGE; do
    host::_image "$(config::version "$image_key")" || ((failures += 1))
  done

  if ((failures > 0)); then
    runtime::error "doctor failed: ${failures} check(s) require attention"
    return 1
  fi
  runtime::ok "doctor passed"
}
