# Atlas

Atlas is a local Internal Developer Platform baseline for cloud-native
streaming and lakehouse workloads. Its control model is defined by the frozen
architecture and GitOps standards under `docs/`.

Bootstrap is implemented as a small Bash 5 program:

```bash
task bootstrap:doctor
task bootstrap:render
task bootstrap:status
task bootstrap:apply
```

See `bootstrap/README.md` for its safety, supply-chain, and Tier-0 contracts.

Repository governance starts with `AGENTS.md` and `CONTRIBUTING.md`. Use the
fast edit loop and the focused suite for the area being changed:

```bash
task check
task quality:gitops-core # example affected suite
```

`task quality:full` runs all 31 repository-only commands. The backward-
compatible `task quality` alias does the same. Use the exhaustive local gate
for shared test infrastructure, Recovery or ceremony implementation,
contract/schema authority, supply-chain, release, and cross-cutting changes;
the required GitHub `quality` check always runs every repository and disposable
server contract in parallel.

The mutating Kind verification is intentionally explicit:

```bash
task integration
```
