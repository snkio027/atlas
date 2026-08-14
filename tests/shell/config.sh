#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-config-test.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

config_module="${ATLAS_TEST_ROOT}/bootstrap/lib/config.sh"

config::_fixture() {
  local name=$1 fixture
  fixture="${test_workspace}/${name}"
  mkdir -p \
    "${fixture}/env" \
    "${fixture}/clusters/kind" \
    "${fixture}/vendor/charts/argo-cd-10.3.3" \
    "${fixture}/gitops/root/overlays/local-orbstack" \
    "${fixture}/gitops/root/base" \
    "${fixture}/gitops/platform/applications/base" \
    "${fixture}/gitops/platform/management/argocd-self" \
    "${fixture}/gitops/platform/management/projects" \
    "${fixture}/bootstrap/argocd"

  cp versions.lock "${fixture}/versions.lock"
  cp env/local-orbstack.env "${fixture}/env/local-orbstack.env"
  cp bootstrap/argocd/atlas-bootstrap-project.yaml "${fixture}/bootstrap/argocd/"
  cp gitops/root/base/*-app.yaml "${fixture}/gitops/root/base/"
  cp gitops/platform/applications/base/argocd-self-app.yaml "${fixture}/gitops/platform/applications/base/"
  cp gitops/platform/management/projects/*-project.yaml "${fixture}/gitops/platform/management/projects/"
  : > "${fixture}/clusters/kind/local-orbstack.yaml"
  : > "${fixture}/vendor/charts/argo-cd-10.3.3.tgz"
  : > "${fixture}/vendor/charts/argo-cd-10.3.3/Chart.yaml"
  : > "${fixture}/gitops/platform/management/argocd-self/values.yaml"
  printf '%s\n' "$fixture"
}

config::_load() {
  local root=$1 environment=${2:-local-orbstack}
  bash -Eeuo pipefail -c '
    source "$1"
    config::load "$2" "$3"
  ' _ "$config_module" "$root" "$environment"
}

config::_replace() {
  local file=$1 key=$2 value=$3
  sed -i.bak "s#^${key}=.*#${key}=${value}#" "$file"
  rm -f "${file}.bak"
}

config::_assert_rejected() {
  local label=$1 root=$2 environment=${3:-local-orbstack}
  if config::_load "$root" "$environment" > /dev/null 2>&1; then
    test::fail "configuration accepted ${label}"
  fi
}

valid_fixture=$(config::_fixture valid)
config::_load "$valid_fixture"
test::pass "valid configuration resolves"

override_output=$(ATLAS_CLUSTER_NAME=environment-override bash -Eeuo pipefail -c '
  source "$1"
  config::load "$2" local-orbstack
  config::get ATLAS_CLUSTER_NAME
' _ "$config_module" "$valid_fixture")
[[ $override_output == atlas-local ]] || test::fail "process environment overrode the selected profile"

if bash -Eeuo pipefail -c '
  source "$1"
  config::load "$2" local-orbstack
  ATLAS_CONFIG[ATLAS_CLUSTER_NAME]=mutated
' _ "$config_module" "$valid_fixture" > /dev/null 2>&1; then
  test::fail "resolved configuration remained mutable"
fi
test::pass "resolved configuration is file-owned and read-only"

fixture=$(config::_fixture environment-mismatch)
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_ENVIRONMENT wrong-profile
config::_assert_rejected "a profile identity mismatch" "$fixture"

for port in 1023 65536; do
  fixture=$(config::_fixture "port-${port}")
  config::_replace "${fixture}/env/local-orbstack.env" ATLAS_REGISTRY_PORT "$port"
  config::_assert_rejected "registry port ${port}" "$fixture"
done

fixture=$(config::_fixture kubectl-timeout)
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_KUBECTL_TIMEOUT 301s
config::_assert_rejected "an excessive kubectl timeout" "$fixture"

fixture=$(config::_fixture ready-timeout)
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_READY_TIMEOUT 1801s
config::_assert_rejected "an excessive readiness timeout" "$fixture"

fixture=$(config::_fixture path-traversal)
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_KIND_CONFIG clusters/kind/../../outside.yaml
config::_assert_rejected "Kind path traversal" "$fixture"

fixture=$(config::_fixture path-symlink)
external_kind="${test_workspace}/external-kind"
mkdir -p "$external_kind"
rm -rf "${fixture}/clusters/kind"
ln -s "$external_kind" "${fixture}/clusters/kind"
: > "${external_kind}/local-orbstack.yaml"
config::_assert_rejected "a Kind path resolving outside the repository" "$fixture"

fixture=$(config::_fixture profile-symlink)
mv "${fixture}/env/local-orbstack.env" "${fixture}/profile.env"
ln -s "${fixture}/profile.env" "${fixture}/env/local-orbstack.env"
config::_assert_rejected "a symlinked profile" "$fixture"

fixture=$(config::_fixture repo-mismatch)
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_GIT_REPO_URL https://github.com/example/atlas.git
config::_assert_rejected "a Git URL inconsistent with manifests" "$fixture"

fixture=$(config::_fixture revision-mismatch)
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_GIT_REVISION release
config::_assert_rejected "a Git revision inconsistent with manifests" "$fixture"

fixture=$(config::_fixture non-evaluating)
sentinel="${test_workspace}/must-not-execute"
unsafe_value="\$(touch\${IFS}${sentinel})"
config::_replace "${fixture}/env/local-orbstack.env" ATLAS_ROOT_NAME "$unsafe_value"
config::_assert_rejected "an executable-looking value" "$fixture"
[[ ! -e $sentinel ]] || test::fail "configuration input was executed"
test::pass "invalid, unsafe, and inconsistent configuration fails closed"

cli_fixture="${test_workspace}/cli"
mkdir -p "$cli_fixture"
cp -R bootstrap "$cli_fixture/"
"${cli_fixture}/bootstrap/atlas" --help > /dev/null
"${cli_fixture}/bootstrap/atlas" --version | grep -Eq '^atlas-bootstrap [0-9]+\.[0-9]+\.[0-9]+$'
if "${cli_fixture}/bootstrap/atlas" invalid-command > /dev/null 2>&1; then
  test::fail "an invalid command was accepted without configuration"
fi
if "${cli_fixture}/bootstrap/atlas" doctor --invalid > /dev/null 2>&1; then
  test::fail "an invalid option was accepted without configuration"
fi
test::pass "global help, version, and argument errors do not load a profile"
