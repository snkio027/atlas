# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_AUDIT_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_AUDIT_LOADED=1

audit::_canonical_destination() {
  local requested=$1 canonical repository

  [[ $requested == /* ]] || {
    recovery::die "--audit-dir must be an absolute path"
    return 1
  }
  [[ $requested != *$'\n'* && $requested != *$'\r'* && $requested != *$'\t'* ]] || {
    recovery::die "--audit-dir must not contain control characters"
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
