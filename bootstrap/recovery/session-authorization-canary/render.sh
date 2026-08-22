# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_SESSION_AUTHORIZATION_CANARY_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_SESSION_AUTHORIZATION_CANARY_LOADED=1

readonly _ATLAS_SESSION_CANARY_RECOVERY_OPERATOR='__ATLAS_RECOVERY_OPERATOR_USERNAME__'
readonly _ATLAS_SESSION_CANARY_AUTHORIZER='__ATLAS_SESSION_AUTHORIZER_USERNAME__'

session_canary::_definition_path() {
  local name=$1 path
  path="${ATLAS_RECOVERY_ROOT_DIR}/bootstrap/recovery/session-authorization-canary/${name}"
  [[ -f $path && ! -L $path ]] || {
    recovery::die "session authorization canary definition is missing or unsafe: ${path}"
    return 1
  }
  printf '%s\n' "$path"
}

session_canary::_placeholder_count() {
  local content=$1 placeholder=$2 remaining count=0
  remaining=$content
  while [[ $remaining == *"$placeholder"* ]]; do
    remaining=${remaining#*"$placeholder"}
    count=$((count + 1))
  done
  printf '%s\n' "$count"
}

session_canary::_render_template() {
  local name=$1 recovery_operator=$2 authorizer=$3
  local expected_recovery=$4 expected_authorizer=$5 path content
  local recovery_count authorizer_count

  path=$(session_canary::_definition_path "$name") || return 1
  content=$(< "$path")
  recovery_count=$(session_canary::_placeholder_count "$content" "$_ATLAS_SESSION_CANARY_RECOVERY_OPERATOR")
  authorizer_count=$(session_canary::_placeholder_count "$content" "$_ATLAS_SESSION_CANARY_AUTHORIZER")
  ((recovery_count == expected_recovery && authorizer_count == expected_authorizer)) || {
    recovery::die "unexpected principal placeholder count: ${name}"
    return 1
  }

  content=${content//$_ATLAS_SESSION_CANARY_RECOVERY_OPERATOR/$recovery_operator}
  content=${content//$_ATLAS_SESSION_CANARY_AUTHORIZER/$authorizer}
  [[ $content != *'__ATLAS_'* ]] || {
    recovery::die "unresolved placeholder in session authorization canary: ${name}"
    return 1
  }
  printf '%s\n' "$content"
}

session_canary::render_manifests() {
  local recovery_operator=$1 authorizer=$2

  principal_identity::validate_pair "$recovery_operator" "$authorizer" || return 1

  session_canary::_render_template rbac.yaml.tpl "$recovery_operator" "$authorizer" 0 1
  printf '%s\n' '---'
  session_canary::_render_template fence-authorization.yaml.tpl "$recovery_operator" "$authorizer" 1 4
  printf '%s\n' '---'
  session_canary::_render_template binding-shape-authorization.yaml.tpl "$recovery_operator" "$authorizer" 1 2
  printf '%s\n' '---'
  session_canary::_render_template permission-authorization.yaml.tpl "$recovery_operator" "$authorizer" 1 1
  printf '%s\n' '---'
  session_canary::_render_template guard-fixture.yaml "$recovery_operator" "$authorizer" 0 0
  printf '%s\n' '---'
  session_canary::_render_template guard-authorization.yaml.tpl "$recovery_operator" "$authorizer" 2 0
}

session_canary::dispatch() {
  local recovery_operator='' session_authorizer=''
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
      --session-authorizer)
        (($# >= 2)) || {
          recovery::die "--session-authorizer requires a value"
          return 2
        }
        [[ -z $session_authorizer ]] || {
          recovery::die "--session-authorizer may be specified only once"
          return 2
        }
        session_authorizer=$2
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
    recovery::die "phase0 session-authorization-canary-manifests requires --recovery-operator"
    return 2
  }
  [[ -n $session_authorizer ]] || {
    recovery::die "phase0 session-authorization-canary-manifests requires --session-authorizer"
    return 2
  }
  session_canary::render_manifests "$recovery_operator" "$session_authorizer"
}
