# shellcheck shell=bash

[[ -n ${_ATLAS_DRILL_LIFECYCLE_LOADED:-} ]] && return 0
readonly _ATLAS_DRILL_LIFECYCLE_LOADED=1

readonly ATLAS_DRILL_WAIT_TIMEOUT=300s
readonly ATLAS_DRILL_AUDIT_TIMEOUT_SECONDS=20

drill::_kind() {
  env \
    -u DOCKER_API_VERSION \
    -u DOCKER_CERT_PATH \
    -u DOCKER_CONFIG \
    -u DOCKER_DEFAULT_PLATFORM \
    -u DOCKER_HOST \
    -u DOCKER_TLS_VERIFY \
    -u KUBECONFIG \
    DOCKER_CONTEXT="$(drill::target docker_context)" \
    KIND_EXPERIMENTAL_PROVIDER=docker \
    kind "$@"
}

drill::_docker() {
  env \
    -u DOCKER_API_VERSION \
    -u DOCKER_CERT_PATH \
    -u DOCKER_CONFIG \
    -u DOCKER_DEFAULT_PLATFORM \
    -u DOCKER_HOST \
    -u DOCKER_TLS_VERIFY \
    DOCKER_CONTEXT="$(drill::target docker_context)" \
    docker "$@"
}

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

drill::_reject_kind_environment() {
  local names
  names=$(env | awk -F= '$1 ~ /^KIND_/ {print $1}') || return 1
  [[ -z $names ]] || {
    drill::die "inherited KIND_* environment variables are forbidden: ${names//$'\n'/,}"
    return 1
  }
}

drill::_reject_docker_environment() {
  local names
  names=$(env | awk -F= '$1 ~ /^DOCKER_/ {print $1}') || return 1
  [[ -z $names ]] || {
    drill::die "inherited DOCKER_* environment variables are forbidden: ${names//$'\n'/,}"
    return 1
  }
}

drill::_verify_docker_target() {
  local actual_context actual_endpoint
  drill::_reject_kind_environment || return 1
  drill::_reject_docker_environment || return 1
  actual_context=$(drill::_docker context show 2> /dev/null) || return 1
  actual_endpoint=$(drill::_docker context inspect "$(drill::target docker_context)" \
    --format '{{.Endpoints.docker.Host}}' 2> /dev/null) || return 1
  [[ $actual_context == "$(drill::target docker_context)" ]] || {
    drill::die "the effective Docker context differs from the approved target"
    return 1
  }
  [[ $actual_endpoint == "$(drill::target docker_endpoint)" ]] || {
    drill::die "the OrbStack Docker endpoint differs from the approved target"
    return 1
  }
}

drill::_verify_node_image() {
  drill::_docker image inspect "$(drill::target KIND_NODE_IMAGE)" > /dev/null 2>&1 || {
    drill::die "approved Kind node image is unavailable on the approved Docker target"
    return 1
  }
}

drill::_host_preflight() {
  local tool actual
  [[ $(uname -s) == Darwin ]] || {
    drill::die "audited Kind drills require Darwin"
    return 1
  }
  [[ $(uname -m) == arm64 ]] || {
    drill::die "audited Kind drills require arm64"
    return 1
  }
  for tool in awk chmod cmp date docker env git grep id install kind ls mktemp mv rmdir sed shasum stat kubectl; do
    command -v "$tool" > /dev/null 2>&1 || {
      drill::die "required command is missing: ${tool}"
      return 1
    }
  done
  drill::_verify_docker_target || return 1

  actual=$(drill::_version_triplet "$BASH_VERSION") || return 1
  drill::_require_exact_version bash "$actual" "$(drill::target BASH_VERSION)" || return 1
  actual=$(drill::_kind version 2> /dev/null | awk '{sub(/^v/, "", $2); print $2; exit}') || return 1
  drill::_require_exact_version kind "$actual" "$(drill::target KIND_VERSION)" || return 1
  actual=$(env -u KUBECONFIG kubectl version --client --output=json 2> /dev/null | awk -F'"' '/"gitVersion"/ {sub(/^v/, "", $4); print $4; exit}') || return 1
  drill::_require_exact_version kubectl "$actual" "$(drill::target KUBECTL_VERSION)" || return 1
  drill::_docker info > /dev/null 2>&1 || {
    drill::die "Docker daemon is unavailable"
    return 1
  }
  drill::_verify_node_image || return 1
}

drill::_cluster_absent() {
  local cluster clusters containers kubeconfig
  cluster=$(drill::target cluster_name) || return 1
  clusters=$(drill::_kind get clusters --quiet 2>&1) || {
    drill::die "Kind cluster discovery failed: ${clusters}"
    return 1
  }
  grep -Fqx -- "$cluster" <<< "$clusters" && {
    drill::die "drill cluster already exists; reuse is forbidden: ${cluster}"
    return 1
  }
  containers=$(drill::_docker ps -a \
    --filter "label=io.x-k8s.kind.cluster=${cluster}" \
    --format '{{.Names}}') || {
    drill::die "Docker node discovery failed"
    return 1
  }
  [[ -z $containers ]] || {
    drill::die "retained Kind nodes already exist; implicit cleanup is forbidden"
    return 1
  }
  kubeconfig=$(drill::target kubeconfig) || return 1
  [[ ! -e $kubeconfig && ! -L $kubeconfig ]] || {
    drill::die "drill kubeconfig already exists; reuse is forbidden"
    return 1
  }
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
  local plan_sha approval_sha expected response
  plan_sha=$(drill::operation plan_sha) || return 1
  approval_sha=$(drill::operation approval_sha) || return 1
  expected="CREATE $(drill::target cluster_name) ${approval_sha:0:12}"
  drill::journal_append GATE PROMPTED "cluster-lifecycle approval requested" || return 1
  drill::_terminal_available || {
    drill::journal_append GATE DENIED "interactive terminal unavailable" || true
    drill::die "cluster creation requires an interactive terminal"
    return 1
  }
  if ! {
    printf 'Human Judgment Gate: cluster-lifecycle\n'
    printf 'actionId=%s\n' "$(drill::operation action_id)"
    printf 'actor=%s\n' "$(drill::operation actor)"
    printf 'cluster=%s\n' "$(drill::target cluster_name)"
    printf 'context=%s\n' "$(drill::target context)"
    printf 'kubeconfig=%s\n' "$(drill::target kubeconfig)"
    printf 'auditDirectory=%s\n' "$(drill::target audit_directory)"
    printf 'evidenceSession=%s\n' "$(drill::operation evidence_session)"
    printf 'storageAssertion=%s\n' "$(drill::target storage_assertion)"
    printf 'dockerContext=%s\n' "$(drill::target docker_context)"
    printf 'dockerEndpoint=%s\n' "$(drill::target docker_endpoint)"
    printf 'gitCommit=%s\n' "$(drill::operation git_commit)"
    printf 'gitTree=%s\n' "$(drill::operation git_tree)"
    printf 'kindConfigSHA256=%s\n' "$(drill::operation config_sha)"
    printf 'auditPolicySHA256=%s\n' "$(drill::operation policy_sha)"
    printf 'versionsLockSHA256=%s\n' "$(drill::operation versions_sha)"
    printf 'nodeImage=%s\n' "$(drill::target KIND_NODE_IMAGE)"
    printf 'preMutationManifestSHA256=%s\n' "$(drill::operation pre_mutation_sha)"
    printf 'approvalSHA256=%s\n' "$(drill::operation approval_sha)"
    printf 'planSHA256=%s\n' "$plan_sha"
    printf 'Type exactly: %s\n> ' "$expected"
  } > /dev/tty; then
    drill::journal_append GATE DENIED "terminal write failed" || true
    return 1
  fi
  if ! drill::_read_confirmation response; then
    drill::journal_append GATE DENIED "terminal read failed" || true
    return 1
  fi
  [[ $response == "$expected" ]] || {
    drill::journal_append GATE DENIED "challenge mismatch" || true
    drill::die "cluster-lifecycle Human Judgment challenge did not match"
    return 1
  }
  drill::journal_append GATE APPROVED "exact approval-bound challenge matched" || return 1
}

drill::_verify_audit_directory_writable() {
  local directory probe
  directory=$(drill::target audit_directory) || return 1
  probe=$(mktemp "${directory}/.atlas-write-probe.XXXXXX") || {
    drill::die "audit directory write probe failed"
    return 1
  }
  chmod 0600 "$probe" || return 1
  rm -f -- "$probe" || return 1
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
  record=$(drill::_docker inspect --format \
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
      drill::assert_managed_file "$log_file" 600 "API audit log" || return 1
      return 0
    fi
    sleep 1
  done
  drill::die "API audit log did not record the explicit readiness probe"
}

drill::verify_cluster() {
  local cluster node actual_image current_context mounted_sha policy_snapshot kubeconfig
  cluster=$(drill::target cluster_name) || return 1
  node="${cluster}-control-plane"
  policy_snapshot=$(drill::operation policy_snapshot) || return 1
  kubeconfig=$(drill::target kubeconfig) || return 1
  drill::assert_managed_file "$policy_snapshot" 400 "approved audit policy snapshot" || return 1

  drill::_kind get clusters --quiet | grep -Fqx -- "$cluster" || {
    drill::die "created Kind cluster is not discoverable"
    return 1
  }
  [[ $(drill::_docker ps -a --filter "label=io.x-k8s.kind.cluster=${cluster}" --format '{{.Names}}') == "$node" ]] || {
    drill::die "drill cluster must have exactly one canonical control-plane node"
    return 1
  }
  actual_image=$(drill::_docker inspect --format '{{.Config.Image}}' "$node") || return 1
  [[ $actual_image == "$(drill::target KIND_NODE_IMAGE)" ]] || {
    drill::die "Kind node image differs from the approved digest"
    return 1
  }
  drill::assert_managed_file "$kubeconfig" 600 "generated kubeconfig" || return 1
  current_context=$(drill::_kubectl config current-context) || return 1
  [[ $current_context == "$(drill::target context)" ]] || {
    drill::die "generated kubeconfig has an unexpected current context"
    return 1
  }
  drill::_kubectl wait node --all --for=condition=Ready --timeout="$ATLAS_DRILL_WAIT_TIMEOUT" > /dev/null || return 1

  drill::_verify_mount "$node" /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml "$policy_snapshot" false || {
    drill::die "approved audit policy snapshot mount is missing or writable"
    return 1
  }
  drill::_verify_mount "$node" /var/log/kubernetes/audit "$(drill::target audit_directory)" true || {
    drill::die "audit log mount is missing or read-only"
    return 1
  }
  drill::_docker exec "$node" grep -Fq -- '--audit-policy-file=/etc/kubernetes/policies/atlas-recovery-audit-policy.yaml' /etc/kubernetes/manifests/kube-apiserver.yaml || {
    drill::die "live API server audit-policy-file argument is missing"
    return 1
  }
  drill::_docker exec "$node" grep -Fq -- '--audit-log-path=/var/log/kubernetes/audit/kube-apiserver-audit.log' /etc/kubernetes/manifests/kube-apiserver.yaml || {
    drill::die "live API server audit-log-path argument is missing"
    return 1
  }
  mounted_sha=$(drill::_docker exec "$node" sha256sum /etc/kubernetes/policies/atlas-recovery-audit-policy.yaml | awk '{print $1}') || return 1
  [[ $mounted_sha == "$(drill::operation policy_sha)" ]] || {
    drill::die "mounted audit policy differs from the Gate-approved snapshot"
    return 1
  }
  drill::_kubectl get --raw=/readyz > /dev/null || return 1
  drill::_wait_for_audit_event || return 1
}

drill::_retained_state() {
  local cluster clusters containers kubeconfig_state
  cluster=$(drill::target cluster_name) || return 1
  clusters=$(drill::_kind get clusters --quiet 2> /dev/null) || clusters=DISCOVERY_ERROR
  containers=$(drill::_docker ps -a --filter "label=io.x-k8s.kind.cluster=${cluster}" --format '{{.Names}}' 2> /dev/null) || containers=DISCOVERY_ERROR
  containers=${containers//$'\n'/,}
  if [[ -e $(drill::target kubeconfig) || -L $(drill::target kubeconfig) ]]; then
    kubeconfig_state=PRESENT
  else
    kubeconfig_state=ABSENT
  fi
  printf 'kind=%s;containers=%s;kubeconfig=%s' "${clusters:-ABSENT}" "${containers:-ABSENT}" "$kubeconfig_state"
}

drill::_remove_temporary_directory() {
  local directory=$1
  [[ $directory == "${TMPDIR%/}/atlas-kind-drill."* && -d $directory && ! -L $directory ]] || return 1
  rm -rf -- "$directory"
}

drill::_create_cluster_inner() {
  local temporary_directory=$1 base_config ambient_after retained
  base_config="${temporary_directory}/kind-base.yaml"

  drill::_cluster_absent || return 1
  drill::render_base_kind_config "$base_config" || return 1
  drill::validate_kind_config "$base_config" "${ATLAS_DRILL_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml" || return 1
  drill::prepare_evidence "$base_config" || return 1
  drill::_human_gate || return 1

  if ! drill::revalidate_approved_inputs; then
    drill::journal_append PREMUTATION DENIED "approved inputs changed or became unavailable" || true
    return 1
  fi
  if ! drill::_verify_docker_target; then
    drill::journal_append PREMUTATION DENIED "Docker mutation target changed or became unavailable" || true
    return 1
  fi
  if ! drill::_verify_node_image; then
    drill::journal_append PREMUTATION DENIED "approved node image became unavailable" || true
    return 1
  fi
  if ! drill::_cluster_absent; then
    drill::journal_append PREMUTATION DENIED "cluster state changed after approval" || true
    return 1
  fi
  if ! drill::_verify_audit_directory_writable; then
    drill::journal_append PREMUTATION DENIED "audit directory write probe failed" || true
    return 1
  fi
  drill::journal_append CREATE STARTED "invoking digest-pinned Kind with Docker provider" || return 1

  if ! drill::_kind create cluster \
    --name "$(drill::target cluster_name)" \
    --image "$(drill::target KIND_NODE_IMAGE)" \
    --config "$(drill::operation config_file)" \
    --kubeconfig "$(drill::target kubeconfig)" \
    --wait "$ATLAS_DRILL_WAIT_TIMEOUT" \
    --retain; then
    retained=$(drill::_retained_state) || retained=INVENTORY_UNAVAILABLE
    drill::journal_append CREATE FAILED "$retained" || true
    drill::die "Kind creation failed; retained state and evidence require human review"
    return 1
  fi
  retained=$(drill::_retained_state) || retained=INVENTORY_UNAVAILABLE
  drill::journal_append CREATE SUCCEEDED "$retained" || return 1

  if ! drill::verify_cluster; then
    retained=$(drill::_retained_state) || retained=INVENTORY_UNAVAILABLE
    drill::journal_append VERIFY FAILED "$retained" || true
    return 1
  fi
  ambient_after="$(drill::operation evidence_session)/ambient-after.sha256"
  drill::_snapshot_ambient_kubeconfigs "$ambient_after" || return 1
  cmp -s "$(drill::operation ambient_before)" "$ambient_after" || {
    drill::journal_append VERIFY FAILED "ambient/default kubeconfig changed" || true
    drill::die "ambient/default kubeconfig changed during drill creation"
    return 1
  }
  drill::journal_append VERIFY READY "cluster, audit, ownership, and isolation checks passed" || return 1
  printf 'drill-cluster\tREADY\t%s\t%s\t%s\n' \
    "$(drill::target cluster_name)" "$(drill::target context)" "$(drill::operation evidence_session)"
}

drill::create_cluster() {
  local cluster_name=$1 context=$2 kubeconfig=$3 audit_directory=$4 evidence_root=$5 storage_assertion=$6
  local temporary_directory status release_status=0
  drill::resolve_target "$cluster_name" "$context" "$kubeconfig" "$audit_directory" "$evidence_root" "$storage_assertion" || return 1
  drill::_host_preflight || return 1
  drill::acquire_lifecycle_lock || return 1
  if ! temporary_directory=$(mktemp -d "${TMPDIR%/}/atlas-kind-drill.XXXXXX"); then
    drill::release_lifecycle_lock || true
    return 1
  fi

  if drill::_create_cluster_inner "$temporary_directory"; then
    status=0
  else
    status=$?
  fi
  drill::_remove_temporary_directory "$temporary_directory" || true
  drill::release_lifecycle_lock || release_status=$?
  ((release_status == 0)) || return "$release_status"
  return "$status"
}
