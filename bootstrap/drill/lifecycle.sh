# shellcheck shell=bash

[[ -n ${_ATLAS_DRILL_LIFECYCLE_LOADED:-} ]] && return 0
readonly _ATLAS_DRILL_LIFECYCLE_LOADED=1

readonly ATLAS_DRILL_WAIT_TIMEOUT=300s
readonly ATLAS_DRILL_AUDIT_TIMEOUT_SECONDS=20

drill::_version_triplet() {
  local value=$1
  [[ $value =~ ([0-9]+)\.([0-9]+)\.([0-9]+) ]] || return 1
  printf '%s.%s.%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
}

drill::_require_exact_version() {
  local tool=$1 actual=$2 expected=$3
  [[ $actual == "$expected" ]] || {
    drill::die "${tool} version mismatch: expected=${expected} actual=${actual}"
    return 1
  }
}

drill::_host_preflight() {
  local tool actual docker_endpoint expected_endpoint
  [[ $(uname -s) == Darwin ]] || {
    drill::die "audited Kind drills require Darwin"
    return 1
  }
  [[ $(uname -m) == arm64 ]] || {
    drill::die "audited Kind drills require arm64"
    return 1
  }
  for tool in docker kind kubectl shasum awk grep mktemp chmod stat cmp; do
    command -v "$tool" > /dev/null 2>&1 || {
      drill::die "required command is missing: ${tool}"
      return 1
    }
  done

  [[ -z ${DOCKER_HOST:-} ]] || {
    drill::die "DOCKER_HOST overrides are forbidden for an OrbStack drill"
    return 1
  }
  [[ $(docker context show 2> /dev/null) == orbstack ]] || {
    drill::die "the active Docker context must be orbstack"
    return 1
  }
  [[ -n ${HOME:-} ]] || {
    drill::die "HOME is unavailable for OrbStack endpoint validation"
    return 1
  }
  docker_endpoint=$(docker context inspect orbstack --format '{{.Endpoints.docker.Host}}' 2> /dev/null) || return 1
  expected_endpoint="unix://${HOME}/.orbstack/run/docker.sock"
  [[ $docker_endpoint == "$expected_endpoint" ]] || {
    drill::die "the orbstack Docker context has an unexpected endpoint"
    return 1
  }

  actual=$(drill::_version_triplet "$BASH_VERSION") || return 1
  drill::_require_exact_version bash "$actual" "$(drill::target BASH_VERSION)" || return 1
  actual=$(kind version 2> /dev/null | awk '{sub(/^v/, "", $2); print $2; exit}') || return 1
  drill::_require_exact_version kind "$actual" "$(drill::target KIND_VERSION)" || return 1
  actual=$(env -u KUBECONFIG kubectl version --client --output=json 2> /dev/null | awk -F'"' '/"gitVersion"/ {sub(/^v/, "", $4); print $4; exit}') || return 1
  drill::_require_exact_version kubectl "$actual" "$(drill::target KUBECTL_VERSION)" || return 1

  docker info > /dev/null 2>&1 || {
    drill::die "Docker daemon is unavailable"
    return 1
  }
  docker image inspect "$(drill::target KIND_NODE_IMAGE)" > /dev/null 2>&1 || {
    drill::die "locked Kind node image is not available locally"
    return 1
  }
}

drill::_cluster_absent() {
  local cluster clusters containers
  cluster=$(drill::target cluster_name)
  clusters=$(kind get clusters --quiet 2>&1) || {
    drill::die "Kind cluster discovery failed: ${clusters}"
    return 1
  }
  grep -Fqx -- "$cluster" <<< "$clusters" && {
    drill::die "drill cluster already exists; reuse is forbidden: ${cluster}"
    return 1
  }
  containers=$(docker ps -a \
    --filter "label=io.x-k8s.kind.cluster=${cluster}" \
    --format '{{.Names}}') || {
    drill::die "Docker node discovery failed"
    return 1
  }
  [[ -z $containers ]] || {
    drill::die "retained Kind nodes already exist; implicit cleanup is forbidden"
    return 1
  }
  [[ ! -e $(drill::target kubeconfig) && ! -L $(drill::target kubeconfig) ]] || {
    drill::die "drill kubeconfig already exists; reuse is forbidden"
    return 1
  }
}

drill::_snapshot_ambient_kubeconfigs() {
  local destination=$1 candidate parent canonical hash
  local -a ambient_paths=()
  drill::_ambient_kubeconfig_paths ambient_paths || return 1
  : > "$destination" || return 1
  chmod 0600 "$destination" || return 1
  for candidate in "${ambient_paths[@]}"; do
    if [[ -e $candidate || -L $candidate ]]; then
      [[ -f $candidate && ! -L $candidate ]] || {
        drill::die "ambient kubeconfig is not a safe regular file: ${candidate}"
        return 1
      }
      parent=$(cd "$(dirname "$candidate")" && pwd -P) || return 1
      canonical="${parent}/$(basename "$candidate")"
      hash=$(shasum -a 256 "$canonical" | awk '{print $1}') || return 1
      printf 'PRESENT\t%s\t%s\n' "$canonical" "$hash" >> "$destination" || return 1
    else
      printf 'ABSENT\t%s\n' "$candidate" >> "$destination" || return 1
    fi
  done
}

drill::_terminal_available() {
  [[ -t 0 && -t 1 && -r /dev/tty && -w /dev/tty ]]
}

drill::_read_confirmation() {
  local variable_name=$1 read_value
  IFS= read -r read_value < /dev/tty || return 1
  printf -v "$variable_name" '%s' "$read_value" || return 1
}

drill::_human_gate() {
  local config_sha=$1 expected response
  expected="CREATE $(drill::target cluster_name) ${config_sha:0:12}"
  drill::_terminal_available || {
    drill::die "cluster creation requires an interactive terminal"
    return 1
  }

  {
    printf 'Human Judgment Gate: cluster-lifecycle\n'
    printf 'cluster=%s\n' "$(drill::target cluster_name)"
    printf 'context=%s\n' "$(drill::target context)"
    printf 'kubeconfig=%s\n' "$(drill::target kubeconfig)"
    printf 'auditDirectory=%s\n' "$(drill::target audit_directory)"
    printf 'nodeImage=%s\n' "$(drill::target KIND_NODE_IMAGE)"
    printf 'kindConfigSHA256=%s\n' "$config_sha"
    printf 'Type exactly: %s\n> ' "$expected"
  } > /dev/tty || return 1
  drill::_read_confirmation response || return 1
  [[ $response == "$expected" ]] || {
    drill::die "cluster-lifecycle Human Judgment challenge did not match"
    return 1
  }
}

drill::_verify_audit_directory_writable() {
  local directory probe
  directory=$(drill::target audit_directory)
  probe=$(mktemp "${directory}/.atlas-write-probe.XXXXXX") || {
    drill::die "audit directory write probe failed"
    return 1
  }
  chmod 0600 "$probe" || return 1
  rm -f -- "$probe" || return 1
}

drill::_kubeconfig_mode() {
  local file=$1 mode
  if mode=$(stat -f '%Lp' "$file" 2> /dev/null); then
    printf '%s\n' "$mode"
  else
    stat -c '%a' "$file"
  fi
}

drill::_kubectl() {
  env -u KUBECONFIG kubectl \
    --kubeconfig "$(drill::target kubeconfig)" \
    --context "$(drill::target context)" \
    --request-timeout=20s \
    "$@"
}

drill::_verify_mount() {
  local node=$1 destination=$2 expected_source=$3 expected_rw=$4 record source writable
  record=$(docker inspect --format \
    "{{range .Mounts}}{{if eq .Destination \"${destination}\"}}{{printf \"%s\\t%t\" .Source .RW}}{{end}}{{end}}" \
    "$node") || return 1
  IFS=$'\t' read -r source writable <<< "$record"
  [[ $source == "$expected_source" && $writable == "$expected_rw" ]]
}

drill::_wait_for_audit_event() {
  local log_file attempt
  log_file="$(drill::target audit_directory)/kube-apiserver-audit.log"
  for ((attempt = 0; attempt < ATLAS_DRILL_AUDIT_TIMEOUT_SECONDS; attempt++)); do
    if [[ -f $log_file && ! -L $log_file && -s $log_file ]] && grep -Fq '"requestURI":"/readyz"' "$log_file"; then
      return 0
    fi
    sleep 1
  done
  drill::die "API audit log did not record the explicit readiness probe"
}

drill::verify_cluster() {
  local cluster node actual_image current_context policy policy_sha mounted_sha
  cluster=$(drill::target cluster_name)
  node="${cluster}-control-plane"
  policy="${ATLAS_DRILL_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml"

  kind get clusters --quiet | grep -Fqx -- "$cluster" || {
    drill::die "created Kind cluster is not discoverable"
    return 1
  }
  [[ $(docker ps -a --filter "label=io.x-k8s.kind.cluster=${cluster}" --format '{{.Names}}') == "$node" ]] || {
    drill::die "drill cluster must have exactly one canonical control-plane node"
    return 1
  }
  actual_image=$(docker inspect --format '{{.Config.Image}}' "$node") || return 1
  [[ $actual_image == "$(drill::target KIND_NODE_IMAGE)" ]] || {
    drill::die "Kind node image differs from versions.lock"
    return 1
  }

  [[ -s $(drill::target kubeconfig) && ! -L $(drill::target kubeconfig) ]] || {
    drill::die "generated kubeconfig is missing or unsafe"
    return 1
  }
  [[ $(drill::_kubeconfig_mode "$(drill::target kubeconfig)") == 600 ]] || {
    drill::die "generated kubeconfig must have mode 0600"
    return 1
  }
  current_context=$(drill::_kubectl config current-context) || return 1
  [[ $current_context == "$(drill::target context)" ]] || {
    drill::die "generated kubeconfig has an unexpected current context"
    return 1
  }
  drill::_kubectl wait node --all --for=condition=Ready --timeout="$ATLAS_DRILL_WAIT_TIMEOUT" > /dev/null || return 1

  drill::_verify_mount "$node" /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml "$policy" false || {
    drill::die "audit policy mount is missing or writable"
    return 1
  }
  drill::_verify_mount "$node" /var/log/kubernetes/audit "$(drill::target audit_directory)" true || {
    drill::die "audit log mount is missing or read-only"
    return 1
  }
  docker exec "$node" grep -Fq -- '--audit-policy-file=/etc/kubernetes/policies/atlas-recovery-audit-policy.yaml' /etc/kubernetes/manifests/kube-apiserver.yaml || {
    drill::die "live API server audit-policy-file argument is missing"
    return 1
  }
  docker exec "$node" grep -Fq -- '--audit-log-path=/var/log/kubernetes/audit/kube-apiserver-audit.log' /etc/kubernetes/manifests/kube-apiserver.yaml || {
    drill::die "live API server audit-log-path argument is missing"
    return 1
  }
  policy_sha=$(shasum -a 256 "$policy" | awk '{print $1}') || return 1
  mounted_sha=$(docker exec "$node" sha256sum /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml | awk '{print $1}') || return 1
  [[ $mounted_sha == "$policy_sha" ]] || {
    drill::die "mounted audit policy checksum differs from Git"
    return 1
  }

  drill::_kubectl get --raw=/readyz > /dev/null || return 1
  drill::_wait_for_audit_event || return 1
}

drill::_remove_temporary_directory() {
  local directory=$1
  [[ $directory == "${TMPDIR:-/tmp}/atlas-kind-drill."* && -d $directory && ! -L $directory ]] || return 1
  rm -rf -- "$directory"
}

drill::_create_cluster_inner() {
  local temporary_directory=$1 config_file ambient_before ambient_after config_sha
  config_file="${temporary_directory}/kind.yaml"
  ambient_before="${temporary_directory}/ambient-before.sha256"
  ambient_after="${temporary_directory}/ambient-after.sha256"

  drill::_host_preflight || return 1
  drill::_cluster_absent || return 1
  drill::render_kind_config "$config_file" || return 1
  drill::validate_kind_config "$config_file" || return 1
  config_sha=$(shasum -a 256 "$config_file" | awk '{print $1}') || return 1
  drill::_snapshot_ambient_kubeconfigs "$ambient_before" || return 1
  drill::_human_gate "$config_sha" || return 1

  # Recheck every uniqueness boundary after the human decision and immediately
  # before the first durable mutation.
  drill::_cluster_absent || return 1
  drill::revalidate_target_paths || return 1
  drill::_directory_empty "$(drill::target audit_directory)" || {
    drill::die "audit directory changed after approval"
    return 1
  }
  drill::_verify_audit_directory_writable || return 1

  if ! env -u KUBECONFIG kind create cluster \
    --name "$(drill::target cluster_name)" \
    --image "$(drill::target KIND_NODE_IMAGE)" \
    --config "$config_file" \
    --kubeconfig "$(drill::target kubeconfig)" \
    --wait "$ATLAS_DRILL_WAIT_TIMEOUT" \
    --retain; then
    drill::die "Kind creation failed; retained state requires separate human review"
    return 1
  fi

  drill::verify_cluster || return 1
  drill::_snapshot_ambient_kubeconfigs "$ambient_after" || return 1
  cmp -s "$ambient_before" "$ambient_after" || {
    drill::die "ambient/default kubeconfig changed during drill creation"
    return 1
  }
  printf 'drill-cluster\tREADY\t%s\t%s\n' "$(drill::target cluster_name)" "$(drill::target context)"
}

drill::create_cluster() {
  local cluster_name=$1 context=$2 kubeconfig=$3 audit_directory=$4 temporary_directory status
  drill::resolve_target "$cluster_name" "$context" "$kubeconfig" "$audit_directory" || return 1
  temporary_directory=$(mktemp -d "${TMPDIR:-/tmp}/atlas-kind-drill.XXXXXX") || return 1

  if drill::_create_cluster_inner "$temporary_directory"; then
    status=0
  else
    status=$?
  fi
  drill::_remove_temporary_directory "$temporary_directory" || true
  return "$status"
}
