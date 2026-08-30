# ADR-0005: Define PERSONAL_LOCAL target materialization

- Status: Accepted
- Date: 2026-08-30
- Deciders: repository owner and required CODEOWNERS
- Amends: [ADR-0003](0003-bootstrap-break-glass-recovery.md) PERSONAL_LOCAL authorization flow
- Profile: PERSONAL_LOCAL / NON-PRODUCTION
- Implementation authorization: NONE
- Runtime authorization: NONE
- Supersedes: none
- Superseded by: none

## Context

ADR-0003 defines the `PERSONAL_LOCAL` rollout profile as a bounded,
single-owner alternative to the Production Argo authorization proof. The
repository-only contract can establish `PERSONAL_LOCAL_DEFINED`, while a
separately approved, target-bound live preflight is required for
`PERSONAL_LOCAL_READY`.

The current v1 Target and final Owner Gate cannot be constructed without a
pre-authorization runtime fact. The Target requires the exact `kube-system`
Namespace UID, and that UID is included in the Target projection bound by the
final Owner Gate. The UID normally requires an exact live-object read, but the
live preflight cannot begin until the final Gate has already approved the
complete Target. Historical evidence cannot prove that a previous UID still
identifies the current cluster instance.

The resulting dependency is circular:

```text
pre-authorization Target material
  -> ownerGateTargetProjectionSHA256
  -> final Owner Gate approval
  -> independent final Gate SHA
  -> ownerGateState / ownerGateSHA256
  -> targetFingerprintSHA256
  -> approvedTargetDocumentSHA256
  -> LIVE_READ_ONLY_PREFLIGHT
```

Target discovery cannot be hidden inside the final preflight or inferred from
an old target. It needs an independently Human-gated, information-acquisition
step with a smaller authority surface.

This amendment applies only to `PERSONAL_LOCAL`. It does not change the
Production Profile, recovery authority, Admission rollout, or ADR-0003 Phase
2 through Phase 4 ordering.

## Decision

If accepted, Atlas will introduce an independently approved operation:

```text
PERSONAL_LOCAL_TARGET_MATERIALIZATION
```

Its sole purpose is to obtain and attest the minimum runtime-bound facts needed
to construct the later `PERSONAL_LOCAL_READ_ONLY_PREFLIGHT` Target and Owner
Gate. It is an information-acquisition transition with bounded owner-local
control writes. It does not establish live convergence, Argo authorization,
Admission authority, recovery authority, or Production readiness.

### Profile versioning

The existing Profile remains:

```text
profileID: atlas.argocd.authorization-probe-profile/personal-local/v1
status: historical repository-only contract
live eligibility: NEVER
maximum result: PERSONAL_LOCAL_DEFINED
```

Its canonical waiver decision SHA remains historical:

```text
34e42bc31933ecf63fa5d878b611c3119415c3503481c7863e5e1cb5a4eff949
```

The only live-eligible Personal Local Profile will be:

```text
profileID: atlas.argocd.authorization-probe-profile/personal-local/v2
```

The v2 `waiverDecisionSHA256` MUST be derived from the complete canonical v2
Profile document. It MUST NOT reuse the v1 hash. Implementations MUST select a
Profile version explicitly; v1 fallback and dual authorization are forbidden.

### State, Gate, and operation taxonomy

The durable Profile result states are:

```text
PERSONAL_LOCAL_DEFINED
  -> TARGET_MATERIALIZED
  -> PERSONAL_LOCAL_READY
  -> PERSONAL_LOCAL_OBSERVING
  -> PERSONAL_LOCAL_OBSERVED
```

An attempted transition can produce:

```text
PERSONAL_LOCAL_BLOCKED
```

`PERSONAL_LOCAL_BLOCKED` is an attempt result. It does not erase or replace the
last successful durable Profile state.

The authorization checkpoints are not durable states:

```text
Materialization Owner Gate = APPROVED
Final Preflight Owner Gate  = APPROVED
```

The execution modes are not states:

```text
LIVE_TARGET_MATERIALIZATION
LIVE_READ_ONLY_PREFLIGHT
```

The transition authority is:

```text
PERSONAL_LOCAL_DEFINED
  --[Materialization Owner Gate APPROVED
     + LIVE_TARGET_MATERIALIZATION]-->
TARGET_MATERIALIZED

TARGET_MATERIALIZED
  --[Final Preflight Owner Gate APPROVED
     + LIVE_READ_ONLY_PREFLIGHT]-->
PERSONAL_LOCAL_READY
```

`TARGET_MATERIALIZED` proves only that one bounded read session produced
complete, validated, hash-bound Target-material evidence. It does not prove
live convergence, Argo hardening, authorization, or Personal Local readiness.

### Credential mechanism

The only eligible credential mode and authentication shape are:

```text
credentialMode: STATIC_IN_KUBECONFIG
authenticationShape: X509_EMBEDDED_DATA
```

The selected user's authentication key set MUST be exactly:

```text
client-certificate-data
client-key-data
```

Dynamic or external authentication is forbidden, including `exec`,
`auth-provider`, `token`, `tokenFile`, username/password, client certificate
or key paths, interactive authentication, and impersonation fields.

The credential boundary is:

```text
Credential projection or disclosure                 FORBIDDEN
Credential extraction                               FORBIDDEN
Credential persistence                              FORBIDDEN
Credential-specific hashing                         FORBIDDEN
Opaque kubeconfig-byte consumption for shape checks AUTHORIZED
Credential interpretation by approved kubectl       AUTHORIZED
```

An approved whole-file parser MAY consume kubeconfig bytes only as opaque
input needed to select the context, cluster, user name, and sorted user key
names. Credential values MUST NOT be semantically selected, decoded,
separately hashed, emitted, logged, persisted, or placed in a temporary
projection. Only the approved kubectl may interpret those values, and only to
authenticate the two Materialization requests defined below.

### Canonical Materialization Plan

The future repository-owned Plan has the ID:

```text
atlas.argocd.authorization-personal-local-target-materialization-plan/v1
```

Its ordered operation surface is:

```text
1.  validate the external Materialization Owner Gate
2.  acquire the create-once session claim
3.  validate kubeconfig custody
4.  hash the kubeconfig as one opaque file
5.  validate kubectl custody
6.  hash kubectl
7.  validate the kubectl client version
8.  project the selected context, cluster, and user identity
9.  project the selected cluster TLS authority
10. validate the selected-user authentication key shape
11. compute the embedded CA public-key/SPKI SHA-256
12. GET /version
13. GET /api/v1/namespaces/kube-system
14. revalidate file custody and whole-file hashes
15. construct and validate Materialization Evidence
16. commit the create-once terminal receipt
```

Plan arrays are ordered and normative. Canonicalization is recursive object-key
sorting, preserved array order, compact UTF-8 JSON, no trailing newline, and
SHA-256. The Materialization Gate MUST bind both the Plan ID and canonical Plan
SHA. An implementation MUST NOT add, reorder, skip, or infer operations.

### Materialization Owner Gate

The distinct Gate is:

```text
gateID: atlas.argocd.authorization-personal-local-target-materialization-owner-gate/v1
operation: PERSONAL_LOCAL_TARGET_MATERIALIZATION
decision: NOT_AUTHORIZED | APPROVED
```

It MUST bind at least:

```text
schemaVersion
gateID
operation
decision
rolloutProfile
profileID
contractGitCommit
authorityBaseline
repositoryURL
waiverDecisionSHA256
materializationPlanID
materializationPlanSHA256
materializationEvidenceSchemaID
environmentName
clusterName
kubeContext
kubeconfigPath
kubectlPath
kubectlVersion
kubernetesVersion
sessionReceiptRoot
sessionID
issuedAt
expiresAt
```

The Gate MUST NOT require values that the operation exists to discover:

```text
kubeconfigSHA256
kubectlSHA256
apiServerURL
apiServerCASPKISHA256
kubeSystemNamespaceUID
```

The expected canonical Gate SHA MUST be supplied to the executor independently
of the Gate document. The executor cannot create, approve, or infer its own
authoritative Gate.

### Local custody and projection surface

The kubeconfig MUST be the exact Owner-approved absolute path, a regular
non-symlink file owned by the invoking Owner UID, readable by that Owner, and
have no group or other permission bits. Modes `0400` and `0600` satisfy this
contract.

The kubectl MUST be the exact Owner-approved absolute path, a regular
non-symlink file, executable by the invoking user, not group- or other-writable,
hash-bound, and version-bound. It need not be owned by the invoking user.

ADR-0005 v1 does not define portable ACL semantics. The before/after custody
and hash checks detect stable execution-boundary drift; they do not claim to
eliminate every instantaneous filesystem TOCTOU race. That residual risk is
accepted only for the single-owner Personal Local Profile.

The only permitted kubeconfig projections establish:

```text
selected context name
selected context cluster
selected context user name
selected cluster name
API Server URL
insecure-skip-tls-verify
certificate-authority path
certificate-authority-data
selected user sorted key-name set
```

Ordinary fields use non-raw projections. The exact embedded
`certificate-authority-data` is the only raw field projection. An external CA
path, missing embedded CA, or `insecure-skip-tls-verify=true` fails closed. The
embedded CA may be decoded only to calculate its public-key/SPKI SHA-256.

Ambient kubeconfig fallback, context discovery, `.users` value projection,
credential output, and any unapproved path are forbidden.

### Exact Kubernetes request surface

After local authority validation, Materialization permits exactly:

```text
GET /version
GET /api/v1/namespaces/kube-system
```

The Namespace response MUST have `apiVersion=v1`, `kind=Namespace`,
`metadata.name=kube-system`, and a valid non-empty UID. `/version` MUST prove
the locked Kubernetes version.

The following counters MUST remain zero:

```text
collection reads
Secret reads
Argo API or CLI calls
Kubernetes/GitOps/runtime mutations
unexpected requests
```

The approved kubectl may use the eligible embedded X.509 credentials only for
these two exact requests. API discovery, troubleshooting reads, ConfigMap or
workload reads, `kubectl auth can-i`, impersonation, port-forward, exec, and
every mutation are forbidden.

Materialization performs bounded owner-local session control writes. Those
writes are not Kubernetes, GitOps, recovery, or workload mutations.

### Replay contract

The Gate's `sessionReceiptRoot` MUST be an Owner-selected canonical absolute
path to an owner-controlled, `0700`, non-symlink directory.

After pure Gate and input validation, but before reading the kubeconfig, using
credentials, or accessing an API, the executor MUST atomically create:

```text
<sessionReceiptRoot>/<sessionID>/claim.json
```

The session directory is create-once. Any existing entry for the same
`sessionID` fails closed before kubeconfig read, credential use, or API access.
The claim canonically binds the session ID, Materialization Gate SHA, Plan SHA,
Git commit, waiver SHA, claim time, and `CLAIMED` state.

Every claimed session is consumed. It may never execute again, whether it
ends as success, failure, or interruption. A conformant executor creates one
terminal document without overwriting prior state:

```text
terminal.json: MATERIALIZED | BLOCKED
```

A successful terminal binds the canonical Materialization Evidence SHA. A
blocked terminal records only a non-sensitive failure classification. If the
executor cannot write a terminal document, the existing claim still consumes
the session. Retry requires a new Gate, expected Gate SHA, session ID, claim,
Evidence, and final Gate.

These owner-local receipts are replay and audit controls. They are unrelated
to the ADR-0002 Adoption Receipt and MUST NOT share its names or semantics.

### Materialization Evidence

The future Evidence contract ID is:

```text
atlas.argocd.authorization-personal-local-target-materialization-evidence/v1
```

Its only successful result is `TARGET_MATERIALIZED`. It MUST bind the v2
Profile and waiver, Git authority, Plan ID/SHA, Materialization Gate SHA,
session claim SHA, session and time, context, credential mode and shape,
kubeconfig SHA, kubectl SHA/version, Kubernetes version, API endpoint, CA SPKI,
`kube-system` UID, exact local operations, exact Kubernetes reads,
completeness, and assurance classifications.

Exported Evidence MUST NOT contain absolute host paths or credential material.
The Materialization Gate SHA transitively binds the Owner-selected paths.

Completeness MUST prove exactly two expected and executed Kubernetes requests,
with zero skipped, collection, Secret, Argo, mutation, and unexpected calls.
Evidence uses exact-key schema validation and canonical compact JSON with
recursively sorted object keys, preserved array order, no trailing newline,
and SHA-256. Sensitive-content detection and time-order validation fail closed.

### Final PERSONAL_LOCAL v2 provenance

The future Final Target v2 MUST add:

```text
targetMaterializationEvidenceSHA256
```

The construction order is:

```text
verified Materialization Evidence
  -> canonical Evidence SHA
  -> Final Target pre-Gate projection
  -> ownerGateTargetProjectionSHA256
  -> Human approves Final Owner Gate
  -> independent final Gate SHA
  -> ownerGateState / ownerGateSHA256
  -> targetFingerprintSHA256
  -> approvedTargetDocumentSHA256
```

`ownerGateTargetProjectionSHA256` is derived after Target Materialization.
`targetFingerprintSHA256` is derived after Gate assembly and before live
execution. `approvedTargetDocumentSHA256` is derived after final Target
assembly and before live execution.

The final preflight MUST receive the Materialization Owner Gate and
Materialization Evidence as independent documents in addition to the Final
Target, Final Owner Gate, independently supplied final Gate SHA, and expected
Git commit. It MUST:

1. validate the Materialization Gate schema and semantics;
2. recompute its canonical SHA and compare it with the Evidence;
3. validate the Evidence schema and semantics;
4. recompute its canonical SHA and compare it with the Final Target;
5. compare all materialized Target facts with the Evidence;
6. validate the Final Gate and independent final Gate SHA; and
7. independently revalidate the current local and live Target authority.

The final preflight also MUST validate the owner-local replay artifacts through
the exact `sessionReceiptRoot` retained in the Materialization Gate:

```text
canonicalSHA(claim.json)
  == Evidence.materializationSessionClaimSHA256

terminal.json.state
  == MATERIALIZED

terminal.json.materializationEvidenceSHA256
  == canonicalSHA(Materialization Evidence)
```

These are local filesystem reads and do not expand the Kubernetes request
surface. Evidence SHA alone cannot substitute for validating the
Materialization Gate, claim, or terminal documents.

### Final preflight request invariant

The Final Personal Local preflight remains exactly:

```text
1 x GET /version
26 x exact-object GET
  = 13 x snapshot-before
  + 13 x snapshot-after

collection reads = 0
Secret reads     = 0
extra reads      = 0
mutations        = 0
```

The `kube-system` UID MUST be revalidated using the Namespace read already
present in each snapshot. It MUST NOT cause a twenty-seventh object read.

```text
INV-TM-09

Execution-time revalidation MUST NOT add a Kubernetes request outside the
existing final-preflight /version plus 13-by-2 exact read surface.
The kubeSystemNamespaceUID MUST be revalidated through the kube-system reads
already present in snapshot-before and snapshot-after.
```

### Freshness

The final Gate may be issued only when:

```text
0 seconds
<= finalOwnerGate.issuedAt - materializationEvidence.completedAt
<= 900 seconds
```

This materialization-to-approval limit is independent from final Gate expiry
and full execution-time Target revalidation.

### Failure and retry semantics

Every failure or uncertainty produces `PERSONAL_LOCAL_BLOCKED` for the
attempt. This includes repository, Profile, waiver, Gate, Plan, session claim,
credential shape, custody, tool, context, TLS, version, UID, request inventory,
Evidence, receipt, sensitive-content, time, network, or provenance mismatch.

A failed attempt does not alter the last successful durable Profile state. No
partial Materialization Evidence may be carried forward. Every retry needs a
new action-specific Materialization Gate and all new downstream session and
evidence artifacts.

### Independence invariants

```text
INV-TM-01  Materialization authorization cannot authorize the final preflight.
INV-TM-02  Materialization Evidence cannot approve the Final Owner Gate.
INV-TM-03  Human review of complete Materialization Evidence precedes the
           Final Owner Gate.
INV-TM-04  The executor cannot generate its own expected Gate SHA.
INV-TM-05  TARGET_MATERIALIZED does not imply PERSONAL_LOCAL_READY.
INV-TM-06  Final preflight independently revalidates materialized authority.
INV-TM-07  Changed material requires a new Gate, session, Evidence, and final
           Gate.
INV-TM-08  Production authorization and recovery semantics remain unchanged.
INV-TM-09  Final revalidation adds no Kubernetes request outside /version and
           the existing 13-by-2 exact snapshots.
```

### Production and rollout boundary

All states remain `PERSONAL_LOCAL / NON-PRODUCTION`. This ADR does not alter:

```text
Argo API authorization     RUNTIME_UNPROVEN
Production recovery        NOT_AUTHORIZED
INV-02 Runtime Closure     NOT COMPLETE
Identity v2 / Receipt      NOT IMPLEMENTED
```

It does not authorize Materialization execution, kubeconfig or credential use,
cluster access, Admission activation, Signal, Receipt, Phase 1C, Cilium, or any
runtime mutation. ADR-0003 Phase 2 to Phase 3 to Phase 4 ordering remains
binding.

## Consequences

Positive consequences are:

- the pre-authorization Target circular dependency is removed without
  weakening the final Gate;
- Target runtime fields receive machine-verifiable provenance rather than
  manual transcription;
- credential use and credential disclosure have distinct, testable rules;
- dynamic credential providers cannot create hidden network authority;
- the exact Materialization and final preflight request surfaces are bounded;
- create-once owner-local claims make session replay fail closed for conformant
  executors; and
- v1 remains historically interpretable while v2 has one unambiguous live
  contract.

Costs and limitations are:

- Personal Local now has two Human Gates and two evidence classes before
  Admission observation;
- the owner must retain Materialization Gate, Evidence, claim, and terminal
  artifacts through final preflight validation;
- local file checks detect stable boundary drift but not every instantaneous
  race; and
- none of these assurances qualifies Production recovery or multi-operator
  custody.

## Alternatives considered

### Reuse a historical Namespace UID

Rejected because it cannot prove that the current target cluster has not been
replaced.

### Let the final preflight discover the UID before validating its Gate

Rejected because it makes an unauthorized live read part of Target
construction and lets the Target participate in authorizing itself.

### Put unknown runtime values in the Materialization Gate

Rejected because placeholders do not form an exact authority commitment and
move rather than remove the circular dependency.

### Allow dynamic credential providers

Rejected because helper execution, interactive authentication, and additional
network traffic cannot fit the exact two-request Materialization surface.

### Modify PERSONAL_LOCAL v1 in place

Rejected because the same Profile ID and waiver meaning would describe two
different contracts.

### Trust only the Materialization Evidence SHA

Rejected because the final validator must also prove which approved Gate,
paths, context, Plan, and session authorized the Evidence.

### Permit reusable Materialization sessions

Rejected because one Gate could produce multiple competing evidence documents.

## Verification

Acceptance of this ADR would authorize only separately reviewed repository
implementation. It would not authorize a real Materialization execution.

Implementation MUST be split from this governance change and MUST include:

- preserved v1 and explicit v2 Profile definitions;
- canonical Plan, Gate, Evidence, Target, and final Gate/Evidence schemas;
- a repository-only Materialization executor and offline validator;
- fake, hash-bound kubeconfig and kubectl backends;
- exact credential-shape and credential non-disclosure tests;
- create-once claim, replay, interruption, and terminal receipt tests;
- Profile v1 rejection and v2 waiver-binding tests;
- Gate and Evidence provenance tests;
- stable custody-drift counterexamples;
- exact two-request Materialization tests;
- proof that final UID revalidation reuses snapshot reads and adds no request;
- sensitive-content, incomplete-result, and time-order negative tests;
- reachability assertions proving no live Kustomization or Application reaches
  the implementation definitions; and
- the normal repository Quality gate.

The first real Materialization requires another explicit, target-bound Human
Judgment Gate after the repository-only implementation is merged and verified.
