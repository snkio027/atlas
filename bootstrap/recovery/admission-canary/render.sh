# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_ADMISSION_CANARY_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_ADMISSION_CANARY_LOADED=1

readonly _ATLAS_RECOVERY_OPERATOR_PLACEHOLDER='__ATLAS_RECOVERY_OPERATOR_USERNAME__'

admission_canary::_definition_path() {
  local name=$1 path
  path="${ATLAS_RECOVERY_ROOT_DIR}/bootstrap/recovery/admission-canary/${name}"
  [[ -f $path && ! -L $path ]] || {
    recovery::die "admission canary definition is missing or unsafe: ${path}"
    return 1
  }
  printf '%s\n' "$path"
}

admission_canary::_render_template() {
  local name=$1 username=$2 expected_occurrences=$3 path content remaining
  local occurrences=0
  path=$(admission_canary::_definition_path "$name") || return 1
  content=$(< "$path")
  remaining=$content
  while [[ $remaining == *"$_ATLAS_RECOVERY_OPERATOR_PLACEHOLDER"* ]]; do
    remaining=${remaining#*"$_ATLAS_RECOVERY_OPERATOR_PLACEHOLDER"}
    occurrences=$((occurrences + 1))
  done
  ((occurrences == expected_occurrences)) || {
    recovery::die "unexpected Recovery Operator placeholder count: ${name}"
    return 1
  }
  printf '%s\n' "${content//$_ATLAS_RECOVERY_OPERATOR_PLACEHOLDER/$username}"
}

admission_canary::render_manifests() {
  local username=$1 fixture fixture_content
  principal_identity::validate_recovery_operator "$username" || return 1
  fixture=$(admission_canary::_definition_path fixture.yaml) || return 1
  fixture_content=$(< "$fixture")

  printf '%s\n' "$fixture_content"
  printf '%s\n' '---'
  admission_canary::_render_template escape-rbac.yaml.tpl "$username" 1
  printf '%s\n' '---'
  admission_canary::_render_template protection.yaml.tpl "$username" 1
}

admission_canary::dispatch() {
  local recovery_operator=''
  while (($# > 0)); do
    case "$1" in
      --recovery-operator)
        (($# >= 2)) || {
          recovery::die "--recovery-operator requires a value"
          return 2
        }
        [[ -z $recovery_operator ]] || {
          recovery::die "--recovery-operator may be specified only once"
          return 2
        }
        recovery_operator=$2
        shift 2
        ;;
      -h | --help)
        recovery::usage
        return 0
        ;;
      *)
        recovery::die "unknown option: $1"
        return 2
        ;;
    esac
  done

  [[ -n $recovery_operator ]] || {
    recovery::die "phase0 admission-canary-manifests requires --recovery-operator"
    return 2
  }
  admission_canary::render_manifests "$recovery_operator"
}
