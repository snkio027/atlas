# ADR-0004: Bound recovery principal identities for X.509

- Status: Accepted
- Date: 2026-08-17
- Deciders: repository owner and required CODEOWNERS
- Amends: [ADR-0003](0003-bootstrap-break-glass-recovery.md) principal identity contract
- Supersedes: none
- Superseded by: none

## Context

ADR-0003 defines two independent X.509 usernames for Phase-0 recovery. During
the first Human-gated runtime ceremony on the audited disposable drill cluster,
the Recovery Operator certificates were issued, but OpenSSL rejected the
Session Authorizer certificate request before CSR creation:

```text
ASN1_mbstring_ncopy:string too long:maxsize=64
```

The rejected ADR-0003 username was:

```text
atlas:recovery-authorizer:<kubeSystemNamespaceUID>:g<generation>
```

With a canonical 36-byte Kubernetes Namespace UID and generation `g2`, this
username is 65 ASCII bytes. It cannot be represented in the certificate Common
Name used by Atlas. The failure occurred before the Session Authorizer obtained
a certificate, RBAC binding, or recovery mutation authority. There is no live
or historical authorization that requires migration or compatibility support.

[Kubernetes X.509 authentication](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#x509-client-certs)
maps the certificate Subject Common Name to the username and Organization
values to groups. Subject Alternative Name does not replace that username
mapping. Kubernetes also places a successfully authenticated certificate in
`system:authenticated`, even when the certificate omits Organization. Atlas
must therefore use a valid Common Name, continue to omit Organization, and
verify the effective group-derived permission baseline.

This is a recovery-authority amendment to Accepted ADR-0003, not a wording
clarification. At acceptance time, Phase-0 Runtime Closure remained `NO-GO`
until the amendment was implemented and a new ceremony succeeded. The retained
drill state, credentials, plan, evidence session, and Human Gate from the
failed ceremony were not eligible for reuse.

## Implementation status

The amendment is implemented, contract-tested, and runtime-verified. A new
Human-gated ceremony using fresh target identity, plan, credentials, and
evidence succeeded, and Phase 0 is now `COMPLETE / FROZEN` as recorded in the
[Phase-0 Recovery Authority runbook](../runbooks/recovery-phase0.md).

This closure verifies only the Phase-0 canary recovery surface. It does not
activate production Admission, Signal, Receipt, receipt-aware Bootstrap, or
any later ADR-0003 rollout phase.

## Decision

### Canonical identities

ADR-0003's Recovery Operator identity remains unchanged:

```text
atlas:break-glass:<kubeSystemNamespaceUID>:g<generation>
```

It MUST continue to match this ASCII grammar exactly:

```text
^atlas:break-glass:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:g[1-9][0-9]{0,5}$
```

ADR-0003's Session Authorizer identity is replaced by:

```text
atlas:session-authz:<kubeSystemNamespaceUID>:g<generation>
```

The Session Authorizer username MUST match this ASCII grammar exactly:

```text
^atlas:session-authz:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}:g[1-9][0-9]{0,5}$
```

The shared rules are:

- `<kubeSystemNamespaceUID>` is the exact lowercase, hyphenated, 36-byte UID
  read from the target cluster's `kube-system` Namespace;
- `<generation>` is an unpadded decimal integer from `1` through `999999`,
  represented by `g[1-9][0-9]{0,5}`;
- the complete username is ASCII-only and at most 64 bytes;
- case folding, Unicode normalization, alternate UID encodings, leading zeroes,
  signs, whitespace, and trailing data are forbidden;
- the certificate Subject Common Name equals the complete canonical username
  byte for byte and the Subject contains no Organization;
- RBAC continues to bind an exact `User` subject. Group-based recovery mutation
  authority remains forbidden.

The byte projections are principal-specific:

- Recovery Operator has a 56-byte fixed projection and produces a complete
  username of 57 through 62 bytes;
- Session Authorizer has a 58-byte fixed projection and produces a complete
  username of 59 through 64 bytes.

These ASCII, exact-format, generation-range, and 64-byte Common Name checks
apply to every Recovery Operator and Session Authorizer generation planned by
a ceremony. This amendment changes only the Session Authorizer prefix and
makes the shared bounds explicit; it does not combine principals, expand
permissions, or change custody.

### Pre-Gate validation order

The recovery command MUST complete the following sequence before presenting a
Human Judgment Gate:

1. perform read-only target discovery and read the `kube-system` Namespace UID;
2. construct every current and previous Recovery Operator and Session
   Authorizer username required by the plan;
3. validate the canonical grammar, target UID equality, ASCII bytes, generation
   range, and complete Common Name byte length for every username;
4. use only those validated usernames to construct the plan and Human Gate
   projection.

Any validation error fails closed before creation of a credential directory,
private key, CSR, certificate, Kubernetes object, or other cluster mutation.
Partial validation is not sufficient: every planned principal must pass before
the workflow may proceed.

After Gate approval and before credential issuance, the recovery command MUST
repeat read-only target discovery against the current live API authority. It
MUST re-read the live `kube-system` Namespace UID and re-derive the complete
target fingerprint, including the API endpoint and API server CA SPKI binding.
The live fingerprint and UID MUST equal the approved target and plan exactly.

The command MUST then reconstruct every current and previous principal from the
live UID, validate all identity rules again, and compare each result byte for
byte with the approved Plan. Unavailable discovery, unreadable live state, or
any target, UID, CA, principal, or plan drift fails closed before creation of a
credential directory, private key, CSR, certificate, Kubernetes object, or
other cluster mutation. Comparing only approved or previously stored values is
not a valid Post-Gate revalidation.

### Single identity projection

The implementation MUST derive the canonical Session Authorizer username once
and propagate the exact value to all consumers:

- X.509 Subject Common Name and post-issuance certificate inspection;
- Kubernetes authentication identity verification;
- RBAC `subjects[].name` fields;
- ValidatingAdmissionPolicy CEL expressions and authorization probes;
- plan, Human Gate, journal, audit correlation, evidence projections, and
  retained-state inventory;
- contract tests, runtime tests, and the Phase-0 runbook.

No consumer may independently reconstruct, truncate, hash, normalize, or alias
the username. A mismatch between any two projections is `DRIFTED` and denies
recovery mutation.

### No compatibility or migration path

Atlas MUST NOT accept, issue, bind, or authorize the rejected
`atlas:recovery-authorizer:` form. There is no dual-name transition and no
fallback authorization path.

The failed ceremony never created a certificate or RBAC binding for that
username, so there is no identity to migrate. Its retained Recovery Operator
certificates may still authenticate and inherit the ordinary
`system:authenticated` baseline, but they have no Recovery Mutation Authority
and MUST NOT be reused in a later ceremony.

Acceptance of this ADR authorizes implementation of the amended identity
contract in a separately reviewed change. It does not authorize credential
issuance, retained-state cleanup, Admission activation, Fence or permission
creation, cluster mutation, or a Phase-0 ceremony.

The acceptance change MUST:

- add a reciprocal `Amended by: ADR-0004` link to ADR-0003;
- replace ADR-0003's active Session Authorizer identity and validation contract
  with this decision while retaining an auditable link to the amendment;
- keep ADR-0004 and the ADR index status synchronized.

After implementation, retained-state disposition and a new ceremony require
independent Human Judgment Gates. The rerun MUST use new private keys,
certificates, plan, evidence session, and approval values.

## Consequences

- The Session Authorizer has a deterministic X.509-compatible username for all
  supported generations.
- Invalid identities fail before sensitive local state or cluster state is
  created.
- Principal identity remains bound to the full `kube-system` Namespace UID; no
  lossy digest or external lookup table is introduced.
- RBAC, Admission, audit, evidence, and credentials share one exact identity.
- The rejected name remains historical evidence only and can never gain a
  compatibility authorization path.
- Generation `1000000` and above require a new ADR before use. Atlas must not
  silently shorten another field or roll over the generation counter.
- Phase-0 Runtime Closure remained blocked until the amendment was implemented
  and a completely new Human-gated ceremony succeeded. That gate is now
  satisfied by the evidence recorded in the Phase-0 runbook.

## Alternatives considered

### Retain `atlas:recovery-authorizer:`

Rejected because the minimum valid username is 65 bytes and cannot be used as
the required X.509 Common Name.

### Put the username in Subject Alternative Name

Rejected because Kubernetes' client-certificate authenticator derives the
username from Subject Common Name. This would change the authentication model
rather than repair the current one.

### Put an authorization identity in Organization

Rejected because Organization becomes a Kubernetes group. It would reintroduce
group-based recovery authority, which ADR-0003 explicitly forbids.

### Hash or compact the Namespace UID

Rejected because it weakens direct target binding and introduces collision,
encoding, and evidence-correlation rules without necessity. The shorter prefix
retains the full canonical UID.

### Truncate the Common Name during certificate issuance

Rejected because certificate, RBAC, Admission, plan, and evidence identities
would diverge. Silent truncation is a failure-open identity transform.

### Permit both old and new usernames

Rejected because the old username never obtained an identity or authority and
cannot be represented under the selected X.509 model. Dual authorization would
only expand the recovery trust boundary.

## Verification

The implementation change MUST add deterministic tests that prove:

- `atlas:break-glass:<canonical-uid>:g1` is accepted at 57 bytes;
- `atlas:break-glass:<canonical-uid>:g999999` is accepted at 62 bytes;
- `atlas:session-authz:<canonical-uid>:g1` is accepted at 59 bytes;
- `atlas:session-authz:<canonical-uid>:g999999` is accepted at exactly 64
  bytes;
- a seven-digit generation producing 65 bytes is rejected;
- generation zero, leading zeroes, signs, whitespace, empty digits, and values
  above `999999` are rejected;
- uppercase, compact, malformed, foreign, missing, or otherwise non-canonical
  Namespace UIDs are rejected;
- non-ASCII input is rejected under both `LC_ALL=C` and an available UTF-8
  locale;
- any Common Name longer than 64 bytes is rejected before the Human Gate,
  credential-directory creation, credential-producing OpenSSL invocation,
  mutating kubectl invocation, or other cluster mutation;
- every planned current and previous principal is validated, and one invalid
  principal rejects the complete plan;
- Post-Gate target discovery re-reads the live target fingerprint and Namespace
  UID; API, CA, UID, or availability drift is rejected before credential
  creation or mutation, while an exact live match proceeds;
- rendered RBAC, VAP CEL, plan, evidence, and runbook projections contain the
  exact new username and no active reference to the rejected form;
- the issued certificate Subject and Kubernetes authenticated username equal
  the approved username byte for byte and contain no Organization;
- effective permissions before activation and after revocation contain only the
  accepted `system:authenticated` non-mutation baseline;
- no compatibility RoleBinding, policy exception, alias, or dual-subject path
  exists.

The amended implementation passed `task quality` and the existing Phase-0 mock
contracts. The retained-state disposition and new execution used separate
Human Judgment Gates. The successful audited disposable-cluster ceremony
closed Phase-0 Runtime Closure; its non-secret evidence identifiers and digests
are recorded in the Phase-0 runbook.

Reviewers accepted this ADR after confirming the grammar, byte boundary,
failure ordering, single-projection rule, absence of a compatibility path, and
the independent post-implementation Human Gates.
