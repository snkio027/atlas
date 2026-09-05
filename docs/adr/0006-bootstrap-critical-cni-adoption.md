# ADR-0006: Bootstrap-critical CNI instantiation and Argo adoption

- Status: Accepted
- Date: 2026-09-05
- Deciders: repository owner and required CODEOWNERS
- Supersedes: none
- Superseded by: none

## Context

Atlas defines Cilium as its primary programmable CNI. In steady state, Cilium
is a Tier-1 Platform Capability whose desired state belongs to Git and whose
continuous reconciliation belongs to Argo CD.

The current Bootstrap creates Kind and requires every Node to be Ready before
it establishes the local Registry and Argo CD Seed. Kind currently installs its
default CNI. If a future Atlas Kind substrate disables that CNI, the dependency
cycle becomes:

```text
Kind API available
  -> primary CNI required
  -> Nodes Ready
  -> Argo CD operational
```

An ordinary post-Argo Cilium Application cannot make the initial cluster
network-ready because Argo itself depends on that network. Installing Kind's
default CNI first and replacing it later would introduce transient dual-CNI
ownership, IPAM and routing ambiguity, and a disruptive mutation outside the
intended steady-state owner.

Two additional implementation constraints follow from the current code:

1. Kind API availability and Kubernetes Node readiness are currently treated as
   one gate and must be separated before Cilium can be seeded.
2. Registry configuration and image preloading currently consume the complete
   Ready Node inventory, but the Cilium images must reach each Kind node's
   containerd before Nodes can become Ready.

This decision changes control ownership and narrows the Bootstrap boundary. It
therefore requires an ADR and explicit amendments to the frozen Architecture
and GitOps standards. It does not select a Cilium release or authorize an
implementation or runtime change.

## Decision

Atlas adopts:

```text
A -- BOOTSTRAP_INSTANTIATE_ARGO_ADOPT
```

### Bootstrap-critical substrate category

`BOOTSTRAP_CRITICAL_SUBSTRATE` is an architectural category for a capability
that is required before the GitOps reconciler itself can become operational.
It is not a generic Bootstrap extension mechanism.

A capability in this category MUST satisfy all of the following:

- Git owns its single exact desired-state definition.
- Its complete offline artifact and image authority is exact and locally
  available before execution.
- Bootstrap mutation authority is creation-scoped to a cluster successfully
  created by the same locked Bootstrap invocation.
- Bootstrap MUST NOT seed it into a pre-existing cluster.
- Bootstrap performs no steady-state reconciliation.
- Bootstrap permanently stops writing that capability for the cluster after a
  successful Seed.
- Argo CD explicitly adopts the same Git-defined objects and becomes the only
  steady-state Desired-State Reconciler.
- Missing, partial, unknown, or drifted state fails closed.
- Runtime behavior remains owned by Kubernetes and the capability's own
  controllers.

For Atlas v1, this exception is limited to the primary CNI. It does not apply
to Gateway, observability, Flink, Redpanda, MinIO, other Operators, platform
services, or workloads.

The Bootstrap invariant is:

> Bootstrap MUST NOT instantiate ordinary platform capabilities. Bootstrap MAY
> finitely instantiate an exact Git-defined bootstrap-critical substrate
> capability when that capability is required before the GitOps reconciler can
> exist. Such authority is creation-scoped, ends before steady-state adoption,
> and MUST NOT be reacquired for an existing or drifted substrate.

### Ownership

The ownership model is:

```text
Git                         exact Cilium Definition
Bootstrap                   finite initial Instantiation
Argo CD                     steady-state Desired-State Reconciliation
Cilium/Kubernetes           Runtime Behavior
```

Cilium remains a normal Tier-1 Platform Capability in steady state. Exactly one
`platform-project` Leaf Application owns its Git reconciliation. No fourth
AppProject, new Tier-0 Application, or additional Root child is introduced.

Bootstrap directly instantiates the Git-defined Cilium manifests before Argo
exists. It does not represent the pre-Argo phase as an Argo Application.

### Semantic lifecycle

The lifecycle checkpoints are:

```text
ABSENT
  -> KIND_API_AVAILABLE
  -> CNI_IMAGE_CACHE_READY
  -> CNI_SEEDING
  -> CNI_SEEDED
  -> NODES_READY
  -> SUBSTRATE_IDENTITY_COMMITTED
  -> REGISTRY_READY
  -> ARGO_SEEDED
  -> ROOT_INSTANTIATED
  -> CILIUM_ADOPTION_PENDING
  -> CILIUM_ADOPTED
  -> BOOTSTRAP_COMPLETE
```

These names are semantic checkpoints. They do not require a persisted
repository state machine, a new Receipt, Fence, Signal, recovery ceremony, or
assurance subsystem. Persistent state may be introduced only after a separate,
demonstrated recovery requirement and review.

Bootstrap may mutate Cilium only between `KIND_API_AVAILABLE` and the successful
completion of `CNI_SEEDED`, and only when the current invocation created the
cluster. `CNI_SEEDED` is the permanent end of normal Bootstrap Cilium mutation
authority for that cluster. There is intentionally no Bootstrap writer while
Argo is being established.

`CILIUM_ADOPTED` proves that Argo has assumed steady-state authority. The gap
between Seed completion and adoption is bounded and has no Desired-State
Reconciler; overlap between Bootstrap and Argo Cilium writers is forbidden.

### Retry and drift semantics

- At `ABSENT`, Bootstrap may create the cluster.
- While CNI Seed is in progress, only the invocation that created the cluster
  may continue.
- If that invocation terminates before successful `CNI_SEEDED`, a later normal
  Bootstrap MUST report `SUBSTRATE_INCOMPLETE` and MUST NOT reacquire Seed
  authority. Recovery is destroy-and-recreate through a separately authorized
  destructive path.
- If CNI Seed, Node readiness, and substrate identity commitment completed but
  Argo did not, a later Bootstrap may continue only after exact read-only Cilium
  validation. It MUST NOT write Cilium.
- At and after `CILIUM_ADOPTED`, Bootstrap remains permanently read-only with
  respect to Cilium for that cluster.
- An existing cluster with missing, partial, unknown, foreign, or drifted
  Cilium state fails closed. Normal Bootstrap performs no repair, replacement,
  migration, or reinstallation.
- After adoption, Argo owns ordinary desired-state convergence. Bootstrap may
  report or wait for drift resolution but may not participate in it.

This deliberately favors destructive recreation of a disposable local Kind
substrate over a partial-Seed resume protocol.

### Seed and adoption parity

Bootstrap Seed authority and Argo Application authority MUST consume the same:

```text
Git revision
vendored Chart and renderable tree authority
Atlas values
release name
namespace
rendered object identity set
```

The initial Argo adoption proof MUST establish at least:

- exactly one Cilium Application in `platform-project`;
- the exact expected repository, resolved initial Git revision, vendored
  source, values, release name, and destination;
- `Synced` and `Healthy` status;
- the exact expected Git-managed object ownership and inventory;
- no unknown or shared Git ownership; and
- no concurrent Bootstrap Cilium writer.

Health alone is not an adoption proof. Later Git revisions remain Argo-owned
steady-state changes after the exact initial handoff has been proven.

### Kind and offline supply-chain consequences

Future implementation is expected to require:

- disabling Kind's default CNI;
- retaining the IPv4-only local baseline;
- retaining kube-proxy unless separately authorized;
- explicitly matching the Kubernetes Pod CIDR and Cilium IPAM;
- separating API-ready from Node-ready gates;
- validating a pre-ready Kind container inventory;
- importing every required Cilium image by digest into each node before Node
  readiness;
- completing Cilium Seed readiness before the existing full Node Ready and
  topology validation;
- preserving exact cluster identity checks; and
- treating the Kind configuration SHA change as drift that requires a new
  cluster rather than in-place conversion.

The future implementation MUST NOT download a Cilium artifact or image at
runtime. It must preserve complete image inventory, exact digests, and the
existing offline supply-chain boundary.

## Consequences

Positive consequences are:

- empty-host Bootstrap remains reproducible;
- Atlas has one primary CNI ownership model rather than a migration between two
  CNIs;
- Bootstrap retains only finite pre-Argo authority;
- Argo remains the sole steady-state GitOps reconciler;
- the same Git definition and offline artifacts drive Seed and steady state;
  and
- no transient default-CNI replacement is required.

Costs and limitations are:

- Bootstrap gains one narrowly scoped pre-Argo mutation responsibility;
- its lifecycle must distinguish API, CNI, and Node readiness;
- pre-ready offline image delivery requires a separate implementation slice;
- failed initial CNI Seed requires an authorized destructive rebuild; and
- exact Seed-to-Argo adoption parity must be proven.

## Alternatives considered

### Kind default CNI followed by Cilium replacement

Rejected. It creates transient dual-CNI ownership, routing and IPAM ambiguity,
network disruption, and a hidden substrate mutation that is neither a clean
Bootstrap instantiation nor ordinary Argo reconciliation.

### External or manual Cilium prerequisite

Rejected. It moves critical initial state outside Git and Bootstrap, breaks
reproducible empty-host creation, and leaves ownership, version, and recovery
evidence dependent on an untracked operator action.

## Version boundary

This ADR does not select or authorize:

- a Cilium or Chart version;
- a Chart checksum;
- image names or digests;
- Helm value keys;
- Cilium readiness resource names; or
- any version-specific Cilium capability.

The ownership decision is version-independent. Compatibility with the locked
Kubernetes and Kind versions, exact render settings, readiness resources,
required image inventory, and IPAM field names are decided only after a
separate Owner Version Gate.

## Verification and rollout gates

Implementation MUST remain split into these separately reviewed steps:

1. Owner Version Gate;
2. Platform-1S supply-chain authority;
3. Platform-1B substrate and Bootstrap implementation;
4. Platform-1A Argo steady-state Application and adoption proof;
5. disposable end-to-end validation; and
6. explicit Local Activation Human Gate.

Later implementation must prove creation-scoped authority, exact offline image
availability, Seed/render parity, zero Bootstrap mutation for existing
clusters, failure closure for incomplete or drifted state, one steady-state
Application owner, and an exact Argo adoption proof.

Acceptance of this ADR does not approve a Cilium version, supply-chain change,
implementation, Kind rebuild, cluster access, or runtime mutation.
