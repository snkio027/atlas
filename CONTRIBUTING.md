# Contributing to Atlas

Atlas changes are reviewed against architecture and trust boundaries before
implementation style. Start with `AGENTS.md` and the normative documents under
`docs/`.

## Workflow

1. Create a focused branch from `main`.
2. Link an accepted ADR when the change affects architecture, control authority,
   implementation language, configuration format, or recovery semantics.
3. Keep Tier-0 changes separate from ordinary platform or workload changes.
4. Run `task check` and the affected focused quality suite locally.
5. Run `task quality:full` when the change affects shared test infrastructure,
   Recovery or ceremony implementation, contract/schema authority,
   `versions.lock`, `vendor/`, a release, or an unclassifiable cross-cutting
   surface.
6. Open a pull request and wait for the required aggregate `quality` check and
   CODEOWNER review. Its independent CI shards remain exhaustive on every pull
   request and `main` push.

Focused suite entrypoints are intentionally capability-sized rather than one
task per script:

```bash
task quality:bootstrap
task quality:recovery-drill
task quality:gitops-core
task quality:authorization-probe
task quality:personal-local-static
task quality:target-materialization
task quality:final-v2-preflight
```

The two `quality:server-*` tasks are restricted to GitHub Actions because they
create and remove dedicated disposable Kind clusters. They are not local edit
loop commands. `task quality` remains a backward-compatible alias for
`task quality:full`.

For Bootstrap lifecycle changes, record an explicitly approved
`task integration` run when a suitable local Kind environment is available.
The integration task is mutating and is not implied by the normal quality gate.

Direct pushes, force pushes, automatic dependency merges, and bypassing required
checks are not part of the normal contribution path.

## Pull request evidence

Describe:

- the governing architecture section;
- affected trust tier and failure domain;
- behavior before and after the change;
- idempotency and interrupted-operation behavior;
- offline and supply-chain impact;
- permissions or secret impact;
- tests run and any verification that could not be performed.

## Commit scope

Prefer one coherent concern per change set. In particular, do not combine:

- Tier-0 trust changes with platform feature work;
- recovery behavior with the normal Bootstrap path;
- architecture decisions with their full implementation;
- generated upstream artifacts with unrelated first-party edits.

Vendored artifacts must remain byte-for-byte attributable to their locked
upstream release. Do not reformat vendored files.
