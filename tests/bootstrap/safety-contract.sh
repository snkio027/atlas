#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-safety-test.XXXXXX")
test_workspace=$(cd "$test_workspace" && pwd -P)
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

cli_root="${test_workspace}/cli"
cli_state_dir="${cli_root}/.state"
cli_lock_dir="${cli_state_dir}/bootstrap.lock"
lock_unit_root="${test_workspace}/lock-unit"
lock_unit_state_dir="${lock_unit_root}/.state"
lock_unit_dir="${lock_unit_state_dir}/bootstrap.lock"
mkdir -p "$cli_root"
cp -R bootstrap "${cli_root}/bootstrap"
mkdir -m 0700 "$cli_state_dir" "$cli_lock_dir" "$lock_unit_root" "$lock_unit_state_dir"
printf '%s\n' 99999999 > "${cli_lock_dir}/pid"
chmod 0600 "${cli_lock_dir}/pid"

if approval_output=$("${cli_root}/bootstrap/atlas" apply 2>&1); then
  test::fail "apply succeeded without Tier-0 approval"
fi
grep -Fq 'Tier-0 approval is required' <<< "$approval_output" || test::fail "apply did not fail at the Tier-0 gate"
[[ $(< "${cli_lock_dir}/pid") == 99999999 ]] || test::fail "unapproved apply mutated the CLI lifecycle lock"
rm -f "${cli_lock_dir}/pid"
rmdir "$cli_lock_dir"
test::pass "Tier-0 approval precedes configuration and lock recovery"

lock_cycle() {
  local isolated_root=$1
  bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    source bootstrap/lib/lock.sh
    lock::acquire "$1/.state" "$1"
    lock::release
  ' _ "$isolated_root"
}

mkdir -m 0700 "$lock_unit_dir"
printf '%s\n' "$$" > "${lock_unit_dir}/pid"
chmod 0600 "${lock_unit_dir}/pid"
if lock_output=$(lock_cycle "$lock_unit_root" 2>&1); then
  test::fail "lock::acquire ignored a live lifecycle lock"
fi
grep -Fq 'another bootstrap process is running' <<< "$lock_output" || test::fail "concurrent execution did not fail closed"
live_owner_record=$(< "${lock_unit_dir}/pid")
[[ ${live_owner_record%%$'\t'*} == "$$" ]] || test::fail "failed acquisition modified a foreign lock"
rm -f "${lock_unit_dir}/pid"
rmdir "$lock_unit_dir"
test::pass "lock::acquire preserves a live foreign lock"

stale_pid=99999999
kill -0 "$stale_pid" 2> /dev/null && test::fail "chosen stale PID is unexpectedly live"
mkdir -m 0700 "$lock_unit_dir"
printf '%s\n' "$stale_pid" > "${lock_unit_dir}/pid"
chmod 0600 "${lock_unit_dir}/pid"
stale_output=$(lock_cycle "$lock_unit_root" 2>&1)
grep -Fq "recovering stale bootstrap lock: pid=${stale_pid}" <<< "$stale_output" || test::fail "stale lock recovery was not reported"
[[ ! -e $lock_unit_dir ]] || test::fail "lock::release left the isolated recovered lock behind"
test::pass "lock::acquire and lock::release recover only isolated stale state"

seed_decision() {
  local state=$1
  ATLAS_TEST_ARGOCD_SELF_STATE=$state bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    source bootstrap/argocd/handoff.sh
    argocd::_argocd_self_state() {
      [[ $ATLAS_TEST_ARGOCD_SELF_STATE != ERROR ]] || return 1
      printf "%s\n" "$ATLAS_TEST_ARGOCD_SELF_STATE"
    }
    argocd::install_seed() {
      printf "SEED_APPLIED\n"
    }
    argocd::_ensure_seed_authority
  '
}

argocd_self_state() {
  local application_record=$1
  ATLAS_TEST_APPLICATION_RECORD=$application_record bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    source bootstrap/argocd/handoff.sh
    runtime::kubectl() {
      case "$2" in
        customresourcedefinition) printf "customresourcedefinition.apiextensions.k8s.io/applications.argoproj.io\n" ;;
        application) printf "%s" "$ATLAS_TEST_APPLICATION_RECORD" ;;
        *) return 1 ;;
      esac
    }
    argocd::_argocd_self_state
  '
}

[[ $(argocd_self_state $'argocd-self\tSynced/Healthy') == HEALTHY ]] || test::fail "Healthy argocd-self was not classified as healthy"
[[ $(argocd_self_state $'argocd-self\t/') == PRESENT:/ ]] || test::fail "argocd-self without status was classified as absent"
[[ $(argocd_self_state '') == ABSENT ]] || test::fail "missing argocd-self was not classified as absent"

absent_output=$(seed_decision ABSENT 2>&1)
[[ $(grep -Fc SEED_APPLIED <<< "$absent_output") == 1 ]] || test::fail "Bootstrap did not install the initial Seed exactly once"

healthy_output=$(seed_decision HEALTHY 2>&1)
! grep -Fq SEED_APPLIED <<< "$healthy_output" || test::fail "Bootstrap reapplied Seed after argocd-self became Healthy"

if unhealthy_output=$(seed_decision PRESENT:OutOfSync/Degraded 2>&1); then
  test::fail "Bootstrap reclaimed Seed authority from an unhealthy argocd-self"
fi
grep -Fq 'will not resume Seed authority' <<< "$unhealthy_output" || test::fail "unhealthy argocd-self did not fail with the control-boundary error"
! grep -Fq SEED_APPLIED <<< "$unhealthy_output" || test::fail "unhealthy argocd-self applied Seed"

if inspection_output=$(seed_decision ERROR 2>&1); then
  test::fail "Bootstrap continued when argocd-self health inspection failed"
fi
grep -Fq 'unable to inspect argocd-self health state' <<< "$inspection_output" || test::fail "argocd-self health inspection error was not reported"
test::pass "Seed mutation is denied while argocd-self exists"
