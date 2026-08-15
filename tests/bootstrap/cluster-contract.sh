#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

# shellcheck source=bootstrap/lib/runtime.sh
source bootstrap/lib/runtime.sh
# shellcheck source=bootstrap/cluster/kind.sh
source bootstrap/cluster/kind.sh

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-cluster-contract.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

valid_config="${test_workspace}/valid.yaml"
cat > "$valid_config" << 'EOF'
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
networking:
  ipFamily: ipv4
nodes:
  - role: control-plane
    labels:
      atlas.io/node-pool: control
  - role: worker
    labels:
      atlas.io/node-pool: gateway
  - role: worker
    extraMounts:
      - hostPath: /tmp/source
        containerPath: /mnt/source
  - role: worker
    kubeadmConfigPatches:
      - |
        kind: JoinConfiguration
        nodeRegistration:
          featureGates: {Example: true}
          imagePullPolicy: "*literal-content"
          taints:
            - key: "atlas.io/node-pool"
              value: data
              effect: "NoSchedule"
EOF

assert_valid_roles() {
  local label=$1 fixture=$2 expected=$3 shell_roles yq_roles
  shell_roles=$(cluster::_parse_kind_node_roles "$fixture")
  yq_roles=$(yq -r '.nodes[].role' "$fixture")
  [[ $shell_roles == "$yq_roles" ]] || test::fail "${label}: Shell and yq role sequences differ"
  [[ $shell_roles == "$expected" ]] || test::fail "${label}: unexpected role sequence"
}

one_node_config="${test_workspace}/one-node.yaml"
printf 'nodes:\n  - role: control-plane\n' > "$one_node_config"
assert_valid_roles "one node" "$one_node_config" control-plane

two_node_config="${test_workspace}/two-node.yaml"
printf 'nodes:\n  - role: control-plane\n  - role: worker\n    extraPortMappings:\n      - containerPort: 8080\n        hostPort: 18080\n' > "$two_node_config"
assert_valid_roles "two nodes" "$two_node_config" $'control-plane\nworker'

assert_valid_roles "four nodes with nested configuration" "$valid_config" $'control-plane\nworker\nworker\nworker'

generated_config="${test_workspace}/generated-nodes.yaml"
printf 'nodes:\n  - role: control-plane\n' > "$generated_config"
generated_roles=control-plane
for ((worker_index = 1; worker_index <= 24; worker_index++)); do
  printf '  - role: worker\n' >> "$generated_config"
  generated_roles+=$'\nworker'
done
assert_valid_roles "generated 25-node topology" "$generated_config" "$generated_roles"
test::pass "canonical 1-node, 2-node, 4-node, and generated N-node topologies match yq"

assert_rejected() {
  local label=$1 content=$2 fixture output
  fixture="${test_workspace}/invalid.yaml"
  printf '%s' "$content" > "$fixture"
  if output=$(cluster::_parse_kind_node_roles "$fixture" 2>&1); then
    test::fail "${label} was accepted: ${output}"
  fi
}

assert_valid_yaml_rejected() {
  local label=$1 content=$2 fixture
  fixture="${test_workspace}/valid-but-unsupported.yaml"
  printf '%s' "$content" > "$fixture"
  yq '.' "$fixture" > /dev/null || test::fail "${label} fixture is not legal YAML"
  assert_rejected "$label" "$content"
}

assert_rejected "missing nodes" $'apiVersion: kind.x-k8s.io/v1alpha4\nkind: Cluster\n'
assert_rejected "duplicate nodes" $'nodes:\n  - role: control-plane\nnodes:\n  - role: worker\n'
assert_rejected "tab indentation" $'nodes:\n\t- role: control-plane\n'
assert_rejected "flow-style nodes" $'nodes: [{role: control-plane}]\n'
assert_rejected "aliased nodes" $'nodes: *topology\n'
assert_rejected "anchored node" $'nodes:\n  - &primary role: control-plane\n'
assert_rejected "aliased node" $'nodes:\n  - *primary\n'
assert_rejected "missing node role" $'nodes:\n  - labels:\n      atlas.io/node-pool: data\n'
assert_rejected "unknown node role" $'nodes:\n  - role: edge\n'
assert_rejected "misindented node" $'nodes:\n  - role: control-plane\n   - role: worker\n'
assert_rejected "nested node entry" $'nodes:\n  - role: control-plane\n    - role: worker\n'
assert_rejected "nested node role" $'nodes:\n  - role: control-plane\n    role: worker\n'
assert_valid_yaml_rejected "node image override" $'nodes:\n  - role: control-plane\n    image: kindest/node:latest\n'
assert_valid_yaml_rejected "quoted node image override" $'nodes:\n  - role: control-plane\n    "image": kindest/node:latest\n'
assert_valid_yaml_rejected "tagged node image override" $'nodes:\n  - role: control-plane\n    !!str image: kindest/node:latest\n'
assert_valid_yaml_rejected "explicit node image override" $'nodes:\n  - role: control-plane\n    ? image\n    : kindest/node:latest\n'
assert_rejected "unsupported node property" $'nodes:\n  - role: control-plane\n    extraFoo: value\n'
assert_rejected "flow-style node mapping" $'nodes:\n  - role: control-plane\n    labels: {atlas.io/node-pool: data}\n'
assert_rejected "node mapping alias" $'nodes:\n  - role: control-plane\n    labels: *labels\n'
assert_rejected "nested mapping alias" $'nodes:\n  - role: control-plane\n    labels:\n      atlas.io/node-pool: *label\n'
assert_rejected "nested flow-style mapping" $'nodes:\n  - role: control-plane\n    labels:\n      atlas.io/node-pool: {name: data}\n'
assert_rejected "no control-plane" $'nodes:\n  - role: worker\n'
assert_rejected "multiple control-planes" $'nodes:\n  - role: control-plane\n  - role: control-plane\n'
test::pass "non-canonical and unsupported Kind topology syntax fails closed"

ATLAS_ROOT_DIR=$test_workspace
readonly ATLAS_ROOT_DIR
cp "$valid_config" "${test_workspace}/kind.yaml"

readonly MOCK_CLUSTER=atlas-unit
readonly MOCK_IMAGE='kindest/node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
readonly MOCK_REPO='https://github.com/snkio027/atlas.git'
MOCK_MARKER_HASH=$(runtime::sha256 "${test_workspace}/kind.yaml")
MOCK_DOCKER_NAMES=$'node-zeta\nnode-alpha\nnode-mu\nnode-beta'
MOCK_DOCKER_NAMES_AFTER_FIRST=''
docker_ps_log="${test_workspace}/docker-ps.log"
: > "$docker_ps_log"
declare -A MOCK_DOCKER_DETAILS
MOCK_DOCKER_DETAILS["node-zeta"]="${MOCK_CLUSTER}|control-plane|${MOCK_IMAGE}|true"
MOCK_DOCKER_DETAILS["node-alpha"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|true"
MOCK_DOCKER_DETAILS["node-mu"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|true"
MOCK_DOCKER_DETAILS["node-beta"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|true"
MOCK_KUBERNETES_NODES=$'node-beta\nnode-zeta\nnode-alpha\nnode-mu'
MOCK_CONTROL_PLANES=node-zeta
declare -A MOCK_READY
MOCK_READY["node-zeta"]=True
MOCK_READY["node-alpha"]=True
MOCK_READY["node-mu"]=True
MOCK_READY["node-beta"]=True

config::get() {
  case "$1" in
    ATLAS_CLUSTER_NAME) printf '%s\n' "$MOCK_CLUSTER" ;;
    ATLAS_KIND_CONFIG) printf 'kind.yaml\n' ;;
    ATLAS_GIT_REPO_URL) printf '%s\n' "$MOCK_REPO" ;;
    *) return 1 ;;
  esac
}

config::version() {
  [[ $1 == KIND_NODE_IMAGE ]] || return 1
  printf '%s\n' "$MOCK_IMAGE"
}

docker() {
  case "$1" in
    ps)
      local calls
      calls=$(wc -l < "$docker_ps_log")
      printf 'PS\n' >> "$docker_ps_log"
      if [[ -n $MOCK_DOCKER_NAMES_AFTER_FIRST && $calls -ge 1 ]]; then
        printf '%s\n' "$MOCK_DOCKER_NAMES_AFTER_FIRST"
      else
        printf '%s\n' "$MOCK_DOCKER_NAMES"
      fi
      ;;
    inspect)
      local container=${!#}
      [[ -n ${MOCK_DOCKER_DETAILS[$container]+set} ]] || return 1
      printf '%s\n' "${MOCK_DOCKER_DETAILS[$container]}"
      ;;
    *) return 1 ;;
  esac
}

runtime::kubectl() {
  if [[ $1 == get && $2 == configmap && $3 == atlas-bootstrap-identity ]]; then
    printf '%s\t%s\n' "$MOCK_REPO" "$MOCK_MARKER_HASH"
    return 0
  fi
  if [[ $1 == get && $2 == nodes ]]; then
    local argument selector_present=false
    for argument in "$@"; do
      [[ $argument == --selector ]] && selector_present=true
    done
    if [[ $selector_present == true ]]; then
      printf '%s\n' "$MOCK_CONTROL_PLANES"
    else
      printf '%s\n' "$MOCK_KUBERNETES_NODES"
    fi
    return 0
  fi
  if [[ $1 == get && $2 == node && -n ${3:-} ]]; then
    [[ -n ${MOCK_READY[$3]+set} ]] || return 1
    printf '%s\n' "${MOCK_READY[$3]}"
    return 0
  fi
  return 1
}

cluster::_validate_nodes
listed_nodes=$(cluster::list_validated_kind_node_containers)
[[ $listed_nodes == "$MOCK_DOCKER_NAMES" ]] || test::fail "validated node enumeration changed or predicted container names"
cluster::_marker_matches || test::fail "exact Kind config identity did not match"
MOCK_MARKER_HASH=ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
if cluster::_marker_matches; then
  test::fail "drifted Kind config identity was accepted"
fi
MOCK_MARKER_HASH=$(runtime::sha256 "${test_workspace}/kind.yaml")
test::pass "runtime validation uses exact Docker and Kubernetes node sets"

MOCK_DOCKER_DETAILS["node-intruder"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|true"
MOCK_DOCKER_NAMES_AFTER_FIRST="${MOCK_DOCKER_NAMES}"$'\nnode-intruder'
: > "$docker_ps_log"
listed_nodes=$(cluster::list_validated_kind_node_containers)
[[ $listed_nodes == "$MOCK_DOCKER_NAMES" ]] || test::fail "Registry-facing inventory was re-enumerated after validation"
[[ $(wc -l < "$docker_ps_log") -eq 1 ]] || test::fail "validated inventory performed more than one Docker enumeration"
MOCK_DOCKER_NAMES_AFTER_FIRST=''
unset 'MOCK_DOCKER_DETAILS[node-intruder]'
test::pass "Registry-facing node names come from the exact validated inventory snapshot"

assert_runtime_rejected() {
  local label=$1
  if cluster::_validate_nodes > /dev/null 2>&1; then
    test::fail "${label} was accepted"
  fi
}

original_control_plane_details=${MOCK_DOCKER_DETAILS["node-zeta"]}
original_worker_details=${MOCK_DOCKER_DETAILS["node-beta"]}
MOCK_DOCKER_DETAILS["node-beta"]="${MOCK_CLUSTER}|external-load-balancer|envoy@sha256:bbbb|true"
assert_runtime_rejected "an implicit Kind external load balancer"
MOCK_DOCKER_DETAILS["node-beta"]="${MOCK_CLUSTER}|unknown|${MOCK_IMAGE}|true"
assert_runtime_rejected "an unknown Kind container role"
MOCK_DOCKER_DETAILS["node-beta"]="foreign|worker|${MOCK_IMAGE}|true"
assert_runtime_rejected "a drifted Kind cluster label"
MOCK_DOCKER_DETAILS["node-beta"]="${MOCK_CLUSTER}|worker|kindest/node@sha256:bbbb|true"
assert_runtime_rejected "a drifted Kind node image"
MOCK_DOCKER_DETAILS["node-beta"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|false"
assert_runtime_rejected "a stopped Kind node"
MOCK_DOCKER_DETAILS["node-beta"]=$original_worker_details

MOCK_DOCKER_DETAILS["node-zeta"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|true"
MOCK_DOCKER_DETAILS["node-beta"]="${MOCK_CLUSTER}|control-plane|${MOCK_IMAGE}|true"
assert_runtime_rejected "a worker and control-plane role exchange"
MOCK_DOCKER_DETAILS["node-zeta"]=$original_control_plane_details
MOCK_DOCKER_DETAILS["node-beta"]=$original_worker_details

MOCK_DOCKER_NAMES=$'node-zeta\nnode-alpha\nnode-mu'
assert_runtime_rejected "a missing Docker worker"
MOCK_DOCKER_NAMES=$'node-zeta\nnode-alpha\nnode-mu\nnode-beta'

MOCK_DOCKER_DETAILS["node-extra"]="${MOCK_CLUSTER}|worker|${MOCK_IMAGE}|true"
MOCK_READY["node-extra"]=True
MOCK_DOCKER_NAMES+=$'\nnode-extra'
MOCK_KUBERNETES_NODES+=$'\nnode-extra'
assert_runtime_rejected "an extra Docker worker"
MOCK_DOCKER_NAMES=$'node-zeta\nnode-alpha\nnode-mu\nnode-beta'
MOCK_KUBERNETES_NODES=$'node-beta\nnode-zeta\nnode-alpha\nnode-mu'
unset 'MOCK_DOCKER_DETAILS[node-extra]' 'MOCK_READY[node-extra]'

MOCK_KUBERNETES_NODES=$'node-zeta\nnode-alpha\nnode-mu\nnode-foreign'
assert_runtime_rejected "a mismatched Docker and Kubernetes node-name set"
MOCK_KUBERNETES_NODES=$'node-beta\nnode-zeta\nnode-alpha\nnode-mu'

MOCK_CONTROL_PLANES=node-alpha
assert_runtime_rejected "a Kubernetes control-plane label on a worker"
MOCK_CONTROL_PLANES=node-zeta

MOCK_READY["node-mu"]=False
assert_runtime_rejected "a NotReady Kubernetes Node"
MOCK_READY["node-mu"]=True
test::pass "role, label, image, state, membership, and readiness drift fails closed"

mutation_log="${test_workspace}/mutation.log"
printf 'nodes:\n  - role: control-plane\n    image: forbidden\n' > "${test_workspace}/kind.yaml"
runtime::docker_image_present() {
  printf 'IMAGE_CHECK\n' >> "$mutation_log"
}
runtime::kind_cluster_exists() {
  printf 'CLUSTER_DISCOVERY\n' >> "$mutation_log"
  return 1
}
kind() {
  printf 'KIND_CREATE\n' >> "$mutation_log"
}
if cluster::ensure_kind > /dev/null 2>&1; then
  test::fail "invalid topology reached a successful Kind ensure"
fi
[[ ! -e $mutation_log ]] || test::fail "invalid topology reached image inspection or Kind mutation"
test::pass "topology validation precedes Kind discovery and creation"
