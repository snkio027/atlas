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

ADR-0005 makes Personal Local Profile selection explicitly versioned.
`personal-local-profile-v1.json` preserves the historical canonical v1
decision with SHA-256
`34e42bc31933ecf63fa5d878b611c3119415c3503481c7863e5e1cb5a4eff949`.
It is repository-only, is never live-eligible, and cannot produce a result
beyond `PERSONAL_LOCAL_DEFINED`. Both `personal-local-preflight run` and
`personal-local-preflight validate` therefore return `PERSONAL_LOCAL_BLOCKED`
before they parse arguments or read a Target, kubeconfig, credential, tool, or
API. Historical v1 static validation remains in the repository contract test.
The explicitly named v1 Target, Gate, and Evidence schemas are historical
artifacts only.

`personal-local-profile-v2.json` is the only future live-eligible Personal
Local authority. Its canonical document is a new waiver decision and cannot
fall back to or authorize alongside v1. It defines two distinct gates,
operations, and execution modes:

```text
v2 waiverDecisionSHA256
  c70520051531935249298bb5b0fe714a987b1ae1de3ceaee8e939c4d7153be6a
```

```text
PERSONAL_LOCAL_TARGET_MATERIALIZATION
  + LIVE_TARGET_MATERIALIZATION
  -> TARGET_MATERIALIZED

PERSONAL_LOCAL_READ_ONLY_PREFLIGHT
  + LIVE_READ_ONLY_PREFLIGHT
  -> PERSONAL_LOCAL_READY
```

The repository currently defines only this static authority surface. It does
not contain a Materialization executor or a v2 live preflight implementation.
No checked-in fixture, schema, or test is an approved Gate or live Evidence.

The v2 Profile keeps the Personal Local assurance classification fixed:

```text
Argo API authorization  RUNTIME_UNPROVEN
Production recovery     NOT_AUTHORIZED
```

### Target Materialization contract

`personal-local-target-materialization-plan.json` locks the canonical sixteen
ordered operations and the only two Kubernetes requests: `GET /version` and
`GET /api/v1/namespaces/kube-system`. It permits no collection or Secret read,
Argo call, Kubernetes/GitOps/runtime mutation, or unexpected request.

```text
materializationPlanSHA256
  b9f2f0f2a171e410d20d15c5582408185fa8bc7b440442c965230301ccd379dc
```

The Materialization Owner Gate schema binds the v2 Profile and waiver, exact
Git and tool authority, plan and Evidence schema, selected context and paths,
and one create-once session. It deliberately excludes the kubeconfig and
kubectl hashes, API endpoint, CA SPKI, and Namespace UID that Materialization
exists to discover. Its expected canonical SHA must eventually be supplied
independently.

The only eligible credential shape is a static kubeconfig user containing
exactly `client-certificate-data` and `client-key-data`. The schema surface
does not authorize projecting, extracting, persisting, or separately hashing
either value. Claim and terminal receipt schemas lock one `CLAIMED` session to
one terminal `MATERIALIZED` or `BLOCKED` result. Successful Evidence contains
no host path or credential material and proves both exact requests completed.

### Final v2 preflight contract

The v2 Target adds `profileID` and
`targetMaterializationEvidenceSHA256` without changing the historical v1
schema. The v2 final Owner Gate binds that Evidence SHA, the complete Target
pre-Gate projection, desired projection, exact thirteen-object read plan, two
snapshots, local authority hashes, and time window. Its expected SHA remains an
independent input.

The final preflight validates Materialization Gate, claim, terminal, and
Evidence provenance. A successful projection requires exactly one
`/version` read plus 26 exact-object reads, zero skipped/collection/Secret/Argo
or Mutation calls, and thirteen per-object hashes satisfying
`desired == live-before == live-after`. The `kube-system` UID must be
revalidated through the existing two Namespace snapshot reads, never a new
request. The final Gate may be issued only zero to 900 seconds after successful
Materialization Evidence completion.

All files in this directory remain unreachable from every live Kustomization
and Application. Runtime Materialization, kubeconfig or credential use,
Kubernetes or Argo access, live preflight, Admission observation, Phase 1C,
Signal, Receipt, and every runtime mutation require later explicit authority.
