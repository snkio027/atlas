#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-kind-drill-test.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

drill_cli=./bootstrap/drill/atlas-kind-drill
mock_bin="${test_workspace}/bin"
command_log="${test_workspace}/commands.log"
cluster_state="${test_workspace}/cluster.state"
ambient_kubeconfig="${test_workspace}/ambient.kubeconfig"
default_kubeconfig="${test_workspace}/home/.kube/config"
mkdir -m 0700 "$mock_bin" "${test_workspace}/audit" "${test_workspace}/home"
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
    cluster='' image='' kubeconfig='' retain=false
    shift 2
    while (($# > 0)); do
      case "$1" in
        --name) cluster=$2; shift 2 ;;
        --image) image=$2; shift 2 ;;
        --kubeconfig) kubeconfig=$2; shift 2 ;;
        --config | --wait) shift 2 ;;
        --retain) retain=true; shift ;;
        *) exit 2 ;;
      esac
    done
    [[ -n $cluster && $image == "$ATLAS_TEST_NODE_IMAGE" && -n $kubeconfig && $retain == true ]] || exit 92
    printf '%s\n' "$cluster" > "$ATLAS_TEST_CLUSTER_STATE"
    printf 'apiVersion: v1\nkind: Config\ncurrent-context: kind-%s\n' "$cluster" > "$kubeconfig"
    chmod 0600 "$kubeconfig"
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
      printf '%s\tfalse\n' "$ATLAS_TEST_POLICY"
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
    printf '%s  %s\n' "$ATLAS_TEST_POLICY_SHA" "${4:-}"
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
    printf 'ok\n'
    ;;
  *) exit 2 ;;
esac
EOF

chmod 0755 "${mock_bin}/docker" "${mock_bin}/kind" "${mock_bin}/kubectl" "${mock_bin}/uname"

export PATH="${mock_bin}:${PATH}"
export HOME="${test_workspace}/home"
export KUBECONFIG=$ambient_kubeconfig
export ATLAS_TEST_COMMAND_LOG=$command_log
export ATLAS_TEST_CLUSTER_STATE=$cluster_state
export ATLAS_TEST_KIND_VERSION=$locked_kind
export ATLAS_TEST_KUBECTL_VERSION=$locked_kubectl
export ATLAS_TEST_NODE_IMAGE=$locked_image
export ATLAS_TEST_POLICY="${ATLAS_TEST_ROOT}/clusters/kind/recovery-audit-policy.yaml"
export ATLAS_TEST_POLICY_SHA
ATLAS_TEST_POLICY_SHA=$(shasum -a 256 "$ATLAS_TEST_POLICY" | awk '{print $1}')

"$drill_cli" --help > /dev/null
"$drill_cli" --version | grep -Eq '^atlas-kind-drill [0-9]+\.[0-9]+\.[0-9]+$'

cluster_one=atlas-recovery-drill-20260815t010203z-a1b2c3d4
context_one="kind-${cluster_one}"
mkdir -m 0700 "${test_workspace}/credentials-one" "${test_workspace}/audit/${cluster_one}"
kubeconfig_one="${test_workspace}/credentials-one/${cluster_one}.kubeconfig"

if "$drill_cli" create \
  --cluster-name invalid \
  --context kind-invalid \
  --kubeconfig "$kubeconfig_one" \
  --audit-dir "${test_workspace}/audit/${cluster_one}" > /dev/null 2>&1; then
  test::fail "an unscoped drill cluster name was accepted"
fi
if "$drill_cli" create \
  --cluster-name "$cluster_one" \
  --context developer \
  --kubeconfig "$kubeconfig_one" \
  --audit-dir "${test_workspace}/audit/${cluster_one}" > /dev/null 2>&1; then
  test::fail "a default or mismatched context was accepted"
fi
if KUBECONFIG=$kubeconfig_one "$drill_cli" create \
  --cluster-name "$cluster_one" \
  --context "$context_one" \
  --kubeconfig "$kubeconfig_one" \
  --audit-dir "${test_workspace}/audit/${cluster_one}" > /dev/null 2>&1; then
  test::fail "an ambient kubeconfig destination was accepted"
fi
test::pass "drill identity and kubeconfig isolation fail closed"

cluster_two=atlas-recovery-drill-20260815t020304z-b2c3d4e5
context_two="kind-${cluster_two}"
mkdir -m 0700 "${test_workspace}/credentials-two" "${test_workspace}/audit/${cluster_two}"
kubeconfig_two="${test_workspace}/credentials-two/${cluster_two}.kubeconfig"
: > "$command_log"
ATLAS_TEST_EXISTING_CLUSTER=$cluster_two \
  ATLAS_TEST_KUBECONFIG=$kubeconfig_two \
  ATLAS_TEST_CONTEXT=$context_two \
  ATLAS_TEST_AUDIT_DIR="${test_workspace}/audit/${cluster_two}" \
  "$drill_cli" create \
  --cluster-name "$cluster_two" \
  --context "$context_two" \
  --kubeconfig "$kubeconfig_two" \
  --audit-dir "${test_workspace}/audit/${cluster_two}" > /dev/null 2>&1 && test::fail "an existing drill cluster was reused"
if grep -Fq 'KIND_CREATE' "$command_log"; then
  test::fail "existing-cluster rejection reached Kind creation"
fi
test::pass "existing clusters fail before approval or mutation"

cluster_three=atlas-recovery-drill-20260815t030405z-c3d4e5f6
context_three="kind-${cluster_three}"
mkdir -m 0700 "${test_workspace}/credentials-three" "${test_workspace}/audit/${cluster_three}"
kubeconfig_three="${test_workspace}/credentials-three/${cluster_three}.kubeconfig"
: > "$command_log"
ATLAS_TEST_KUBECONFIG=$kubeconfig_three \
  ATLAS_TEST_CONTEXT=$context_three \
  ATLAS_TEST_AUDIT_DIR="${test_workspace}/audit/${cluster_three}" \
  "$drill_cli" create \
  --cluster-name "$cluster_three" \
  --context "$context_three" \
  --kubeconfig "$kubeconfig_three" \
  --audit-dir "${test_workspace}/audit/${cluster_three}" > /dev/null 2>&1 && test::fail "non-interactive creation bypassed the Human Judgment Gate"
if grep -Fq 'KIND_CREATE' "$command_log"; then
  test::fail "Human Judgment rejection reached Kind creation"
fi
test::pass "cluster creation has no non-interactive approval bypass"

tampered_config="${test_workspace}/tampered-kind.yaml"
"$ATLAS_TEST_ROOT/bootstrap/recovery/atlas-recovery" phase0 audit-config \
  --audit-dir "${test_workspace}/audit/${cluster_one}" |
  sed '/name: audit-policy-file/d' > "$tampered_config"
if ATLAS_TAMPERED_CLUSTER=$cluster_one \
  ATLAS_TAMPERED_CONTEXT=$context_one \
  ATLAS_TAMPERED_KUBECONFIG=$kubeconfig_one \
  ATLAS_TAMPERED_AUDIT="${test_workspace}/audit/${cluster_one}" \
  ATLAS_TAMPERED_CONFIG=$tampered_config \
  ATLAS_DRILL_ROOT_DIR=$ATLAS_TEST_ROOT \
  bash -c '
    set -Eeuo pipefail
    drill::die() { return 1; }
    source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/contract.sh"
    drill::resolve_target "$ATLAS_TAMPERED_CLUSTER" "$ATLAS_TAMPERED_CONTEXT" "$ATLAS_TAMPERED_KUBECONFIG" "$ATLAS_TAMPERED_AUDIT"
    drill::validate_kind_config "$ATLAS_TAMPERED_CONFIG"
  ' > /dev/null 2>&1; then
  test::fail "a Kind config missing an audit argument was accepted"
fi
test::pass "rendered audit mounts and API server arguments are prevalidated"

cluster_four=atlas-recovery-drill-20260815t040506z-d4e5f6a7
context_four="kind-${cluster_four}"
audit_four="${test_workspace}/audit/${cluster_four}"
kubeconfig_four="${test_workspace}/credentials-four/${cluster_four}.kubeconfig"
mkdir -m 0700 "${test_workspace}/credentials-four" "$audit_four"
: > "$command_log"
export ATLAS_TEST_KUBECONFIG
ATLAS_TEST_KUBECONFIG="$(cd "$(dirname "$kubeconfig_four")" && pwd -P)/$(basename "$kubeconfig_four")"
export ATLAS_TEST_CONTEXT=$context_four
export ATLAS_TEST_AUDIT_DIR
ATLAS_TEST_AUDIT_DIR=$(cd "$audit_four" && pwd -P)

ATLAS_DRILL_ROOT_DIR=$ATLAS_TEST_ROOT
readonly ATLAS_DRILL_ROOT_DIR
# shellcheck source=bootstrap/drill/contract.sh
source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/contract.sh"
# shellcheck source=bootstrap/drill/lifecycle.sh
source "$ATLAS_DRILL_ROOT_DIR/bootstrap/drill/lifecycle.sh"
drill::die() {
  printf 'drill-test: %s\n' "$*" >&2
  return 1
}
drill::_human_gate() {
  printf 'GATE\t%s\n' "$1" >> "$ATLAS_TEST_COMMAND_LOG"
}

drill::create_cluster "$cluster_four" "$context_four" "$kubeconfig_four" "$audit_four" > /dev/null
[[ -s $kubeconfig_four && $(stat -f '%Lp' "$kubeconfig_four") == 600 ]] || test::fail "isolated kubeconfig was not created with mode 0600"
[[ $(shasum -a 256 "$ambient_kubeconfig" | awk '{print $1}') == "$ambient_hash" ]] || test::fail "ambient kubeconfig changed"
[[ $(shasum -a 256 "$default_kubeconfig" | awk '{print $1}') == "$default_hash" ]] || test::fail "default kubeconfig changed"
[[ -s ${audit_four}/kube-apiserver-audit.log ]] || test::fail "API audit output was not verified"
gate_line=$(grep -n '^GATE' "$command_log" | cut -d: -f1)
create_line=$(grep -n '^KIND_CREATE' "$command_log" | cut -d: -f1)
[[ -n $gate_line && -n $create_line && $gate_line -lt $create_line ]] || test::fail "cluster mutation preceded the Human Judgment Gate"
grep -Fq $'KIND_CREATE\tcreate\tcluster' "$command_log" || test::fail "mocked Kind creation was not exercised"
grep -Fq $'\t--retain' "$command_log" || test::fail "Kind failure retention is not enabled"
test::pass "mocked lifecycle verifies explicit context, locked image, audit output, and gate order"

test::assert_not_found 'kind[[:space:]]+delete|kubectl[[:space:]]+config[[:space:]]+(use-context|set-context)|--approve' bootstrap/drill
test::assert_not_found 'atlas-kind-drill|bootstrap/drill' bootstrap/recovery
test::assert_not_found 'certificatesigningrequests|ClusterRoleBinding|RoleBinding|ValidatingAdmissionPolicy|atlas-adoption-(signal|receipt)' bootstrap/drill
test::pass "drill lifecycle is physically separate from recovery and future authorization gates"
