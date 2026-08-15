# shellcheck shell=bash

[[ -n ${_ATLAS_DRILL_EVIDENCE_LOADED:-} ]] && return 0
readonly _ATLAS_DRILL_EVIDENCE_LOADED=1

declare -gA ATLAS_DRILL_OPERATION=()
ATLAS_DRILL_JOURNAL_SEQUENCE=0
ATLAS_DRILL_JOURNAL_PREVIOUS_SHA=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
ATLAS_DRILL_JOURNAL_FILE_SHA=$ATLAS_DRILL_JOURNAL_PREVIOUS_SHA

drill::operation() {
  local key=$1
  [[ -n ${ATLAS_DRILL_OPERATION[$key]+present} ]] || {
    drill::die "internal operation key is unavailable: ${key}"
    return 1
  }
  printf '%s\n' "${ATLAS_DRILL_OPERATION[$key]}"
}

drill::_sha256() {
  local file=$1
  shasum -a 256 "$file" | awk '{print $1}'
}

drill::_json_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "$value"
}

drill::_git() {
  env -i PATH="$PATH" LC_ALL=C git --no-replace-objects "$@"
}

drill::_git_authority() {
  local repository toplevel status commit tree
  repository=$(cd "$ATLAS_DRILL_ROOT_DIR" && pwd -P) || return 1
  toplevel=$(drill::_git -C "$repository" rev-parse --show-toplevel) || return 1
  [[ $toplevel == "$repository" ]] || {
    drill::die "Git authority does not resolve to the Atlas repository root"
    return 1
  }
  status=$(drill::_git -C "$repository" status --porcelain=v1 --untracked-files=all) || return 1
  [[ -z $status ]] || {
    drill::die "the repository must be clean before lifecycle approval"
    return 1
  }
  commit=$(drill::_git -C "$repository" rev-parse --verify 'HEAD^{commit}') || return 1
  tree=$(drill::_git -C "$repository" rev-parse --verify 'HEAD^{tree}') || return 1
  [[ $commit =~ ^[0-9a-f]{40}$ && $tree =~ ^[0-9a-f]{40}$ ]] || {
    drill::die "Git commit or tree authority is malformed"
    return 1
  }
  printf '%s\t%s\n' "$commit" "$tree"
}

drill::_snapshot_ambient_kubeconfigs() {
  local destination=$1 candidate parent canonical hash
  local -a ambient_paths=()
  [[ ! -e $destination && ! -L $destination ]] || return 1
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
      hash=$(drill::_sha256 "$canonical") || return 1
      printf 'PRESENT\t%s\t%s\n' "$canonical" "$hash" >> "$destination" || return 1
    else
      printf 'ABSENT\t%s\n' "$candidate" >> "$destination" || return 1
    fi
  done
  chmod 0400 "$destination" || return 1
  drill::assert_managed_file "$destination" 400 "ambient kubeconfig snapshot" || return 1
}

drill::_create_evidence_session() {
  local fingerprint=$1 start_compact=$2 session_id=$3 root fingerprint_directory session_directory
  root=$(drill::target evidence_root) || return 1
  fingerprint_directory="${root}/${fingerprint}"
  if mkdir -m 0700 "$fingerprint_directory" 2> /dev/null; then
    :
  else
    drill::assert_managed_directory "$fingerprint_directory" "evidence fingerprint directory" || return 1
  fi
  session_directory="${fingerprint_directory}/${start_compact}-${session_id}"
  mkdir -m 0700 "$session_directory" 2> /dev/null || {
    drill::die "evidence session already exists; reuse is forbidden"
    return 1
  }
  drill::assert_managed_directory "$session_directory" "evidence session" || return 1
  printf '%s\n' "$session_directory"
}

drill::_write_plan() {
  local plan_file=$1 plan_sha_file=$2
  local actor action_id prepared_at fingerprint json plan_sha
  actor=$(drill::operation actor) || return 1
  action_id=$(drill::operation action_id) || return 1
  prepared_at=$(drill::operation prepared_at) || return 1
  fingerprint=$(drill::operation target_fingerprint) || return 1

  printf -v json '%s' \
    "{\"schemaVersion\":1,\"action\":\"cluster.create\",\"actionId\":\"$(drill::_json_escape "$action_id")\",\"actor\":\"$(drill::_json_escape "$actor")\",\"preparedAt\":\"${prepared_at}\",\"targetFingerprintSHA256\":\"${fingerprint}\",\"clusterName\":\"$(drill::_json_escape "$(drill::target cluster_name)")\",\"context\":\"$(drill::_json_escape "$(drill::target context)")\",\"kubeconfig\":\"$(drill::_json_escape "$(drill::target kubeconfig)")\",\"auditDirectory\":\"$(drill::_json_escape "$(drill::target audit_directory)")\",\"evidenceSession\":\"$(drill::_json_escape "$(drill::operation evidence_session)")\",\"storageAssertion\":\"$(drill::target storage_assertion)\",\"dockerContext\":\"$(drill::target docker_context)\",\"dockerEndpoint\":\"$(drill::_json_escape "$(drill::target docker_endpoint)")\",\"gitCommit\":\"$(drill::operation git_commit)\",\"gitTree\":\"$(drill::operation git_tree)\",\"kindConfigSHA256\":\"$(drill::operation config_sha)\",\"auditPolicySHA256\":\"$(drill::operation policy_sha)\",\"versionsLockSHA256\":\"$(drill::operation versions_sha)\",\"nodeImage\":\"$(drill::target KIND_NODE_IMAGE)\"}"
  printf '%s\n' "$json" > "$plan_file" || return 1
  chmod 0400 "$plan_file" || return 1
  plan_sha=$(drill::_sha256 "$plan_file") || return 1
  ATLAS_DRILL_OPERATION[plan_sha]=$plan_sha
  printf '%s  plan.json\n' "$plan_sha" > "$plan_sha_file" || return 1
  chmod 0400 "$plan_sha_file" || return 1
}

drill::_write_pre_mutation_manifest() {
  local pre_mutation_file=$1 pre_mutation_sha_file=$2 pre_mutation_sha approval_sha
  printf '%s  plan.json\n%s  kind.yaml\n%s  audit-policy.yaml\n%s  versions.lock\n%s  ambient-before.sha256\n' \
    "$(drill::operation plan_sha)" \
    "$(drill::operation config_sha)" \
    "$(drill::operation policy_sha)" \
    "$(drill::operation versions_sha)" \
    "$(drill::operation ambient_before_sha)" > "$pre_mutation_file" || return 1
  chmod 0400 "$pre_mutation_file" || return 1
  pre_mutation_sha=$(drill::_sha256 "$pre_mutation_file") || return 1
  ATLAS_DRILL_OPERATION[pre_mutation_sha]=$pre_mutation_sha
  printf '%s  pre-mutation.sha256\n' "$pre_mutation_sha" > "$pre_mutation_sha_file" || return 1
  chmod 0400 "$pre_mutation_sha_file" || return 1
  approval_sha=$(printf 'planSHA256=%s\npreMutationManifestSHA256=%s\n' \
    "$(drill::operation plan_sha)" "$pre_mutation_sha" | shasum -a 256 | awk '{print $1}') || return 1
  ATLAS_DRILL_OPERATION[approval_sha]=$approval_sha
}

drill::prepare_evidence() {
  local base_config=$1 authority git_commit git_tree policy_source versions_file
  local policy_sha versions_sha fingerprint_payload fingerprint start_utc start_compact session_id session
  local policy_snapshot versions_snapshot config_file ambient_before plan_file plan_sha_file
  local pre_mutation_file pre_mutation_sha_file journal_file actor

  authority=$(drill::_git_authority) || return 1
  IFS=$'\t' read -r git_commit git_tree <<< "$authority"
  policy_source="${ATLAS_DRILL_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml"
  versions_file="${ATLAS_DRILL_ROOT_DIR}/versions.lock"
  [[ -f $policy_source && ! -L $policy_source && -f $versions_file && ! -L $versions_file ]] || {
    drill::die "recovery authority inputs are missing or unsafe"
    return 1
  }
  policy_sha=$(drill::_sha256 "$policy_source") || return 1
  versions_sha=$(drill::_sha256 "$versions_file") || return 1
  printf -v fingerprint_payload '%s\n%s\n%s\n%s\n%s\n%s\n%s' \
    "$(drill::target cluster_name)" "$(drill::target context)" \
    "$(drill::target audit_directory)" "$(drill::target kubeconfig)" \
    "$(drill::target KIND_NODE_IMAGE)" "$(drill::target docker_context)" \
    "$(drill::target docker_endpoint)"
  fingerprint=$(printf '%s' "$fingerprint_payload" | shasum -a 256 | awk '{print $1}') || return 1
  start_utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  start_compact=$(date -u +%Y%m%dT%H%M%SZ) || return 1
  session_id=${ATLAS_DRILL_TARGET[cluster_name]##*-}
  session=$(drill::_create_evidence_session "$fingerprint" "$start_compact" "$session_id") || return 1

  policy_snapshot="${session}/audit-policy.yaml"
  versions_snapshot="${session}/versions.lock"
  config_file="${session}/kind.yaml"
  ambient_before="${session}/ambient-before.sha256"
  plan_file="${session}/plan.json"
  plan_sha_file="${session}/plan.sha256"
  pre_mutation_file="${session}/pre-mutation.sha256"
  pre_mutation_sha_file="${session}/pre-mutation-manifest.sha256"
  journal_file="${session}/journal.jsonl"
  install -m 0400 "$policy_source" "$policy_snapshot" || return 1
  install -m 0400 "$versions_file" "$versions_snapshot" || return 1
  drill::assert_managed_file "$policy_snapshot" 400 "audit policy snapshot" || return 1
  drill::assert_managed_file "$versions_snapshot" 400 "versions.lock snapshot" || return 1
  [[ $(drill::_sha256 "$policy_snapshot") == "$policy_sha" ]] || {
    drill::die "audit policy snapshot differs from the approved source"
    return 1
  }
  [[ $(drill::_sha256 "$versions_snapshot") == "$versions_sha" ]] || {
    drill::die "versions.lock snapshot differs from the approved source"
    return 1
  }
  drill::render_kind_config "$base_config" "$config_file" "$policy_snapshot" || return 1
  drill::validate_kind_config "$config_file" "$policy_snapshot" || return 1
  drill::assert_managed_file "$config_file" 400 "Kind configuration evidence" || return 1
  drill::_snapshot_ambient_kubeconfigs "$ambient_before" || return 1
  actor="$(id -u):$(id -un)" || return 1

  ATLAS_DRILL_OPERATION[actor]=$actor
  ATLAS_DRILL_OPERATION[prepared_at]=$start_utc
  ATLAS_DRILL_OPERATION[target_fingerprint]=$fingerprint
  ATLAS_DRILL_OPERATION[action_id]="$(drill::target cluster_name)-${start_compact}-${session_id}"
  ATLAS_DRILL_OPERATION[git_commit]=$git_commit
  ATLAS_DRILL_OPERATION[git_tree]=$git_tree
  ATLAS_DRILL_OPERATION[policy_sha]=$policy_sha
  ATLAS_DRILL_OPERATION[versions_sha]=$versions_sha
  ATLAS_DRILL_OPERATION[versions_snapshot]=$versions_snapshot
  ATLAS_DRILL_OPERATION[policy_snapshot]=$policy_snapshot
  ATLAS_DRILL_OPERATION[config_file]=$config_file
  ATLAS_DRILL_OPERATION[config_sha]=$(drill::_sha256 "$config_file") || return 1
  ATLAS_DRILL_OPERATION[ambient_before]=$ambient_before
  ATLAS_DRILL_OPERATION[ambient_before_sha]=$(drill::_sha256 "$ambient_before") || return 1
  ATLAS_DRILL_OPERATION[evidence_session]=$session
  ATLAS_DRILL_OPERATION[plan_file]=$plan_file
  ATLAS_DRILL_OPERATION[plan_sha_file]=$plan_sha_file
  ATLAS_DRILL_OPERATION[pre_mutation_file]=$pre_mutation_file
  ATLAS_DRILL_OPERATION[pre_mutation_sha_file]=$pre_mutation_sha_file
  ATLAS_DRILL_OPERATION[journal_file]=$journal_file
  drill::_write_plan "$plan_file" "$plan_sha_file" || return 1
  drill::_write_pre_mutation_manifest "$pre_mutation_file" "$pre_mutation_sha_file" || return 1
  : > "$journal_file" || return 1
  chmod 0600 "$journal_file" || return 1
  readonly -A ATLAS_DRILL_OPERATION
  drill::journal_append PREPARE READY "authority inputs sealed" || return 1
}

drill::journal_integrity() {
  local journal_file current_sha
  journal_file=$(drill::operation journal_file) || return 1
  drill::assert_managed_file "$journal_file" 600 "lifecycle journal" || return 1
  current_sha=$(drill::_sha256 "$journal_file") || return 1
  [[ $current_sha == "$ATLAS_DRILL_JOURNAL_FILE_SHA" ]] || {
    drill::die "lifecycle journal changed outside the current operation"
    return 1
  }
}

drill::journal_append() {
  local action=$1 outcome=$2 detail=$3 journal_file utc payload entry_sha entry
  drill::journal_integrity || return 1
  journal_file=$(drill::operation journal_file) || return 1
  utc=$(date -u +%Y-%m-%dT%H:%M:%SZ) || return 1
  ((ATLAS_DRILL_JOURNAL_SEQUENCE += 1))
  printf -v payload '%s' \
    "{\"sequence\":${ATLAS_DRILL_JOURNAL_SEQUENCE},\"previousEntrySHA256\":\"${ATLAS_DRILL_JOURNAL_PREVIOUS_SHA}\",\"utc\":\"${utc}\",\"actionId\":\"$(drill::_json_escape "$(drill::operation action_id)")\",\"actor\":\"$(drill::_json_escape "$(drill::operation actor)")\",\"action\":\"$(drill::_json_escape "$action")\",\"outcome\":\"$(drill::_json_escape "$outcome")\",\"detail\":\"$(drill::_json_escape "$detail")\",\"planSHA256\":\"$(drill::operation plan_sha)\",\"preMutationManifestSHA256\":\"$(drill::operation pre_mutation_sha)\",\"approvalSHA256\":\"$(drill::operation approval_sha)\",\"kindConfigSHA256\":\"$(drill::operation config_sha)\",\"auditPolicySHA256\":\"$(drill::operation policy_sha)\",\"versionsLockSHA256\":\"$(drill::operation versions_sha)\"}"
  entry_sha=$(printf '%s' "$payload" | shasum -a 256 | awk '{print $1}') || return 1
  entry="${payload%?},\"entrySHA256\":\"${entry_sha}\"}"
  printf '%s\n' "$entry" >> "$journal_file" || return 1
  ATLAS_DRILL_JOURNAL_PREVIOUS_SHA=$entry_sha
  ATLAS_DRILL_JOURNAL_FILE_SHA=$(drill::_sha256 "$journal_file") || return 1
}

drill::revalidate_approved_inputs() {
  local authority git_commit git_tree policy_source versions_file plan_sha_file
  local pre_mutation_sha_file recalculated_approval_sha
  authority=$(drill::_git_authority) || return 1
  IFS=$'\t' read -r git_commit git_tree <<< "$authority"
  [[ $git_commit == "$(drill::operation git_commit)" && $git_tree == "$(drill::operation git_tree)" ]] || {
    drill::die "Git commit or tree changed after approval"
    return 1
  }
  drill::revalidate_target_paths || return 1
  policy_source="${ATLAS_DRILL_ROOT_DIR}/clusters/kind/recovery-audit-policy.yaml"
  versions_file="${ATLAS_DRILL_ROOT_DIR}/versions.lock"
  [[ $(drill::_sha256 "$policy_source") == "$(drill::operation policy_sha)" ]] || {
    drill::die "audit policy changed after approval"
    return 1
  }
  [[ $(drill::_sha256 "$versions_file") == "$(drill::operation versions_sha)" ]] || {
    drill::die "versions.lock changed after approval"
    return 1
  }
  drill::assert_managed_directory "$(drill::operation evidence_session)" "evidence session" || return 1
  drill::assert_managed_file "$(drill::operation policy_snapshot)" 400 "audit policy snapshot" || return 1
  drill::assert_managed_file "$(drill::operation versions_snapshot)" 400 "versions.lock snapshot" || return 1
  drill::assert_managed_file "$(drill::operation ambient_before)" 400 "ambient kubeconfig snapshot" || return 1
  drill::assert_managed_file "$(drill::operation config_file)" 400 "Kind configuration evidence" || return 1
  drill::assert_managed_file "$(drill::operation plan_file)" 400 "lifecycle plan" || return 1
  plan_sha_file=$(drill::operation plan_sha_file) || return 1
  drill::assert_managed_file "$plan_sha_file" 400 "lifecycle plan hash" || return 1
  drill::assert_managed_file "$(drill::operation pre_mutation_file)" 400 "pre-mutation hash manifest" || return 1
  pre_mutation_sha_file=$(drill::operation pre_mutation_sha_file) || return 1
  drill::assert_managed_file "$pre_mutation_sha_file" 400 "pre-mutation manifest hash" || return 1
  [[ $(drill::_sha256 "$(drill::operation policy_snapshot)") == "$(drill::operation policy_sha)" ]] || {
    drill::die "audit policy snapshot changed after approval"
    return 1
  }
  [[ $(drill::_sha256 "$(drill::operation versions_snapshot)") == "$(drill::operation versions_sha)" ]] || {
    drill::die "versions.lock snapshot changed after approval"
    return 1
  }
  [[ $(drill::_sha256 "$(drill::operation ambient_before)") == "$(drill::operation ambient_before_sha)" ]] || {
    drill::die "ambient kubeconfig snapshot changed after approval"
    return 1
  }
  [[ $(drill::_sha256 "$(drill::operation config_file)") == "$(drill::operation config_sha)" ]] || {
    drill::die "Kind configuration changed after approval"
    return 1
  }
  [[ $(drill::_sha256 "$(drill::operation plan_file)") == "$(drill::operation plan_sha)" ]] || {
    drill::die "lifecycle plan changed after approval"
    return 1
  }
  grep -Fqx "$(drill::operation plan_sha)  plan.json" "$plan_sha_file" || {
    drill::die "lifecycle plan hash record changed after approval"
    return 1
  }
  [[ $(drill::_sha256 "$(drill::operation pre_mutation_file)") == "$(drill::operation pre_mutation_sha)" ]] || {
    drill::die "pre-mutation manifest changed after approval"
    return 1
  }
  grep -Fqx "$(drill::operation pre_mutation_sha)  pre-mutation.sha256" "$pre_mutation_sha_file" || {
    drill::die "pre-mutation manifest hash record changed after approval"
    return 1
  }
  recalculated_approval_sha=$(printf 'planSHA256=%s\npreMutationManifestSHA256=%s\n' \
    "$(drill::operation plan_sha)" "$(drill::operation pre_mutation_sha)" | shasum -a 256 | awk '{print $1}') || return 1
  [[ $recalculated_approval_sha == "$(drill::operation approval_sha)" ]] || {
    drill::die "Human Judgment approval anchor changed after approval"
    return 1
  }
  drill::validate_kind_config "$(drill::operation config_file)" "$(drill::operation policy_snapshot)" || return 1
  drill::journal_integrity || return 1
}
