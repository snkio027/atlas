# Atlas Bootstrap

Bootstrap establishes only the minimum control plane needed for Argo CD to
adopt the Git-defined desired state. It does not install platform components,
business workloads, Gateway resources, Operators, or application secrets.

## Commands

```bash
./bootstrap/atlas doctor
./bootstrap/atlas render
./bootstrap/atlas status
./bootstrap/atlas apply --approve-tier0
./bootstrap/atlas --help
./bootstrap/atlas --version
```

Use `--env test` for the isolated `atlas-test` integration profile.

`doctor` and `status` are read-only. `render` writes deterministic manifests to
the ignored `.state/rendered/` directory. `apply` creates the Kind substrate,
local Registry, adoption-compatible Argo CD Seed, `atlas-bootstrap` AppProject,
and External Root Anchor. It returns success only after the Git-managed
`argocd-self` Application is Synced and Healthy.

The `--approve-tier0` flag is mandatory for `apply`. An existing External Root
is compared but never overwritten by the normal Bootstrap path. Root repair is
a separate break-glass operation and is intentionally not implemented here.

While `argocd-self` exists, normal Bootstrap does not reapply the Seed. Its
Synced/Healthy state is a handoff-health signal, not the monotonic adoption proof
defined by ADR-0002. An existing but unhealthy Application makes `apply` fail
closed. If `argocd-self` is later deleted, the current heuristic can re-enter the
Seed path; ADR-0002 Phase 4 is required to close that known authority gap.

## Shell modules

```text
bootstrap/
├── atlas
├── lib/{runtime,lock,config}.sh
├── host/doctor.sh
├── cluster/kind.sh
├── registry/local.sh
├── argocd/{render,seed,handoff,status}.sh
├── drill/
│   ├── atlas-kind-drill
│   ├── contract.sh
│   ├── evidence.sh
│   ├── cluster-create.sh
│   └── lock.sh
└── recovery/
    ├── atlas-recovery
    └── audit-config.sh
```

Files follow lifecycle and domain boundaries. Public functions use explicit
actions such as `cluster::ensure_kind`, `registry::ensure_local`,
`argocd::install_seed`, `argocd::instantiate_root`, and
`argocd::inspect_status`; `reconcile` is not used as a generic workflow name.

`bootstrap/recovery/atlas-recovery` is a physically separate entry point
governed by ADR-0003. Its current Phase-0 surface only renders an audited Kind
configuration. It cannot create a cluster, issue credentials, activate RBAC or
Admission, or dispatch recovery. The normal `bootstrap/atlas` command never
imports it.

`bootstrap/drill/atlas-kind-drill` is the separately gated cluster-lifecycle
entry point. It can create one uniquely named audited Kind drill cluster, but
has no reuse or delete command and cannot issue credentials or dispatch
recovery. Creation requires a clean Git authority, an owner-only evidence root,
an owner-controlled, read-only, approval-bound policy snapshot, a hash-chained
journal, an exact interactive challenge, and the explicitly bound OrbStack
Docker context and endpoint. Git
authority is resolved through an environment-clean read-only invocation, and
sparse checkout or tracked entries hidden by index flags are rejected. Evidence
storage in shared temporary directories is also rejected. It is not included in
routine tasks; see the Phase-0 runbook before considering execution.

## Configuration contract

Environment Profiles and `versions.lock` are parsed separately as strict
`KEY=value` data. They are never sourced or evaluated. Resolution rejects
unknown or duplicate keys, symlinks, repository escapes, unsafe ports and
timeouts, Profile identity mismatches, and Git source values inconsistent with
the checked-in AppProjects and Applications. The resolved maps become read-only
before command execution. Help, version, and argument errors do not load a
Profile.

## Kind topology contract

Normal Bootstrap accepts a canonical Kind topology with exactly one explicit
`control-plane` entry and any non-negative number of explicit `worker` entries.
Multiple control planes are rejected because Kind then creates an implicit
Envoy load balancer outside Atlas's locked node-image supply chain. The drill
cluster remains independently constrained to its single-node audit topology.

The Shell parser intentionally supports only Atlas's normalized `nodes:` YAML
subset. It rejects duplicate or flow-style declarations, omitted or unknown
roles, aliases, tabs, and node-level `image:` overrides before cluster discovery
or creation. Nested labels, mounts, and kubeadm patch literals remain Kind-owned
content. Runtime validation compares declared role counts with running Docker
containers, proves the exact Docker/Kubernetes Node name sets and control-plane
labels, requires every Node to be Ready, and rejects auxiliary or unknown Kind
container roles. Registry configuration and image preloading consume only this
validated Kubernetes Node set.

## Supply-chain contract

`versions.lock` contains exact tool, chart, and image versions. Bootstrap never
resolves `latest` and never downloads artifacts. The locked Argo CD Chart archive
and its GitOps-renderable vendored tree must exist under `vendor/charts/`. All
locked images must already be present in the local Docker content store before
entering offline operation.

The Seed and `argocd-self` use the same Chart, values, release name, images, and
Application Health asset. Helm is only a renderer; Atlas creates no Helm Release
state.

## Verification

`task quality` runs non-mutating Shell, configuration, Bootstrap, render,
GitOps, supply-chain, and mocked drill cluster-creation contracts. The drill
tests do not create a cluster. `task integration` is separate because it
performs two explicitly approved applies against the `test` Kind profile and
verifies stable resource identities after a Healthy GitOps handoff.
