#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)
readonly ROOT_DIR

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

pass() {
  printf 'PASS: %s\n' "$*"
}

assert_not_found() {
  local pattern=$1
  shift
  if rg --line-number "$pattern" "$@"; then
    fail "forbidden pattern found: ${pattern}"
  fi
}

cd "$ROOT_DIR"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-bootstrap-test.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

mapfile -t shell_files < <(find bootstrap -type f \( -name '*.sh' -o -name atlas \) | sort)
((${#shell_files[@]} == 7)) || fail "expected seven production Shell files, found ${#shell_files[@]}"

bash -n "${shell_files[@]}"
shellcheck -x "${shell_files[@]}"
shfmt -d -i 2 -ci -sr "${shell_files[@]}"
pass "Shell syntax, ShellCheck, and shfmt"

assert_not_found 'helm[[:space:]]+(install|upgrade)' bootstrap
assert_not_found 'kubectl[^\n]*(apply|create)[^\n]*https?://' bootstrap
assert_not_found '^kind:[[:space:]]+ApplicationSet$' bootstrap gitops/root gitops/platform/applications
assert_not_found 'resources-finalizer\.argocd\.argoproj\.io' bootstrap/argocd gitops/root
assert_not_found '(redpanda|minio|flink|loki|tempo|prometheus|envoy)' gitops/root
assert_not_found "(group|kind): '\*'" gitops/platform/management/projects/platform-project.yaml
pass "architecture anti-patterns are absent"

root_output=$(kubectl kustomize gitops/root/overlays/local-orbstack)
[[ $(grep -c '^kind: Application$' <<< "$root_output") == 3 ]] || fail "Root must contain exactly three macro Applications"
for application in project-bootstrap platform-control workload-control; do
  grep -Fq "name: ${application}" <<< "$root_output" || fail "Root is missing ${application}"
done
grep -Fq 'project: atlas-bootstrap' bootstrap/argocd/root-app.yaml.tpl || fail "External Root must use atlas-bootstrap"
grep -Fq 'name: atlas-bootstrap' bootstrap/argocd/atlas-bootstrap-project.yaml || fail "atlas-bootstrap AppProject is missing"
pass "External Root and two-level macro DAG contracts"

kubectl kustomize gitops/platform/management/projects > /dev/null
platform_output=$(kubectl kustomize gitops/platform/applications/overlays/local-orbstack)
kubectl kustomize gitops/workloads/applications/overlays/local-orbstack > /dev/null
kubectl kustomize gitops/platform/management/argocd-self/base > /dev/null
pass "Kustomize entrypoints"

[[ $(grep -c '^kind: Application$' <<< "$platform_output") == 1 ]] || fail "Platform DAG must contain the argocd-self adoption leaf"
grep -Fq 'name: argocd-self' <<< "$platform_output" || fail "Platform DAG is missing argocd-self"
grep -Fq 'argocd.argoproj.io/sync-wave: "-90"' <<< "$platform_output" || fail "argocd-self must use management wave -90"
grep -Fq 'project: platform-project' <<< "$platform_output" || fail "argocd-self must use platform-project"
grep -Fq 'path: vendor/charts/argo-cd-10.3.3' <<< "$platform_output" || fail "argocd-self does not use the vendored chart"
grep -Fq 'releaseName: atlas-argocd' <<< "$platform_output" || fail "argocd-self does not preserve Seed object identity"
grep -Fq 'path: gitops/platform/management/argocd-self/base' <<< "$platform_output" || fail "argocd-self does not own the shared health capability"
pass "argocd-self adoption leaf"

./bootstrap/atlas render > /dev/null
seed_sha=$(shasum -a 256 .state/rendered/argocd-seed.yaml | awk '{print $1}')
root_sha=$(shasum -a 256 .state/rendered/root-app.yaml | awk '{print $1}')
./bootstrap/atlas render > /dev/null
[[ $(shasum -a 256 .state/rendered/argocd-seed.yaml | awk '{print $1}') == "$seed_sha" ]] || fail "Argo CD rendering is not deterministic"
[[ $(shasum -a 256 .state/rendered/root-app.yaml | awk '{print $1}') == "$root_sha" ]] || fail "Root rendering is not deterministic"
grep -Fq "$(awk -F= '$1 == "ARGOCD_IMAGE" {print $2}' versions.lock)" .state/rendered/argocd-seed.yaml || fail "locked Argo CD image is not rendered"
grep -Fq "$(awk -F= '$1 == "REDIS_IMAGE" {print $2}' versions.lock)" .state/rendered/argocd-seed.yaml || fail "locked Redis image is not rendered"
[[ $(yq eval-all 'select(.kind == "Deployment" and .metadata.name == "atlas-argocd-applicationset-controller") | .spec.replicas' .state/rendered/argocd-seed.yaml) == 0 ]] || fail "ApplicationSet controller must be inactive in the frozen baseline"
grep -Fq 'project: atlas-bootstrap' .state/rendered/root-app.yaml || fail "rendered Root uses the wrong AppProject"
assert_not_found '^kind:[[:space:]]+(Application|AppProject)$' .state/rendered/argocd-seed.yaml
rg -q 'External Root health and argocd-self adoption' bootstrap/argocd/reconcile.sh || fail "Bootstrap does not enforce argocd-self adoption"
pass "deterministic Argo CD and Root rendering"

chart_path=$(awk -F= '$1 == "ARGOCD_CHART_PATH" {print $2}' versions.lock)
chart_version=$(awk -F= '$1 == "ARGOCD_CHART_VERSION" {print $2}' versions.lock)
argocd_version=$(awk -F= '$1 == "ARGOCD_VERSION" {print $2}' versions.lock)
argocd_image=$(awk -F= '$1 == "ARGOCD_IMAGE" {print $2}' versions.lock)
redis_image=$(awk -F= '$1 == "REDIS_IMAGE" {print $2}' versions.lock)
[[ $(yq '.version' "${chart_path}/Chart.yaml") == "$chart_version" ]] || fail "vendored chart version differs from versions.lock"
[[ $(yq '.appVersion' "${chart_path}/Chart.yaml") == "v${argocd_version}" ]] || fail "vendored Argo CD version differs from versions.lock"
values_argocd_image="$(yq '.global.image.repository' gitops/platform/management/argocd-self/values.yaml):$(yq '.global.image.tag' gitops/platform/management/argocd-self/values.yaml)"
values_redis_image="$(yq '.redis.image.repository' gitops/platform/management/argocd-self/values.yaml):$(yq '.redis.image.tag' gitops/platform/management/argocd-self/values.yaml)"
[[ $values_argocd_image == "$argocd_image" ]] || fail "argocd-self Argo CD image differs from versions.lock"
[[ $values_redis_image == "$redis_image" ]] || fail "argocd-self Redis image differs from versions.lock"
helm template atlas-argocd "$chart_path" \
  --namespace argocd \
  --include-crds \
  --values gitops/platform/management/argocd-self/values.yaml > "${test_workspace}/argocd-self.yaml"
cmp -s .state/rendered/argocd-seed.yaml "${test_workspace}/argocd-self.yaml" || fail "Seed and argocd-self render different desired states"
pass "Bootstrap Adoption Contract parity"

if approval_output=$(./bootstrap/atlas apply 2>&1); then
  fail "apply succeeded without Tier-0 approval"
fi
grep -Fq 'Tier-0 approval is required' <<< "$approval_output" || fail "apply did not fail at the Tier-0 gate"
pass "Tier-0 approval precedes every mutation"

mkdir -m 0700 .state/bootstrap.lock
printf '%s\n' "$$" > .state/bootstrap.lock/pid
if lock_output=$(./bootstrap/atlas render 2>&1); then
  fail "render ignored a live lifecycle lock"
fi
grep -Fq 'another bootstrap process is running' <<< "$lock_output" || fail "concurrent execution did not fail closed"
rm -f .state/bootstrap.lock/pid
rmdir .state/bootstrap.lock
pass "concurrent Bootstrap execution fails closed"

test_root="${test_workspace}/config"
mkdir -p "${test_root}/env"
cp versions.lock "${test_root}/versions.lock"
sed 's#^ATLAS_ROOT_NAME=.*#ATLAS_ROOT_NAME=$(touch /tmp/atlas-config-must-not-execute)#' \
  env/local-orbstack.env > "${test_root}/env/local-orbstack.env"

# shellcheck source=bootstrap/lib/config.sh
source bootstrap/lib/config.sh
if config::load "$test_root" local-orbstack 2> /dev/null; then
  fail "unsafe configuration value was accepted"
fi
[[ ! -e /tmp/atlas-config-must-not-execute ]] || fail "configuration parser executed input"
pass "configuration parser is allowlisted and non-evaluating"
