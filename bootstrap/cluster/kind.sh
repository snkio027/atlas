# shellcheck shell=bash

[[ -n ${_ATLAS_CLUSTER_LOADED:-} ]] && return 0
readonly _ATLAS_CLUSTER_LOADED=1

cluster::_config_path() {
  printf '%s/%s\n' "$ATLAS_ROOT_DIR" "$(config::get ATLAS_KIND_CONFIG)"
}

cluster::_identity() {
  runtime::sha256 "$(cluster::_config_path)"
}

cluster::_parse_kind_node_roles() {
  local config_file=${1:-$(cluster::_config_path)} line leading_spaces
  local in_nodes=false nodes_seen=0 control_planes=0 workers=0 literal_indent=-1 indent=0
  local -a roles=()

  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line != *$'\t'* ]] || {
      runtime::die "Kind config contains a tab: ${config_file}"
      return 1
    }

    if [[ $line =~ ^nodes[[:space:]]*: ]]; then
      [[ $line == nodes: ]] || {
        runtime::die "Kind config must use the canonical top-level nodes declaration: ${config_file}"
        return 1
      }
      ((nodes_seen += 1))
      ((nodes_seen == 1)) || {
        runtime::die "Kind config contains more than one top-level nodes declaration: ${config_file}"
        return 1
      }
      in_nodes=true
      continue
    fi

    [[ $in_nodes == true ]] || continue
    [[ -z $line || $line =~ ^[[:space:]]+$ ]] && continue
    leading_spaces=${line%%[! ]*}
    indent=${#leading_spaces}
    if ((literal_indent >= 0)); then
      if ((indent > literal_indent)); then
        continue
      fi
      literal_indent=-1
    fi
    if [[ $line =~ ^[^[:space:]#] ]]; then
      in_nodes=false
      continue
    fi
    [[ $line =~ ^[[:space:]]*# ]] && continue

    if [[ $line =~ ^\ \ -\ role:\ (control-plane|worker)$ ]]; then
      roles+=("${BASH_REMATCH[1]}")
      if [[ ${BASH_REMATCH[1]} == control-plane ]]; then
        ((control_planes += 1))
      else
        ((workers += 1))
      fi
      continue
    fi

    if [[ $line =~ ^\ \ - ]]; then
      runtime::die "every Kind node must start with an explicit canonical role: ${line}"
      return 1
    fi
    if ((indent < 4)) || { ((indent == 4)) && [[ $line =~ ^\ {4}- ]]; }; then
      runtime::die "unrecognized Kind node entry or indentation: ${line}"
      return 1
    fi
    if [[ $line =~ ^\ {4}image[[:space:]]*: ]]; then
      runtime::die "node-level image is forbidden; versions.lock is authoritative"
      return 1
    fi
    if [[ $line =~ ^\ {4}role[[:space:]]*: ]]; then
      runtime::die "Kind node role must appear only in the node entry"
      return 1
    fi
    if [[ $line =~ ^\ {4,}(-\ )?[^#]*[\|\>][+-]?[[:space:]]*(#.*)?$ ]]; then
      literal_indent=$indent
      continue
    fi
    if [[ $line =~ ^\ {4}.*(\&[[:alnum:]_-]+|\*[[:alnum:]_-]+|\<\<:|\{|\[) ]]; then
      runtime::die "Kind node mappings must not use aliases, anchors, merge keys, or flow style"
      return 1
    fi
  done < "$config_file"

  ((nodes_seen == 1)) || {
    runtime::die "Kind config must contain exactly one top-level nodes declaration: ${config_file}"
    return 1
  }
  ((control_planes == 1)) || {
    # More than one control-plane introduces Kind's implicit Envoy load
    # balancer, which is outside the locked Atlas image supply chain.
    runtime::die "Atlas supports exactly one Kind control-plane, found ${control_planes}"
    return 1
  }
  ((${#roles[@]} == control_planes + workers)) || {
    runtime::die "Kind node topology could not be parsed completely: ${config_file}"
    return 1
  }
  printf '%s\n' "${roles[@]}"
}

cluster::_inspect_kind_containers() {
  local cluster image names_output name details actual_cluster role actual_image running
  cluster=$(config::get ATLAS_CLUSTER_NAME)
  image=$(config::version KIND_NODE_IMAGE)

  names_output=$(docker ps -a \
    --filter "label=io.x-k8s.kind.cluster=${cluster}" \
    --format '{{.Names}}') || {
    runtime::die "unable to inspect Kind containers for ${cluster}"
    return 1
  }
  [[ -n $names_output ]] || {
    runtime::die "no Kind containers found for ${cluster}"
    return 1
  }

  while IFS= read -r name; do
    details=$(docker inspect --format \
      '{{index .Config.Labels "io.x-k8s.kind.cluster"}}{{"|"}}{{index .Config.Labels "io.x-k8s.kind.role"}}{{"|"}}{{.Config.Image}}{{"|"}}{{.State.Running}}' \
      "$name") || {
      runtime::die "unable to inspect Kind container: ${name}"
      return 1
    }
    IFS='|' read -r actual_cluster role actual_image running <<< "$details"
    [[ $actual_cluster == "$cluster" ]] || {
      runtime::die "Kind container cluster label drift: container=${name} actual=${actual_cluster:-missing}"
      return 1
    }
    case "$role" in
      control-plane | worker) ;;
      external-load-balancer)
        runtime::die "Kind external load balancer is unsupported; Atlas requires exactly one control-plane"
        return 1
        ;;
      *)
        runtime::die "unknown Kind container role: container=${name} role=${role:-missing}"
        return 1
        ;;
    esac
    [[ $actual_image == "$image" ]] || {
      runtime::die "Kind node image drift: node=${name} expected=${image} actual=${actual_image}"
      return 1
    }
    [[ $running == true ]] || {
      runtime::die "Kind node is not running: ${name}"
      return 1
    }
    printf '%s|%s\n' "$name" "$role"
  done <<< "$names_output"
}

cluster::list_kind_node_containers() {
  local inventory name role
  cluster::_validate_nodes || return 1
  inventory=$(cluster::_inspect_kind_containers) || return 1
  while IFS='|' read -r name role; do
    [[ -n $name && -n $role ]] || {
      runtime::die "invalid Kind node inventory"
      return 1
    }
    printf '%s\n' "$name"
  done <<< "$inventory"
}

cluster::_validate_nodes() {
  local expected_output inventory kubernetes_output control_plane_output name role ready
  local expected_control_planes=0 expected_workers=0 actual_control_planes=0 actual_workers=0
  local -A docker_roles=() kubernetes_nodes=() kubernetes_control_planes=()

  expected_output=$(cluster::_parse_kind_node_roles) || return 1
  while IFS= read -r role; do
    case "$role" in
      control-plane) ((expected_control_planes += 1)) ;;
      worker) ((expected_workers += 1)) ;;
      *)
        runtime::die "unexpected parsed Kind node role: ${role:-missing}"
        return 1
        ;;
    esac
  done <<< "$expected_output"

  inventory=$(cluster::_inspect_kind_containers) || return 1
  while IFS='|' read -r name role; do
    [[ -z ${docker_roles[$name]+set} ]] || {
      runtime::die "duplicate Kind container name: ${name}"
      return 1
    }
    docker_roles[$name]=$role
    if [[ $role == control-plane ]]; then
      ((actual_control_planes += 1))
    else
      ((actual_workers += 1))
    fi
  done <<< "$inventory"

  ((actual_control_planes == expected_control_planes && actual_workers == expected_workers)) || {
    runtime::die "Kind node role count drift: expected=control-plane:${expected_control_planes},worker:${expected_workers} actual=control-plane:${actual_control_planes},worker:${actual_workers}"
    return 1
  }

  kubernetes_output=$(runtime::kubectl get nodes \
    --output 'jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}') || {
    runtime::die "unable to list Kubernetes Nodes"
    return 1
  }
  [[ -n $kubernetes_output ]] || {
    runtime::die "Kubernetes returned no Nodes"
    return 1
  }
  while IFS= read -r name; do
    [[ -n $name ]] || continue
    [[ -z ${kubernetes_nodes[$name]+set} ]] || {
      runtime::die "duplicate Kubernetes Node name: ${name}"
      return 1
    }
    kubernetes_nodes[$name]=true
  done <<< "$kubernetes_output"

  ((${#kubernetes_nodes[@]} == ${#docker_roles[@]})) || {
    runtime::die "Docker and Kubernetes node counts differ: docker=${#docker_roles[@]} kubernetes=${#kubernetes_nodes[@]}"
    return 1
  }
  for name in "${!docker_roles[@]}"; do
    [[ -n ${kubernetes_nodes[$name]+set} ]] || {
      runtime::die "Kind container is absent from Kubernetes Nodes: ${name}"
      return 1
    }
  done
  for name in "${!kubernetes_nodes[@]}"; do
    [[ -n ${docker_roles[$name]+set} ]] || {
      runtime::die "Kubernetes Node is absent from Kind containers: ${name}"
      return 1
    }
  done

  control_plane_output=$(runtime::kubectl get nodes \
    --selector node-role.kubernetes.io/control-plane \
    --output 'jsonpath={range .items[*]}{.metadata.name}{"\n"}{end}') || {
    runtime::die "unable to list Kubernetes control-plane Nodes"
    return 1
  }
  if [[ -n $control_plane_output ]]; then
    while IFS= read -r name; do
      [[ -n $name ]] || continue
      kubernetes_control_planes[$name]=true
    done <<< "$control_plane_output"
  fi
  ((${#kubernetes_control_planes[@]} == expected_control_planes)) || {
    runtime::die "Kubernetes control-plane label count drift: expected=${expected_control_planes} actual=${#kubernetes_control_planes[@]}"
    return 1
  }

  for name in "${!docker_roles[@]}"; do
    if [[ ${docker_roles[$name]} == control-plane ]]; then
      [[ -n ${kubernetes_control_planes[$name]+set} ]] || {
        runtime::die "Kind control-plane lacks the Kubernetes control-plane label: ${name}"
        return 1
      }
    else
      [[ -z ${kubernetes_control_planes[$name]+set} ]] || {
        runtime::die "Kind worker has the Kubernetes control-plane label: ${name}"
        return 1
      }
    fi
    ready=$(runtime::kubectl get node "$name" \
      --output 'jsonpath={range .status.conditions[?(@.type=="Ready")]}{.status}{end}') || {
      runtime::die "unable to inspect Kubernetes Node readiness: ${name}"
      return 1
    }
    [[ $ready == True ]] || {
      runtime::die "Kubernetes Node is not Ready: node=${name} status=${ready:-missing}"
      return 1
    }
  done
}

cluster::_marker_matches() {
  local expected_repo expected_hash actual
  expected_repo=$(config::get ATLAS_GIT_REPO_URL)
  expected_hash=$(cluster::_identity)
  actual=$(runtime::kubectl get configmap atlas-bootstrap-identity \
    --namespace kube-system \
    --output 'jsonpath={.data.repo}{"\t"}{.data.kindConfigSHA}{"\n"}' 2> /dev/null) || return 1
  [[ $actual == "${expected_repo}"$'\t'"${expected_hash}" ]]
}

cluster::_create_marker() {
  runtime::kubectl create configmap atlas-bootstrap-identity \
    --namespace kube-system \
    --from-literal "repo=$(config::get ATLAS_GIT_REPO_URL)" \
    --from-literal "kindConfigSHA=$(cluster::_identity)" > /dev/null
}

cluster::ensure_kind() {
  runtime::phase cluster
  local cluster image config_file timeout status existing=false
  cluster=$(config::get ATLAS_CLUSTER_NAME)
  image=$(config::version KIND_NODE_IMAGE)
  config_file=$(cluster::_config_path)
  timeout=$(config::get ATLAS_READY_TIMEOUT)

  cluster::_parse_kind_node_roles "$config_file" > /dev/null || return 1
  runtime::docker_image_present "$image" || {
    runtime::die "Kind node image is not available locally: ${image}"
    return 1
  }

  if runtime::kind_cluster_exists "$cluster"; then
    existing=true
    runtime::info "Kind cluster already exists: ${cluster}"
  else
    status=$?
    ((status == 1)) || return "$status"
    runtime::info "creating Kind cluster: ${cluster}"
    kind create cluster \
      --name "$cluster" \
      --image "$image" \
      --config "$config_file" \
      --wait "$timeout" || return 1
  fi

  runtime::kubectl wait node --all --for condition=Ready --timeout "$timeout" > /dev/null || return 1
  cluster::_validate_nodes || return 1
  if [[ $existing == true ]]; then
    cluster::_marker_matches || {
      runtime::die "existing cluster is not owned by this Atlas configuration: ${cluster}"
      return 1
    }
  else
    cluster::_create_marker || return 1
  fi
  runtime::ok "Kind substrate is ready: ${cluster}"
}

cluster::inspect_status() {
  local cluster status
  cluster=$(config::get ATLAS_CLUSTER_NAME)
  if runtime::kind_cluster_exists "$cluster"; then
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
