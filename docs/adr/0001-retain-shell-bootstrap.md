# ADR-0001: Retain Shell as the Bootstrap Stage-1 implementation

- Status: Accepted
- Date: 2026-08-15
- Deciders: repository owner
- Supersedes: none
- Superseded by: none

## Context

Atlas currently has a compact Bash 5.x Bootstrap that has passed deterministic
rendering, adoption parity, fail-closed safety, and live Kind integration tests.
A Zig Stage-1 with a thin Shell Stage-0 was considered as a future design, but
maintaining two active engines would create competing behavior and control-path
ambiguity before the existing adoption state machine is fully hardened.

The implementation language does not change the frozen External Root,
Two-Level DAG, AppProject, offline supply-chain, or Bootstrap termination
invariants.

## Decision

Shell remains the sole Bootstrap Stage-1 implementation for the current phase.

- `bootstrap/atlas` remains the only user entry point.
- Bash 5.x modules under `bootstrap/` remain the authoritative engine.
- No parallel Zig engine, language shim, or duplicate command implementation is
  permitted.
- Shell changes must remain modular, ShellCheck-clean, shfmt-clean, testable,
  fail-closed, and explicit about side effects.
- A future language migration requires a superseding ADR, behavioral contract
  parity, staged cutover, rollback criteria, and removal of the old engine.

## Consequences

Atlas can focus immediately on control-state correctness, configuration
validation, CI enforcement, and recovery behavior without a language migration.
Shell's weaker type and process abstractions must be compensated by strict
parsers, immutable inputs, narrow modules, explicit exit handling, and stronger
contract tests.

## Alternatives considered

### Zig Stage-1 plus Shell Stage-0

Deferred. It adds a compiler toolchain and a second behavior surface before the
current Bootstrap control semantics are fully specified.

### Parallel Shell and Zig implementations

Rejected because behavioral drift would make recovery authority ambiguous.

## Verification

- CI runs the repository's Shell and architecture quality gates.
- No `src/*.zig` Bootstrap engine or second executable entry point exists.
- Any proposal to change implementation language references a superseding ADR.
