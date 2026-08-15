# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_AUDIT_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_AUDIT_LOADED=1

audit::_canonical_destination() {
  local requested=$1 canonical repository

  [[ $requested == /* ]] || {
    recovery::die "--audit-dir must be an absolute path"
    return 1
  }
  audit::_path_bytes_are_safe "$requested" || {
    recovery::die "--audit-dir must be valid YAML-printable UTF-8 without C0 or C1 control characters"
    return 1
  }
  [[ -d $requested && ! -L $requested ]] || {
    recovery::die "--audit-dir must be an existing non-symlink directory"
    return 1
  }

  canonical=$(cd "$requested" && pwd -P)
  repository=$(cd "$ATLAS_RECOVERY_ROOT_DIR" && pwd -P)
  [[ $canonical != / ]] || {
    recovery::die "--audit-dir must not be the filesystem root"
    return 1
  }
  [[ $canonical != "$repository" && $canonical != "${repository}/"* ]] || {
    recovery::die "--audit-dir must remain outside the repository"
    return 1
  }
  [[ -w $canonical ]] || {
    recovery::die "--audit-dir is not writable: ${canonical}"
    return 1
  }

  printf '%s\n' "$canonical"
}

audit::_path_bytes_are_safe() {
  local value=$1
  local LC_ALL=C
  local index=0 length first second third fourth

  # Bash arguments cannot contain NUL. Decode every other byte under the C
  # locale so the result does not depend on the caller's character locale.
  length=${#value}
  while ((index < length)); do
    printf -v first '%d' "'${value:index:1}"
    if ((first <= 31 || first == 127)); then
      return 1
    fi
    if ((first <= 127)); then
      ((index += 1))
      continue
    fi

    ((index + 1 < length)) || return 1
    printf -v second '%d' "'${value:index+1:1}"
    if ((first >= 194 && first <= 223)); then
      ((second >= 128 && second <= 191)) || return 1
      # U+0080 through U+009F encode as C2 80 through C2 9F.
      ((first != 194 || second >= 160)) || return 1
      ((index += 2))
      continue
    fi

    ((index + 2 < length)) || return 1
    printf -v third '%d' "'${value:index+2:1}"
    if ((first >= 224 && first <= 239)); then
      ((third >= 128 && third <= 191)) || return 1
      # YAML 1.2 excludes U+FFFE and U+FFFF despite their valid UTF-8 form.
      ((first != 239 || second != 191 || third < 190)) || return 1
      if ((first == 224)); then
        ((second >= 160 && second <= 191)) || return 1
      elif ((first == 237)); then
        ((second >= 128 && second <= 159)) || return 1
      else
        ((second >= 128 && second <= 191)) || return 1
      fi
      ((index += 3))
      continue
    fi

    ((first >= 240 && first <= 244 && index + 3 < length)) || return 1
    printf -v fourth '%d' "'${value:index+3:1}"
    ((third >= 128 && third <= 191 && fourth >= 128 && fourth <= 191)) || return 1
    if ((first == 240)); then
      ((second >= 144 && second <= 191)) || return 1
    elif ((first == 244)); then
      ((second >= 128 && second <= 143)) || return 1
    else
      ((second >= 128 && second <= 191)) || return 1
    fi
    ((index += 4))
  done
}

audit::_yaml_scalar() {
  local value=$1
  value=${value//\'/\'\'}
  printf "'%s'" "$value"
}

audit::render_kind_config() {
  local audit_directory policy_file
  audit_directory=$(audit::_canonical_destination "$1")
  policy_file="${ATLAS_RECOVERY_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml"
  [[ -f $policy_file && ! -L $policy_file ]] || {
    recovery::die "recovery audit policy is missing or unsafe: ${policy_file}"
    return 1
  }

  cat << EOF
apiVersion: kind.x-k8s.io/v1alpha4
kind: Cluster

networking:
  ipFamily: ipv4
  apiServerAddress: 127.0.0.1

nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |-
        apiVersion: kubeadm.k8s.io/v1beta4
        kind: ClusterConfiguration
        apiServer:
          extraArgs:
            - name: audit-log-path
              value: /var/log/kubernetes/audit/kube-apiserver-audit.log
            - name: audit-log-maxage
              value: "1"
            - name: audit-log-maxbackup
              value: "5"
            - name: audit-log-maxsize
              value: "100"
            - name: audit-policy-file
              value: /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml
          extraVolumes:
            - name: atlas-recovery-audit-policy
              hostPath: /etc/kubernetes/policies
              mountPath: /etc/kubernetes/policies
              readOnly: true
              pathType: Directory
            - name: atlas-recovery-audit-log
              hostPath: /var/log/kubernetes/audit
              mountPath: /var/log/kubernetes/audit
              readOnly: false
              pathType: Directory
    extraMounts:
      - hostPath: $(audit::_yaml_scalar "$policy_file")
        containerPath: /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml
        readOnly: true
      - hostPath: $(audit::_yaml_scalar "$audit_directory")
        containerPath: /var/log/kubernetes/audit
        readOnly: false
EOF
}

audit::dispatch() {
  local action=${1:-}
  [[ -n $action ]] || {
    recovery::die "phase0 requires an action"
    return 2
  }
  shift

  [[ $action == audit-config ]] || {
    recovery::die "unknown phase0 action: ${action}"
    return 2
  }

  local audit_directory=
  while (($# > 0)); do
    case "$1" in
      --audit-dir)
        (($# >= 2)) || {
          recovery::die "--audit-dir requires a value"
          return 2
        }
        [[ -z $audit_directory ]] || {
          recovery::die "--audit-dir may be specified only once"
          return 2
        }
        audit_directory=$2
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

  [[ -n $audit_directory ]] || {
    recovery::die "phase0 audit-config requires --audit-dir"
    return 2
  }
  audit::render_kind_config "$audit_directory"
}
