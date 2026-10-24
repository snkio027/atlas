# Atlas contributor contract

Atlas is the Go implementation for single-owner local development. The original
Shell project is archived as `snkio027/atlas-legacy`; its history and uncommitted
local work remain preserved. Read `docs/architecture.md` and `docs/adr/` first.
Design proposals and implementation status must remain distinguishable.

- Inspect existing changes; preserve unrelated work.
- Keep normal Bootstrap, recovery, and drill capabilities separate. This first
  milestone implements normal Bootstrap only, for disposable test clusters.
- No plaintext credentials, kubeconfig, private keys, or generated Secret data
  may enter Git. `.state/` is local private state.
- Parse configuration strictly as data. Never source or evaluate configuration.
- Treat unavailable reads and unknown states as failures, not absence.
- Normal Bootstrap must not overwrite a drifted External Root or regain Seed
  authority after the durable handoff latch exists.
- Pin external tool versions and image digests; verify vendored artifacts.
  Runtime commands never download dependencies or execute recovery commands.
- Run `task quality` for changes and real rendering verification when rendering
  changes. Record which tests actually ran; simulated success is not live proof.
- Cluster creation, Tier-0 writes, credential use, and cleanup require a
  separately reviewed exact target and explicit user authorization. Ordinary
  build/test work does not authorize those operations.
- Use latest development evidence for local/read-only iteration. Preserve failures
  as regression tests and the S1 failure journal, not duplicate runtime bundles.
  Before discarding a stopped run, verify request intents, Git source, audit and
  current target: external mutation or uncertain impact requires full immutable
  evidence and a reviewed recovery decision. Never automatically clear a STOP lock.
  Final Gate PASS and credential/Trust Root evidence remain fully retained.
- Do not import old Atlas live state, credentials, approvals, or cluster names.
  The approved repository rename/archive preserves that history; further legacy
  code or state changes require their own scope.
- Review changes to authority, configuration, deployment scope, or recovery in
  a dedicated ADR. Initial design ADRs remain Proposed pending owner review.

No production-readiness claim is permitted until admission protection,
independent recovery, runtime integration, and release supply-chain verification
have been designed, implemented, and exercised.
