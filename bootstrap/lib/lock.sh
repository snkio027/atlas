# shellcheck shell=bash

[[ -n ${_ATLAS_LOCK_LOADED:-} ]] && return 0
readonly _ATLAS_LOCK_LOADED=1

ATLAS_LOCK_DIR=
ATLAS_LOCK_DIR_IDENTITY=
ATLAS_LOCK_OWNER_FILE=
ATLAS_LOCK_OWNER_IDENTITY=
ATLAS_LOCK_OWNER_RECORD=
ATLAS_LOCK_STATE_DIR=
ATLAS_LOCK_STATE_IDENTITY=

lock::_clear_binding() {
  ATLAS_LOCK_DIR=
  ATLAS_LOCK_DIR_IDENTITY=
  ATLAS_LOCK_OWNER_FILE=
  ATLAS_LOCK_OWNER_IDENTITY=
  ATLAS_LOCK_OWNER_RECORD=
  ATLAS_LOCK_STATE_DIR=
  ATLAS_LOCK_STATE_IDENTITY=
}

lock::_assert_identity() {
  local path=$1 expected=$2 label=$3 actual
  actual=$(runtime::path_identity "$path") || {
    runtime::die "unable to inspect ${label} identity: ${path}"
    return 1
  }
  [[ $actual == "$expected" ]] || {
    runtime::die "${label} identity changed: ${path}"
    return 1
  }
}

lock::_create_owner_file() {
  local owner_file=$1 token
  token=$(printf '%s:%s:%s:%s\n' "$$" "$BASHPID" "$RANDOM" "$(date +%s)" | shasum -a 256 | awk '{print $1}') || return 1
  [[ $token =~ ^[0-9a-f]{64}$ ]] || return 1
  printf '%s\t%s\n' "$$" "$token" > "$owner_file" || return 1
  chmod 0600 "$owner_file" || return 1
  runtime::assert_private_file "$owner_file" "bootstrap lock owner file"
}

lock::_owner_record() {
  local owner_file=$1 record size
  IFS= read -r record < "$owner_file" || {
    runtime::die "bootstrap lock has an incomplete owner record: ${owner_file}"
    return 1
  }
  size=$(wc -c < "$owner_file") || return 1
  size=${size//[[:space:]]/}
  [[ $size == $((${#record} + 1)) ]] || {
    runtime::die "bootstrap lock has an invalid owner record: ${owner_file}"
    return 1
  }
  if [[ $record =~ ^[1-9][0-9]*$ ]]; then
    printf '%s\n' "$record"
    return 0
  fi
  local token_pattern='^[1-9][0-9]*'$'\t''[0-9a-f]{64}$'
  [[ $record =~ $token_pattern ]] || {
    runtime::die "bootstrap lock has an invalid owner record: ${owner_file}"
    return 1
  }
  printf '%s\n' "$record"
}

lock::_contains_only_owner() (
  local lock_dir=$1 owner_file=$2
  local -a entries
  shopt -s dotglob nullglob
  entries=("${lock_dir}"/*)
  ((${#entries[@]} == 1)) && [[ ${entries[0]} == "$owner_file" ]]
)

lock::_bind() {
  local state_dir=$1 lock_dir=$2 owner_file=$3
  local lock_identity owner_identity owner_pid owner_record state_identity
  runtime::assert_private_directory "$state_dir" "Bootstrap state directory" || return 1
  runtime::assert_private_directory "$lock_dir" "bootstrap lock directory" || return 1
  runtime::assert_private_file "$owner_file" "bootstrap lock owner file" || return 1
  owner_record=$(lock::_owner_record "$owner_file") || return 1
  [[ $owner_record == *$'\t'* ]] || {
    runtime::die "new bootstrap lock owner record has no custody token: ${owner_file}"
    return 1
  }
  owner_pid=${owner_record%%$'\t'*}
  [[ $owner_pid == "$$" ]] || {
    runtime::die "bootstrap lock owner changed before binding: ${lock_dir}"
    return 1
  }

  state_identity=$(runtime::path_identity "$state_dir") || return 1
  lock_identity=$(runtime::path_identity "$lock_dir") || return 1
  owner_identity=$(runtime::path_identity "$owner_file") || return 1
  ATLAS_LOCK_STATE_DIR=$state_dir
  ATLAS_LOCK_STATE_IDENTITY=$state_identity
  ATLAS_LOCK_DIR=$lock_dir
  ATLAS_LOCK_DIR_IDENTITY=$lock_identity
  ATLAS_LOCK_OWNER_FILE=$owner_file
  ATLAS_LOCK_OWNER_IDENTITY=$owner_identity
  ATLAS_LOCK_OWNER_RECORD=$owner_record
}

lock::assert_held() {
  local owner_pid owner_record
  [[ -n ${ATLAS_LOCK_DIR:-} && -n ${ATLAS_LOCK_STATE_DIR:-} && -n ${ATLAS_LOCK_OWNER_FILE:-} ]] || {
    runtime::die "bootstrap lifecycle lock is not held"
    return 1
  }
  runtime::assert_private_directory "$ATLAS_LOCK_STATE_DIR" "Bootstrap state directory" || return 1
  lock::_assert_identity "$ATLAS_LOCK_STATE_DIR" "$ATLAS_LOCK_STATE_IDENTITY" "Bootstrap state directory" || return 1
  runtime::assert_private_directory "$ATLAS_LOCK_DIR" "bootstrap lock directory" || return 1
  lock::_assert_identity "$ATLAS_LOCK_DIR" "$ATLAS_LOCK_DIR_IDENTITY" "bootstrap lock directory" || return 1
  runtime::assert_private_file "$ATLAS_LOCK_OWNER_FILE" "bootstrap lock owner file" || return 1
  lock::_assert_identity "$ATLAS_LOCK_OWNER_FILE" "$ATLAS_LOCK_OWNER_IDENTITY" "bootstrap lock owner file" || return 1
  owner_record=$(lock::_owner_record "$ATLAS_LOCK_OWNER_FILE") || return 1
  [[ $owner_record == "$ATLAS_LOCK_OWNER_RECORD" ]] || {
    runtime::die "bootstrap lock owner record changed: ${ATLAS_LOCK_DIR}"
    return 1
  }
  owner_pid=${owner_record%%$'\t'*}
  [[ $owner_pid == "$$" ]] || {
    runtime::die "bootstrap lock owner changed: ${ATLAS_LOCK_DIR}"
    return 1
  }
}

lock::acquire() {
  local state_dir=$1 repository_root=$2
  local lock_dir="${state_dir}/bootstrap.lock"
  local owner_file="${lock_dir}/pid"
  local lock_identity owner_identity owner_pid owner_record state_identity

  [[ -z ${ATLAS_LOCK_DIR:-} ]] || {
    runtime::die "bootstrap lifecycle lock is already held: ${ATLAS_LOCK_DIR}"
    return 1
  }
  [[ $repository_root == /* && $state_dir == "${repository_root}/.state" ]] || {
    runtime::die "Bootstrap state directory is outside the repository authority: ${state_dir}"
    return 1
  }
  [[ $(runtime::_canonical_directory "$repository_root") == "$repository_root" ]] || {
    runtime::die "repository root is not canonical: ${repository_root}"
    return 1
  }

  runtime::ensure_private_directory "$state_dir" "Bootstrap state directory" || return 1
  state_identity=$(runtime::path_identity "$state_dir") || return 1

  [[ ! -L $lock_dir ]] || {
    runtime::die "bootstrap lock path is a symlink: ${lock_dir}"
    return 1
  }

  if mkdir -m 0700 "$lock_dir" 2> /dev/null; then
    lock::_create_owner_file "$owner_file" || {
      runtime::die "unable to create bootstrap lock owner: ${owner_file}"
      return 1
    }
    lock::_assert_identity "$state_dir" "$state_identity" "Bootstrap state directory" || return 1
    lock::_bind "$state_dir" "$lock_dir" "$owner_file"
    return
  fi

  runtime::assert_private_directory "$lock_dir" "bootstrap lock directory" || return 1
  runtime::assert_private_file "$owner_file" "bootstrap lock owner file" || return 1
  lock_identity=$(runtime::path_identity "$lock_dir") || return 1
  owner_identity=$(runtime::path_identity "$owner_file") || return 1
  owner_record=$(lock::_owner_record "$owner_file") || return 1
  owner_pid=${owner_record%%$'\t'*}
  if kill -0 "$owner_pid" 2> /dev/null; then
    runtime::die "another bootstrap process is running: pid=${owner_pid}"
    return 1
  fi

  runtime::warn "recovering stale bootstrap lock: pid=${owner_pid}"
  lock::_assert_identity "$state_dir" "$state_identity" "Bootstrap state directory" || return 1
  lock::_assert_identity "$lock_dir" "$lock_identity" "bootstrap lock directory" || return 1
  lock::_assert_identity "$owner_file" "$owner_identity" "bootstrap lock owner file" || return 1
  lock::_contains_only_owner "$lock_dir" "$owner_file" || {
    runtime::die "bootstrap lock contains unexpected files: ${lock_dir}"
    return 1
  }
  rm "$owner_file" || return 1
  rmdir "$lock_dir" || {
    runtime::die "bootstrap lock contains unexpected files: ${lock_dir}"
    return 1
  }
  lock::_assert_identity "$state_dir" "$state_identity" "Bootstrap state directory" || return 1
  mkdir -m 0700 "$lock_dir" || return 1
  lock::_create_owner_file "$owner_file" || return 1
  lock::_bind "$state_dir" "$lock_dir" "$owner_file"
}

lock::release() {
  [[ -n ${ATLAS_LOCK_DIR:-} ]] || return 0
  lock::assert_held || return 1
  lock::_contains_only_owner "$ATLAS_LOCK_DIR" "$ATLAS_LOCK_OWNER_FILE" || {
    runtime::die "bootstrap lock contains unexpected files: ${ATLAS_LOCK_DIR}"
    return 1
  }
  rm "$ATLAS_LOCK_OWNER_FILE" || return 1
  rmdir "$ATLAS_LOCK_DIR" || {
    runtime::die "bootstrap lock contains unexpected files: ${ATLAS_LOCK_DIR}"
    return 1
  }
  lock::_assert_identity "$ATLAS_LOCK_STATE_DIR" "$ATLAS_LOCK_STATE_IDENTITY" "Bootstrap state directory" || return 1
  lock::_clear_binding
}
