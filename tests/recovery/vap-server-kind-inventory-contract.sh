#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
# shellcheck source=tests/recovery/vap-server-kind-inventory.sh
source "$(dirname "${BASH_SOURCE[0]}")/vap-server-kind-inventory.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-vap-server-inventory.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

mock_kind_inventory_failure() {
  [[ $# -eq 3 && $1 == get && $2 == clusters && $3 == --quiet ]] ||
    test::fail "inventory helper invoked an unexpected Kind command"
  printf 'simulated Kind inventory failure\n' >&2
  return 42
}

assert_inventory_failure_closed() {
  local check=$1 stage=$2
  local stdout_file="${test_workspace}/${stage}.stdout"
  local stderr_file="${test_workspace}/${stage}.stderr"

  if "$check" mock_kind_inventory_failure atlas-vap-ci-contract \
    > "$stdout_file" 2> "$stderr_file"; then
    test::fail "Kind inventory failure was accepted during ${stage}"
  fi
  [[ ! -s $stdout_file ]] || test::fail "inventory failure emitted a successful cluster snapshot"
  grep -Fqx "unable to enumerate Kind clusters during ${stage}" "$stderr_file" ||
    test::fail "inventory failure did not identify the ${stage} boundary"
}

assert_inventory_failure_closed \
  vap_server_inventory::creation_target_absent creation-preflight
assert_inventory_failure_closed \
  vap_server_inventory::deleted_target_absent post-delete

test::pass "Kind inventory failures close both VAP server lifecycle boundaries"
