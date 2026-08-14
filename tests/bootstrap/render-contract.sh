#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
cd "$ATLAS_TEST_ROOT"

test_workspace=$(mktemp -d "${TMPDIR:-/tmp}/atlas-render-test.XXXXXX")
trap 'rm -rf "$test_workspace"' EXIT

./bootstrap/atlas render > /dev/null
seed_sha=$(shasum -a 256 .state/rendered/argocd-seed.yaml | awk '{print $1}')
root_sha=$(shasum -a 256 .state/rendered/root-app.yaml | awk '{print $1}')
./bootstrap/atlas render > /dev/null
[[ $(shasum -a 256 .state/rendered/argocd-seed.yaml | awk '{print $1}') == "$seed_sha" ]] || test::fail "Argo CD rendering is not deterministic"
[[ $(shasum -a 256 .state/rendered/root-app.yaml | awk '{print $1}') == "$root_sha" ]] || test::fail "Root rendering is not deterministic"

argocd_image=$(awk -F= '$1 == "ARGOCD_IMAGE" {print $2}' versions.lock)
redis_image=$(awk -F= '$1 == "REDIS_IMAGE" {print $2}' versions.lock)
grep -Fq "$argocd_image" .state/rendered/argocd-seed.yaml || test::fail "locked Argo CD image is not rendered"
grep -Fq "$redis_image" .state/rendered/argocd-seed.yaml || test::fail "locked Redis image is not rendered"
[[ $(yq eval-all 'select(.kind == "Deployment" and .metadata.name == "atlas-argocd-applicationset-controller") | .spec.replicas' .state/rendered/argocd-seed.yaml) == 0 ]] || test::fail "ApplicationSet controller must be inactive"
grep -Fq 'project: atlas-bootstrap' .state/rendered/root-app.yaml || test::fail "rendered Root uses the wrong AppProject"
test::assert_not_found '^kind:[[:space:]]+(Application|AppProject)$' .state/rendered/argocd-seed.yaml
test::pass "deterministic Argo CD and Root rendering"

chart_file=$(awk -F= '$1 == "ARGOCD_CHART_FILE" {print $2}' versions.lock)
chart_path=$(awk -F= '$1 == "ARGOCD_CHART_PATH" {print $2}' versions.lock)
values=gitops/platform/management/argocd-self/values.yaml

helm lint "$chart_path" --values "$values" > /dev/null
helm template atlas-argocd "$chart_path" \
  --namespace argocd \
  --include-crds \
  --values "$values" > "${test_workspace}/argocd-self-tree.yaml"
helm template atlas-argocd "$chart_file" \
  --namespace argocd \
  --include-crds \
  --values "$values" > "${test_workspace}/argocd-self-archive.yaml"

cmp -s .state/rendered/argocd-seed.yaml "${test_workspace}/argocd-self-tree.yaml" || test::fail "Seed and argocd-self render different desired states"
cmp -s "${test_workspace}/argocd-self-tree.yaml" "${test_workspace}/argocd-self-archive.yaml" || test::fail "vendored Chart archive and tree render differently"
test::pass "Helm lint and Bootstrap Adoption Contract parity"
