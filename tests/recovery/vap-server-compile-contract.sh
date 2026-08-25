#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
# shellcheck source=tests/recovery/vap-server-kind-inventory.sh
source "$(dirname "${BASH_SOURCE[0]}")/vap-server-kind-inventory.sh"
cd "$ATLAS_TEST_ROOT"

recovery::die() {
  test::fail "$*"
}

readonly ATLAS_RECOVERY_ROOT_DIR=$ATLAS_TEST_ROOT
# shellcheck source=bootstrap/recovery/canary-session.sh
source bootstrap/recovery/canary-session.sh
# shellcheck source=bootstrap/recovery/canary-ceremony.sh
source bootstrap/recovery/canary-ceremony.sh

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
session_static="${test_workspace}/session-static.yaml"
authorizer_activation="${test_workspace}/authorizer-activation.yaml"

./bootstrap/recovery/atlas-recovery phase0 admission-canary-manifests \
  --recovery-operator "$recovery_operator" > "$admission_bundle"
./bootstrap/recovery/atlas-recovery phase0 session-authorization-canary-manifests \
  --recovery-operator "$recovery_operator" \
  --session-authorizer "$session_authorizer" > "$session_bundle"
yq ea 'select(.kind == "ValidatingAdmissionPolicy")' \
  "$admission_bundle" "$session_bundle" > "$policies"
yq ea 'select(.kind != "RoleBinding" or .metadata.name != "atlas-bootstrap-recovery-authorizer-canary")' \
  "$session_bundle" > "$session_static"
yq ea 'select(.kind == "RoleBinding" and .metadata.name == "atlas-bootstrap-recovery-authorizer-canary")' \
  "$session_bundle" > "$authorizer_activation"
[[ $(yq ea '[.] | length' "$policies") -eq 5 ]] ||
  test::fail "server-side contract did not render exactly five VAPs"
[[ $(yq ea '[.] | length' "$session_static") -eq 11 &&
$(yq ea '[.] | length' "$authorizer_activation") -eq 1 ]] ||
  test::fail "server-side contract did not split the Session Authorizer activation"

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

kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$admission_bundle" > /dev/null
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$session_static" > /dev/null

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

verify_live_projections() {
  local scope=$1 phase namespace resource kind name label bundle count=0 expected=17
  local desired_raw desired live_raw live
  while IFS=$'\t' read -r phase namespace resource kind name label; do
    [[ $scope == full || $phase != activation ]] || continue
    case "$phase" in
      admission) bundle=$admission_bundle ;;
      static) bundle=$session_static ;;
      activation) bundle=$authorizer_activation ;;
      *) test::fail "unknown definition phase: ${phase}" ;;
    esac
    desired_raw="${test_workspace}/${scope}-${label}-desired-raw.json"
    desired="${test_workspace}/${scope}-${label}-desired.json"
    live_raw="${test_workspace}/${scope}-${label}-live-raw.json"
    live="${test_workspace}/${scope}-${label}-live.json"
    KIND=$kind NAMESPACE=$namespace NAME=$name yq ea -o=json -I=0 '
      select(.kind == env(KIND) and .metadata.name == env(NAME) and
        (.metadata.namespace // "cluster") == env(NAMESPACE))
    ' "$bundle" > "$desired_raw"
    [[ $(wc -l < "$desired_raw" | tr -d ' ') == 1 ]] ||
      test::fail "approved bundle projection is not unique: ${label}"
    phase0_ceremony::_normalize_definition "$desired_raw" > "$desired"
    if [[ $namespace == cluster ]]; then
      kubectl --kubeconfig "$kubeconfig" get "$resource" "$name" -o json > "$live_raw"
    else
      kubectl --kubeconfig "$kubeconfig" get "$resource" "$name" -n "$namespace" -o json > "$live_raw"
    fi
    phase0_ceremony::_normalize_definition "$live_raw" > "$live"
    cmp -s "$desired" "$live" ||
      test::fail "locked API Server projection differs from the approved bundle: ${label}"
    ((count += 1))
  done < <(phase0_ceremony::_definition_inventory)
  [[ $scope == full ]] || expected=16
  ((count == expected)) || test::fail "server-side projection inventory is incomplete: ${scope}"
}

verify_live_projections static
kubectl --kubeconfig "$kubeconfig" create --validate=strict -f "$authorizer_activation" > /dev/null
verify_live_projections full

missing_fence_session=0123456789abcdef0123456789abcdef
missing_fence_binding="${test_workspace}/binding-missing-fence.json"
missing_fence_evidence="${test_workspace}/missing-fence-evidence"
missing_fence_journal="${test_workspace}/missing-fence-journal.tsv"
mkdir -m 0700 "$missing_fence_evidence" "$missing_fence_evidence/authorization"
: > "$missing_fence_journal"
ATLAS_PHASE0_OPERATION["evidence_session"]=$missing_fence_evidence
ATLAS_PHASE0_OPERATION["session_id"]=$missing_fence_session
ATLAS_PHASE0_OPERATION["target_fingerprint"]=$(printf 'a%.0s' {1..64})
ATLAS_PHASE0_OPERATION["plan_sha"]=$(printf 'b%.0s' {1..64})
ATLAS_PHASE0_OPERATION["recovery_principal"]=$recovery_operator
ATLAS_PHASE0_TARGET["known_good_revision"]=$(printf 'c%.0s' {1..40})
phase0_ceremony::_write_permission_binding "$missing_fence_binding" \
  00000000-0000-0000-0000-000000000000 "${ATLAS_PHASE0_OPERATION[plan_sha]}"
missing_fence_name="atlas-bg-canary-${missing_fence_session}"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get rolebinding "$missing_fence_name" \
  -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "missing-Fence negative control already exists"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get configmap \
  atlas-bootstrap-operation-fence-canary -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "missing-Fence negative control unexpectedly found a Fence"
[[ $(kubectl --kubeconfig "$kubeconfig" --as="$session_authorizer" auth can-i create \
  rolebindings.rbac.authorization.k8s.io -n kube-system 2> /dev/null) == yes ]] ||
  test::fail "exact Session Authorizer lacks the permission required by the negative control"
phase0_session::journal_append() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$missing_fence_journal"
}
phase0_ceremony::_expect_rejected permission-missing-fence \
  "no params found for policy binding with \`Deny\` parameterNotFoundAction" \
  kubectl --kubeconfig "$kubeconfig" --as="$session_authorizer" \
  create --validate=strict -f "$missing_fence_binding"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get rolebinding "$missing_fence_name" \
  -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "missing-Fence negative control persisted a RoleBinding"
grep -Fqx $'PROBE\tREJECTED\tpermission-missing-fence' "$missing_fence_journal" ||
  test::fail "server-side missing-Fence rejection was not journaled"

session_evidence="${test_workspace}/session-authorization-evidence"
session_journal="${test_workspace}/session-authorization-journal.tsv"
session_authorizer_kubeconfig='session-authorizer-impersonation'
recovery_operator_kubeconfig='recovery-operator-impersonation'
server_admin_kubeconfig=$kubeconfig
mkdir -m 0700 "$session_evidence" "$session_evidence/authorization" "$session_evidence/postflight"
: > "$session_journal"
ATLAS_PHASE0_OPERATION[evidence_session]=$session_evidence
ATLAS_PHASE0_OPERATION[session_id]=01234567890123456789012345678901
ATLAS_PHASE0_OPERATION[operation_id]=12345678901234567890123456789012
ATLAS_PHASE0_OPERATION[target_fingerprint]=$(printf '2%.0s' {1..64})
ATLAS_PHASE0_OPERATION[plan_sha]=$(printf '3%.0s' {1..64})
ATLAS_PHASE0_OPERATION[prepared_at]=2026-08-25T17:46:00Z
ATLAS_PHASE0_OPERATION[recovery_principal]=$recovery_operator
ATLAS_PHASE0_OPERATION[authorizer_principal]=$session_authorizer
ATLAS_PHASE0_OPERATION[authorizer_kubeconfig]=$session_authorizer_kubeconfig
ATLAS_PHASE0_OPERATION[recovery_kubeconfig]=$recovery_operator_kubeconfig
ATLAS_PHASE0_TARGET[admin_kubeconfig]=$server_admin_kubeconfig
ATLAS_PHASE0_TARGET[known_good_revision]=$(printf '4%.0s' {1..40})
phase0_session::admin() {
  kubectl --kubeconfig "$server_admin_kubeconfig" "$@"
}
phase0_session::_kubectl() {
  local requested_kubeconfig=$1
  shift
  case "$requested_kubeconfig" in
    "$session_authorizer_kubeconfig")
      kubectl --kubeconfig "$server_admin_kubeconfig" --as="$session_authorizer" "$@"
      ;;
    "$recovery_operator_kubeconfig")
      kubectl --kubeconfig "$server_admin_kubeconfig" --as="$recovery_operator" "$@"
      ;;
    "$server_admin_kubeconfig")
      kubectl --kubeconfig "$server_admin_kubeconfig" "$@"
      ;;
    *)
      test::fail "session authorization drill used an unapproved kubeconfig: ${requested_kubeconfig}"
      ;;
  esac
}
phase0_session::journal_append() {
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$session_journal"
}
phase0_ceremony::_session_authorization_drill
for expected_record in \
  $'FENCE\tACQUIRED' \
  $'PERMISSION\tINSTALLED' \
  $'GUARD\tVERIFIED' \
  $'PERMISSION\tREMOVED' \
  $'FENCE\tRELEASED'; do
  grep -Fq "$expected_record" "$session_journal" ||
    test::fail "server-side session authorization drill omitted ${expected_record}"
done
grep -Fq 'Canary permission Binding does not match the Fence lineage' \
  "$session_evidence/authorization/rejected-permission-wrong-lineage.log" ||
  test::fail "numeric wrong-lineage probe did not reach the Permission VAP"
[[ $(yq -r '[.metadata.labels."atlas.io/recovery-session", .data.operationID,
  .data.clusterFingerprintSHA256, .data.sessionID, .data.planSHA256,
  .data.knownGoodRevision] | map(tag) | unique | .[]' \
  "$session_evidence/authorization/fence.json") == '!!str' ]] ||
  test::fail "numeric Fence lineage was not emitted as Kubernetes strings"
[[ $(yq -r '[.metadata.labels."atlas.io/recovery-session",
  .metadata.annotations."atlas.io/recovery-fence-uid",
  .metadata.annotations."atlas.io/recovery-plan-sha256",
  .metadata.annotations."atlas.io/recovery-target-sha256",
  .metadata.annotations."atlas.io/recovery-revision"] | map(tag) | unique | .[]' \
  "$session_evidence/authorization/binding-wrong-lineage.json") == '!!str' ]] ||
  test::fail "numeric permission lineage was not emitted as Kubernetes strings"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get configmap \
  atlas-bootstrap-operation-fence-canary -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "server-side session authorization drill left the Fence"
[[ -z $(kubectl --kubeconfig "$kubeconfig" get rolebinding \
  "atlas-bg-canary-${ATLAS_PHASE0_OPERATION[session_id]}" \
  -n kube-system --ignore-not-found -o name) ]] ||
  test::fail "server-side session authorization drill left temporary permission"
guard_after_drill=$(kubectl --kubeconfig "$kubeconfig" get configmap \
  atlas-bootstrap-recovery-guard-canary -n kube-system -o json)
[[ $(yq -o=json -I=0 '.data' <<< "$guard_after_drill") == '{"sentinel":"recovery-guard-canary"}' &&
$(yq 'has(.metadata.annotations)' <<< "$guard_after_drill") == false ]] ||
  test::fail "server-side session authorization drill did not restore the inert Guard"

run_escape_drill() {
  local evidence_session=$1 recovery_calls=$2 journal=$3 principal_kubeconfig=recovery-impersonation
  local ci_admin_kubeconfig=$kubeconfig
  mkdir -m 0700 "$evidence_session" "$evidence_session/authorization"
  : > "$recovery_calls"
  : > "$journal"
  ATLAS_PHASE0_OPERATION[evidence_session]=$evidence_session
  ATLAS_PHASE0_OPERATION[admission_bundle]=$admission_bundle
  ATLAS_PHASE0_OPERATION[admission_bundle_sha]=$(phase0_session::_sha256 "$admission_bundle")
  ATLAS_PHASE0_OPERATION[recovery_kubeconfig]=$principal_kubeconfig
  ATLAS_PHASE0_TARGET[admin_kubeconfig]=$ci_admin_kubeconfig
  phase0_session::admin() {
    kubectl --kubeconfig "$ci_admin_kubeconfig" "$@"
  }
  phase0_session::_kubectl() {
    local requested_kubeconfig=$1
    shift
    if [[ $requested_kubeconfig == "$principal_kubeconfig" ]]; then
      {
        printf '%q' "$1"
        printf ' %q' "${@:2}"
        printf '\n'
      } >> "$recovery_calls"
      kubectl --kubeconfig "$ci_admin_kubeconfig" --as="$recovery_operator" "$@"
    elif [[ $requested_kubeconfig == "$ci_admin_kubeconfig" ]]; then
      kubectl --kubeconfig "$ci_admin_kubeconfig" "$@"
    else
      test::fail "escape drill used an unapproved kubeconfig: ${requested_kubeconfig}"
    fi
  }
  phase0_session::journal_append() {
    printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$journal"
  }
  phase0_ceremony::_admission_escape_drill
}

escape_binding=atlas-bootstrap-admission-escape-canary
drift_before="${test_workspace}/drift-binding-before.json"
drift_after="${test_workspace}/drift-binding-after.json"
drift_projection_before="${test_workspace}/drift-binding-before.projection.json"
drift_projection_after="${test_workspace}/drift-binding-after.projection.json"
drift_recovery_calls="${test_workspace}/drift-recovery-calls.txt"
drift_evidence="${test_workspace}/drift-escape-evidence"
drift_journal="${test_workspace}/drift-escape-journal.tsv"

kubectl --kubeconfig "$kubeconfig" patch validatingadmissionpolicybinding "$escape_binding" \
  --type=merge -p '{"metadata":{"labels":{"atlas.io/test-drift":"present"}}}' > /dev/null
kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicybinding "$escape_binding" \
  -o json > "$drift_before"
if (
  recovery::die() {
    printf '%s\n' "$*" >&2
    return 1
  }
  run_escape_drill "$drift_evidence" "$drift_recovery_calls" "$drift_journal"
) > "${test_workspace}/drift.stdout" 2> "${test_workspace}/drift.stderr"; then
  test::fail "server-side escape drill accepted a pre-suspend Binding drift"
fi
grep -Fq 'drifted from the approved Bundle before suspend' "${test_workspace}/drift.stderr" ||
  test::fail "pre-suspend drift did not fail at the approved projection gate"
[[ ! -s $drift_recovery_calls ]] ||
  test::fail "pre-suspend drift reached the exact Recovery principal mutation path"
kubectl --kubeconfig "$kubeconfig" get validatingadmissionpolicybinding "$escape_binding" \
  -o json > "$drift_after"
yq -o=json -I=0 '
  {"uid":.metadata.uid,"resourceVersion":.metadata.resourceVersion,
   "labels":.metadata.labels,"annotations":.metadata.annotations,"spec":.spec} | sort_keys(..)
' "$drift_before" > "$drift_projection_before"
yq -o=json -I=0 '
  {"uid":.metadata.uid,"resourceVersion":.metadata.resourceVersion,
   "labels":.metadata.labels,"annotations":.metadata.annotations,"spec":.spec} | sort_keys(..)
' "$drift_after" > "$drift_projection_after"
cmp -s "$drift_projection_before" "$drift_projection_after" ||
  test::fail "pre-suspend drift failure changed the Binding"

kubectl --kubeconfig "$kubeconfig" patch validatingadmissionpolicybinding "$escape_binding" \
  --type=json -p '[{"op":"remove","path":"/metadata/labels/atlas.io~1test-drift"}]' > /dev/null
verify_live_projections full

escape_evidence="${test_workspace}/escape-evidence"
escape_recovery_calls="${test_workspace}/escape-recovery-calls.txt"
escape_journal="${test_workspace}/escape-journal.tsv"
[[ $(kubectl --kubeconfig "$kubeconfig" --as="$recovery_operator" auth can-i patch \
  "validatingadmissionpolicybindings.admissionregistration.k8s.io/${escape_binding}" 2> /dev/null) == yes ]] ||
  test::fail "exact Recovery principal lacks the canary escape permission"
run_escape_drill "$escape_evidence" "$escape_recovery_calls" "$escape_journal"
mapfile -t escape_recovery_commands < "$escape_recovery_calls"
expected_suspend_command="patch validatingadmissionpolicybinding atlas-bootstrap-admission-escape-canary --type=json --patch-file ${escape_evidence}/authorization/suspend-patch.json"
expected_restore_command="patch validatingadmissionpolicybinding atlas-bootstrap-admission-escape-canary --type=json --patch-file ${escape_evidence}/authorization/restore-patch.json"
[[ ${#escape_recovery_commands[@]} == 2 &&
  ${escape_recovery_commands[0]} == "$expected_suspend_command" &&
  ${escape_recovery_commands[1]} == "$expected_restore_command" ]] ||
  test::fail "server-side escape drill did not limit the exact Recovery principal to suspend and restore"
grep -Fq $'ADMISSION_SUSPEND\tVERIFIED' "$escape_journal" ||
  test::fail "server-side escape drill did not verify suspension"
grep -Fq $'ADMISSION_RESTORE\tVERIFIED' "$escape_journal" ||
  test::fail "server-side escape drill did not verify exact restore"
[[ $(yq -o=json -I=0 '.spec.validationActions' \
  "$escape_evidence/authorization/admission-binding-restored.json") == '["Audit","Deny"]' ]] ||
  test::fail "server-side escape drill did not restore canonical validationActions"
[[ $(yq -r '.status.typeChecking.expressionWarnings | length' \
  "$escape_evidence/authorization/admission-policy-restored-typecheck.json") == 0 ]] ||
  test::fail "restored Policy did not retain a warning-free type-check"
policy_uid_before=$(yq -r '.metadata.uid' "$escape_evidence/authorization/admission-policy-enforced.json")
policy_uid_after=$(yq -r '.metadata.uid' "$escape_evidence/authorization/admission-policy-restored.json")
binding_uid_before=$(yq -r '.metadata.uid' "$escape_evidence/authorization/admission-binding-enforced.json")
binding_uid_after=$(yq -r '.metadata.uid' "$escape_evidence/authorization/admission-binding-restored.json")
[[ $policy_uid_before == "$policy_uid_after" && $binding_uid_before == "$binding_uid_after" ]] ||
  test::fail "server-side suspend/restore replaced a protected object UID"
verify_live_projections full

test::pass "all 17 definitions compile and the locked API Server completes suspend/restore plus the Fence/Permission/Guard matrix"
