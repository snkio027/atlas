# ADR-0002: Establish a monotonic Bootstrap adoption proof

- Status: Proposed
- Date: 2026-08-15
- Deciders: repository owner and required CODEOWNERS
- Supersedes: none
- Superseded by: none

## Context

Atlas Architecture INV-02 requires Bootstrap to cease active control after
GitOps adoption. The current Shell implementation decides Seed authority from
the live `argocd-self` Application: absence permits Seed installation, presence
denies it. That decision fails closed while the Application exists, but it is
not monotonic. Deleting `argocd-self` after a successful handoff makes a normal
`apply` infer that initialization never happened and permits Seed authority
again.

Application presence is current state, not durable evidence of a completed
trust transition. A local `.state/` file is also insufficient because it is
host-local, routinely disposable, and unavailable during recovery from another
machine. Recovery Seed remains a break-glass operation under GitOps Standard
sections 18.2 and 21.4; ordinary `apply` must not infer recovery authority from
missing control-plane objects.

This decision affects Recovery Authority and therefore requires an accepted
ADR before implementation. While this ADR remains Proposed, it does not
authorize changes to Bootstrap control semantics or GitOps manifests.

## Decision

Atlas will use three related records to make adoption monotonic within the
normal control path:

1. The existing Bootstrap cluster identity in `kube-system` will carry a
   lifecycle schema. A cluster created by the proof-aware Bootstrap is
   explicitly Seed-eligible; the absence of that schema on a legacy cluster is
   never interpreted as a fresh cluster.
2. `argocd-self` will create a GitOps-only adoption signal. The Seed path must
   not render or apply this object. Its presence proves that GitOps has begun
   reconciling the self-managed control-plane definition.
3. After observing the valid GitOps signal and both External Root and
   `argocd-self` as Synced/Healthy, Bootstrap will create an immutable Adoption
   Receipt in `kube-system`. The receipt will bind to the cluster identity,
   repository, and observed Application identities. Normal Bootstrap will
   neither update nor delete it.

The effective state machine is:

```text
NEVER_INITIALIZED
        |
        v
   SEED_ACTIVE
        |
        v
ADOPTION_OBSERVED
        |
        v
     ADOPTED

ADOPTED -/-> SEED_ACTIVE
```

Seed authority is permitted only when all of the following are true:

- the cluster identity is owned by the selected Atlas configuration;
- the lifecycle schema explicitly identifies a proof-aware fresh cluster;
- no valid GitOps adoption signal exists;
- no valid Adoption Receipt exists.

The GitOps signal moves the normal path to `ADOPTION_OBSERVED` and immediately
terminates Seed authority, even if `argocd-self` has not yet become Healthy. A
valid Adoption Receipt moves it to `ADOPTED`. Once the receipt exists, missing
or unhealthy Argo CD Applications and workloads cannot restore Seed authority.
Normal `apply` may continue non-Seed convergence that remains inside its
existing authority, but it must report the damaged handoff and fail if GitOps
cannot recover.

Any missing, malformed, foreign, or contradictory identity/proof combination
is `AMBIGUOUS`, not `NEVER_INITIALIZED`, and fails closed. Restoring Seed after
`ADOPTION_OBSERVED` or `ADOPTED` requires a separately designed, explicitly
approved break-glass command and runbook. This ADR does not authorize that
recovery operation.

### Rollout

Implementation must use two independently verifiable phases:

1. Add the GitOps-only signal and verify that every supported existing cluster
   has reconciled it. Bootstrap behavior remains unchanged during this phase.
2. Add lifecycle-schema initialization, immutable receipt creation, and
   proof-aware Seed gating. A legacy cluster without a valid signal or receipt
   fails closed rather than being migrated by inference.

No receipt-aware Bootstrap may be released until phase-one evidence exists for
the supported existing environments. Newly created clusters receive the
lifecycle schema before any Seed mutation.

## Consequences

- Deleting `argocd-self` after adoption no longer re-enables ordinary Seed
  installation.
- Fresh-cluster initialization and damaged adopted-cluster recovery become
  distinguishable without relying on host-local state.
- Bootstrap performs one final, bounded receipt write as part of handoff; this
  does not grant steady-state reconciliation authority.
- The signal is Tier-1 GitOps state. The receipt and lifecycle schema are
  Bootstrap-owned substrate records. Neither changes the Tier-0 Root graph.
- RBAC, audit, and future admission policy must protect lifecycle records from
  unauthorized deletion. Absolute protection from cluster-admin deletion is
  outside the normal-path invariant.
- Rollback after a receipt has been issued cannot restore the previous
  Application-existence heuristic. A defect must be corrected forward or
  handled through an approved break-glass process.

## Alternatives considered

### Continue using live `argocd-self` presence

Rejected because deletion makes authority regress from adopted to Seed-active.

### Store proof only under repository `.state/`

Rejected because it is host-local, disposable, and unsuitable for recovery
from another operator workstation.

### Use only a GitOps-managed marker

Rejected as the sole proof because namespace loss or accidental deletion would
again make prior adoption ambiguous. The persistent substrate receipt preserves
the observation after control-plane damage.

### Use a mutable lifecycle phase only

Rejected as the final proof because an in-place phase field is easier to regress
accidentally. The final receipt is create-once and immutable; state before that
point remains explicitly non-adopted or ambiguous.

### Introduce an external state database

Rejected for the current phase because it adds a new Bootstrap dependency and
recovery failure domain before the Kubernetes substrate exists.

## Verification

Implementation is conformant only when it proves all of the following:

- a fresh proof-aware cluster can install Seed and complete handoff;
- the Seed render never contains the GitOps adoption signal;
- `argocd-self` creates the signal through its Git-managed desired state;
- a valid signal terminates Seed authority before Application health;
- a valid receipt permanently denies Seed in the normal path, including after
  deleting `argocd-self`, the External Root, or Argo CD workloads;
- missing or malformed proof on a legacy cluster fails closed;
- interruption before receipt creation resumes without a backward transition;
- interruption after receipt creation never reapplies Seed;
- two normal applies after adoption preserve the receipt and control-plane
  resource identities;
- no normal command can delete or rewrite the receipt;
- `task quality` and an explicitly approved `task integration` pass on the
  target macOS and OrbStack environment.

The implementation change must update the Bootstrap runbook with proof
inspection and failure semantics. Break-glass Seed restoration requires its own
accepted design, tests, and runbook.
