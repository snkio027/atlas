# shellcheck shell=bash

[[ -n ${_ATLAS_LOCK_LOADED:-} ]] && return 0
readonly _ATLAS_LOCK_LOADED=1

ATLAS_LOCK_DIR=

lock::acquire() {
  local state_dir=$1
  local lock_dir="${state_dir}/bootstrap.lock"
  local owner_file="${lock_dir}/pid"
  local owner_pid

  mkdir -p "$state_dir"
  [[ ! -L $lock_dir ]] || {
    runtime::die "bootstrap lock path is a symlink: ${lock_dir}"
    return 1
  }

  if mkdir -m 0700 "$lock_dir" 2> /dev/null; then
    printf '%s\n' "$$" > "$owner_file"
    chmod 0600 "$owner_file"
    ATLAS_LOCK_DIR=$lock_dir
    return 0
  fi

  [[ -f $owner_file && ! -L $owner_file ]] || {
    runtime::die "bootstrap lock is not recoverable: ${lock_dir}"
    return 1
  }
  read -r owner_pid < "$owner_file"
  [[ $owner_pid =~ ^[1-9][0-9]*$ ]] || {
    runtime::die "bootstrap lock has an invalid owner: ${lock_dir}"
    return 1
  }
  if kill -0 "$owner_pid" 2> /dev/null; then
    runtime::die "another bootstrap process is running: pid=${owner_pid}"
    return 1
  fi

  runtime::warn "recovering stale bootstrap lock: pid=${owner_pid}"
  rm -f "$owner_file"
  rmdir "$lock_dir" || {
    runtime::die "bootstrap lock contains unexpected files: ${lock_dir}"
    return 1
  }
  mkdir -m 0700 "$lock_dir"
  printf '%s\n' "$$" > "$owner_file"
  chmod 0600 "$owner_file"
  ATLAS_LOCK_DIR=$lock_dir
}

lock::release() {
  [[ -n ${ATLAS_LOCK_DIR:-} ]] || return 0
  [[ -d $ATLAS_LOCK_DIR && ! -L $ATLAS_LOCK_DIR ]] || return 0
  local owner_pid=
  read -r owner_pid < "${ATLAS_LOCK_DIR}/pid" 2> /dev/null || return 0
  # Never clean up a lock owned by another process, even during an EXIT trap.
  [[ $owner_pid == "$$" ]] || return 0
  rm -f "${ATLAS_LOCK_DIR}/pid"
  rmdir "$ATLAS_LOCK_DIR"
  ATLAS_LOCK_DIR=
}
