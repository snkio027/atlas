#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-status-exit.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

fixture_root="${test_workspace}/atlas"
fixture_bootstrap="${fixture_root}/bootstrap"
argocd_log="${test_workspace}/argocd.log"
config_log="${test_workspace}/config.log"
mkdir -p \
  "${fixture_bootstrap}/lib" \
  "${fixture_bootstrap}/host" \
  "${fixture_bootstrap}/cluster" \
  "${fixture_bootstrap}/registry" \
  "${fixture_bootstrap}/argocd" \
  "${fixture_bootstrap}/status"
cp bootstrap/atlas "${fixture_bootstrap}/atlas"
cp bootstrap/status/report.sh "${fixture_bootstrap}/status/report.sh"

cat > "${fixture_bootstrap}/lib/runtime.sh" << 'EOF'
runtime::die() {
  printf 'ERROR: %s\n' "$*" >&2
  return 1
}
runtime::on_exit() { :; }
EOF
cat > "${fixture_bootstrap}/lib/lock.sh" << 'EOF'
lock::acquire() { :; }
lock::release() { :; }
EOF
cat > "${fixture_bootstrap}/lib/config.sh" << 'EOF'
config::load() {
  printf 'load\n' >> "$ATLAS_TEST_CONFIG_LOG"
}
EOF
cat > "${fixture_bootstrap}/host/doctor.sh" << 'EOF'
host::doctor() { :; }
EOF
cat > "${fixture_bootstrap}/cluster/kind.sh" << 'EOF'
cluster::inspect_status() {
  printf '%s\n' "${ATLAS_TEST_CLUSTER_RECORD:-$'cluster\tREADY\tatlas-test'}"
  return "${ATLAS_TEST_CLUSTER_RC:-0}"
}
cluster::ensure_kind() { :; }
EOF
cat > "${fixture_bootstrap}/registry/local.sh" << 'EOF'
registry::inspect_status() {
  printf '%s\n' "${ATLAS_TEST_REGISTRY_RECORD:-$'registry\tREADY\tatlas-registry'}"
  return "${ATLAS_TEST_REGISTRY_RC:-0}"
}
registry::ensure_local() { :; }
EOF
cat > "${fixture_bootstrap}/argocd/render.sh" << 'EOF'
argocd::render() { :; }
EOF
cat > "${fixture_bootstrap}/argocd/seed.sh" << 'EOF'
argocd::install_seed() { :; }
EOF
cat > "${fixture_bootstrap}/argocd/handoff.sh" << 'EOF'
argocd::handoff() { :; }
EOF
cat > "${fixture_bootstrap}/argocd/status.sh" << 'EOF'
argocd::inspect_status() {
  printf 'inspect\n' >> "$ATLAS_TEST_ARGOCD_LOG"
  case "${ATLAS_TEST_ARGOCD_MODE:-complete}" in
    complete)
      printf '%s\n' "${ATLAS_TEST_ARGOCD_RECORD:-$'argocd\tREADY\targocd'}"
      printf '%s\n' "${ATLAS_TEST_ROOT_RECORD:-$'root\tSynced/Healthy\tatlas-root'}"
      printf '%s\n' "${ATLAS_TEST_SELF_RECORD:-$'argocd-self\tSynced/Healthy\targocd-self'}"
      ;;
    partial)
      printf '%s\n' "${ATLAS_TEST_ARGOCD_RECORD:-$'argocd\tREADY\targocd'}"
      ;;
    empty) ;;
    *) return 64 ;;
  esac
  return "${ATLAS_TEST_ARGOCD_RC:-0}"
}
EOF
chmod 0700 "${fixture_bootstrap}/atlas"
: > "$argocd_log"
: > "$config_log"

export ATLAS_TEST_ARGOCD_LOG=$argocd_log
export ATLAS_TEST_CONFIG_LOG=$config_log

run_status() {
  local expected_status=$1
  shift
  local output actual_status
  if output=$(env "$@" "${fixture_bootstrap}/atlas" status --env test --check 2> "${test_workspace}/stderr"); then
    actual_status=0
  else
    actual_status=$?
  fi
  ((actual_status == expected_status)) ||
    test::fail "status --check returned ${actual_status}; expected ${expected_status}; output=${output}"
  printf '%s\n' "$output"
}

ready_output=$(run_status 0)
[[ $(wc -l <<< "$ready_output") -eq 5 ]] || test::fail "ready status did not emit exactly five component records"
grep -Fqx $'root\tSynced/Healthy\tatlas-root' <<< "$ready_output" || test::fail "ready Root status was not preserved"
test::pass "status --check succeeds only for the complete ready report"

if default_output=$(env \
  ATLAS_TEST_CLUSTER_RECORD=$'cluster\tDRIFTED\tatlas-test' \
  "${fixture_bootstrap}/atlas" status --env test 2> "${test_workspace}/stderr"); then
  default_status=0
else
  default_status=$?
fi
((default_status == 0)) || test::fail "observational status changed its compatibility exit code"
grep -Fqx $'cluster\tDRIFTED\tatlas-test' <<< "$default_output" || test::fail "observational status hid drift"

run_status 1 ATLAS_TEST_CLUSTER_RECORD=$'cluster\tDRIFTED\tatlas-test' > /dev/null
run_status 1 ATLAS_TEST_REGISTRY_RECORD=$'registry\tABSENT\tatlas-registry' > /dev/null
run_status 1 ATLAS_TEST_ARGOCD_RECORD=$'argocd\tDEGRADED\targocd' > /dev/null
run_status 1 ATLAS_TEST_ROOT_RECORD=$'root\tOutOfSync/Healthy\tatlas-root' > /dev/null
test::pass "known absent, drifted, degraded, and unhealthy states return 1"

run_status 2 \
  ATLAS_TEST_CLUSTER_RECORD=$'cluster\tDRIFTED\tatlas-test' \
  ATLAS_TEST_REGISTRY_RECORD=$'registry\tUNAVAILABLE\tatlas-registry' > /dev/null
run_status 2 ATLAS_TEST_ROOT_RECORD=$'root\tFUTURE_STATE\tatlas-root' > /dev/null
run_status 2 ATLAS_TEST_REGISTRY_RECORD=$'unexpected\tREADY\tatlas-registry' > /dev/null
run_status 2 ATLAS_TEST_REGISTRY_RECORD=$'cluster\tREADY\tduplicate-cluster' > /dev/null
run_status 2 ATLAS_TEST_ROOT_RECORD=$'root\tSynced/Healthy\tatlas-root\textra-field' > /dev/null
run_status 2 ATLAS_TEST_ARGOCD_MODE=partial > /dev/null
test::pass "unavailable, unknown, unexpected, duplicate, malformed, and incomplete reports fail closed with 2"

: > "$argocd_log"
absent_output=$(run_status 1 \
  ATLAS_TEST_CLUSTER_RECORD=$'cluster\tABSENT\tatlas-test' \
  ATLAS_TEST_CLUSTER_RC=1)
[[ ! -s $argocd_log ]] || test::fail "Argo CD was queried for an absent cluster"
grep -Fqx $'argocd-self\tABSENT\tcluster absent' <<< "$absent_output" ||
  test::fail "absent cluster did not synthesize Argo CD absence"

: > "$argocd_log"
unavailable_output=$(run_status 2 \
  ATLAS_TEST_CLUSTER_RECORD=$'cluster\tUNAVAILABLE\tatlas-test' \
  ATLAS_TEST_CLUSTER_RC=2)
[[ ! -s $argocd_log ]] || test::fail "Argo CD was queried when cluster status was unavailable"
grep -Fqx $'root\tUNAVAILABLE\tcluster status unavailable' <<< "$unavailable_output" ||
  test::fail "unavailable cluster did not synthesize Argo CD uncertainty"

: > "$argocd_log"
run_status 1 ATLAS_TEST_CLUSTER_RECORD=$'cluster\tDRIFTED\tatlas-test' > /dev/null
[[ $(wc -l < "$argocd_log") -eq 1 ]] ||
  test::fail "a drifted existing cluster did not use exactly one Argo CD inspection"
test::pass "one cluster snapshot governs downstream Argo CD inspection"

: > "$config_log"
if "${fixture_bootstrap}/atlas" doctor --check > /dev/null 2> "${test_workspace}/stderr"; then
  test::fail "doctor accepted the status-only --check option"
fi
[[ ! -s $config_log ]] || test::fail "invalid --check usage loaded the environment profile"
grep -Fq -- '--check is valid only with status' "${test_workspace}/stderr" ||
  test::fail "invalid --check usage lacked a precise error"
test::pass "--check is status-only and argument errors remain profile-free"
