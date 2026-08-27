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
code zero for a successful evaluation. Any stderr, other output, transport
error, timeout, or non-zero exit is invalid rather than an authorization
decision.

Actions containing a fine-grained suffix require a separately proven v3.5.1
evaluator capability. If the exact action cannot be represented reliably, the
result is `UNSUPPORTED`; a real mutation must never be substituted.

The target and evidence fixtures under `tests/gitops/fixtures` are synthetic.
They contain no real endpoint, identity, credential, kubeconfig, certificate,
token, cookie, or host path. Live convergence, identity selection, credential
handling, and probe execution remain subject to a separate Human Judgment
Gate.
