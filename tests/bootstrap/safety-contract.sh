#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-safety-test.XXXXXX")
lock_dir=.state/bootstrap.lock
cleanup() {
  rm -f "${lock_dir}/pid"
  rmdir "$lock_dir" 2> /dev/null || true
  rm -rf "$test_workspace"
}
trap cleanup EXIT

if approval_output=$(./bootstrap/atlas apply 2>&1); then
  test::fail "apply succeeded without Tier-0 approval"
fi
grep -Fq 'Tier-0 approval is required' <<< "$approval_output" || test::fail "apply did not fail at the Tier-0 gate"
test::pass "Tier-0 approval precedes configuration and mutation"

mkdir -p .state
mkdir -m 0700 "$lock_dir"
printf '%s\n' 99999999 > "${lock_dir}/pid"
if recovery_output=$(./bootstrap/atlas apply 2>&1); then
  test::fail "interrupted recovery bypassed Tier-0 approval"
fi
grep -Fq 'Tier-0 approval is required' <<< "$recovery_output" || test::fail "interrupted recovery did not stop at the Tier-0 gate"
[[ -f ${lock_dir}/pid ]] || test::fail "unapproved apply mutated the stale lifecycle lock"
rm -f "${lock_dir}/pid"
rmdir "$lock_dir"
test::pass "interrupted recovery cannot bypass Tier-0 approval"

mkdir -m 0700 "$lock_dir"
printf '%s\n' "$$" > "${lock_dir}/pid"
if lock_output=$(./bootstrap/atlas render 2>&1); then
  test::fail "render ignored a live lifecycle lock"
fi
grep -Fq 'another bootstrap process is running' <<< "$lock_output" || test::fail "concurrent execution did not fail closed"
rm -f "${lock_dir}/pid"
rmdir "$lock_dir"
test::pass "concurrent Bootstrap execution fails closed"

seed_decision() {
  local state=$1
  ATLAS_TEST_ADOPTION_STATE=$state bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    source bootstrap/argocd/handoff.sh
    argocd::_adoption_state() {
      [[ $ATLAS_TEST_ADOPTION_STATE != ERROR ]] || return 1
      printf "%s\n" "$ATLAS_TEST_ADOPTION_STATE"
    }
    argocd::install_seed() {
      printf "SEED_APPLIED\n"
    }
    argocd::_ensure_seed_authority
  '
}

adoption_state() {
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
    argocd::_adoption_state
  '
}

[[ $(adoption_state $'argocd-self\tSynced/Healthy') == ADOPTED ]] || test::fail "Healthy argocd-self was not classified as adopted"
[[ $(adoption_state $'argocd-self\t/') == PRESENT:/ ]] || test::fail "argocd-self without status was classified as absent"
[[ $(adoption_state '') == ABSENT ]] || test::fail "missing argocd-self was not classified as absent"

absent_output=$(seed_decision ABSENT 2>&1)
[[ $(grep -Fc SEED_APPLIED <<< "$absent_output") == 1 ]] || test::fail "Bootstrap did not install the initial Seed exactly once"

adopted_output=$(seed_decision ADOPTED 2>&1)
! grep -Fq SEED_APPLIED <<< "$adopted_output" || test::fail "Bootstrap reapplied Seed after adoption"

if unhealthy_output=$(seed_decision PRESENT:OutOfSync/Degraded 2>&1); then
  test::fail "Bootstrap reclaimed Seed authority from an unhealthy argocd-self"
fi
grep -Fq 'will not resume Seed authority' <<< "$unhealthy_output" || test::fail "unhealthy adoption did not fail with the control-boundary error"
! grep -Fq SEED_APPLIED <<< "$unhealthy_output" || test::fail "unhealthy adoption applied Seed"

if inspection_output=$(seed_decision ERROR 2>&1); then
  test::fail "Bootstrap continued when adoption inspection failed"
fi
grep -Fq 'unable to inspect argocd-self adoption state' <<< "$inspection_output" || test::fail "adoption inspection error was not reported"
test::pass "Seed authority is initial-only and terminates after argocd-self exists"
