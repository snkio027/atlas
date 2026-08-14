# Contributing to Atlas

Atlas changes are reviewed against architecture and trust boundaries before
implementation style. Start with `AGENTS.md` and the normative documents under
`docs/`.

## Workflow

1. Create a focused branch from `main`.
2. Link an accepted ADR when the change affects architecture, control authority,
   implementation language, configuration format, or recovery semantics.
3. Keep Tier-0 changes separate from ordinary platform or workload changes.
4. Run `task quality` locally.
5. Open a pull request and wait for required checks and CODEOWNER review.

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
