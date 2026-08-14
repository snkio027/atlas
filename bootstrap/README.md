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

After `argocd-self` exists, normal Bootstrap never reapplies the Seed. A Healthy
Application confirms that control has transferred; an existing but unhealthy
Application makes `apply` fail closed so that Bootstrap cannot silently reclaim
authority.

## Shell modules

```text
bootstrap/
├── atlas
├── lib/{runtime,lock,config}.sh
├── host/doctor.sh
├── cluster/kind.sh
├── registry/local.sh
└── argocd/{render,seed,handoff,status}.sh
```

Files follow lifecycle and domain boundaries. Public functions use explicit
actions such as `cluster::ensure_kind`, `registry::ensure_local`,
`argocd::install_seed`, `argocd::instantiate_root`, and
`argocd::inspect_status`; `reconcile` is not used as a generic workflow name.

## Configuration contract

Environment Profiles and `versions.lock` are parsed separately as strict
`KEY=value` data. They are never sourced or evaluated. Resolution rejects
unknown or duplicate keys, symlinks, repository escapes, unsafe ports and
timeouts, Profile identity mismatches, and Git source values inconsistent with
the checked-in AppProjects and Applications. The resolved maps become read-only
before command execution. Help, version, and argument errors do not load a
Profile.

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
GitOps, and supply-chain contracts. `task integration` is separate because it
performs two explicitly approved applies against the `test` Kind profile and
verifies stable resource identities after adoption.
