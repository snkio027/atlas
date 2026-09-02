#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

readonly expected_commit=${1:-}
readonly probe_root=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract
readonly library=$ATLAS_TEST_ROOT/$probe_root/lib/contract-primitives.sh

[[ $expected_commit =~ ^[0-9a-f]{40}$ ]] || test::fail 'expected contract commit must be supplied'
[[ -f $library && ! -L $library ]] || test::fail 'shared contract primitive library is unavailable'

# shellcheck source=gitops/platform/management/protection-foundation/definitions/argo-hardening/probe-contract/lib/contract-primitives.sh
source "$library"
[[ ${ATLAS_CONTRACT_PRIMITIVES_LOADED:-} == 1 ]] || test::fail 'shared primitive marker is unavailable'

workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-contract-primitives.XXXXXX")
chmod 0700 "$workspace"
workspace=$(cd "$workspace" && pwd -P)
fixture_repo=${workspace}/repo
cleanup() {
  rm -rf "$workspace"
}
trap cleanup EXIT

document=${workspace}/document.json
schema=${workspace}/schema.json
printf '%s' '{"b":2,"a":1}' > "$document"
printf '%s' '{"type":"object","required":["a","b"]}' > "$schema"
[[ $(contract_primitives::canonical_json "$document") == '{"a":1,"b":2}' &&
$(contract_primitives::canonical_sha "$document") == 43258cff783fe7036d8a43033f830adfc60ec037382473548ac742b888292777 &&
$(printf '%s' atlas | contract_primitives::sha256_text) == 7c82602500857aa6ed0cf38c4c3e4ec645bdcaa82c00b9155eb08be100c778a9 ]] ||
  test::fail 'canonical bytes or SHA primitive drifted'
if contract_primitives::document_is_canonical "$document"; then
  test::fail 'non-canonical document was accepted'
fi
canonical_projection=$(contract_primitives::canonical_json "$document")
printf '%s' "$canonical_projection" > "${document}.canonical"
contract_primitives::document_is_canonical "${document}.canonical" || test::fail 'canonical document was rejected'
contract_primitives::assert_exact_keys "$document" "$schema" || test::fail 'exact-key projection was rejected'
contract_primitives::assert_tags "$document" '!!int' '.a' '.b' || test::fail 'tag projection was rejected'

private_dir=${workspace}/private
mkdir -m 0700 "$private_dir"
private_dir=$(cd "$private_dir" && pwd -P)
contract_primitives::assert_owner_directory "$private_dir" 700 || test::fail 'private owner directory was rejected'
[[ $(contract_primitives::path_uid "$private_dir") == "$(id -u)" &&
$(contract_primitives::path_mode "$private_dir") == 700 &&
$(contract_primitives::canonical_directory "$private_dir") == "$private_dir" &&
$(contract_primitives::path_identity "$private_dir") =~ ^[^:]+:[^:]+:[0-9]+:[0-7]+:[0-9]+$ ]] ||
  test::fail 'portable stat or canonical-directory projection drifted'
chmod 0750 "$private_dir"
if contract_primitives::assert_owner_directory "$private_dir" 700; then
  test::fail 'directory mode drift was accepted'
fi
chmod 0700 "$private_dir"
ln -s "$private_dir" "${workspace}/private-link"
if contract_primitives::assert_owner_directory "${workspace}/private-link" 700; then
  test::fail 'directory symlink was accepted'
fi

private_file=${private_dir}/private.json
printf '%s' '{}' > "$private_file"
chmod 0600 "$private_file"
contract_primitives::assert_private_file "$private_file" true || test::fail 'private file was rejected'
chmod 0644 "$private_file"
if contract_primitives::assert_private_file "$private_file" true; then
  test::fail 'private file mode drift was accepted'
fi
chmod 0600 "$private_file"
ln -s "$private_file" "${private_file}.link"
if contract_primitives::assert_private_file "${private_file}.link" true; then
  test::fail 'private file symlink was accepted'
fi

executable=${private_dir}/tool
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' > "$executable"
chmod 0700 "$executable"
contract_primitives::assert_executable_file "$executable" || test::fail 'private executable was rejected'
chmod 0722 "$executable"
if contract_primitives::assert_executable_file "$executable"; then
  test::fail 'group-writable executable was accepted'
fi

output=${private_dir}/result.json
contract_primitives::assert_output_destination "$output" || test::fail 'fresh output destination was rejected'
contract_primitives::atomic_create "${document}.canonical" "$output" || test::fail 'atomic create failed'
[[ $(contract_primitives::sha256_file "$output") == "$(contract_primitives::sha256_file "${document}.canonical")" &&
$(contract_primitives::path_mode "$output") == 600 ]] || test::fail 'atomic output projection drifted'
if contract_primitives::assert_output_destination "$output"; then
  test::fail 'existing output destination was accepted'
fi
ln -s "${private_dir}/absent" "${private_dir}/output-link"
if contract_primitives::assert_output_destination "${private_dir}/output-link"; then
  test::fail 'output symlink was accepted'
fi

capture_tmp=${workspace}/capture
mkdir -m 0700 "$capture_tmp"
captured=
contract_primitives::capture "$capture_tmp" captured printf '%s' expected || test::fail 'strict capture rejected clean stdout'
[[ $captured == expected ]] || test::fail 'strict capture changed stdout'
if contract_primitives::capture "$capture_tmp" captured sh -c 'printf diagnostic >&2'; then
  test::fail 'strict capture accepted stderr'
fi
if contract_primitives::capture "$capture_tmp" captured sh -c 'exit 7'; then
  test::fail 'strict capture accepted non-zero exit'
fi

versions=${workspace}/versions.lock
printf '%s\n' 'TOOL_VERSION=1.2.3' > "$versions"
[[ $(contract_primitives::locked_version "$versions" TOOL_VERSION) == 1.2.3 ]] ||
  test::fail 'locked version projection drifted'
printf '%s\n' 'TOOL_VERSION=1.2.3' >> "$versions"
if contract_primitives::locked_version "$versions" TOOL_VERSION > /dev/null; then
  test::fail 'duplicate locked version was accepted'
fi

isolated_root=$(GIT_DIR=/nonexistent GIT_WORK_TREE=/nonexistent \
  contract_primitives::git -C "$ATLAS_TEST_ROOT" rev-parse --show-toplevel) ||
  test::fail 'isolated Git primitive inherited ambient Git authority'
[[ $isolated_root == "$ATLAS_TEST_ROOT" ]] || test::fail 'isolated Git primitive resolved the wrong repository'

git clone --quiet --no-hardlinks "$ATLAS_TEST_ROOT" "$fixture_repo"
git -C "$fixture_repo" checkout --quiet --detach "$expected_commit"
fixture_library=${fixture_repo}/${probe_root}/lib/contract-primitives.sh
approved_library_blob=$(git -C "$fixture_repo" rev-parse "${expected_commit}:${probe_root}/lib/contract-primitives.sh")
readonly approved_library_blob

restore_fixture_library() {
  git -C "$fixture_repo" show "${expected_commit}:${probe_root}/lib/contract-primitives.sh" > "$fixture_library"
  chmod 0644 "$fixture_library"
}

assert_loader_blocked() {
  local executable_name=$1 stderr status
  stderr=${workspace}/${executable_name}.stderr
  local executable_path=${fixture_repo}/${probe_root}/${executable_name}
  case $executable_name in
    personal-local-target-materialization)
      set +e
      "$executable_path" run --owner-gate /absent --expected-owner-gate-sha "$(printf '0%.0s' {1..64})" \
        --expected-commit "$expected_commit" --output "${private_dir}/${executable_name}.json" > /dev/null 2> "$stderr"
      status=$?
      set -e
      ;;
    personal-local-target-v2)
      set +e
      "$executable_path" project --materialization-owner-gate /absent \
        --expected-materialization-owner-gate-sha "$(printf '0%.0s' {1..64})" \
        --materialization-evidence /absent --expected-commit "$expected_commit" > /dev/null 2> "$stderr"
      status=$?
      set -e
      ;;
    personal-local-read-only-preflight)
      set +e
      "$executable_path" validate --target /absent --materialization-owner-gate /absent \
        --expected-materialization-owner-gate-sha "$(printf '0%.0s' {1..64})" --materialization-evidence /absent \
        --final-owner-gate /absent --expected-final-owner-gate-sha "$(printf '0%.0s' {1..64})" \
        --expected-commit "$expected_commit" --evidence /absent > /dev/null 2> "$stderr"
      status=$?
      set -e
      ;;
  esac
  [[ $status -eq 24 && $(< "$stderr") == *'shared contract primitive authority validation failed'* ]] ||
    test::fail "${executable_name} did not fail closed on shared-library authority drift"
}

for executable_name in personal-local-target-materialization personal-local-target-v2 personal-local-read-only-preflight; do
  printf '\n' >> "$fixture_library"
  assert_loader_blocked "$executable_name"
  restore_fixture_library
done

for executable_name in personal-local-target-materialization personal-local-target-v2 personal-local-read-only-preflight; do
  chmod 0600 "$fixture_library"
  assert_loader_blocked "$executable_name"
  chmod 0644 "$fixture_library"
done

for executable_name in personal-local-target-materialization personal-local-target-v2 personal-local-read-only-preflight; do
  mv "$fixture_library" "${fixture_library}.regular"
  ln -s "${fixture_library}.regular" "$fixture_library"
  assert_loader_blocked "$executable_name"
  rm -f "$fixture_library"
  mv "${fixture_library}.regular" "$fixture_library"
done

replacement_source=${workspace}/replacement-contract-primitives.sh
replacement_sentinel=${workspace}/replacement-executed
# shellcheck disable=SC2016 # replacement source must expand the sentinel only if it is executed
printf '%s\n' \
  'printf "%s\n" replacement-executed > "${ATLAS_TOCTOU_SENTINEL}"' \
  'readonly ATLAS_CONTRACT_PRIMITIVES_LOADED=replacement' > "$replacement_source"
chmod 0600 "$replacement_source"

wrapper_dir=${workspace}/git-wrapper
mkdir -m 0700 "$wrapper_dir"
git_wrapper=${wrapper_dir}/git
real_git=$(command -v git)
{
  printf '%s\n' '#!/usr/bin/env bash' 'set -u'
  printf 'real_git=%q\n' "$real_git"
  printf 'replacement_source=%q\n' "$replacement_source"
  printf 'fixture_library=%q\n' "$fixture_library"
  printf 'approved_library_blob=%q\n' "$approved_library_blob"
  # shellcheck disable=SC2016 # write the wrapper's runtime expressions literally
  printf '"$real_git" "$@"\n'
  printf 'status=$?\n'
  # shellcheck disable=SC2016 # write the wrapper's runtime expressions literally
  printf 'if [[ $status -eq 0 && " $* " == *" cat-file blob ${approved_library_blob} "* ]]; then\n'
  # shellcheck disable=SC2016 # write the wrapper's runtime expressions literally
  printf '  cp "$replacement_source" "$fixture_library"\n'
  # shellcheck disable=SC2016 # write the wrapper's runtime expressions literally
  printf '  chmod 0644 "$fixture_library"\n'
  printf 'fi\n'
  # shellcheck disable=SC2016 # write the wrapper's runtime expressions literally
  printf 'exit "$status"\n'
} > "$git_wrapper"
chmod 0700 "$git_wrapper"

toctou_stdout=${workspace}/toctou.stdout
toctou_stderr=${workspace}/toctou.stderr
target_v2_executable=${fixture_repo}/${probe_root}/personal-local-target-v2
bash_path=$(command -v bash)
set +e
# shellcheck disable=SC2016 # the clean child shell expands only its explicitly supplied arguments
env -i PATH="${wrapper_dir}:$PATH" LC_ALL=C ATLAS_TOCTOU_SENTINEL="$replacement_sentinel" \
  "$bash_path" -c '
    set -Eeuo pipefail
    source "$1"
    target_v2::_load_contract_primitives "$2"
    printf "%s\n" "${ATLAS_CONTRACT_PRIMITIVES_LOADED}"
  ' _ "$target_v2_executable" "$expected_commit" > "$toctou_stdout" 2> "$toctou_stderr"
toctou_status=$?
set -e
[[ $toctou_status -eq 0 && $(< "$toctou_stdout") == 1 && ! -s $toctou_stderr ]] ||
  test::fail 'approved Git blob snapshot was not loaded after working-tree replacement'
[[ ! -e $replacement_sentinel ]] || test::fail 'replacement working-tree library code was executed'
[[ $(git hash-object --no-filters "$fixture_library") != "$approved_library_blob" ]] ||
  test::fail 'TOCTOU fixture did not replace the working-tree library'

test::pass 'shared PERSONAL_LOCAL contract primitives and blob-bound loader'
