# Bootstrap and GitOps Handoff Runbook

## Preconditions

```bash
task quality
./bootstrap/atlas doctor --env test
./bootstrap/atlas status --env test
```

Confirm that the selected Profile, Git revision, locked artifacts, Docker
context, and target cluster name are correct. `doctor`, `render`, and `status`
must not change external state.

## Initial test bootstrap

Crossing Tier-0 requires explicit human approval:

```bash
./bootstrap/atlas apply --env test --approve-tier0
```

Successful completion requires the External Root and `argocd-self` to be
Synced/Healthy. This is a handoff-health condition, not the Receipt-backed
adoption proof defined by ADR-0002. Verify again with:

```bash
./bootstrap/atlas status --env test
```

## Failure boundary

Bootstrap is idempotent only within its defined authority. Preserve logs and
inspect the failed phase before retrying. Do not delete a cluster, Registry,
Root Application, or persistent data as a generic retry mechanism.

If `argocd-self` exists but is not Healthy, do not assume the Seed may safely
retake ownership. Normal `apply` reports the observed state and stops without
reapplying Seed resources. Recovery requires a separately approved break-glass
path; it is not part of this command.

Root drift is a break-glass concern. Normal Bootstrap must report it and stop.

## Explicit integration verification

The integration task mutates the `test` profile and therefore remains outside
the default quality gate:

```bash
task integration
```

It performs two approved applies and verifies that the Argo CD server,
Application Controller, and External Root retain their Kubernetes identities.

## Recorded runtime baseline

The following record captures completed verification; it does not grant future
mutation authority or redefine the Bootstrap contract.

### 2026-08-16 — Four-node test control plane

- Git revision: `74500e6a001e853d6c66f7c7b6d36d1a979034d0`
- Kind configuration SHA-256:
  `3dbcba1ffd9b84b675f79d4af8b411592d329ce5793d30a5f6d31c94bed6ab05`
- Target: `atlas-test`, context `kind-atlas-test`, Docker context `orbstack`
- Host: macOS on Apple Silicon with the locked `versions.lock` toolchain and
  images
- Main Quality evidence:
  <https://github.com/snkio027/atlas/actions/runs/31939616229>

An exact Human Judgment Gate authorized replacement of the previous single-node
test cluster and the Tier-0 operations performed by `task integration`. The run
completed with these results:

- one control-plane and three workers were Ready;
- the `control`, `gateway`, `compute`, and `data` node pools were unique;
- only the data worker carried `atlas.io/node-pool=data:NoSchedule`; the
  control-plane carried Kind's standard control-plane taint and the other
  workers were untainted;
- all node containers used the locked Kind node-image digest;
- all nodes had identical local Registry trust configuration and the locked
  Argo CD and Redis Seed images;
- the Bootstrap Identity recorded the exact Kind configuration SHA-256 above;
- the Registry, Argo CD, External Root, and `argocd-self` reported Ready or
  Synced/Healthy as applicable;
- all five Argo CD Applications reported Synced/Healthy; and
- the second approved apply retained the Argo CD server, Application
  Controller, and External Root UIDs.

The node pools are scheduling roles on one OrbStack host, not HA replicas or
independent failure domains. This record does not close INV-02, implement the
ADR-0002 Signal/Receipt state machine, or complete ADR-0003 break-glass
recovery. Platform expansion remains outside this validated baseline.
