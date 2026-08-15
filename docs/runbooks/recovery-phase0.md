# Phase-0 Admission Escape Preparation

This runbook covers the audited Kind definition and the isolated drill-cluster
lifecycle implemented for ADR-0003 Phase 0. It does not declare Phase 0
complete. Reading this runbook, merging the implementation, or running Quality
does not authorize cluster creation, credential issuance, RBAC or Admission
activation, canary mutation, recovery execution, teardown, or Tier-0 changes.

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

## Drill-cluster lifecycle boundary

The rendered file remains an offline definition. Cluster creation is a separate
operation exposed only through:

```text
bootstrap/drill/atlas-kind-drill create
```

It is intentionally absent from `Taskfile.yaml`. Before an authorized ceremony,
the operator prepares four disjoint paths on encrypted local storage:

- an empty audit directory named exactly after the unique drill cluster;
- a new kubeconfig path whose parent is owner-only;
- an existing evidence root; and
- the repository itself at a clean, reviewed commit.

The audit directory, evidence root, and kubeconfig parent must be owned by the
current UID, mode `0700`, non-symlink, and free of extended ACLs. The operator
must explicitly attest `encrypted-owner-controlled` storage. An execution shape
is:

```bash
./bootstrap/drill/atlas-kind-drill create \
  --cluster-name atlas-recovery-drill-YYYYMMDDtHHMMSSz-0123abcd \
  --context kind-atlas-recovery-drill-YYYYMMDDtHHMMSSz-0123abcd \
  --kubeconfig /absolute/owner-only/atlas-recovery-drill-YYYYMMDDtHHMMSSz-0123abcd.kubeconfig \
  --audit-dir /absolute/encrypted/atlas-recovery-drill-YYYYMMDDtHHMMSSz-0123abcd \
  --evidence-root /absolute/encrypted/atlas-recovery-evidence \
  --storage-assertion encrypted-owner-controlled
```

Do not run this example without a separate cluster-lifecycle authorization. The
command rejects inherited `KIND_*` topology variables and `DOCKER_HOST`, fixes
Kind to the Docker provider, and verifies the supported OrbStack endpoint. It
never reads or changes the default Kubernetes context.

Before the interactive challenge, the command acquires a dedicated host lock,
snapshots the audit policy into the evidence session, renders the final Kind
configuration against that snapshot, and writes `plan.json`, `plan.sha256`,
`pre-mutation.sha256`, ambient kubeconfig hashes, and a hash-chained
`journal.jsonl`. The challenge binds:

- actor, UTC time, action ID, cluster, context, and all explicit paths;
- Git commit and tree;
- Kind configuration, policy, and `versions.lock` SHA-256 values;
- the digest-pinned node image; and
- the encrypted-storage assertion and complete plan hash.

Every authority input and managed path is revalidated after approval and before
`kind create`. The policy mounted into the node is the owner-only snapshot, not
the live working-tree file. Successful verification requires current-UID,
non-symlink kubeconfig and audit-log files with mode `0600`, the approved policy
hash inside the node, an exact context, a Ready node, and a recorded `/readyz`
audit event.

Cluster lifecycle approval is not reusable for later Phase-0 gates. The
following remain separately reviewed and unauthorized:

- Recovery Operator and Session Authorizer credential ceremonies;
- persistent Escape and canary-scoped Session Authorizer RBAC;
- disposable protection and recovery-authorization canaries; and
- canary suspend/restore and Fence/Permission Bundle exercises.

Production Session Authorizer RBAC and production `Deny` remain forbidden in
Phase 0. No file introduced by this foundation is referenced by the External
Root or another live Kustomization.

## Retained-state handling

Kind is always invoked with `--retain`. A create or verification failure keeps
the evidence session, policy snapshot, exact Kind configuration, plan, journal,
audit directory, generated kubeconfig, and any discovered node names. The
journal records the observable retained-state inventory.

After failure:

1. do not rerun the command with the same or a different path set;
2. do not reuse, overwrite, or implicitly delete the cluster or kubeconfig;
3. preserve the evidence and audit roots without changing owner, mode, ACL, or
   file contents; and
4. request a separate human review for diagnosis and eventual teardown.

The lifecycle command has no delete or stale-lock recovery operation. An
interrupted process may leave its owner-only host lock; that is a deliberate
fail-closed signal and requires a separately reviewed disposition. This runbook
does not grant teardown or lock-removal authority.

## Verification

```bash
task quality
```

Quality verifies deterministic rendering, path confinement, first-match audit
semantics, YAML-printable UTF-8 validation, owner-only custody, provider
pinning, lifecycle locking, plan-bound approval, post-Gate hash revalidation,
journaling, retained-state evidence, physical isolation from normal Bootstrap,
and a fully mocked create/verify path. It does not run Kind and does not replace
the separately authorized macOS/OrbStack runtime drill required by ADR-0003.
