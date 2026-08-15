# ADR-0003: Define Bootstrap break-glass recovery and admission escape

- Status: Proposed
- Date: 2026-08-15
- Deciders: repository owner and required CODEOWNERS
- Clarifies: ADR-0002 evidence protection and recovery authority
- Supersedes: none
- Superseded by: none

## Context

Atlas Architecture v1.0.2 requires Bootstrap to terminate after GitOps
adoption, requires human judgment for break-glass operations, and defines the
recovery order as "Freeze Outside-In, Recover Inside-Out." GitOps Standard
v1.0.3 requires a snapshot before mutation, an adoption-compatible Recovery
Seed, repair to a known-good Git revision, and health-gated inside-out resume.

ADR-0002 defines a monotonic adoption proof and deliberately removes recovery
authority from ordinary `bootstrap/atlas apply`. It also makes an accepted and
implemented break-glass design, admission escape, audit path, and recovery
drill release prerequisites for receipt-aware enforcement.

The current implementation has no such recovery command or authority model.
It also has an External Root and child control Applications that follow the
mutable `main` revision. A GitOps manifest referenced by a reachable
Kustomization may therefore become live after merge. Resource definition and
runtime activation must not share a change set.

There is also an upstream constraint that narrows ADR-0002's protection
mechanism. Kubernetes intentionally prevents a `ValidatingAdmissionPolicy`
from matching `ValidatingAdmissionPolicy` and
`ValidatingAdmissionPolicyBinding` resources, so a VAP cannot protect itself or
its Binding. Atlas must protect those two resources with Kubernetes RBAC, Git
prune controls, exact-object checks, and human-gated recovery rather than claim
VAP self-protection. The VAP still protects the Identity, Signal, Receipt,
Recovery Session, and their namespaces.

Relevant upstream semantics are:

- [Validating Admission Policy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/)
- [ValidatingAdmissionPolicy API](https://kubernetes.io/docs/reference/kubernetes-api/admissionregistration/validating-admission-policy-v1/)
- [ValidatingAdmissionPolicyBinding API](https://kubernetes.io/docs/reference/kubernetes-api/admissionregistration/validating-admission-policy-binding-v1/)
- [Kubernetes X.509 authentication](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#x509-client-certs)
- [Kubernetes CertificateSigningRequest](https://kubernetes.io/docs/reference/access-authn-authz/certificate-signing-requests/)

The phase statement remains:

> ADR-0002 is accepted, but the INV-02 runtime gap is not fixed. Until Phase 4
> and the complete recovery drill pass, monotonic adoption proof is decided but
> not implemented.

This ADR is a governance proposal only. While it remains `Proposed`, it does
not authorize admission activation, credential issuance, recovery execution,
Tier-0 mutation, legacy migration, Receipt reissue, or receipt-aware release.

## Decision

Atlas will use a separately authenticated, session-scoped, fail-closed
break-glass workflow. Recovery authority is never inferred from cluster damage
and is never added to ordinary `apply`.

### Authority invariants

The following rules apply to every recovery:

1. Git remains the definition authority. Live repair is temporary and must
   converge to an explicitly approved Git revision.
2. `bootstrap/atlas` never imports, dispatches, or accepts recovery behavior.
3. A recovery cannot create a cluster, delete a cluster, change the local
   Registry, or perform workload/data recovery.
4. Recovery requires an existing Atlas cluster whose substrate fingerprint and
   Identity can be read and matched exactly.
5. Read-only inspection does not grant mutation authority.
6. Every mutation is bound to one Recovery Session, one cluster fingerprint,
   one known-good Git commit, one plan hash, and one principal generation.
7. Any API, evidence, authorization, hash, or precondition uncertainty stops
   the workflow before the next mutation.
8. Recovery never fabricates an old Kubernetes UID, restores an object from a
   raw backup, or treats health as ownership proof.
9. No automatic rollback crosses an ownership or trust boundary.
10. The External Root resumes normal reconciliation last.

### Cluster target and principal identity

The immutable recovery target fingerprint is the tuple:

```text
apiServerURL
kubeSystemNamespaceUID
apiServerCASPKISHA256
repositoryURL
environmentName
```

The `kube-system` Namespace UID is the substrate cluster identifier. It is used
instead of the Bootstrap Identity UID because Identity v1 to v2 migration must
not change the recovery target.

The canonical X.509 principal username is:

```text
atlas:break-glass:<kubeSystemNamespaceUID>:g<generation>
```

Its only organization is:

```text
atlas:break-glass
```

Neither value may use `system:masters`, `system:*`, a human username, or a
normal automation identity. Admission exceptions match the complete username,
including generation. A group-only exception is forbidden.

The canonical recovery resources are:

| Resource | Name |
| --- | --- |
| Protection ValidatingAdmissionPolicy | `atlas-bootstrap-evidence-protection` |
| Protection ValidatingAdmissionPolicyBinding | `atlas-bootstrap-evidence-protection` |
| Escape ClusterRole | `atlas-bootstrap-break-glass-escape` |
| Escape ClusterRoleBinding | `atlas-bootstrap-break-glass-escape` |
| Recovery ClusterRole | `atlas-bootstrap-break-glass-recovery` |
| Temporary Recovery ClusterRoleBinding | `atlas-bg-recovery-<sessionID>` |
| Canary Policy and Binding | `atlas-bootstrap-admission-escape-canary` |
| Recovery Session ConfigMap | `atlas-bootstrap-recovery-session` |

The temporary Binding carries
`atlas.io/recovery-session=<sessionID>`. Canonical names or labels cannot change
without an accepted superseding ADR because Phase-4 Bootstrap uses them as
authority inputs. `sessionID` is exactly 32 lowercase hexadecimal characters
generated from 128 bits of cryptographically secure randomness.

### Principal credential lifecycle

#### Generation

The reference principal is an X.509 client certificate issued through a
manually reviewed `certificates.k8s.io/v1` CSR using signer
`kubernetes.io/kube-apiserver-client`.

- The private key is generated outside the cluster and never leaves encrypted
  operator-controlled custody except for an approved recovery session.
- The CSR requests only `digital signature` and `client auth` usages.
- The requested and accepted lifetime must not exceed 30 days.
- The CSR subject must match the exact username and organization above.
- CSR approval, certificate extraction, fingerprinting, and CSR cleanup are
  journaled as one credential ceremony.
- The cluster CA private key is never exported or copied into the recovery
  package.
- The certificate is tested only against the exact cluster fingerprint.

Kubernetes does not support X.509 client certificate revocation. Atlas
therefore treats certificate expiry as cryptographic expiry and RBAC plus the
generation-specific admission exception as effective revocation.

#### Authorization tiers

Authentication does not imply standing recovery authority. Authorization is
split into two tiers.

`Escape` is the persistent, minimum Phase-0 capability. A narrowly scoped
ClusterRoleBinding for the exact principal generation permits:

- read of the canonical protection Policy and Binding;
- read of the target fingerprint and non-secret recovery evidence metadata;
- `patch` and `update` of only the canonical protection Binding by
  `resourceNames`;
- read-only canary verification.

It grants no wildcard resource, verb, API group, namespace, impersonation,
Secret read, CSR approval, Application mutation, Seed mutation, evidence
rewrite, or Tier-0 mutation.

`Recovery` is a temporary, session-scoped authorization created only after a
Human Judgment Gate. Its reviewed allow-list contains only the API groups,
kinds, namespaces, and verbs required by the approved Recovery Plan. It is
bound to the same exact principal generation and is removed before the session
can close. Wildcard verbs, wildcard resources, `impersonate`, and
`system:masters` are forbidden.

The temporary ClusterRoleBinding has a canonical session label and exact name
recorded in the plan. Its presence is itself a recovery lock. It grants the
principal permission to create and delete the canonical Recovery Session and
to delete only that same temporary Binding as the final self-revocation step.
It grants no authority to create, update, or bind other RBAC objects.

The Recovery role may include narrowly reviewed permissions needed to:

- freeze and resume the named Argo CD control Applications;
- restore the adoption-compatible Seed and its required cluster-scoped
  resources;
- validate or restore the canonical AppProject and External Root;
- read protected adoption evidence;
- create and delete the Recovery Session record;
- reissue the canonical Receipt under the protocol in this ADR.

Permissions are derived from the known-good recovery render and checked before
use. They do not extend to tenant payloads, data-plane objects, unrelated
namespaces, cluster lifecycle, or Secret payload export.

#### Custody

The credential package contains only the client certificate, encrypted private
key, API endpoint, cluster CA certificate, context name, certificate serial,
generation, validity interval, and target fingerprint. It must not contain a
cluster-admin credential.

The package:

- is never committed to Git, stored in a Kubernetes Secret, placed under
  repository `.state/`, passed in an environment variable, or written to shell
  history;
- is escrowed in encrypted storage independent of Argo CD and the protected
  namespaces;
- has at least two independently recoverable encrypted copies for production;
- records custodians, checksum, certificate serial, generation, issue time,
  expiry, and the test evidence from the credential ceremony;
- is materialized only on encrypted local storage with owner-only permissions
  for the duration of a session.

Production release uses separation between the operator and at least one
credential custodian or approving owner. A disposable non-production drill may
use one repository owner only when that exception is explicit in the drill
record.

#### Rotation and revocation

Rotation occurs before two-thirds of the certificate lifetime has elapsed and
immediately after any recovery use, custody breach, failed custody audit, or
operator departure.

Rotation is ordered:

1. issue the next generation certificate;
2. install its exact narrow Escape RBAC subject;
3. add the next exact username to the admission exception;
4. exercise canary suspend and restore with the next generation;
5. remove the previous Escape and any Recovery bindings;
6. remove the previous username from the admission exception;
7. archive the rotation evidence and destroy accessible copies of the old key.

Revocation starts with steps 5 and 6 and does not wait for certificate expiry.
An old certificate may remain cryptographically authenticated until expiry, but
it must be unauthorized by RBAC and excluded from the admission bypass. Failure
to complete either revocation fence is `AUTHORITY_DEGRADED`; production Deny or
recovery must not proceed.

### Physical recovery-command isolation

The future recovery entry point is a separate executable:

```text
bootstrap/recovery/atlas-recovery
```

It is not a subcommand, flag, sourced module, or code path of
`bootstrap/atlas`. The normal CLI must reject recovery flags. Recovery code may
reuse read-only configuration parsing, hashing, logging, and rendering helpers,
but it must not call `cluster::ensure_kind`, `registry::ensure_local`, normal
`argocd::handoff`, or normal `apply`.

The interface separates read-only planning from mutation:

```text
atlas-recovery inspect
atlas-recovery plan
atlas-recovery admission canary-suspend
atlas-recovery admission canary-restore
atlas-recovery execute
atlas-recovery resume
atlas-recovery close
```

`inspect` and `plan` use a read-only kubeconfig. Every other command requires a
dedicated break-glass kubeconfig passed by an explicit absolute, non-symlink
path. The command never falls back to the current context, default kubeconfig,
or environment-provided credentials.

`plan` produces an immutable plan description and SHA-256 digest before the
first mutation. `execute`, `resume`, and `close` require exact confirmation of:

- cluster fingerprint;
- Recovery Session ID;
- known-good full Git commit SHA;
- plan SHA-256;
- action-specific Human Judgment challenge.

The production command requires a TTY for each Human Judgment Gate. A reusable
boolean approval flag is not sufficient. A future signed non-interactive
approval format requires a separate accepted governance decision.

### Admission protection and escape contract

#### Protection boundary clarification

The production VAP protects unauthorized create, update, or deletion, as
applicable, of:

- `atlas-bootstrap-identity`;
- `atlas-bootstrap-adoption-signal`;
- `atlas-bootstrap-adoption-receipt`;
- `atlas-bootstrap-recovery-session` when present;
- the `argocd` and `kube-system` Namespaces when deletion would remove protected
  records.

The VAP and its Binding cannot match themselves under the Kubernetes API.
Their protection is instead the combination of:

- exact, least-privilege Kubernetes RBAC;
- Argo CD `Prune=confirm` and `Delete=false` protection;
- no cascading deletion finalizer;
- separation of definition and activation PRs;
- exact UID, resourceVersion, desired-projection, and hash checks;
- Human Judgment for every enforcement mutation;
- cluster API audit evidence.

Acceptance of this ADR overrides only ADR-0002's impossible VAP
self-protection mechanism. It does not weaken protection of adoption evidence,
namespaces, or the requirement that protection precede a valid Signal. The
acceptance commit must add a reciprocal clarification link to ADR-0002.

#### Enforcement states

The canonical admission states are:

| State | Policy | Binding | Normal mutation |
| --- | --- | --- | --- |
| `UNINSTALLED` | absent | absent | Phase 0/1A only; Signal invalid |
| `OBSERVING` | expected spec | `Audit` and optionally `Warn` | allowed and audited; Signal invalid |
| `ENFORCED` | `failurePolicy: Fail` and expected spec | `Deny` + `Audit` | denied; Signal may become valid |
| `SUSPENDED` | unchanged expected spec | `Audit` only | recovery session only |
| `DRIFTED` | readable but unexpected | any | deny recovery mutation until separately approved repair |
| `UNAVAILABLE` | unreadable or undecodable | unreadable or undecodable | deny all mutation |

`Deny` and `Warn` are never combined. `Audit` remains present in enforcing and
suspended states. A match condition excludes only the exact current
break-glass username. Kubernetes evaluates a policy as skipped when any match
condition is false, so the exception must be an independently tested,
minimal expression and must not depend on a parameter resource, Argo CD, or a
protected Namespace.

#### Suspend and exact restore

Routine escape suspends only the Binding. The Policy remains present, type
checked, and unchanged. Deleting either object, setting `failurePolicy: Ignore`,
or replacing the entire object is not a supported suspend operation.

Before suspend, the command saves raw API objects and a canonical desired
projection containing:

```text
apiVersion
kind
metadata.name
metadata.labels
metadata.annotations
spec
```

UID, resourceVersion, managedFields, timestamps, and status are retained in the
raw snapshot but excluded from the desired-projection hash.

Suspend uses a JSON Patch that tests the Binding UID, resourceVersion, and
exact enforcing `validationActions`, then replaces only
`validationActions: [Deny, Audit]` with `validationActions: [Audit]`. A
precondition mismatch is `DRIFTED` and performs no mutation.

Restore uses the current resourceVersion and the recorded suspended projection
as preconditions, restores the exact approved enforcing actions, and verifies:

- Policy and Binding desired-projection hashes;
- Policy type-check status;
- ordinary-principal denial;
- exact-principal exception;
- audit annotation emission;
- unchanged protected-object UIDs and contents unless the Recovery Plan
  explicitly authorized their reissue.

If the pre-suspend Policy or Binding was already drifted, its snapshot is
evidence, not a restore target. Repair requires a separate Human Judgment Gate
and the exact object projection rendered from the known-good revision. Restore
failure leaves the graph frozen, the Recovery Session present, and normal
reconciliation disabled.

Phase 0 proves this flow against a disposable canary Policy and Binding whose
targets are isolated from Atlas production evidence. No production `Deny`
Binding may exist before the canary has been suspended and restored by the
escrowed principal.

### Recovery Session and exclusivity

Every mutating recovery creates exactly one immutable ConfigMap before the
first control-plane mutation:

```text
kind: ConfigMap
namespace: kube-system
name: atlas-bootstrap-recovery-session
schema: atlas.io/bootstrap-recovery-session/v1
```

It records at least:

```text
sessionID=<32 lowercase hexadecimal characters from 128 random bits>
clusterFingerprintSHA256=<target fingerprint digest>
principal=<exact username and generation>
planSHA256=<approved plan digest>
knownGoodRevision=<full Git commit SHA>
startedAt=<operator workstation UTC audit time>
```

The record is a cluster-side lock, not a clock-based lease. It has no automatic
expiry. An interrupted session therefore cannot silently restore normal
mutation authority. A different session or principal must fail closed.

An independent authorizing administrator, not the recovery principal, installs
the exact temporary Recovery ClusterRoleBinding under the session-open gate
before this ConfigMap is created. The Binding is a lock as soon as it exists,
closing the authorization-before-lock race. Phase-4 normal Bootstrap must look
for both the canonical Session and any temporary Binding carrying the
recovery-session label before every mutation.
Presence of either means `RECOVERY_IN_PROGRESS` and denies normal mutation,
including Receipt creation. Only `resume` with the same session tuple may
continue.

Closure first deletes the exact Session while the temporary Binding still acts
as the lock, then self-deletes the exact temporary Binding. A crash between the
two operations therefore remains fail closed. Successful postflight,
enforcement restoration, an explicit close gate, and a complete evidence
bundle are preconditions for both deletions.

### Snapshot, hashes, and audit evidence

The operator must supply an absolute `--evidence-root`; there is no default.
It must be an existing, owner-only, encrypted, non-symlink directory outside
the repository, repository `.state/`, shared temporary directories, and the
cluster being recovered.

The canonical session path is:

```text
<evidence-root>/<cluster-fingerprint-sha256>/<UTC-start>-<session-id>/
```

The bundle contains:

```text
session.json
target.json
authorization/
preflight/
freeze/
admission/
seed/
git/
receipt/
resume/
postflight/
journal.jsonl
plan.json
plan.sha256
pre-mutation.sha256
bundle.sha256
result.json
```

Before mutation, the snapshot includes:

- cluster version, node readiness, target fingerprint, Identity, Signal,
  Receipt, and any existing Recovery Session;
- raw and canonical Policy and Binding objects plus type-check status;
- External Root, AppProjects, child control Applications, `argocd-self`, Argo
  CD workloads, CRDs, and controller configuration;
- current source revisions, sync policies, health, UIDs, resourceVersions, and
  Argo resource inventory;
- relevant Events and available Kubernetes API audit records;
- the known-good commit, tree ID, `versions.lock`, vendored artifact hashes,
  rendered Seed hash, and recovery tool commit;
- metadata and names of relevant Secrets, but never Secret data or token
  material.

Every file is SHA-256 hashed. `journal.jsonl` is append-only and hash chained;
each entry contains sequence, previous-entry hash, UTC time, action ID, actor,
object identity, before/after UID and resourceVersion where applicable,
request outcome, and artifact hashes. The pre-mutation manifest hash is copied
into the Human Judgment record before the first mutation. The final bundle hash
is copied to an external, access-controlled incident record or write-once
archive after closure.

The bundle must never contain a private key, kubeconfig `client-key-data`,
bearer token, Secret `data`/`stringData`, registry credentials, or decrypted
repository credentials. Redaction failure is fatal before mutation.

### Known-good Git revision

A branch, tag, `HEAD`, or symbolic `main` is never a recovery input. The
candidate is a full 40-character commit SHA from the canonical repository or
an approved offline mirror.

Before approval, the command creates an isolated clean worktree and proves:

- the object is a commit and its tree ID is recorded;
- it is reachable from an authenticated canonical reference or is accompanied
  by an approved offline provenance record;
- `task quality` passes at that exact commit;
- `versions.lock`, the vendored chart archive/tree, image digests, Root,
  AppProjects, and `argocd-self` are internally consistent;
- the Recovery Seed render is deterministic and adoption-compatible;
- the revision contains the recovery contracts required by the target phase;
- no uncommitted file or ambient environment override affects the render.

The plan records both the commit SHA and the hashes of recovery-critical paths.
Human approval confirms the exact commit, not merely that it is older or was
previously on `main`.

If `main` is unsafe, Atlas repairs Git forward through a reviewed commit or PR.
It does not force-push, move a tag, or use long-lived live edits as the final
state. Control Applications remain frozen and pinned to the approved commit
until the repaired `main` has passed required checks and its critical-path
hashes are confirmed.

### Recovery sequence

#### 1. Inspect and authorize the session

Read the target with a non-mutating identity. Refuse a missing, foreign, or
changed substrate fingerprint; unreadable Identity; existing foreign Recovery
Session; unavailable audit destination; or unverified principal generation.

Select the known-good revision, build the plan, capture the preflight snapshot,
verify redaction, anchor `pre-mutation.sha256`, and obtain the session-open
Human Judgment Gate. No mutation precedes these checks.

#### 2. Create the Recovery Session and freeze outside-in

The independent authorizing administrator installs the exact temporary
Recovery Binding under the session-open gate. The recovery principal then
creates the session record with create-only semantics. Presence of either
blocks normal mutation. `AlreadyExists` requires an exact re-read and match;
neither object is overwritten.

Freeze by resourceVersion-guarded JSON Patch without deleting Applications:

```text
External Root
  -> project-bootstrap / platform-control / workload-control
  -> argocd-self and affected control leaves
```

For every Application, snapshot and remove automated sync before moving inward.
A mismatch or patch failure stops the workflow with all already-frozen layers
left frozen. The recovery does not automatically restore a layer whose prior
revision is suspected unsafe.

#### 3. Pin the control graph and suspend admission when required

Pin the External Root and every recovery-relevant child source to the approved
full commit while automated sync remains disabled. This is necessary because
the current Git manifests otherwise follow `main` at more than one Application
level.

If the plan needs a mutation that the evidence policy would deny, suspend only
the canonical Binding to `Audit` using the exact protocol above. If no
protected object or Namespace mutation is needed, leave admission enforced.

#### 4. Restore an adoption-compatible Seed

Render only from the approved commit and locked artifacts. The Recovery Seed:

- matches `versions.lock` and the `argocd-self` desired version;
- contains no GitOps Signal, Receipt, Recovery Session, External Root, tenant
  resource, or unrelated platform component;
- uses Helm only as a renderer and creates no Helm release state;
- preserves existing evidence and refuses unplanned UID changes;
- applies only the reviewed Seed resource inventory with a session-specific
  field manager;
- waits for every Argo CD Seed control-plane workload and CRD required by the
  shared health contract.

Seed mutation has its own Human Judgment Gate. A partial apply is corrected
forward by `resume`; the command never deletes the Argo CD Namespace or rolls
back by applying an older unverified manifest.

#### 5. Restore GitOps ownership while still frozen

Validate or restore the exact `atlas-bootstrap` AppProject and External Root
from the approved revision. Existing drift is never overwritten without an
object-specific Tier-0 gate. Recreate only an absent or unrecoverable object
explicitly listed in the plan; otherwise patch the minimum fields with UID and
resourceVersion preconditions.

Materialize and manually sync the pinned `argocd-self` desired state before
other platform capabilities. Child automated sync remains disabled. Confirm
the controller inventory and the Signal object created by GitOps.

If admission was suspended, restore `ENFORCED` and pass all positive, negative,
type-check, and audit probes before classifying the Signal as protected or
continuing to Receipt work.

#### 6. Reissue the Receipt when bound UIDs changed

Receipt reissue follows the separate protocol below. It occurs only while the
Recovery Session exists, the graph remains frozen, admission is enforced, and
the Signal is valid and protected.

#### 7. Repair Git and resume inside-out

Repair `main` forward and verify it before removing temporary revision pins.
Resume one health-gated layer at a time:

```text
Seed
  -> argocd-self
  -> Management Capability
  -> Platform Capability DAG
  -> Platform Control
  -> Workload Control
  -> Tenant Workloads when separately authorized
  -> External Root normal reconciliation
```

Restoring each automated sync policy uses the preflight object and approved Git
projection as explicit inputs. Failure at any layer refreezes that layer and
everything outside it. External Root resume is a final, separate Human
Judgment Gate.

#### 8. Close the session

Capture postflight evidence, verify the Receipt and protected Signal, verify
admission enforcement, and verify the normal principal is denied.

Only then may a Human Judgment Gate authorize deletion of the exact Recovery
Session followed immediately by self-deletion of the exact temporary Recovery
Binding. The Binding remains the lock between those operations. Verify both are
absent with the read-only identity, rotate or revoke the used credential, then
finalize and externally anchor `bundle.sha256`. A failed close leaves at least
one lock visible and normal mutation denied.

### Receipt reissue protocol

Receipt reissue is permitted only when:

- the substrate fingerprint and Identity v2 UID are unchanged;
- the selected Profile and repository still match;
- a valid predecessor Receipt exists when the Recovery Plan is created and its
  exact raw object and hash are present in the pre-mutation evidence bundle;
- a planned recovery changed the Signal, External Root, or `argocd-self` UID;
- the replacement objects are rendered from the known-good revision;
- the replacement Signal is valid and protected under `ENFORCED` admission;
- the Recovery Session and Receipt-reissue Human Judgment Gate are active.

An Identity v2 UID change, foreign Identity, unverifiable predecessor Receipt,
or repository mismatch is not eligible for reissue under this ADR. It is
`AMBIGUOUS` and requires a separate migration or disaster decision.

The canonical Receipt remains create-once within each generation. Reissue is:

1. read and hash the predecessor Receipt;
2. verify its UID, resourceVersion, schema, Identity binding, and evidence
   bundle copy;
3. compute the complete successor payload before deletion;
4. delete the predecessor with UID and resourceVersion preconditions;
5. create the successor at the same canonical name with create-only semantics;
6. re-read and exactly validate the API response UID, resourceVersion, and
   payload;
7. verify that ordinary principals cannot update or delete it.

The successor retains ADR-0002 fields and adds at least the fields below. The
initial Receipt is generation 1, so the first reissue is generation 2.

```text
generation=<predecessor generation + 1>
predecessorReceiptUID=<old Receipt UID>
predecessorReceiptSHA256=<old canonical payload hash>
recoverySessionID=<current session ID>
knownGoodRevision=<approved full commit SHA>
```

It binds the unchanged Identity UID and the current Signal, Root, and
`argocd-self` UIDs.

If deletion succeeds and creation is interrupted, the protected Signal keeps
Seed authority denied and the Recovery Session keeps normal Receipt creation
denied. `resume` recomputes the same successor from the immutable plan and
predecessor evidence, then performs create-only. It never recreates the old UID
or silently starts a different generation.

Once the successor create succeeds, rollback to the predecessor is forbidden.
Further damage is corrected forward in another explicitly authorized reissue.

### Interruption, restart, and rollback boundaries

| Last completed point | Required restart behavior | Rollback boundary |
| --- | --- | --- |
| Before temporary Recovery Binding | rerun read-only plan | no cluster side effect |
| Temporary Binding exists, Session absent | same principal creates exact Session or self-revokes Binding under gate | Binding alone keeps normal mutation denied |
| Session created, before freeze | resume same session or human-gated close | delete only the exact unused session record |
| Partially frozen | resume freezing from journal | do not auto-resume any layer |
| Admission suspended | restore exact Binding first or remain frozen | no other resume while `SUSPENDED` |
| Seed partially restored | correct forward from the same render | never delete Namespace or apply an unverified older Seed |
| Root or child UID changed | complete inside-out recovery | old UID cannot be restored |
| Predecessor Receipt deleted | create the precomputed successor | predecessor cannot be recreated |
| Successor Receipt created | validate and continue forward | successor is never rolled back |
| Partially resumed | refreeze failed and outer layers | External Root remains frozen |
| Close failed | keep session and audit evidence | normal mutation remains denied |

`SIGINT`, `SIGTERM`, workstation loss, API timeout, Forbidden, conflict,
malformed response, hash mismatch, failed type check, failed health gate, or
audit-write failure stops before the next mutation. Completed API mutations are
journaled when observable; an uncertain response is re-read before any retry.
An unknown outcome is never treated as absence.

### Human Judgment Gate matrix

The following operations require a new, action-specific approval. Approval is
bound to the session, target fingerprint, plan hash, object set, and current
checkpoint and cannot be reused:

| Operation | Gate |
| --- | --- |
| Issue, rotate, release, or revoke a credential generation | credential ceremony |
| Open or abandon a Recovery Session | session gate |
| Install or self-revoke temporary Recovery RBAC | session authority gate |
| Freeze External Root | Tier-0 freeze gate |
| Freeze each named control layer | layer-specific freeze gate |
| Suspend or restore the production admission Binding | admission gate for each direction |
| Repair the production Policy or Binding from Git | protection-repair gate |
| Apply the Recovery Seed | Seed mutation gate |
| Patch, restore, or recreate `atlas-bootstrap` AppProject | Tier-0 capability gate |
| Patch, restore, recreate, or repoint External Root | Tier-0 Root gate |
| Delete a predecessor and create a successor Receipt | Receipt reissue gate |
| Resume each control layer | layer-specific resume gate |
| Resume External Root normal reconciliation | final Tier-0 resume gate |
| Remove Recovery RBAC and close the session | closure gate |

Snapshot, hash calculation, dry-run render, API discovery, diff, plan creation,
and health reads are non-mutating and do not need a per-operation gate. They
still require audit logging and must fail closed on uncertainty.

### Definition and activation rollout

Implementation is split into independently reviewable phases and PRs:

0. **Admission escape readiness.** Establish and escrow the independent
   principal, narrow Escape RBAC, audit path, and disposable canary. Exercise
   canary suspend and restore. No production self-protecting `Deny` exists.
1. **Protection foundation.** This phase is subdivided:
   - **1A — Resource definitions.** Add VAP, Binding, Signal, RBAC, and tests,
     but do not reference them from a Kustomization reachable by External Root.
     Definition paths must fail conformance if accidentally wired.
   - **1B — Audit/Warn activation.** A separate Human-Gated PR wires only
     observation mode. Collect real audit evidence before proceeding.
   - **1C — Fail+Deny and Signal.** Activate `Fail` + `Deny` only after escape
     revalidation. Signal wiring follows in a separate activation change or a
     deterministic health-gated step after enforcement is proven. The merge
     that makes each reachable is itself a Human Judgment Gate.
2. **Complete break-glass recovery.** Implement the isolated recovery command,
   session lock, evidence bundle, exact restore, Receipt reissue, runbook, and
   macOS/OrbStack drill. This ADR must already be Accepted.
3. **Legacy-cluster migration.** Migrate Identity v1 to v2, commit the Receipt,
   and prove the historical Bootstrap downgrade fence. Normal `apply` cannot
   perform it.
4. **Receipt-aware Bootstrap.** Last, enable the ADR-0002 normal state machine,
   Recovery Session check, create-once Receipt commitment, and post-adoption
   read-only matrix.

No phase may be folded into another PR to save review time. In particular,
files introduced in Phase 1A must be unreachable from every live Kustomization,
and neither acceptance of this ADR nor a definition-only merge authorizes
runtime activation.

### macOS and OrbStack recovery drill

Before Phase 4 release, Atlas performs a complete drill on a disposable,
uniquely named Kind cluster running on the supported macOS and OrbStack target
with exact `versions.lock` versions. The drill must not reuse a developer's
normal cluster or credential.

The drill enables Kubernetes API audit output and archives it in the evidence
bundle. It proves at least:

- principal generation, custody metadata, target binding, and narrow RBAC;
- ordinary-principal denial and exact-principal admission exception;
- canary and production Binding suspend/restore with projection hash parity;
- definition-only resources remain unreachable before their activation PR;
- snapshot redaction and deterministic hash manifests;
- freeze order and exact restoration of sync policies;
- pinning every `main`-tracking control Application to a full commit;
- interruption and same-session resume at every checkpoint in the rollback
  table;
- adoption-compatible Seed restore without Helm release state;
- Root, `argocd-self`, and Signal loss/recreation followed by Receipt reissue;
- interruption after predecessor Receipt deletion and before successor create;
- predecessor lineage, successor create-only behavior, and admission protection;
- rejection of Identity UID changes and foreign recovery targets;
- the exact receipt-unaware Bootstrap at `783e858` fails before mutation;
- forward Git repair and health-gated inside-out resume;
- External Root resumes last;
- temporary Recovery RBAC is absent at close and the used credential generation
  is rotated or revoked;
- postflight `task quality`, status, UIDs, resourceVersions, audit chain, and
  final bundle hash are recorded.

The drill is destructive only to its disposable fixture and requires explicit
authorization. Passing simulated unit tests alone is insufficient evidence.

## Consequences

- Ordinary Bootstrap remains simple and cannot acquire emergency authority by
  flag or damaged-state inference.
- Admission escape remains available even when Argo CD is unavailable because
  its credential and narrow authorization are independent.
- VAP protects adoption evidence while RBAC and Git governance protect the VAP
  and Binding in accordance with upstream Kubernetes constraints.
- Every recovery produces target-bound, hash-anchored evidence and an explicit
  mutation journal.
- A Recovery Session makes interrupted work visible and prevents a normal
  process from racing Receipt reissue.
- Receipt lineage preserves the monotonic trust transition across legitimate
  Signal, Root, or `argocd-self` recreation without pretending Kubernetes UIDs
  are restorable.
- Recovery is operationally slower because high-impact steps require separate
  human decisions. This is intentional for Tier-0 and recovery authority.
- Maintaining a ready principal requires scheduled rotation, custody audits,
  canary drills, and prompt post-use revocation.
- Forward Git repair may keep the control graph frozen longer than a live edit,
  but preserves Git as definition authority.
- ADR-0002 remains an accepted design whose runtime gap is unresolved until all
  release prerequisites and Phase 4 are complete.

## Alternatives considered

### Add a recovery flag to `bootstrap/atlas apply`

Rejected because a shared parser and dispatcher would let a normal path acquire
recovery authority and make review of side effects ambiguous.

### Reuse the Kind or cluster-admin kubeconfig

Rejected because it is over-privileged, has unrelated lifecycle and custody,
and cannot prove use of the dedicated emergency principal.

### Use a ServiceAccount token in `argocd` or `kube-system`

Rejected because the credential would depend on a protected Namespace and
cluster-resident token machinery that may be part of the incident.

### Grant permanent broad recovery RBAC

Rejected because possession of the escrowed credential would become standing
cluster-admin authority. Only narrow Escape permission persists; Recovery
permission is session scoped.

### Have the VAP protect its own Policy and Binding

Rejected because the Kubernetes API explicitly prevents a
ValidatingAdmissionPolicy from matching those resources.

### Delete the Binding to suspend enforcement

Rejected because deletion changes UID, loses exact restoration evidence, and
widens the race window. A preconditioned action-only patch is smaller and
reversible.

### Set `failurePolicy: Ignore`

Rejected because it changes error handling at the Policy and can silently
bypass malformed or unavailable validation. Suspension changes only the
Binding from `Deny` + `Audit` to `Audit`.

### Recover directly from current `main`

Rejected because `main` may be the incident source and is mutable. Recovery
uses one verified full commit and repairs `main` forward.

### Restore raw Kubernetes object backups

Rejected because server metadata and UIDs are not portable or restorable.
Backups are evidence; recovery recreates desired objects and explicitly rebinds
the Receipt.

### Automatically roll back after a failed step

Rejected because rollback may restart unsafe reconciliation, erase evidence,
or cross a trust boundary with stale assumptions. Atlas stops frozen and
resumes from explicit checkpoints.

## Verification

Implementation is conformant only when automated tests and the target drill
prove all of the following:

- `bootstrap/atlas` has no recovery command, flag, imports, or mutation path;
- the recovery executable refuses default or environment-supplied credentials;
- the principal subject, generation, certificate lifetime, CSR approval, and
  target fingerprint are exact;
- old principal generations are unauthorized after rotation or revocation;
- persistent Escape RBAC is resource-name scoped and full Recovery RBAC exists
  only during an active session;
- no wildcard, impersonation, Secret-export, cluster-lifecycle, tenant, or data
  permission is granted;
- VAP target rules exclude VAP and Binding resources and protect every required
  evidence object and Namespace;
- observation, enforcement, suspension, drift, and unavailable states are
  classified exactly;
- suspend changes only Binding validation actions and restore matches the
  approved desired-projection hash;
- every mutation tests UID and resourceVersion where the API supports them;
- a foreign Recovery Session, any temporary Recovery Binding, and every
  uncertain read deny normal mutation;
- snapshots redact all credential and Secret payload material;
- journal hashes, pre-mutation anchor, and final bundle hash verify;
- only full, verified Git commits can become known-good inputs;
- every `main` source is pinned while the graph is frozen;
- Seed rendering is deterministic, version locked, and adoption compatible;
- no recovery operation creates Helm release state;
- Receipt reissue preserves Identity UID, predecessor lineage, and create-only
  successor semantics;
- interruption after predecessor deletion resumes the same successor without
  reopening Seed authority;
- Identity UID change or unverifiable lineage fails as `AMBIGUOUS`;
- each Human Judgment Gate is action specific and journaled;
- each rollout phase remains independently reviewable and Phase-1A resources
  are unreachable from the live Root graph;
- the complete macOS/OrbStack drill passes before Phase 4 release.

The implementation PRs must add the recovery runbook, credential ceremony,
rotation and revocation procedure, evidence schema, permission inventory,
failure injection tests, and drill record. `task quality` remains necessary but
is not sufficient for recovery acceptance.

## Acceptance gates

This ADR remains `Proposed` until reviewers confirm:

- the upstream VAP self-protection limitation and ADR-0002 clarification are
  accurately represented;
- the principal, custody, rotation, effective revocation, and RBAC model are
  acceptable;
- normal and recovery command paths are physically isolated;
- Binding-only suspend and exact restoration cannot create an untracked bypass;
- snapshots, redaction, hashes, journal, and external anchoring are complete;
- known-good revision selection preserves Git definition authority;
- recovery order is adoption compatible and follows outside-in/inside-out;
- Receipt reissue preserves monotonic authority through UID changes;
- every interruption state has a fail-closed resume and rollback boundary;
- Human Judgment Gates cover every trust-boundary mutation;
- definition and activation changes cannot enter the live Root graph together;
- the macOS/OrbStack drill is sufficient for the supported environment.

Acceptance will authorize separately reviewed implementation in Phase 0 through
Phase 4 order. It will not authorize a Tier-0 apply, admission activation,
credential issuance, recovery execution, legacy migration, Receipt reissue,
receipt-aware release, or production enablement.
