#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_parent=${RUNNER_TEMP:-${TMPDIR:-${HOME}/.cache}}
test_workspace=$(mktemp -d "${test_parent%/}/atlas-kind-drill-test.XXXXXX")
shared_evidence=$(mktemp -d /tmp/atlas-kind-drill-shared-test.XXXXXX)
trap 'rm -rf "$test_workspace" "$shared_evidence"' EXIT

drill_cli=./bootstrap/drill/atlas-kind-drill
mock_bin="${test_workspace}/bin"
command_log="${test_workspace}/commands.log"
cluster_state="${test_workspace}/cluster.state"
policy_path_state="${test_workspace}/policy-path.state"
docker_endpoint_state="${test_workspace}/docker-endpoint.state"
ambient_kubeconfig="${test_workspace}/ambient.kubeconfig"
default_kubeconfig="${test_workspace}/home/.kube/config"
private_tmp="${test_workspace}/tmp"
lock_parent="${test_workspace}/system-locks"
mkdir -m 0700 "$mock_bin" "${test_workspace}/audit" "${test_workspace}/credentials" \
  "${test_workspace}/evidence" "${test_workspace}/home" "$private_tmp" "$lock_parent"
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
[[ ${DOCKER_CONTEXT:-} == orbstack ]] || exit 94
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
[[ ${DOCKER_CONTEXT:-} == orbstack ]] || exit 90
case "${1:-}" in
  info) exit 0 ;;
  context)
    case "${2:-}" in
      show) printf '%s\n' "$DOCKER_CONTEXT" ;;
      inspect) cat "$ATLAS_TEST_DOCKER_ENDPOINT_STATE" ;;
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
drill::_version_triplet() {
  drill::target BASH_VERSION
}
drill::_system_temporary_directory() {
  printf '%s\n' "$ATLAS_TEST_LOCK_PARENT"
}
if [[ ${ATLAS_TEST_GATE_MODE:-approve} == noninteractive ]]; then
  drill::_terminal_available() { return 1; }
else
  drill::_human_gate() {
    drill::journal_append GATE PROMPTED "mock approval requested"
    drill::journal_append GATE APPROVED "mock exact challenge matched"
    printf 'GATE\t%s\n' "$(drill::operation approval_sha)" >> "$ATLAS_TEST_COMMAND_LOG"
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
        printf 'changed after approval\n' > "${ATLAS_DRILL_ROOT_DIR}/tracked"
        ;;
      tamper-docker)
        printf 'unix:///changed-after-approval.sock\n' > "$ATLAS_TEST_DOCKER_ENDPOINT_STATE"
        ;;
      tamper-manifest)
        chmod 0600 "$(drill::operation pre_mutation_file)"
        printf '%064d  forged\n' 0 >> "$(drill::operation pre_mutation_file)"
        chmod 0400 "$(drill::operation pre_mutation_file)"
        ;;
      tamper-plan-hash-record)
        chmod 0600 "$(drill::operation plan_sha_file)"
        printf '%064d  forged\n' 0 >> "$(drill::operation plan_sha_file)"
        chmod 0400 "$(drill::operation plan_sha_file)"
        ;;
      tamper-manifest-hash-record)
        chmod 0600 "$(drill::operation pre_mutation_sha_file)"
        printf '%064d  forged\n' 0 >> "$(drill::operation pre_mutation_sha_file)"
        chmod 0400 "$(drill::operation pre_mutation_sha_file)"
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

cat > "${test_workspace}/run-git-authority" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
source "$ATLAS_TEST_IMPLEMENTATION_ROOT/bootstrap/drill/evidence.sh"
drill::die() {
  printf 'git-authority-test: %s\n' "$*" >&2
  return 1
}
drill::_git_authority
EOF
chmod 0755 "${test_workspace}/run-git-authority"

export PATH="${mock_bin}:${PATH}"
export HOME="${test_workspace}/home"
export TMPDIR="${private_tmp}/"
export KUBECONFIG=$ambient_kubeconfig
export ATLAS_DRILL_ROOT_DIR=$ATLAS_TEST_ROOT
export ATLAS_TEST_COMMAND_LOG=$command_log
export ATLAS_TEST_CLUSTER_STATE=$cluster_state
export ATLAS_TEST_POLICY_PATH_STATE=$policy_path_state
export ATLAS_TEST_DOCKER_ENDPOINT_STATE=$docker_endpoint_state
export ATLAS_TEST_LOCK_PARENT=$lock_parent
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

drill_test::journal_chain_valid() {
  local journal=$1 line previous current payload calculated
  previous=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  while IFS= read -r line; do
    [[ $(yq -p=json '.previousEntrySHA256' <<< "$line") == "$previous" ]] || return 1
    current=$(yq -p=json '.entrySHA256' <<< "$line") || return 1
    [[ $current =~ ^[0-9a-f]{64}$ ]] || return 1
    payload=$(sed -E 's/,"entrySHA256":"[0-9a-f]{64}"}$/}/' <<< "$line") || return 1
    [[ $payload != "$line" ]] || return 1
    calculated=$(printf '%s' "$payload" | shasum -a 256 | awk '{print $1}') || return 1
    [[ $calculated == "$current" ]] || return 1
    previous=$current
  done < "$journal"
}

drill_test::verify_journal_chain() {
  drill_test::journal_chain_valid "$1" || test::fail "journal hash chain or entry digest is invalid"
}

drill_test::reset_runtime() {
  rm -f -- "$cluster_state" "$policy_path_state" "$ATLAS_TEST_KUBECONFIG"
  printf 'unix://%s/.orbstack/run/docker.sock\n' "$HOME" > "$docker_endpoint_state"
  : > "$command_log"
  unset ATLAS_TEST_EXISTING_CLUSTER ATLAS_TEST_KIND_CREATE_FAIL ATLAS_TEST_GATE_MODE
}

printf 'unix://%s/.orbstack/run/docker.sock\n' "$HOME" > "$docker_endpoint_state"

"$drill_cli" --help > /dev/null
"$drill_cli" --version | grep -Eq '^atlas-kind-drill [0-9]+\.[0-9]+\.[0-9]+$'

git_fixture="${test_workspace}/git-atlas"
foreign_git_fixture="${test_workspace}/git-foreign"
mkdir -m 0700 "$git_fixture" "$foreign_git_fixture"
git_fixture=$(cd "$git_fixture" && pwd -P)
foreign_git_fixture=$(cd "$foreign_git_fixture" && pwd -P)
mkdir -m 0700 "${git_fixture}/bootstrap" "${git_fixture}/clusters"
cp -R "${ATLAS_TEST_ROOT}/bootstrap/drill" "${git_fixture}/bootstrap/"
cp -R "${ATLAS_TEST_ROOT}/bootstrap/recovery" "${git_fixture}/bootstrap/"
cp -R "${ATLAS_TEST_ROOT}/clusters/kind" "${git_fixture}/clusters/"
cp "${ATLAS_TEST_ROOT}/versions.lock" "${git_fixture}/versions.lock"
git -C "$git_fixture" init -q
git -C "$git_fixture" config user.name atlas-test
git -C "$git_fixture" config user.email atlas-test@example.invalid
printf 'atlas\n' > "${git_fixture}/tracked"
git -C "$git_fixture" add bootstrap clusters versions.lock tracked
git -C "$git_fixture" -c commit.gpgsign=false commit -q -m initial
git -C "$foreign_git_fixture" init -q
git -C "$foreign_git_fixture" config user.name foreign-test
git -C "$foreign_git_fixture" config user.email foreign-test@example.invalid
printf 'foreign\n' > "${foreign_git_fixture}/tracked"
git -C "$foreign_git_fixture" add tracked
git -C "$foreign_git_fixture" -c commit.gpgsign=false commit -q -m initial
expected_git_commit=$(git -C "$git_fixture" rev-parse 'HEAD^{commit}')
expected_git_tree=$(git -C "$git_fixture" rev-parse 'HEAD^{tree}')
authority=$(
  ATLAS_TEST_IMPLEMENTATION_ROOT=$ATLAS_TEST_ROOT \
    ATLAS_DRILL_ROOT_DIR=$git_fixture \
    GIT_DIR="${foreign_git_fixture}/.git" \
    GIT_WORK_TREE=$foreign_git_fixture \
    GIT_INDEX_FILE="${foreign_git_fixture}/.git/index" \
    GIT_COMMON_DIR="${foreign_git_fixture}/.git" \
    GIT_OBJECT_DIRECTORY="${foreign_git_fixture}/.git/objects" \
    GIT_ALTERNATE_OBJECT_DIRECTORIES="${foreign_git_fixture}/.git/objects" \
    GIT_NAMESPACE=foreign \
    "${test_workspace}/run-git-authority"
)
[[ $authority == "${expected_git_commit}"$'\t'"${expected_git_tree}" ]] || test::fail "Git authority inherited repository-redirection variables"
mkdir -m 0700 "${git_fixture}/nested"
ATLAS_TEST_IMPLEMENTATION_ROOT=$ATLAS_TEST_ROOT ATLAS_DRILL_ROOT_DIR="${git_fixture}/nested" \
  "${test_workspace}/run-git-authority" > /dev/null 2>&1 &&
  test::fail "Git authority accepted a non-root Atlas path"
test::pass "Git authority clears repository environment and requires the exact Atlas root"

ATLAS_DRILL_ROOT_DIR=$git_fixture
export ATLAS_DRILL_ROOT_DIR

for hidden_flag in assume-unchanged skip-worktree; do
  hidden_suffix=aabbccdd
  [[ $hidden_flag == skip-worktree ]] && hidden_suffix=bbccddee
  cluster="atlas-recovery-drill-20260815t001122z-${hidden_suffix}"
  drill_test::prepare_target "$cluster"
  : > "$command_log"
  git -C "$git_fixture" update-index "--${hidden_flag}" tracked
  printf 'hidden worktree change\n' > "${git_fixture}/tracked"
  "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "${hidden_flag} bypassed Git authority"
  grep -Eq '^(GATE|KIND_CREATE)' "$command_log" && test::fail "${hidden_flag} reached the Gate or Kind creation"
  git -C "$git_fixture" update-index "--no-${hidden_flag}" tracked
  printf 'atlas\n' > "${git_fixture}/tracked"
done
cluster=atlas-recovery-drill-20260815t001122z-ccddee00
drill_test::prepare_target "$cluster"
: > "$command_log"
git -C "$git_fixture" config core.sparseCheckout true
"${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "sparse-checkout bypassed Git authority"
grep -Eq '^(GATE|KIND_CREATE)' "$command_log" && test::fail "sparse-checkout reached the Gate or Kind creation"
git -C "$git_fixture" config --unset core.sparseCheckout
test::pass "hidden index entries and sparse-checkout fail before the Gate and Kind creation"

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
if "$drill_cli" create \
  --cluster-name "$cluster_one" --context "$ATLAS_TEST_CONTEXT" \
  --kubeconfig "$ATLAS_TEST_KUBECONFIG" --audit-dir "$ATLAS_TEST_AUDIT_DIR" \
  --evidence-root "$shared_evidence" \
  --storage-assertion encrypted-owner-controlled > /dev/null 2>&1; then
  test::fail "a shared temporary evidence root was accepted"
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
DOCKER_CONTEXT=remote "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "a caller-selected Docker target was accepted"
grep -Fq KIND_CREATE "$command_log" && test::fail "Kind environment rejection reached cluster creation"
test::pass "existing state and inherited Kind or Docker topology controls fail closed"

cluster_four=atlas-recovery-drill-20260815t040506z-d4e5f6a7
drill_test::prepare_target "$cluster_four"
lock_root="${lock_parent}/atlas-kind-drill-locks-$(id -u)"
mkdir -m 0700 "${lock_root}/${cluster_four}.lock"
mkdir -m 0700 "${test_workspace}/alternate-tmp"
TMPDIR="${test_workspace}/alternate-tmp" "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "a concurrent lifecycle lock was ignored"
grep -Fq KIND_CREATE "$command_log" && test::fail "lock rejection reached cluster creation"
rmdir "${lock_root}/${cluster_four}.lock"
test::pass "the dedicated host lifecycle lock rejects concurrent creation"

cluster_five=atlas-recovery-drill-20260815t050607z-e5f6a7b8
drill_test::prepare_target "$cluster_five"
: > "$command_log"
noninteractive_output="${test_workspace}/noninteractive.log"
ATLAS_TEST_GATE_MODE=noninteractive "${test_workspace}/run-lifecycle" > /dev/null 2> "$noninteractive_output" && test::fail "non-interactive creation bypassed the Human Judgment Gate"
grep -Fq KIND_CREATE "$command_log" && test::fail "Human Judgment rejection reached Kind creation"
noninteractive_journal=$(drill_test::journal)
[[ -s $noninteractive_journal ]] || {
  sed -n '1,80p' "$noninteractive_output" >&2
  test::fail "a denied Human Gate was not journaled"
}
grep -Fq '"action":"GATE","outcome":"DENIED"' "$noninteractive_journal" || test::fail "Human Gate denial is absent from the journal"
test::pass "cluster creation has no non-interactive or unjournaled approval path"

for gate_mode in tamper-policy tamper-config tamper-git tamper-docker tamper-manifest tamper-plan-hash-record tamper-manifest-hash-record; do
  cluster_suffix=f6a7b8c9
  [[ $gate_mode == tamper-config ]] && cluster_suffix=07b8c9da
  [[ $gate_mode == tamper-git ]] && cluster_suffix=18c9daeb
  [[ $gate_mode == tamper-docker ]] && cluster_suffix=29daebfc
  [[ $gate_mode == tamper-manifest ]] && cluster_suffix=3aebfc0d
  [[ $gate_mode == tamper-plan-hash-record ]] && cluster_suffix=4bfc0d1e
  [[ $gate_mode == tamper-manifest-hash-record ]] && cluster_suffix=5c0d1e2f
  cluster="atlas-recovery-drill-20260815t060708z-${cluster_suffix}"
  drill_test::prepare_target "$cluster"
  : > "$command_log"
  ATLAS_TEST_GATE_MODE=$gate_mode "${test_workspace}/run-lifecycle" > /dev/null 2>&1 && test::fail "${gate_mode} crossed the pre-mutation revalidation gate"
  grep -Fq KIND_CREATE "$command_log" && test::fail "${gate_mode} reached Kind creation"
  tamper_journal=$(drill_test::journal)
  grep -Fq '"action":"PREMUTATION","outcome":"DENIED"' "$tamper_journal" || test::fail "${gate_mode} rejection was not journaled"
  printf 'atlas\n' > "${git_fixture}/tracked"
  printf 'unix://%s/.orbstack/run/docker.sock\n' "$HOME" > "$docker_endpoint_state"
done
test::pass "all Gate-approved authority inputs and the Docker target are revalidated"

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
evidence_session=$(dirname "$plan")
pre_mutation_manifest="${evidence_session}/pre-mutation.sha256"
pre_mutation_hash_record="${evidence_session}/pre-mutation-manifest.sha256"
[[ -s $journal && -s $plan ]] || test::fail "successful lifecycle evidence is missing"
grep -Fq '"action":"GATE","outcome":"APPROVED"' "$journal" || test::fail "Gate approval is absent from the journal"
grep -Fq '"action":"VERIFY","outcome":"READY"' "$journal" || test::fail "READY result is absent from the journal"
grep -Fq '"previousEntrySHA256"' "$journal" || test::fail "journal entries are not hash chained"
drill_test::verify_journal_chain "$journal"
forged_journal="${test_workspace}/forged-journal.jsonl"
sed '1s/"entrySHA256":"[0-9a-f]\{64\}"/"entrySHA256":"0000000000000000000000000000000000000000000000000000000000000000"/' \
  "$journal" > "$forged_journal"
drill_test::journal_chain_valid "$forged_journal" && test::fail "a forged journal entry digest was accepted"
[[ $(yq '.gitCommit | length' "$plan") == 40 ]] || test::fail "plan lacks the Git commit authority"
[[ $(yq '.gitTree | length' "$plan") == 40 ]] || test::fail "plan lacks the Git tree authority"
[[ $(yq -r '.dockerContext' "$plan") == orbstack ]] || test::fail "plan lacks the Docker context authority"
[[ $(yq -r '.dockerEndpoint' "$plan") == "unix://${HOME}/.orbstack/run/docker.sock" ]] || test::fail "plan lacks the Docker endpoint authority"
[[ $(yq '.kindConfigSHA256 | length' "$plan") == 64 ]] || test::fail "plan lacks the Kind config hash"
[[ $(yq '.auditPolicySHA256 | length' "$plan") == 64 ]] || test::fail "plan lacks the audit policy hash"
[[ $(yq '.versionsLockSHA256 | length' "$plan") == 64 ]] || test::fail "plan lacks the versions.lock hash"
[[ $(yq -r '.storageAssertion' "$plan") == encrypted-owner-controlled ]] || test::fail "plan lacks the storage assertion"
[[ $(yq -r '.nodeImage' "$plan") == "$locked_image" ]] || test::fail "plan lacks the digest-pinned node image"
[[ -s ${evidence_session}/versions.lock ]] || test::fail "pre-mutation evidence lacks the versions.lock snapshot"
manifest_sha=$(shasum -a 256 "$pre_mutation_manifest" | awk '{print $1}')
plan_sha=$(shasum -a 256 "$plan" | awk '{print $1}')
approval_sha=$(printf 'planSHA256=%s\npreMutationManifestSHA256=%s\n' "$plan_sha" "$manifest_sha" | shasum -a 256 | awk '{print $1}')
grep -Fqx "${manifest_sha}  pre-mutation.sha256" "$pre_mutation_hash_record" || test::fail "pre-mutation manifest hash is not persisted"
grep -Fqx "$(shasum -a 256 "${evidence_session}/versions.lock" | awk '{print $1}')  versions.lock" "$pre_mutation_manifest" ||
  test::fail "pre-mutation manifest does not bind its versions.lock snapshot"
gate_record=$(grep '"action":"GATE","outcome":"APPROVED"' "$journal")
[[ $gate_record == *"\"preMutationManifestSHA256\":\"${manifest_sha}\""* ]] || test::fail "Human Judgment approval lacks the pre-mutation manifest anchor"
[[ $gate_record == *"\"approvalSHA256\":\"${approval_sha}\""* ]] || test::fail "Human Judgment approval lacks the combined approval anchor"
[[ $(shasum -a 256 "$ambient_kubeconfig" | awk '{print $1}') == "$ambient_hash" ]] || test::fail "ambient kubeconfig changed"
[[ $(shasum -a 256 "$default_kubeconfig" | awk '{print $1}') == "$default_hash" ]] || test::fail "default kubeconfig changed"
gate_line=$(grep -n '^GATE' "$command_log" | cut -d: -f1)
create_line=$(grep -n '^KIND_CREATE' "$command_log" | cut -d: -f1)
[[ -n $gate_line && -n $create_line && $gate_line -lt $create_line ]] || test::fail "cluster mutation preceded the Human Judgment Gate"
grep -Fq $'\t--retain' "$command_log" || test::fail "Kind failure retention is not enabled"
test::pass "mocked lifecycle binds authority, journals approval, and verifies audit output"

test::assert_not_found 'kind[[:space:]]+delete|kubectl[[:space:]]+config[[:space:]]+(use-context|set-context)|--approve' bootstrap/drill
test::assert_not_found 'TMPDIR' bootstrap/drill/lock.sh
test::assert_not_found 'atlas-kind-drill|bootstrap/drill' bootstrap/recovery
test::assert_not_found 'certificatesigningrequests|ClusterRoleBinding|RoleBinding|ValidatingAdmissionPolicy|atlas-adoption-(signal|receipt)' bootstrap/drill
test::pass "drill lifecycle remains separate from recovery and future authorization gates"
