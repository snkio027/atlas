# Atlas Runbooks

Runbooks describe approved operation of implemented behavior; they do not grant
additional authority or replace architecture.

- [Bootstrap and GitOps handoff](bootstrap.md)
- [Phase-0 admission escape preparation](recovery-phase0.md)

Break-glass Root repair and destructive cluster teardown are not normal
Bootstrap operations. The Phase-0 runbook documents non-mutating definition
rendering, the Human-gated drill-cluster lifecycle, and the separately gated
runtime canary ceremony. Documentation does not authorize execution, teardown,
credential issuance, RBAC or Admission activation, or recovery operations.
