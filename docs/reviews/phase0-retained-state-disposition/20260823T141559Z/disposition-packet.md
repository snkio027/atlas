# Phase-0 Retained-State Disposition Packet

- Schema: `atlas.io/phase0-retained-state-disposition/v1`
- Prepared: `2026-08-23T14:15:59Z`
- Actor: `501:nekoreb`
- Decision: `DESTROY-AND-RECREATE`
- Cluster: `atlas-recovery-drill-20260816t184119z-a8ccecc3`
- Context: `kind-atlas-recovery-drill-20260816t184119z-a8ccecc3`
- Target root: `/Users/nekoreb/Workspace/03_Projects/atlas-local-validation/atlas-recovery-drill-20260816t184119z-a8ccecc3`
- Inventory SHA-256: `94ea8b29e8e3f031e2ffca5d880f16acd84a68e726be68f45945c813dd7b0e9c`
- Approval projection SHA-256: `9805aab22f099fd96129e09ee8c3974a64d1e8d0d39ad346415a8181f3841e2a`
- Supersedes approval SHA-256: `e7b384904de1687e629702830bdccab3d96c72948c6541072131d1e877c2fa1c`
- Supersedes packet manifest SHA-256: `a77b6df2c3cc5a4683b0a9a179886243bff12b0ab03c30f4fbd8452f8c02d6b2`

This packet authorizes no action by itself. The only valid Human Judgment Gate
for the later disposition execution is:

```text
DISPOSE PHASE0 atlas-recovery-drill-20260816t184119z-a8ccecc3 9805aab22f099fd96129e09ee8c3974a64d1e8d0d39ad346415a8181f3841e2a
```

That Gate authorizes only the exact disposition described here. It does not
authorize Kind deletion, new cluster creation, credential issuance, recovery,
Admission activation, Ceremony execution, GitOps, Tier-0, or normal cluster
mutation.

## Supersession notice

The superseded packet formatted local Asia/Shanghai modification times with
a `Z` suffix. No retained file or out-of-scope state changed. This packet
recomputes every `mtimeUTC` from the file epoch in UTC. No destructive
operation occurred under the superseded approval.

## Read-only findings

The old Drill cluster was already deleted before this inventory:

- `kind get clusters` reports no Kind clusters through the explicit OrbStack
  Docker authority;
- no OrbStack container has a Kind cluster label;
- `https://127.0.0.1:49868` returns `connection refused` with the isolated old
  admin kubeconfig;
- the default kubeconfig contains no old Drill Context;
- the isolated admin kubeconfig still contains the old Context and endpoint;
- both the cluster-creation and Phase-0 runtime lifecycle locks are absent.

The deletion was not executed or journaled by this inventory. Its time and
actor are therefore unknown. The packet records the cluster as
`ABSENT_BEFORE_DISPOSITION`, not as successfully disposed by Atlas. If a
container, endpoint, Context, or lock reappears, execution must fail closed and
require a new packet; this packet never authorizes deleting that new state.

The retained target identity is:

| Field | Value |
| --- | --- |
| API server | `https://127.0.0.1:49868` |
| `kube-system` UID | `db942c70-4875-4ae5-9345-e00fdd4b0359` |
| CA SPKI SHA-256 | `1c53f933472ef6e5a55ae173c69194adc8719666b2d5f239212ff53ab500e6a9` |
| Creation Git revision | `a8ccecc30b89838b6df098bf62a32351886eff86` |
| Failed runtime Git revision | `653a695a828e8c5b32f882f19b18f82186d5dd9d` |
| New implementation baseline | `main@7dda2638318c8f9d4be48b41d6034526e043fa4d` |

The creation journal has six valid hash-linked entries and terminates at
`VERIFY/READY` with tip
`4aefb952cf76d92e16ca908c9c3c8a236490539ae3eac272429710e2eb59414f`.
Its pre-mutation manifest validates all five files.

The runtime journal has ten valid hash-linked entries and terminates at
`RESULT/FAILED` with tip
`f0dc522fea395d70d888c90d208358fd07c9aaf6e106b380d6eae8a5edb09fb9`.
The retired runtime approval is
`b03d6e89b22eff443c757950b2a5cced31754dbb4f710571252a446312dc52d2`;
the retired Session ID is `c5b509a911590fefef634878f085e923`.

The failure occurred after issuance and local retention of exact-user Recovery
Operator certificates for:

- current `atlas:break-glass:db942c70-4875-4ae5-9345-e00fdd4b0359:g2`;
- previous `atlas:break-glass:db942c70-4875-4ae5-9345-e00fdd4b0359:g1`.

Both certificates expired on `2026-08-16T20:14:13Z`. Their keys, certificates,
CSRs, kubeconfigs, and copied CA remain destruction targets. The Session
Authorizer directory contains only an unbound private key and copied CA; no
Authorizer CSR, certificate, kubeconfig, or authorization was created.

The journal and retained evidence show that no canary RBAC, VAP, Fence, Guard,
or temporary permission Binding installation was reached. Because the cluster
is already gone, live object absence cannot be queried and is classified as
`UNAVAILABLE_BY_CLUSTER_DELETION`, not as an API-verified absence result.

## Exact classification

`inventory.tsv` is the authority for all 42 retained files, including each
relative path, inode, owner, mode, byte size, modification time, SHA-256, and
disposition class.

| Class | Count | Required result |
| --- | ---: | --- |
| `PRESERVE_AUDIT_SEAL` | 2 | Preserve, rehash, externally anchor, never modify or reuse |
| `PRESERVE_CREATION_NEVER_REUSE` | 10 | Preserve as historical creation evidence; never use as new Ceremony input |
| `PRESERVE_RUNTIME_NEVER_REUSE` | 18 | Preserve as failed runtime evidence; retire Plan, Approval and Session ID |
| `DESTROY` | 12 | Remove only after exact path, inode, owner, mode and hash revalidation |

The four credential directories may be removed only after every `DESTROY` file
has been removed and only while their inodes remain exactly:

```text
10862027 runtime-credentials/recovery-operator
10862028 runtime-credentials/session-authorizer
10850605 runtime-credentials
10850183 credentials
```

The old target-scoped Recovery Operator generations `g1` and `g2` are
`BURNED`. The rejected old-prefix Session Authorizer identities and all old
Plan/Approval/Session values are `RETIRED / NEVER_REUSE`. This is scoped to the
old Namespace UID and CA. A new cluster with a new UID and CA may use the
separately reviewed asymmetric plan `Recovery g3/g2 + Authorizer g2/g1`, but
only through new keys, paths, evidence, Plan, Session ID and approval.

## Out-of-scope baseline

The disposition must not change these observations:

| Object | Pre-disposition state |
| --- | --- |
| OrbStack containers | none |
| Kind clusters | none |
| Default kubeconfig | inode `524781`, SHA-256 `7ab6f3c2bb4e56ab6e609fc3c786d509260802e349a409a0e4056cd4cd911117` |
| Default contexts | `kind-atlas-test`, `kind-datalab`, `orbstack` |
| `atlas-test` | Context present; endpoint `127.0.0.1:50137` refused; container absent |
| `datalab` | Context present; container absent |
| Local Atlas HEAD | `427e026b109c865e20c527a7d38b7e3c58c30747` |
| Local topology file | user-modified; SHA-256 `f849d3d073504851e9302bf0158d03bd9788cc84b436a47193e3872237865143` |

The absence of `atlas-test` predates disposition. This packet can verify that
the disposition does not modify its kubeconfig entry or other host state, but
it cannot claim a healthy four-node baseline.

## Gated execution order

1. Recompute the approval projection and inventory hashes.
2. Revalidate every retained file's path, inode, owner, mode, size and SHA-256;
   reject symlinks and any unclassified file.
3. Revalidate cluster, container, endpoint, default Context and both lock paths
   remain absent. Any reappearance fails closed; do not delete it.
4. Revalidate the default kubeconfig, local topology file and local Git state
   against the out-of-scope baseline.
5. Recompute and record the two Audit hashes and all 28 creation/runtime
   evidence hashes before any destructive operation.
6. After the exact Human Gate, remove only the 12 `DESTROY` files from
   `inventory.tsv` with per-file inode and hash preconditions.
7. Remove only the four exact empty credential directories, in the recorded
   child-to-parent inode order.
8. Verify the admin kubeconfig and runtime credential paths are absent.
9. Verify the cluster, containers, endpoint, old Context and locks remain
   absent without issuing a cluster deletion command.
10. Verify all 30 preserved hashes and the entire out-of-scope baseline remain
    unchanged.
11. Create an owner-only post-disposition record containing the Gate, exact
    deleted paths, preserved hashes, absence checks, out-of-scope checks and a
    final result hash.

APFS/SSD overwrite is not an accepted erasure claim. Effective invalidation is
provided by removal from the asserted encrypted owner-controlled store,
destruction of the credential files, expiration of the certificates, and the
already-absent cluster endpoint and CA authority.

## Completion boundary

Disposition succeeds only when every exact credential target and directory is
absent, all preserved hashes match, all out-of-scope hashes match, and the
post-disposition record is sealed. It does not close Phase-0 Runtime Closure
and it does not authorize the next cluster or Ceremony Gates.
