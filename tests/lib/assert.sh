# shellcheck shell=bash

[[ -n ${_ATLAS_TEST_ASSERT_LOADED:-} ]] && return 0
readonly _ATLAS_TEST_ASSERT_LOADED=1

ATLAS_TEST_ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
# shellcheck disable=SC2034 # Used by scripts that source this library.
readonly ATLAS_TEST_ROOT

test::fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

test::pass() {
  printf 'PASS: %s\n' "$*"
}

test::assert_not_found() {
  local pattern=$1
  shift
  if rg --line-number "$pattern" "$@"; then
    test::fail "forbidden pattern found: ${pattern}"
  fi
}
