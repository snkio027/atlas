#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

# shellcheck source=bootstrap/lib/runtime.sh
source bootstrap/lib/runtime.sh

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-docker-authority.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

mock_bin="${test_workspace}/bin"
mock_home="${test_workspace}/home"
docker_log="${test_workspace}/docker.log"
kind_log="${test_workspace}/kind.log"
context_state="${test_workspace}/context"
endpoint_state="${test_workspace}/endpoint"
mkdir -p "$mock_bin" "$mock_home"
: > "$docker_log"
: > "$kind_log"
printf 'orbstack\n' > "$context_state"
printf 'unix://%s/.orbstack/run/docker.sock\n' "$mock_home" > "$endpoint_state"

cat > "${mock_bin}/docker" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s|%s|%s|%s\n' \
  "${DOCKER_CONTEXT:-}" \
  "${DOCKER_HOST:-}" \
  "${DOCKER_CONFIG:-}" \
  "$*" >> "$ATLAS_TEST_DOCKER_LOG"
case "${1:-} ${2:-}" in
  'context show') cat "$ATLAS_TEST_CONTEXT_STATE" ;;
  'context inspect') cat "$ATLAS_TEST_ENDPOINT_STATE" ;;
  'info ' | 'image inspect') exit 0 ;;
  *) exit 64 ;;
esac
EOF
cat > "${mock_bin}/kind" << 'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s|%s|%s|%s|%s|%s\n' \
  "${DOCKER_CONTEXT:-}" \
  "${KIND_EXPERIMENTAL_PROVIDER:-}" \
  "${KIND_EXPERIMENTAL_DOCKER_NETWORK:-}" \
  "${KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER:-}" \
  "${KIND_DNS_SEARCH:-}" \
  "$*" >> "$ATLAS_TEST_KIND_LOG"
case "${1:-} ${2:-}" in
  'get clusters') printf 'atlas-test\n' ;;
  'version ') printf 'kind v0.32.0 go1.24.0 darwin/arm64\n' ;;
  *) exit 64 ;;
esac
EOF
chmod 0700 "${mock_bin}/docker" "${mock_bin}/kind"

export PATH="${mock_bin}:${PATH}"
export HOME=$mock_home
export ATLAS_TEST_DOCKER_LOG=$docker_log
export ATLAS_TEST_KIND_LOG=$kind_log
export ATLAS_TEST_CONTEXT_STATE=$context_state
export ATLAS_TEST_ENDPOINT_STATE=$endpoint_state

runtime::assert_docker_authority
runtime::kind_cluster_exists atlas-test || test::fail "the bound Kind cluster was not discovered"
if runtime::kind_cluster_exists atlas-absent; then
  test::fail "an absent Kind cluster was reported present"
fi

grep -Fq 'orbstack|||context show' "$docker_log" || test::fail "Docker context discovery was not explicitly bound"
grep -Fq 'orbstack|docker||||get clusters --quiet' "$kind_log" || test::fail "Kind discovery was not bound to Docker on OrbStack"
test::pass "normal Docker and Kind discovery use the explicit OrbStack authority"

: > "$docker_log"
: > "$kind_log"
(
  export DOCKER_HOST=tcp://127.0.0.1:2375
  export DOCKER_CONFIG=/tmp/foreign-docker-config
  runtime::docker info
  export KIND_EXPERIMENTAL_DOCKER_NETWORK=foreign
  export KIND_EXPERIMENTAL_CONTAINERD_SNAPSHOTTER=1
  export KIND_DNS_SEARCH=foreign.example
  runtime::kind get clusters --quiet > /dev/null
)
grep -Fq 'orbstack|||info' "$docker_log" || test::fail "the Docker wrapper retained caller target variables"
grep -Fq 'orbstack|docker||||get clusters --quiet' "$kind_log" || test::fail "the Kind wrapper retained caller topology variables"
test::pass "command wrappers clear known target variables before execution"

assert_environment_rejected() {
  local variable_name=$1 output
  : > "$docker_log"
  : > "$kind_log"
  if output=$(env "$variable_name=foreign" bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    runtime::kind_cluster_exists atlas-test
  ' 2>&1); then
    test::fail "${variable_name} was accepted"
  fi
  grep -Fq 'inherited DOCKER_* and KIND_* environment variables are forbidden' <<< "$output" ||
    test::fail "${variable_name} did not fail at the environment authority boundary"
  [[ ! -s $docker_log && ! -s $kind_log ]] ||
    test::fail "${variable_name} reached Docker or Kind before rejection"
}

assert_environment_rejected DOCKER_HOST
assert_environment_rejected DOCKER_FUTURE_TARGET
assert_environment_rejected KIND_EXPERIMENTAL_PROVIDER
assert_environment_rejected KIND_FUTURE_PROVIDER
test::pass "inherited current and future Docker or Kind target variables fail closed"

printf 'remote\n' > "$context_state"
if runtime::assert_docker_authority > /dev/null 2>&1; then
  test::fail "a drifted effective Docker context was accepted"
fi
printf 'orbstack\n' > "$context_state"
printf 'unix:///tmp/foreign-docker.sock\n' > "$endpoint_state"
if runtime::assert_docker_authority > /dev/null 2>&1; then
  test::fail "a drifted OrbStack endpoint was accepted"
fi
printf 'unix://%s/.orbstack/run/docker.sock\n' "$mock_home" > "$endpoint_state"
test::pass "Docker context and endpoint drift fail closed"

test::assert_not_found '(^[[:space:]]*|\$\(|[|;&][[:space:]]*|\b(if|then|else|elif|while|until)[[:space:]]+|![[:space:]]+)(docker|kind)[[:space:]]' \
  bootstrap/host bootstrap/cluster bootstrap/registry
test::pass "normal host, cluster, and Registry modules use only authority wrappers"
