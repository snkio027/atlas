# Phase-0 Recovery Authority Preparation

This runbook covers the audited Kind definition, isolated drill-cluster
lifecycle, and canary-only runtime ceremony implemented for ADR-0003 Phase 0.
It does not declare Phase 0 complete. Reading this runbook, merging the
implementation, or running Quality does not authorize cluster creation,
credential issuance, RBAC or Admission activation, canary mutation, recovery
execution, teardown, or Tier-0 changes. A real ceremony requires its own exact
Human Judgment Gate after the code has been reviewed and merged.

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

## Render the admission escape canary definitions

The first canary definition bundle is intentionally limited to the Recovery
Operator admission-escape boundary. Supply the planned exact X.509 username;
the command accepts only the ADR-0003 form containing a lowercase
`kube-system` Namespace UID and a positive generation:

```bash
recovery_operator=atlas:break-glass:00000000-0000-0000-0000-000000000000:g1
./bootstrap/recovery/atlas-recovery phase0 admission-canary-manifests \
  --recovery-operator "$recovery_operator" \
  > .state/recovery-phase0-admission-canary.yaml
```

Rendering is deterministic and performs no API call. The five-document bundle
contains:

- one inert `kube-system` ConfigMap canary fixture;
- the `atlas-bootstrap-admission-escape-canary` Policy and Binding using
  `failurePolicy: Fail` and the semantic action set `{Audit, Deny}`;
- the canonical Escape ClusterRole restricted to exact-name Policy, Binding,
  and `kube-system` Namespace inspection plus exact-name Binding
  `patch`/`update`; and
- one exact-user ClusterRoleBinding with no group subject.

The Policy denies matched fixture mutation by every username except the exact
rendered Recovery Operator. The Escape role cannot create or delete resources,
read Secrets or ConfigMaps in any Namespace, mutate the canary fixture, or
target production evidence. Fixture reads are deliberately absent rather than
granted cluster-wide; any future namespaced read requires a separately reviewed
`Role` and `RoleBinding` decision. Standing mutation authority is limited to
suspending and restoring the canary Binding; the runtime ceremony constrains
that operation to the exact UID/resourceVersion and raw `validationActions`
JSON Patch defined by ADR-0003.

This render command does not authorize applying the bundle. Activation requires an
audited disposable target, a separately issued and escrowed Recovery Operator
credential, an authority-specific Human Judgment Gate, server-side CEL type
checking, effective-permission probes, and retained audit evidence. The current
runtime ceremony implements those actions only for the reviewed canary names on
the disposable target described below. Production Session Authorization and
production `Deny` remain absent.

## Render the Session Authorization canary definitions

The second definition bundle models only the `kube-system` canary authority
needed to prove Fence-gated temporary permission. Supply the independently
planned principals. Both usernames must contain the same lowercase
`kube-system` Namespace UID and positive, independently rotatable generations:

```bash
namespace_uid=00000000-0000-0000-0000-000000000000
recovery_operator="atlas:break-glass:${namespace_uid}:g1"
session_authorizer="atlas:recovery-authorizer:${namespace_uid}:g1"
./bootstrap/recovery/atlas-recovery phase0 \
  session-authorization-canary-manifests \
  --recovery-operator "$recovery_operator" \
  --session-authorizer "$session_authorizer" \
  > .state/recovery-phase0-session-authorization-canary.yaml
```

The deterministic twelve-document bundle closes the Phase-0 definition set and
contains:

- a `kube-system` permission `Role` that can only `get` the exact inert
  admission-escape fixture and `get`/`patch`/`update` the exact inert Guard
  fixture when a temporary Binding exists;
- a separate `kube-system` Session Authorizer `Role` and exact-user
  `RoleBinding` for canary Fence and temporary RoleBinding lifecycle plus
  exact `bind` on that permission Role;
- parameter-free Fence and Binding Shape Policy/Binding pairs; and
- a Permission Policy/Binding parameterized only by
  `kube-system/atlas-bootstrap-operation-fence-canary`, with
  `parameterNotFoundAction: Deny` and `failurePolicy: Fail`; and
- an inert Guard ConfigMap plus a parameter-free Guard Policy/Binding pair that
  permits only the exact Recovery Operator to add or remove the canonical
  freeze rule without changing the remaining fixture projection.

Kubernetes RBAC cannot constrain collection `create` by object name. The Fence
Policy therefore matches every ConfigMap mutation attempted by the exact
Session Authorizer as well as every mutation of the canonical canary Fence.
The Binding Shape Policy similarly matches every RoleBinding mutation attempted
by that principal, preventing omission of the session selector. Permission
authorization compares the temporary RoleBinding's session, plan, target,
revision, Fence UID, roleRef, and Recovery Operator subject to the Fence.

No Fence ConfigMap or temporary Recovery Operator RoleBinding is rendered, so
the fixture and Guard permissions have no standing subject. No ClusterRole or
ClusterRoleBinding is present, and same-named ConfigMaps in other Namespaces
remain unreadable. Every authorization Policy uses `failurePolicy: Fail`; every
Binding uses the canonical action set `[Audit, Deny]`. Only Permission
authorization is parameterized. The other three controls remain independent of
Fence readability so a missing Fence cannot bypass Fence, Binding Shape, or
Guard enforcement.

The static authorization routing contract is:

| Request | Fence | Binding Shape | Permission | Guard | Definition result |
| --- | --- | --- | --- | --- | --- |
| unrelated ConfigMap or RBAC request | skip | skip | skip | skip | unaffected by recovery definitions |
| Session Authorizer creates the exact Fence | evaluate | skip | skip | skip | eligible only for the exact Fence projection |
| Session Authorizer creates another ConfigMap | deny | skip | skip | skip | denied |
| Session Authorizer creates an exact temporary Binding while Fence is absent | skip | evaluate | deny missing parameter | skip | denied |
| UPDATE removes a recovery-session label | skip | evaluate old and new object | evaluate through old-object selector | skip | denied on shape or lineage mismatch |
| Recovery Operator adds or removes the exact Guard value | skip | skip | skip | evaluate | eligible only with separate temporary RBAC and exact projection |
| ordinary principal changes or removes the Guard | skip | skip | skip | deny | denied |
| Recovery Operator performs a Guard no-op or deletes the selected fixture | skip | skip | skip | deny | denied |

“Eligible” describes Admission only; Kubernetes RBAC must independently grant
the exact operation. Complete normalized projections and the routing matrix are
checked by Quality. API-server CEL type checking, positive and negative
permission probes, resource installation, Fence creation, temporary
RoleBinding creation, cleanup, and evidence collection occur only inside the
separately authorized runtime ceremony.

This render command performs no API call and is not an activation mechanism.
The definitions are absent from all Kustomizations and must not be applied to
the existing four-node normal Bootstrap cluster.

## Runtime canary ceremony

The runtime command operates only on a disposable cluster previously created
by `bootstrap/drill/atlas-kind-drill create`. It does not create, reuse, or
delete a cluster. Prepare four existing, mutually disjoint, current-UID-owned
directories with mode `0700` and no extended ACL:

- the cluster's audit directory;
- the exact cluster-creation evidence session containing its sealed plan,
  policy, Kind configuration, versions snapshot, pre-mutation manifest, and
  hash-chained `VERIFY/READY` journal;
- a separate runtime evidence root; and
- an empty short-lived credential directory.

All four directories must be outside the repository, filesystem root, and
shared temporary directories. The isolated admin kubeconfig must be a regular
mode-`0600` file named `<cluster-name>.kubeconfig`; its parent must also be
owner-only. The runtime evidence root must not contain the creation evidence.
Use the reviewed, clean commit that contains the ceremony implementation as the
known-good revision:

```bash
cluster=atlas-recovery-drill-YYYYMMDDtHHMMSSz-0123abcd
reviewed_revision=0000000000000000000000000000000000000000

./bootstrap/recovery/atlas-recovery phase0 canary-drill \
  --cluster-name "$cluster" \
  --context "kind-${cluster}" \
  --admin-kubeconfig "/absolute/owner-only/${cluster}.kubeconfig" \
  --audit-dir "/absolute/encrypted/audit/${cluster}" \
  --creation-evidence "/absolute/encrypted/creation-evidence/session" \
  --evidence-root "/absolute/encrypted/runtime-evidence" \
  --credential-dir "/absolute/encrypted/runtime-credentials" \
  --storage-assertion encrypted-owner-controlled \
  --known-good-revision "$reviewed_revision"
```

The placeholder revision deliberately cannot authorize a real run. Before the
interactive prompt, the command proves a clean exact Git authority, validates
the complete cluster-creation evidence chain, matches its audit-policy snapshot
to the reviewed revision, verifies the isolated single Ready node and loopback
API endpoint, confirms Kubernetes and tool versions, checks that every canary
object is absent, and binds the rendered bundles plus all authority inputs into
an owner-only plan and pre-mutation hash. It then requires the exact, displayed
`RUN PHASE0 <cluster> <approval-sha>` challenge and revalidates every approved
input before the first CSR is created.

An approved ceremony executes this fixed sequence:

1. issue two one-hour, exact-username client certificates with no Organization,
   verify their subjects and key pairs, and delete each CSR with UID and
   resourceVersion preconditions;
2. install the 17 canary definitions and require all five Policies to complete
   API-server CEL type checking without warnings;
3. prove ordinary mutation denial, suspend only the canary admission Binding to
   `{Audit}`, exercise and restore the fixture, restore `{Audit, Deny}`, and
   prove the canonical Binding projection plus denial again;
4. exercise positive and negative Fence, Binding Shape, Permission, and Guard
   paths, including missing-Fence and wrong-lineage denial;
5. delete the temporary permission Binding before the Fence, then delete every
   canary definition using captured UID/resourceVersion preconditions;
6. prove both credentials retain no mutation grants, remove their local key,
   certificate, CSR, kubeconfig, and CA files, and seal the audit copy, result,
   journal tip, and bundle hashes.

Success retains the disposable cluster but leaves no canary resource or local
credential material. Failure stops immediately and intentionally retains the
observable cluster objects, credential directory, audit log, and evidence
session for human review; there is no automatic rollback, retry, production
fallback, or teardown path. Any retained-state disposition requires a new
authorization.

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
must explicitly attest `encrypted-owner-controlled` storage. The evidence root
must not be located below a shared temporary root, including `/tmp`,
`/private/tmp`, `/var/tmp`, `/private/var/tmp`, `/dev/shm`, or `/usr/tmp`.
An execution shape is:

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
command rejects inherited `KIND_*` and `DOCKER_*` variables. Every Docker and
Kind call explicitly selects the `orbstack` context; its exact socket endpoint
is included in the plan and revalidated after approval. The command never reads
or changes the default Kubernetes context.

Before the interactive challenge, the command acquires a dedicated host lock,
snapshots the audit policy into the evidence session, renders the final Kind
configuration against that snapshot, snapshots `versions.lock`, and writes
`plan.json`, `plan.sha256`, `pre-mutation.sha256`,
`pre-mutation-manifest.sha256`, ambient kubeconfig hashes, and a hash-chained
`journal.jsonl`. The challenge binds:

- actor, UTC time, action ID, cluster, context, and all explicit paths;
- Git commit and tree;
- Kind configuration, policy, and `versions.lock` SHA-256 values;
- the OrbStack Docker context and endpoint;
- the digest-pinned node image; and
- the encrypted-storage assertion, complete plan hash, pre-mutation manifest
  hash, and their combined approval hash.

Every authority input and managed path is revalidated after approval and before
`kind create`. The policy mounted into the node is the owner-only snapshot, not
the live working-tree file. Git inspection clears repository, index, namespace,
replacement-object, and configuration environment overrides and requires the
reported top-level to equal the canonical Atlas root. It disables fsmonitor and
ignore-stat shortcuts and rejects sparse checkout plus every tracked entry with
`assume-unchanged` or `skip-worktree`. Successful verification requires
current-UID,
non-symlink kubeconfig and audit-log files with mode `0600`, the approved policy
hash inside the node, an exact context, a Ready node, and a recorded `/readyz`
audit event.

Cluster lifecycle approval is not reusable for the runtime ceremony. The
runtime command always creates a new plan-bound Human Gate. Its canary-only
credentials, RBAC, Admission, suspend/restore, Fence, and Permission exercise
remain unauthorized until that exact challenge is independently approved.

Production Session Authorizer RBAC and production `Deny` remain forbidden in
Phase 0. No file introduced by this foundation is referenced by the External
Root or another live Kustomization.

## Retained-state handling

Kind is always invoked with `--retain`. A create or verification failure keeps
the evidence session, policy snapshot, exact Kind configuration, plan, journal,
audit directory, generated kubeconfig, and any discovered node names. The
journal records the observable retained-state inventory.

A runtime ceremony failure follows the same preserve-first rule. Do not delete
remaining canary objects, CSR-derived credential files, the runtime journal, or
the audit log. The command has no retry, resume, implicit cleanup, or cluster
delete mode; cleanup after a failed runtime ceremony requires a separate plan
and Human Judgment Gate.

After failure:

1. do not rerun the command with the same or a different path set;
2. do not reuse, overwrite, or implicitly delete the cluster or kubeconfig;
3. preserve the evidence and audit roots without changing owner, mode, ACL, or
   file contents; and
4. request a separate human review for diagnosis and eventual teardown.

The lifecycle command has no delete or stale-lock recovery operation. Its
per-UID lock root is deterministic and does not depend on caller `TMPDIR`.
An interrupted process may leave its owner-only host lock; that is a deliberate
fail-closed signal and requires a separately reviewed disposition. This runbook
does not grant teardown or lock-removal authority.

## Verification

```bash
task quality
```

Quality verifies deterministic rendering, path confinement, first-match audit
semantics, YAML-printable UTF-8 validation, owner-only custody, provider
pinning, lifecycle locking, plan-bound approval, post-Gate hash revalidation,
journaling with independently recomputed entry digests, retained-state evidence,
physical isolation from normal Bootstrap, a fully mocked create/verify path,
and the runtime ceremony's exact inventory, projections, probe ordering, cleanup
preconditions, and credential revocation contract. It does not run Kind or
contact a cluster and does not replace the separately authorized macOS/OrbStack
runtime ceremony required by ADR-0003.
