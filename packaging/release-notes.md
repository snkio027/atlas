Atlas local development release for one owner on Apple Silicon macOS + OrbStack.
MIT licensed. This is not production, high availability, independent disaster
recovery, multi-tenancy, automatic recovery or an in-place upgrade release.

Verify the archive checksum and GitHub attestation against `snkio027/atlas`,
`.github/workflows/release.yml`, and the exact tag before extraction/execution.
The archive contains atlas-install, a bound runtime manifest, the first-install
guide, licenses and checksums. The installer needs no source checkout or Go/Task.
The repository contains the separate platform/application maintenance CLI;
optional data capabilities and permanent native-client access are post-install
changes and are not automatically activated by this archive.

This release is initially a draft. Promote the same archive only after exact-tag
Quality, archive/attestation checks and the reviewed validation scope are recorded.
Do not rebuild or replace accepted bytes. Historical r2 runtime evidence remains
bound to its original implementation; a new tag does not retroactively retest it.
The final public release description must identify both runtime and incremental
validation commits and any unproven archive/host portability limits.
