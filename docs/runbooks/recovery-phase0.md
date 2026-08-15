# Phase-0 Admission Escape Preparation

This runbook covers only the non-mutating foundation currently implemented for
ADR-0003 Phase 0. It does not declare Phase 0 complete and does not authorize
credential issuance, RBAC or Admission activation, Kind cluster creation,
canary mutation, recovery execution, or Tier-0 changes.

## Render the audited drill-cluster configuration

Choose an existing, owner-controlled, writable directory on encrypted storage.
The directory must be outside the repository and must not itself be a symlink.
The command validates that boundary and writes the Kind configuration to
standard output:

```bash
audit_destination=/absolute/encrypted/path/atlas-recovery-audit
./bootstrap/recovery/atlas-recovery phase0 audit-config \
  --audit-dir "$audit_destination" > .state/recovery-phase0-kind.yaml
```

Rendering is deterministic for the same canonical destination. It does not
read a kubeconfig, use a default Kubernetes context, invoke `kind`, or contact
a cluster.

The generated configuration mounts:

- `clusters/kind/recovery-audit-policy.yaml` read-only into the Kind node;
- the external audit destination as the writable API audit-log directory; and
- both paths into the kube-apiserver static Pod through kubeadm.

The audit policy captures request and response bodies only for the recovery,
adoption, RBAC, Admission, and Argo control objects needed by ADR-0003.
Secrets, ServiceAccount token requests, TokenReviews, and CSRs are matched
first at `Metadata` level; the catch-all is also `Metadata`.

Kubernetes selects the audit rule from request attributes. A collection
`CREATE` does not provide the new ConfigMap name at that decision point, so all
ConfigMap creates in `argocd` and `kube-system` remain `Metadata` rather than
risk capturing an unrelated sensitive body. Named follow-up reads and later
mutations of the canonical recovery objects use `RequestResponse`; the future
evidence journal must bind that read to the CREATE response UID and
resourceVersion.

## Human-gated boundary

The rendered file is an offline definition, not permission to create or alter
a cluster. A later Phase-0 change must separately review and gate all of the
following before they become runtime state:

- creation of a uniquely named disposable Kind drill cluster with the locked
  node image;
- Recovery Operator and Session Authorizer credential ceremonies;
- persistent Escape and canary-scoped Session Authorizer RBAC;
- disposable protection and recovery-authorization canaries; and
- canary suspend/restore and Fence/Permission Bundle exercises.

Production Session Authorizer RBAC and production `Deny` remain forbidden in
Phase 0. No file introduced by this foundation is referenced by the External
Root or another live Kustomization.

## Verification

```bash
task quality
```

Quality verifies deterministic rendering, path confinement, first-match audit
semantics, YAML-printable UTF-8 validation under C and UTF-8 locales, physical
isolation from normal Bootstrap, and the absence of cluster mutation commands.
It does not replace the macOS/OrbStack runtime drill required by ADR-0003.
