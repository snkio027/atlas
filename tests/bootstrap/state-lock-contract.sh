#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-state-lock-test.XXXXXX")
test_workspace=$(cd "$test_workspace" && pwd -P)
cleanup() {
  chmod -R u+rwX "$test_workspace" 2> /dev/null || true
  rm -rf "$test_workspace"
}
trap cleanup EXIT

lock_command() {
  local repository_root=$1 action=$2
  bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    source bootstrap/lib/lock.sh
    trap '\''status=$?; runtime::on_exit "$status" || status=$?; trap - EXIT; exit "$status"'\'' EXIT
    lock::acquire "$1/.state" "$1"
    case "$2" in
      cycle) lock::release ;;
      remove-lock)
        rm "$1/.state/bootstrap.lock/pid"
        rmdir "$1/.state/bootstrap.lock"
        lock::release
        ;;
      replace-owner)
        rm "$1/.state/bootstrap.lock/pid"
        printf "%s\n" "$$" > "$1/.state/bootstrap.lock/pid"
        chmod 0600 "$1/.state/bootstrap.lock/pid"
        lock::release
        ;;
      replace-state)
        mv "$1/.state" "$1/.state.approved"
        mkdir -m 0700 "$1/.state"
        lock::release
        ;;
      replace-lock)
        mv "$1/.state/bootstrap.lock" "$1/.state/bootstrap.lock.approved"
        mkdir -m 0700 "$1/.state/bootstrap.lock"
        printf "%s\n" "$$" > "$1/.state/bootstrap.lock/pid"
        chmod 0600 "$1/.state/bootstrap.lock/pid"
        lock::release
        ;;
      permission-drift)
        chmod 0755 "$1/.state/bootstrap.lock"
        lock::release
        ;;
      release-failure)
        touch "$1/.state/bootstrap.lock/unexpected"
        ;;
    esac
  ' _ "$repository_root" "$action"
}

new_repository_fixture() {
  local name=$1 root
  root="${test_workspace}/${name}"
  mkdir -m 0700 "$root"
  printf '%s\n' "$root"
}

atomic_capture_with_probe() {
  local destination=$1 producer_log=$2
  ATLAS_TEST_PRODUCER_LOG=$producer_log bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    producer() {
      printf "CALLED\n" > "$ATLAS_TEST_PRODUCER_LOG"
      printf "content\n"
    }
    runtime::atomic_capture "$1" producer
  ' _ "$destination"
}

state_symlink_root=$(new_repository_fixture state-symlink)
state_symlink_target="${test_workspace}/state-symlink-target"
mkdir -m 0700 "$state_symlink_target"
ln -s "$state_symlink_target" "${state_symlink_root}/.state"
if output=$(lock_command "$state_symlink_root" cycle 2>&1); then
  test::fail "lock::acquire accepted a symlink Bootstrap state directory"
fi
grep -Fq 'Bootstrap state directory must not be a symlink' <<< "$output" || test::fail "state symlink rejection was not explicit"
[[ ! -e ${state_symlink_target}/bootstrap.lock ]] || test::fail "state symlink redirected the lifecycle lock outside the repository"
test::pass "Bootstrap state directory rejects symlink redirection"

state_mode_root=$(new_repository_fixture state-mode)
mkdir -m 0755 "${state_mode_root}/.state"
if lock_command "$state_mode_root" cycle > /dev/null 2>&1; then
  test::fail "lock::acquire accepted a broadly accessible Bootstrap state directory"
fi
test::pass "Bootstrap state directory requires mode 0700"

rendered_symlink_root=$(new_repository_fixture rendered-symlink)
mkdir -m 0700 "${rendered_symlink_root}/.state"
rendered_symlink_target="${test_workspace}/rendered-symlink-target"
mkdir -m 0700 "$rendered_symlink_target"
ln -s "$rendered_symlink_target" "${rendered_symlink_root}/.state/rendered"
if bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  runtime::atomic_capture "$1/.state/rendered/output" printf "%s\n" content
' _ "$rendered_symlink_root" > /dev/null 2>&1; then
  test::fail "atomic_capture accepted a symlink rendered directory"
fi
[[ ! -e ${rendered_symlink_target}/output ]] || test::fail "rendered symlink redirected output outside the state directory"
test::pass "rendered output directory rejects symlink redirection"

rendered_mode_root=$(new_repository_fixture rendered-mode)
mkdir -m 0700 "${rendered_mode_root}/.state"
mkdir -m 0755 "${rendered_mode_root}/.state/rendered"
if bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  runtime::atomic_capture "$1/.state/rendered/output" printf "%s\n" content
' _ "$rendered_mode_root" > /dev/null 2>&1; then
  test::fail "atomic_capture accepted a broadly accessible rendered directory"
fi
test::pass "rendered output directory requires mode 0700"

destination_root=$(new_repository_fixture destination-types)
mkdir -m 0700 "${destination_root}/.state"
mkdir -m 0700 "${destination_root}/.state/rendered"
destination="${destination_root}/.state/rendered/output"
outside_directory="${test_workspace}/atomic-outside"
mkdir -m 0700 "$outside_directory"

ln -s "$outside_directory" "$destination"
producer_log="${test_workspace}/symlink-producer.log"
if atomic_capture_with_probe "$destination" "$producer_log" > /dev/null 2>&1; then
  test::fail "atomic_capture accepted a destination symlink to an external directory"
fi
[[ ! -e $producer_log ]] || test::fail "destination symlink was rejected only after content generation"
[[ -z $(find "$outside_directory" -mindepth 1 -maxdepth 1 -print -quit) ]] || test::fail "destination symlink caused an external temporary write"
rm "$destination"

mkdir "$destination"
producer_log="${test_workspace}/directory-producer.log"
if atomic_capture_with_probe "$destination" "$producer_log" > /dev/null 2>&1; then
  test::fail "atomic_capture accepted a directory destination"
fi
[[ ! -e $producer_log ]] || test::fail "directory destination was rejected only after content generation"
rmdir "$destination"

mkfifo "$destination"
producer_log="${test_workspace}/fifo-producer.log"
if atomic_capture_with_probe "$destination" "$producer_log" > /dev/null 2>&1; then
  test::fail "atomic_capture accepted a FIFO destination"
fi
[[ ! -e $producer_log ]] || test::fail "FIFO destination was rejected only after content generation"
rm "$destination"
test::pass "atomic output rejects every non-regular destination before generation"

commit_race_root=$(new_repository_fixture destination-race)
mkdir -m 0700 "${commit_race_root}/.state"
mkdir -m 0700 "${commit_race_root}/.state/rendered"
commit_race_destination="${commit_race_root}/.state/rendered/output"
commit_race_outside="${test_workspace}/commit-race-outside"
mkdir -m 0700 "$commit_race_outside"
if ATLAS_TEST_OUTSIDE=$commit_race_outside bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  replace_destination() {
    ln -s "$ATLAS_TEST_OUTSIDE" "$1"
    printf "content\n"
  }
  runtime::atomic_capture "$1" replace_destination "$1"
' _ "$commit_race_destination" > /dev/null 2>&1; then
  test::fail "atomic_capture committed after the destination became a symlink"
fi
[[ -z $(find "$commit_race_outside" -mindepth 1 -maxdepth 1 -print -quit) ]] || test::fail "commit-time destination replacement caused an external write"
[[ -z $(find "${commit_race_root}/.state/rendered" -name '.atlas.*' -print -quit) ]] || test::fail "commit-time destination rejection retained an atomic temporary file"
test::pass "atomic output revalidates its destination before commit"

acl_root=$(new_repository_fixture extended-acl)
mkdir -m 0700 "${acl_root}/directory"
printf 'content\n' > "${acl_root}/file"
chmod 0600 "${acl_root}/file"
if bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  runtime::_path_has_extended_acl() { return 0; }
  runtime::assert_private_directory "$1/directory" "ACL directory"
' _ "$acl_root" > /dev/null 2>&1; then
  test::fail "private directory custody accepted an extended ACL"
fi
if bash -Eeuo pipefail -c '
  source bootstrap/lib/runtime.sh
  runtime::_path_has_extended_acl() { return 0; }
  runtime::assert_private_file "$1/file" "ACL file"
' _ "$acl_root" > /dev/null 2>&1; then
  test::fail "private file custody accepted an extended ACL"
fi
if [[ $(uname -s) == Darwin ]]; then
  chmod +a 'everyone allow read' "${acl_root}/directory"
  if bash -Eeuo pipefail -c '
    source bootstrap/lib/runtime.sh
    runtime::assert_private_directory "$1/directory" "macOS ACL directory"
  ' _ "$acl_root" > /dev/null 2>&1; then
    test::fail "private directory custody accepted a real macOS extended ACL"
  fi
  chmod -N "${acl_root}/directory"
fi
test::pass "private custody rejects extended ACLs"

created_root=$(new_repository_fixture created-state)
lock_command "$created_root" cycle
[[ -d ${created_root}/.state ]] || test::fail "lock cycle did not create the state directory"
[[ ! -e ${created_root}/.state/bootstrap.lock ]] || test::fail "successful lock cycle retained the lifecycle lock"
test::pass "private state creation and normal lock release succeed"

missing_lock_root=$(new_repository_fixture missing-lock)
if lock_command "$missing_lock_root" remove-lock > /dev/null 2>&1; then
  test::fail "lock::release accepted deletion of the held lifecycle lock"
fi
test::pass "held lock deletion fails release closed"

replaced_owner_root=$(new_repository_fixture replaced-owner)
if lock_command "$replaced_owner_root" replace-owner > /dev/null 2>&1; then
  test::fail "lock::release accepted replacement of the owner file"
fi
[[ -f ${replaced_owner_root}/.state/bootstrap.lock/pid ]] || test::fail "release removed the replacement owner file"
test::pass "owner file identity replacement fails release closed"

replaced_state_root=$(new_repository_fixture replaced-state)
if lock_command "$replaced_state_root" replace-state > /dev/null 2>&1; then
  test::fail "lock::release accepted replacement of the bound state directory"
fi
[[ -d ${replaced_state_root}/.state.approved/bootstrap.lock ]] || test::fail "state replacement test lost the approved lock evidence"
test::pass "state directory inode replacement fails release closed"

replaced_lock_root=$(new_repository_fixture replaced-lock)
if lock_command "$replaced_lock_root" replace-lock > /dev/null 2>&1; then
  test::fail "lock::release accepted replacement of the bound lock directory"
fi
[[ -f ${replaced_lock_root}/.state/bootstrap.lock/pid ]] || test::fail "release removed files from the replacement lock directory"
test::pass "lock directory inode replacement fails release closed"

permission_drift_root=$(new_repository_fixture permission-drift)
if lock_command "$permission_drift_root" permission-drift > /dev/null 2>&1; then
  test::fail "lock::release accepted lifecycle lock permission drift"
fi
[[ -f ${permission_drift_root}/.state/bootstrap.lock/pid ]] || test::fail "release removed the owner after permission drift"
test::pass "lifecycle lock permission drift fails release closed"

release_failure_root=$(new_repository_fixture release-failure)
if output=$(lock_command "$release_failure_root" release-failure 2>&1); then
  test::fail "successful business execution swallowed lifecycle lock release failure"
fi
grep -Fq 'bootstrap lifecycle lock release failed' <<< "$output" || test::fail "release failure was not surfaced by the EXIT handler"
[[ -d ${release_failure_root}/.state/bootstrap.lock ]] || test::fail "release failure did not retain the inconsistent lock"
test::pass "successful business execution becomes nonzero when lock release fails"
