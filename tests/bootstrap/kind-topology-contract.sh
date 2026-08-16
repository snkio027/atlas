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

readonly kind_config=clusters/kind/local-orbstack.yaml

parsed_roles=$(cluster::_parse_kind_node_roles "$kind_config")
[[ $parsed_roles == $'control-plane\nworker\nworker\nworker' ]] || test::fail "Kind topology is not one control-plane plus three workers"
[[ $(yq '.nodes | length' "$kind_config") -eq 4 ]] || test::fail "Kind topology does not contain exactly four nodes"

topology=$(yq -r '.nodes[] | .role + ":" + .labels."atlas.io/node-pool"' "$kind_config")
expected_topology=$'control-plane:control\nworker:gateway\nworker:compute\nworker:data'
[[ $topology == "$expected_topology" ]] || test::fail "Kind roles and canonical node pools differ from the four-node contract"

unique_pools=$(yq -r '[.nodes[].labels."atlas.io/node-pool"] | unique | length' "$kind_config")
[[ $unique_pools -eq 4 ]] || test::fail "Kind node-pool labels are absent or duplicated"
if rg -n 'node-role\.local/' "$kind_config"; then
  test::fail "legacy node-role.local labels or taints remain in the Kind topology"
fi
if rg -n 'topology\.kubernetes\.io/(zone|region)' "$kind_config"; then
  test::fail "single-host Kind topology declares a false failure domain"
fi
[[ $(yq '[.nodes[] | select(has("image"))] | length' "$kind_config") -eq 0 ]] || test::fail "Kind topology declares a node-level image"
test::pass "four Kind nodes have unique canonical roles and node pools"

data_node=$(yq '.nodes[] | select(.labels."atlas.io/node-pool" == "data")' "$kind_config")
[[ $(yq '.kubeadmConfigPatches | length' <<< "$data_node") -eq 1 ]] || test::fail "data node must have exactly one kubeadm patch"

kubeadm_patch=$(yq -r '.kubeadmConfigPatches[0]' <<< "$data_node")
[[ $(yq -r '.apiVersion' <<< "$kubeadm_patch") == kubeadm.k8s.io/v1beta4 ]] || test::fail "data node does not use kubeadm v1beta4"
[[ $(yq -r '.kind' <<< "$kubeadm_patch") == JoinConfiguration ]] || test::fail "data node patch is not a JoinConfiguration"
[[ $(yq '.nodeRegistration.taints | length' <<< "$kubeadm_patch") -eq 1 ]] || test::fail "data node must have exactly one explicit taint"
[[ $(yq -r '.nodeRegistration.taints[0].key' <<< "$kubeadm_patch") == atlas.io/node-pool ]] || test::fail "data taint key is not canonical"
[[ $(yq -r '.nodeRegistration.taints[0].value' <<< "$kubeadm_patch") == data ]] || test::fail "data taint value is incorrect"
[[ $(yq -r '.nodeRegistration.taints[0].effect' <<< "$kubeadm_patch") == NoSchedule ]] || test::fail "data taint effect is not NoSchedule"

non_data_patches=$(yq '[.nodes[] | select(.labels."atlas.io/node-pool" != "data") | select(has("kubeadmConfigPatches"))] | length' "$kind_config")
[[ $non_data_patches -eq 0 ]] || test::fail "a non-data node has a kubeadm patch"
test::pass "data scheduling requires the v1beta4 JoinConfiguration taint"

readonly bootstrap_readme=bootstrap/README.md
grep -Fq 'does not support in-place expansion' "$bootstrap_readme" || test::fail "README omits the Kind replacement boundary"
grep -Fq "report \`DRIFTED\`" "$bootstrap_readme" || test::fail "README omits post-merge drift semantics"
grep -Fq 'will not add nodes, repair topology, or delete' "$bootstrap_readme" || test::fail "README overstates normal Bootstrap migration authority"
grep -Fq 'Human Judgment Gate' "$bootstrap_readme" || test::fail "README omits the destructive migration gate"
test::assert_not_found 'data-zone-1|topology\.kubernetes\.io/(zone|region)' "$bootstrap_readme"
test::pass "single-node replacement remains an explicit Human-gated migration"
