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

## Personal Local rollout profile

`personal-local-profile.json` defines the separately selected ADR-0003
`PERSONAL_LOCAL` rollout profile. It is not a fallback from this Production
contract: the Production result remains `UNSUPPORTED`, and profile selection
must be explicit.

The Personal Local profile replaces Argo API authorization execution with a
Human-gated Kubernetes read-only preflight over the same thirteen exact desired
objects. It permits only exact-object `get` operations and `/version`; Secret,
collection, Argo API, `--core`, credential, and Mutation access remain
forbidden. A Ready preflight must prove the complete Git-derived desired
projection equals both live snapshots. Its assurance is still recorded as:

```text
Argo API authorization  RUNTIME_UNPROVEN
Production recovery     NOT_AUTHORIZED
```

`personal-local-target.schema.json` binds the exact Git commit, target
fingerprint, canonical profile-decision hash, action-specific Owner Gate hash,
locked kubectl, and thirteen desired object hashes.
`personal-local-owner-gate.schema.json` defines the independently supplied
Owner Gate document. Its canonical hash binds the operation, profile, commit,
pre-authorization Target projection, thirteen-object read plan, two snapshots,
and target fingerprint inputs. The Target cannot authorize itself: the
preflight caller must separately provide the expected canonical Gate SHA.

`personal-local-preflight` is the authoritative `run` and `validate` entrypoint.
`run` verifies the external Gate and exact Git hydration before using the
approved kubeconfig for `/version` and two complete snapshots of the thirteen
exact raw object paths. The selected context must use its embedded
`certificate-authority-data`, must not enable insecure TLS, and the decoded CA
SPKI must equal the Gate-approved target hash. External CA paths are rejected
to keep the authority check and subsequent reads within one hash-bound
kubeconfig boundary. The preflight recursively projects live values through
the desired field mask, verifies array shape, and writes
`PERSONAL_LOCAL_READY` only when all 26 reads and both aggregate hashes equal
the approved desired projection. `validate` independently repeats Target,
Gate, hydration, complete nested Evidence shape and semantic validation,
read-inventory, time ordering, and sensitive-content rejection. Both commands
return `24` and
`PERSONAL_LOCAL_BLOCKED` for uncertainty.

The checked-in fixtures have execution mode `REPOSITORY_ONLY_SYNTHETIC`, use a
canonical `NOT_AUTHORIZED` Gate document, and can produce only
`PERSONAL_LOCAL_DEFINED`. They are not live evidence. Required Quality runs the
entrypoint only against a local fake kubectl whose executable SHA is bound into
the Target and Gate; it does not access a Kubernetes API.

Only a later, separately authorized `LIVE_READ_ONLY_PREFLIGHT` with an approved
Owner Gate, independently supplied Gate SHA, and complete reads may produce
`PERSONAL_LOCAL_READY`. Any mismatch becomes `PERSONAL_LOCAL_BLOCKED`.
Admission observation, candidate wiring, post-squash revalidation, and the
30-to-60-minute observation window remain outside this repository-only
definition change.
