#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
# shellcheck source=tests/recovery/vap-server-kind-inventory.sh
source "$(dirname "${BASH_SOURCE[0]}")/vap-server-kind-inventory.sh"
cd "$ATLAS_TEST_ROOT"

[[ ${ATLAS_CI_KIND_VAP:-} == 1 ]] ||
  test::fail "server-side VAP compilation requires the explicit CI gate"
[[ $(uname -s) == Linux ]] ||
  test::fail "server-side VAP compilation is confined to the Linux CI runner"

locked_value() {
  local key=$1
  awk -F= -v key="$key" '
    $1 == key { count += 1; value = $2 }
    END {
      if (count == 1) print value
      else exit 1
    }
  ' versions.lock
}

kind_version=$(locked_value KIND_VERSION)
kubernetes_version=$(locked_value KUBERNETES_VERSION)
node_image=$(locked_value KIND_NODE_IMAGE)
[[ $kind_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || test::fail "locked Kind version is invalid"
[[ $kubernetes_version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  test::fail "locked Kubernetes version is invalid"
[[ $node_image =~ @sha256:[0-9a-f]{64}$ ]] || test::fail "locked Kind node image is not digest pinned"

actual_kind_version=$(kind version)
[[ $actual_kind_version == "kind v${kind_version} "* ]] ||
  test::fail "CI Kind version differs from versions.lock: ${actual_kind_version}"

test_workspace=$(mktemp -d "${RUNNER_TEMP:-/tmp}/atlas-vap-server.XXXXXX")
cluster_name="atlas-vap-ci-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-1}"
kubeconfig="${test_workspace}/${cluster_name}.kubeconfig"
created=false

ci_docker() {
  env \
    -u DOCKER_API_VERSION \
    -u DOCKER_CERT_PATH \
    -u DOCKER_CONFIG \
    -u DOCKER_DEFAULT_PLATFORM \
    -u DOCKER_HOST \
    -u DOCKER_TLS_VERIFY \
    DOCKER_CONTEXT=default \
    docker "$@"
}

ci_kind() {
  env \
    -u DOCKER_API_VERSION \
    -u DOCKER_CERT_PATH \
    -u DOCKER_CONFIG \
    -u DOCKER_DEFAULT_PLATFORM \
    -u DOCKER_HOST \
    -u DOCKER_TLS_VERIFY \
    -u KIND_CLUSTER_NAME \
    -u KIND_DNS_SEARCH \
    -u KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER \
    -u KIND_EXPERIMENTAL_DOCKER_CONCURRENT_LOADS \
    -u KIND_EXPERIMENTAL_DOCKER_CONCURRENT_PULLS \
    -u KIND_EXPERIMENTAL_DOCKER_DNSSEARCH \
    -u KIND_EXPERIMENTAL_DOCKER_NETWORK \
    -u KIND_EXPERIMENTAL_PODMAN_NETWORK \
    -u KIND_EXPERIMENTAL_PROVIDER \
    DOCKER_CONTEXT=default \
    KIND_EXPERIMENTAL_PROVIDER=docker \
    KUBECONFIG="$kubeconfig" \
    kind "$@"
}

cleanup() {
  local status=$? cleanup_status=0
  trap - EXIT INT TERM
  if [[ $created == true ]]; then
    ci_kind delete cluster --name "$cluster_name" || cleanup_status=$?
    if ! vap_server_inventory::deleted_target_absent ci_kind "$cluster_name"; then
      cleanup_status=1
    fi
  fi
  rm -rf "$test_workspace"
  ((status == 0 && cleanup_status != 0)) && status=$cleanup_status
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

[[ ${#cluster_name} -le 63 && $cluster_name =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  test::fail "generated CI Kind cluster name is invalid"
vap_server_inventory::creation_target_absent ci_kind "$cluster_name" ||
  test::fail "CI Kind creation target is not safely absent: ${cluster_name}"

created=true
ci_kind create cluster \
  --name "$cluster_name" \
  --image "$node_image" \
  --kubeconfig "$kubeconfig" \
  --wait 120s

actual_node_image=$(ci_docker inspect "${cluster_name}-control-plane" --format '{{.Config.Image}}')
[[ $actual_node_image == "$node_image" ]] ||
  test::fail "CI Kind node image differs from versions.lock"
server_version=$(kubectl --kubeconfig "$kubeconfig" version -o json | yq -r '.serverVersion.gitVersion')
[[ $server_version == "v${kubernetes_version}" ]] ||
  test::fail "CI Kubernetes server differs from versions.lock: ${server_version}"

recovery_operator=atlas:break-glass:12345678-1234-1234-1234-123456789abc:g3
session_authorizer=atlas:session-authz:12345678-1234-1234-1234-123456789abc:g2
admission_bundle="${test_workspace}/admission.yaml"
session_bundle="${test_workspace}/session.yaml"
policies="${test_workspace}/policies.yaml"
negative_policy="${test_workspace}/negative-policy.yaml"

./bootstrap/recovery/atlas-recovery phase0 admission-canary-manifests \
  --recovery-operator "$recovery_operator" > "$admission_bundle"
./bootstrap/recovery/atlas-recovery phase0 session-authorization-canary-manifests \
  --recovery-operator "$recovery_operator" \
  --session-authorizer "$session_authorizer" > "$session_bundle"
yq ea 'select(.kind == "ValidatingAdmissionPolicy")' \
  "$admission_bundle" "$session_bundle" > "$policies"
[[ $(yq ea '[.] | length' "$policies") -eq 5 ]] ||
  test::fail "server-side contract did not render exactly five VAPs"

negative_name=atlas-bootstrap-recovery-binding-shape-authorization-negative
RESOURCE_NAME=atlas-bootstrap-recovery-binding-shape-authorization-canary \
  NEGATIVE_NAME=$negative_name yq ea '
    select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME)) |
    .metadata.name = strenv(NEGATIVE_NAME) |
    .spec.matchConditions[0].expression |= sub("\\)$"; "")
  ' "$policies" > "$negative_policy"

if kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$negative_policy" \
  > "${test_workspace}/negative.stdout" 2> "${test_workspace}/negative.stderr"; then
  test::fail "Kubernetes API Server accepted the intentionally malformed CEL control"
fi
grep -Fq 'compilation failed' "${test_workspace}/negative.stderr" ||
  test::fail "negative CEL control was not rejected by server-side compilation"
grep -Fq "missing ')'" "${test_workspace}/negative.stderr" ||
  test::fail "negative CEL control did not exercise the missing-parenthesis failure"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicy "$negative_name" \
  --ignore-not-found -o name) ]] || test::fail "negative CEL control persisted unexpectedly"

kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$policies" > /dev/null

wait_for_typecheck() {
  local name=$1 attempt object generation observed warnings
  for ((attempt = 0; attempt < 60; attempt++)); do
    object=$(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicy "$name" -o json)
    generation=$(yq -r '.metadata.generation' <<< "$object")
    observed=$(yq -r '.status.observedGeneration // 0' <<< "$object")
    if [[ $generation == "$observed" && $(yq '.status | has("typeChecking")' <<< "$object") == true ]]; then
      warnings=$(yq -r '.status.typeChecking.expressionWarnings | length' <<< "$object")
      ((warnings == 0)) || test::fail "VAP type checking reported warnings: ${name}"
      return 0
    fi
    sleep 1
  done
  test::fail "VAP type checking did not complete: ${name}"
}

for policy_name in \
  atlas-bootstrap-admission-escape-canary \
  atlas-bootstrap-recovery-fence-authorization-canary \
  atlas-bootstrap-recovery-binding-shape-authorization-canary \
  atlas-bootstrap-recovery-permission-authorization-canary \
  atlas-bootstrap-recovery-guard-authorization-canary; do
  wait_for_typecheck "$policy_name"
done

live_policy_count=$(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicies \
  -l app.kubernetes.io/part-of=atlas-recovery -o json | yq '.items | length')
((live_policy_count == 5)) || test::fail "locked API Server does not contain exactly five tested VAPs"

test::pass "all five Phase-0 VAPs compile on the locked Kubernetes API Server"
