#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly preflight=$probe_root/personal-local-preflight
readonly profile=$probe_root/personal-local-profile-v1.json
readonly expected_commit=${1:-}
test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-personal-local-v1-fence.XXXXXX")
cleanup() {
  rm -rf "$test_workspace"
}
trap cleanup EXIT

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] ||
  test::fail "expected contract commit must be supplied by the caller"
[[ -x $preflight ]] || test::fail "historical PERSONAL_LOCAL v1 validator is not executable"

canonical_profile=$(yq -o=json -I=0 'sort_keys(..)' "$profile") ||
  test::fail "could not canonicalize historical v1 Profile"
profile_sha=$(printf '%s' "$canonical_profile" | shasum -a 256 | awk '{print $1}')
[[ $profile_sha == 34e42bc31933ecf63fa5d878b611c3119415c3503481c7863e5e1cb5a4eff949 &&
  $(yq -r '.profileID' "$profile") == atlas.argocd.authorization-probe-profile/personal-local/v1 ]] ||
  test::fail "historical PERSONAL_LOCAL v1 authority drifted"

sentinel_bin=$test_workspace/bin
sentinel_log=$test_workspace/client-invocations.log
mkdir -p "$sentinel_bin"
for executable in kubectl openssl helm git; do
  helper=$sentinel_bin/$executable
  apply_name=$executable
  printf '#!/usr/bin/env bash\nprintf "%%s\\n" %q >> %q\nexit 97\n' "$apply_name" "$sentinel_log" > "$helper"
  chmod 0700 "$helper"
done

for command in run validate; do
  stdout_file=$test_workspace/${command}.stdout
  stderr_file=$test_workspace/${command}.stderr
  artifact_file=$test_workspace/${command}-artifact.json
  status=0
  arguments=(
    --target "$test_workspace/does-not-exist-target.json"
    --owner-gate "$test_workspace/does-not-exist-gate.json"
    --expected-owner-gate-sha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    --expected-commit "$expected_commit"
  )
  if [[ $command == run ]]; then
    arguments+=(--output "$artifact_file")
  else
    arguments+=(--evidence "$artifact_file")
  fi
  PATH=$sentinel_bin:$PATH \
    TMPDIR=$test_workspace \
    KUBECONFIG=$test_workspace/ambient-kubeconfig \
    ARGOCD_AUTH_TOKEN=ATLAS_V1_FENCE_SENTINEL \
    "$preflight" "$command" "${arguments[@]}" > "$stdout_file" 2> "$stderr_file" || status=$?

  [[ $status -eq 24 && ! -s $stdout_file && ! -e $artifact_file && ! -s $sentinel_log ]] ||
    test::fail "v1 ${command} crossed the pre-authority execution fence"
  grep -Fqx 'PERSONAL_LOCAL_BLOCKED: PERSONAL_LOCAL profile v1 is never live-eligible' "$stderr_file" ||
    test::fail "v1 ${command} did not return the canonical blocked classification"
done

if find "$test_workspace" -maxdepth 1 -type d -name 'atlas-personal-local-preflight.*' | grep -q .; then
  test::fail "v1 command created a temporary execution workspace"
fi

test::pass "PERSONAL_LOCAL v1 run and validate are permanently fenced before authority access"
