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
Synced/Healthy. Verify again with:

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
