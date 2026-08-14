# Atlas Architecture Overview

Atlas separates one-time trust bootstrapping from continuous desired-state and
runtime reconciliation.

```text
Human / Policy Governance
          |
          v
Shell Bootstrap (finite authority)
          |
          v
External Root Anchor (Tier-0)
          |
          +-- Project Bootstrap
          +-- Platform Control (Tier-1)
          `-- Workload Control (Tier-1 orchestration)
                         |
                         v
                  Tenant Workloads (Tier-2)
```

The External Root and Two-Level Reconciliation DAG are frozen architecture.
Directory layout expresses domain ownership; sync waves express dependencies.
Argo CD is the sole continuous GitOps reconciler, while domain operators own
runtime behavior.

This page is intentionally non-normative. See the
[Operating Model](operating-model.md) and [GitOps Standard](../standards/gitops.md)
for binding invariants and exact identifiers.
