# shellcheck shell=bash

[[ -n ${_ATLAS_DRILL_LOCK_LOADED:-} ]] && return 0
readonly _ATLAS_DRILL_LOCK_LOADED=1

ATLAS_DRILL_LOCK_PATH=''
ATLAS_DRILL_LOCK_TOKEN=''

drill::_lock_root() {
  local temporary_root lock_root
  [[ -n ${TMPDIR:-} ]] || {
    drill::die "TMPDIR is required for the host lifecycle lock"
    return 1
  }
  temporary_root=${TMPDIR%/}
  drill::assert_managed_directory "$temporary_root" "TMPDIR" || return 1
  lock_root="${temporary_root}/atlas-kind-drill-locks"
  if mkdir -m 0700 "$lock_root" 2> /dev/null; then
    :
  else
    drill::assert_managed_directory "$lock_root" "drill lock root" || return 1
  fi
  printf '%s\n' "$lock_root"
}

drill::acquire_lifecycle_lock() {
  local lock_root cluster owner_file started
  [[ -z $ATLAS_DRILL_LOCK_PATH ]] || {
    drill::die "a lifecycle lock is already held by this process"
    return 1
  }
  lock_root=$(drill::_lock_root) || return 1
  cluster=$(drill::target cluster_name) || return 1
  ATLAS_DRILL_LOCK_PATH="${lock_root}/${cluster}.lock"
  mkdir -m 0700 "$ATLAS_DRILL_LOCK_PATH" 2> /dev/null || {
    drill::die "drill lifecycle lock already exists; stale locks require human review"
    ATLAS_DRILL_LOCK_PATH=''
    return 1
  }
  started=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  ATLAS_DRILL_LOCK_TOKEN="${cluster}:${BASHPID}:${RANDOM}${RANDOM}"
  owner_file="${ATLAS_DRILL_LOCK_PATH}/owner"
  printf 'token=%s\npid=%s\nstartedAt=%s\n' \
    "$ATLAS_DRILL_LOCK_TOKEN" "$BASHPID" "$started" > "$owner_file" || return 1
  chmod 0600 "$owner_file" || return 1
  drill::assert_managed_directory "$ATLAS_DRILL_LOCK_PATH" "lifecycle lock" || return 1
  drill::assert_managed_file "$owner_file" 600 "lifecycle lock owner" || return 1
}

drill::release_lifecycle_lock() {
  local owner_file expected actual
  [[ -n $ATLAS_DRILL_LOCK_PATH && -n $ATLAS_DRILL_LOCK_TOKEN ]] || return 0
  owner_file="${ATLAS_DRILL_LOCK_PATH}/owner"
  drill::assert_managed_directory "$ATLAS_DRILL_LOCK_PATH" "lifecycle lock" || return 1
  drill::assert_managed_file "$owner_file" 600 "lifecycle lock owner" || return 1
  expected="token=${ATLAS_DRILL_LOCK_TOKEN}"
  actual=$(sed -n '1p' "$owner_file") || return 1
  [[ $actual == "$expected" ]] || {
    drill::die "lifecycle lock ownership changed; refusing release"
    return 1
  }
  rm -f -- "$owner_file" || return 1
  rmdir "$ATLAS_DRILL_LOCK_PATH" || return 1
  ATLAS_DRILL_LOCK_PATH=''
  ATLAS_DRILL_LOCK_TOKEN=''
}
