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
Operation Fence, and their namespaces.

Relevant upstream semantics are:

- [Validating Admission Policy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/)
- [ValidatingAdmissionPolicy API](https://kubernetes.io/docs/reference/kubernetes-api/admissionregistration/validating-admission-policy-v1/)
- [ValidatingAdmissionPolicyBinding API](https://kubernetes.io/docs/reference/kubernetes-api/admissionregistration/validating-admission-policy-binding-v1/)
- [Kubernetes X.509 authentication](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#x509-client-certs)
- [Kubernetes CertificateSigningRequest](https://kubernetes.io/docs/reference/access-authn-authz/certificate-signing-requests/)
- [Kubernetes RBAC](https://kubernetes.io/docs/reference/access-authn-authz/rbac/)
- [Argo CD terminate operation](https://argo-cd.readthedocs.io/en/stable/faq/#how-can-i-terminate-a-sync)
- [Argo CD RBAC](https://argo-cd.readthedocs.io/en/stable/operator-manual/rbac/)
- [kind audit logging](https://kind.sigs.k8s.io/docs/user/auditing/)

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
6. Every recovery mutation and every normal Bootstrap cluster mutation after
   an API server exists is bound to one Operation Fence. Recovery also binds
   the Fence to one cluster fingerprint, one known-good Git commit, one plan
   hash, and exact principal generations.
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

Recovery uses two independent X.509 usernames:

```text
Recovery Operator:  atlas:break-glass:<kubeSystemNamespaceUID>:g<generation>
Session Authorizer: atlas:recovery-authorizer:<kubeSystemNamespaceUID>:g<generation>
```

Both certificate subjects omit Organization (`O`) entirely. Kubernetes maps
X.509 Organization values to authorization groups and automatically adds every
authenticated certificate to `system:authenticated`; an Organization is not a
descriptive label. Atlas therefore uses exact usernames only and forbids RBAC
subjects that grant mutation to either a custom recovery group or
`system:authenticated`.

The platform may retain Kubernetes' normal read-only API discovery permissions
for `system:authenticated`. Credential ceremonies and sessions verify with the
actual certificate that these are the only group-derived permissions and that
no group-derived mutation is effective.

The Recovery Operator performs the approved recovery plan. The Session
Authorizer is a different principal that atomically acquires the Operation
Fence, installs the temporary permission bundle, removes that bundle, and
releases the Fence. In production the two credentials are held by different
people or independent custody roles. One certificate must never contain both
usernames or be bound to both authorities.

Neither username may use `system:masters`, another `system:*` prefix, a human
username, or a normal automation identity. Admission exceptions match the
complete username, including generation. A group-only exception is forbidden.

The canonical recovery resources are:

| Resource | Name |
| --- | --- |
| Protection ValidatingAdmissionPolicy | `atlas-bootstrap-evidence-protection` |
| Protection ValidatingAdmissionPolicyBinding | `atlas-bootstrap-evidence-protection` |
| Recovery authorization Policy and Binding | `atlas-bootstrap-recovery-authorization` |
| Escape ClusterRole | `atlas-bootstrap-break-glass-escape` |
| Escape ClusterRoleBinding | `atlas-bootstrap-break-glass-escape` |
| Session Authorizer ClusterRole and Binding | `atlas-bootstrap-recovery-authorizer-cluster` |
| Session Authorizer Role and RoleBinding | `atlas-bootstrap-recovery-authorizer` per approved Namespace |
| Cluster-scoped Recovery ClusterRole | `atlas-bootstrap-recovery-cluster` |
| Namespaced Recovery Role | `atlas-bootstrap-recovery` in each approved Namespace |
| Temporary Recovery bindings | `atlas-bg-<scope>-<sessionID>` |
| Canary Policy and Binding | `atlas-bootstrap-admission-escape-canary` |
| Operation Fence ConfigMap | `atlas-bootstrap-operation-fence` |

Every temporary Binding carries
`atlas.io/recovery-session=<sessionID>`. Canonical names or labels cannot change
without an accepted superseding ADR because Phase-4 Bootstrap uses them as
authority inputs. `sessionID` is exactly 32 lowercase hexadecimal characters
generated from 128 bits of cryptographically secure randomness.

### Principal credential lifecycle

#### Generation

Each principal uses its own X.509 client certificate issued through a manually
reviewed `certificates.k8s.io/v1` CSR using signer
`kubernetes.io/kube-apiserver-client`. Keys, CSRs, certificates, generations,
and escrow packages are never shared between the two principals.

- The private key is generated outside the cluster and never leaves encrypted
  operator-controlled custody except for an approved recovery session.
- The CSR requests only `digital signature` and `client auth` usages.
- The requested and accepted lifetime must not exceed 30 days.
- The CSR Common Name must match one exact canonical username and the subject
  must contain no Organization values.
- CSR approval, certificate extraction, fingerprinting, and CSR cleanup are
  journaled as one credential ceremony.
- The cluster CA private key is never exported or copied into the recovery
  package.
- The certificate is tested only against the exact cluster fingerprint.

Kubernetes does not support X.509 client certificate revocation. Atlas
therefore treats certificate expiry as cryptographic expiry and exact-user
RBAC plus generation-specific admission rules as effective revocation.

#### Authorization tiers

Authentication does not imply standing recovery authority. Authorization is
split between persistent Escape, persistent Session Authorization, and a
temporary Recovery Permission Bundle.

`Escape` is the Recovery Operator's minimum Phase-0 capability. Its exact-user
ClusterRoleBinding permits:

- read of the canonical protection and recovery-authorization Policies and
  Bindings;
- read of the target fingerprint and non-secret recovery evidence metadata;
- `patch` and `update` of the production protection Binding by exact
  `resourceNames`;
- `patch` and `update` of the separate canary Binding by exact
  `resourceNames`;
- read-only canary and enforcement verification.

Canary and production targets are separate RBAC rules so a test cannot select
the production Binding accidentally. Escape grants no wildcard resource,
verb, API group, namespace, impersonation, Secret read, CSR approval,
Application mutation, Seed mutation, evidence rewrite, or Tier-0 mutation.

`Session Authorization` belongs only to the exact Session Authorizer username.
It is itself Namespace split: a ClusterRoleBinding contains only cluster-scoped
Binding lifecycle and exact `bind`, while a RoleBinding in each approved
Namespace contains that Namespace's Fence or RoleBinding lifecycle. It may:

- create and delete the canonical Operation Fence in `kube-system`;
- create and delete session-labeled RoleBindings in only `kube-system`,
  `argocd`, and Namespaces enumerated by the approved plan;
- create and delete the one session-labeled ClusterRoleBinding for the
  cluster-scoped Recovery role;
- use `bind` only on the canonical, pre-existing Recovery Roles and
  ClusterRole named in this ADR;
- read, but never create, patch, update, escalate, or delete those Roles and
  ClusterRole.

The Session Authorizer cannot mutate adoption evidence, Applications,
AppProjects, Seed resources, Policy specifications, workload resources, Role
definitions, or any unrelated Binding. Kubernetes RBAC cannot constrain a
top-level `create` by `resourceNames`; the always-enforced recovery-
authorization VAP therefore validates the exact Fence name and schema and the
exact temporary Binding name, session label, roleRef, Namespace, and
Recovery-Operator subject. A missing, suspended, unavailable, or mismatched
authorization Policy denies session opening and closing.

The `Recovery Permission Bundle` is installed only after the Session
Authorizer has acquired the Operation Fence. It is structurally split:

```text
Recovery Permission Bundle
├── ClusterRole + ClusterRoleBinding: cluster-scoped resources only
├── kube-system Role + RoleBinding: Identity/Fence reads and planned Receipt reissue
├── argocd Role + RoleBinding: Application, Signal, AppProject, and Seed work
└── explicit Namespace Role + RoleBinding: only when named by the plan
```

A ClusterRoleBinding must never carry namespaced Recovery permissions. All
namespaced writes are granted by a RoleBinding in that exact Namespace. Roles
and ClusterRole are static, reviewed, Git-defined inputs installed before an
incident; the Session Authorizer never creates or updates plan-derived role
definitions.

An additional Namespace is eligible only when its Recovery Role and Session
Authorizer RoleBinding were activated through a prior reviewed change. An
incident cannot expand this set; a missing Namespace authorization is
`AUTHORITY_UNAVAILABLE` and remains frozen.

The bundle grants only API groups, kinds, Namespaces, and verbs required by the
recovery contract. For top-level `create`, RBAC granularity stops at
`kind + namespace` for namespaced objects and `kind + cluster` for
cluster-scoped objects. Atlas does not claim exact-object create enforcement
from RBAC. Exact intended names come from the immutable plan; the admission
policies enforce canonical Fence, evidence, and temporary-RBAC objects, while
the command and audit journal enforce the approved Seed/Application inventory.

The bundle may include narrowly reviewed permissions needed to:

- freeze and resume named Argo CD control Applications;
- restore the adoption-compatible Seed and required cluster-scoped resources;
- validate or restore the canonical AppProject and External Root;
- read protected adoption evidence;
- reissue the canonical Receipt under the protocol in this ADR.

It contains no wildcard verb or resource, `impersonate`, `escalate`, arbitrary
`bind`, tenant payload, data-plane, unrelated Namespace, cluster-lifecycle, or
Secret-export authority. Every session verifies effective permissions with the
actual Recovery Operator and Session Authorizer credentials, including
negative checks outside the intended scopes.

#### Custody

Each principal has a separate credential package containing only its client
certificate, encrypted private key, API endpoint, cluster CA certificate,
context name, certificate serial, generation, validity interval, and target
fingerprint. Neither package may contain a cluster-admin credential or the
other principal's material.

Each package:

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

Production release separates the Recovery Operator, Session Authorizer, and at
least one approving owner or credential custodian. The same human cannot use
both principals in one production session. A disposable non-production drill
may use one repository owner only when that exception is explicit in the drill
record.

#### Rotation and revocation

Each principal rotates independently before two-thirds of its certificate
lifetime has elapsed and immediately after any use, custody breach, failed
custody audit, or holder departure.

Rotation is ordered:

1. issue the next generation certificate;
2. install only that principal's exact-user persistent RBAC subject;
3. add the next exact username to the applicable admission rules;
4. exercise canary suspend/restore for a Recovery Operator or a canary Fence
   and permission-bundle ceremony for a Session Authorizer;
5. remove the previous exact-user persistent and temporary bindings;
6. remove the previous username from every admission rule;
7. use the actual old certificate to prove that only permitted read-only
   discovery remains and every mutation is denied;
8. archive the rotation evidence and destroy accessible copies of the old key.

Revocation starts with steps 5 through 7 and does not wait for certificate
expiry. An old certificate may remain cryptographically authenticated and a
member of `system:authenticated` until expiry, but it must have no mutation
through exact-user or group RBAC and no admission exception. Failure to prove
all revocation fences with the actual credential is `AUTHORITY_DEGRADED`;
production Deny or recovery must not proceed.

If the Session Authorizer is unavailable, a new session cannot start and an
open session cannot install, change, or remove permission bindings or release
the Fence. The Recovery Operator may continue only within an already installed
bundle and approved checkpoint. The graph otherwise remains frozen and the
Fence remains present; there is no cluster-admin fallback in this ADR.

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

`inspect` and `plan` use a read-only kubeconfig. Mutating recovery steps require
the Recovery Operator kubeconfig; Fence and permission-bundle steps require the
separate Session Authorizer kubeconfig. Each is passed by its own explicit
absolute, non-symlink option. The command verifies the expected exact username
before use and never falls back to the current context, default kubeconfig,
environment-provided credentials, or the other principal.

Freeze and controlled manual sync use a version-matched Argo CD client in
Kubernetes core mode. Its platform-specific artifact and checksum must be
locked and locally available before Phase 2; an ambient `argocd` binary or Argo
API token is forbidden.

`plan` produces an immutable plan description and SHA-256 digest before the
first mutation. `execute`, `resume`, and `close` require exact confirmation of:

- cluster fingerprint;
- recovery session ID and Operation Fence UID;
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
- `atlas-bootstrap-operation-fence` when present;
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

The separate `atlas-bootstrap-recovery-authorization` Policy and Binding
constrain Session Authorizer create/delete requests for the Fence and temporary
RBAC bundle. They remain `failurePolicy: Fail` with `{Audit, Deny}` throughout
recovery and are never part of evidence-protection suspend. They validate exact
usernames, generations, names, Namespaces, labels, roleRefs, subjects, plan
hash, and session ID. Their absence, drift, or uncertainty is
`AUTHORITY_UNAVAILABLE`.

Acceptance of this ADR overrides only ADR-0002's impossible VAP
self-protection mechanism. It does not weaken protection of adoption evidence,
namespaces, or the requirement that protection precede a valid Signal. The
acceptance commit must add a reciprocal clarification link to ADR-0002.

#### Enforcement states

The canonical admission states are:

| State | Policy | Binding | Normal mutation |
| --- | --- | --- | --- |
| `UNINSTALLED` | absent | absent | Phase 0/1A only; Signal invalid |
| `OBSERVING` | expected spec | `{Audit}` or `{Audit, Warn}` | allowed and audited; Signal invalid |
| `ENFORCED` | `failurePolicy: Fail` and expected spec | `{Audit, Deny}` | denied; Signal may become valid |
| `SUSPENDED` | unchanged expected spec | `{Audit}` | recovery session only |
| `DRIFTED` | readable but unexpected | any | deny recovery mutation until separately approved repair |
| `UNAVAILABLE` | unreadable or undecodable | unreadable or undecodable | deny all mutation |

`Deny` and `Warn` are never combined. `validationActions` is a semantic set:
order has no meaning and duplicates are invalid. `Audit` remains present in
enforcing and suspended states. A match condition excludes only the exact current
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

Canonical projection encoding is UTF-8 JSON with no insignificant whitespace,
object keys recursively sorted by Unicode code point, and normalized scalar
representations. Lists retain API order unless the Kubernetes schema declares
set semantics. Declared sets are duplicate-checked and sorted by their
canonical element encoding before hashing. At minimum this rule applies to
`validationActions`; repository desired state writes it canonically as
`[Audit, Deny]`. The SHA-256 input is the exact canonical byte sequence, and
the canonicalizer version is recorded in the evidence bundle.

Suspend first validates the returned `validationActions` as the semantic set
`{Audit, Deny}`. Its JSON Patch then tests the Binding UID, resourceVersion,
and the exact raw array returned by that same read, regardless of order, before
replacing only that array with `[Audit]`. A semantic mismatch or patch
precondition mismatch is `DRIFTED` and performs no mutation.

Restore uses the current resourceVersion and the recorded suspended projection
as preconditions, tests the actual raw suspended array, restores the canonical
approved array `[Audit, Deny]`, and verifies:

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
failure leaves the graph frozen, the Operation Fence present, and normal
reconciliation disabled.

Phase 0 proves this flow against a disposable canary Policy and Binding whose
targets are isolated from Atlas production evidence. No production `Deny`
Binding may exist before the canary has been suspended and restored by the
escrowed principal.

Session Authorization has an equally isolated Phase-0 canary. Its exact-user
RBAC and recovery-authorization canary may create and delete only a separately
named canary Fence and temporary bindings to static, inert canary Roles. It
cannot select the production Fence, production Recovery Roles, or a production
temporary-binding name. The canary ceremony exercises CREATE conflicts,
Admission exactness, `bind`, cleanup, and old-generation denial without
granting a production Recovery permission. The production Session
Authorization bindings remain absent until Phase 1C.

### Operation Fence and exclusivity

Atlas uses one canonical, create-only Operation Fence for every cluster
mutation performed by normal Bootstrap after an API server exists and for
every recovery mutation:

```text
kind: ConfigMap
namespace: kube-system
name: atlas-bootstrap-operation-fence
schema: atlas.io/bootstrap-operation-fence/v1
immutable: true
```

Once the target API server exists, normal `bootstrap/atlas apply` and
`atlas-recovery` must acquire this same object with one Kubernetes CREATE
before their first cluster mutation. They never use a read-absence-then-act
sequence. Exactly one create can succeed; `AlreadyExists`, timeout, Forbidden,
transport failure, undecodable response, or an uncertain create result denies
mutation until an authoritative re-read resolves the exact object. The
missing-cluster substrate exception is defined under acquisition and release.

Every Fence records:

```text
operationID=<32 lowercase hexadecimal characters from 128 random bits>
mode=<normal-apply|recovery>
clusterFingerprintSHA256=<target fingerprint digest>
holderUsername=<authenticated creator username>
createdAt=<operator workstation UTC audit time>
```

A recovery Fence additionally records:

```text
sessionID=<32 lowercase hexadecimal characters from 128 random bits>
recoveryPrincipal=<exact Recovery Operator username and generation>
authorizerPrincipal=<exact Session Authorizer username and generation>
planSHA256=<approved plan digest>
knownGoodRevision=<full Git commit SHA>
```

The API CREATE response UID and resourceVersion identify the held Fence.
`createdAt` is audit context only and grants no authority. The Fence has no
automatic expiry; an interrupted owner cannot silently reopen mutation.

Admission validates a `normal-apply` create only from an exact Bootstrap
username recorded in the environment's reviewed activation manifest, when
`holderUsername` equals that authenticated username and the payload contains no
recovery authority. A `recovery` create is accepted only from the exact current
Session Authorizer and must bind the approved Recovery Operator, session, plan,
target, and revision. Update is always denied. Delete is accepted only from the
matching authorized normal holder or Session Authorizer and still requires
client-side UID and resourceVersion preconditions. Group membership alone
never authorizes Fence creation or deletion.

#### Participants and enforcement boundary

The Fence gives mutual exclusion only to conformant normal Bootstrap and
recovery commands. Phase-4 normal Bootstrap must acquire it, hold it across all
mutations, and release it only on a completed or explicitly failed-closed
operation. Recovery acquisition is the Session Authorizer's first mutation;
temporary RBAC is installed only after the recovery Fence is re-read and
validated.

Argo CD, Kubernetes controllers, and human API clients do not consume this
ConfigMap. They are controlled separately:

- Argo CD propagation is stopped by the Freeze contract;
- protected evidence and recovery authorization are enforced by Admission;
- ordinary operator rights are constrained by Kubernetes and Argo CD RBAC;
- direct malicious cluster-admin mutation remains outside the threat model.

Calling the Fence a cluster-wide mutex without these boundaries is forbidden.
It is the atomic mutex for the two Atlas command paths and the durable signal
that makes their concurrent mutation fail closed.

#### Acquisition and release

For an existing Atlas cluster, normal Bootstrap creates a `normal-apply` Fence
before Registry, Seed, AppProject, External Root, Receipt, or any other
mutation. A pre-existing Fence always fails the normal command; normal
Bootstrap never steals, expires, or deletes a foreign Fence.

A missing cluster is the only bootstrap boundary at which a Kubernetes Fence
cannot yet exist. In that case the existing host lifecycle lock serializes
local Kind and Registry setup. Recovery rejects the target because no readable
Atlas Identity exists. As soon as the new API server is ready, normal Bootstrap
must create the Fence before creating Identity or any other Kubernetes object;
the cluster is not an eligible recovery target before that Identity is
committed. Phase 2 must split current substrate creation from Identity creation
to preserve this ordering. The host lock is not represented as a cluster-wide
mutex and never substitutes for the Fence on an existing cluster.

Recovery order is:

1. the Session Authorizer creates the `recovery` Fence with create-only
   semantics;
2. both principals re-read and validate its UID, resourceVersion, payload, and
   admission protection;
3. the Session Authorizer installs the Namespace-split Permission Bundle;
4. the Recovery Operator executes only the bound plan;
5. the Session Authorizer removes every temporary RoleBinding and
   ClusterRoleBinding;
6. after postflight and the close gate, the Session Authorizer deletes the
   Fence with UID and resourceVersion preconditions.

The Fence is released last. A missing temporary Binding does not imply a free
operation, and a leftover Binding without the expected Fence is
`AUTHORITY_DRIFTED`; it grants no right to reconstruct or acquire a Fence.
Delete timeout or an uncertain response is re-read. A UID mismatch, changed
resourceVersion, foreign holder, incomplete bundle removal, or unavailable
Session Authorizer leaves the Fence in place and normal mutation denied.

A normal holder releases its Fence only after successful completion or a
deterministic, fully observed failure with no API request still in flight. Any
unknown mutation outcome retains the Fence. Release uses the CREATE response
UID and latest validated resourceVersion as delete preconditions; an uncertain
delete is authoritatively re-read and never reported as successful from a
timeout alone.

`resume` requires the same session, plan, target, Recovery Operator, Session
Authorizer, Fence UID, and known-good revision. No second plan may reuse an
existing Fence.

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
  Receipt, Operation Fence, and temporary Recovery Permission Bundle;
- raw and canonical Policy and Binding objects plus type-check status;
- effective permission inventories for the read-only identity, Recovery
  Operator, and Session Authorizer, including group-derived permissions;
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

### Audit readiness

Kubernetes API audit is a Phase-0 precondition, not an artifact that recovery
may enable after an incident. The current `clusters/kind/local-orbstack.yaml`
does not configure API audit and therefore cannot provide Phase-0 evidence as
written.

A new disposable drill cluster must enable audit at Kind creation by using
`kubeadmConfigPatches` for the API server audit flags and read-only policy mount
plus an explicit writable audit-log mount. The policy records at least Metadata
for all requests and sufficient request/response detail for Admission, RBAC,
Application, AppProject, Fence, and adoption-evidence operations. It must omit
Secret bodies, tokens, and credential material. The host audit destination is
encrypted, outside the repository, and incorporated into the evidence bundle.

An existing disposable Kind cluster without this configuration is not
retrofitted or treated as conformant. A separately named audited drill cluster
is created under an explicit cluster-lifecycle Human Gate; this ADR never
authorizes deletion of the existing developer cluster. A non-disposable cluster
without an independently verified audit destination remains blocked at Phase 0
and requires a separate substrate change and maintenance decision.

Unreadable audit configuration, unwritable destination, missing event range,
or failed redaction is `AUDIT_UNAVAILABLE` and denies canary, production Deny,
and recovery mutation.

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
- the version-matched Argo CD core-mode client artifact is checksum verified
  and available offline;
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
changed substrate fingerprint; unreadable Identity; any Operation Fence or
temporary Recovery Binding; unavailable audit destination; unverified
principal generation; or unexpected effective permission for either principal.

Select the known-good revision, build the plan, capture the preflight snapshot,
verify redaction, anchor `pre-mutation.sha256`, and obtain the session-open
Human Judgment Gate. No mutation precedes these checks.

#### 2. Acquire the Fence and install permission

The Session Authorizer performs one create-only API operation for the canonical
recovery Fence as the first mutation. A competing normal apply that already
holds the Fence wins; recovery installs no RBAC. If recovery wins, both
principals validate the returned Fence UID, resourceVersion, payload, and
protection before the Session Authorizer installs the Namespace-split
Permission Bundle.

Every expected temporary Binding must be present with the exact label,
Namespace, roleRef, subject, UID, and plan hash before the Recovery Operator
may mutate. A partial bundle is removed by the Session Authorizer or remains
fenced; it never authorizes partial recovery.

#### 3. Freeze and pin outside-in

Freeze is more than disabling AutoSync. Before walking the graph, the Recovery
Operator installs an exact, snapshotted `argocd-rbac-cm` recovery fragment that
denies `applications sync`, `override`, and `update` to every ordinary Argo CD
subject. Argo CD deny takes precedence over allow. The Recovery Operator uses
the version-locked Argo CD client in Kubernetes core mode, so no Argo API token
or manual-sync exception is introduced. Direct Kubernetes Application writes
by ordinary credentials must already be denied by effective RBAC checks.

The fragment uses the canonical key
`policy.atlas-recovery-freeze.csv`. Installation tests ConfigMap UID and
resourceVersion, waits for Argo CD RBAC reload, and proves with representative
ordinary identities that sync, override, and update are denied. Removal tests
the actual recovery value and current resourceVersion, restores the exact
preflight value or absence, and verifies the canonical ConfigMap projection
hash. Any failed reload or permission probe leaves Freeze incomplete.

Then freeze each layer outside-in without deleting Applications:

```text
External Root
  -> project-bootstrap / platform-control / workload-control
  -> argocd-self and affected control leaves
```

For every Application, the command:

1. snapshots UID, resourceVersion, automated sync, `.operation`,
   `status.operationState`, and managed-resource versions;
2. removes automated sync with UID/resourceVersion preconditions;
3. re-reads operation state, invokes the supported Argo CD terminate operation
   when `.operation` is present or phase is `Running`, and only waits when it
   is already `Terminating`;
4. waits until `.operation` is absent and `status.operationState.phase` is
   neither `Running` nor `Terminating`;
5. verifies during a bounded quiet interval that no new Argo-managed mutation
   occurred;
6. re-reads and journals final UID and resourceVersion before moving inward.

Termination failure, a new operation, resource mutation, or unavailable state
stops progress with all already-frozen outer layers left frozen. Removing
AutoSync alone never satisfies the Freeze gate. The Argo RBAC deny fragment
remains active until the inside-out resume explicitly reaches the appropriate
layer and is removed under its own gate.

Pin the External Root and every recovery-relevant child source to the approved
full commit while automated sync remains disabled. This is necessary because
the current Git manifests otherwise follow `main` at more than one Application
level.

The recovery does not automatically restore a layer whose prior revision is
suspected unsafe.

#### 4. Suspend admission when required and restore the Seed

If the plan needs a mutation that the evidence policy would deny, suspend only
the canonical Binding to `Audit` using the exact protocol above. If no
protected object or Namespace mutation is needed, leave admission enforced.

Render only from the approved commit and locked artifacts. The Recovery Seed:

- matches `versions.lock` and the `argocd-self` desired version;
- contains no GitOps Signal, Receipt, Operation Fence, External Root, tenant
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

Materialize and manually sync the pinned `argocd-self` desired state through
the version-matched Argo CD client in Kubernetes core mode before other
platform capabilities. Child automated sync remains disabled. Re-verify the
manual-sync deny fragment after this sync, then confirm the controller
inventory and the Signal object created by GitOps.

If admission was suspended, restore `ENFORCED` and pass all positive, negative,
type-check, and audit probes before classifying the Signal as protected or
continuing to Receipt work.

#### 6. Reissue the Receipt when bound UIDs changed

Receipt reissue follows the separate protocol below. It occurs only while the
recovery Operation Fence exists, the graph remains frozen, admission is enforced, and
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
Judgment Gate. The Argo RBAC manual-sync deny is hash-checked throughout resume
and removed only under a separate gate immediately before External Root resume,
after all inner layers and the repaired Git revision are verified.

#### 8. Close the session

Capture postflight evidence, verify the Receipt and protected Signal, verify
admission enforcement, verify normal-principal denial, and verify the manual-
sync guard was removed only at its approved checkpoint.

The Session Authorizer then removes every temporary RoleBinding and
ClusterRoleBinding and proves with the actual Recovery Operator credential that
all Recovery mutations are denied. Only then may the final close gate authorize
deletion of the Operation Fence with its UID and resourceVersion preconditions.
Verify absence with the read-only identity, rotate or revoke both used
credential generations, then finalize and externally anchor `bundle.sha256`.
A failed close keeps the Fence present and normal mutation denied.

### Receipt reissue protocol

Receipt reissue is permitted only when:

- the substrate fingerprint and Identity v2 UID are unchanged;
- the selected Profile and repository still match;
- a valid predecessor Receipt exists when the Recovery Plan is created and its
  exact raw object and hash are present in the pre-mutation evidence bundle;
- a planned recovery changed the Signal, External Root, or `argocd-self` UID;
- the replacement objects are rendered from the known-good revision;
- the replacement Signal is valid and protected under `ENFORCED` admission;
- the recovery Fence and Receipt-reissue Human Judgment Gate are active.

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
Seed authority denied and the Operation Fence keeps normal Receipt creation
denied. `resume` recomputes the same successor from the immutable plan and
predecessor evidence, then performs create-only. It never recreates the old UID
or silently starts a different generation.

Once the successor create succeeds, rollback to the predecessor is forbidden.
Further damage is corrected forward in another explicitly authorized reissue.

### Interruption, restart, and rollback boundaries

| Last completed point | Required restart behavior | Rollback boundary |
| --- | --- | --- |
| Before Fence acquire | rerun read-only plan | no cluster side effect |
| Fence acquired, bundle absent or partial | Session Authorizer completes or removes bundle under gate | Fence keeps normal mutation denied |
| Bundle complete, before freeze | resume same session or remove bundle and release Fence under gate | no recovery mutation occurred |
| Partially frozen | resume freezing from journal | do not auto-resume any layer |
| Admission suspended | restore exact Binding first or remain frozen | no other resume while `SUSPENDED` |
| Seed partially restored | correct forward from the same render | never delete Namespace or apply an unverified older Seed |
| Root or child UID changed | complete inside-out recovery | old UID cannot be restored |
| Predecessor Receipt deleted | create the precomputed successor | predecessor cannot be recreated |
| Successor Receipt created | validate and continue forward | successor is never rolled back |
| Partially resumed | refreeze failed and outer layers | External Root remains frozen |
| Bundle removed, Fence present | Session Authorizer releases exact Fence | Recovery Operator has no mutation authority |
| Fence release uncertain | authoritative re-read by read-only identity | never assume release from timeout |
| Close failed | keep Fence and audit evidence | normal mutation remains denied |

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
| Issue, rotate, release, or revoke either credential generation | principal-specific credential ceremony |
| Activate, modify, or deactivate persistent Escape or Session Authorization RBAC | principal-specific authority gate |
| Acquire or release the Operation Fence | Session Authorizer gate for each direction |
| Install or remove the Recovery Permission Bundle | session authority gate |
| Select or change the known-good Git commit | revision-selection gate |
| Install or remove the Argo manual-sync deny | manual-sync freeze gate |
| Freeze External Root | Tier-0 freeze gate |
| Freeze each named control layer | layer-specific freeze gate |
| Terminate an in-flight sync operation | Application-specific termination gate |
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

0. **Admission escape readiness.** Establish and separately escrow the Recovery
   Operator and Session Authorizer, Recovery Operator Escape RBAC, an audited
   Kind target, and disposable protection and recovery-authorization canaries.
   Exercise canary suspend/restore and a canary Fence/Permission Bundle
   ceremony. Session Authorizer authority is canary-scoped; production Session
   Authorizer RBAC is not active. No production `Deny` exists.
1. **Protection foundation.** This phase is subdivided:
   - **1A — Resource definitions.** Add VAP, Binding, Signal, Operation Fence
     contract, Namespace-split RBAC, and tests, but do not reference them from a
     Kustomization reachable by External Root. Definition paths must fail
     conformance if accidentally wired.
   - **1B — Audit/Warn activation.** A separate Human-Gated PR wires only
     observation mode. Collect real audit evidence before proceeding.
   - **1C — Fail+Deny and Signal.** Activate `Fail` + `Deny` only after escape
     revalidation. Activate production Session Authorizer RBAC only after the
     recovery-authorization Deny is proven. Signal wiring follows in a separate
     activation change or a deterministic health-gated step after enforcement
     is proven. The merge that makes each reachable is itself a Human Judgment
     Gate.
2. **Complete break-glass recovery.** Implement the isolated recovery command,
   shared CAS Operation Fence, Permission Bundle, evidence bundle, exact
   restore, Receipt reissue, runbook, and macOS/OrbStack drill. This ADR must
   already be Accepted. The first independently reviewed Phase-2 change adds
   Fence acquisition/release to normal Bootstrap without changing adoption
   authority; the recovery command remains non-mutating until that exact normal
   version passes the forced-race tests.
3. **Legacy-cluster migration.** Migrate Identity v1 to v2, commit the Receipt,
   and prove the historical Bootstrap downgrade fence. Normal `apply` cannot
   perform it.
4. **Receipt-aware Bootstrap.** Last, enable the ADR-0002 normal state machine,
   retain mandatory Operation Fence acquisition, enable create-once Receipt
   commitment, and enforce the post-adoption read-only matrix.

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

- both principal generations, separate custody, target binding, and effective
  permission inventories;
- Namespace-split RoleBindings, cluster-only ClusterRoleBinding, explicit
  top-level-create granularity, and recovery-authorization Admission checks;
- a forced race between normal apply and recovery proves exactly one Fence
  create wins and the loser performs no mutation;
- ordinary-principal denial and exact-principal admission exception;
- canary and production Binding suspend/restore with projection hash parity;
- both API orders of `{Audit, Deny}` produce the same semantic and canonical
  hash while JSON Patch tests the actual raw order;
- definition-only resources remain unreachable before their activation PR;
- snapshot redaction and deterministic hash manifests;
- freeze order, termination of in-flight operations, manual-sync denial, quiet
  interval, and exact restoration of sync policies;
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
- every temporary Recovery Binding is absent before Fence release and both used
  credential generations are rotated or revoked;
- postflight `task quality`, status, UIDs, resourceVersions, audit chain, and
  final bundle hash are recorded.

The drill is destructive only to its disposable fixture and requires explicit
authorization. Passing simulated unit tests alone is insufficient evidence.

## Consequences

- Ordinary Bootstrap remains simple and cannot acquire emergency authority by
  flag or damaged-state inference.
- Admission escape remains available even when Argo CD is unavailable because
  Recovery Operator and Session Authorizer credentials are independent of it.
- VAP protects adoption evidence while RBAC and Git governance protect the VAP
  and Binding in accordance with upstream Kubernetes constraints.
- Every recovery produces target-bound, hash-anchored evidence and an explicit
  mutation journal.
- The create-only Operation Fence removes the normal/recovery check-then-act
  race and makes interrupted ownership visible.
- Namespace-split Roles prevent a cluster-scoped Binding from silently
  broadening namespaced recovery access.
- Receipt lineage preserves the monotonic trust transition across legitimate
  Signal, Root, or `argocd-self` recreation without pretending Kubernetes UIDs
  are restorable.
- Recovery is operationally slower because high-impact steps require separate
  human decisions. This is intentional for Tier-0 and recovery authority.
- Maintaining two ready principals requires scheduled independent rotation,
  custody audits, canary/authorization drills, and prompt post-use revocation.
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

### Use one ClusterRoleBinding for the entire Recovery Plan

Rejected because namespaced rules in a ClusterRoleBinding apply in every
Namespace. Atlas separates cluster-scoped authority from an explicit
RoleBinding per Namespace and acknowledges the remaining top-level-create
granularity.

### Check a Recovery Session object before mutation

Rejected because a read-only absence check has a TOCTOU window. Both Atlas
mutation paths must contend on one create-only Operation Fence as their first
mutation.

### Put recovery authority in an X.509 Organization group

Rejected because Organization becomes a Kubernetes group and every certificate
also joins `system:authenticated`. Exact-user subjects and effective-permission
tests make rotation and revocation auditable.

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
Binding from semantic set `{Audit, Deny}` to `{Audit}`.

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
- the recovery executable refuses default, environment-supplied, or swapped
  Recovery Operator and Session Authorizer credentials;
- both principal subjects, generations, certificate lifetimes, CSR approvals,
  absent Organizations, and target fingerprints are exact;
- no RBAC Mutation is inherited through a custom group or
  `system:authenticated`; only expected read-only discovery remains;
- old principal generations fail every mutation after rotation or revocation
  when tested with the actual old certificates;
- Escape RBAC grants exact production and canary Binding patch/update rules;
- Session Authorizer RBAC has only canonical Fence and temporary-binding
  lifecycle plus exact `bind`, with no role-definition or recovery mutation;
- the Permission Bundle uses cluster-only ClusterRoleBinding and one explicit
  RoleBinding per approved Namespace;
- tests document that RBAC top-level create is kind/Namespace or kind/cluster
  scoped, while Admission exactly constrains Fence, evidence, and temporary
  RBAC objects;
- no wildcard, impersonation, arbitrary bind/escalate, Secret-export,
  cluster-lifecycle, tenant, or data permission is granted;
- VAP target rules exclude VAP and Binding resources and protect every required
  evidence object and Namespace;
- observation, enforcement, suspension, drift, and unavailable states are
  classified exactly;
- both API orders of `{Audit, Deny}` compare equal, JSON Patch tests the actual
  raw order, and canonical projection hashing is deterministic;
- suspend changes only Binding validation actions and restore matches the
  approved canonical desired-projection hash;
- every mutation tests UID and resourceVersion where the API supports them;
- a forced normal/recovery race has exactly one successful Fence create and the
  loser performs no mutation;
- on a missing-cluster bootstrap, the host lock covers substrate creation and
  the Fence is the first Kubernetes object created after API readiness;
- a foreign Fence, leftover temporary Binding, or uncertain Fence result denies
  normal and recovery mutation;
- bundle installation occurs only after Fence acquisition and bundle removal
  precedes UID/resourceVersion-guarded Fence release;
- Phase-0 Session Authorization can mutate only canary Fence and Binding names
  bound to inert canary Roles, never production recovery objects;
- normal Fence admission accepts only the reviewed exact Bootstrap username,
  never a group-only subject;
- snapshots redact all credential and Secret payload material;
- journal hashes, pre-mutation anchor, and final bundle hash verify;
- only full, verified Git commits can become known-good inputs;
- every `main` source is pinned while the graph is frozen;
- Freeze disables AutoSync, terminates every running operation, waits for idle,
  proves a quiet interval, blocks ordinary manual sync, and re-reads each UID
  and resourceVersion;
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
- a new drill cluster has API audit from creation and a non-audited existing
  cluster cannot pass Phase 0;
- the complete macOS/OrbStack drill passes before Phase 4 release.

The implementation PRs must add the recovery runbook, credential ceremony,
rotation and revocation procedure, evidence schema, permission inventory,
failure injection tests, and drill record. `task quality` remains necessary but
is not sufficient for recovery acceptance.

## Acceptance gates

This ADR remains `Proposed` until reviewers confirm:

- the upstream VAP self-protection limitation and ADR-0002 clarification are
  accurately represented;
- Recovery Operator and Session Authorizer identities, separate custody,
  rotation, availability, and effective revocation are acceptable;
- absence of X.509 Organization groups and treatment of
  `system:authenticated` permissions are explicit;
- cluster-scoped and Namespace-scoped permissions are separated and
  top-level-create granularity is not overstated;
- Canary Binding mutation and production Binding mutation have distinct exact
  Escape rules;
- one create-only Operation Fence removes the normal/recovery TOCTOU and its
  participant boundary is explicit;
- the absent-cluster exception is limited to host-locked substrate creation and
  Identity is committed only after acquiring the first cluster Fence;
- normal and recovery command paths are physically isolated;
- Binding-only suspend and exact restoration cannot create an untracked bypass;
- set normalization, raw-array patch preconditions, and canonical JSON hashing
  are deterministic;
- Freeze terminates running operations and prevents new ordinary manual syncs;
- snapshots, redaction, hashes, journal, and external anchoring are complete;
- API audit is a cluster-creation prerequisite and missing audit fails Phase 0;
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
