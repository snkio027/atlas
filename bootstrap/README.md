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

## Supply-chain contract

`versions.lock` contains exact tool, chart, and image versions. Bootstrap never
resolves `latest` and never downloads artifacts. The locked Argo CD Chart archive
and its GitOps-renderable vendored tree must exist under `vendor/charts/`. All
locked images must already be present in the local Docker content store before
entering offline operation.

The Seed and `argocd-self` use the same Chart, values, release name, images, and
Application Health asset. Helm is only a renderer; Atlas creates no Helm Release
state.
