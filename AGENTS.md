# Atlas Agent Contract

This file is the repository-level operating contract for automated agents and
human contributors. It does not override the normative architecture.

## Authority order

1. `docs/architecture/operating-model.md` (Architecture v1.0.2)
2. `docs/standards/gitops.md` (GitOps v1.0.3)
3. `docs/standards/network.md` (Network Standard v1.0)
4. Accepted records under `docs/adr/`
5. Implementation, tests, and runbooks

When these disagree, stop at the higher authority and propose an ADR or a
standards change. Passing tests never authorizes an architecture violation.

## Current implementation decision

Bootstrap Stage-1 remains Shell. ADR-0001 is authoritative. Do not introduce a
parallel Zig engine, turn `bootstrap/atlas` into a language shim, or translate
modules into another language without an accepted superseding ADR.

## Trust boundaries

| Scope | Boundary | Change requirement |
| --- | --- | --- |
| `gitops/root/**` | Tier-0 External Root and macro DAG | Explicit Human Judgment Gate and CODEOWNER review |
| `bootstrap/argocd/root-app.yaml.tpl` | Tier-0 instantiation | Explicit Human Judgment Gate and CODEOWNER review |
| `bootstrap/argocd/atlas-bootstrap-project.yaml` | Tier-0 trust bootstrap | Explicit Human Judgment Gate and CODEOWNER review |
| `docs/architecture/**`, `docs/standards/**`, `docs/adr/**` | Normative governance | CODEOWNER review; semantic changes require an ADR |
| `versions.lock`, `vendor/**` | Supply-chain authority | Checksum/digest verification and CODEOWNER review |
| `gitops/platform/**` | Tier-1 platform control | Architecture and conformance review |
| `gitops/workloads/**` | Tier-2 workload control | AppProject and namespace-boundary review |

Agents may read and analyze every repository file. A request to change code does
not implicitly authorize applying Tier-0 resources, changing repository rules,
deleting clusters, rotating credentials, or performing break-glass recovery.

## Required change protocol

1. Identify the controlling architecture section and trust tier.
2. Inspect the current tests and dirty worktree before editing.
3. Preserve unrelated user changes.
4. Make the smallest coherent change; do not combine governance, Tier-0, and
   implementation-language changes in one patch.
5. Check correctness, idempotency, failure recovery, offline artifacts,
   permissions, tests, and documentation.
6. Run `task check` and the affected focused suite before proposing or pushing
   a change. Run `task quality:full` for shared test infrastructure, Recovery or
   ceremony implementation, contract/schema authority, supply-chain, release,
   or unclassifiable cross-cutting changes.

An ADR is required before changing control ownership, trust boundaries,
Application nesting, canonical AppProject names, implementation language,
configuration format, offline artifact flow, or recovery authority.

## Non-negotiable safety rules

- Never commit plaintext secret material. Git may contain references only.
- Never use floating image tags or mutable CI Action tags.
- Never use `source` or `eval` to load repository configuration.
- Never introduce Helm release state; Helm is a renderer only.
- Never add a cascading Argo CD resources finalizer to the External Root.
- Never overwrite a drifted External Root in the normal Bootstrap path.
- Never treat `Healthy` tests alone as proof that a trust transition is valid.
- Keep recovery commands separate from normal Bootstrap commands.

## Local verification

```bash
task check
task quality:gitops-core # example affected suite
./bootstrap/atlas doctor --env test
./bootstrap/atlas status --env test
```

`task quality:full` is the exhaustive repository-only gate; `task quality` is
its backward-compatible alias. The required GitHub `quality` check always runs
all repository-only and disposable Kubernetes server contracts in independent
parallel shards, then fails unless every shard succeeds. Server suites are CI-
only and must not be used as a local edit loop.

`apply` is intentionally absent from the routine verification commands because
it mutates the substrate and crosses the Tier-0 boundary.
