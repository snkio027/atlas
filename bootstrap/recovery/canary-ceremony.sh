# shellcheck shell=bash

[[ -n ${_ATLAS_RECOVERY_CANARY_CEREMONY_LOADED:-} ]] && return 0
readonly _ATLAS_RECOVERY_CANARY_CEREMONY_LOADED=1

readonly ATLAS_PHASE0_CERTIFICATE_SECONDS=3600
readonly ATLAS_PHASE0_WAIT_ATTEMPTS=30
readonly ATLAS_PHASE0_GUARD_VALUE='p, role:atlas-recovery-guard-canary, applications, *, */*, deny'

phase0_ceremony::_evidence_file() {
  printf '%s/%s\n' "$(phase0_session::operation evidence_session)" "$1"
}

phase0_ceremony::_record_object() {
  local kubeconfig=$1 resource=$2 name=$3 destination=$4
  local -a arguments=(get "$resource" "$name" -o json)
  case "$resource" in
    configmap | role | rolebinding) arguments+=(-n kube-system) ;;
  esac
  phase0_session::_kubectl "$kubeconfig" "${arguments[@]}" > "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_write_delete_options() {
  local snapshot=$1 destination=$2 uid resource_version
  uid=$(yq -r '.metadata.uid' "$snapshot") || return 1
  resource_version=$(yq -r '.metadata.resourceVersion' "$snapshot") || return 1
  [[ $uid =~ ^[0-9a-f-]{36}$ && $resource_version =~ ^[0-9]+$ ]] || return 1
  printf '{"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":"%s","resourceVersion":"%s"}}\n' \
    "$uid" "$resource_version" > "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_delete_with_preconditions() {
  local kubeconfig=$1 uri=$2 snapshot=$3 label=$4 options
  options="$(phase0_ceremony::_evidence_file "postflight/delete-${label}.json")"
  phase0_ceremony::_write_delete_options "$snapshot" "$options" || return 1
  phase0_session::_kubectl "$kubeconfig" delete --raw "$uri" -f "$options" > /dev/null || return 1
}

phase0_ceremony::_wait_for_certificate() {
  local csr_name=$1 attempt certificate
  for ((attempt = 0; attempt < ATLAS_PHASE0_WAIT_ATTEMPTS; attempt++)); do
    certificate=$(phase0_session::admin get certificatesigningrequest "$csr_name" \
      -o jsonpath='{.status.certificate}') || return 1
    if [[ -n $certificate ]]; then
      printf '%s\n' "$certificate"
      return 0
    fi
    sleep 1
  done
  recovery::die "certificate was not issued: ${csr_name}"
}

phase0_ceremony::_write_principal_kubeconfig() {
  local username=$1 key=$2 certificate=$3 ca_file=$4 destination=$5 context
  context=$(phase0_session::target context) || return 1
  [[ ! -e $destination && ! -L $destination ]] || return 1
  phase0_session::kubectl_config "$destination" config set-cluster phase0-drill \
    --server="$(phase0_session::operation api_server)" \
    --certificate-authority="$ca_file" > /dev/null || return 1
  phase0_session::kubectl_config "$destination" config set-credentials phase0-principal \
    --client-certificate="$certificate" --client-key="$key" > /dev/null || return 1
  phase0_session::kubectl_config "$destination" config set-context "$context" \
    --cluster=phase0-drill --user=phase0-principal > /dev/null || return 1
  phase0_session::kubectl_config "$destination" config use-context "$context" > /dev/null || return 1
  chmod 0600 "$destination" || return 1
  phase0_session::assert_file "$destination" 600 "generated principal kubeconfig" || return 1
  ! grep -Eq 'client-key-data|client-certificate-data' "$destination" || {
    recovery::die "generated principal kubeconfig embeds credential material"
    return 1
  }
  [[ $(phase0_session::principal "$destination" auth whoami -o json | yq -r '.status.userInfo.username') == "$username" ]] || {
    recovery::die "issued credential authenticates as an unexpected principal"
    return 1
  }
}

phase0_ceremony::_write_csr_manifest() {
  local destination=$1 csr_name=$2 certificate_request=$3
  ATLAS_JSON_CSR_NAME=$csr_name \
    ATLAS_JSON_CERTIFICATE_REQUEST=$certificate_request \
    ATLAS_JSON_CERTIFICATE_SECONDS=$ATLAS_PHASE0_CERTIFICATE_SECONDS \
    yq -n -o=json -I=0 '
      {
        "apiVersion": "certificates.k8s.io/v1",
        "kind": "CertificateSigningRequest",
        "metadata": {
          "name": strenv(ATLAS_JSON_CSR_NAME),
          "labels": {
            "app.kubernetes.io/part-of": "atlas-recovery",
            "atlas.io/recovery-scope": "canary"
          }
        },
        "spec": {
          "request": strenv(ATLAS_JSON_CERTIFICATE_REQUEST),
          "signerName": "kubernetes.io/kube-apiserver-client",
          "expirationSeconds": (strenv(ATLAS_JSON_CERTIFICATE_SECONDS) | tonumber),
          "usages": ["digital signature", "client auth"]
        }
      }
    ' > "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_issue_principal() {
  local role=$1 username=$2 csr_name=$3 directory=$4 ca_file=$5
  local key csr certificate kubeconfig csr_manifest csr_snapshot certificate_data
  local metadata_file serial fingerprint not_before not_after namespace_uid
  key="${directory}/${role}.key"
  csr="${directory}/${role}.csr"
  certificate="${directory}/${role}.crt"
  kubeconfig="${directory}/${role}.kubeconfig"
  csr_manifest="$(phase0_ceremony::_evidence_file "authorization/${role}-csr.json")"
  csr_snapshot="$(phase0_ceremony::_evidence_file "authorization/${role}-csr-issued.json")"
  metadata_file="$(phase0_ceremony::_evidence_file "authorization/${role}-certificate.json")"

  namespace_uid=$(phase0_session::operation namespace_uid) || return 1
  case "$role" in
    recovery | previous_recovery)
      principal_identity::validate_recovery_operator "$username" "$namespace_uid" || return 1
      ;;
    authorizer | previous_authorizer)
      principal_identity::validate_session_authorizer "$username" "$namespace_uid" || return 1
      ;;
    *)
      recovery::die "unknown credential principal role: ${role}"
      return 1
      ;;
  esac

  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$key" > /dev/null 2>&1 || return 1
  chmod 0600 "$key" || return 1
  openssl req -new -sha256 -key "$key" -subj "/CN=${username}" -out "$csr" || return 1
  chmod 0600 "$csr" || return 1
  principal_identity::validate_csr "$role" "$username" "$csr" || return 1
  certificate_data=$(base64 < "$csr" | tr -d '\n') || return 1
  phase0_ceremony::_write_csr_manifest "$csr_manifest" "$csr_name" "$certificate_data" || return 1

  phase0_session::journal_append "CREDENTIAL_${role^^}" STARTED "creating and approving isolated CSR ${csr_name}" || return 1
  phase0_session::admin create --validate=strict -f "$csr_manifest" > /dev/null || return 1
  phase0_session::admin certificate approve "$csr_name" > /dev/null || return 1
  certificate_data=$(phase0_ceremony::_wait_for_certificate "$csr_name") || return 1
  printf '%s' "$certificate_data" | base64 -D > "$certificate" || return 1
  chmod 0600 "$certificate" || return 1
  phase0_ceremony::_record_object "$(phase0_session::target admin_kubeconfig)" \
    certificatesigningrequest "$csr_name" "$csr_snapshot" || return 1
  [[ $(yq -r '.spec.signerName' "$csr_snapshot") == kubernetes.io/kube-apiserver-client &&
  $(yq -o=json -I=0 '.spec.usages' "$csr_snapshot") == '["digital signature","client auth"]' &&
  $(yq -r '.spec.expirationSeconds' "$csr_snapshot") == "$ATLAS_PHASE0_CERTIFICATE_SECONDS" ]] || return 1

  principal_identity::validate_certificate "$role" "$username" "$certificate" || return 1
  cmp -s <(openssl pkey -in "$key" -pubout 2> /dev/null) <(openssl x509 -in "$certificate" -pubkey -noout) || return 1
  openssl x509 -in "$certificate" -checkend 600 -noout > /dev/null || return 1
  if openssl x509 -in "$certificate" -checkend $((ATLAS_PHASE0_CERTIFICATE_SECONDS + 60)) -noout > /dev/null; then
    recovery::die "issued certificate lifetime exceeds the requested drill window"
    return 1
  fi
  serial=$(openssl x509 -in "$certificate" -noout -serial | cut -d= -f2) || return 1
  fingerprint=$(openssl x509 -in "$certificate" -noout -fingerprint -sha256 | cut -d= -f2 | tr -d ':') || return 1
  not_before=$(openssl x509 -in "$certificate" -noout -startdate | cut -d= -f2-) || return 1
  not_after=$(openssl x509 -in "$certificate" -noout -enddate | cut -d= -f2-) || return 1
  printf '{"username":"%s","csrName":"%s","serial":"%s","fingerprintSHA256":"%s","notBefore":"%s","notAfter":"%s","signerName":"kubernetes.io/kube-apiserver-client","expirationSeconds":%d,"organizations":[]}\n' \
    "$(phase0_session::_json_escape "$username")" "$csr_name" "$serial" "$fingerprint" \
    "$(phase0_session::_json_escape "$not_before")" "$(phase0_session::_json_escape "$not_after")" \
    "$ATLAS_PHASE0_CERTIFICATE_SECONDS" > "$metadata_file" || return 1
  chmod 0400 "$metadata_file" || return 1

  phase0_ceremony::_write_principal_kubeconfig "$username" "$key" "$certificate" "$ca_file" "$kubeconfig" || return 1
  phase0_ceremony::_delete_with_preconditions "$(phase0_session::target admin_kubeconfig)" \
    "/apis/certificates.k8s.io/v1/certificatesigningrequests/${csr_name}" "$csr_snapshot" "${role}-csr" || return 1
  ATLAS_PHASE0_OPERATION["${role}_key"]=$key
  ATLAS_PHASE0_OPERATION["${role}_csr"]=$csr
  ATLAS_PHASE0_OPERATION["${role}_certificate"]=$certificate
  ATLAS_PHASE0_OPERATION["${role}_kubeconfig"]=$kubeconfig
  ATLAS_PHASE0_OPERATION["${role}_csr_name"]=$csr_name
  phase0_session::journal_append "CREDENTIAL_${role^^}" ISSUED "short-lived exact-user credential verified; CSR removed" || return 1
}

phase0_ceremony::_issue_credentials() {
  local root recovery_directory authorizer_directory recovery_ca authorizer_ca prefix
  root=$(phase0_session::target credential_directory) || return 1
  recovery_directory="${root}/recovery-operator"
  authorizer_directory="${root}/session-authorizer"
  mkdir -m 0700 "$recovery_directory" "$authorizer_directory" || return 1
  phase0_session::assert_directory "$recovery_directory" "Recovery Operator credential directory" || return 1
  phase0_session::assert_directory "$authorizer_directory" "Session Authorizer credential directory" || return 1
  recovery_ca="${recovery_directory}/cluster-ca.crt"
  authorizer_ca="${authorizer_directory}/cluster-ca.crt"
  printf '%s' "$(phase0_session::operation ca_data)" | base64 -D > "$recovery_ca" || return 1
  install -m 0600 "$recovery_ca" "$authorizer_ca" || return 1
  chmod 0600 "$recovery_ca" || return 1
  openssl x509 -in "$recovery_ca" -noout > /dev/null || return 1
  openssl x509 -in "$authorizer_ca" -noout > /dev/null || return 1
  ATLAS_PHASE0_OPERATION["recovery_credential_directory"]=$recovery_directory
  ATLAS_PHASE0_OPERATION["authorizer_credential_directory"]=$authorizer_directory
  ATLAS_PHASE0_OPERATION["recovery_ca_file"]=$recovery_ca
  ATLAS_PHASE0_OPERATION["authorizer_ca_file"]=$authorizer_ca
  prefix=${ATLAS_PHASE0_OPERATION[session_id]:0:12}
  phase0_ceremony::_issue_principal recovery "$(phase0_session::operation recovery_principal)" \
    "atlas-bg-recovery-g$(phase0_session::target recovery_generation)-${prefix}" \
    "$recovery_directory" "$recovery_ca" || return 1
  phase0_ceremony::_issue_principal previous_recovery "$(phase0_session::operation previous_recovery_principal)" \
    "atlas-bg-recovery-g$(phase0_session::target previous_recovery_generation)-${prefix}" \
    "$recovery_directory" "$recovery_ca" || return 1
  phase0_ceremony::_issue_principal authorizer "$(phase0_session::operation authorizer_principal)" \
    "atlas-bg-authorizer-g$(phase0_session::target authorizer_generation)-${prefix}" \
    "$authorizer_directory" "$authorizer_ca" || return 1
  phase0_ceremony::_issue_principal previous_authorizer "$(phase0_session::operation previous_authorizer_principal)" \
    "atlas-bg-authorizer-g$(phase0_session::target previous_authorizer_generation)-${prefix}" \
    "$authorizer_directory" "$authorizer_ca" || return 1
  phase0_ceremony::_capture_permission_baseline
}

phase0_ceremony::_permission_inventory() {
  local kubeconfig=$1 destination=$2 temporary diagnostics namespace line namespaces listing
  temporary="${destination}.tmp"
  diagnostics="${destination}.stderr.tmp"
  namespaces=$(phase0_session::operation permission_namespaces) || return 1
  : > "$temporary" || return 1
  while IFS= read -r namespace || [[ -n $namespace ]]; do
    [[ -n $namespace ]] || return 1
    : > "$diagnostics" || return 1
    if ! listing=$(phase0_session::principal "$kubeconfig" auth can-i --list \
      --namespace="$namespace" --no-headers 2> "$diagnostics"); then
      rm -f -- "$temporary" "$diagnostics"
      recovery::die "credential permission inventory command failed: ${namespace}"
      return 1
    fi
    if [[ -s $diagnostics ]]; then
      rm -f -- "$temporary" "$diagnostics"
      recovery::die "credential permission inventory emitted diagnostics and may be incomplete: ${namespace}"
      return 1
    fi
    while IFS= read -r line || [[ -n $line ]]; do
      [[ -n $line ]] || continue
      printf '%s\t%s\n' "$namespace" "$line" >> "$temporary" || return 1
    done <<< "$listing"
  done < "$namespaces"
  rm -f -- "$diagnostics" || return 1
  LC_ALL=C sort -u "$temporary" -o "$temporary" || return 1
  [[ -s $temporary ]] || return 1
  mv "$temporary" "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_snapshot_permission_namespaces() {
  local destination=$1 temporary
  temporary="${destination}.tmp"
  phase0_session::admin get namespaces -o json |
    yq -r '.items[].metadata.name' | LC_ALL=C sort -u > "$temporary" || return 1
  [[ -s $temporary ]] || return 1
  mv "$temporary" "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_assert_permission_inventory_non_mutating() {
  local inventory=$1 line namespace rule resource verbs verb
  local -a verb_list
  while IFS= read -r line || [[ -n $line ]]; do
    if [[ $line != *$'\t'* || ${line#*$'\t'} == *$'\t'* ]]; then
      recovery::die "credential baseline contains an invalid permission record"
      return 1
    fi
    namespace=${line%%$'\t'*}
    rule=${line#*$'\t'}
    if [[ ! $namespace =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ||
      ! $rule =~ ^([^[:space:]]*)[[:space:]]+\[([^]]*)\][[:space:]]+\[([^]]*)\][[:space:]]+\[([^]]+)\][[:space:]]*$ ]]; then
      recovery::die "credential baseline contains an invalid permission record"
      return 1
    fi
    resource=${BASH_REMATCH[1]}
    verbs=${BASH_REMATCH[4]}
    IFS=' ' read -r -a verb_list <<< "$verbs"
    ((${#verb_list[@]} > 0)) || {
      recovery::die "credential baseline contains an invalid permission record"
      return 1
    }
    for verb in "${verb_list[@]}"; do
      case "$verb" in
        get | list | watch) ;;
        create)
          case "$resource" in
            selfsubjectaccessreviews.authorization.k8s.io | \
              selfsubjectrulesreviews.authorization.k8s.io | \
              selfsubjectreviews.authentication.k8s.io) ;;
            *)
              recovery::die "credential baseline contains a state-changing permission: ${resource} ${verb}"
              return 1
              ;;
          esac
          ;;
        *)
          recovery::die "credential baseline contains an unexpected permission: ${resource:-<non-resource>} ${verb}"
          return 1
          ;;
      esac
    done
  done < "$inventory"
}

phase0_ceremony::_assert_can_i() {
  local kubeconfig=$1 expected=$2 output status
  shift 2
  if output=$(phase0_session::principal "$kubeconfig" auth can-i "$@"); then
    status=0
  else
    status=$?
  fi
  case "$expected" in
    yes) [[ $status == 0 && $output == yes ]] ;;
    no) [[ $status == 1 && $output == no ]] ;;
    *) return 1 ;;
  esac
}

phase0_ceremony::_assert_mutation_denied() {
  local kubeconfig=$1 label=$2 verb resource namespace
  while IFS=$'\t' read -r verb resource namespace; do
    if [[ $namespace == cluster ]]; then
      phase0_ceremony::_assert_can_i "$kubeconfig" no "$verb" "$resource" || {
        recovery::die "credential unexpectedly retains mutation permission: ${label} ${verb} ${resource} ${namespace}"
        return 1
      }
    else
      phase0_ceremony::_assert_can_i "$kubeconfig" no "$verb" "$resource" -n "$namespace" || {
        recovery::die "credential unexpectedly retains mutation permission: ${label} ${verb} ${resource} ${namespace}"
        return 1
      }
    fi
  done << 'EOF'
patch	validatingadmissionpolicybindings.admissionregistration.k8s.io/atlas-bootstrap-admission-escape-canary	cluster
create	configmaps	kube-system
create	rolebindings.rbac.authorization.k8s.io	kube-system
patch	configmaps/atlas-bootstrap-recovery-guard-canary	kube-system
create	secrets	kube-system
delete	namespaces	cluster
create	certificatesigningrequests.certificates.k8s.io	cluster
EOF
}

phase0_ceremony::_capture_permission_baseline() {
  local role kubeconfig inventory reference=''
  ATLAS_PHASE0_OPERATION["permission_namespaces"]=$(phase0_ceremony::_evidence_file \
    authorization/permission-namespaces-before.txt) || return 1
  phase0_ceremony::_snapshot_permission_namespaces \
    "$(phase0_session::operation permission_namespaces)" || return 1
  for role in recovery previous_recovery authorizer previous_authorizer; do
    kubeconfig=$(phase0_session::operation "${role}_kubeconfig") || return 1
    inventory=$(phase0_ceremony::_evidence_file "authorization/${role}-permissions-before.txt") || return 1
    phase0_ceremony::_permission_inventory "$kubeconfig" "$inventory" || return 1
    phase0_ceremony::_assert_permission_inventory_non_mutating "$inventory" || return 1
    phase0_ceremony::_assert_can_i "$kubeconfig" yes get /api || {
      recovery::die "credential lacks the expected authenticated discovery permission: ${role}"
      return 1
    }
    phase0_ceremony::_assert_mutation_denied "$kubeconfig" "$role-before" || return 1
    if [[ -z $reference ]]; then
      reference=$inventory
    else
      cmp -s "$reference" "$inventory" || {
        recovery::die "unbound credential effective permissions differ before activation"
        return 1
      }
    fi
  done
  phase0_session::journal_append CREDENTIALS BASELINE \
    "four current/previous credentials have identical namespace-complete non-mutating permission baselines" || return 1
}

phase0_ceremony::_verify_active_permissions() {
  local role kubeconfig baseline active
  phase0_ceremony::_assert_can_i "$(phase0_session::operation recovery_kubeconfig)" yes \
    patch validatingadmissionpolicybindings.admissionregistration.k8s.io/atlas-bootstrap-admission-escape-canary || return 1
  phase0_ceremony::_assert_can_i "$(phase0_session::operation authorizer_kubeconfig)" yes \
    create configmaps -n kube-system || return 1
  phase0_ceremony::_assert_can_i "$(phase0_session::operation authorizer_kubeconfig)" yes \
    create rolebindings.rbac.authorization.k8s.io -n kube-system || return 1
  for role in previous_recovery previous_authorizer; do
    kubeconfig=$(phase0_session::operation "${role}_kubeconfig") || return 1
    baseline=$(phase0_ceremony::_evidence_file "authorization/${role}-permissions-before.txt") || return 1
    active=$(phase0_ceremony::_evidence_file "authorization/${role}-permissions-active.txt") || return 1
    phase0_ceremony::_permission_inventory "$kubeconfig" "$active" || return 1
    cmp -s "$baseline" "$active" || {
      recovery::die "old-generation effective permissions changed during activation: ${role}"
      return 1
    }
    phase0_ceremony::_assert_mutation_denied "$kubeconfig" "${role}-active" || return 1
  done
  phase0_session::journal_append CREDENTIALS ACTIVE \
    "current grants verified; both old-generation credentials remain mutation-denied" || return 1
}

phase0_ceremony::_wait_policy_typecheck() {
  local name=$1 destination=$2
  local attempt object generation observed warnings
  for ((attempt = 0; attempt < ATLAS_PHASE0_WAIT_ATTEMPTS; attempt++)); do
    object=$(phase0_session::admin get validatingadmissionpolicy "$name" -o json) || return 1
    generation=$(yq -r '.metadata.generation' <<< "$object") || return 1
    observed=$(yq -r '.status.observedGeneration // 0' <<< "$object") || return 1
    if [[ $generation == "$observed" && $(yq '.status | has("typeChecking")' <<< "$object") == true ]]; then
      warnings=$(yq -r '.status.typeChecking.expressionWarnings | length' <<< "$object") || return 1
      ((warnings == 0)) || {
        recovery::die "VAP type checking reported warnings: ${name}"
        return 1
      }
      printf '%s\n' "$object" > "$destination" || return 1
      chmod 0400 "$destination" || return 1
      return 0
    fi
    sleep 1
  done
  recovery::die "VAP type checking did not complete: ${name}"
}

phase0_ceremony::_definition_inventory() {
  cat << 'EOF'
admission	kube-system	configmap	ConfigMap	atlas-bootstrap-admission-escape-canary	admission-fixture
admission	cluster	clusterrole	ClusterRole	atlas-bootstrap-break-glass-escape	escape-role
admission	cluster	clusterrolebinding	ClusterRoleBinding	atlas-bootstrap-break-glass-escape	escape-binding
admission	cluster	validatingadmissionpolicy	ValidatingAdmissionPolicy	atlas-bootstrap-admission-escape-canary	admission-policy
admission	cluster	validatingadmissionpolicybinding	ValidatingAdmissionPolicyBinding	atlas-bootstrap-admission-escape-canary	admission-binding
static	kube-system	role	Role	atlas-bootstrap-recovery-canary	recovery-role
static	kube-system	role	Role	atlas-bootstrap-recovery-authorizer-canary	authorizer-role
activation	kube-system	rolebinding	RoleBinding	atlas-bootstrap-recovery-authorizer-canary	authorizer-binding
static	cluster	validatingadmissionpolicy	ValidatingAdmissionPolicy	atlas-bootstrap-recovery-fence-authorization-canary	fence-policy
static	cluster	validatingadmissionpolicybinding	ValidatingAdmissionPolicyBinding	atlas-bootstrap-recovery-fence-authorization-canary	fence-binding
static	cluster	validatingadmissionpolicy	ValidatingAdmissionPolicy	atlas-bootstrap-recovery-binding-shape-authorization-canary	shape-policy
static	cluster	validatingadmissionpolicybinding	ValidatingAdmissionPolicyBinding	atlas-bootstrap-recovery-binding-shape-authorization-canary	shape-binding
static	cluster	validatingadmissionpolicy	ValidatingAdmissionPolicy	atlas-bootstrap-recovery-permission-authorization-canary	permission-policy
static	cluster	validatingadmissionpolicybinding	ValidatingAdmissionPolicyBinding	atlas-bootstrap-recovery-permission-authorization-canary	permission-binding-definition
static	kube-system	configmap	ConfigMap	atlas-bootstrap-recovery-guard-canary	guard-fixture
static	cluster	validatingadmissionpolicy	ValidatingAdmissionPolicy	atlas-bootstrap-recovery-guard-authorization-canary	guard-policy
static	cluster	validatingadmissionpolicybinding	ValidatingAdmissionPolicyBinding	atlas-bootstrap-recovery-guard-authorization-canary	guard-binding
EOF
}

phase0_ceremony::_normalize_definition() {
  yq -o=json -I=0 '
    del(.metadata.uid, .metadata.resourceVersion, .metadata.generation,
      .metadata.creationTimestamp, .metadata.managedFields, .status) |
    (select(.spec.matchConstraints.matchPolicy == "Equivalent") |
      .spec.matchConstraints) |= del(.matchPolicy) |
    (select(((.spec.matchConstraints.namespaceSelector | type) == "!!map") and
      ((.spec.matchConstraints.namespaceSelector | length) == 0)) |
      .spec.matchConstraints) |= del(.namespaceSelector) |
    (select(((.spec.matchConstraints.objectSelector | type) == "!!map") and
      ((.spec.matchConstraints.objectSelector | length) == 0)) |
      .spec.matchConstraints) |= del(.objectSelector) |
    (select(.spec.matchResources.matchPolicy == "Equivalent") |
      .spec.matchResources) |= del(.matchPolicy) |
    (select(((.spec.matchResources.namespaceSelector | type) == "!!map") and
      ((.spec.matchResources.namespaceSelector | length) == 0)) |
      .spec.matchResources) |= del(.namespaceSelector) |
    (select(((.spec.matchResources.objectSelector | type) == "!!map") and
      ((.spec.matchResources.objectSelector | length) == 0)) |
      .spec.matchResources) |= del(.objectSelector) |
    (select(.kind == "ValidatingAdmissionPolicyBinding") |
      .spec.validationActions) |= sort |
    sort_keys(..)
  ' "$1"
}

phase0_ceremony::_normalized_definition_sha256() {
  phase0_ceremony::_normalize_definition "$1" | shasum -a 256 | awk '{print $1}'
}

phase0_ceremony::_assert_validation_actions() {
  local snapshot=$1 expected_sorted=$2 actions sorted count unique_count
  actions=$(yq -o=json -I=0 '.spec.validationActions' "$snapshot") || return 1
  sorted=$(yq -o=json -I=0 '.spec.validationActions | sort' "$snapshot") || return 1
  count=$(yq -r '.spec.validationActions | length' "$snapshot") || return 1
  unique_count=$(yq -r '.spec.validationActions | unique | length' "$snapshot") || return 1
  [[ $actions != null && $count == "$unique_count" && $sorted == "$expected_sorted" ]] || {
    recovery::die "admission Binding validationActions are drifted or contain duplicates"
    return 1
  }
}

phase0_ceremony::_desired_definition() {
  local bundle=$1 kind=$2 namespace=$3 name=$4 destination=$5
  KIND=$kind NAMESPACE=$namespace NAME=$name yq ea -o=json -I=0 '
    select(.kind == env(KIND) and .metadata.name == env(NAME) and
      (.metadata.namespace // "cluster") == env(NAMESPACE))
  ' "$bundle" > "$destination" || return 1
  [[ $(wc -l < "$destination" | tr -d ' ') == 1 ]] || return 1
  phase0_ceremony::_normalize_definition "$destination" > "${destination}.normalized" || return 1
  mv "${destination}.normalized" "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_verify_live_definitions() {
  local scope=$1 phase namespace resource kind name label bundle desired live hash_file count=0 expected
  local -a arguments
  hash_file=$(phase0_ceremony::_evidence_file "authorization/${scope}-live-projections.sha256") || return 1
  : > "$hash_file" || return 1
  while IFS=$'\t' read -r phase namespace resource kind name label; do
    [[ $scope == full || $phase != activation ]] || continue
    case "$phase" in
      admission) bundle=$(phase0_session::operation admission_bundle) || return 1 ;;
      static) bundle=$(phase0_session::operation session_static_bundle) || return 1 ;;
      activation) bundle=$(phase0_session::operation authorizer_activation_bundle) || return 1 ;;
      *) return 1 ;;
    esac
    desired=$(phase0_ceremony::_evidence_file "authorization/${scope}-${label}-desired.json") || return 1
    live=$(phase0_ceremony::_evidence_file "authorization/${scope}-${label}-live.json") || return 1
    phase0_ceremony::_desired_definition "$bundle" "$kind" "$namespace" "$name" "$desired" || return 1
    arguments=(get "$resource" "$name" -o json)
    [[ $namespace == cluster ]] || arguments+=(-n "$namespace")
    phase0_session::admin "${arguments[@]}" > "${live}.raw" || return 1
    phase0_ceremony::_normalize_definition "${live}.raw" > "$live" || return 1
    rm -f -- "${live}.raw" || return 1
    chmod 0400 "$live" || return 1
    cmp -s "$desired" "$live" || {
      recovery::die "live canary projection differs from the approved bundle: ${label}"
      return 1
    }
    printf '%s  %s\n' "$(phase0_session::_sha256 "$live")" "$label" >> "$hash_file" || return 1
    ((count += 1))
  done < <(phase0_ceremony::_definition_inventory)
  expected=17
  [[ $scope == full ]] || expected=16
  ((count == expected)) || return 1
  chmod 0400 "$hash_file" || return 1
}

phase0_ceremony::_activate_authorizer() {
  phase0_session::journal_append AUTHORIZER STARTED "activating Session Authorizer after static controls verified" || return 1
  phase0_session::admin create --validate=strict \
    -f "$(phase0_session::operation authorizer_activation_bundle)" > /dev/null || return 1
  phase0_ceremony::_verify_live_definitions full || return 1
  phase0_ceremony::_assert_can_i "$(phase0_session::operation authorizer_kubeconfig)" yes \
    create configmaps -n kube-system || return 1
  phase0_session::journal_append AUTHORIZER ACTIVE "all 17 live projections match approval; authorizer grant is effective" || return 1
}

phase0_ceremony::_install_definitions() {
  local policy destination hash_file name
  phase0_session::journal_append DEFINITIONS STARTED "creating reviewed canary bundles" || return 1
  phase0_session::admin create --validate=strict -f "$(phase0_session::operation admission_bundle)" > /dev/null || return 1
  phase0_session::admin create --validate=strict -f "$(phase0_session::operation session_static_bundle)" > /dev/null || return 1
  hash_file="$(phase0_ceremony::_evidence_file authorization/policy-projections.sha256)"
  : > "$hash_file" || return 1
  for name in \
    atlas-bootstrap-admission-escape-canary \
    atlas-bootstrap-recovery-fence-authorization-canary \
    atlas-bootstrap-recovery-binding-shape-authorization-canary \
    atlas-bootstrap-recovery-permission-authorization-canary \
    atlas-bootstrap-recovery-guard-authorization-canary; do
    destination="$(phase0_ceremony::_evidence_file "authorization/${name}-live.json")"
    phase0_ceremony::_wait_policy_typecheck "$name" "$destination" || return 1
    policy=$(yq -o=json -I=0 '.spec | sort_keys(..)' "$destination") || return 1
    printf '%s  %s\n' "$(printf '%s' "$policy" | shasum -a 256 | awk '{print $1}')" "${name}/spec" >> "$hash_file" || return 1
  done
  chmod 0400 "$hash_file" || return 1
  phase0_ceremony::_verify_live_definitions static || return 1
  phase0_session::journal_append DEFINITIONS READY "16 static definitions match approval; five Policies type checked" || return 1
  phase0_ceremony::_activate_authorizer
}

phase0_ceremony::_expect_rejected() {
  local label=$1 expected_diagnostic=$2 log
  shift 2
  log=$(phase0_ceremony::_evidence_file "authorization/rejected-${label}.log") || return 1
  if "$@" > /dev/null 2> "$log"; then
    recovery::die "negative authorization probe unexpectedly succeeded: ${label}"
    return 1
  fi
  grep -Fqi "$expected_diagnostic" "$log" || {
    recovery::die "negative probe failed for an unexpected reason: ${label}"
    return 1
  }
  chmod 0400 "$log" || return 1
  phase0_session::journal_append PROBE REJECTED "$label" || return 1
}

phase0_ceremony::_binding_patch() {
  local kubeconfig=$1 source_snapshot=$2 actions_json=$3 label=$4 uid resource_version raw_actions patch
  uid=$(yq -r '.metadata.uid' "$source_snapshot") || return 1
  resource_version=$(yq -r '.metadata.resourceVersion' "$source_snapshot") || return 1
  raw_actions=$(yq -o=json -I=0 '.spec.validationActions' "$source_snapshot") || return 1
  patch="$(phase0_ceremony::_evidence_file "authorization/${label}-patch.json")"
  printf '[{"op":"test","path":"/metadata/uid","value":"%s"},{"op":"test","path":"/metadata/resourceVersion","value":"%s"},{"op":"test","path":"/spec/validationActions","value":%s},{"op":"replace","path":"/spec/validationActions","value":%s}]\n' \
    "$uid" "$resource_version" "$raw_actions" "$actions_json" > "$patch" || return 1
  chmod 0400 "$patch" || return 1
  phase0_session::_kubectl "$kubeconfig" patch validatingadmissionpolicybinding \
    atlas-bootstrap-admission-escape-canary --type=json --patch-file "$patch" > /dev/null
}

phase0_ceremony::_patch_fixture() {
  local kubeconfig=$1 value=$2 label=$3 dry_run=${4:-false} snapshot uid resource_version patch
  local -a arguments
  [[ $dry_run == true || $dry_run == false ]] || return 1
  snapshot="$(phase0_ceremony::_evidence_file "authorization/${label}-fixture-before.json")"
  phase0_ceremony::_record_object "$(phase0_session::target admin_kubeconfig)" configmap \
    atlas-bootstrap-admission-escape-canary "$snapshot" || return 1
  uid=$(yq -r '.metadata.uid' "$snapshot") || return 1
  resource_version=$(yq -r '.metadata.resourceVersion' "$snapshot") || return 1
  patch="$(phase0_ceremony::_evidence_file "authorization/${label}-fixture-patch.json")"
  printf '[{"op":"test","path":"/metadata/uid","value":"%s"},{"op":"test","path":"/metadata/resourceVersion","value":"%s"},{"op":"replace","path":"/data/sentinel","value":"%s"}]\n' \
    "$uid" "$resource_version" "$value" > "$patch" || return 1
  chmod 0400 "$patch" || return 1
  arguments=(patch configmap atlas-bootstrap-admission-escape-canary
    -n kube-system --type=json --patch-file "$patch")
  [[ $dry_run == false ]] || arguments+=(--dry-run=server)
  phase0_session::_kubectl "$kubeconfig" "${arguments[@]}" > /dev/null
}

phase0_ceremony::_wait_fixture_admission() {
  local kubeconfig=$1 expected=$2 value=$3 label=$4 expected_diagnostic=$5
  local attempt attempt_label diagnostic
  [[ $expected == allowed || $expected == denied ]] || return 1
  for ((attempt = 1; attempt <= ATLAS_PHASE0_WAIT_ATTEMPTS; attempt++)); do
    attempt_label="${label}-$(printf '%02d' "$attempt")"
    diagnostic="$(phase0_ceremony::_evidence_file "authorization/${attempt_label}.stderr")" || return 1
    if phase0_ceremony::_patch_fixture "$kubeconfig" "$value" "$attempt_label" true 2> "$diagnostic"; then
      chmod 0400 "$diagnostic" || return 1
      [[ $expected == allowed ]] && return 0
    else
      chmod 0400 "$diagnostic" || return 1
      grep -Fqi "$expected_diagnostic" "$diagnostic" || {
        recovery::die "admission propagation probe failed for an unexpected reason: ${label}"
        return 1
      }
      [[ $expected == denied ]] && return 0
    fi
    sleep 1
  done
  recovery::die "admission propagation did not reach the approved ${expected} state: ${label}"
  return 1
}

phase0_ceremony::_admission_escape_drill() {
  local recovery_kubeconfig admin_kubeconfig admission_bundle admission_bundle_sha
  local approved_policy approved_binding approved_policy_hash approved_binding_hash
  local enforced_policy enforced_binding enforced_typechecked suspended restored_policy restored_binding restored_typechecked
  local policy_uid binding_uid restored_actions
  recovery_kubeconfig=$(phase0_session::operation recovery_kubeconfig) || return 1
  admin_kubeconfig=$(phase0_session::target admin_kubeconfig) || return 1
  admission_bundle=$(phase0_session::operation admission_bundle) || return 1
  admission_bundle_sha=$(phase0_session::operation admission_bundle_sha) || return 1
  [[ $(phase0_session::_sha256 "$admission_bundle") == "$admission_bundle_sha" ]] || {
    recovery::die "approved admission Bundle hash drifted before suspend"
    return 1
  }

  approved_policy="$(phase0_ceremony::_evidence_file authorization/admission-policy-approved.json)"
  approved_binding="$(phase0_ceremony::_evidence_file authorization/admission-binding-approved.json)"
  phase0_ceremony::_desired_definition "$admission_bundle" ValidatingAdmissionPolicy cluster \
    atlas-bootstrap-admission-escape-canary "$approved_policy" || return 1
  phase0_ceremony::_desired_definition "$admission_bundle" ValidatingAdmissionPolicyBinding cluster \
    atlas-bootstrap-admission-escape-canary "$approved_binding" || return 1
  phase0_ceremony::_assert_validation_actions "$approved_binding" '["Audit","Deny"]' || return 1
  [[ $(yq -o=json -I=0 '.spec.validationActions' "$approved_binding") == '["Audit","Deny"]' ]] || {
    recovery::die "approved admission Bundle does not use canonical Audit+Deny order"
    return 1
  }
  approved_policy_hash=$(phase0_session::_sha256 "$approved_policy") || return 1
  approved_binding_hash=$(phase0_session::_sha256 "$approved_binding") || return 1

  enforced_policy="$(phase0_ceremony::_evidence_file authorization/admission-policy-enforced.json)"
  enforced_binding="$(phase0_ceremony::_evidence_file authorization/admission-binding-enforced.json)"
  suspended="$(phase0_ceremony::_evidence_file authorization/admission-binding-suspended.json)"
  restored_policy="$(phase0_ceremony::_evidence_file authorization/admission-policy-restored.json)"
  restored_binding="$(phase0_ceremony::_evidence_file authorization/admission-binding-restored.json)"
  enforced_typechecked="$(phase0_ceremony::_evidence_file authorization/admission-policy-enforced-typecheck.json)"
  restored_typechecked="$(phase0_ceremony::_evidence_file authorization/admission-policy-restored-typecheck.json)"
  phase0_ceremony::_record_object "$admin_kubeconfig" validatingadmissionpolicy \
    atlas-bootstrap-admission-escape-canary "$enforced_policy" || return 1
  phase0_ceremony::_record_object "$admin_kubeconfig" validatingadmissionpolicybinding \
    atlas-bootstrap-admission-escape-canary "$enforced_binding" || return 1
  policy_uid=$(yq -r '.metadata.uid' "$enforced_policy") || return 1
  binding_uid=$(yq -r '.metadata.uid' "$enforced_binding") || return 1
  [[ $policy_uid =~ ^[0-9a-f-]{36}$ && $binding_uid =~ ^[0-9a-f-]{36}$ ]] || return 1
  [[ $(phase0_ceremony::_normalized_definition_sha256 "$enforced_policy") == "$approved_policy_hash" &&
  $(phase0_ceremony::_normalized_definition_sha256 "$enforced_binding") == "$approved_binding_hash" ]] || {
    recovery::die "live admission Policy or Binding drifted from the approved Bundle before suspend"
    return 1
  }
  phase0_ceremony::_assert_validation_actions "$enforced_binding" '["Audit","Deny"]' || return 1
  phase0_ceremony::_wait_policy_typecheck atlas-bootstrap-admission-escape-canary \
    "$enforced_typechecked" || return 1
  [[ $(yq -r '.metadata.uid' "$enforced_typechecked") == "$policy_uid" &&
  $(phase0_ceremony::_normalized_definition_sha256 "$enforced_typechecked") == "$approved_policy_hash" ]] || return 1
  phase0_ceremony::_expect_rejected admission-fixture-enforced \
    'Atlas admission escape canary mutation requires' \
    phase0_ceremony::_patch_fixture "$admin_kubeconfig" denied admission-enforced || return 1

  phase0_session::journal_append ADMISSION_SUSPEND STARTED "replacing only validationActions with Audit" || return 1
  phase0_ceremony::_binding_patch "$recovery_kubeconfig" "$enforced_binding" '["Audit"]' suspend || return 1
  phase0_ceremony::_record_object "$admin_kubeconfig" validatingadmissionpolicybinding \
    atlas-bootstrap-admission-escape-canary "$suspended" || return 1
  [[ $(yq -r '.metadata.uid' "$suspended") == "$binding_uid" ]] || return 1
  phase0_ceremony::_assert_validation_actions "$suspended" '["Audit"]' || return 1
  [[ $(yq -o=json -I=0 '.spec.validationActions' "$suspended") == '["Audit"]' ]] || return 1
  phase0_ceremony::_wait_fixture_admission "$admin_kubeconfig" allowed suspended \
    admission-suspended-propagation 'Atlas admission escape canary mutation requires' || return 1
  phase0_ceremony::_patch_fixture "$admin_kubeconfig" suspended admission-suspended || return 1
  phase0_ceremony::_patch_fixture "$admin_kubeconfig" admission-escape-canary admission-reverted || return 1
  phase0_session::journal_append ADMISSION_SUSPEND VERIFIED "ordinary mutation audited while suspended and fixture restored" || return 1

  phase0_session::journal_append ADMISSION_RESTORE STARTED "restoring canonical Audit+Deny actions" || return 1
  phase0_ceremony::_binding_patch "$recovery_kubeconfig" "$suspended" '["Audit","Deny"]' restore || return 1
  phase0_ceremony::_record_object "$admin_kubeconfig" validatingadmissionpolicy \
    atlas-bootstrap-admission-escape-canary "$restored_policy" || return 1
  phase0_ceremony::_record_object "$admin_kubeconfig" validatingadmissionpolicybinding \
    atlas-bootstrap-admission-escape-canary "$restored_binding" || return 1
  [[ $(yq -r '.metadata.uid' "$restored_policy") == "$policy_uid" &&
  $(yq -r '.metadata.uid' "$restored_binding") == "$binding_uid" ]] || return 1
  [[ $(phase0_ceremony::_normalized_definition_sha256 "$restored_policy") == "$approved_policy_hash" &&
  $(phase0_ceremony::_normalized_definition_sha256 "$restored_binding") == "$approved_binding_hash" ]] || return 1
  phase0_ceremony::_assert_validation_actions "$restored_binding" '["Audit","Deny"]' || return 1
  restored_actions=$(yq -o=json -I=0 '.spec.validationActions' "$restored_binding") || return 1
  [[ $restored_actions == '["Audit","Deny"]' ]] || return 1
  phase0_ceremony::_wait_policy_typecheck atlas-bootstrap-admission-escape-canary \
    "$restored_typechecked" || return 1
  [[ $(yq -r '.metadata.uid' "$restored_typechecked") == "$policy_uid" &&
  $(phase0_ceremony::_normalized_definition_sha256 "$restored_typechecked") == "$approved_policy_hash" ]] || return 1
  phase0_ceremony::_wait_fixture_admission "$admin_kubeconfig" denied denied \
    admission-restored-propagation 'Atlas admission escape canary mutation requires' || return 1
  phase0_session::journal_append ADMISSION_RESTORE VERIFIED \
    "approved Policy and Binding projections, UIDs and Policy type-check restored; server-side ordinary mutation denied" || return 1
}

phase0_ceremony::_write_fence() {
  local destination=$1 session_id operation_id target_fingerprint authorizer
  local prepared_at recovery plan_sha revision
  session_id=$(phase0_session::operation session_id) || return 1
  operation_id=$(phase0_session::operation operation_id) || return 1
  target_fingerprint=$(phase0_session::operation target_fingerprint) || return 1
  authorizer=$(phase0_session::operation authorizer_principal) || return 1
  prepared_at=$(phase0_session::operation prepared_at) || return 1
  recovery=$(phase0_session::operation recovery_principal) || return 1
  plan_sha=$(phase0_session::operation plan_sha) || return 1
  revision=$(phase0_session::target known_good_revision) || return 1
  ATLAS_JSON_SESSION_ID=$session_id \
    ATLAS_JSON_OPERATION_ID=$operation_id \
    ATLAS_JSON_TARGET_FINGERPRINT=$target_fingerprint \
    ATLAS_JSON_AUTHORIZER_PRINCIPAL=$authorizer \
    ATLAS_JSON_PREPARED_AT=$prepared_at \
    ATLAS_JSON_RECOVERY_PRINCIPAL=$recovery \
    ATLAS_JSON_PLAN_SHA=$plan_sha \
    ATLAS_JSON_KNOWN_GOOD_REVISION=$revision \
    yq -n -o=json -I=0 '
      {
        "apiVersion": "v1",
        "kind": "ConfigMap",
        "metadata": {
          "name": "atlas-bootstrap-operation-fence-canary",
          "namespace": "kube-system",
          "labels": {
            "app.kubernetes.io/part-of": "atlas-recovery",
            "atlas.io/recovery-scope": "canary",
            "atlas.io/recovery-session": strenv(ATLAS_JSON_SESSION_ID)
          }
        },
        "immutable": true,
        "data": {
          "schema": "atlas.io/bootstrap-operation-fence/v1",
          "operationID": strenv(ATLAS_JSON_OPERATION_ID),
          "mode": "recovery",
          "clusterFingerprintSHA256": strenv(ATLAS_JSON_TARGET_FINGERPRINT),
          "holderUsername": strenv(ATLAS_JSON_AUTHORIZER_PRINCIPAL),
          "createdAt": strenv(ATLAS_JSON_PREPARED_AT),
          "sessionID": strenv(ATLAS_JSON_SESSION_ID),
          "recoveryPrincipal": strenv(ATLAS_JSON_RECOVERY_PRINCIPAL),
          "authorizerPrincipal": strenv(ATLAS_JSON_AUTHORIZER_PRINCIPAL),
          "planSHA256": strenv(ATLAS_JSON_PLAN_SHA),
          "knownGoodRevision": strenv(ATLAS_JSON_KNOWN_GOOD_REVISION)
        }
      }
    ' > "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_write_permission_binding() {
  local destination=$1 fence_uid=$2 plan_sha=$3
  local session_id target_fingerprint revision recovery
  session_id=$(phase0_session::operation session_id) || return 1
  target_fingerprint=$(phase0_session::operation target_fingerprint) || return 1
  revision=$(phase0_session::target known_good_revision) || return 1
  recovery=$(phase0_session::operation recovery_principal) || return 1
  ATLAS_JSON_SESSION_ID=$session_id \
    ATLAS_JSON_FENCE_UID=$fence_uid \
    ATLAS_JSON_PLAN_SHA=$plan_sha \
    ATLAS_JSON_TARGET_FINGERPRINT=$target_fingerprint \
    ATLAS_JSON_KNOWN_GOOD_REVISION=$revision \
    ATLAS_JSON_RECOVERY_PRINCIPAL=$recovery \
    yq -n -o=json -I=0 '
      {
        "apiVersion": "rbac.authorization.k8s.io/v1",
        "kind": "RoleBinding",
        "metadata": {
          "name": ("atlas-bg-canary-" + strenv(ATLAS_JSON_SESSION_ID)),
          "namespace": "kube-system",
          "labels": {
            "app.kubernetes.io/part-of": "atlas-recovery",
            "atlas.io/recovery-scope": "canary",
            "atlas.io/recovery-session": strenv(ATLAS_JSON_SESSION_ID)
          },
          "annotations": {
            "atlas.io/recovery-fence-uid": strenv(ATLAS_JSON_FENCE_UID),
            "atlas.io/recovery-plan-sha256": strenv(ATLAS_JSON_PLAN_SHA),
            "atlas.io/recovery-target-sha256": strenv(ATLAS_JSON_TARGET_FINGERPRINT),
            "atlas.io/recovery-revision": strenv(ATLAS_JSON_KNOWN_GOOD_REVISION)
          }
        },
        "roleRef": {
          "apiGroup": "rbac.authorization.k8s.io",
          "kind": "Role",
          "name": "atlas-bootstrap-recovery-canary"
        },
        "subjects": [{
          "apiGroup": "rbac.authorization.k8s.io",
          "kind": "User",
          "name": strenv(ATLAS_JSON_RECOVERY_PRINCIPAL)
        }]
      }
    ' > "$destination" || return 1
  chmod 0400 "$destination" || return 1
}

phase0_ceremony::_guard_patch() {
  local kubeconfig=$1 operation=$2 label=$3 snapshot uid resource_version patch path
  snapshot="$(phase0_ceremony::_evidence_file "authorization/${label}-guard-before.json")"
  phase0_ceremony::_record_object "$(phase0_session::target admin_kubeconfig)" configmap \
    atlas-bootstrap-recovery-guard-canary "$snapshot" || return 1
  uid=$(yq -r '.metadata.uid' "$snapshot") || return 1
  resource_version=$(yq -r '.metadata.resourceVersion' "$snapshot") || return 1
  patch="$(phase0_ceremony::_evidence_file "authorization/${label}-guard-patch.json")"
  path=/data/policy.atlas-recovery-freeze.csv
  case "$operation" in
    add)
      printf '[{"op":"test","path":"/metadata/uid","value":"%s"},{"op":"test","path":"/metadata/resourceVersion","value":"%s"},{"op":"test","path":"/data/sentinel","value":"recovery-guard-canary"},{"op":"add","path":"%s","value":"%s"}]\n' \
        "$uid" "$resource_version" "$path" "$ATLAS_PHASE0_GUARD_VALUE" > "$patch"
      ;;
    remove)
      printf '[{"op":"test","path":"/metadata/uid","value":"%s"},{"op":"test","path":"/metadata/resourceVersion","value":"%s"},{"op":"test","path":"%s","value":"%s"},{"op":"remove","path":"%s"}]\n' \
        "$uid" "$resource_version" "$path" "$ATLAS_PHASE0_GUARD_VALUE" "$path" > "$patch"
      ;;
    replace-bypass)
      printf '[{"op":"test","path":"/metadata/uid","value":"%s"},{"op":"test","path":"/metadata/resourceVersion","value":"%s"},{"op":"remove","path":"%s"},{"op":"add","path":"/data/unexpected","value":"replacement"}]\n' \
        "$uid" "$resource_version" "$path" > "$patch"
      ;;
    metadata-bypass)
      printf '[{"op":"test","path":"/metadata/uid","value":"%s"},{"op":"test","path":"/metadata/resourceVersion","value":"%s"},{"op":"add","path":"/metadata/annotations","value":{"atlas.io/unapproved":"drift"}},{"op":"add","path":"%s","value":"%s"}]\n' \
        "$uid" "$resource_version" "$path" "$ATLAS_PHASE0_GUARD_VALUE" > "$patch"
      ;;
    *) return 1 ;;
  esac
  chmod 0400 "$patch" || return 1
  phase0_session::_kubectl "$kubeconfig" patch configmap atlas-bootstrap-recovery-guard-canary \
    -n kube-system --type=json --patch-file "$patch" > /dev/null
}

phase0_ceremony::_session_authorization_drill() {
  local authorizer recovery admin fence_file fence_snapshot fence_uid binding_missing binding_wrong binding_exact
  local binding_snapshot unrelated noncanonical malformed_binding
  authorizer=$(phase0_session::operation authorizer_kubeconfig) || return 1
  recovery=$(phase0_session::operation recovery_kubeconfig) || return 1
  admin=$(phase0_session::target admin_kubeconfig) || return 1
  fence_file="$(phase0_ceremony::_evidence_file authorization/fence.json)"
  fence_snapshot="$(phase0_ceremony::_evidence_file authorization/fence-live.json)"
  binding_missing="$(phase0_ceremony::_evidence_file authorization/binding-missing-fence.json)"
  binding_wrong="$(phase0_ceremony::_evidence_file authorization/binding-wrong-lineage.json)"
  binding_exact="$(phase0_ceremony::_evidence_file authorization/binding-exact.json)"
  binding_snapshot="$(phase0_ceremony::_evidence_file authorization/binding-live.json)"
  unrelated="$(phase0_ceremony::_evidence_file authorization/unrelated-binding.yaml)"
  noncanonical="$(phase0_ceremony::_evidence_file authorization/noncanonical-configmap.yaml)"
  malformed_binding="$(phase0_ceremony::_evidence_file authorization/binding-malformed-shape.yaml)"

  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: atlas-phase0-noncanonical-%s\n  namespace: kube-system\ndata:\n  sentinel: denied\n' \
    "${ATLAS_PHASE0_OPERATION[session_id]:0:8}" > "$noncanonical" || return 1
  chmod 0400 "$noncanonical" || return 1
  phase0_ceremony::_expect_rejected fence-noncanonical-create \
    'Canary Fence requests must target' \
    phase0_session::_kubectl "$authorizer" create --validate=strict -f "$noncanonical" || return 1

  printf "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: atlas-bg-canary-malformed\n  namespace: kube-system\n  labels:\n    app.kubernetes.io/part-of: atlas-recovery\n    atlas.io/recovery-scope: canary\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: atlas-bootstrap-recovery-canary\nsubjects:\n  - apiGroup: rbac.authorization.k8s.io\n    kind: User\n    name: '%s'\n" \
    "$(phase0_session::operation recovery_principal)" > "$malformed_binding" || return 1
  chmod 0400 "$malformed_binding" || return 1
  phase0_ceremony::_expect_rejected shape-malformed-binding \
    'Canary permission Binding name or labels are invalid' \
    phase0_session::_kubectl "$authorizer" create --validate=strict -f "$malformed_binding" || return 1

  phase0_ceremony::_write_permission_binding "$binding_missing" 00000000-0000-0000-0000-000000000000 \
    "$(phase0_session::operation plan_sha)" || return 1
  phase0_ceremony::_expect_rejected permission-missing-fence \
    "no params found for policy binding with \`Deny\` parameterNotFoundAction" \
    phase0_session::_kubectl "$authorizer" create --validate=strict -f "$binding_missing" || return 1

  printf 'apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: atlas-phase0-unrelated-%s\n  namespace: kube-system\nroleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: view\nsubjects:\n  - apiGroup: rbac.authorization.k8s.io\n    kind: User\n    name: atlas:unrelated\n' \
    "${ATLAS_PHASE0_OPERATION[session_id]:0:8}" > "$unrelated" || return 1
  chmod 0400 "$unrelated" || return 1
  phase0_session::admin create --validate=strict -f "$unrelated" > /dev/null || return 1
  phase0_session::admin delete -f "$unrelated" --wait=true > /dev/null || return 1
  phase0_session::journal_append PROBE ALLOWED "unrelated RBAC skips recovery controls without a Fence" || return 1

  phase0_ceremony::_write_fence "$fence_file" || return 1
  phase0_session::journal_append FENCE STARTED "Session Authorizer creating canonical canary Fence" || return 1
  phase0_session::_kubectl "$authorizer" create --validate=strict -f "$fence_file" > /dev/null || return 1
  phase0_ceremony::_record_object "$admin" configmap atlas-bootstrap-operation-fence-canary "$fence_snapshot" || return 1
  fence_uid=$(yq -r '.metadata.uid' "$fence_snapshot") || return 1
  [[ $fence_uid =~ ^[0-9a-f-]{36}$ ]] || return 1
  ATLAS_PHASE0_OPERATION[fence_uid]=$fence_uid
  phase0_ceremony::_expect_rejected fence-create-conflict \
    'already exists' \
    phase0_session::_kubectl "$authorizer" create --validate=strict -f "$fence_file" || return 1
  phase0_ceremony::_expect_rejected fence-update \
    'cannot patch resource "configmaps"' \
    phase0_session::_kubectl "$authorizer" patch configmap atlas-bootstrap-operation-fence-canary \
    -n kube-system --type=merge -p '{"data":{"mode":"invalid"}}' || return 1
  phase0_session::journal_append FENCE ACQUIRED "create-only Fence UID=${fence_uid}" || return 1

  phase0_ceremony::_write_permission_binding "$binding_wrong" "$fence_uid" \
    0000000000000000000000000000000000000000000000000000000000000000 || return 1
  phase0_ceremony::_expect_rejected permission-wrong-lineage \
    'Canary permission Binding does not match the Fence lineage' \
    phase0_session::_kubectl "$authorizer" create --validate=strict -f "$binding_wrong" || return 1
  phase0_ceremony::_write_permission_binding "$binding_exact" "$fence_uid" \
    "$(phase0_session::operation plan_sha)" || return 1
  phase0_session::_kubectl "$authorizer" create --validate=strict -f "$binding_exact" > /dev/null || return 1
  phase0_ceremony::_record_object "$admin" rolebinding \
    "atlas-bg-canary-$(phase0_session::operation session_id)" "$binding_snapshot" || return 1
  phase0_session::journal_append PERMISSION INSTALLED "temporary RoleBinding matches Fence lineage" || return 1

  [[ $(phase0_session::_kubectl "$recovery" auth can-i get configmap/atlas-bootstrap-recovery-guard-canary -n kube-system) == yes ]] || return 1
  phase0_ceremony::_expect_rejected guard-ordinary-add \
    'Canary guard mutation requires the exact Recovery Operator' \
    phase0_ceremony::_guard_patch "$admin" add guard-ordinary-add || return 1
  phase0_ceremony::_expect_rejected guard-metadata-replacement \
    'Canary guard UPDATE changed a field outside the guarded projection' \
    phase0_ceremony::_guard_patch "$recovery" metadata-bypass guard-metadata-replacement || return 1
  phase0_ceremony::_guard_patch "$recovery" add guard-add || return 1
  phase0_ceremony::_expect_rejected guard-data-replacement \
    'Canary guard UPDATE changed a field outside the guarded projection' \
    phase0_ceremony::_guard_patch "$recovery" replace-bypass guard-replacement || return 1
  phase0_ceremony::_expect_rejected guard-ordinary-remove \
    'Canary guard mutation requires the exact Recovery Operator' \
    phase0_ceremony::_guard_patch "$admin" remove guard-ordinary-remove || return 1
  phase0_ceremony::_guard_patch "$recovery" remove guard-remove || return 1
  phase0_session::journal_append GUARD VERIFIED \
    "exact add/remove allowed; ordinary, metadata, and data replacement mutations denied" || return 1

  phase0_ceremony::_delete_with_preconditions "$authorizer" \
    "/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/rolebindings/atlas-bg-canary-$(phase0_session::operation session_id)" \
    "$binding_snapshot" permission-binding || return 1
  phase0_session::journal_append PERMISSION REMOVED "temporary RoleBinding deleted with UID/resourceVersion preconditions" || return 1
  phase0_ceremony::_delete_with_preconditions "$authorizer" \
    /api/v1/namespaces/kube-system/configmaps/atlas-bootstrap-operation-fence-canary \
    "$fence_snapshot" fence || return 1
  phase0_session::journal_append FENCE RELEASED "Fence deleted last with UID/resourceVersion preconditions" || return 1
}

phase0_ceremony::_snapshot_for_delete() {
  local resource=$1 name=$2 label=$3
  local destination
  destination=$(phase0_ceremony::_evidence_file "postflight/${label}-before-delete.json") || return 1
  phase0_ceremony::_record_object "$(phase0_session::target admin_kubeconfig)" "$resource" "$name" "$destination" || return 1
  printf '%s\n' "$destination"
}

phase0_ceremony::_cleanup_cluster_resources() {
  local namespace resource name uri label snapshot output
  local -a arguments
  snapshot=$(phase0_ceremony::_snapshot_for_delete rolebinding \
    atlas-bootstrap-recovery-authorizer-canary authorizer-binding) || return 1
  phase0_ceremony::_delete_with_preconditions "$(phase0_session::target admin_kubeconfig)" \
    /apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/rolebindings/atlas-bootstrap-recovery-authorizer-canary \
    "$snapshot" authorizer-binding || return 1
  output=$(phase0_session::admin get rolebinding atlas-bootstrap-recovery-authorizer-canary \
    --ignore-not-found -o name -n kube-system) || return 1
  [[ -z $output ]] || return 1
  phase0_ceremony::_assert_can_i "$(phase0_session::operation authorizer_kubeconfig)" no \
    create configmaps -n kube-system || return 1
  phase0_session::journal_append AUTHORIZER REVOKED "authorizer RoleBinding removed before protected controls" || return 1
  while IFS=$'\t' read -r namespace resource name uri label; do
    snapshot=$(phase0_ceremony::_snapshot_for_delete "$resource" "$name" "$label") || return 1
    phase0_ceremony::_delete_with_preconditions "$(phase0_session::target admin_kubeconfig)" \
      "$uri" "$snapshot" "$label" || return 1
    arguments=(get "$resource" "$name" --ignore-not-found -o name)
    [[ $namespace == cluster ]] || arguments+=(-n "$namespace")
    output=$(phase0_session::admin "${arguments[@]}") || return 1
    [[ -z $output ]] || return 1
  done << 'EOF'
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-guard-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings/atlas-bootstrap-recovery-guard-authorization-canary	guard-binding
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-permission-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings/atlas-bootstrap-recovery-permission-authorization-canary	permission-binding-definition
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-binding-shape-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings/atlas-bootstrap-recovery-binding-shape-authorization-canary	shape-binding
cluster	validatingadmissionpolicybinding	atlas-bootstrap-recovery-fence-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings/atlas-bootstrap-recovery-fence-authorization-canary	fence-binding
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-guard-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-recovery-guard-authorization-canary	guard-policy
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-permission-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-recovery-permission-authorization-canary	permission-policy
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-binding-shape-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-recovery-binding-shape-authorization-canary	shape-policy
cluster	validatingadmissionpolicy	atlas-bootstrap-recovery-fence-authorization-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-recovery-fence-authorization-canary	fence-policy
kube-system	role	atlas-bootstrap-recovery-authorizer-canary	/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/roles/atlas-bootstrap-recovery-authorizer-canary	authorizer-role
kube-system	role	atlas-bootstrap-recovery-canary	/apis/rbac.authorization.k8s.io/v1/namespaces/kube-system/roles/atlas-bootstrap-recovery-canary	recovery-role
kube-system	configmap	atlas-bootstrap-recovery-guard-canary	/api/v1/namespaces/kube-system/configmaps/atlas-bootstrap-recovery-guard-canary	guard-fixture
cluster	validatingadmissionpolicybinding	atlas-bootstrap-admission-escape-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings/atlas-bootstrap-admission-escape-canary	admission-binding
cluster	validatingadmissionpolicy	atlas-bootstrap-admission-escape-canary	/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/atlas-bootstrap-admission-escape-canary	admission-policy
cluster	clusterrolebinding	atlas-bootstrap-break-glass-escape	/apis/rbac.authorization.k8s.io/v1/clusterrolebindings/atlas-bootstrap-break-glass-escape	escape-binding
cluster	clusterrole	atlas-bootstrap-break-glass-escape	/apis/rbac.authorization.k8s.io/v1/clusterroles/atlas-bootstrap-break-glass-escape	escape-role
kube-system	configmap	atlas-bootstrap-admission-escape-canary	/api/v1/namespaces/kube-system/configmaps/atlas-bootstrap-admission-escape-canary	admission-fixture
EOF
  phase0_session::journal_append CLEANUP VERIFIED "all canary definitions removed with exact API preconditions" || return 1
}

phase0_ceremony::_cleanup_credentials() {
  local role key path directory baseline after directory_key namespace_after
  directory=$(phase0_session::target credential_directory) || return 1
  namespace_after=$(phase0_ceremony::_evidence_file postflight/permission-namespaces-after.txt) || return 1
  phase0_ceremony::_snapshot_permission_namespaces "$namespace_after" || return 1
  cmp -s "$(phase0_session::operation permission_namespaces)" "$namespace_after" || {
    recovery::die "the namespace set changed during credential permission verification"
    return 1
  }
  for role in recovery previous_recovery authorizer previous_authorizer; do
    path=$(phase0_session::operation "${role}_kubeconfig") || return 1
    phase0_ceremony::_assert_mutation_denied "$path" "${role}-after" || return 1
    baseline=$(phase0_ceremony::_evidence_file "authorization/${role}-permissions-before.txt") || return 1
    after=$(phase0_ceremony::_evidence_file "postflight/${role}-permissions-after.txt") || return 1
    phase0_ceremony::_permission_inventory "$path" "$after" || return 1
    cmp -s "$baseline" "$after" || {
      recovery::die "credential permissions did not return to the authenticated baseline: ${role}"
      return 1
    }
  done
  phase0_session::journal_append CREDENTIALS REVOKED \
    "current/previous credentials returned exactly to their discovery-only baseline" || return 1
  for key in recovery_key recovery_csr recovery_certificate recovery_kubeconfig \
    previous_recovery_key previous_recovery_csr previous_recovery_certificate previous_recovery_kubeconfig \
    authorizer_key authorizer_csr authorizer_certificate authorizer_kubeconfig \
    previous_authorizer_key previous_authorizer_csr previous_authorizer_certificate previous_authorizer_kubeconfig \
    recovery_ca_file authorizer_ca_file; do
    path=$(phase0_session::operation "$key") || return 1
    [[ $path == "${directory}/"* && -f $path && ! -L $path ]] || return 1
    rm -f -- "$path" || return 1
  done
  for directory_key in recovery_credential_directory authorizer_credential_directory; do
    path=$(phase0_session::operation "$directory_key") || return 1
    [[ $path == "${directory}/"* ]] || return 1
    rmdir "$path" || return 1
  done
  phase0_session::_directory_empty "$directory" || return 1
  phase0_session::journal_append CREDENTIALS REMOVED "materialized keys, certificates, CSRs, and kubeconfigs removed" || return 1
}

phase0_ceremony::_snapshot_audit_log() {
  local destination=$1 attempt line valid
  for ((attempt = 0; attempt < 3; attempt++)); do
    install -m 0600 "$(phase0_session::target audit_log)" "$destination" || return 1
    valid=true
    while IFS= read -r line || [[ -n $line ]]; do
      [[ -n $line ]] || {
        valid=false
        break
      }
      yq -e -p=json '.kind == "Event" and has("requestURI")' <<< "$line" > /dev/null || {
        valid=false
        break
      }
    done < "$destination"
    if [[ $valid == true ]]; then
      chmod 0400 "$destination" || return 1
      return 0
    fi
    sleep 1
  done
  recovery::die "API audit snapshot contains an incomplete or malformed event"
}

phase0_ceremony::_capture_audit_boundary() {
  local snapshot record line_count source_identity
  snapshot=$(phase0_ceremony::_evidence_file audit/pre-mutation.jsonl) || return 1
  record=$(phase0_ceremony::_evidence_file audit/pre-mutation-boundary.json) || return 1
  phase0_ceremony::_snapshot_audit_log "$snapshot" || return 1
  line_count=$(wc -l < "$snapshot" | tr -d ' ') || return 1
  [[ $line_count =~ ^[0-9]+$ && $line_count -gt 0 ]] || return 1
  source_identity=$(phase0_session::_path_identity "$(phase0_session::target audit_log)") || return 1
  ATLAS_PHASE0_OPERATION["audit_boundary_lines"]=$line_count
  ATLAS_PHASE0_OPERATION["audit_boundary_sha"]=$(phase0_session::_sha256 "$snapshot") || return 1
  ATLAS_PHASE0_OPERATION["audit_log_identity"]=$source_identity
  printf '{"sourceIdentity":"%s","lineCount":%s,"prefixSHA256":"%s"}\n' \
    "$source_identity" "$line_count" "$(phase0_session::operation audit_boundary_sha)" > "$record" || return 1
  chmod 0400 "$record" || return 1
  phase0_session::journal_append AUDIT_BOUNDARY RECORDED \
    "pre-mutation lines=${line_count} sha256=$(phase0_session::operation audit_boundary_sha)" || return 1
}

phase0_ceremony::_verify_audit_delta() {
  local delta=$1 line username code
  local session_id binding_name recovery_principal authorizer_principal
  local recovery_allowed=false recovery_denied=false authorizer_allowed=false authorizer_denied=false annotation=false
  session_id=$(phase0_session::operation session_id) || return 1
  binding_name="atlas-bg-canary-${session_id}"
  recovery_principal=$(phase0_session::operation recovery_principal) || return 1
  authorizer_principal=$(phase0_session::operation authorizer_principal) || return 1
  [[ -s $delta ]] || {
    recovery::die "API audit delta is empty; stale evidence cannot satisfy this ceremony"
    return 1
  }
  grep -Fq "$session_id" "$delta" || return 1
  grep -Fq "$binding_name" "$delta" || return 1
  while IFS= read -r line || [[ -n $line ]]; do
    username=$(yq -r '.user.username // ""' <<< "$line") || return 1
    code=$(yq -r '.responseStatus.code // 200' <<< "$line") || return 1
    [[ $code =~ ^[0-9]+$ ]] || return 1
    if grep -Fq 'validation.policy.admission.k8s.io/' <<< "$line"; then
      annotation=true
    fi
    case "$username" in
      "$recovery_principal")
        if ((code >= 200 && code < 300)); then recovery_allowed=true; fi
        if ((code >= 400)); then recovery_denied=true; fi
        ;;
      "$authorizer_principal")
        if ((code >= 200 && code < 300)); then authorizer_allowed=true; fi
        if ((code >= 400)); then authorizer_denied=true; fi
        ;;
    esac
  done < "$delta"
  [[ $recovery_allowed == true && $recovery_denied == true &&
    $authorizer_allowed == true && $authorizer_denied == true && $annotation == true ]] || {
    recovery::die "current-session audit delta lacks principal outcomes or VAP Audit annotation"
    return 1
  }
}

phase0_ceremony::_finalize_evidence() {
  local session audit_copy audit_prefix audit_delta result manifest file relative boundary_lines
  [[ -z $ATLAS_PHASE0_LOCK_PATH && -z $ATLAS_PHASE0_LOCK_TOKEN ]] || {
    recovery::die "Phase-0 evidence cannot close while the runtime lock is held"
    return 1
  }
  session=$(phase0_session::operation evidence_session) || return 1
  audit_copy="${session}/audit/kube-apiserver-audit.log"
  audit_prefix="${session}/audit/verified-pre-mutation-prefix.jsonl"
  audit_delta="${session}/audit/current-session.jsonl"
  [[ $(phase0_session::_path_identity "$(phase0_session::target audit_log)") == "$(phase0_session::operation audit_log_identity)" ]] || {
    recovery::die "API audit log identity changed during the ceremony"
    return 1
  }
  phase0_ceremony::_snapshot_audit_log "$audit_copy" || return 1
  boundary_lines=$(phase0_session::operation audit_boundary_lines) || return 1
  sed -n "1,${boundary_lines}p" "$audit_copy" > "$audit_prefix" || return 1
  [[ $(phase0_session::_sha256 "$audit_prefix") == "$(phase0_session::operation audit_boundary_sha)" ]] || {
    recovery::die "API audit prefix changed after the pre-mutation boundary"
    return 1
  }
  sed -n "$((boundary_lines + 1)),\$p" "$audit_copy" > "$audit_delta" || return 1
  chmod 0400 "$audit_prefix" "$audit_delta" || return 1
  phase0_ceremony::_verify_audit_delta "$audit_delta" || return 1
  if grep -ERq 'BEGIN ([A-Z ]+)?PRIVATE KEY|client-key-data|bearerToken|"token"[[:space:]]*:' "$session"; then
    recovery::die "credential material was detected in Phase-0 evidence"
    return 1
  fi
  phase0_session::journal_append RESULT SUCCEEDED "Phase-0 runtime closure evidence completed; drill cluster retained" || return 1
  result="${session}/result.json"
  printf '{"status":"SUCCEEDED","clusterName":"%s","targetFingerprintSHA256":"%s","sessionID":"%s","canaryResources":"ABSENT","temporaryCredentials":"ABSENT","runtimeLock":"ABSENT","drillCluster":"RETAINED","journalTipSHA256":"%s"}\n' \
    "$(phase0_session::target cluster_name)" "$(phase0_session::operation target_fingerprint)" \
    "$(phase0_session::operation session_id)" "$ATLAS_PHASE0_JOURNAL_PREVIOUS_SHA" > "$result" || return 1
  chmod 0400 "$result" || return 1
  manifest="${session}/bundle.sha256"
  : > "$manifest" || return 1
  while IFS= read -r file; do
    [[ $file != "$manifest" ]] || continue
    relative=${file#"${session}/"}
    printf '%s  %s\n' "$(phase0_session::_sha256 "$file")" "$relative" >> "$manifest" || return 1
  done < <(find "$session" -type f | LC_ALL=C sort)
  chmod 0400 "$manifest" || return 1
}

phase0_ceremony::_run() {
  phase0_ceremony::_capture_audit_boundary || return 1
  phase0_ceremony::_issue_credentials || return 1
  phase0_ceremony::_install_definitions || return 1
  phase0_ceremony::_verify_active_permissions || return 1
  phase0_ceremony::_admission_escape_drill || return 1
  phase0_ceremony::_session_authorization_drill || return 1
  phase0_ceremony::_cleanup_cluster_resources || return 1
  phase0_ceremony::_cleanup_credentials || return 1
}

phase0_ceremony::run() {
  local cluster_name=$1 context=$2 admin_kubeconfig=$3 audit_directory=$4 creation_evidence=$5
  local evidence_root=$6 credential_directory=$7 storage_assertion=$8 known_good_revision=$9
  local recovery_generation=${10} previous_recovery_generation=${11}
  local authorizer_generation=${12} previous_authorizer_generation=${13} status release_status=0
  phase0_session::resolve_target "$cluster_name" "$context" "$admin_kubeconfig" "$audit_directory" \
    "$creation_evidence" "$evidence_root" "$credential_directory" "$storage_assertion" \
    "$known_good_revision" "$recovery_generation" "$previous_recovery_generation" \
    "$authorizer_generation" "$previous_authorizer_generation" || return 1
  phase0_session::_tool_preflight || return 1
  phase0_session::acquire_lock || return 1
  phase0_session::arm_unexpected_exit_guard
  if phase0_session::prepare; then
    if phase0_session::human_gate; then
      if phase0_session::revalidate; then
        if phase0_session::journal_append PREMUTATION READY "approved authority inputs revalidated"; then
          if phase0_ceremony::_run; then
            status=0
          else
            status=$?
            phase0_session::journal_append RESULT FAILED "runtime stopped fail closed; retained state requires review" || true
          fi
        else
          status=$?
        fi
      else
        status=$?
        phase0_session::journal_append PREMUTATION DENIED "approved authority inputs changed or became unavailable" || true
      fi
    else
      status=$?
    fi
  else
    status=$?
  fi
  phase0_session::release_lock || release_status=$?
  ((release_status == 0)) || return "$release_status"
  if ((status == 0)); then
    if phase0_ceremony::_finalize_evidence; then
      printf 'phase0-runtime\tCLOSED\t%s\t%s\n' \
        "$(phase0_session::target cluster_name)" "$(phase0_session::operation evidence_session)"
    else
      status=$?
      phase0_session::journal_append RESULT FAILED \
        "runtime resources closed but final evidence sealing failed" || true
    fi
  fi
  phase0_session::disarm_unexpected_exit_guard
  return "$status"
}
