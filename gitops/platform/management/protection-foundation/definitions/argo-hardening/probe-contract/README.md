# Argo authorization probe contract

This directory defines the repository-only contract for the future,
target-bound ADR-0003 Phase 1B live authorization probe. It is not an
executable probe, an identity, a credential, or a GitOps activation entrypoint.
No Kustomization or Application references this directory.

Identity remains `IDENTITY_UNAVAILABLE`. The target schema describes what a
future, separately approved identity decision and Human Gate would have to
bind; it does not select or authorize either identity category.

The contract locks:

- the Argo CD v3.5.1 client to the digest-pinned `ARGOCD_IMAGE` in
  `versions.lock` instead of an ambient executable;
- the named target, TLS, Kubernetes read surface, and identity-reference
  inputs that a future Human Gate must approve;
- the complete positive and negative authorization matrix derived from the
  reviewed Argo Action Inventory;
- the only commands and transport operations that may be used by a future
  implementation;
- strict `READY`, `DENIED`, `DRIFTED`, `UNSUPPORTED`, and `INVALID`
  classifications; and
- a machine-readable, secret-free evidence projection.

The authorization query is `argocd account can-i`, which calls the Argo API
Server. Kubernetes-direct `--core` mode is forbidden because it bypasses Argo
authentication and RBAC. The locked v3.5.1 CLI prints `yes` or `no` with exit
code zero for a successful base-action evaluation. Any stderr, other output,
transport error, timeout, or non-zero exit is invalid rather than an
authorization decision.

Argo CD v3.5.1 rejects fine-grained `action/*`, `delete/*`, and `update/*`
strings before RBAC evaluation. Those three matrix entries are therefore
`UNSUPPORTED`, are never sent to the API, and keep the overall Gate at
`UNSUPPORTED`. A real mutation or wildcard literal must never be substituted.

A synthetic admin password cannot distinguish a disabled account from an
invalid password in v3.5.1. Until an approved Identity decision supplies an
already-managed valid admin credential, the admin check is
`ADMIN_PROOF_UNAVAILABLE` and is not executed.

The caller supplies the approved Git commit independently of the Target. The
runtime checkout must equal that commit, and desired hydration is performed
from an archive materialized from the same commit. The approved target document
binds that commit, the identity decision, credential reference, expected
subject/issuer/claims hashes, and thirteen Git-derived desired object
projections. Evidence must echo those values, use the exact approved TLS server
name, and prove each desired projection equals both live snapshots. Stability
without desired-state equality is drift, not convergence.

The target and failure-closed evidence fixtures under `tests/gitops/fixtures`
are synthetic. The evidence intentionally ends in `UNSUPPORTED`; this contract
cannot produce `READY` while the fine-grained and admin proof gaps remain.
They contain no real endpoint, identity, credential, kubeconfig, certificate,
token, cookie, or host path. Live convergence, identity selection, credential
handling, and probe execution remain subject to a separate Human Judgment
Gate.
