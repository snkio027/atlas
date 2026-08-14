#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test::assert_not_found 'helm[[:space:]]+(install|upgrade)' bootstrap
test::assert_not_found 'kind[[:space:]]+load[[:space:]]+docker-image' bootstrap
test::assert_not_found 'kubectl[^\n]*(apply|create)[^\n]*https?://' bootstrap
test::assert_not_found '^kind:[[:space:]]+ApplicationSet$' bootstrap gitops/root gitops/platform/applications
test::assert_not_found 'resources-finalizer\.argocd\.argoproj\.io' bootstrap/argocd gitops/root
test::assert_not_found '(redpanda|minio|flink|loki|tempo|prometheus|envoy)' gitops/root
test::assert_not_found "(group|kind): '\*'" gitops/platform/management/projects/platform-project.yaml
test::pass "architecture anti-patterns are absent"

for environment in local-orbstack prod; do
  root_output=$(kubectl kustomize "gitops/root/overlays/${environment}")
  [[ $(grep -c '^kind: Application$' <<< "$root_output") == 3 ]] || test::fail "${environment} Root must contain exactly three macro Applications"
  for application in project-bootstrap platform-control workload-control; do
    grep -Fq "name: ${application}" <<< "$root_output" || test::fail "${environment} Root is missing ${application}"
  done
done

grep -Fq 'project: atlas-bootstrap' bootstrap/argocd/root-app.yaml.tpl || test::fail "External Root must use atlas-bootstrap"
grep -Fq 'name: atlas-bootstrap' bootstrap/argocd/atlas-bootstrap-project.yaml || test::fail "atlas-bootstrap AppProject is missing"
test::pass "External Root and two-level macro DAG contracts"

kubectl kustomize gitops/platform/management/projects > /dev/null
kubectl kustomize gitops/platform/management/argocd-self/base > /dev/null
for environment in local-orbstack prod; do
  kubectl kustomize "gitops/platform/applications/overlays/${environment}" > /dev/null
  kubectl kustomize "gitops/workloads/applications/overlays/${environment}" > /dev/null
done
test::pass "all Kustomize entrypoints"

platform_output=$(kubectl kustomize gitops/platform/applications/overlays/local-orbstack)
[[ $(grep -c '^kind: Application$' <<< "$platform_output") == 1 ]] || test::fail "Platform DAG must contain the argocd-self adoption leaf"
grep -Fq 'name: argocd-self' <<< "$platform_output" || test::fail "Platform DAG is missing argocd-self"
grep -Fq 'argocd.argoproj.io/sync-wave: "-90"' <<< "$platform_output" || test::fail "argocd-self must use management wave -90"
grep -Fq 'project: platform-project' <<< "$platform_output" || test::fail "argocd-self must use platform-project"
grep -Fq 'path: vendor/charts/argo-cd-10.3.3' <<< "$platform_output" || test::fail "argocd-self does not use the vendored chart"
grep -Fq 'releaseName: atlas-argocd' <<< "$platform_output" || test::fail "argocd-self does not preserve Seed object identity"
grep -Fq 'path: gitops/platform/management/argocd-self/base' <<< "$platform_output" || test::fail "argocd-self does not own the shared health capability"
test::pass "argocd-self adoption leaf"

for project in atlas-bootstrap platform-project workload-project; do
  rg -q "name: ${project}" bootstrap/argocd gitops/platform/management/projects || test::fail "canonical AppProject is missing: ${project}"
done
grep -Fq '/gitops/root/** @snkio027' .github/CODEOWNERS || test::fail "Tier-0 Root has no explicit CODEOWNER"
test::pass "canonical projects and Tier-0 ownership"

chart_file=$(awk -F= '$1 == "ARGOCD_CHART_FILE" {print $2}' versions.lock)
chart_path=$(awk -F= '$1 == "ARGOCD_CHART_PATH" {print $2}' versions.lock)
chart_sha=$(awk -F= '$1 == "ARGOCD_CHART_SHA256" {print $2}' versions.lock)
chart_version=$(awk -F= '$1 == "ARGOCD_CHART_VERSION" {print $2}' versions.lock)
argocd_version=$(awk -F= '$1 == "ARGOCD_VERSION" {print $2}' versions.lock)

[[ $(shasum -a 256 "$chart_file" | awk '{print $1}') == "$chart_sha" ]] || test::fail "vendored Chart checksum differs from versions.lock"
[[ $(yq '.version' "${chart_path}/Chart.yaml") == "$chart_version" ]] || test::fail "vendored Chart version differs from versions.lock"
[[ $(yq '.appVersion' "${chart_path}/Chart.yaml") == "v${argocd_version}" ]] || test::fail "vendored Argo CD version differs from versions.lock"

while IFS='=' read -r key value; do
  [[ $key == *_IMAGE ]] || continue
  [[ $value =~ @sha256:[0-9a-f]{64}$ ]] || test::fail "image is not digest-pinned: ${key}"
  [[ $value != *:latest* ]] || test::fail "floating latest image is forbidden: ${key}"
done < versions.lock

argocd_image=$(awk -F= '$1 == "ARGOCD_IMAGE" {print $2}' versions.lock)
redis_image=$(awk -F= '$1 == "REDIS_IMAGE" {print $2}' versions.lock)
values_argocd_image="$(yq '.global.image.repository' gitops/platform/management/argocd-self/values.yaml):$(yq '.global.image.tag' gitops/platform/management/argocd-self/values.yaml)"
values_redis_image="$(yq '.redis.image.repository' gitops/platform/management/argocd-self/values.yaml):$(yq '.redis.image.tag' gitops/platform/management/argocd-self/values.yaml)"
[[ $values_argocd_image == "$argocd_image" ]] || test::fail "argocd-self Argo CD image differs from versions.lock"
[[ $values_redis_image == "$redis_image" ]] || test::fail "argocd-self Redis image differs from versions.lock"

test::assert_not_found '^kind:[[:space:]]+Secret$' bootstrap env gitops
test::assert_not_found 'image:[[:space:]]*[^#[:space:]]*:latest([@[:space:]]|$)' bootstrap env gitops
if find bootstrap clusters env gitops -type l -print -quit | grep -q .; then
  test::fail "first-party Bootstrap inputs must not be symlinks"
fi

while IFS= read -r action; do
  [[ $action =~ uses:[[:space:]]+[^@[:space:]]+@[0-9a-f]{40}([[:space:]]|$) ]] || test::fail "GitHub Action is not pinned to an immutable SHA: ${action}"
done < <(rg '^[[:space:]]*-[[:space:]]+uses:' .github/workflows)
test::pass "checksums, image digests, secret boundary, input safety, and immutable CI Actions"
