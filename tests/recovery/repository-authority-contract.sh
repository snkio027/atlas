#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'
umask 077

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-repository-authority.XXXXXX")
trap 'rm -rf -- "$test_workspace"' EXIT
fixture="${test_workspace}/atlas"
mkdir -p "$fixture/env"
chmod 0700 "$fixture" "$fixture/env"

write_profile() {
  local environment=$1 repository_url=$2
  printf 'ATLAS_ENVIRONMENT=%s\nATLAS_GIT_REPO_URL=%s\n' \
    "$environment" "$repository_url" > "$fixture/env/local-orbstack.env"
}

write_profile local-orbstack https://github.com/snkio027/atlas.git
git -C "$fixture" init -q -b main
git -C "$fixture" config user.name atlas-test
git -C "$fixture" config user.email atlas-test@example.invalid
git -C "$fixture" add env/local-orbstack.env
git -C "$fixture" commit -q -m initial
git -C "$fixture" remote add origin https://github.com/snkio027/atlas.git

readonly ATLAS_RECOVERY_ROOT_DIR=$fixture

recovery::die() {
  printf '%s\n' "$*" >&2
  return 1
}

# shellcheck source=bootstrap/recovery/principal-identity.sh
source bootstrap/recovery/principal-identity.sh
# shellcheck source=bootstrap/recovery/canary-session.sh
source bootstrap/recovery/canary-session.sh

ATLAS_PHASE0_TARGET[environment_name]=local-orbstack
ATLAS_PHASE0_TARGET[known_good_revision]=$(git -C "$fixture" rev-parse HEAD)
authority=$(phase0_session::_git_authority)
IFS=$'\t' read -r commit tree repository_url environment_name <<< "$authority"
[[ $commit == "${ATLAS_PHASE0_TARGET[known_good_revision]}" &&
  $tree == "$(git -C "$fixture" rev-parse 'HEAD^{tree}')" &&
  $repository_url == https://github.com/snkio027/atlas.git &&
  $environment_name == local-orbstack ]] ||
  test::fail "reviewed Git authority projection is incomplete"

for equivalent_url in \
  https://github.com/snkio027/atlas \
  git@github.com:snkio027/atlas.git \
  ssh://git@github.com/snkio027/atlas.git; do
  [[ $(phase0_session::_canonical_repository_url "$equivalent_url") == https://github.com/snkio027/atlas.git ]] ||
    test::fail "equivalent repository URL did not normalize: ${equivalent_url}"
done

for unsafe_url in \
  http://github.com/snkio027/atlas.git \
  https://example.com/snkio027/atlas.git \
  'https://github.com/snkio027/atlas.git?ref=main' \
  https://github.com/nested/snkio027/atlas.git; do
  if phase0_session::_canonical_repository_url "$unsafe_url" > /dev/null 2>&1; then
    test::fail "unsafe repository URL was accepted: ${unsafe_url}"
  fi
done

git -C "$fixture" remote set-url origin git@github.com:snkio027/atlas.git
phase0_session::_git_authority > /dev/null
git -C "$fixture" remote set-url origin https://github.com/foreign/atlas.git
if phase0_session::_git_authority > /dev/null 2>&1; then
  test::fail "wrong-fork Git origin was accepted"
fi
git -C "$fixture" remote set-url origin https://github.com/snkio027/atlas.git

write_profile test https://github.com/snkio027/atlas.git
git -C "$fixture" add env/local-orbstack.env
git -C "$fixture" commit -q -m wrong-environment
ATLAS_PHASE0_TARGET[known_good_revision]=$(git -C "$fixture" rev-parse HEAD)
if phase0_session::_git_authority > /dev/null 2>&1; then
  test::fail "reviewed Profile with a foreign environment was accepted"
fi

write_profile local-orbstack https://github.com/foreign/atlas.git
git -C "$fixture" add env/local-orbstack.env
git -C "$fixture" commit -q -m wrong-repository
ATLAS_PHASE0_TARGET[known_good_revision]=$(git -C "$fixture" rev-parse HEAD)
if phase0_session::_git_authority > /dev/null 2>&1; then
  test::fail "reviewed Profile with a foreign repository was accepted"
fi

test::pass "Phase-0 repository and environment authority contract"
