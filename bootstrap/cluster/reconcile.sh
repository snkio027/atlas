# shellcheck shell=bash

[[ -n ${_ATLAS_CLUSTER_LOADED:-} ]] && return 0
readonly _ATLAS_CLUSTER_LOADED=1

cluster::_config_path() {
  printf '%s/%s\n' "$ATLAS_ROOT_DIR" "$(config::get ATLAS_KIND_CONFIG)"
}

cluster::_identity() {
  core::sha256 "$(cluster::_config_path)"
}

cluster::_validate_nodes() {
  local cluster image
  cluster=$(config::get ATLAS_CLUSTER_NAME)
  image=$(config::version KIND_NODE_IMAGE)
  local -a nodes=()
  mapfile -t nodes < <(docker ps -a --filter "label=io.x-k8s.kind.cluster=${cluster}" --format '{{.Names}}')
  ((${#nodes[@]} == 1)) || {
    core::die "expected one Kind node for ${cluster}, found ${#nodes[@]}"
    return 1
  }
  [[ ${nodes[0]} == "${cluster}-control-plane" ]] || {
    core::die "unexpected Kind node: ${nodes[0]}"
    return 1
  }
  local actual_image
  actual_image=$(docker inspect --format '{{.Config.Image}}' "${nodes[0]}")
  [[ $actual_image == "$image" ]] || {
    core::die "Kind node image drift: expected=${image} actual=${actual_image}"
    return 1
  }
}

cluster::_marker_matches() {
  local expected_repo expected_hash actual
  expected_repo=$(config::get ATLAS_GIT_REPO_URL)
  expected_hash=$(cluster::_identity)
  actual=$(core::kubectl get configmap atlas-bootstrap-identity \
    --namespace kube-system \
    --output 'jsonpath={.data.repo}{"\t"}{.data.kindConfigSHA}{"\n"}' 2> /dev/null) || return 1
  [[ $actual == "${expected_repo}"$'\t'"${expected_hash}" ]]
}

cluster::_create_marker() {
  core::kubectl create configmap atlas-bootstrap-identity \
    --namespace kube-system \
    --from-literal "repo=$(config::get ATLAS_GIT_REPO_URL)" \
    --from-literal "kindConfigSHA=$(cluster::_identity)" > /dev/null
}

cluster::reconcile() {
  core::phase cluster
  local cluster image config_file timeout status
  cluster=$(config::get ATLAS_CLUSTER_NAME)
  image=$(config::version KIND_NODE_IMAGE)
  config_file=$(cluster::_config_path)
  timeout=$(config::get ATLAS_READY_TIMEOUT)

  core::docker_image_present "$image" || {
    core::die "Kind node image is not available locally: ${image}"
    return 1
  }

  if core::kind_cluster_exists "$cluster"; then
    core::info "Kind cluster already exists: ${cluster}"
    cluster::_validate_nodes
    cluster::_marker_matches || {
      core::die "existing cluster is not owned by this Atlas configuration: ${cluster}"
      return 1
    }
  else
    status=$?
    ((status == 1)) || return "$status"
    core::info "creating Kind cluster: ${cluster}"
    kind create cluster \
      --name "$cluster" \
      --image "$image" \
      --config "$config_file" \
      --wait "$timeout"
    cluster::_validate_nodes
    cluster::_create_marker
  fi

  core::kubectl wait node --all --for condition=Ready --timeout "$timeout" > /dev/null
  core::ok "Kind substrate is ready: ${cluster}"
}

cluster::status() {
  local cluster status
  cluster=$(config::get ATLAS_CLUSTER_NAME)
  if core::kind_cluster_exists "$cluster"; then
    if cluster::_validate_nodes && cluster::_marker_matches; then
      printf 'cluster\tREADY\t%s\n' "$cluster"
    else
      printf 'cluster\tDRIFTED\t%s\n' "$cluster"
      return 1
    fi
  else
    status=$?
    if ((status == 1)); then
      printf 'cluster\tABSENT\t%s\n' "$cluster"
      return 0
    fi
    printf 'cluster\tUNAVAILABLE\t%s\n' "$cluster"
    return "$status"
  fi
}
