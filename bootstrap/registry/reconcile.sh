# shellcheck shell=bash

[[ -n ${_ATLAS_REGISTRY_LOADED:-} ]] && return 0
readonly _ATLAS_REGISTRY_LOADED=1

registry::_container_exists() {
  docker container inspect "$(config::get ATLAS_REGISTRY_NAME)" > /dev/null 2>&1
}

registry::_validate_container() {
  local name image expected_cluster expected_binding actual
  name=$(config::get ATLAS_REGISTRY_NAME)
  image=$(config::version REGISTRY_IMAGE)
  expected_cluster=$(config::get ATLAS_CLUSTER_NAME)
  expected_binding="127.0.0.1:$(config::get ATLAS_REGISTRY_PORT)"

  actual=$(docker inspect --format '{{.Config.Image}}' "$name")
  [[ $actual == "$image" ]] || {
    core::die "Registry image drift: expected=${image} actual=${actual}"
    return 1
  }
  actual=$(docker inspect --format '{{index .Config.Labels "io.atlas.managed"}}' "$name")
  [[ $actual == true ]] || {
    core::die "Registry ownership label is missing: ${name}"
    return 1
  }
  actual=$(docker inspect --format '{{index .Config.Labels "io.atlas.cluster"}}' "$name")
  [[ $actual == "$expected_cluster" ]] || {
    core::die "Registry cluster label drift: ${name}"
    return 1
  }
  actual=$(docker inspect --format '{{.HostConfig.NetworkMode}}' "$name")
  [[ $actual == kind ]] || {
    core::die "Registry is not attached to the Kind network: ${name}"
    return 1
  }
  actual=$(docker inspect --format '{{with (index .HostConfig.PortBindings "5000/tcp")}}{{(index . 0).HostIp}}:{{(index . 0).HostPort}}{{end}}' "$name")
  [[ $actual == "$expected_binding" ]] || {
    core::die "Registry port binding drift: expected=${expected_binding} actual=${actual:-none}"
    return 1
  }
}

registry::_running() {
  [[ $(docker inspect --format '{{.State.Running}}' "$(config::get ATLAS_REGISTRY_NAME)") == true ]]
}

registry::_configure_node() {
  local node=$1 host port temporary target
  host=$(config::get ATLAS_REGISTRY_HOST)
  port=$(config::get ATLAS_REGISTRY_PORT)
  target="/etc/containerd/certs.d/${host}:${port}"
  temporary=$(mktemp "${TMPDIR:-/tmp}/atlas-registry.XXXXXX")
  printf 'server = "http://%s:5000"\n\n[host."http://%s:5000"]\n  capabilities = ["pull", "resolve", "push"]\n' \
    "$(config::get ATLAS_REGISTRY_NAME)" "$(config::get ATLAS_REGISTRY_NAME)" > "$temporary"
  if docker exec "$node" mkdir -p "$target" && docker cp "$temporary" "${node}:${target}/hosts.toml"; then
    rm -f "$temporary"
    return 0
  fi
  local status=$?
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
  docker exec "$node" ctr --namespace k8s.io images list --quiet "name==${image}" | grep -Fxq "$image"
}

registry::_load_node_image() {
  local node=$1 image=$2 platform digest sources source
  registry::_node_has_image "$node" "$image" && return 0

  platform=$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")
  digest=${image##*@}
  docker image save "$image" | docker exec --privileged --interactive "$node" \
    ctr --namespace k8s.io images import \
    --platform "$platform" \
    --digests \
    --snapshotter overlayfs - > /dev/null

  sources=$(docker exec "$node" ctr --namespace k8s.io images list --quiet "target.digest==${digest}")
  source=${sources%%$'\n'*}
  [[ -n $source ]] || {
    core::die "imported image digest is absent on node: node=${node} image=${image}"
    return 1
  }
  docker exec "$node" ctr --namespace k8s.io images tag "$source" "$image" > /dev/null
  registry::_node_has_image "$node" "$image" || {
    core::die "locked image reference is absent after import: node=${node} image=${image}"
    return 1
  }
}

registry::_preload_seed_images() {
  local node image
  local -a nodes=()
  mapfile -t nodes < <(kind get nodes --name "$(config::get ATLAS_CLUSTER_NAME)")
  ((${#nodes[@]} > 0)) || {
    core::die "Kind returned no nodes while preloading images"
    return 1
  }

  for image in "$(config::version ARGOCD_IMAGE)" "$(config::version REDIS_IMAGE)"; do
    core::docker_image_present "$image" || {
      core::die "Bootstrap image is not available locally: ${image}"
      return 1
    }
    for node in "${nodes[@]}"; do
      registry::_load_node_image "$node" "$image"
    done
  done
}

registry::reconcile() {
  core::phase registry
  local name image port cluster node
  local -a nodes=()
  name=$(config::get ATLAS_REGISTRY_NAME)
  image=$(config::version REGISTRY_IMAGE)
  port=$(config::get ATLAS_REGISTRY_PORT)
  cluster=$(config::get ATLAS_CLUSTER_NAME)

  core::docker_image_present "$image" || {
    core::die "Registry image is not available locally: ${image}"
    return 1
  }
  docker network inspect kind > /dev/null 2>&1 || {
    core::die "Kind network is absent; reconcile the cluster first"
    return 1
  }

  if registry::_container_exists; then
    registry::_validate_container
    registry::_running || docker start "$name" > /dev/null
  else
    if ! docker run --detach \
      --name "$name" \
      --network kind \
      --restart unless-stopped \
      --label io.atlas.managed=true \
      --label "io.atlas.cluster=${cluster}" \
      --publish "127.0.0.1:${port}:5000" \
      --volume "${name}-data:/var/lib/registry" \
      "$image" > /dev/null; then
      if registry::_container_exists && registry::_validate_container; then
        docker rm "$name" > /dev/null || true
      fi
      return 1
    fi
  fi

  mapfile -t nodes < <(kind get nodes --name "$cluster")
  ((${#nodes[@]} > 0)) || {
    core::die "Kind returned no nodes for ${cluster}"
    return 1
  }
  for node in "${nodes[@]}"; do
    registry::_configure_node "$node"
  done
  core::wait_for 30 1 "local Registry" registry::_health
  registry::_preload_seed_images
  core::ok "local Registry and seed image cache are ready"
}

registry::status() {
  local name
  name=$(config::get ATLAS_REGISTRY_NAME)
  if ! docker info > /dev/null 2>&1; then
    printf 'registry\tUNAVAILABLE\t%s\n' "$name"
    return 2
  fi
  if ! registry::_container_exists; then
    printf 'registry\tABSENT\t%s\n' "$name"
    return 0
  fi
  if registry::_validate_container && registry::_running && registry::_health; then
    printf 'registry\tREADY\t%s\n' "$name"
    return 0
  fi
  printf 'registry\tDRIFTED\t%s\n' "$name"
  return 1
}
