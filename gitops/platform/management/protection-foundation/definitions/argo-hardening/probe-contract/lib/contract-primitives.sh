#!/usr/bin/env bash

# Deterministic, authority-neutral primitives shared by the live PERSONAL_LOCAL
# v2 executables. Callers must verify this file against their approved Git blob
# before sourcing it.

# shellcheck disable=SC2034 # Each executor verifies this marker after sourcing.
readonly ATLAS_CONTRACT_PRIMITIVES_LOADED=1

contract_primitives::sha256_text() {
  shasum -a 256 | awk '{print $1}'
}

contract_primitives::sha256_file() {
  shasum -a 256 "$1" | awk '{print $1}'
}

contract_primitives::canonical_json() {
  yq -o=json -I=0 'sort_keys(..)' "$1" 2> /dev/null
}

contract_primitives::canonical_sha() {
  local projection
  projection=$(contract_primitives::canonical_json "$1") || return 1
  printf '%s' "$projection" | contract_primitives::sha256_text
}

contract_primitives::document_is_canonical() {
  local document=$1 projection
  projection=$(contract_primitives::canonical_json "$document") || return 1
  cmp -s "$document" <(printf '%s' "$projection")
}

contract_primitives::document_keys() {
  yq -r 'keys | .[]' "$1" | sort
}

contract_primitives::schema_keys() {
  yq -r '.required[]' "$1" | sort
}

contract_primitives::assert_exact_keys() {
  [[ $(contract_primitives::document_keys "$1") == "$(contract_primitives::schema_keys "$2")" ]]
}

contract_primitives::has_tag() {
  local document=$1 expression=$2 expected_tag=$3
  yq -e "(${expression} | tag) == \"${expected_tag}\"" "$document" > /dev/null 2>&1
}

contract_primitives::assert_tags() {
  local document=$1 expected_tag=$2 expression
  shift 2
  for expression in "$@"; do
    contract_primitives::has_tag "$document" "$expression" "$expected_tag" || return 1
  done
}

contract_primitives::path_uid() {
  local value
  if value=$(stat -f '%u' "$1" 2> /dev/null); then
    printf '%s\n' "$value"
  else
    stat -c '%u' "$1"
  fi
}

contract_primitives::path_mode() {
  local value
  if value=$(stat -f '%Lp' "$1" 2> /dev/null); then
    printf '%s\n' "$value"
  else
    stat -c '%a' "$1"
  fi
}

contract_primitives::path_identity() {
  local value
  if value=$(stat -f '%d:%i:%u:%Lp:%z' "$1" 2> /dev/null); then
    printf '%s\n' "$value"
  else
    stat -c '%d:%i:%u:%a:%s' "$1"
  fi
}

contract_primitives::canonical_directory() {
  (cd "$1" 2> /dev/null && pwd -P)
}

contract_primitives::assert_owner_directory() {
  local path=$1 expected_mode=$2 canonical mode
  [[ $path == /* && -d $path && ! -L $path ]] || return 1
  canonical=$(contract_primitives::canonical_directory "$path") || return 1
  [[ $canonical == "$path" && $(contract_primitives::path_uid "$path") == "$(id -u)" ]] || return 1
  mode=$(contract_primitives::path_mode "$path") || return 1
  [[ $mode == "$expected_mode" ]]
}

contract_primitives::assert_private_file() {
  local path=$1 require_owner=$2 mode mode_value parent canonical_parent
  [[ $path == /* && -f $path && ! -L $path ]] || return 1
  parent=$(dirname "$path")
  canonical_parent=$(contract_primitives::canonical_directory "$parent") || return 1
  [[ "${canonical_parent}/$(basename "$path")" == "$path" ]] || return 1
  if [[ $require_owner == true ]]; then
    [[ $(contract_primitives::path_uid "$path") == "$(id -u)" ]] || return 1
  fi
  mode=$(contract_primitives::path_mode "$path") || return 1
  [[ $mode =~ ^[0-7]{3,4}$ ]] || return 1
  mode_value=$((8#$mode))
  (((mode_value & 0077) == 0))
}

contract_primitives::assert_executable_file() {
  local path=$1 mode mode_value parent canonical_parent
  [[ $path == /* && -f $path && ! -L $path && -x $path ]] || return 1
  parent=$(dirname "$path")
  canonical_parent=$(contract_primitives::canonical_directory "$parent") || return 1
  [[ "${canonical_parent}/$(basename "$path")" == "$path" ]] || return 1
  mode=$(contract_primitives::path_mode "$path") || return 1
  [[ $mode =~ ^[0-7]{3,4}$ ]] || return 1
  mode_value=$((8#$mode))
  (((mode_value & 0022) == 0))
}

contract_primitives::assert_output_destination() {
  local output=$1 parent canonical_parent
  [[ $output == /* && ! -e $output && ! -L $output ]] || return 1
  parent=$(dirname "$output")
  contract_primitives::assert_owner_directory "$parent" 700 || return 1
  canonical_parent=$(contract_primitives::canonical_directory "$parent") || return 1
  [[ "${canonical_parent}/$(basename "$output")" == "$output" ]]
}

contract_primitives::git() {
  env -i PATH="$PATH" LC_ALL=C git --no-replace-objects \
    -c core.fsmonitor=false -c core.ignoreStat=false "$@"
}

contract_primitives::repository_root() {
  local script_dir=$1 root expected_dir
  root=$(contract_primitives::git -C "$script_dir" rev-parse --show-toplevel) || return 1
  expected_dir=${root}/gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
  [[ $script_dir == "$expected_dir" ]] || return 1
  printf '%s\n' "$root"
}

contract_primitives::assert_no_ambient_authority() {
  local variable
  [[ -z ${KUBECONFIG+x} ]] || return 1
  while IFS= read -r variable; do
    [[ $variable != ARGOCD_* ]] || return 1
  done < <(compgen -A variable)
}

contract_primitives::capture() {
  local temporary_directory=$1 output_name=$2
  shift 2
  local stderr_file=${temporary_directory}/command.stderr contract_capture_value status
  : > "$stderr_file" || return 1
  if contract_capture_value=$("$@" 2> "$stderr_file"); then
    status=0
  else
    status=$?
  fi
  ((status == 0)) || return 1
  [[ ! -s $stderr_file ]] || return 1
  printf -v "$output_name" '%s' "$contract_capture_value"
}

contract_primitives::locked_version() {
  local versions_file=$1 key=$2 count value
  [[ -f $versions_file && ! -L $versions_file ]] || return 1
  count=$(awk -F= -v key="$key" '$1 == key {count++} END {print count+0}' "$versions_file") || return 1
  [[ $count -eq 1 ]] || return 1
  value=$(awk -F= -v key="$key" '$1 == key {print substr($0, index($0, "=") + 1)}' "$versions_file") || return 1
  [[ $value =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  printf '%s\n' "$value"
}

contract_primitives::atomic_create() {
  local source=$1 output=$2 staged
  contract_primitives::assert_output_destination "$output" || return 1
  staged=$(mktemp "${output}.tmp.XXXXXX") || return 1
  chmod 0600 "$staged" || return 1
  cp "$source" "$staged" || return 1
  [[ $(contract_primitives::sha256_file "$staged") == "$(contract_primitives::sha256_file "$source")" ]] || return 1
  ln "$staged" "$output" || return 1
  rm -f "$staged" || :
  return 0
}
