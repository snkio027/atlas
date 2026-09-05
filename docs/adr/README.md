# Architecture Decision Records

ADRs record decisions that change or clarify Atlas architecture. They do not
silently override the frozen standards; a conflicting proposal must explicitly
identify and update the higher authority through its review process.

## Index

| ADR | Status | Decision |
| --- | --- | --- |
| [0001](0001-retain-shell-bootstrap.md) | Accepted | Retain Shell as the Bootstrap Stage-1 implementation |
| [0002](0002-monotonic-bootstrap-adoption-proof.md) | Accepted | Establish a monotonic Bootstrap adoption proof |
| [0003](0003-bootstrap-break-glass-recovery.md) | Accepted | Define Bootstrap break-glass recovery and admission escape |
| [0004](0004-length-bounded-recovery-principal-identities.md) | Accepted | Bound recovery principal identities for X.509 |
| [0005](0005-personal-local-target-materialization.md) | Accepted | Define PERSONAL_LOCAL target materialization |
| [0006](0006-bootstrap-critical-cni-adoption.md) | Accepted | Define bootstrap-critical CNI instantiation and Argo adoption |

## Lifecycle

Use `0000-template.md`. Valid statuses are Proposed, Accepted, Rejected,
Deprecated, and Superseded. A superseding ADR must link both directions and
state the migration and rollback boundary.

An ADR is required for changes to control ownership, trust tiers, canonical
identifiers, implementation language, configuration format, offline artifact
flow, and recovery authority.
