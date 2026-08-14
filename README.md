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

Repository governance starts with `AGENTS.md` and `CONTRIBUTING.md`. Run every
mandatory local gate with:

```bash
task quality
```

The mutating Kind verification is intentionally explicit:

```bash
task integration
```
