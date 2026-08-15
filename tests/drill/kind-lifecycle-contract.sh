#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-kind-drill-test.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

drill_cli=./bootstrap/drill/atlas-kind-drill
mock_bin="${test_workspace}/bin"
command_log="${test_workspace}/commands.log"
cluster_state="${test_workspace}/cluster.state"
policy_path_state="${test_workspace}/policy-path.state"
ambient_kubeconfig="${test_workspace}/ambient.kubeconfig"
default_kubeconfig="${test_workspace}/home/.kube/config"
private_tmp="${test_workspace}/tmp"
mkdir -m 0700 "$mock_bin" "${test_workspace}/audit" "${test_workspace}/credentials" \
  "${test_workspace}/evidence" "${test_workspace}/home" "$private_tmp"
mkdir -m 0700 "${test_workspace}/home/.kube"
printf 'apiVersion: v1\nkind: Config\ncurrent-context: developer\n' > "$ambient_kubeconfig"
printf 'apiVersion: v1\nkind: Config\ncurrent-context: default-developer\n' > "$default_kubeconfig"
ambient_hash=$(shasum -a 256 "$ambient_kubeconfig" | awk '{print $1}')
default_hash=$(shasum -a 256 "$default_kubeconfig" | awk '{print $1}')

locked_kind=$(awk -F= '$1 == "KIND_VERSION" {print $2}' versions.lock)
locked_kubectl=$(awk -F= '$1 == "KUBECTL_VERSION" {print $2}' versions.lock)
locked_image=$(awk -F= '$1 == "KIND_NODE_IMAGE" {print $2}' versions.lock)

cat > "${mock_bin}/uname" << 'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  -s) printf 'Darwin\n' ;;
  -m) printf 'arm64\n' ;;
  *) exit 2 ;;
esac
EOF

cat > "${mock_bin}/kind" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ ${KIND_EXPERIMENTAL_PROVIDER:-} == docker ]] || exit 90
case "${1:-}" in
  version)
    printf 'kind v%s go-test darwin/arm64\n' "$ATLAS_TEST_KIND_VERSION"
    ;;
  get)
    [[ ${2:-} == clusters && ${3:-} == --quiet ]] || exit 2
    if [[ -n ${ATLAS_TEST_EXISTING_CLUSTER:-} ]]; then
      printf '%s\n' "$ATLAS_TEST_EXISTING_CLUSTER"
    elif [[ -s $ATLAS_TEST_CLUSTER_STATE ]]; then
      cat "$ATLAS_TEST_CLUSTER_STATE"
    fi
    ;;
  create)
    [[ ${2:-} == cluster ]] || exit 2
    [[ -z ${KUBECONFIG+x} ]] || exit 91
    printf 'KIND_CREATE' >> "$ATLAS_TEST_COMMAND_LOG"
    printf '\t%s' "$@" >> "$ATLAS_TEST_COMMAND_LOG"
    printf '\n' >> "$ATLAS_TEST_COMMAND_LOG"
    cluster='' image='' kubeconfig='' config='' retain=false
    shift 2
    while (($# > 0)); do
      case "$1" in
        --name) cluster=$2; shift 2 ;;
        --image) image=$2; shift 2 ;;
        --kubeconfig) kubeconfig=$2; shift 2 ;;
        --config) config=$2; shift 2 ;;
        --wait) shift 2 ;;
        --retain) retain=true; shift ;;
        *) exit 2 ;;
      esac
    done
    [[ -n $cluster && $image == "$ATLAS_TEST_NODE_IMAGE" && -n $kubeconfig && -s $config && $retain == true ]] || exit 92
    awk '
      /- hostPath:/ { host = $3; gsub(/^\047|\047$/, "", host) }
      /containerPath: \/etc\/kubernetes\/policies\/atlas-recovery-audit-policy.yaml/ { print host; exit }
    ' "$config" > "$ATLAS_TEST_POLICY_PATH_STATE"
    [[ -s $ATLAS_TEST_POLICY_PATH_STATE ]] || exit 93
    printf '%s\n' "$cluster" > "$ATLAS_TEST_CLUSTER_STATE"
    printf 'apiVersion: v1\nkind: Config\ncurrent-context: kind-%s\n' "$cluster" > "$kubeconfig"
    chmod 0600 "$kubeconfig"
    [[ -z ${ATLAS_TEST_KIND_CREATE_FAIL:-} ]] || exit 42
    ;;
  *) exit 2 ;;
esac
EOF

cat > "${mock_bin}/docker" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
case "${1:-}" in
  info) exit 0 ;;
  context)
    case "${2:-}" in
      show) printf 'orbstack\n' ;;
      inspect) printf 'unix://%s/.orbstack/run/docker.sock\n' "$HOME" ;;
      *) exit 2 ;;
    esac
    ;;
  image)
    [[ ${2:-} == inspect && ${3:-} == "$ATLAS_TEST_NODE_IMAGE" ]] || exit 2
    ;;
  ps)
    if [[ -s $ATLAS_TEST_CLUSTER_STATE ]]; then
      printf '%s-control-plane\n' "$(cat "$ATLAS_TEST_CLUSTER_STATE")"
    fi
    ;;
  inspect)
    format=${3:-}
    if [[ $format == *Config.Image* ]]; then
      printf '%s\n' "$ATLAS_TEST_NODE_IMAGE"
    elif [[ $format == *etc/kubernetes/policies* ]]; then
      printf '%s\tfalse\n' "$(cat "$ATLAS_TEST_POLICY_PATH_STATE")"
    elif [[ $format == *var/log/kubernetes/audit* ]]; then
      printf '%s\ttrue\n' "$ATLAS_TEST_AUDIT_DIR"
    else
      exit 2
    fi
    ;;
  exec)
    if [[ ${3:-} == grep ]]; then
      exit 0
    fi
    [[ ${3:-} == sha256sum ]] || exit 2
    policy=$(cat "$ATLAS_TEST_POLICY_PATH_STATE")
    printf '%s  %s\n' "$(shasum -a 256 "$policy" | awk '{print $1}')" "${4:-}"
    ;;
  *) exit 2 ;;
esac
EOF

cat > "${mock_bin}/kubectl" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${1:-} == version ]]; then
  printf '{\n  "clientVersion": {\n    "gitVersion": "v%s"\n  }\n}\n' "$ATLAS_TEST_KUBECTL_VERSION"
  exit 0
fi
[[ -z ${KUBECONFIG+x} ]] || exit 91
[[ ${1:-} == --kubeconfig && ${2:-} == "$ATLAS_TEST_KUBECONFIG" ]] || exit 92
[[ ${3:-} == --context && ${4:-} == "$ATLAS_TEST_CONTEXT" ]] || exit 93
[[ ${5:-} == --request-timeout=20s ]] || exit 94
shift 5
printf 'KUBECTL' >> "$ATLAS_TEST_COMMAND_LOG"
printf '\t%s' "$@" >> "$ATLAS_TEST_COMMAND_LOG"
printf '\n' >> "$ATLAS_TEST_COMMAND_LOG"
case "${1:-} ${2:-}" in
  'config current-context') printf '%s\n' "$ATLAS_TEST_CONTEXT" ;;
  'wait node') exit 0 ;;
  'get --raw=/readyz')
    printf '{"verb":"get","requestURI":"/readyz"}\n' >> "$ATLAS_TEST_AUDIT_DIR/kube-apiserver-audit.log"
    chmod 0600 "$ATLAS_TEST_AUDIT_DIR/kube-apiserver-audit.log"
    printf 'ok\n'
    ;;
  *) exit 2 ;;
esac
EOF

cat > "${test_workspace}/run-lifecycle" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'
umask 077
source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/contract.sh"
source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/lock.sh"
source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/evidence.sh"
source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/lifecycle.sh"
drill::die() {
  printf 'drill-test: %s\n' "$*" >&2
  return 1
}
drill::_git_authority() {
  if [[ -e ${ATLAS_TEST_GIT_CHANGED_MARKER:-/nonexistent} ]]; then
    printf '%040d\t%040d\n' 3 4
  else
    printf '%040d\t%040d\n' 1 2
  fi
}
drill::_version_triplet() {
  drill::target BASH_VERSION
}
if [[ ${ATLAS_TEST_GATE_MODE:-approve} == noninteractive ]]; then
  drill::_terminal_available() { return 1; }
else
  drill::_human_gate() {
    drill::journal_append GATE PROMPTED "mock approval requested"
    drill::journal_append GATE APPROVED "mock exact challenge matched"
    printf 'GATE\t%s\n' "$(drill::operation plan_sha)" >> "$ATLAS_TEST_COMMAND_LOG"
    case "${ATLAS_TEST_GATE_MODE:-approve}" in
      tamper-policy)
        chmod 0600 "$(drill::operation policy_snapshot)"
        printf '# changed after approval\n' >> "$(drill::operation policy_snapshot)"
        chmod 0400 "$(drill::operation policy_snapshot)"
        ;;
      tamper-config)
        chmod 0600 "$(drill::operation config_file)"
        printf '# changed after approval\n' >> "$(drill::operation config_file)"
        chmod 0400 "$(drill::operation config_file)"
        ;;
      tamper-git)
        : > "$ATLAS_TEST_GIT_CHANGED_MARKER"
        ;;
    esac
  }
fi
drill::create_cluster \
  "$ATLAS_TEST_CLUSTER" "$ATLAS_TEST_CONTEXT" "$ATLAS_TEST_KUBECONFIG" \
  "$ATLAS_TEST_AUDIT_DIR" "$ATLAS_TEST_EVIDENCE_ROOT" encrypted-owner-controlled
EOF

chmod 0755 "${mock_bin}/docker" "${mock_bin}/kind" "${mock_bin}/kubectl" "${mock_bin}/uname" \
  "${test_workspace}/run-lifecycle"

export PATH="${mock_bin}:${PATH}"
export HOME="${test_workspace}/home"
export TMPDIR="${private_tmp}/"
export KUBECONFIG=$ambient_kubeconfig
export ATLAS_DRILL_ROOT_DIR=$ATLAS_TEST_ROOT
export ATLAS_TEST_COMMAND_LOG=$command_log
export ATLAS_TEST_CLUSTER_STATE=$cluster_state
export ATLAS_TEST_POLICY_PATH_STATE=$policy_path_state
export ATLAS_TEST_GIT_CHANGED_MARKER="${test_workspace}/git-changed.marker"
export ATLAS_TEST_KIND_VERSION=$locked_kind
export ATLAS_TEST_KUBECTL_VERSION=$locked_kubectl
export ATLAS_TEST_NODE_IMAGE=$locked_image

drill_test::prepare_target() {
  local cluster=$1
  ATLAS_TEST_CLUSTER=$cluster
  ATLAS_TEST_CONTEXT="kind-${cluster}"
  ATLAS_TEST_AUDIT_DIR="${test_workspace}/audit/${cluster}"
  ATLAS_TEST_EVIDENCE_ROOT="${test_workspace}/evidence/${cluster}"
  credential_parent="${test_workspace}/credentials/${cluster}"
  mkdir -m 0700 "$ATLAS_TEST_AUDIT_DIR" "$ATLAS_TEST_EVIDENCE_ROOT" "$credential_parent"
  ATLAS_TEST_AUDIT_DIR=$(cd "$ATLAS_TEST_AUDIT_DIR" && pwd -P)
  ATLAS_TEST_EVIDENCE_ROOT=$(cd "$ATLAS_TEST_EVIDENCE_ROOT" && pwd -P)
  ATLAS_TEST_KUBECONFIG="$(cd "$credential_parent" && pwd -P)/${cluster}.kubeconfig"
  export ATLAS_TEST_CLUSTER ATLAS_TEST_CONTEXT ATLAS_TEST_AUDIT_DIR ATLAS_TEST_EVIDENCE_ROOT ATLAS_TEST_KUBECONFIG
}

drill_test::journal() {
  find "$ATLAS_TEST_EVIDENCE_ROOT" -type f -name journal.jsonl -print -quit
}

drill_test::plan() {
  find "$ATLAS_TEST_EVIDENCE_ROOT" -type f -name plan.json -print -quit
}

drill_test::verify_journal_chain() {
  local journal=$1 line previous current
  previous=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  while IFS= read -r line; do
    [[ $(yq -p=json '.previousEntrySHA256' <<< "$line") == "$previous" ]] || test::fail "journal hash chain is discontinuous"
    current=$(yq -p=json '.entrySHA256' <<< "$line")
    [[ $current =~ ^[0-9a-f]{64}$ ]] || test::fail "journal entry hash is malformed"
    previous=$current
  done < "$journal"
}

drill_test::reset_runtime() {
  rm -f -- "$cluster_state" "$policy_path_state" "$ATLAS_TEST_KUBECONFIG"
  rm -f -- "$ATLAS_TEST_GIT_CHANGED_MARKER"
  : > "$command_log"
  unset ATLAS_TEST_EXISTING_CLUSTER ATLAS_TEST_KIND_CREATE_FAIL ATLAS_TEST_GATE_MODE
}

"$drill_cli" --help > /dev/null
"$drill_cli" --version | grep -Eq '^atlas-kind-drill [0-9]+\.[0-9]+\.[0-9]+$'

cluster_one=atlas-recovery-drill-20260815t010203z-a1b2c3d4
drill_test::prepare_target "$cluster_one"
if "$drill_cli" create \
  --cluster-name invalid --context kind-invalid \
  --kubeconfig "$ATLAS_TEST_KUBECONFIG" --audit-dir "$ATLAS_TEST_AUDIT_DIR" \
  --evidence-root "$ATLAS_TEST_EVIDENCE_ROOT" \
  --storage-assertion encrypted-owner-controlled > /dev/null 2>&1; then
  test::fail "an unscoped drill cluster name was accepted"
fi
if "$drill_cli" create \
  --cluster-name "$cluster_one" --context developer \
  --kubeconfig "$ATLAS_TEST_KUBECONFIG" --audit-dir "$ATLAS_TEST_AUDIT_DIR" \
  --evidence-root "$ATLAS_TEST_EVIDENCE_ROOT" \
  --storage-assertion encrypted-owner-controlled > /dev/null 2>&1; then
  test::fail "a default or mismatched context was accepted"
fi
if KUBECONFIG=$ATLAS_TEST_KUBECONFIG "$drill_cli" create \
  --cluster-name "$cluster_one" --context "$ATLAS_TEST_CONTEXT" \
  --kubeconfig "$ATLAS_TEST_KUBECONFIG" --audit-dir "$ATLAS_TEST_AUDIT_DIR" \
  --evidence-root "$ATLAS_TEST_EVIDENCE_ROOT" \
  --storage-assertion encrypted-owner-controlled > /dev/null 2>&1; then
  test::fail "an ambient kubeconfig destination was accepted"
fi
test::pass "drill identity, storage attestation, and kubeconfig isolation fail closed"

cluster_two=atlas-recovery-drill-20260815t020304z-b2c3d4e5
drill_test::prepare_target "$cluster_two"
chmod 0777 "$(dirname "$ATLAS_TEST_KUBECONFIG")"
if "${test_workspace}/run-lifecycle" > /dev/null 2>&1; then
  test::fail "a world-writable kubeconfig parent was accepted"
fi
chmod 0700 "$(dirname "$ATLAS_TEST_KUBECONFIG")"
chmod 0777 "$ATLAS_TEST_AUDIT_DIR"
if "${test_workspace}/run-lifecycle" > /dev/null 2>&1; then
  test::fail "a world-writable audit directory was accepted"
fi
chmod 0700 "$ATLAS_TEST_AUDIT_DIR"
chmod 0777 "$ATLAS_TEST_EVIDENCE_ROOT"
if "${test_workspace}/run-lifecycle" > /dev/null 2>&1; then
  test::fail "a world-writable evidence root was accepted"
fi
chmod 0700 "$ATLAS_TEST_EVIDENCE_ROOT"
test::pass "audit, evidence, and credential directories require owner-only custody"

cluster_three=atlas-recovery-drill-20260815t030405z-c3d4e5f6
drill_test::prepare_target "$cluster_three"
: > "$command_log"
ATLAS_TEST_EXISTING_CLUSTER=$cluster_three "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "an existing drill cluster was reused"
grep -Fq KIND_CREATE "$command_log" && test::fail "existing-cluster rejection reached Kind creation"
KIND_EXPERIMENTAL_PROVIDER=podman "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "a caller-selected Kind provider was accepted"
grep -Fq KIND_CREATE "$command_log" && test::fail "Kind environment rejection reached cluster creation"
test::pass "existing state and all inherited KIND_* topology controls fail closed"

cluster_four=atlas-recovery-drill-20260815t040506z-d4e5f6a7
drill_test::prepare_target "$cluster_four"
lock_root="${TMPDIR%/}/atlas-kind-drill-locks"
mkdir -m 0700 "${lock_root}/${cluster_four}.lock"
"${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "a concurrent lifecycle lock was ignored"
grep -Fq KIND_CREATE "$command_log" && test::fail "lock rejection reached cluster creation"
rmdir "${lock_root}/${cluster_four}.lock"
test::pass "the dedicated host lifecycle lock rejects concurrent creation"

cluster_five=atlas-recovery-drill-20260815t050607z-e5f6a7b8
drill_test::prepare_target "$cluster_five"
: > "$command_log"
ATLAS_TEST_GATE_MODE=noninteractive "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "non-interactive creation bypassed the Human Judgment Gate"
grep -Fq KIND_CREATE "$command_log" && test::fail "Human Judgment rejection reached Kind creation"
noninteractive_journal=$(drill_test::journal)
[[ -s $noninteractive_journal ]] || test::fail "a denied Human Gate was not journaled"
grep -Fq '"action":"GATE","outcome":"DENIED"' "$noninteractive_journal" || test::fail "Human Gate denial is absent from the journal"
test::pass "cluster creation has no non-interactive or unjournaled approval path"

for gate_mode in tamper-policy tamper-config tamper-git; do
  cluster_suffix=f6a7b8c9
  [[ $gate_mode == tamper-config ]] && cluster_suffix=07b8c9da
  [[ $gate_mode == tamper-git ]] && cluster_suffix=18c9daeb
  cluster="atlas-recovery-drill-20260815t060708z-${cluster_suffix}"
  drill_test::prepare_target "$cluster"
  : > "$command_log"
  ATLAS_TEST_GATE_MODE=$gate_mode "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "${gate_mode} crossed the pre-mutation revalidation gate"
  grep -Fq KIND_CREATE "$command_log" && test::fail "${gate_mode} reached Kind creation"
  tamper_journal=$(drill_test::journal)
  grep -Fq '"action":"PREMUTATION","outcome":"DENIED"' "$tamper_journal" || test::fail "${gate_mode} rejection was not journaled"
  rm -f -- "$ATLAS_TEST_GIT_CHANGED_MARKER"
done
test::pass "Gate-approved policy and Kind configuration hashes are revalidated"

cluster_eight=atlas-recovery-drill-20260815t080910z-18c9daeb
drill_test::prepare_target "$cluster_eight"
: > "$command_log"
ATLAS_TEST_KIND_CREATE_FAIL=1 "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "a failed Kind create returned success"
failed_journal=$(drill_test::journal)
grep -Fq '"action":"CREATE","outcome":"FAILED"' "$failed_journal" || test::fail "retained create failure was not journaled"
[[ -s $(drill_test::plan) ]] || test::fail "failed creation removed its approved plan"
[[ -s $ATLAS_TEST_KUBECONFIG && -s $cluster_state ]] || test::fail "mock retained state was unexpectedly cleaned"
drill_test::reset_runtime
test::pass "failed creation preserves inputs, journal, and retained-state inventory"

cluster_nine=atlas-recovery-drill-20260815t091011z-29daebfc
drill_test::prepare_target "$cluster_nine"
: > "$command_log"
"${test_workspace}/run-lifecycle" > /dev/null
journal=$(drill_test::journal)
plan=$(drill_test::plan)
[[ -s $journal && -s $plan ]] || test::fail "successful lifecycle evidence is missing"
grep -Fq '"action":"GATE","outcome":"APPROVED"' "$journal" || test::fail "Gate approval is absent from the journal"
grep -Fq '"action":"VERIFY","outcome":"READY"' "$journal" || test::fail "READY result is absent from the journal"
grep -Fq '"previousEntrySHA256"' "$journal" || test::fail "journal entries are not hash chained"
drill_test::verify_journal_chain "$journal"
[[ $(yq '.gitCommit | length' "$plan") == 40 ]] || test::fail "plan lacks the Git commit authority"
[[ $(yq '.gitTree | length' "$plan") == 40 ]] || test::fail "plan lacks the Git tree authority"
[[ $(yq '.kindConfigSHA256 | length' "$plan") == 64 ]] || test::fail "plan lacks the Kind config hash"
[[ $(yq '.auditPolicySHA256 | length' "$plan") == 64 ]] || test::fail "plan lacks the audit policy hash"
[[ $(yq '.versionsLockSHA256 | length' "$plan") == 64 ]] || test::fail "plan lacks the versions.lock hash"
[[ $(yq -r '.storageAssertion' "$plan") == encrypted-owner-controlled ]] || test::fail "plan lacks the storage assertion"
[[ $(yq -r '.nodeImage' "$plan") == "$locked_image" ]] || test::fail "plan lacks the digest-pinned node image"
[[ $(shasum -a 256 "$ambient_kubeconfig" | awk '{print $1}') == "$ambient_hash" ]] || test::fail "ambient kubeconfig changed"
[[ $(shasum -a 256 "$default_kubeconfig" | awk '{print $1}') == "$default_hash" ]] || test::fail "default kubeconfig changed"
gate_line=$(grep -n '^GATE' "$command_log" | cut -d: -f1)
create_line=$(grep -n '^KIND_CREATE' "$command_log" | cut -d: -f1)
[[ -n $gate_line && -n $create_line && $gate_line -lt $create_line ]] || test::fail "cluster mutation preceded the Human Judgment Gate"
grep -Fq $'\t--retain' "$command_log" || test::fail "Kind failure retention is not enabled"
test::pass "mocked lifecycle binds authority, journals approval, and verifies audit output"

test::assert_not_found 'kind[[:space:]]+delete|kubectl[[:space:]]+config[[:space:]]+(use-context|set-context)|--approve' bootstrap/drill
test::assert_not_found 'atlas-kind-drill|bootstrap/drill' bootstrap/recovery
test::assert_not_found 'certificatesigningrequests|ClusterRoleBinding|RoleBinding|ValidatingAdmissionPolicy|atlas-adoption-(signal|receipt)' bootstrap/drill
test::pass "drill lifecycle remains separate from recovery and future authorization gates"
