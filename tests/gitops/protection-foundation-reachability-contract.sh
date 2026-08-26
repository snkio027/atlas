#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
# shellcheck source=tests/gitops/lib/reachability-graph.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/reachability-graph.sh"
cd "$ATLAS_TEST_ROOT"

readonly definition_path=gitops/platform/management/protection-foundation/definitions

for environment in local-orbstack prod; do
  gitops_reachability::assert_definition_unreachable \
    "gitops/root/overlays/${environment}" "$definition_path" ||
    test::fail "${environment} Root reaches Phase 1A definitions"

  root_render=$(kubectl kustomize "gitops/root/overlays/${environment}")
  [[ $(yq ea '[select(.kind == "Application")] | length' <<< "$root_render") -eq 3 ]] ||
    test::fail "${environment} Root no longer has exactly three macro Applications"
done

platform_render=$(kubectl kustomize gitops/platform/applications/base)
[[ $(yq ea '[select(.kind == "Application")] | length' <<< "$platform_render") -eq 1 &&
$(yq ea -r 'select(.kind == "Application") | .metadata.name' <<< "$platform_render") == argocd-self ]] ||
  test::fail "live Platform DAG no longer contains only argocd-self"

live_render=$(mktemp "${TMPDIR:-/tmp}/atlas-live-graph.XXXXXX")
seed_render=$(mktemp "${TMPDIR:-/tmp}/atlas-seed-render.XXXXXX")
cleanup() {
  rm -f "$live_render" "$seed_render"
}
trap cleanup EXIT

{
  for environment in local-orbstack prod; do
    kubectl kustomize "gitops/root/overlays/${environment}"
    kubectl kustomize "gitops/platform/applications/overlays/${environment}"
    kubectl kustomize "gitops/workloads/applications/overlays/${environment}"
  done
  kubectl kustomize gitops/platform/management/projects
  kubectl kustomize gitops/platform/management/argocd-self/base
} >> "$live_render"

for forbidden in \
  atlas-bootstrap-evidence-protection \
  atlas-bootstrap-recovery-fence-authorization \
  atlas-bootstrap-recovery-binding-shape-authorization \
  atlas-bootstrap-recovery-permission-authorization \
  atlas-bootstrap-recovery-guard-authorization \
  atlas-bootstrap-adoption-signal \
  atlas-bootstrap-operation-fence \
  atlas-bootstrap-recovery-authorizer-cluster; do
  ! grep -Fq "$forbidden" "$live_render" ||
    test::fail "live GitOps render contains Phase 1A definition: ${forbidden}"
done
! grep -Fq 'kind: ApplicationSet' "$live_render" ||
  test::fail "live GitOps render contains an ApplicationSet"

chart_path=$(awk -F= '$1 == "ARGOCD_CHART_PATH" {print $2}' versions.lock)
helm template atlas-argocd "$chart_path" \
  --namespace argocd \
  --include-crds \
  --values gitops/platform/management/argocd-self/values.yaml > "$seed_render"
! grep -Fq 'atlas-bootstrap-adoption-signal' "$seed_render" ||
  test::fail "Bootstrap Seed contains the GitOps Adoption Signal"

if gitops_reachability::assert_definition_unreachable \
  tests/gitops/fixtures/reachability-wired "$definition_path" > /dev/null 2>&1; then
  test::fail "reachability detector accepted a graph wired to Phase 1A definitions"
fi

test::pass "Phase 1A definitions are unreachable across both live control graphs"
