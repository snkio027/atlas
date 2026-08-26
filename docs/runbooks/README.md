# Atlas Runbooks

Runbooks describe approved operation of implemented behavior; they do not grant
additional authority or replace architecture.

- [Bootstrap and GitOps handoff](bootstrap.md)
- [Phase-0 recovery authority](recovery-phase0.md) — `COMPLETE / FROZEN`

Break-glass Root repair and destructive cluster teardown are not normal
Bootstrap operations. The Phase-0 runbook records the verified Human-gated
runtime closure and the frozen drill surface. GitOps Phase 1A definition work
is authorized to start, but documentation does not authorize execution,
teardown, credential issuance, GitOps wiring, RBAC or Admission activation, or
recovery operations.
