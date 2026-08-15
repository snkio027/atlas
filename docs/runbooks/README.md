# Atlas Runbooks

Runbooks describe approved operation of implemented behavior; they do not grant
additional authority or replace architecture.

- [Bootstrap and GitOps handoff](bootstrap.md)
- [Phase-0 admission escape preparation](recovery-phase0.md)

Break-glass Root repair and destructive cluster teardown are not normal
Bootstrap operations. The Phase-0 runbook documents both non-mutating audit
rendering and the separately Human-gated drill-cluster lifecycle. Documentation
does not authorize execution, teardown, credentials, RBAC, Admission, or
recovery operations.
