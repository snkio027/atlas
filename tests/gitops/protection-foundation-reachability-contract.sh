#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

# shellcheck source=tests/lib/assert.sh
source "$(dirname "${BASH_SOURCE[0]}")/../lib/assert.sh"
# shellcheck source=tests/gitops/lib/reachability-graph.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/reachability-graph.sh"
cd "$ATLAS_TEST_ROOT"

readonly definition_path=gitops/platform/management/protection-foundation/definitions
readonly hardening_path=$definition_path/argo-hardening

expected_reachable_definitions=$(printf '%s\n' \
  "$hardening_path/application-overlay" \
  "$hardening_path/application-overlay/kustomization.yaml" \
  "$hardening_path/argocd-self-base-overlay" \
  "$hardening_path/argocd-self-base-overlay/kustomization.yaml" \
  "$hardening_path/argocd-values-hardening.yaml" | sort)

for environment in local-orbstack prod; do
  graph=$(gitops_reachability::collect_paths "gitops/root/overlays/${environment}") ||
    test::fail "${environment} Root control graph could not be traversed"
  reachable_definitions=$(
    while IFS= read -r reachable_path; do
      reachable_path=${reachable_path#"$ATLAS_TEST_ROOT/"}
      [[ $reachable_path == "$definition_path"/* ]] || continue
      printf '%s\n' "$reachable_path"
    done <<< "$graph" | sort -u
  )
  [[ $reachable_definitions == "$expected_reachable_definitions" ]] || {
    printf 'expected reachable Protection paths:\n%s\n' "$expected_reachable_definitions" >&2
    printf 'actual reachable Protection paths:\n%s\n' "$reachable_definitions" >&2
    test::fail "${environment} Root reaches an unapproved Protection projection"
  }

  for forbidden_projection in \
    "$definition_path/admission" \
    "$definition_path/rbac/escape" \
    "$definition_path/rbac/session" \
    "$definition_path/signal" \
    "$hardening_path/probe-contract" \
    "$definition_path/applicationset-recovery-contract.json"; do
    gitops_reachability::assert_definition_unreachable \
      "gitops/root/overlays/${environment}" "$forbidden_projection" ||
      test::fail "${environment} Root reaches inactive projection: ${forbidden_projection}"
  done

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

[[ $(yq ea '[select(.kind == "ValidatingAdmissionPolicy" or
  .kind == "ValidatingAdmissionPolicyBinding")] | length' "$live_render") -eq 0 ]] ||
  test::fail "live GitOps render contains Admission protection"
[[ $(yq ea '[select(
  (.kind == "Role" or .kind == "RoleBinding" or
    .kind == "ClusterRole" or .kind == "ClusterRoleBinding") and
  (.metadata.name | test("^atlas-bootstrap-(break-glass|recovery)")))] | length' \
  "$live_render") -eq 0 ]] || test::fail "live GitOps render contains Recovery RBAC"
[[ $(yq ea '[select(.kind == "ConfigMap" and
  (.metadata.name == "atlas-bootstrap-adoption-signal" or
    .metadata.name == "atlas-bootstrap-adoption-receipt" or
    .metadata.name == "atlas-bootstrap-operation-fence"))] | length' \
  "$live_render") -eq 0 ]] || test::fail "live GitOps render contains Signal, Receipt, or Fence"
[[ $(yq ea '[select(.kind == "ApplicationSet")] | length' "$live_render") -eq 0 ]] ||
  test::fail "live GitOps render contains an ApplicationSet"

chart_path=$(awk -F= '$1 == "ARGOCD_CHART_PATH" {print $2}' versions.lock)
helm template atlas-argocd "$chart_path" \
  --namespace argocd \
  --include-crds \
  --values gitops/platform/management/argocd-self/values.yaml > "$seed_render"
! grep -Fq 'atlas-bootstrap-adoption-signal' "$seed_render" ||
  test::fail "Bootstrap Seed contains the GitOps Adoption Signal"

if gitops_reachability::assert_definition_unreachable \
  tests/gitops/fixtures/reachability-wired "$definition_path/admission" > /dev/null 2>&1; then
  test::fail "reachability detector accepted a graph wired to Phase 1A definitions"
fi

test::pass "only Phase 1B Change 1 Argo hardening is reachable across both control graphs"
