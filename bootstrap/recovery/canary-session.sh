# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_CANARY_SESSION_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_CANARY_SESSION_LOADED=1
readonly ATLAS_PHASE0_ENVIRONMENT_NAME=local-orbstack

declare -gA ATLAS_PHASE0_TARGET=()
declare -gA ATLAS_PHASE0_OPERATION=()

ATLAS_PHASE0_JOURNAL_SEQUENCE=0
ATLAS_PHASE0_JOURNAL_PREVIOUS_SHA=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
ATLAS_PHASE0_JOURNAL_FILE_SHA=$ATLAS_PHASE0_JOURNAL_PREVIOUS_SHA
ATLAS_PHASE0_LOCK_PATH=''
ATLAS_PHASE0_LOCK_TOKEN=''
ATLAS_PHASE0_UNEXPECTED_EXIT_GUARD=false

phase0_session::arm_unexpected_exit_guard() {
  ATLAS_PHASE0_UNEXPECTED_EXIT_GUARD=true
}

phase0_session::disarm_unexpected_exit_guard() {
  ATLAS_PHASE0_UNEXPECTED_EXIT_GUARD=false
}

phase0_session::record_unexpected_exit() {
  local status=$1 journal
  [[ $ATLAS_PHASE0_UNEXPECTED_EXIT_GUARD == true ]] || return 0
  ATLAS_PHASE0_UNEXPECTED_EXIT_GUARD=false

  # An unexpected shell exit must never attempt cleanup or lock release. Append
  # a terminal record only when the approved session Journal is available; the
  # retained lock and runtime state remain evidence for human disposition.
  [[ -n ${ATLAS_PHASE0_OPERATION[journal_file]+present} ]] || return 0
  journal=${ATLAS_PHASE0_OPERATION[journal_file]}
  [[ -f $journal && ! -L $journal ]] || return 0
  phase0_session::journal_append RESULT FAILED_RETAINED \
    "unexpected shell exit status=${status}; runtime state and lock retained for human review"
}

phase0_session::_unexpected_exit_trap() {
  local status=$1 guarded=$ATLAS_PHASE0_UNEXPECTED_EXIT_GUARD
  trap - EXIT
  set +e
  set +u
  set +o pipefail
  [[ $guarded != true || $status -ne 0 ]] || status=1
  phase0_session::record_unexpected_exit "$status" ||
    printf 'atlas-recovery: failed to journal unexpected Phase-0 exit; retained state requires human review\n' >&2
  exit "$status"
}

phase0_session::install_unexpected_exit_trap() {
  trap 'phase0_session::_unexpected_exit_trap "$?"' EXIT
}

phase0_session::target() {
  local key=$1
  [[ -n ${ATLAS_PHASE0_TARGET[$key]+present} ]] || {
    recovery::die "internal Phase-0 target key is unavailable: ${key}"
    return 1
  }
  printf '%s\n' "${ATLAS_PHASE0_TARGET[$key]}"
}

phase0_session::operation() {
  local key=$1
  [[ -n ${ATLAS_PHASE0_OPERATION[$key]+present} ]] || {
    recovery::die "internal Phase-0 operation key is unavailable: ${key}"
    return 1
  }
  printf '%s\n' "${ATLAS_PHASE0_OPERATION[$key]}"
}

phase0_session::_sha256() {
  shasum -a 256 "$1" | awk '{print $1}'
}

phase0_session::_json_escape() {
  local value=$1 code octal character escape
  local LC_ALL=C
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  value=${value//$'\b'/\\b}
  value=${value//$'\f'/\\f}
  for ((code = 1; code < 32; code++)); do
    case "$code" in
      8 | 9 | 10 | 12 | 13) continue ;;
    esac
    printf -v octal '%03o' "$code"
    printf -v character '%b' "\\${octal}"
    printf -v escape '\\u%04x' "$code"
    value=${value//"$character"/$escape}
  done
  printf '%s' "$value"
}

phase0_session::_path_uid() {
  stat -f '%u' "$1"
}

phase0_session::_path_mode() {
  stat -f '%Lp' "$1"
}

phase0_session::_path_identity() {
  stat -f '%d:%i' "$1"
}

phase0_session::_path_has_extended_acl() {
  local listing permissions
  listing=$(LC_ALL=C ls -ld "$1") || return 2
  permissions=${listing%%[[:space:]]*}
  [[ $permissions == *+* ]]
}

phase0_session::assert_directory() {
  local directory=$1 label=$2 current_uid acl_status
  current_uid=$(id -u) || return 1
  [[ -d $directory && ! -L $directory ]] || {
    recovery::die "${label} must be an existing non-symlink directory"
    return 1
  }
  [[ $(phase0_session::_path_uid "$directory") == "$current_uid" ]] || {
    recovery::die "${label} must be owned by the current UID"
    return 1
  }
  [[ $(phase0_session::_path_mode "$directory") == 700 ]] || {
    recovery::die "${label} must have mode 0700"
    return 1
  }
  if phase0_session::_path_has_extended_acl "$directory"; then
    recovery::die "${label} must not have an extended ACL"
    return 1
  else
    acl_status=$?
    ((acl_status == 1)) || {
      recovery::die "${label} ACL state is unavailable"
      return 1
    }
  fi
}

phase0_session::assert_file() {
  local file=$1 expected_mode=$2 label=$3 current_uid acl_status
  current_uid=$(id -u) || return 1
  [[ -f $file && ! -L $file ]] || {
    recovery::die "${label} must be a regular non-symlink file"
    return 1
  }
  [[ $(phase0_session::_path_uid "$file") == "$current_uid" ]] || {
    recovery::die "${label} must be owned by the current UID"
    return 1
  }
  [[ $(phase0_session::_path_mode "$file") == "$expected_mode" ]] || {
    recovery::die "${label} must have mode 0${expected_mode}"
    return 1
  }
  if phase0_session::_path_has_extended_acl "$file"; then
    recovery::die "${label} must not have an extended ACL"
    return 1
  else
    acl_status=$?
    ((acl_status == 1)) || {
      recovery::die "${label} ACL state is unavailable"
      return 1
    }
  fi
}

phase0_session::_canonical_directory() {
  local requested=$1 label=$2 canonical repository
  [[ $requested == /* ]] || {
    recovery::die "${label} must be an absolute path"
    return 1
  }
  phase0_session::assert_directory "$requested" "$label" || return 1
  canonical=$(cd "$requested" && pwd -P) || return 1
  repository=$(cd "$ATLAS_RECOVERY_ROOT_DIR" && pwd -P) || return 1
  [[ $canonical != / && $canonical != "$repository" && $canonical != "${repository}/"* ]] || {
    recovery::die "${label} must remain outside the repository and filesystem root"
    return 1
  }
  printf '%s\n' "$canonical"
}

phase0_session::_canonical_file() {
  local requested=$1 expected_mode=$2 label=$3 parent canonical
  [[ $requested == /* ]] || {
    recovery::die "${label} must be an absolute path"
    return 1
  }
  phase0_session::assert_file "$requested" "$expected_mode" "$label" || return 1
  parent=$(cd "$(dirname "$requested")" && pwd -P) || return 1
  phase0_session::assert_directory "$parent" "${label} parent" || return 1
  canonical="${parent}/$(basename "$requested")"
  [[ $canonical != "${ATLAS_RECOVERY_ROOT_DIR}/"* ]] || {
    recovery::die "${label} must remain outside the repository"
    return 1
  }
  printf '%s\n' "$canonical"
}

phase0_session::_reject_shared_temporary_path() {
  local path=$1 label=$2 root
  for root in /tmp /private/tmp /var/tmp /private/var/tmp /dev/shm /usr/tmp; do
    [[ $path != "$root" && $path != "${root}/"* ]] || {
      recovery::die "${label} must remain outside shared temporary directories"
      return 1
    }
  done
}

phase0_session::_directory_empty() {
  local directory=$1
  (
    shopt -s dotglob nullglob
    local -a entries=("${directory}"/*)
    ((${#entries[@]} == 0))
  )
}

phase0_session::_paths_disjoint() {
  local first=$1 second=$2
  [[ $first != "$second" && $first != "${second}/"* && $second != "${first}/"* ]]
}

phase0_session::_locked_value() {
  local key=$1 file line value='' count=0
  file="${ATLAS_RECOVERY_ROOT_DIR}/versions.lock"
  [[ -f $file && ! -L $file ]] || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line == "${key}="* ]] || continue
    value=${line#*=}
    [[ $value =~ ^[^[:space:]]+$ ]] || return 1
    ((count += 1))
  done < "$file"
  ((count == 1)) || {
    recovery::die "versions.lock must contain ${key} exactly once"
    return 1
  }
  printf '%s\n' "$value"
}

phase0_session::resolve_target() {
  local cluster_name=$1 context=$2 admin_kubeconfig=$3 audit_directory=$4
  local creation_evidence=$5 evidence_root=$6 credential_directory=$7
  local storage_assertion=$8 known_good_revision=$9
  local recovery_generation=${10} previous_recovery_generation=${11}
  local authorizer_generation=${12} previous_authorizer_generation=${13}
  local expected_context resolved_admin resolved_audit resolved_creation resolved_evidence resolved_credentials key
  local admin_parent

  [[ $cluster_name =~ ^atlas-recovery-drill-[0-9]{8}t[0-9]{6}z-[0-9a-f]{8}$ ]] || {
    recovery::die "--cluster-name is not a canonical disposable drill name"
    return 1
  }
  expected_context="kind-${cluster_name}"
  [[ $context == "$expected_context" ]] || {
    recovery::die "--context must equal ${expected_context}"
    return 1
  }
  [[ $known_good_revision =~ ^[0-9a-f]{40}$ ]] || {
    recovery::die "--known-good-revision must be a full lowercase commit SHA"
    return 1
  }
  [[ $storage_assertion == encrypted-owner-controlled ]] || {
    recovery::die "--storage-assertion must equal encrypted-owner-controlled"
    return 1
  }
  principal_identity::validate_rotation "$recovery_generation" "$previous_recovery_generation" \
    "Recovery Operator" || return 1
  principal_identity::validate_rotation "$authorizer_generation" "$previous_authorizer_generation" \
    "Session Authorizer" || return 1

  resolved_admin=$(phase0_session::_canonical_file "$admin_kubeconfig" 600 "--admin-kubeconfig") || return 1
  [[ $(basename "$resolved_admin") == "${cluster_name}.kubeconfig" ]] || {
    recovery::die "--admin-kubeconfig basename must equal ${cluster_name}.kubeconfig"
    return 1
  }
  resolved_audit=$(phase0_session::_canonical_directory "$audit_directory" "--audit-dir") || return 1
  [[ $(basename "$resolved_audit") == "$cluster_name" ]] || {
    recovery::die "--audit-dir basename must equal the cluster name"
    return 1
  }
  resolved_creation=$(phase0_session::_canonical_directory "$creation_evidence" "--creation-evidence") || return 1
  resolved_evidence=$(phase0_session::_canonical_directory "$evidence_root" "--evidence-root") || return 1
  resolved_credentials=$(phase0_session::_canonical_directory "$credential_directory" "--credential-dir") || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_audit" "--audit-dir" || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_creation" "--creation-evidence" || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_evidence" "--evidence-root" || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_credentials" "--credential-dir" || return 1
  phase0_session::_directory_empty "$resolved_credentials" || {
    recovery::die "--credential-dir must be empty for a one-time ceremony"
    return 1
  }
  for key in "$resolved_audit" "$resolved_creation" "$resolved_evidence" "$resolved_credentials"; do
    phase0_session::_paths_disjoint "$resolved_admin" "$key" || {
      recovery::die "admin kubeconfig and managed directories must be disjoint"
      return 1
    }
  done
  phase0_session::_paths_disjoint "$resolved_audit" "$resolved_creation" || return 1
  phase0_session::_paths_disjoint "$resolved_audit" "$resolved_evidence" || return 1
  phase0_session::_paths_disjoint "$resolved_audit" "$resolved_credentials" || return 1
  phase0_session::_paths_disjoint "$resolved_creation" "$resolved_evidence" || return 1
  phase0_session::_paths_disjoint "$resolved_creation" "$resolved_credentials" || return 1
  phase0_session::_paths_disjoint "$resolved_evidence" "$resolved_credentials" || return 1

  admin_parent=$(dirname "$resolved_admin") || return 1

  ATLAS_PHASE0_TARGET[cluster_name]=$cluster_name
  ATLAS_PHASE0_TARGET[context]=$context
  ATLAS_PHASE0_TARGET[admin_kubeconfig]=$resolved_admin
  ATLAS_PHASE0_TARGET[admin_parent]=$admin_parent
  ATLAS_PHASE0_TARGET[audit_directory]=$resolved_audit
  ATLAS_PHASE0_TARGET[audit_log]="${resolved_audit}/kube-apiserver-audit.log"
  ATLAS_PHASE0_TARGET[creation_evidence]=$resolved_creation
  ATLAS_PHASE0_TARGET[evidence_root]=$resolved_evidence
  ATLAS_PHASE0_TARGET[credential_directory]=$resolved_credentials
  ATLAS_PHASE0_TARGET[storage_assertion]=$storage_assertion
  ATLAS_PHASE0_TARGET[known_good_revision]=$known_good_revision
  ATLAS_PHASE0_TARGET[environment_name]=$ATLAS_PHASE0_ENVIRONMENT_NAME
  ATLAS_PHASE0_TARGET[recovery_generation]=$recovery_generation
  ATLAS_PHASE0_TARGET[previous_recovery_generation]=$previous_recovery_generation
  ATLAS_PHASE0_TARGET[authorizer_generation]=$authorizer_generation
  ATLAS_PHASE0_TARGET[previous_authorizer_generation]=$previous_authorizer_generation
  ATLAS_PHASE0_TARGET[admin_kubeconfig_identity]=$(phase0_session::_path_identity "$resolved_admin") || return 1
  ATLAS_PHASE0_TARGET[admin_parent_identity]=$(phase0_session::_path_identity "$admin_parent") || return 1
  ATLAS_PHASE0_TARGET[audit_directory_identity]=$(phase0_session::_path_identity "$resolved_audit") || return 1
  ATLAS_PHASE0_TARGET[creation_evidence_identity]=$(phase0_session::_path_identity "$resolved_creation") || return 1
  ATLAS_PHASE0_TARGET[evidence_root_identity]=$(phase0_session::_path_identity "$resolved_evidence") || return 1
  ATLAS_PHASE0_TARGET[credential_directory_identity]=$(phase0_session::_path_identity "$resolved_credentials") || return 1
  for key in BASH_VERSION KUBECTL_VERSION KUBERNETES_VERSION KIND_NODE_IMAGE OPENSSL_VERSION YQ_VERSION; do
    ATLAS_PHASE0_TARGET[$key]=$(phase0_session::_locked_value "$key") || return 1
  done
  readonly -A ATLAS_PHASE0_TARGET
}

phase0_session::_version_triplet() {
  [[ $1 =~ ([0-9]+)\.([0-9]+)\.([0-9]+) ]] || return 1
  printf '%s.%s.%s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
}

phase0_session::_kubectl() {
  local kubeconfig=$1
  shift
  env -u KUBECONFIG -u KUBERC -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u NO_PROXY \
    -u http_proxy -u https_proxy -u all_proxy -u no_proxy \
    kubectl --kubeconfig "$kubeconfig" --context "$(phase0_session::target context)" \
    --request-timeout=20s "$@"
}

phase0_session::kubectl_config() {
  local kubeconfig=$1
  shift
  env -u KUBECONFIG -u KUBERC -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY -u NO_PROXY \
    -u http_proxy -u https_proxy -u all_proxy -u no_proxy \
    kubectl --kubeconfig "$kubeconfig" "$@"
}

phase0_session::admin() {
  phase0_session::_kubectl "$(phase0_session::target admin_kubeconfig)" "$@"
}

phase0_session::principal() {
  phase0_session::_kubectl "$1" "${@:2}"
}

phase0_session::_tool_preflight() {
  local tool actual
  [[ $(uname -s) == Darwin && $(uname -m) == arm64 ]] || {
    recovery::die "Phase-0 runtime drills require Darwin arm64"
    return 1
  }
  for tool in awk base64 cat chmod cmp cut date dirname env find git grep id install ls mkdir mktemp mv openssl rm rmdir sed shasum sleep sort stat tr wc kubectl yq; do
    command -v "$tool" > /dev/null 2>&1 || {
      recovery::die "required Phase-0 command is missing: ${tool}"
      return 1
    }
  done
  actual=$(phase0_session::_version_triplet "$BASH_VERSION") || return 1
  [[ $actual == "$(phase0_session::target BASH_VERSION)" ]] || return 1
  actual=$(kubectl version --client -o json | yq -r '.clientVersion.gitVersion | sub("^v"; "")') || return 1
  [[ $actual == "$(phase0_session::target KUBECTL_VERSION)" ]] || return 1
  actual=$(openssl version | awk '{print $2}') || return 1
  [[ $actual == "$(phase0_session::target OPENSSL_VERSION)" ]] || return 1
  actual=$(yq --version | awk '{sub(/^v/, "", $NF); print $NF}') || return 1
  [[ $actual == "$(phase0_session::target YQ_VERSION)" ]] || return 1
}

phase0_session::_git() {
  env -i PATH="$PATH" LC_ALL=C git --no-replace-objects \
    -c core.fsmonitor=false -c core.ignoreStat=false "$@"
}

phase0_session::_canonical_repository_url() {
  local url=$1 path owner repository
  case "$url" in
    https://github.com/*) path=${url#https://github.com/} ;;
    git@github.com:*) path=${url#git@github.com:} ;;
    ssh://git@github.com/*) path=${url#ssh://git@github.com/} ;;
    *)
      recovery::die "Git origin is not a supported canonical GitHub repository URL"
      return 1
      ;;
  esac
  path=${path%.git}
  [[ $path =~ ^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)$ ]] || {
    recovery::die "Git origin repository path is not canonical"
    return 1
  }
  owner=${BASH_REMATCH[1]}
  repository=${BASH_REMATCH[2]}
  printf 'https://github.com/%s/%s.git\n' "$owner" "$repository"
}

phase0_session::_profile_value() {
  local file=$1 key=$2 line value='' count=0
  [[ -f $file && ! -L $file ]] || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    [[ $line == "${key}="* ]] || continue
    value=${line#*=}
    [[ -n $value && $value != *[[:space:]]* ]] || return 1
    ((count += 1))
  done < "$file"
  ((count == 1)) || {
    recovery::die "Phase-0 environment Profile must contain ${key} exactly once"
    return 1
  }
  printf '%s\n' "$value"
}

phase0_session::_git_authority() {
  local repository toplevel sparse status commit tree descriptor process_id entry tag
  local remote_url repository_url profile profile_repository profile_environment environment_name
  repository=$(cd "$ATLAS_RECOVERY_ROOT_DIR" && pwd -P) || return 1
  toplevel=$(phase0_session::_git -C "$repository" rev-parse --show-toplevel) || return 1
  [[ $toplevel == "$repository" ]] || return 1
  if sparse=$(phase0_session::_git -C "$repository" config --bool --get core.sparseCheckout); then
    [[ $sparse == false ]] || return 1
  else
    status=$?
    ((status == 1)) || return 1
  fi
  exec {descriptor}< <(phase0_session::_git -C "$repository" ls-files -v -z) || return 1
  process_id=$!
  while IFS= read -r -d '' -u "$descriptor" entry; do
    tag=${entry:0:1}
    [[ $tag != S && ! $tag =~ [a-z] ]] || {
      exec {descriptor}<&-
      wait "$process_id" || true
      recovery::die "hidden Git index entries are forbidden for Phase-0 authority"
      return 1
    }
  done
  exec {descriptor}<&-
  wait "$process_id" || return 1
  status=$(phase0_session::_git -C "$repository" status --porcelain=v1 --untracked-files=all) || return 1
  [[ -z $status ]] || {
    recovery::die "the repository must be clean before Phase-0 approval"
    return 1
  }
  commit=$(phase0_session::_git -C "$repository" rev-parse --verify 'HEAD^{commit}') || return 1
  tree=$(phase0_session::_git -C "$repository" rev-parse --verify 'HEAD^{tree}') || return 1
  [[ $commit == "$(phase0_session::target known_good_revision)" ]] || {
    recovery::die "known-good revision does not equal the reviewed clean HEAD"
    return 1
  }
  environment_name=$(phase0_session::target environment_name) || return 1
  profile="${repository}/env/${environment_name}.env"
  profile_environment=$(phase0_session::_profile_value "$profile" ATLAS_ENVIRONMENT) || return 1
  [[ $profile_environment == "$environment_name" ]] || {
    recovery::die "Phase-0 environment does not match its reviewed Profile"
    return 1
  }
  profile_repository=$(phase0_session::_profile_value "$profile" ATLAS_GIT_REPO_URL) || return 1
  profile_repository=$(phase0_session::_canonical_repository_url "$profile_repository") || return 1
  remote_url=$(phase0_session::_git -C "$repository" remote get-url origin) || return 1
  repository_url=$(phase0_session::_canonical_repository_url "$remote_url") || return 1
  [[ $repository_url == "$profile_repository" ]] || {
    recovery::die "Git origin does not match the reviewed environment repository"
    return 1
  }
  printf '%s\t%s\t%s\t%s\n' "$commit" "$tree" "$repository_url" "$environment_name"
}

phase0_session::_verify_creation_journal() {
  local journal=$1 line sequence=0 previous=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
  local entry_sha recorded_previous recorded_sequence payload calculated suffix last_action='' last_outcome=''
  phase0_session::assert_file "$journal" 600 "cluster-creation journal" || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    [[ -n $line ]] || return 1
    recorded_sequence=$(yq -r '.sequence' <<< "$line") || return 1
    recorded_previous=$(yq -r '.previousEntrySHA256' <<< "$line") || return 1
    entry_sha=$(yq -r '.entrySHA256' <<< "$line") || return 1
    ((sequence += 1))
    [[ $recorded_sequence == "$sequence" && $recorded_previous == "$previous" && $entry_sha =~ ^[0-9a-f]{64}$ ]] || return 1
    suffix=",\"entrySHA256\":\"${entry_sha}\"}"
    [[ $line == *"$suffix" ]] || return 1
    payload=${line%"$suffix"}
    payload="${payload}}"
    calculated=$(printf '%s' "$payload" | shasum -a 256 | awk '{print $1}') || return 1
    [[ $calculated == "$entry_sha" ]] || return 1
    previous=$entry_sha
    last_action=$(yq -r '.action' <<< "$line") || return 1
    last_outcome=$(yq -r '.outcome' <<< "$line") || return 1
  done < "$journal"
  [[ $sequence -gt 0 && $last_action == VERIFY && $last_outcome == READY ]] || {
    recovery::die "cluster-creation evidence does not end in VERIFY/READY"
    return 1
  }
  ATLAS_PHASE0_OPERATION[creation_journal_tip]=$previous
}

phase0_session::_verify_creation_evidence() {
  local root plan plan_sha_file journal expected_plan_sha plan_sha audit_policy policy_sha repository_policy_sha
  local kind_config versions_snapshot pre_manifest pre_manifest_sha ambient_snapshot expected_record
  root=$(phase0_session::target creation_evidence) || return 1
  plan="${root}/plan.json"
  plan_sha_file="${root}/plan.sha256"
  journal="${root}/journal.jsonl"
  audit_policy="${root}/audit-policy.yaml"
  kind_config="${root}/kind.yaml"
  versions_snapshot="${root}/versions.lock"
  pre_manifest="${root}/pre-mutation.sha256"
  pre_manifest_sha="${root}/pre-mutation-manifest.sha256"
  ambient_snapshot="${root}/ambient-before.sha256"
  phase0_session::assert_file "$plan" 400 "cluster-creation plan" || return 1
  phase0_session::assert_file "$plan_sha_file" 400 "cluster-creation plan hash" || return 1
  phase0_session::assert_file "$audit_policy" 400 "cluster-creation audit policy" || return 1
  phase0_session::assert_file "$kind_config" 400 "cluster-creation Kind configuration" || return 1
  phase0_session::assert_file "$versions_snapshot" 400 "cluster-creation versions snapshot" || return 1
  phase0_session::assert_file "$pre_manifest" 400 "cluster-creation pre-mutation manifest" || return 1
  phase0_session::assert_file "$pre_manifest_sha" 400 "cluster-creation pre-mutation hash" || return 1
  phase0_session::assert_file "$ambient_snapshot" 400 "cluster-creation ambient kubeconfig snapshot" || return 1
  plan_sha=$(phase0_session::_sha256 "$plan") || return 1
  expected_plan_sha="${plan_sha}  plan.json"
  [[ $(< "$plan_sha_file") == "$expected_plan_sha" ]] || return 1
  [[ $(yq -r '.action' "$plan") == cluster.create &&
  $(yq -r '.clusterName' "$plan") == "$(phase0_session::target cluster_name)" &&
  $(yq -r '.context' "$plan") == "$(phase0_session::target context)" &&
  $(yq -r '.kubeconfig' "$plan") == "$(phase0_session::target admin_kubeconfig)" &&
  $(yq -r '.auditDirectory' "$plan") == "$(phase0_session::target audit_directory)" &&
  $(yq -r '.evidenceSession' "$plan") == "$root" &&
  $(yq -r '.dockerContext' "$plan") == orbstack &&
  $(yq -r '.nodeImage' "$plan") == "$(phase0_session::target KIND_NODE_IMAGE)" &&
  $(yq -r '.gitCommit' "$plan") =~ ^[0-9a-f]{40}$ &&
  $(yq -r '.gitTree' "$plan") =~ ^[0-9a-f]{40}$ &&
  $(yq -r '.storageAssertion' "$plan") == encrypted-owner-controlled ]] || {
    recovery::die "cluster-creation evidence targets another drill cluster"
    return 1
  }
  policy_sha=$(phase0_session::_sha256 "$audit_policy") || return 1
  [[ $policy_sha == "$(yq -r '.auditPolicySHA256' "$plan")" ]] || return 1
  repository_policy_sha=$(phase0_session::_sha256 "${ATLAS_RECOVERY_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml") || return 1
  [[ $policy_sha == "$repository_policy_sha" ]] || {
    recovery::die "drill cluster audit policy differs from the reviewed runtime revision"
    return 1
  }
  [[ $(phase0_session::_sha256 "$kind_config") == "$(yq -r '.kindConfigSHA256' "$plan")" &&
  $(phase0_session::_sha256 "$versions_snapshot") == "$(yq -r '.versionsLockSHA256' "$plan")" ]] || return 1
  expected_record="$(phase0_session::_sha256 "$pre_manifest")  pre-mutation.sha256"
  [[ $(< "$pre_manifest_sha") == "$expected_record" ]] || return 1
  expected_record="$(phase0_session::_sha256 "$plan")  plan.json"
  grep -Fqx "$expected_record" "$pre_manifest" || return 1
  expected_record="$(phase0_session::_sha256 "$kind_config")  kind.yaml"
  grep -Fqx "$expected_record" "$pre_manifest" || return 1
  expected_record="$(phase0_session::_sha256 "$audit_policy")  audit-policy.yaml"
  grep -Fqx "$expected_record" "$pre_manifest" || return 1
  expected_record="$(phase0_session::_sha256 "$versions_snapshot")  versions.lock"
  grep -Fqx "$expected_record" "$pre_manifest" || return 1
  expected_record="$(phase0_session::_sha256 "$ambient_snapshot")  ambient-before.sha256"
  grep -Fqx "$expected_record" "$pre_manifest" || return 1
  [[ $(wc -l < "$pre_manifest" | tr -d ' ') == 5 ]] || return 1
  ATLAS_PHASE0_OPERATION[creation_plan_sha]=$plan_sha
  ATLAS_PHASE0_OPERATION[audit_policy_sha]=$policy_sha
  phase0_session::_verify_creation_journal "$journal"
}

phase0_session::_admin_target() {
  local context whoami username groups cluster_view server ca_data ca_spki_sha
  local namespace_uid node_name ready server_version
  context=$(phase0_session::admin config current-context) || return 1
  [[ $context == "$(phase0_session::target context)" ]] || return 1
  whoami=$(phase0_session::admin auth whoami -o json) || return 1
  username=$(yq -r '.status.userInfo.username' <<< "$whoami") || return 1
  groups=$(yq -o=json -I=0 '.status.userInfo.groups | sort' <<< "$whoami") || return 1
  [[ $username == kubernetes-admin &&
    $groups == '["kubeadm:cluster-admins","system:authenticated"]' ]] || {
    recovery::die "admin kubeconfig is not the isolated Kind cluster authority"
    return 1
  }
  cluster_view=$(phase0_session::admin config view --raw --minify -o json) || return 1
  server=$(yq -r '.clusters[0].cluster.server' <<< "$cluster_view") || return 1
  [[ $server =~ ^https://127\.0\.0\.1:[0-9]+$ ]] || return 1
  ca_data=$(yq -r '.clusters[0].cluster."certificate-authority-data"' <<< "$cluster_view") || return 1
  [[ -n $ca_data && $ca_data != null ]] || return 1
  ca_spki_sha=$(phase0_session::_ca_spki_sha256 "$ca_data") || return 1
  namespace_uid=$(phase0_session::admin get namespace kube-system -o jsonpath='{.metadata.uid}') || return 1
  [[ $namespace_uid =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || return 1
  # shellcheck disable=SC2016 # $n is a yq variable, not a Shell expansion.
  node_name=$(phase0_session::admin get nodes -o json | yq -r '.items | length as $n | select($n == 1) | .[0].metadata.name') || return 1
  [[ $node_name == "$(phase0_session::target cluster_name)-control-plane" ]] || return 1
  ready=$(phase0_session::admin get node "$node_name" -o json | yq -r '.status.conditions[] | select(.type == "Ready") | .status') || return 1
  [[ $ready == "True" ]] || return 1
  server_version=$(phase0_session::admin version -o json | yq -r '.serverVersion.gitVersion | sub("^v"; "")') || return 1
  [[ $server_version == "$(phase0_session::target KUBERNETES_VERSION)" ]] || return 1

  ATLAS_PHASE0_OPERATION[namespace_uid]=$namespace_uid
  ATLAS_PHASE0_OPERATION[api_server]=$server
  ATLAS_PHASE0_OPERATION[ca_data]=$ca_data
  ATLAS_PHASE0_OPERATION[ca_spki_sha]=$ca_spki_sha
}

phase0_session::_ca_spki_sha256() {
  local ca_data=$1
  printf '%s' "$ca_data" | openssl base64 -d -A |
    openssl x509 -pubkey -noout |
    openssl pkey -pubin -outform DER |
    shasum -a 256 | awk '{print $1}'
}

phase0_session::_target_fingerprint() {
  local payload
  printf -v payload 'apiServerURL=%s\nkubeSystemNamespaceUID=%s\napiServerCASPKISHA256=%s\nrepositoryURL=%s\nenvironmentName=%s\n' \
    "$(phase0_session::operation api_server)" "$(phase0_session::operation namespace_uid)" \
    "$(phase0_session::operation ca_spki_sha)" "$(phase0_session::operation repository_url)" \
    "$(phase0_session::operation environment_name)"
  printf '%s' "$payload" | shasum -a 256 | awk '{print $1}'
}

phase0_session::_assert_runtime_absent() {
  local namespace resource name output retained
  local -a arguments
  while IFS=$'\t' read -r namespace resource name; do
    arguments=(get "$resource" "$name" --ignore-not-found -o name)
    [[ $namespace == cluster ]] || arguments+=(-n "$namespace")
    output=$(phase0_session::admin "${arguments[@]}") || return 1
    [[ -z $output ]] || {
      recovery::die "Phase-0 runtime object already exists: ${resource}/${name}"
      return 1
    }
  done << 'EOF'
cluster	validatingadmissionpolicy	atlas-bootstrap-admission-escape-canary
cluster	validatingadmissionpolicybinding	atlas-bootstrap-admission-escape-canary
cluster	clusterrole	atlas-bootstrap-break-glass-escape
cluster	clusterrolebinding	atlas-bootstrap-break-glass-escape
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-fence-authorization-canary
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-fence-authorization-canary
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-binding-shape-authorization-canary
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-binding-shape-authorization-canary
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-permission-authorization-canary
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-permission-authorization-canary
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-guard-authorization-canary
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-guard-authorization-canary
kube-system	configmap	atlas-bootstrap-admission-escape-canary
kube-system	role	atlas-bootstrap-recovery-canary
kube-system	role	atlas-bootstrap-recovery-authorizer-canary
kube-system	rolebinding	atlas-bootstrap-recovery-authorizer-canary
kube-system	configmap	atlas-bootstrap-recovery-guard-canary
kube-system	configmap	atlas-bootstrap-operation-fence-canary
EOF

  retained=$(phase0_session::admin get rolebindings -n kube-system -o json | yq -r '
    [.items[] | select(
      .metadata.name == "atlas-bg-canary-malformed" or
      (.metadata.name | test("^atlas-bg-canary-")) or
      (.metadata.name | test("^atlas-phase0-unrelated-")) or
      (has(.metadata.labels) and
        (.metadata.labels | has("atlas.io/recovery-session")))) |
      .metadata.name] | sort | join(",")
  ') || return 1
  [[ -z $retained ]] || {
    recovery::die "retained Phase-0 RoleBinding exists: ${retained}"
    return 1
  }

  retained=$(phase0_session::admin get certificatesigningrequests -o json | yq -r '
    [.items[] | select(
      (.metadata.name | test("^atlas-bg-recovery-")) or
      (.metadata.name | test("^atlas-bg-authorizer-")) or
      (has(.metadata.labels) and
        .metadata.labels["app.kubernetes.io/part-of"] == "atlas-recovery" and
        .metadata.labels["atlas.io/recovery-scope"] == "canary")) |
      .metadata.name] | sort | join(",")
  ') || return 1
  [[ -z $retained ]] || {
    recovery::die "retained Phase-0 CSR exists: ${retained}"
    return 1
  }
}

phase0_session::_audit_ready() {
  local log
  log=$(phase0_session::target audit_log) || return 1
  phase0_session::assert_file "$log" 600 "API audit log" || return 1
  [[ -s $log ]] || return 1
  grep -Fq '"requestURI":"/readyz"' "$log" || {
    recovery::die "API audit log lacks the cluster-creation readiness event"
    return 1
  }
}

phase0_session::revalidate_target_paths() {
  local resolved_admin resolved_audit resolved_creation resolved_evidence resolved_credentials
  local session path key expected_identity first second
  local -a directories

  resolved_admin=$(phase0_session::_canonical_file "$(phase0_session::target admin_kubeconfig)" 600 "--admin-kubeconfig") || return 1
  resolved_audit=$(phase0_session::_canonical_directory "$(phase0_session::target audit_directory)" "--audit-dir") || return 1
  resolved_creation=$(phase0_session::_canonical_directory "$(phase0_session::target creation_evidence)" "--creation-evidence") || return 1
  resolved_evidence=$(phase0_session::_canonical_directory "$(phase0_session::target evidence_root)" "--evidence-root") || return 1
  resolved_credentials=$(phase0_session::_canonical_directory "$(phase0_session::target credential_directory)" "--credential-dir") || return 1

  [[ $resolved_admin == "$(phase0_session::target admin_kubeconfig)" &&
  $(dirname "$resolved_admin") == "$(phase0_session::target admin_parent)" &&
  $resolved_audit == "$(phase0_session::target audit_directory)" &&
  $resolved_creation == "$(phase0_session::target creation_evidence)" &&
  $resolved_evidence == "$(phase0_session::target evidence_root)" &&
  $resolved_credentials == "$(phase0_session::target credential_directory)" ]] || {
    recovery::die "a managed Phase-0 path changed after approval"
    return 1
  }

  for key in admin_kubeconfig admin_parent audit_directory creation_evidence evidence_root credential_directory; do
    path=$(phase0_session::target "$key") || return 1
    expected_identity=$(phase0_session::target "${key}_identity") || return 1
    [[ $(phase0_session::_path_identity "$path") == "$expected_identity" ]] || {
      recovery::die "managed Phase-0 path identity changed after approval: ${key}"
      return 1
    }
  done

  phase0_session::_reject_shared_temporary_path "$resolved_audit" "--audit-dir" || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_creation" "--creation-evidence" || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_evidence" "--evidence-root" || return 1
  phase0_session::_reject_shared_temporary_path "$resolved_credentials" "--credential-dir" || return 1
  phase0_session::_directory_empty "$resolved_credentials" || {
    recovery::die "--credential-dir changed or is no longer empty after approval"
    return 1
  }

  directories=("$resolved_audit" "$resolved_creation" "$resolved_evidence" "$resolved_credentials")
  for ((first = 0; first < ${#directories[@]}; first++)); do
    phase0_session::_paths_disjoint "$resolved_admin" "${directories[first]}" || return 1
    for ((second = first + 1; second < ${#directories[@]}; second++)); do
      phase0_session::_paths_disjoint "${directories[first]}" "${directories[second]}" || return 1
    done
  done

  session=$(phase0_session::operation evidence_session) || return 1
  [[ $session == "${resolved_evidence}/"* ]] || return 1
  phase0_session::assert_directory "$session" "runtime evidence session" || return 1
  for path in authorization audit postflight; do
    phase0_session::assert_directory "${session}/${path}" "runtime evidence ${path} directory" || return 1
  done
}

phase0_session::_create_evidence_session() {
  local fingerprint=$1 start=$2 session_id=$3 root fingerprint_dir session_dir
  root=$(phase0_session::target evidence_root) || return 1
  fingerprint_dir="${root}/${fingerprint}"
  if ! mkdir -m 0700 "$fingerprint_dir" 2> /dev/null; then
    phase0_session::assert_directory "$fingerprint_dir" "runtime evidence fingerprint directory" || return 1
  fi
  session_dir="${fingerprint_dir}/${start}-${session_id}"
  mkdir -m 0700 "$session_dir" || {
    recovery::die "runtime evidence session already exists"
    return 1
  }
  printf '%s\n' "$session_dir"
}

phase0_session::_write_plan() {
  local plan=$1 json
  printf -v json '%s' \
    "{\"schemaVersion\":1,\"action\":\"phase0.canary-drill\",\"actionId\":\"$(phase0_session::_json_escape "$(phase0_session::operation action_id)")\",\"actor\":\"$(phase0_session::_json_escape "$(phase0_session::operation actor)")\",\"preparedAt\":\"$(phase0_session::operation prepared_at)\",\"clusterName\":\"$(phase0_session::target cluster_name)\",\"context\":\"$(phase0_session::target context)\",\"apiServer\":\"$(phase0_session::_json_escape "$(phase0_session::operation api_server)")\",\"apiServerCASPKISHA256\":\"$(phase0_session::operation ca_spki_sha)\",\"namespaceUID\":\"$(phase0_session::operation namespace_uid)\",\"repositoryURL\":\"$(phase0_session::_json_escape "$(phase0_session::operation repository_url)")\",\"environmentName\":\"$(phase0_session::operation environment_name)\",\"adminKubeconfig\":\"$(phase0_session::_json_escape "$(phase0_session::target admin_kubeconfig)")\",\"auditDirectory\":\"$(phase0_session::_json_escape "$(phase0_session::target audit_directory)")\",\"creationEvidence\":\"$(phase0_session::_json_escape "$(phase0_session::target creation_evidence)")\",\"evidenceSession\":\"$(phase0_session::_json_escape "$(phase0_session::operation evidence_session)")\",\"credentialDirectory\":\"$(phase0_session::_json_escape "$(phase0_session::target credential_directory)")\",\"credentialCustody\":\"separate-principal-subdirectories-single-operator-drill\",\"targetFingerprintSHA256\":\"$(phase0_session::operation target_fingerprint)\",\"sessionID\":\"$(phase0_session::operation session_id)\",\"operationID\":\"$(phase0_session::operation operation_id)\",\"recoveryGeneration\":$(phase0_session::target recovery_generation),\"previousRecoveryGeneration\":$(phase0_session::target previous_recovery_generation),\"authorizerGeneration\":$(phase0_session::target authorizer_generation),\"previousAuthorizerGeneration\":$(phase0_session::target previous_authorizer_generation),\"recoveryPrincipal\":\"$(phase0_session::operation recovery_principal)\",\"authorizerPrincipal\":\"$(phase0_session::operation authorizer_principal)\",\"previousRecoveryPrincipal\":\"$(phase0_session::operation previous_recovery_principal)\",\"previousAuthorizerPrincipal\":\"$(phase0_session::operation previous_authorizer_principal)\",\"knownGoodRevision\":\"$(phase0_session::target known_good_revision)\",\"gitTree\":\"$(phase0_session::operation git_tree)\",\"creationPlanSHA256\":\"$(phase0_session::operation creation_plan_sha)\",\"creationJournalTipSHA256\":\"$(phase0_session::operation creation_journal_tip)\",\"admissionBundleSHA256\":\"$(phase0_session::operation admission_bundle_sha)\",\"sessionBundleSHA256\":\"$(phase0_session::operation session_bundle_sha)\",\"versionsLockSHA256\":\"$(phase0_session::operation versions_sha)\",\"auditPolicySHA256\":\"$(phase0_session::operation audit_policy_sha)\",\"adminKubeconfigSHA256\":\"$(phase0_session::operation admin_kubeconfig_sha)\",\"storageAssertion\":\"$(phase0_session::target storage_assertion)\"}"
  printf '%s\n' "$json" > "$plan" || return 1
  chmod 0400 "$plan" || return 1
}

phase0_session::journal_integrity() {
  local current
  phase0_session::assert_file "$(phase0_session::operation journal_file)" 600 "Phase-0 journal" || return 1
  current=$(phase0_session::_sha256 "$(phase0_session::operation journal_file)") || return 1
  [[ $current == "$ATLAS_PHASE0_JOURNAL_FILE_SHA" ]]
}

phase0_session::journal_append() {
  local action=$1 outcome=$2 detail=$3 utc payload entry_sha entry
  phase0_session::journal_integrity || return 1
  utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  ((ATLAS_PHASE0_JOURNAL_SEQUENCE += 1))
  printf -v payload '%s' \
    "{\"sequence\":${ATLAS_PHASE0_JOURNAL_SEQUENCE},\"previousEntrySHA256\":\"${ATLAS_PHASE0_JOURNAL_PREVIOUS_SHA}\",\"utc\":\"${utc}\",\"actionId\":\"$(phase0_session::_json_escape "$(phase0_session::operation action_id)")\",\"actor\":\"$(phase0_session::_json_escape "$(phase0_session::operation actor)")\",\"action\":\"$(phase0_session::_json_escape "$action")\",\"outcome\":\"$(phase0_session::_json_escape "$outcome")\",\"detail\":\"$(phase0_session::_json_escape "$detail")\",\"planSHA256\":\"$(phase0_session::operation plan_sha)\",\"approvalSHA256\":\"$(phase0_session::operation approval_sha)\"}"
  entry_sha=$(printf '%s' "$payload" | shasum -a 256 | awk '{print $1}') || return 1
  entry="${payload%?},\"entrySHA256\":\"${entry_sha}\"}"
  printf '%s\n' "$entry" >> "$(phase0_session::operation journal_file)" || return 1
  ATLAS_PHASE0_JOURNAL_PREVIOUS_SHA=$entry_sha
  ATLAS_PHASE0_JOURNAL_FILE_SHA=$(phase0_session::_sha256 "$(phase0_session::operation journal_file)") || return 1
}

phase0_session::prepare() {
  local authority commit tree repository_url environment_name namespace_uid fingerprint start_utc start_compact
  local principal_plan recovery_principal authorizer_principal
  local previous_recovery_principal previous_authorizer_principal
  local session_id operation_id session admission_bundle session_bundle session_static authorizer_activation
  local versions_snapshot plan pre_manifest
  local plan_sha pre_sha approval_sha journal actor

  phase0_session::_verify_creation_evidence || return 1
  phase0_session::_admin_target || return 1
  phase0_session::_audit_ready || return 1
  phase0_session::_assert_runtime_absent || return 1
  authority=$(phase0_session::_git_authority) || return 1
  IFS=$'\t' read -r commit tree repository_url environment_name <<< "$authority"
  ATLAS_PHASE0_OPERATION[git_commit]=$commit
  ATLAS_PHASE0_OPERATION[git_tree]=$tree
  ATLAS_PHASE0_OPERATION[repository_url]=$repository_url
  ATLAS_PHASE0_OPERATION[environment_name]=$environment_name
  namespace_uid=$(phase0_session::operation namespace_uid) || return 1
  principal_plan=$(principal_identity::plan "$namespace_uid" \
    "$(phase0_session::target recovery_generation)" \
    "$(phase0_session::target previous_recovery_generation)" \
    "$(phase0_session::target authorizer_generation)" \
    "$(phase0_session::target previous_authorizer_generation)") || return 1
  IFS=$'\t' read -r recovery_principal authorizer_principal \
    previous_recovery_principal previous_authorizer_principal <<< "$principal_plan"
  ATLAS_PHASE0_OPERATION[recovery_principal]=$recovery_principal
  ATLAS_PHASE0_OPERATION[authorizer_principal]=$authorizer_principal
  ATLAS_PHASE0_OPERATION[previous_recovery_principal]=$previous_recovery_principal
  ATLAS_PHASE0_OPERATION[previous_authorizer_principal]=$previous_authorizer_principal
  session_id=$(openssl rand -hex 16) || return 1
  operation_id=$(openssl rand -hex 16) || return 1
  [[ $session_id =~ ^[0-9a-f]{32}$ && $operation_id =~ ^[0-9a-f]{32}$ ]] || return 1
  fingerprint=$(phase0_session::_target_fingerprint) || return 1
  start_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  start_compact=$(date -u +%Y%m%dT%H%M%SZ) || return 1
  session=$(phase0_session::_create_evidence_session "$fingerprint" "$start_compact" "$session_id") || return 1
  mkdir -m 0700 "${session}/authorization" "${session}/audit" "${session}/postflight" || return 1

  admission_bundle="${session}/authorization/admission-canary.yaml"
  session_bundle="${session}/authorization/session-authorization-canary.yaml"
  session_static="${session}/authorization/session-authorization-static.yaml"
  authorizer_activation="${session}/authorization/authorizer-activation.yaml"
  versions_snapshot="${session}/versions.lock"
  admission_canary::render_manifests "$(phase0_session::operation recovery_principal)" > "$admission_bundle" || return 1
  session_canary::render_manifests "$(phase0_session::operation recovery_principal)" \
    "$(phase0_session::operation authorizer_principal)" > "$session_bundle" || return 1
  yq ea 'select(.kind != "RoleBinding" or .metadata.name != "atlas-bootstrap-recovery-authorizer-canary")' \
    "$session_bundle" > "$session_static" || return 1
  yq ea 'select(.kind == "RoleBinding" and .metadata.name == "atlas-bootstrap-recovery-authorizer-canary")' \
    "$session_bundle" > "$authorizer_activation" || return 1
  [[ $(yq ea '[select(true)] | length' "$session_static") == 11 &&
  $(yq ea '[select(true)] | length' "$authorizer_activation") == 1 ]] || return 1
  install -m 0400 "${ATLAS_RECOVERY_ROOT_DIR}/versions.lock" "$versions_snapshot" || return 1
  chmod 0400 "$admission_bundle" "$session_bundle" "$session_static" "$authorizer_activation" || return 1

  actor="$(id -u):$(id -un)" || return 1
  ATLAS_PHASE0_OPERATION[actor]=$actor
  ATLAS_PHASE0_OPERATION[prepared_at]=$start_utc
  ATLAS_PHASE0_OPERATION[action_id]="phase0-${start_compact}-${session_id}"
  ATLAS_PHASE0_OPERATION[session_id]=$session_id
  ATLAS_PHASE0_OPERATION[operation_id]=$operation_id
  ATLAS_PHASE0_OPERATION[target_fingerprint]=$fingerprint
  ATLAS_PHASE0_OPERATION[evidence_session]=$session
  ATLAS_PHASE0_OPERATION[admission_bundle]=$admission_bundle
  ATLAS_PHASE0_OPERATION[session_bundle]=$session_bundle
  ATLAS_PHASE0_OPERATION[session_static_bundle]=$session_static
  ATLAS_PHASE0_OPERATION[authorizer_activation_bundle]=$authorizer_activation
  ATLAS_PHASE0_OPERATION[admission_bundle_sha]=$(phase0_session::_sha256 "$admission_bundle") || return 1
  ATLAS_PHASE0_OPERATION[session_bundle_sha]=$(phase0_session::_sha256 "$session_bundle") || return 1
  ATLAS_PHASE0_OPERATION[versions_snapshot]=$versions_snapshot
  ATLAS_PHASE0_OPERATION[versions_sha]=$(phase0_session::_sha256 "$versions_snapshot") || return 1
  ATLAS_PHASE0_OPERATION[admin_kubeconfig_sha]=$(phase0_session::_sha256 "$(phase0_session::target admin_kubeconfig)") || return 1

  plan="${session}/plan.json"
  pre_manifest="${session}/pre-mutation.sha256"
  journal="${session}/journal.jsonl"
  ATLAS_PHASE0_OPERATION[plan_file]=$plan
  ATLAS_PHASE0_OPERATION[pre_mutation_file]=$pre_manifest
  ATLAS_PHASE0_OPERATION[journal_file]=$journal
  phase0_session::_write_plan "$plan" || return 1
  plan_sha=$(phase0_session::_sha256 "$plan") || return 1
  ATLAS_PHASE0_OPERATION[plan_sha]=$plan_sha
  printf '%s  plan.json\n%s  authorization/admission-canary.yaml\n%s  authorization/session-authorization-canary.yaml\n%s  versions.lock\n' \
    "$plan_sha" "$(phase0_session::operation admission_bundle_sha)" \
    "$(phase0_session::operation session_bundle_sha)" "$(phase0_session::operation versions_sha)" > "$pre_manifest" || return 1
  chmod 0400 "$pre_manifest" || return 1
  pre_sha=$(phase0_session::_sha256 "$pre_manifest") || return 1
  ATLAS_PHASE0_OPERATION[pre_mutation_sha]=$pre_sha
  approval_sha=$(printf 'planSHA256=%s\npreMutationSHA256=%s\n' "$plan_sha" "$pre_sha" | shasum -a 256 | awk '{print $1}') || return 1
  ATLAS_PHASE0_OPERATION[approval_sha]=$approval_sha
  : > "$journal" || return 1
  chmod 0600 "$journal" || return 1
  phase0_session::journal_append PREPARE READY "runtime authority inputs sealed" || return 1
}

phase0_session::_terminal_available() {
  [[ -t 0 && -t 1 && -r /dev/tty && -w /dev/tty ]]
}

phase0_session::human_gate() {
  local expected response
  expected="RUN PHASE0 $(phase0_session::target cluster_name) $(phase0_session::operation approval_sha)"
  phase0_session::journal_append GATE PROMPTED "Phase-0 canary ceremony approval requested" || return 1
  phase0_session::_terminal_available || {
    phase0_session::journal_append GATE DENIED "interactive terminal unavailable" || true
    recovery::die "Phase-0 canary drill requires an interactive terminal"
    return 1
  }
  {
    printf 'Human Judgment Gate: Phase-0 canary runtime\n'
    printf 'cluster=%s\n' "$(phase0_session::target cluster_name)"
    printf 'context=%s\n' "$(phase0_session::target context)"
    printf 'adminKubeconfig=%s\n' "$(phase0_session::target admin_kubeconfig)"
    printf 'auditDirectory=%s\n' "$(phase0_session::target audit_directory)"
    printf 'creationEvidence=%s\n' "$(phase0_session::target creation_evidence)"
    printf 'repositoryURL=%s\n' "$(phase0_session::operation repository_url)"
    printf 'environmentName=%s\n' "$(phase0_session::operation environment_name)"
    printf 'targetFingerprintSHA256=%s\n' "$(phase0_session::operation target_fingerprint)"
    printf 'knownGoodRevision=%s\n' "$(phase0_session::target known_good_revision)"
    printf 'recoveryPrincipal=%s\n' "$(phase0_session::operation recovery_principal)"
    printf 'authorizerPrincipal=%s\n' "$(phase0_session::operation authorizer_principal)"
    printf 'previousRecoveryPrincipal=%s\n' "$(phase0_session::operation previous_recovery_principal)"
    printf 'previousAuthorizerPrincipal=%s\n' "$(phase0_session::operation previous_authorizer_principal)"
    printf 'evidenceSession=%s\n' "$(phase0_session::operation evidence_session)"
    printf 'credentialDirectory=%s\n' "$(phase0_session::target credential_directory)"
    printf 'credentialCustody=separate-principal-subdirectories-single-operator-drill\n'
    printf 'planSHA256=%s\n' "$(phase0_session::operation plan_sha)"
    printf 'preMutationSHA256=%s\n' "$(phase0_session::operation pre_mutation_sha)"
    printf 'Type exactly: %s\n> ' "$expected"
  } > /dev/tty || return 1
  IFS= read -r response < /dev/tty || return 1
  [[ $response == "$expected" ]] || {
    phase0_session::journal_append GATE DENIED "challenge mismatch" || true
    recovery::die "Phase-0 Human Judgment challenge did not match"
    return 1
  }
  phase0_session::journal_append GATE APPROVED "exact plan-bound challenge matched"
}

phase0_session::_revalidate_git_authority() {
  local authority commit tree repository_url environment_name
  authority=$(phase0_session::_git_authority) || return 1
  IFS=$'\t' read -r commit tree repository_url environment_name <<< "$authority"
  [[ $commit == "$(phase0_session::operation git_commit)" &&
  $tree == "$(phase0_session::operation git_tree)" &&
  $repository_url == "$(phase0_session::operation repository_url)" &&
  $environment_name == "$(phase0_session::operation environment_name)" ]] || {
    recovery::die "reviewed Git or environment authority changed after approval"
    return 1
  }
}

phase0_session::_revalidate_live_target() {
  local expected_namespace_uid expected_api_server expected_ca_data expected_ca_spki expected_fingerprint
  expected_namespace_uid=$(phase0_session::operation namespace_uid) || return 1
  expected_api_server=$(phase0_session::operation api_server) || return 1
  expected_ca_data=$(phase0_session::operation ca_data) || return 1
  expected_ca_spki=$(phase0_session::operation ca_spki_sha) || return 1
  expected_fingerprint=$(phase0_session::operation target_fingerprint) || return 1

  phase0_session::_admin_target || return 1
  [[ $(phase0_session::operation namespace_uid) == "$expected_namespace_uid" &&
  $(phase0_session::operation api_server) == "$expected_api_server" &&
  $(phase0_session::operation ca_data) == "$expected_ca_data" &&
  $(phase0_session::operation ca_spki_sha) == "$expected_ca_spki" &&
  $(phase0_session::_target_fingerprint) == "$expected_fingerprint" ]] || {
    recovery::die "drill cluster authority changed after approval"
    return 1
  }
  principal_identity::validate_plan "$expected_namespace_uid" \
    "$(phase0_session::target recovery_generation)" \
    "$(phase0_session::target previous_recovery_generation)" \
    "$(phase0_session::target authorizer_generation)" \
    "$(phase0_session::target previous_authorizer_generation)" \
    "$(phase0_session::operation recovery_principal)" \
    "$(phase0_session::operation authorizer_principal)" \
    "$(phase0_session::operation previous_recovery_principal)" \
    "$(phase0_session::operation previous_authorizer_principal)"
}

phase0_session::revalidate() {
  local expected_creation_plan expected_creation_journal expected_audit_policy
  expected_creation_plan=$(phase0_session::operation creation_plan_sha) || return 1
  expected_creation_journal=$(phase0_session::operation creation_journal_tip) || return 1
  expected_audit_policy=$(phase0_session::operation audit_policy_sha) || return 1
  phase0_session::revalidate_target_paths || return 1
  phase0_session::_revalidate_git_authority || return 1
  phase0_session::assert_file "$(phase0_session::target admin_kubeconfig)" 600 "--admin-kubeconfig" || return 1
  [[ $(phase0_session::_sha256 "$(phase0_session::target admin_kubeconfig)") == "$(phase0_session::operation admin_kubeconfig_sha)" ]] || return 1
  [[ $(phase0_session::_sha256 "$(phase0_session::operation admission_bundle)") == "$(phase0_session::operation admission_bundle_sha)" ]] || return 1
  [[ $(phase0_session::_sha256 "$(phase0_session::operation session_bundle)") == "$(phase0_session::operation session_bundle_sha)" ]] || return 1
  cmp -s "$(phase0_session::operation session_static_bundle)" \
    <(yq ea 'select(.kind != "RoleBinding" or .metadata.name != "atlas-bootstrap-recovery-authorizer-canary")' \
      "$(phase0_session::operation session_bundle)") || return 1
  cmp -s "$(phase0_session::operation authorizer_activation_bundle)" \
    <(yq ea 'select(.kind == "RoleBinding" and .metadata.name == "atlas-bootstrap-recovery-authorizer-canary")' \
      "$(phase0_session::operation session_bundle)") || return 1
  [[ $(phase0_session::_sha256 "$(phase0_session::operation versions_snapshot)") == "$(phase0_session::operation versions_sha)" ]] || return 1
  [[ $(phase0_session::_sha256 "$(phase0_session::operation plan_file)") == "$(phase0_session::operation plan_sha)" ]] || return 1
  [[ $(phase0_session::_sha256 "$(phase0_session::operation pre_mutation_file)") == "$(phase0_session::operation pre_mutation_sha)" ]] || return 1
  phase0_session::_verify_creation_evidence || return 1
  [[ $(phase0_session::operation creation_plan_sha) == "$expected_creation_plan" &&
  $(phase0_session::operation creation_journal_tip) == "$expected_creation_journal" &&
  $(phase0_session::operation audit_policy_sha) == "$expected_audit_policy" ]] || {
    recovery::die "cluster-creation evidence changed after approval"
    return 1
  }
  phase0_session::_revalidate_live_target || return 1
  phase0_session::_audit_ready || return 1
  phase0_session::_assert_runtime_absent || return 1
  phase0_session::journal_integrity
}

phase0_session::acquire_lock() {
  local root owner started
  root="/tmp/atlas-phase0-runtime-locks-$(id -u)"
  if ! mkdir -m 0700 "$root" 2> /dev/null; then
    phase0_session::assert_directory "$root" "Phase-0 lock root" || return 1
  fi
  ATLAS_PHASE0_LOCK_PATH="${root}/$(phase0_session::target cluster_name).lock"
  mkdir -m 0700 "$ATLAS_PHASE0_LOCK_PATH" 2> /dev/null || {
    recovery::die "Phase-0 runtime lock already exists; stale locks require human review"
    ATLAS_PHASE0_LOCK_PATH=''
    return 1
  }
  ATLAS_PHASE0_LOCK_TOKEN="$(phase0_session::target cluster_name):${BASHPID}:${RANDOM}${RANDOM}"
  started=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  owner="${ATLAS_PHASE0_LOCK_PATH}/owner"
  printf 'token=%s\npid=%s\nstartedAt=%s\n' "$ATLAS_PHASE0_LOCK_TOKEN" "$BASHPID" "$started" > "$owner" || return 1
  chmod 0600 "$owner" || return 1
}

phase0_session::release_lock() {
  local owner expected actual
  [[ -n $ATLAS_PHASE0_LOCK_PATH && -n $ATLAS_PHASE0_LOCK_TOKEN ]] || return 0
  owner="${ATLAS_PHASE0_LOCK_PATH}/owner"
  phase0_session::assert_file "$owner" 600 "Phase-0 lock owner" || return 1
  expected="token=${ATLAS_PHASE0_LOCK_TOKEN}"
  actual=$(sed -n '1p' "$owner") || return 1
  [[ $actual == "$expected" ]] || return 1
  rm -f -- "$owner" || return 1
  rmdir "$ATLAS_PHASE0_LOCK_PATH" || return 1
  ATLAS_PHASE0_LOCK_PATH=''
  ATLAS_PHASE0_LOCK_TOKEN=''
}
