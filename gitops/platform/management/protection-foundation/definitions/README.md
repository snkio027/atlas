# ADR-0003 Phase 1A protection definitions

This directory is the definition-only production candidate for ADR-0003
Phase 1A. It is intentionally absent from every live Application and
Kustomization reachable from the External Root.

The admission entrypoints encode distinct rollout states:

| Entry point | State represented | Runtime authority |
| --- | --- | --- |
| `admission/base` | definition source | none |
| `admission/overlays/observing` | future `OBSERVING` projection (`Audit`) | not activated |
| `admission/overlays/enforced` | future `ENFORCED` projection (`Audit`, `Deny`) | not activated |

The top-level `kustomization.yaml` renders the observing candidate only. A
future Phase 1B or 1C change must independently wire and human-gate the selected
projection. Presence in this directory never means that a live object exists.

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

The Argo and ApplicationSet assets are candidate contracts only. They do not
change the current `argocd-self` desired state, start the ApplicationSet
Controller, create an ApplicationSet, or activate the recovery guard.
