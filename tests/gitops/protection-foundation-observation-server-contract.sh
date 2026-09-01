#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

[[ ${ATLAS_CI_PHASE1B_OBSERVING:-} == 1 ]] ||
  test::fail "Phase 1B observing server contract requires the explicit CI gate"
[[ $(uname -s) == Linux ]] ||
  test::fail "Phase 1B observing server contract is confined to the Linux CI runner"

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

test_workspace=$(mktemp -d "${RUNNER_TEMP:-/tmp}/atlas-phase1b-observing.XXXXXX")
cluster_name="atlas-phase1b-observing-${GITHUB_RUN_ID:-$$}-${GITHUB_RUN_ATTEMPT:-1}"
kubeconfig="$test_workspace/${cluster_name}.kubeconfig"
audit_directory="$test_workspace/audit"
kind_config="$test_workspace/kind.yaml"
audit_policy="$ATLAS_TEST_ROOT/clusters/kind/recovery-audit-policy.yaml"
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
[[ $audit_policy =~ ^/[A-Za-z0-9._/-]+$ && $audit_directory =~ ^/[A-Za-z0-9._/-]+$ ]] ||
  test::fail "CI audit paths are not safe YAML scalars"
[[ -f $audit_policy && ! -L $audit_policy ]] || test::fail "repository audit policy is missing or unsafe"
mkdir -m 0700 "$audit_directory"

activation=gitops/platform/management/protection-foundation/activation/personal-local-observing
candidate="$test_workspace/candidate.yaml"
policies="$test_workspace/policies.yaml"
bindings="$test_workspace/bindings.yaml"
kubectl kustomize "$activation/argocd-self-base-overlay" > "$candidate"
yq ea 'select(.kind == "ValidatingAdmissionPolicy")' "$candidate" > "$policies"
yq ea 'select(.kind == "ValidatingAdmissionPolicyBinding")' "$candidate" > "$bindings"
[[ $(yq ea '[.] | length' "$policies") -eq 5 &&
$(yq ea '[.] | length' "$bindings") -eq 5 ]] ||
  test::fail "candidate did not render exactly five target-bound Policy/Binding pairs"
[[ $(yq ea '[select(.spec.failurePolicy == "Fail")] | length' "$policies") -eq 5 &&
$(yq ea '[select((.spec.validationActions | length) == 1 and
  .spec.validationActions[0] == "Audit")] | length' "$bindings") -eq 5 ]] ||
  test::fail "candidate is not Fail plus Audit-only"
[[ $(NAME=atlas-bootstrap-recovery-permission-authorization yq ea -r '
  select(.metadata.name == strenv(NAME)) | .spec.paramRef.parameterNotFoundAction
' "$bindings") == Allow ]] ||
  test::fail "observing Permission Binding does not allow a missing Fence"

cat > "$kind_config" << EOF
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster
networking:
  ipFamily: ipv4
  apiServerAddress: 127.0.0.1
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |-
        apiVersion: kubeadm.k8s.io/v1beta4
        kind: ClusterConfiguration
        apiServer:
          extraArgs:
            - name: audit-log-path
              value: /var/log/kubernetes/audit/kube-apiserver-audit.log
            - name: audit-log-maxage
              value: "1"
            - name: audit-log-maxbackup
              value: "1"
            - name: audit-log-maxsize
              value: "20"
            - name: audit-policy-file
              value: /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml
          extraVolumes:
            - name: atlas-observation-audit-policy
              hostPath: /etc/kubernetes/policies
              mountPath: /etc/kubernetes/policies
              readOnly: true
              pathType: Directory
            - name: atlas-observation-audit-log
              hostPath: /var/log/kubernetes/audit
              mountPath: /var/log/kubernetes/audit
              readOnly: false
              pathType: Directory
    extraMounts:
      - hostPath: $audit_policy
        containerPath: /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml
        readOnly: true
      - hostPath: $audit_directory
        containerPath: /var/log/kubernetes/audit
        readOnly: false
EOF

require_cluster_absent creation-preflight
created=true
ci_kind create cluster \
  --name "$cluster_name" \
  --image "$node_image" \
  --config "$kind_config" \
  --kubeconfig "$kubeconfig" \
  --wait 120s

actual_node_image=$(ci_docker inspect "${cluster_name}-control-plane" --format '{{.Config.Image}}')
[[ $actual_node_image == "$node_image" ]] ||
  test::fail "CI Kind node image differs from versions.lock"
server_version=$(kubectl --kubeconfig "$kubeconfig" version -o json | yq -r '.serverVersion.gitVersion')
[[ $server_version == "v${kubernetes_version}" ]] ||
  test::fail "CI Kubernetes server differs from versions.lock: ${server_version}"

kubectl --kubeconfig "$kubeconfig" create namespace argocd > /dev/null
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
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$bindings" > /dev/null

audit_event_exists() {
  local object_name=$1 username=$2 mode=${3:-persistent}
  local audit_snapshot="$test_workspace/audit-snapshot.jsonl" event
  [[ $mode == any || $mode == persistent ]] || return 1
  # The API Server creates its host-mounted audit file as root/0600 on Linux.
  # Read it through the already authority-bound Kind node, then inspect only
  # complete JSONL records from the runner-owned snapshot.
  if ! ci_docker exec "${cluster_name}-control-plane" \
    cat /var/log/kubernetes/audit/kube-apiserver-audit.log \
    > "$audit_snapshot" 2> /dev/null; then
    return 1
  fi
  while IFS= read -r event; do
    if jq -e \
      --arg object_name "$object_name" \
      --arg username "$username" \
      --arg mode "$mode" \
      '
        .verb == "create" and
        .user.username == $username and
        .objectRef.name == $object_name and
        .responseStatus.code == 201 and
        ($mode == "any" or
          ((.requestURI | contains("dryRun=All")) | not)) and
        ((.annotations // {}) | to_entries |
          any(.key | startswith("validation.policy.admission.k8s.io/")))
      ' <<< "$event" > /dev/null 2>&1; then
      return 0
    fi
  done < "$audit_snapshot"
  return 1
}

wait_for_audit_event() {
  local object_name=$1 username=$2 attempt
  for ((attempt = 1; attempt <= 30; attempt++)); do
    audit_event_exists "$object_name" "$username" && return 0
    sleep 1
  done
  test::fail "Audit-only admission event was not observable: ${object_name}"
}

signal=gitops/platform/management/protection-foundation/definitions/signal/adoption-signal.yaml
for ((attempt = 1; attempt <= 30; attempt++)); do
  kubectl --kubeconfig "$kubeconfig" create --dry-run=server --validate=strict -f "$signal" > /dev/null
  audit_event_exists atlas-bootstrap-adoption-signal kubernetes-admin any && break
  sleep 1
done
audit_event_exists atlas-bootstrap-adoption-signal kubernetes-admin any ||
  test::fail "target-bound observing controls did not propagate"
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$signal" > /dev/null
[[ $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-bootstrap-adoption-signal \
  -n argocd -o name) == configmap/atlas-bootstrap-adoption-signal ]] ||
  test::fail "Audit-only candidate blocked an ordinary validation failure"
wait_for_audit_event atlas-bootstrap-adoption-signal kubernetes-admin

target_uid=6c172134-40de-4a43-b5d2-63529fc3feb0
authorizer="atlas:session-authz:${target_uid}:g1"
recovery="atlas:break-glass:${target_uid}:g1"
session=0123456789abcdef0123456789abcdef
authorizer_rbac="$test_workspace/authorizer-rbac.yaml"
cat > "$authorizer_rbac" << EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: atlas-bootstrap-recovery
  namespace: kube-system
rules: []
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: atlas-observation-authorizer-fixture
  namespace: kube-system
rules:
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["rolebindings"]
    verbs: ["create", "delete", "get"]
  - apiGroups: ["rbac.authorization.k8s.io"]
    resources: ["roles"]
    resourceNames: ["atlas-bootstrap-recovery"]
    verbs: ["bind", "get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: atlas-observation-authorizer-fixture
  namespace: kube-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: atlas-observation-authorizer-fixture
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: User
    name: $authorizer
EOF
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$authorizer_rbac" > /dev/null

wait_for_authorizer_permission() {
  local verb=$1 resource=$2 attempt status decision diagnostic
  local decision_file="$test_workspace/authorizer-permission.out"
  local diagnostic_file="$test_workspace/authorizer-permission.err"
  for ((attempt = 1; attempt <= 30; attempt++)); do
    if kubectl --kubeconfig "$kubeconfig" --as="$authorizer" \
      --as-group=system:authenticated auth can-i "$verb" "$resource" \
      --namespace kube-system > "$decision_file" 2> "$diagnostic_file"; then
      status=0
    else
      status=$?
    fi
    decision=$(tr -d '\r\n' < "$decision_file")
    diagnostic=$(< "$diagnostic_file")
    [[ -z $diagnostic ]] || test::fail "authorizer permission probe emitted a diagnostic"
    case "${status}:${decision}" in
      0:yes) return 0 ;;
      1:no) sleep 1 ;;
      *) test::fail "authorizer permission probe returned an unknown result" ;;
    esac
  done
  test::fail "authorizer permission did not converge: ${verb} ${resource}"
}

wait_for_authorizer_permission create rolebindings
wait_for_authorizer_permission bind roles/atlas-bootstrap-recovery

missing_fence="$test_workspace/missing-fence.yaml"
RECOVERY=$(printf '%s' "$recovery" | sed 's/[&/]/\\&/g')
LC_ALL=C sed \
  -e "s/atlas:break-glass:00000000-0000-0000-0000-000000000000:g1/${RECOVERY}/g" \
  tests/gitops/fixtures/phase1a-server/missing-fence-rolebinding.yaml > "$missing_fence"
grep -Fq "$recovery" "$missing_fence" || test::fail "missing-Fence fixture is not target-bound"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-bootstrap-operation-fence \
  -n kube-system --ignore-not-found -o name) ]] || test::fail "missing-Fence fixture found a Fence"
kubectl --kubeconfig "$kubeconfig" --as="$authorizer" --as-group=system:authenticated \
  create --validate=strict -f "$missing_fence" > /dev/null
missing_fence_name="atlas-bg-kube-system-${session}"
[[ $(kubectl --kubeconfig "$kubeconfig" get rolebinding "$missing_fence_name" \
  -n kube-system -o name) == "rolebinding.rbac.authorization.k8s.io/${missing_fence_name}" ]] ||
  test::fail "missing-Fence observing request was rejected"
kubectl --kubeconfig "$kubeconfig" --as="$authorizer" --as-group=system:authenticated \
  delete --wait=true -f "$missing_fence" > /dev/null

runtime_policy="$test_workspace/runtime-error-policy.yaml"
cat > "$runtime_policy" << 'EOF'
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: atlas-observation-runtime-error
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: [""]
        apiVersions: ["v1"]
        operations: ["CREATE"]
        resources: ["configmaps"]
        scope: Namespaced
  matchConditions:
    - name: exact-runtime-error-fixture
      expression: >-
        request.namespace == 'kube-system' &&
        object.metadata.name == 'atlas-observation-runtime-error'
  validations:
    - expression: "object.data['missing'].startsWith('atlas')"
      message: runtime error fixture
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: atlas-observation-runtime-error
spec:
  policyName: atlas-observation-runtime-error
  validationActions:
    - Audit
EOF
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$runtime_policy" > /dev/null
wait_for_typecheck atlas-observation-runtime-error
runtime_configmap="$test_workspace/runtime-error-configmap.yaml"
cat > "$runtime_configmap" << 'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: atlas-observation-runtime-error
  namespace: kube-system
data:
  sentinel: present
EOF
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$runtime_configmap" > /dev/null
[[ $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-observation-runtime-error \
  -n kube-system -o name) == configmap/atlas-observation-runtime-error ]] ||
  test::fail "Audit-only candidate blocked a CEL runtime error"
wait_for_audit_event atlas-observation-runtime-error kubernetes-admin

kubectl --kubeconfig "$kubeconfig" delete --wait=true -f "$runtime_configmap" > /dev/null
kubectl --kubeconfig "$kubeconfig" delete --wait=true -f "$runtime_policy" > /dev/null
kubectl --kubeconfig "$kubeconfig" delete --wait=true -f "$signal" > /dev/null
kubectl --kubeconfig "$kubeconfig" delete --wait=true -f "$authorizer_rbac" > /dev/null
kubectl --kubeconfig "$kubeconfig" delete --wait=true -f "$bindings" > /dev/null
kubectl --kubeconfig "$kubeconfig" delete --wait=true -f "$policies" > /dev/null
kubectl --kubeconfig "$kubeconfig" delete namespace argocd --wait=true > /dev/null

[[ -z $(kubectl --kubeconfig "$kubeconfig" get configmap atlas-observation-runtime-error \
  -n kube-system --ignore-not-found -o name) &&
-z $(kubectl --kubeconfig "$kubeconfig" get rolebinding "$missing_fence_name" \
  -n kube-system --ignore-not-found -o name) &&
-z $(kubectl --kubeconfig "$kubeconfig" get role atlas-observation-authorizer-fixture \
  -n kube-system --ignore-not-found -o name) &&
-z $(kubectl --kubeconfig "$kubeconfig" get role atlas-bootstrap-recovery \
  -n kube-system --ignore-not-found -o name) &&
-z $(kubectl --kubeconfig "$kubeconfig" get rolebinding atlas-observation-authorizer-fixture \
  -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "observing server contract left a persistent fixture"
[[ $(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicies \
  -l app.kubernetes.io/part-of=atlas-recovery -o json | yq '.items | length') -eq 0 &&
$(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicybindings \
  -l app.kubernetes.io/part-of=atlas-recovery -o json | yq '.items | length') -eq 0 ]] ||
  test::fail "observing server contract did not remove all candidate controls"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicy \
  atlas-observation-runtime-error --ignore-not-found -o name) &&
-z $(kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicybinding \
  atlas-observation-runtime-error --ignore-not-found -o name) &&
-z $(kubectl --kubeconfig "$kubeconfig" get namespace argocd --ignore-not-found -o name) ]] ||
  test::fail "observing server contract cleanup is incomplete"

test::pass "target-bound observing controls audit without blocking on locked Kubernetes"
