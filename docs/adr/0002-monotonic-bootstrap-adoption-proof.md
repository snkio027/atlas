# ADR-0002: Establish a monotonic Bootstrap adoption proof

- Status: Accepted
- Date: 2026-08-15
- Deciders: repository owner and required CODEOWNERS
- Clarified by: [ADR-0003](0003-bootstrap-break-glass-recovery.md)
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

Two additional failure modes constrain the design:

1. A GitOps signal that disappears before a durable receipt is committed must
   not allow the state machine to move backward to Seed authority.
2. A receipt-aware cluster must reject a receipt-unaware Bootstrap binary. An
   operator checking out an older Atlas revision must not be able to bypass the
   receipt by using the previous Application-presence heuristic.

This decision affects Recovery Authority and therefore required an accepted
ADR before implementation. Acceptance does not itself authorize changes to
Bootstrap control semantics, AppProject permissions, admission policy, or
GitOps manifests; each implementation and activation step remains separately
reviewed and gated.

## Implementation status

This decision is accepted. Its ADR-0003 Phase-0 recovery prerequisite is
implemented, runtime-verified, and `COMPLETE / FROZEN` as recorded in the
[Phase-0 Recovery Authority runbook](../runbooks/recovery-phase0.md).

The Identity v2 migration, production protection activation, Signal, Receipt,
and receipt-aware normal Bootstrap path remain unimplemented and inactive.
Normal Bootstrap therefore still uses the live `argocd-self` heuristic
described above, and the INV-02 runtime gap remains open until the separately
reviewed Phase 1 through Phase 4 rollout is complete.

## Decision

Atlas will use three protected, independently verifiable Kubernetes records:

1. a Bootstrap-owned cluster identity v2;
2. a GitOps-only adoption signal;
3. a Bootstrap-created, create-once adoption receipt.

The successful Kubernetes API creation of the Adoption Receipt is the
linearization point for the control transfer. Application health is a separate
readiness concern and is not a precondition for committing the receipt.

### Resource contract

#### Cluster Identity v2

The identity remains a ConfigMap named `atlas-bootstrap-identity` in
`kube-system`, but its v2 data schema is deliberately incompatible with the v1
validator.

Required v2 data fields are:

```text
schema=atlas.io/bootstrap-identity/v2
repositoryURL=<canonical repository URL>
kindConfigSHA256=<canonical Kind configuration SHA-256>
clusterName=<selected Atlas cluster name>
```

The v2 object:

- is created before any Seed mutation on a newly created cluster;
- has `immutable: true`;
- is never updated or deleted by normal Bootstrap;
- binds all later evidence through its Kubernetes object UID;
- intentionally omits the v1 keys `repo` and `kindConfigSHA`.

Omitting the v1 keys is a required downgrade fence. The receipt-unaware
Bootstrap at `783e858` reads exactly those two keys and therefore rejects a v2
cluster during cluster identity validation, before Registry, Seed, AppProject,
or External Root mutation.

For an existing cluster, a missing, foreign, malformed, or unsupported identity
is never treated as fresh. A cluster that does not yet exist is a separate
`NO_CLUSTER` condition: Bootstrap may create it and must install Identity v2
before continuing.

#### GitOps Adoption Signal

The signal is an immutable ConfigMap with the canonical identity:

```text
kind: ConfigMap
namespace: argocd
name: atlas-bootstrap-adoption-signal
schema: atlas.io/bootstrap-adoption-signal/v1
```

Its data binds at least:

```text
repositoryURL=<canonical repository URL>
rootName=<canonical External Root name>
```

The signal:

- exists only in the Git-managed `argocd-self` desired state;
- is absent from every Bootstrap Seed render;
- is created in `argocd`, which is already an allowed `platform-project`
  destination, and therefore does not expand Tier-1 into `kube-system`;
- has `immutable: true`;
- carries Argo CD resource-tracking metadata for `argocd-self`;
- carries `Prune=confirm` and `Delete=false` protection annotations;
- is protected, together with the `argocd` namespace, by enforced admission
  policy before Bootstrap may classify it as valid.

Bootstrap accepts the signal only when all of the following match:

- Group, Kind, namespace, and name;
- schema and expected data;
- repository and Root identity selected by the resolved Profile;
- non-empty Kubernetes object UID;
- Argo CD tracking and `argocd-self` resource inventory;
- the required admission policy and binding are active and fail closed.

The receipt records the observed Signal UID. A manually created look-alike,
wrong UID, missing protection control, or contradictory payload is invalid.
Malicious fabrication by a cluster administrator who can also remove admission
policy is outside the normal-path threat model.

#### Adoption Receipt

The receipt is an immutable ConfigMap with the canonical identity:

```text
kind: ConfigMap
namespace: kube-system
name: atlas-bootstrap-adoption-receipt
schema: atlas.io/bootstrap-adoption-receipt/v1
```

It records at least:

```text
identityUID=<observed Identity v2 UID>
signalUID=<observed Signal UID>
repositoryURL=<canonical repository URL>
rootName=<canonical External Root name>
rootUID=<observed External Root UID>
argocdSelfUID=<observed argocd-self UID>
```

Receipt creation requires valid Identity v2 and Signal records plus the
existence, but not the health, of the External Root and `argocd-self`
Applications. Bootstrap uses a create-only API operation:

- it never applies, patches, replaces, updates, or deletes a receipt;
- a successful create-only API operation is the adoption linearization point;
- the UID and resourceVersion returned for the created Receipt are the durable
  server response identifying that committed object;
- an `AlreadyExists` response requires an immediate re-read and exact
  validation;
- any other create or validation failure is fail-closed;
- `metadata.creationTimestamp` is server-side audit time only. It is not the
  linearization proof and must not be used for cross-object or global ordering.

### State model and readiness

The effective normal-path state machine is:

```text
NO_CLUSTER
    |
    v
 FRESH_V2
    |
    v
SEED_ACTIVE
    |
    | valid, protected Signal observed
    v
RECEIPT_COMMITTING
    |
    | create-only Receipt succeeds
    v
   ADOPTED
    |
    +-- Root and argocd-self ready --> ADOPTED_HEALTHY
    `-- otherwise -----------------> ADOPTED_DEGRADED
```

The rules are:

- Seed authority exists only in `FRESH_V2` / `SEED_ACTIVE`.
- A valid Signal immediately denies further Seed mutation in the current and
  every later process. Its admission protection persists the
  `RECEIPT_COMMITTING` latch until the receipt can be committed.
- Receipt creation occurs immediately after Signal validation. Bootstrap does
  not wait for Root or `argocd-self` health before the create.
- Once a valid Receipt exists, neither health degradation nor missing GitOps or
  Tier-0 objects can restore normal Seed authority.
- A valid Receipt is independent completion evidence. Confirmed Signal absence
  after Receipt commitment produces `ADOPTED_DEGRADED`, not a loss of adoption.
  A currently present Signal whose UID disagrees with the Receipt is
  contradictory and therefore `AMBIGUOUS`.
- `ADOPTED_HEALTHY` and `ADOPTED_DEGRADED` describe readiness only. They do not
  change ownership.
- Any invalid or contradictory evidence is `AMBIGUOUS` and denies all normal
  mutation.
- Any evidence read or decode failure is `UNAVAILABLE` and denies all normal
  mutation.

### Evidence state truth table

The tables apply to an existing cluster after every required API read has
completed successfully. `Absent` means a successful authoritative NotFound or
empty result. Timeout, Forbidden, transport failure, malformed API output,
deserialization failure, or inability to read a required evidence or protection
resource is `UNAVAILABLE`, never `Absent`. `UNAVAILABLE` supersedes every table
cell and refuses all normal mutation.

`Valid Receipt` means its schema and contents are internally valid and bind to
the selected Profile and Identity. It remains valid completion evidence when
the referenced Signal is confirmed absent. If a Signal is currently present,
its UID must equal the Receipt's `signalUID`; a mismatch is a cross-record
contradiction and is `AMBIGUOUS`.

#### Valid Identity v2

| Signal | Receipt absent | Receipt valid | Receipt invalid |
| --- | --- | --- | --- |
| Absent | `FRESH_V2`: Seed permitted | `ADOPTED_DEGRADED`: Seed denied; recovery required | `AMBIGUOUS`: deny |
| Valid | `RECEIPT_COMMITTING`: Seed denied; create Receipt | `ADOPTED_*` when Signal UID matches; otherwise `AMBIGUOUS` | `AMBIGUOUS`: deny |
| Invalid | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny |

#### Identity v1

| Signal | Receipt absent | Receipt valid | Receipt invalid |
| --- | --- | --- | --- |
| Absent | `MIGRATION_REQUIRED`: deny | `AMBIGUOUS`: v1 cannot own a valid Receipt; deny | `AMBIGUOUS`: deny |
| Valid | `MIGRATION_REQUIRED`: deny | `AMBIGUOUS`: Receipt requires Identity v2; deny | `AMBIGUOUS`: deny |
| Invalid | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny |

#### Missing, foreign, or malformed Identity

Each of `MISSING`, `FOREIGN`, and `MALFORMED` uses the following complete
matrix; none is equivalent to a new cluster.

| Signal | Receipt absent | Receipt valid | Receipt invalid |
| --- | --- | --- | --- |
| Absent | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny |
| Valid | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny |
| Invalid | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny | `AMBIGUOUS`: deny |

These three tables cover Identity `v1`, `v2`, `missing`, `foreign`, and
`malformed` against Signal `absent`, `valid`, and `invalid` and Receipt
`absent`, `valid`, and `invalid`. Every state other than the single
`v2/absent/absent` cell denies Seed.

### Post-adoption normal authority matrix

After a valid Signal is observed, ordinary `apply` has no write authority over
GitOps control-plane or Tier-0 objects. The phrase "non-Seed convergence" is
limited to the following matrix.

| Resource or operation | Before Signal | Signal observed, Receipt absent | Receipt valid |
| --- | --- | --- | --- |
| Host, configuration, artifact checks | Read | Read | Read |
| Existing Kind node and identity validation | Read | Read | Read |
| Local Registry health, restart, node configuration, image cache | Ensure | Ensure | Ensure |
| Identity v2 | Create on a newly created cluster; otherwise read | Read only | Read only |
| Seed namespace, CRDs, ConfigMap, Deployments, StatefulSets | Create/ensure | No writes | No writes |
| GitOps Signal | No Bootstrap writes | Read only | Read only |
| Adoption Receipt | Must be absent | Create once, then validate | Read only |
| `atlas-bootstrap` AppProject | Initial create/ensure | Read-only exact validation | Read-only exact validation |
| External Root | Initial create; existing object exact validation | Read-only exact validation | Read-only exact validation |
| Root children and `argocd-self` | Read readiness | Read readiness | Read readiness |

After Signal observation, a missing or drifted AppProject, External Root, Argo
CD workload, or Application causes ordinary `apply` to report the damaged
handoff and fail. It does not recreate, annotate, apply, or repair the object.
Recovery belongs exclusively to an approved break-glass command.

### Downgrade fence

Identity v2 schema incompatibility is mandatory, not an implementation detail.
Adding a new field while preserving the v1 `repo` and `kindConfigSHA` values is
non-conformant because the old Bootstrap would ignore the new field.

The release test must run a receipt-unaware Bootstrap, including the exact
`783e858` implementation, against an adopted v2 test cluster. The test deletes
or hides `argocd-self` only inside the controlled recovery fixture and proves
that old Bootstrap fails at Identity validation before any Registry, Argo CD,
AppProject, or External Root mutation. Resource versions, UIDs, and audit
evidence must remain unchanged.

Admission protection is a second fence against evidence deletion, not a
substitute for schema incompatibility.

### Evidence protection and threat boundary

`immutable: true` prevents ConfigMap data updates but does not prevent deletion
and recreation. Kubernetes explicitly permits deleting and recreating an
immutable ConfigMap. Therefore immutability alone is not an adoption proof
protection mechanism.

Before a Signal can be valid, Atlas must install and verify fail-closed
`ValidatingAdmissionPolicy` and `ValidatingAdmissionPolicyBinding` controls.
Kubernetes provides Validating Admission Policy as a stable, in-process CEL
admission mechanism. Kubernetes does not permit a
`ValidatingAdmissionPolicy` to match either `ValidatingAdmissionPolicy` or
`ValidatingAdmissionPolicyBinding` resources. The Atlas admission controls
therefore deny unauthorized update or delete operations against:

- Identity v2;
- GitOps Signal;
- Adoption Receipt;
- the `argocd` and `kube-system` namespaces where deletion would remove the
  protected records.

The protection Policy and Binding are outside their own admission match scope.
They are protected instead by exact, minimal RBAC; Git prune/delete protection;
exact-object and hash verification; and a Human Judgment Gate for every
authorized change. Ordinary principals and Argo CD have no mutation authority
over them. [ADR-0003](0003-bootstrap-break-glass-recovery.md) defines the
canonical split protection, admission escape, and recovery authority model.

Combined evidence enforcement and Policy/Binding protection are enabled only
after an independent break-glass principal exists and has successfully
exercised the exact Binding suspend and restore path against an isolated canary
policy. The principal and its credential must not depend on Argo CD, the
protected namespaces, or the policy objects it may need to suspend.

Production admission activation is staged:

1. deploy and type-check the Policy and Binding with non-blocking `Audit` and/or
   `Warn` actions;
2. inspect audit evidence and run positive and negative request fixtures;
3. verify the break-glass principal and suspend/restore procedure again against
   the production policy target;
4. switch to `failurePolicy: Fail` with `Deny` enforcement and retained audit
   evidence;
5. verify denial for ordinary principals and the bounded exception for the
   break-glass principal;
6. only then allow GitOps to create the Signal.

Normal Bootstrap, Argo CD, controllers, and ordinary human operators receive no
delete or rewrite permission for committed evidence. The independently
authenticated break-glass principal is the only policy exception. `Warn` is not
combined with `Deny` in the enforcing Binding.

The implementation must reference and test the upstream Kubernetes semantics:

- [Immutable ConfigMaps remain deletable](https://kubernetes.io/docs/concepts/configuration/configmap/)
- [Validating Admission Policy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/)

An actively malicious cluster administrator can remove admission policy and
all evidence, or modify the API server itself. That actor is outside this
normal-path threat model. Accidental deletion, Git prune, use of an old
Bootstrap, ordinary operator error, and non-break-glass automation are inside
the threat model and must fail closed.

### Break-glass dependencies

Atlas distinguishes a minimal admission escape path from complete Seed
recovery.

Before any production evidence `Deny` Binding and its separate RBAC/Git/Human
protection are enabled, the minimal admission escape capability must already
provide:

- an independently authenticated and securely escrowed break-glass principal;
- exact target confirmation and human authorization;
- auditable Policy and Binding suspend/restore operations;
- a tested canary proving the principal can suspend a defective enforcing rule
  and restore enforcement;
- a procedure that does not depend on Argo CD or either protected namespace.

This minimal capability prevents admission self-lock. It does not authorize
Seed restoration or satisfy the complete recovery requirement.

Receipt-aware enforcement must not be released merely because this ADR becomes
Accepted. A separate break-glass recovery ADR must first be Accepted and its
implementation must provide:

- an explicit recovery command separate from normal `apply`;
- strong human authorization and exact target confirmation;
- snapshot and audit evidence before mutation;
- controlled suspension and restoration of evidence protection;
- known-good Git revision selection;
- adoption-compatible Seed restoration;
- inside-out health verification;
- an exercised failure and rollback runbook on the target environment.

The break-glass implementation, audit path, admission exception, runbook, and
recovery drill are release prerequisites for receipt-aware enforcement. Until
they exist, the receipt-aware code path must not be enabled in a release used
for recovery-sensitive clusters.

### Rollout and migration

Rollout uses independently reviewable phases:

0. **Admission escape readiness.** Establish the independent break-glass
   principal, authorization and audit path. Exercise Policy and Binding
   suspend/restore using a disposable admission-escape canary. No production
   evidence `Deny` Binding or separate Policy/Binding protection is activated
   before this phase succeeds.
1. **Protection foundation.** Deploy the production admission controls first in
   `Audit` and/or `Warn` mode, validate type checking and audit evidence, then
   switch to fail-closed `Deny`. Re-verify the break-glass exception, and only
   afterward allow GitOps to create the Signal. The Seed remains unchanged, and
   every supported existing cluster must show a valid, protected Signal.
2. **Full Seed recovery readiness.** Accept and implement the separate recovery
   ADR, command, audit controls, and runbook; complete a recovery drill.
3. **Legacy migration.** A human-gated migration workflow validates Identity
   v1, the protected Signal, current Root, and `argocd-self`; replaces Identity
   v1 with incompatible Identity v2; and commits the Receipt. Normal `apply`
   never performs this migration. Interruption after removing v1 but before
   creating v2 produces `MISSING/AMBIGUOUS`; interruption after v2 but before
   Receipt produces `RECEIPT_COMMITTING` because the protected Signal persists.
4. **Receipt-aware Bootstrap.** Enable Identity v2 creation for new clusters,
   create-once Receipt commitment, the authority matrix, and downgrade-fence
   tests. A supported legacy cluster not explicitly migrated fails closed.

No receipt-aware Bootstrap may be released until every supported environment
has phase-zero admission escape capability, phase-one evidence and protection,
phase-two recovery capability, and an explicit phase-three migration
disposition.

## Consequences

- Receipt creation, rather than transient Application health, becomes the
  single durable control-transfer point.
- Health can move between Healthy and Degraded without changing authority.
- A committed Receipt remains authoritative when the Signal is absent; the
  cluster becomes `ADOPTED_DEGRADED` and requires recovery.
- Deleting `argocd-self`, the External Root, or Argo CD workloads after receipt
  commitment cannot restore ordinary Seed authority.
- A valid protected Signal makes interruption before Receipt creation
  resumable without a backward transition.
- Identity v2 prevents receipt-unaware Bootstrap versions from reaching any
  mutating handoff phase.
- Fresh-cluster initialization, legacy migration, adopted-cluster degradation,
  ambiguous evidence, and break-glass recovery become distinct operations.
- Bootstrap performs one final bounded Receipt create as part of handoff; this
  does not grant steady-state reconciliation authority.
- `platform-project` must gain only the explicit admission resource kinds
  required for the protection foundation. This is a Tier-1 permission change
  and requires conformance review.
- Production evidence enforcement and the separate Policy/Binding protections
  cannot activate until the independent escape principal and suspend/restore
  procedure have been exercised.
- Rollback after Identity v2 or Receipt creation cannot restore the previous
  heuristic. Defects are corrected forward or handled through approved
  break-glass recovery.

## Alternatives considered

### Wait for Healthy before creating the Receipt

Rejected because Signal loss during the health wait reopens Seed authority.
Health is readiness, not the ownership linearization point.

### Continue using live `argocd-self` presence

Rejected because deletion makes authority regress from adopted to Seed-active.

### Store proof only under repository `.state/`

Rejected because it is host-local, disposable, and unsuitable for recovery
from another operator workstation.

### Add lifecycle fields while retaining the v1 identity keys

Rejected because receipt-unaware Bootstrap versions ignore unknown fields and
would still pass their identity check.

### Use only a GitOps-managed marker

Rejected as the sole proof because namespace loss or accidental deletion would
again make prior adoption ambiguous. The protected Signal latches the
pre-commit transition; the substrate Receipt preserves the completed transfer.

### Use a mutable lifecycle phase only

Rejected as the final proof because an in-place phase field is easier to
regress accidentally. The final Receipt is create-once and separately
protected.

### Rely only on `immutable: true`

Rejected because Kubernetes permits deletion and recreation of immutable
ConfigMaps.

### Introduce an external state database

Rejected for the current phase because it adds a new Bootstrap dependency and
recovery failure domain before the Kubernetes substrate exists.

## Verification

Implementation is conformant only when it proves all of the following:

- a new cluster creates Identity v2 before any Seed mutation;
- Identity v2 is rejected by the exact receipt-unaware Bootstrap at `783e858`
  before any Argo CD or Tier-0 mutation;
- the Seed render never contains the GitOps Signal;
- `argocd-self` creates the exact Signal through Git-managed desired state;
- admission protection is active before the Signal is accepted;
- production admission policy passes an `Audit`/`Warn` observation phase before
  `Deny`, and the independent escape principal can suspend and restore the
  enforcing Binding before enforcement;
- exact minimal RBAC, Git prune/delete protection, object/hash checks, and Human
  Gates protect the Policy and Binding that VAP cannot match;
- deleting or updating Identity, Signal, Receipt, or their protection controls
  is denied to every normal principal;
- a valid Signal immediately denies Seed and triggers Receipt creation without
  waiting for health;
- interruption before Receipt creation resumes from the protected Signal;
- Receipt create is atomic and create-only, returns the committed UID and
  resourceVersion, and handles `AlreadyExists` by exact validation;
- `metadata.creationTimestamp` is used only as server audit time and never as a
  linearization or global ordering value;
- a valid Receipt with a confirmed missing Signal is `ADOPTED_DEGRADED`, while
  a present Signal whose UID differs from the Receipt is `AMBIGUOUS`;
- API timeout, Forbidden, transport, malformed response, deserialization, and
  evidence-read failures always produce `UNAVAILABLE` and no normal mutation;
- Root and `argocd-self` health changes only
  `ADOPTED_HEALTHY/ADOPTED_DEGRADED`, never Seed authority;
- every cell in the evidence truth tables has a contract test;
- a legacy Identity v1 always produces `MIGRATION_REQUIRED` or `AMBIGUOUS` in
  normal `apply`;
- after Signal observation, ordinary `apply` performs no writes to Seed,
  AppProject, External Root, or GitOps Application objects;
- two normal applies after adoption preserve Receipt and control-plane resource
  identities;
- the separate break-glass command can restore an adoption-compatible Seed
  under explicit authorization and re-establish evidence protection;
- `task quality` and explicitly approved clean-cluster, interruption,
  downgrade, evidence-deletion, and break-glass integration tests pass on the
  target macOS and OrbStack environment.

The Bootstrap runbook must document proof inspection, truth-table outcomes,
read-only post-adoption behavior, downgrade rejection, and failure semantics.
The separate recovery runbook must document every break-glass mutation and
audit artifact.

## Acceptance gates

Reviewers accepted this ADR after confirming all of the following:

- Receipt creation is the unambiguous control-transfer linearization point;
- the create response UID and resourceVersion, rather than timestamp ordering,
  identify the committed Receipt;
- the Signal is durable for the entire pre-Receipt interval;
- a committed Receipt remains authoritative after confirmed Signal loss;
- Identity v2 reliably fences all supported receipt-unaware Bootstrap versions;
- the truth tables and authority matrix are complete;
- all indeterminate evidence reads map to `UNAVAILABLE` and fail closed;
- the minimum admission escape capability precedes production evidence `Deny`
  and separate Policy/Binding protection;
- exact Signal, Receipt, admission, threat-model, and migration contracts are
  acceptable;
- the dependency on an accepted and implemented break-glass design is explicit.

Acceptance of this ADR authorizes separately reviewed phased implementation of
this design. It does not itself authorize a break-glass execution, Tier-0
apply, legacy-cluster migration, or production rollout.
