# Architecture Decision Records

ADRs record decisions that change or clarify Atlas architecture. They do not
silently override the frozen standards; a conflicting proposal must explicitly
identify and update the higher authority through its review process.

## Index

| ADR | Status | Decision |
| --- | --- | --- |
| [0001](0001-retain-shell-bootstrap.md) | Accepted | Retain Shell as the Bootstrap Stage-1 implementation |

## Lifecycle

Use `0000-template.md`. Valid statuses are Proposed, Accepted, Rejected,
Deprecated, and Superseded. A superseding ADR must link both directions and
state the migration and rollback boundary.

An ADR is required for changes to control ownership, trust tiers, canonical
identifiers, implementation language, configuration format, offline artifact
flow, and recovery authority.
