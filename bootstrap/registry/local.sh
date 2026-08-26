# shellcheck shell=bash

[[ -n ${_ATLAS_REGISTRY_LOADED:-} ]] && return 0
readonly _ATLAS_REGISTRY_LOADED=1

registry::_presence_state() {
  local name containers
  name=$(config::get ATLAS_REGISTRY_NAME) || {
    printf 'UNAVAILABLE\n'
    return 0
  }
  containers=$(runtime::docker container ls --all \
    --filter "name=^/${name}$" --format '{{.Names}}' 2> /dev/null) || {
    runtime::error "unable to inspect Registry container presence: ${name}"
    printf 'UNAVAILABLE\n'
    return 0
  }
  case "$containers" in
    '') printf 'ABSENT\n' ;;
    "$name") printf 'PRESENT\n' ;;
    *)
      runtime::error "unexpected Registry container presence result: ${name}"
      printf 'UNAVAILABLE\n'
      ;;
  esac
}

registry::_container_projection() {
  local name=$1
  runtime::docker inspect --format 'image={{.Config.Image}}
managed={{index .Config.Labels "io.atlas.managed"}}
cluster={{index .Config.Labels "io.atlas.cluster"}}
networkMode={{.HostConfig.NetworkMode}}
networkAttachmentCount={{len .NetworkSettings.Networks}}
kindNetworkAttached={{if index .NetworkSettings.Networks "kind"}}true{{else}}false{{end}}
restart={{.HostConfig.RestartPolicy.Name}}
restartMaximumRetryCount={{.HostConfig.RestartPolicy.MaximumRetryCount}}
portBindingCount={{len .HostConfig.PortBindings}}
registryBinding={{with (index .HostConfig.PortBindings "5000/tcp")}}{{len .}}{{range .}}|{{.HostIp}}|{{.HostPort}}{{end}}{{end}}
mountCount={{len .Mounts}}
dataMount={{range .Mounts}}{{.Type}}|{{.Name}}|{{.Destination}}|{{.RW}}{{end}}' "$name"
}

registry::_expected_container_projection() {
  local name image cluster port
  name=$(config::get ATLAS_REGISTRY_NAME) || return 1
  image=$(config::version REGISTRY_IMAGE) || return 1
  cluster=$(config::get ATLAS_CLUSTER_NAME) || return 1
  port=$(config::get ATLAS_REGISTRY_PORT) || return 1

  printf '%s\n' \
    "image=${image}" \
    'managed=true' \
    "cluster=${cluster}" \
    'networkMode=kind' \
    'networkAttachmentCount=1' \
    'kindNetworkAttached=true' \
    'restart=unless-stopped' \
    'restartMaximumRetryCount=0' \
    'portBindingCount=1' \
    "registryBinding=1|127.0.0.1|${port}" \
    'mountCount=1' \
    "dataMount=volume|${name}-data|/var/lib/registry|true"
}

registry::_projection_is_well_formed() {
  local projection=$1 line index=0
  local -a keys=(
    image
    managed
    cluster
    networkMode
    networkAttachmentCount
    kindNetworkAttached
    restart
    restartMaximumRetryCount
    portBindingCount
    registryBinding
    mountCount
    dataMount
  )
  while IFS= read -r line || [[ -n $line ]]; do
    ((index < ${#keys[@]})) || return 1
    [[ $line == "${keys[$index]}="* ]] || return 1
    ((index += 1))
  done <<< "$projection"
  ((index == ${#keys[@]}))
}

registry::_contract_state() {
  local name expected actual
  name=$(config::get ATLAS_REGISTRY_NAME) || return 1
  expected=$(registry::_expected_container_projection) || {
    printf 'UNAVAILABLE\n'
    return 0
  }
  actual=$(registry::_container_projection "$name") || {
    runtime::error "unable to inspect Registry container contract: ${name}"
    printf 'UNAVAILABLE\n'
    return 0
  }
  registry::_projection_is_well_formed "$actual" || {
    runtime::error "malformed Registry container contract projection: ${name}"
    printf 'UNAVAILABLE\n'
    return 0
  }
  if [[ $actual == "$expected" ]]; then
    printf 'MATCH\n'
  else
    printf 'DRIFTED\n'
  fi
}

registry::_runtime_state() {
  local name running
  name=$(config::get ATLAS_REGISTRY_NAME) || {
    printf 'UNAVAILABLE\n'
    return 0
  }
  running=$(runtime::docker inspect --format '{{.State.Running}}' "$name" 2> /dev/null) || {
    runtime::error "unable to inspect Registry runtime state: ${name}"
    printf 'UNAVAILABLE\n'
    return 0
  }
  case "$running" in
    true) printf 'RUNNING\n' ;;
    false) printf 'STOPPED\n' ;;
    *)
      runtime::error "unexpected Registry runtime state: ${name}"
      printf 'UNAVAILABLE\n'
      ;;
  esac
}

registry::_configure_node() {
  local node=$1 host port temporary target status=0
  host=$(config::get ATLAS_REGISTRY_HOST)
  port=$(config::get ATLAS_REGISTRY_PORT)
  target="/etc/containerd/certs.d/${host}:${port}"
  temporary=$(mktemp "${TMPDIR:-/tmp}/atlas-registry.XXXXXX")
  printf 'server = "http://%s:5000"\n\n[host."http://%s:5000"]\n  capabilities = ["pull", "resolve", "push"]\n' \
    "$(config::get ATLAS_REGISTRY_NAME)" "$(config::get ATLAS_REGISTRY_NAME)" > "$temporary"
  lock::assert_held || {
    rm -f "$temporary"
    return 1
  }
  runtime::docker exec "$node" mkdir -p "$target" || {
    status=$?
    rm -f "$temporary"
    return "$status"
  }
  lock::assert_held || {
    rm -f "$temporary"
    return 1
  }
  runtime::docker cp "$temporary" "${node}:${target}/hosts.toml" || status=$?
  rm -f "$temporary"
  return "$status"
}

registry::_health() {
  curl --noproxy '*' --fail --silent --show-error \
    --connect-timeout 2 --max-time 5 \
    "http://$(config::get ATLAS_REGISTRY_HOST):$(config::get ATLAS_REGISTRY_PORT)/v2/" > /dev/null
}

registry::_node_has_image() {
  local node=$1 image=$2
  runtime::docker exec "$node" ctr --namespace k8s.io images list --quiet "name==${image}" | grep -Fxq "$image"
}

registry::_load_node_image() {
  local node=$1 image=$2 platform digest source_refs source_ref
  registry::_node_has_image "$node" "$image" && return 0

  platform=$(runtime::docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")
  digest=${image##*@}
  # Import offline, then restore the exact digest-pinned reference expected by
  # the workloads; Kind's convenience loader is not part of this supply chain.
  lock::assert_held || return 1
  runtime::docker image save "$image" | runtime::docker exec --privileged --interactive "$node" \
    ctr --namespace k8s.io images import \
    --platform "$platform" \
    --digests \
    --snapshotter overlayfs - > /dev/null || return 1

  source_refs=$(runtime::docker exec "$node" ctr --namespace k8s.io images list --quiet "target.digest==${digest}")
  source_ref=${source_refs%%$'\n'*}
  [[ -n $source_ref ]] || {
    runtime::die "imported image digest is absent on node: node=${node} image=${image}"
    return 1
  }
  lock::assert_held || return 1
  runtime::docker exec "$node" ctr --namespace k8s.io images tag "$source_ref" "$image" > /dev/null || return 1
  registry::_node_has_image "$node" "$image" || {
    runtime::die "locked image reference is absent after import: node=${node} image=${image}"
    return 1
  }
}

registry::_preload_seed_images() {
  local node image nodes_output
  local -a nodes=()
  nodes_output=$(cluster::list_validated_kind_node_containers) || return 1
  mapfile -t nodes <<< "$nodes_output"
  ((${#nodes[@]} > 0)) || {
    runtime::die "Kind returned no Kubernetes nodes while preloading images"
    return 1
  }

  for image in "$(config::version ARGOCD_IMAGE)" "$(config::version REDIS_IMAGE)"; do
    runtime::docker_image_present "$image" || {
      runtime::die "Bootstrap image is not available locally: ${image}"
      return 1
    }
    for node in "${nodes[@]}"; do
      registry::_load_node_image "$node" "$image"
    done
  done
}

registry::ensure_local() {
  runtime::phase registry
  local name image port cluster node nodes_output presence_state contract_state runtime_state
  local -a nodes=()
  name=$(config::get ATLAS_REGISTRY_NAME) || return 1
  image=$(config::version REGISTRY_IMAGE) || return 1
  port=$(config::get ATLAS_REGISTRY_PORT) || return 1
  cluster=$(config::get ATLAS_CLUSTER_NAME) || return 1

  runtime::assert_docker_authority || return 1
  runtime::docker_image_present "$image" || {
    runtime::die "Registry image is not available locally: ${image}"
    return 1
  }
  runtime::docker network inspect kind > /dev/null 2>&1 || {
    runtime::die "Kind network is absent; ensure the cluster first"
    return 1
  }

  presence_state=$(registry::_presence_state) || return 1
  case "$presence_state" in
    PRESENT)
      contract_state=$(registry::_contract_state) || return 1
      case "$contract_state" in
        MATCH) ;;
        DRIFTED)
          runtime::die "Registry container contract drift: ${name}"
          return 1
          ;;
        *)
          runtime::die "Registry container contract is unavailable: ${name}"
          return 1
          ;;
      esac
      runtime_state=$(registry::_runtime_state) || return 1
      case "$runtime_state" in
        RUNNING) ;;
        STOPPED)
          lock::assert_held || return 1
          runtime::docker start "$name" > /dev/null || return 1
          ;;
        *)
          runtime::die "Registry runtime state is unavailable: ${name}"
          return 1
          ;;
      esac
      ;;
    ABSENT)
      lock::assert_held || return 1
      if ! runtime::docker run --detach \
        --name "$name" \
        --network kind \
        --restart unless-stopped \
        --label io.atlas.managed=true \
        --label "io.atlas.cluster=${cluster}" \
        --publish "127.0.0.1:${port}:5000" \
        --volume "${name}-data:/var/lib/registry" \
        "$image" > /dev/null; then
        presence_state=$(registry::_presence_state) || return 1
        if [[ $presence_state == PRESENT ]]; then
          contract_state=$(registry::_contract_state) || return 1
          if [[ $contract_state == MATCH ]]; then
            lock::assert_held || return 1
            runtime::docker rm "$name" > /dev/null || true
          fi
        fi
        return 1
      fi
      ;;
    *)
      runtime::die "Registry container presence is unavailable: ${name}"
      return 1
      ;;
  esac

  nodes_output=$(cluster::list_validated_kind_node_containers) || return 1
  mapfile -t nodes <<< "$nodes_output"
  ((${#nodes[@]} > 0)) || {
    runtime::die "Kind returned no Kubernetes nodes for ${cluster}"
    return 1
  }
  for node in "${nodes[@]}"; do
    registry::_configure_node "$node"
  done
  runtime::wait_for 30 1 "local Registry" registry::_health
  registry::_preload_seed_images
  runtime::ok "local Registry and seed image cache are ready"
}

registry::inspect_status() {
  local name presence_state contract_state runtime_state
  name=$(config::get ATLAS_REGISTRY_NAME) || return 2
  if ! runtime::assert_docker_authority || ! runtime::docker info > /dev/null 2>&1; then
    printf 'registry\tUNAVAILABLE\t%s\n' "$name"
    return 2
  fi
  presence_state=$(registry::_presence_state) || presence_state=UNAVAILABLE
  case "$presence_state" in
    ABSENT)
      printf 'registry\tABSENT\t%s\n' "$name"
      return 0
      ;;
    PRESENT) ;;
    *)
      printf 'registry\tUNAVAILABLE\t%s\n' "$name"
      return 2
      ;;
  esac

  contract_state=$(registry::_contract_state) || contract_state=UNAVAILABLE
  case "$contract_state" in
    MATCH) ;;
    DRIFTED)
      printf 'registry\tDRIFTED\t%s\n' "$name"
      return 1
      ;;
    *)
      printf 'registry\tUNAVAILABLE\t%s\n' "$name"
      return 2
      ;;
  esac

  runtime_state=$(registry::_runtime_state) || runtime_state=UNAVAILABLE
  case "$runtime_state" in
    RUNNING) ;;
    STOPPED)
      printf 'registry\tDRIFTED\t%s\n' "$name"
      return 1
      ;;
    *)
      printf 'registry\tUNAVAILABLE\t%s\n' "$name"
      return 2
      ;;
  esac

  if registry::_health; then
    printf 'registry\tREADY\t%s\n' "$name"
    return 0
  fi
  printf 'registry\tDRIFTED\t%s\n' "$name"
  return 1
}
