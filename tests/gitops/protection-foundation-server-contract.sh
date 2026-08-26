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
chart_path=$(locked_value ARGOCD_CHART_PATH)
argocd_values=gitops/platform/management/argocd-self/values.yaml
argocd_hardening_values="$definitions/argo-hardening/argocd-values-hardening.yaml"
enforced="$test_workspace/enforced.yaml"
policies="$test_workspace/policies.yaml"
bindings="$test_workspace/bindings.yaml"
rbac="$test_workspace/rbac.yaml"
negative_policy="$test_workspace/negative-policy.yaml"
kubectl kustomize "$definitions/admission/overlays/enforced" > "$enforced"
{
  kubectl kustomize "$definitions/rbac/escape"
  printf '%s\n' '---'
  kubectl kustomize "$definitions/rbac/session"
} > "$rbac"
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

bootstrap=atlas:bootstrap:00000000-0000-0000-0000-000000000000:g1
bootstrap_rbac=tests/gitops/fixtures/phase1a-server/bootstrap-rbac.yaml
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$bootstrap_rbac" > /dev/null
legacy_identity=tests/gitops/fixtures/phase1a-server/identity-v2-legacy-keys.yaml

wait_for_evidence_enforcement() {
  local attempt diagnostic
  for ((attempt = 1; attempt <= 30; attempt++)); do
    diagnostic="$test_workspace/evidence-enforcement-$(printf '%02d' "$attempt").stderr"
    if kubectl --kubeconfig "$kubeconfig" --as="$bootstrap" --as-group=system:authenticated \
      create --dry-run=server --validate=strict -f "$legacy_identity" \
      > "$test_workspace/evidence-enforcement.stdout" 2> "$diagnostic"; then
      sleep 1
      continue
    fi
    grep -Fq 'Bootstrap Identity v2 projection is invalid' "$diagnostic" || {
      cat "$diagnostic" >&2
      test::fail "Evidence protection propagation probe failed unexpectedly"
    }
    return 0
  done
  test::fail "Evidence protection did not reach the API Server admission path"
}

wait_for_evidence_enforcement
if kubectl --kubeconfig "$kubeconfig" --as="$bootstrap" --as-group=system:authenticated \
  create --validate=strict -f "$legacy_identity" \
  > "$test_workspace/legacy-identity.stdout" 2> "$test_workspace/legacy-identity.stderr"; then
  test::fail "Identity v2 accepted receipt-unaware legacy keys"
fi
grep -Fq 'Bootstrap Identity v2 projection is invalid' "$test_workspace/legacy-identity.stderr" ||
  test::fail "legacy Identity keys did not reach exact v2 projection validation"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-bootstrap-identity \
  -n kube-system --ignore-not-found -o name) ]] || test::fail "rejected legacy Identity persisted"

session=0123456789abcdef0123456789abcdef
missing_fence=tests/gitops/fixtures/phase1a-server/missing-fence-rolebinding.yaml
authorizer=atlas:session-authz:00000000-0000-0000-0000-000000000000:g1
parameter_missing_diagnostic="no params found for policy binding with \`Deny\` parameterNotFoundAction"

wait_for_permission_enforcement() {
  local attempt diagnostic
  for ((attempt = 1; attempt <= 30; attempt++)); do
    diagnostic="$test_workspace/permission-enforcement-$(printf '%02d' "$attempt").stderr"
    if kubectl --kubeconfig "$kubeconfig" --as="$authorizer" --as-group=system:authenticated \
      create --dry-run=server --validate=strict -f "$missing_fence" \
      > "$test_workspace/permission-enforcement.stdout" 2> "$diagnostic"; then
      sleep 1
      continue
    fi
    grep -Fq "$parameter_missing_diagnostic" "$diagnostic" || {
      cat "$diagnostic" >&2
      test::fail "Permission authorization propagation probe failed unexpectedly"
    }
    return 0
  done
  test::fail "Permission authorization did not reach the API Server admission path"
}

wait_for_permission_enforcement
if kubectl --kubeconfig "$kubeconfig" --as="$authorizer" --as-group=system:authenticated \
  create --validate=strict -f "$missing_fence" \
  > "$test_workspace/fence.stdout" 2> "$test_workspace/fence.stderr"; then
  test::fail "Session Authorizer created temporary RBAC without a Fence"
fi
grep -Fq "$parameter_missing_diagnostic" "$test_workspace/fence.stderr" ||
  test::fail "missing-Fence fixture did not fail through parameterNotFoundAction"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get rolebinding \
  "atlas-bg-kube-system-${session}" -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "missing-Fence negative fixture persisted a RoleBinding"

bootstrap_evidence=tests/gitops/fixtures/phase1a-server/bootstrap-evidence.yaml
kubectl --kubeconfig "$kubeconfig" --as="$bootstrap" --as-group=system:authenticated \
  create --validate=strict -f "$bootstrap_evidence" > /dev/null
for bootstrap_object in \
  atlas-bootstrap-identity \
  atlas-bootstrap-adoption-receipt \
  atlas-bootstrap-operation-fence; do
  [[ $(kubectl --kubeconfig "$kubeconfig" get configmap "$bootstrap_object" \
    -n kube-system -o name) == "configmap/${bootstrap_object}" ]] ||
    test::fail "Bootstrap could not create approved evidence: ${bootstrap_object}"
done
fence_update="$test_workspace/fence-update.yaml"
kubectl --kubeconfig "$kubeconfig" get configmap atlas-bootstrap-operation-fence \
  -n kube-system -o yaml | yq '.metadata.labels."atlas.io/forbidden-update" = "true"' > "$fence_update"
if kubectl --kubeconfig "$kubeconfig" --as="$bootstrap" --as-group=system:authenticated \
  replace --validate=strict -f "$fence_update" \
  > "$test_workspace/fence-update.stdout" 2> "$test_workspace/fence-update.stderr"; then
  test::fail "Bootstrap updated the create-only Operation Fence"
fi
if ! grep -Fq 'Fence lifecycle is limited to the canonical create-only object' \
  "$test_workspace/fence-update.stderr" &&
  ! grep -Fq 'Adoption evidence mutation requires the exact reviewed authority' \
    "$test_workspace/fence-update.stderr"; then
  test::fail "Bootstrap Fence update did not reach a canonical protection control"
fi
[[ $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-bootstrap-operation-fence \
  -n kube-system -o json | yq '.metadata.labels | has("atlas.io/forbidden-update")') == false ]] ||
  test::fail "rejected Bootstrap Fence update changed the live object"

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

argocd_chart="$test_workspace/argocd-chart.yaml"
argocd_rbac_cm="$test_workspace/argocd-rbac-cm.yaml"
helm template atlas-argocd "$chart_path" --namespace argocd --include-crds \
  --values "$argocd_values" --values "$argocd_hardening_values" > "$argocd_chart"
NAME=argocd-rbac-cm yq ea \
  'select(.kind == "ConfigMap" and .metadata.name == strenv(NAME))' \
  "$argocd_chart" > "$argocd_rbac_cm"
[[ $(yq ea '[.] | length' "$argocd_rbac_cm") -eq 1 &&
$(yq '.metadata | has("annotations")' "$argocd_rbac_cm") == false &&
$(yq '.metadata | has("finalizers")' "$argocd_rbac_cm") == false &&
$(yq '.metadata | has("ownerReferences")' "$argocd_rbac_cm") == false ]] ||
  test::fail "Chart argocd-rbac-cm fixture does not exercise absent optional metadata"
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$argocd_rbac_cm" > /dev/null
guard="$test_workspace/argocd-rbac-guarded.yaml"
kubectl --kubeconfig "$kubeconfig" get configmap argocd-rbac-cm -n argocd -o yaml |
  GUARD_VALUE='p, role:atlas-phase1a-fixture, *, *, *, deny' yq '
    .data."policy.atlas-recovery-freeze.csv" = strenv(GUARD_VALUE)
  ' > "$guard"

wait_for_guard_enforcement() {
  local attempt diagnostic
  for ((attempt = 1; attempt <= 30; attempt++)); do
    diagnostic="$test_workspace/guard-enforcement-$(printf '%02d' "$attempt").stderr"
    if kubectl --kubeconfig "$kubeconfig" replace --dry-run=server --validate=strict -f "$guard" \
      > "$test_workspace/guard-enforcement.stdout" 2> "$diagnostic"; then
      sleep 1
      continue
    fi
    grep -Fq 'Argo recovery guard mutation requires the exact Recovery Operator' \
      "$diagnostic" || {
      cat "$diagnostic" >&2
      test::fail "Guard authorization propagation probe failed unexpectedly"
    }
    return 0
  done
  test::fail "Guard authorization did not reach the API Server admission path"
}

wait_for_guard_enforcement
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

recovery=atlas:break-glass:00000000-0000-0000-0000-000000000000:g1
guard_rbac=tests/gitops/fixtures/phase1a-server/recovery-guard-rbac.yaml

guard_permission_state() {
  local diagnostic=$1 decision status
  : > "$diagnostic"
  if decision=$(kubectl --kubeconfig "$kubeconfig" --as="$recovery" \
    --as-group=system:authenticated auth can-i update \
    configmaps/argocd-rbac-cm -n argocd 2> "$diagnostic"); then
    status=0
  else
    status=$?
  fi
  [[ ! -s $diagnostic ]] || {
    cat "$diagnostic" >&2
    return 2
  }
  case "${status}:${decision}" in
    0:yes) printf 'READY\n' ;;
    1:no) printf 'PENDING\n' ;;
    *) return 2 ;;
  esac
}

pre_binding_diagnostic="$test_workspace/guard-permission-pre-binding.stderr"
pre_binding_state=$(guard_permission_state "$pre_binding_diagnostic") ||
  test::fail "pre-Binding Recovery guard permission probe was malformed"
[[ $pre_binding_state == PENDING ]] ||
  test::fail "Recovery guard permission existed before its CI-only RoleBinding"

kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$guard_rbac" > /dev/null

wait_for_guard_permission() {
  local attempt diagnostic state
  for ((attempt = 1; attempt <= 30; attempt++)); do
    diagnostic="$test_workspace/guard-permission-$(printf '%02d' "$attempt").stderr"
    state=$(guard_permission_state "$diagnostic") ||
      test::fail "Recovery guard permission probe was malformed"
    case "$state" in
      READY) return 0 ;;
      PENDING) sleep 1 ;;
      *) test::fail "Recovery guard permission probe returned an unknown state" ;;
    esac
  done
  test::fail "Recovery guard permission did not converge"
}

wait_for_guard_permission
kubectl --kubeconfig "$kubeconfig" --as="$recovery" --as-group=system:authenticated \
  replace --validate=strict -f "$guard" > /dev/null
[[ $(kubectl --kubeconfig "$kubeconfig" get configmap argocd-rbac-cm -n argocd -o json |
  yq -r '.data."policy.atlas-recovery-freeze.csv" // ""') == 'p, role:atlas-phase1a-fixture, *, *, *, deny' ]] ||
  test::fail "exact Recovery principal did not add the canonical Guard"

unguarded="$test_workspace/argocd-rbac-unguarded.yaml"
kubectl --kubeconfig "$kubeconfig" get configmap argocd-rbac-cm -n argocd -o yaml |
  yq 'del(.data."policy.atlas-recovery-freeze.csv")' > "$unguarded"
kubectl --kubeconfig "$kubeconfig" --as="$recovery" --as-group=system:authenticated \
  replace --validate=strict -f "$unguarded" > /dev/null
[[ $(kubectl --kubeconfig "$kubeconfig" get configmap argocd-rbac-cm -n argocd -o json |
  yq '.data | has("policy.atlas-recovery-freeze.csv")') == false ]] ||
  test::fail "exact Recovery principal did not remove the canonical Guard"

test::pass "Phase 1A VAPs compile and fail closed on locked Kubernetes"
