# ADR-0003 Phase 1A protection definitions

This directory is the definition-only production candidate for ADR-0003
Phase 1A. It is intentionally absent from every live Application and
Kustomization reachable from the External Root.

Each rollout boundary has an independent entrypoint. There is deliberately no
top-level Kustomization that can combine phases or create the Signal together
with an observing Admission projection.

| Entry point | State represented | Runtime authority |
| --- | --- | --- |
| `admission/base` | definition source | none |
| `admission/overlays/observing` | future `OBSERVING` projection (`Audit`) | not activated |
| `admission/overlays/enforced` | future `ENFORCED` projection (`Audit`, `Deny`) | not activated |
| `rbac/escape` | independently reviewed Admission escape authority | not activated |
| `rbac/session` | Session Authorizer and unbound Recovery roles | not activated |
| `signal` | protected GitOps Signal definition | not created |
| `argo-hardening` | existing-owner Argo hardening increment | not wired |

A future Phase 1B or 1C change must independently wire and human-gate exactly
one reviewed projection. Presence in this directory never means that a live
object exists. Tests may aggregate the independent entrypoints only under
`tests/gitops/fixtures`; that inventory is not an activation path.

All principal-bearing definitions use the canonical all-zero Namespace UID and
generation `g1`. These values are non-production compile fixtures, not
credentials or activation defaults. Future activation must derive and validate
the live `kube-system` UID and independently reviewed principal generations,
then replace every annotated activation input as one coherent projection.

The evidence ValidatingAdmissionPolicy deliberately does not match
ValidatingAdmissionPolicy or ValidatingAdmissionPolicyBinding resources because
Kubernetes forbids that self-protection model. Policy and Binding protection
instead depends on exact least-privilege RBAC, Git prune/delete protection,
projection hashes, audit, and a Human Judgment Gate as required by ADR-0003.

The Argo candidate is expressed through the existing owners: a Helm values
increment for Chart-owned ConfigMaps, a Kustomize overlay for the existing
`argocd-cm`, and an overlay of the existing `argocd-self` Application. It does
not declare duplicate ConfigMaps or a second Application owner. The Argo and
ApplicationSet assets do not change the current desired state, start the
ApplicationSet Controller, create an ApplicationSet, or activate the recovery
guard.
