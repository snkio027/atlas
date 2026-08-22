# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_PRINCIPAL_IDENTITY_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_PRINCIPAL_IDENTITY_LOADED=1

readonly _ATLAS_RECOVERY_PRINCIPAL_PREFIX='atlas:break-glass'
readonly _ATLAS_SESSION_AUTHORIZER_PREFIX='atlas:session-authz'

principal_identity::_validate_generation() {
  local LC_ALL=C
  local generation=$1
  [[ $generation =~ ^[1-9][0-9]{0,5}$ ]] || {
    recovery::die "invalid recovery principal generation"
    return 1
  }
}

principal_identity::validate_rotation() {
  local current=$1 previous=$2 label=$3
  principal_identity::_validate_generation "$current" || return 1
  principal_identity::_validate_generation "$previous" || return 1
  ((10#$current == 10#$previous + 1)) || {
    recovery::die "${label} current generation must immediately follow its previous generation"
    return 1
  }
}

principal_identity::_validate_namespace_uid() {
  local LC_ALL=C
  local namespace_uid=$1
  [[ $namespace_uid =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || {
    recovery::die "invalid kube-system Namespace UID"
    return 1
  }
}

principal_identity::_build() {
  local LC_ALL=C
  local prefix=$1 namespace_uid=$2 generation=$3 username
  principal_identity::_validate_namespace_uid "$namespace_uid" || return 1
  principal_identity::_validate_generation "$generation" || return 1
  printf -v username '%s:%s:g%s' "$prefix" "$namespace_uid" "$generation"
  ((${#username} <= 64)) || {
    recovery::die "recovery principal username exceeds the X.509 Common Name byte limit"
    return 1
  }
  printf '%s\n' "$username"
}

principal_identity::recovery_operator() {
  principal_identity::_build "$_ATLAS_RECOVERY_PRINCIPAL_PREFIX" "$1" "$2"
}

principal_identity::session_authorizer() {
  principal_identity::_build "$_ATLAS_SESSION_AUTHORIZER_PREFIX" "$1" "$2"
}

principal_identity::_namespace_uid() {
  local LC_ALL=C
  local role=$1 username=$2 expected_uid=${3:-} namespace_uid
  case "$role" in
    recovery-operator)
      [[ $username =~ ^atlas:break-glass:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}):g[1-9][0-9]{0,5}$ ]] || {
        recovery::die "invalid Recovery Operator username"
        return 1
      }
      ;;
    session-authorizer)
      [[ $username =~ ^atlas:session-authz:([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}):g[1-9][0-9]{0,5}$ ]] || {
        recovery::die "invalid Session Authorizer username"
        return 1
      }
      ;;
    *)
      recovery::die "unknown recovery principal role: ${role}"
      return 1
      ;;
  esac
  namespace_uid=${BASH_REMATCH[1]}
  ((${#username} <= 64)) || {
    recovery::die "recovery principal username exceeds the X.509 Common Name byte limit"
    return 1
  }
  [[ -z $expected_uid || $namespace_uid == "$expected_uid" ]] || {
    recovery::die "recovery principal targets another kube-system Namespace UID"
    return 1
  }
  printf '%s\n' "$namespace_uid"
}

principal_identity::validate_recovery_operator() {
  principal_identity::_namespace_uid recovery-operator "$1" "${2:-}" > /dev/null
}

principal_identity::validate_session_authorizer() {
  principal_identity::_namespace_uid session-authorizer "$1" "${2:-}" > /dev/null
}

principal_identity::validate_pair() {
  local recovery_operator=$1 session_authorizer=$2 expected_uid=${3:-}
  local recovery_uid authorizer_uid
  recovery_uid=$(principal_identity::_namespace_uid recovery-operator "$recovery_operator" "$expected_uid") || return 1
  authorizer_uid=$(principal_identity::_namespace_uid session-authorizer "$session_authorizer" "$expected_uid") || return 1
  [[ $recovery_uid == "$authorizer_uid" ]] || {
    recovery::die "Recovery Operator and Session Authorizer target different kube-system UIDs"
    return 1
  }
}

principal_identity::plan() {
  local namespace_uid=$1 recovery_generation=$2 previous_recovery_generation=$3
  local authorizer_generation=$4 previous_authorizer_generation=$5
  local recovery authorizer previous_recovery previous_authorizer
  principal_identity::validate_rotation "$recovery_generation" "$previous_recovery_generation" \
    "Recovery Operator" || return 1
  principal_identity::validate_rotation "$authorizer_generation" "$previous_authorizer_generation" \
    "Session Authorizer" || return 1
  recovery=$(principal_identity::recovery_operator "$namespace_uid" "$recovery_generation") || return 1
  authorizer=$(principal_identity::session_authorizer "$namespace_uid" "$authorizer_generation") || return 1
  previous_recovery=$(principal_identity::recovery_operator "$namespace_uid" "$previous_recovery_generation") || return 1
  previous_authorizer=$(principal_identity::session_authorizer "$namespace_uid" "$previous_authorizer_generation") || return 1
  principal_identity::validate_pair "$recovery" "$authorizer" "$namespace_uid" || return 1
  principal_identity::validate_pair "$previous_recovery" "$previous_authorizer" "$namespace_uid" || return 1
  printf '%s\t%s\t%s\t%s\n' "$recovery" "$authorizer" "$previous_recovery" "$previous_authorizer"
}

principal_identity::validate_plan() {
  local namespace_uid=$1 recovery_generation=$2 previous_recovery_generation=$3
  local authorizer_generation=$4 previous_authorizer_generation=$5
  local recovery=$6 authorizer=$7 previous_recovery=$8 previous_authorizer=$9
  local expected
  expected=$(principal_identity::plan "$namespace_uid" \
    "$recovery_generation" "$previous_recovery_generation" \
    "$authorizer_generation" "$previous_authorizer_generation") || return 1
  [[ $expected == "$recovery"$'\t'"$authorizer"$'\t'"$previous_recovery"$'\t'"$previous_authorizer" ]] || {
    recovery::die "planned recovery principals changed after approval"
    return 1
  }
}

principal_identity::_validate_x509_subject() {
  local kind=$1 role=$2 username=$3 path=$4 subject
  case "$role" in
    recovery | previous_recovery)
      principal_identity::validate_recovery_operator "$username" || return 1
      ;;
    authorizer | previous_authorizer)
      principal_identity::validate_session_authorizer "$username" || return 1
      ;;
    *)
      recovery::die "unknown certificate principal role: ${role}"
      return 1
      ;;
  esac
  case "$kind" in
    csr) subject=$(openssl req -in "$path" -noout -subject -nameopt RFC2253) || return 1 ;;
    certificate) subject=$(openssl x509 -in "$path" -noout -subject -nameopt RFC2253) || return 1 ;;
    *)
      recovery::die "unknown X.509 identity document: ${kind}"
      return 1
      ;;
  esac
  [[ $subject == "subject=CN=${username}" ]] || {
    recovery::die "issued ${kind} Subject does not match the planned exact-user identity"
    return 1
  }
}

principal_identity::validate_csr() {
  principal_identity::_validate_x509_subject csr "$1" "$2" "$3"
}

principal_identity::validate_certificate() {
  principal_identity::_validate_x509_subject certificate "$1" "$2" "$3"
}
