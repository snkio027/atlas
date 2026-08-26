#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

[[ ${ATLAS_CI_PHASE1A_VAP:-} == 1 ]] ||
  test::fail "Phase 1A server contract requires the explicit CI gate"
[[ $(uname -s) == Linux ]] ||
  test::fail "Phase 1A server contract is confined to the Linux CI runner"

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
[[ $node_image =~ @sha256:[0-9a-f]{64}$ ]] ||
  test::fail "locked Kind node image is not digest pinned"
actual_kind_version=$(kind version)
[[ $actual_kind_version == "kind v${kind_version} "* ]] ||
  test::fail "CI Kind version differs from versions.lock: ${actual_kind_version}"

test_workspace=$(mktemp -d "${RUNNER_TEMP:-/tmp}/atlas-phase1a-server.XXXXXX")
cluster_name="atlas-phase1a-ci-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-1}"
kubeconfig="$test_workspace/${cluster_name}.kubeconfig"
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

kind_inventory() {
  local output
  if ! output=$(ci_kind get clusters --quiet); then
    return 1
  fi
  printf '%s\n' "$output"
}

require_cluster_absent() {
  local stage=$1 inventory
  if ! inventory=$(kind_inventory); then
    test::fail "unable to enumerate Kind clusters during ${stage}"
  fi
  ! grep -Fqx "$cluster_name" <<< "$inventory" ||
    test::fail "CI cluster is present during ${stage}: ${cluster_name}"
}

cleanup() {
  local status=$? cleanup_status=0
  trap - EXIT INT TERM
  if [[ $created == true ]]; then
    ci_kind delete cluster --name "$cluster_name" || cleanup_status=$?
    require_cluster_absent post-delete || cleanup_status=1
  fi
  rm -rf "$test_workspace"
  ((status == 0 && cleanup_status != 0)) && status=$cleanup_status
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

[[ ${#cluster_name} -le 63 && $cluster_name =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] ||
  test::fail "generated CI Kind cluster name is invalid"
require_cluster_absent creation-preflight

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

definitions=gitops/platform/management/protection-foundation/definitions
enforced="$test_workspace/enforced.yaml"
policies="$test_workspace/policies.yaml"
bindings="$test_workspace/bindings.yaml"
rbac="$test_workspace/rbac.yaml"
negative_policy="$test_workspace/negative-policy.yaml"
kubectl kustomize "$definitions/admission/overlays/enforced" > "$enforced"
kubectl kustomize "$definitions/rbac" > "$rbac"
yq ea 'select(.kind == "ValidatingAdmissionPolicy")' "$enforced" > "$policies"
yq ea 'select(.kind == "ValidatingAdmissionPolicyBinding")' "$enforced" > "$bindings"
[[ $(yq ea '[.] | length' "$policies") -eq 5 &&
$(yq ea '[.] | length' "$bindings") -eq 5 ]] ||
  test::fail "server contract did not render exactly five Policy/Binding pairs"

negative_name=atlas-bootstrap-recovery-binding-shape-authorization-negative
RESOURCE_NAME=atlas-bootstrap-recovery-binding-shape-authorization \
  NEGATIVE_NAME=$negative_name yq ea '
    select(.kind == "ValidatingAdmissionPolicy" and .metadata.name == strenv(RESOURCE_NAME)) |
    .metadata.name = strenv(NEGATIVE_NAME) |
    .spec.matchConditions[0].expression |= sub("\\)$"; "")
  ' "$policies" > "$negative_policy"
if kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$negative_policy" \
  > "$test_workspace/negative.stdout" 2> "$test_workspace/negative.stderr"; then
  test::fail "API Server accepted intentionally malformed Phase 1A CEL"
fi
grep -Fq 'compilation failed' "$test_workspace/negative.stderr" ||
  test::fail "negative CEL control did not reach server-side compilation"
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

for name in \
  atlas-bootstrap-evidence-protection \
  atlas-bootstrap-recovery-fence-authorization \
  atlas-bootstrap-recovery-binding-shape-authorization \
  atlas-bootstrap-recovery-permission-authorization \
  atlas-bootstrap-recovery-guard-authorization; do
  wait_for_typecheck "$name"
done

kubectl --kubeconfig "$kubeconfig" create namespace argocd > /dev/null
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$bindings" > /dev/null
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$rbac" > /dev/null
[[ $(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicies \
  -l app.kubernetes.io/part-of=atlas-recovery -o json | yq '.items | length') -eq 5 ]] ||
  test::fail "API Server does not contain exactly five Phase 1A VAPs"

signal="$definitions/signal/adoption-signal.yaml"
if kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$signal" \
  > "$test_workspace/signal.stdout" 2> "$test_workspace/signal.stderr"; then
  test::fail "ordinary cluster administrator bypassed evidence protection"
fi
grep -Fq 'Adoption evidence mutation requires the exact reviewed authority' \
  "$test_workspace/signal.stderr" ||
  test::fail "Signal negative fixture did not reach evidence protection"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-bootstrap-adoption-signal \
  -n argocd --ignore-not-found -o name) ]] || test::fail "rejected Signal persisted unexpectedly"

kubectl --kubeconfig "$kubeconfig" create configmap argocd-rbac-cm -n argocd \
  --from-literal=policy.default=role:atlas-authenticated-readonly > /dev/null
guard="$test_workspace/argocd-rbac-guarded.yaml"
kubectl --kubeconfig "$kubeconfig" get configmap argocd-rbac-cm -n argocd -o yaml |
  GUARD_VALUE='p, role:atlas-phase1a-fixture, *, *, *, deny' yq '
    .data."policy.atlas-recovery-freeze.csv" = strenv(GUARD_VALUE)
  ' > "$guard"
if kubectl --kubeconfig "$kubeconfig" replace --validate=strict -f "$guard" \
  > "$test_workspace/guard.stdout" 2> "$test_workspace/guard.stderr"; then
  test::fail "ordinary cluster administrator bypassed Guard authorization"
fi
grep -Fq 'Argo recovery guard mutation requires the exact Recovery Operator' \
  "$test_workspace/guard.stderr" ||
  test::fail "Guard negative fixture did not reach Guard authorization"
[[ $(kubectl --kubeconfig "$kubeconfig" get configmap argocd-rbac-cm -n argocd -o json |
  yq '.data | has("policy.atlas-recovery-freeze.csv")') == false ]] ||
  test::fail "rejected recovery guard mutation changed the live ConfigMap"

session=0123456789abcdef0123456789abcdef
missing_fence=tests/gitops/fixtures/phase1a-server/missing-fence-rolebinding.yaml
authorizer=atlas:session-authz:00000000-0000-0000-0000-000000000000:g1
if kubectl --kubeconfig "$kubeconfig" --as="$authorizer" --as-group=system:authenticated \
  create --validate=strict -f "$missing_fence" \
  > "$test_workspace/fence.stdout" 2> "$test_workspace/fence.stderr"; then
  test::fail "Session Authorizer created temporary RBAC without a Fence"
fi
grep -Fq 'parameterNotFoundAction' "$test_workspace/fence.stderr" ||
  test::fail "missing-Fence fixture did not fail through parameterNotFoundAction"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get rolebinding \
  "atlas-bg-kube-system-${session}" -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "missing-Fence negative fixture persisted a RoleBinding"

test::pass "Phase 1A VAPs compile and fail closed on locked Kubernetes"
