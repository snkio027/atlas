# shellcheck shell=bash

[[ -n ${_ATLAS_DRILL_CONTRACT_LOADED:-} ]] && return 0
readonly _ATLAS_DRILL_CONTRACT_LOADED=1

declare -gA ATLAS_DRILL_TARGET=()

drill::target() {
  local key=$1
  [[ -n ${ATLAS_DRILL_TARGET[$key]+present} ]] || {
    drill::die "internal target key is unavailable: ${key}"
    return 1
  }
  printf '%s\n' "${ATLAS_DRILL_TARGET[$key]}"
}

drill::_locked_value() {
  local key=$1 file line candidate value='' count=0
  file="${ATLAS_DRILL_ROOT_DIR}/versions.lock"
  [[ -f $file && ! -L $file ]] || {
    drill::die "versions.lock is missing or unsafe"
    return 1
  }

  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line == "${key}="* ]] || continue
    candidate=${line#*=}
    [[ $candidate =~ ^[^[:space:]]+$ ]] || {
      drill::die "invalid ${key} in versions.lock"
      return 1
    }
    value=$candidate
    ((count += 1))
  done < "$file"

  ((count == 1)) || {
    drill::die "versions.lock must contain ${key} exactly once"
    return 1
  }
  printf '%s\n' "$value"
}

drill::_directory_empty() {
  local directory=$1
  (
    shopt -s dotglob nullglob
    local -a entries=("${directory}"/*)
    ((${#entries[@]} == 0))
  )
}

drill::_canonical_directory() {
  local requested=$1 label=$2 canonical repository
  [[ $requested == /* ]] || {
    drill::die "${label} must be an absolute path"
    return 1
  }
  [[ -d $requested && ! -L $requested ]] || {
    drill::die "${label} must be an existing non-symlink directory"
    return 1
  }
  canonical=$(cd "$requested" && pwd -P) || return 1
  repository=$(cd "$ATLAS_DRILL_ROOT_DIR" && pwd -P) || return 1
  [[ $canonical != / ]] || {
    drill::die "${label} must not be the filesystem root"
    return 1
  }
  [[ $canonical != "$repository" && $canonical != "${repository}/"* ]] || {
    drill::die "${label} must remain outside the repository"
    return 1
  }
  [[ -w $canonical ]] || {
    drill::die "${label} is not writable: ${canonical}"
    return 1
  }
  printf '%s\n' "$canonical"
}

drill::_canonical_kubeconfig() {
  local requested=$1 cluster_name=$2 parent canonical_parent destination repository
  [[ $requested =~ ^/[A-Za-z0-9._/-]+$ ]] || {
    drill::die "--kubeconfig must be an absolute ASCII path"
    return 1
  }
  [[ "/${requested#/}/" != */../* && "/${requested#/}/" != */./* ]] || {
    drill::die "--kubeconfig must not contain dot path components"
    return 1
  }
  [[ $(basename "$requested") == "${cluster_name}.kubeconfig" ]] || {
    drill::die "--kubeconfig basename must be ${cluster_name}.kubeconfig"
    return 1
  }
  [[ ! -e $requested && ! -L $requested ]] || {
    drill::die "--kubeconfig already exists; reuse and overwrite are forbidden"
    return 1
  }

  parent=$(dirname "$requested")
  [[ -d $parent && ! -L $parent && -w $parent ]] || {
    drill::die "--kubeconfig parent must be an existing writable non-symlink directory"
    return 1
  }
  canonical_parent=$(cd "$parent" && pwd -P) || return 1
  destination="${canonical_parent}/$(basename "$requested")"
  repository=$(cd "$ATLAS_DRILL_ROOT_DIR" && pwd -P) || return 1
  [[ $destination != "${repository}/"* ]] || {
    drill::die "--kubeconfig must remain outside the repository"
    return 1
  }
  printf '%s\n' "$destination"
}

drill::_ambient_kubeconfig_paths() {
  local output_name=$1 raw candidate parent
  local -n output=$output_name
  local -a candidates=() configured=()
  output=()
  [[ -n ${HOME:-} ]] || {
    drill::die "HOME is unavailable for default kubeconfig isolation"
    return 1
  }
  candidates=("${HOME}/.kube/config")

  if [[ -n ${KUBECONFIG:-} ]]; then
    raw=$KUBECONFIG
    [[ $raw != :* && $raw != *: && $raw != *::* ]] || {
      drill::die "ambient KUBECONFIG contains an empty path"
      return 1
    }
    IFS=: read -r -a configured <<< "$raw"
    candidates+=("${configured[@]}")
  fi

  for candidate in "${candidates[@]}"; do
    [[ -n $candidate ]] || {
      drill::die "ambient KUBECONFIG contains an empty path"
      return 1
    }
    if [[ $candidate != /* ]]; then
      candidate="$(pwd -P)/${candidate}"
    fi
    parent=$(dirname "$candidate")
    if [[ -d $parent ]]; then
      candidate="$(cd "$parent" && pwd -P)/$(basename "$candidate")" || return 1
    fi
    output+=("$candidate")
  done
}

drill::_reject_ambient_destination() {
  local destination=$1 ambient
  local -a ambient_paths=()
  drill::_ambient_kubeconfig_paths ambient_paths || return 1
  for ambient in "${ambient_paths[@]}"; do
    [[ $destination != "$ambient" ]] || {
      drill::die "--kubeconfig must not target an ambient/default kubeconfig"
      return 1
    }
  done
}

drill::resolve_target() {
  local cluster_name=$1 context=$2 kubeconfig=$3 audit_directory=$4
  local expected_context resolved_kubeconfig resolved_audit key value

  [[ $cluster_name =~ ^atlas-recovery-drill-[0-9]{8}t[0-9]{6}z-[0-9a-f]{8}$ ]] || {
    drill::die "--cluster-name must match atlas-recovery-drill-YYYYMMDDtHHMMSSz-<8 lowercase hex>"
    return 1
  }
  expected_context="kind-${cluster_name}"
  [[ $context == "$expected_context" ]] || {
    drill::die "--context must equal ${expected_context}"
    return 1
  }

  resolved_kubeconfig=$(drill::_canonical_kubeconfig "$kubeconfig" "$cluster_name") || return 1
  drill::_reject_ambient_destination "$resolved_kubeconfig" || return 1
  resolved_audit=$(drill::_canonical_directory "$audit_directory" "--audit-dir") || return 1
  [[ $(basename "$resolved_audit") == "$cluster_name" ]] || {
    drill::die "--audit-dir basename must equal the cluster name"
    return 1
  }
  drill::_directory_empty "$resolved_audit" || {
    drill::die "--audit-dir must be empty for a one-time drill cluster"
    return 1
  }
  [[ $resolved_kubeconfig != "${resolved_audit}/"* ]] || {
    drill::die "--kubeconfig must not be stored in the audit directory"
    return 1
  }

  ATLAS_DRILL_TARGET[cluster_name]=$cluster_name
  ATLAS_DRILL_TARGET[context]=$context
  ATLAS_DRILL_TARGET[kubeconfig]=$resolved_kubeconfig
  ATLAS_DRILL_TARGET[audit_directory]=$resolved_audit
  for key in BASH_VERSION KIND_VERSION KUBECTL_VERSION KIND_NODE_IMAGE; do
    value=$(drill::_locked_value "$key") || return 1
    ATLAS_DRILL_TARGET[$key]=$value
  done
  [[ ${ATLAS_DRILL_TARGET[KIND_NODE_IMAGE]} =~ @sha256:[0-9a-f]{64}$ ]] || {
    drill::die "KIND_NODE_IMAGE is not digest pinned"
    return 1
  }
  readonly -A ATLAS_DRILL_TARGET
}

drill::revalidate_target_paths() {
  local kubeconfig audit_directory resolved
  kubeconfig=$(drill::target kubeconfig) || return 1
  audit_directory=$(drill::target audit_directory) || return 1

  resolved=$(drill::_canonical_kubeconfig "$kubeconfig" "$(drill::target cluster_name)") || return 1
  [[ $resolved == "$kubeconfig" ]] || {
    drill::die "--kubeconfig target changed after validation"
    return 1
  }
  drill::_reject_ambient_destination "$resolved" || return 1

  resolved=$(drill::_canonical_directory "$audit_directory" "--audit-dir") || return 1
  [[ $resolved == "$audit_directory" ]] || {
    drill::die "--audit-dir target changed after validation"
    return 1
  }
}

drill::render_kind_config() {
  local destination=$1
  "${ATLAS_DRILL_ROOT_DIR}/bootstrap/recovery/atlas-recovery" \
    phase0 audit-config \
    --audit-dir "$(drill::target audit_directory)" > "$destination" || return 1
  chmod 0600 "$destination" || return 1
}

drill::validate_kind_config() {
  local config=$1 policy audit_directory quoted_policy quoted_audit content
  local audit_log_argument audit_policy_argument policy_volume audit_volume policy_mount audit_mount
  policy="${ATLAS_DRILL_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml"
  audit_directory=$(drill::target audit_directory) || return 1
  quoted_policy=${policy//\'/\'\'}
  quoted_audit=${audit_directory//\'/\'\'}

  [[ -s $config && ! -L $config ]] || {
    drill::die "rendered Kind configuration is missing or unsafe"
    return 1
  }
  content=$(< "$config") || return 1
  grep -Fqx 'apiVersion: kind.x-k8s.io/v1alpha4' "$config" || {
    drill::die "rendered Kind apiVersion is invalid"
    return 1
  }
  grep -Fqx 'kind: Cluster' "$config" || {
    drill::die "rendered Kind kind is invalid"
    return 1
  }
  [[ $(grep -Fc '  - role: control-plane' "$config") == 1 ]] || {
    drill::die "rendered Kind configuration must contain one control-plane node"
    return 1
  }
  grep -Fq '  - role: worker' "$config" && {
    drill::die "rendered Kind configuration must not contain worker nodes"
    return 1
  }

  printf -v audit_log_argument '%s\n%s' \
    '            - name: audit-log-path' \
    '              value: /var/log/kubernetes/audit/kube-apiserver-audit.log'
  printf -v audit_policy_argument '%s\n%s' \
    '            - name: audit-policy-file' \
    '              value: /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml'
  printf -v policy_volume '%s\n%s\n%s\n%s\n%s' \
    '            - name: atlas-recovery-audit-policy' \
    '              hostPath: /etc/kubernetes/policies' \
    '              mountPath: /etc/kubernetes/policies' \
    '              readOnly: true' \
    '              pathType: Directory'
  printf -v audit_volume '%s\n%s\n%s\n%s\n%s' \
    '            - name: atlas-recovery-audit-log' \
    '              hostPath: /var/log/kubernetes/audit' \
    '              mountPath: /var/log/kubernetes/audit' \
    '              readOnly: false' \
    '              pathType: Directory'
  printf -v policy_mount '%s\n%s\n%s' \
    "      - hostPath: '${quoted_policy}'" \
    '        containerPath: /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml' \
    '        readOnly: true'
  printf -v audit_mount '%s\n%s\n%s' \
    "      - hostPath: '${quoted_audit}'" \
    '        containerPath: /var/log/kubernetes/audit' \
    '        readOnly: false'

  [[ $content == *"$audit_log_argument"* ]] || {
    drill::die "rendered API server audit-log argument is invalid"
    return 1
  }
  [[ $content == *"$audit_policy_argument"* ]] || {
    drill::die "rendered API server audit-policy argument is invalid"
    return 1
  }
  [[ $content == *"$policy_volume"* ]] || {
    drill::die "rendered API server audit-policy volume is invalid"
    return 1
  }
  [[ $content == *"$audit_volume"* ]] || {
    drill::die "rendered API server audit-log volume is invalid"
    return 1
  }
  [[ $content == *"$policy_mount"* ]] || {
    drill::die "rendered audit policy mount is invalid"
    return 1
  }
  [[ $content == *"$audit_mount"* ]] || {
    drill::die "rendered audit directory mount is invalid"
    return 1
  }
}
