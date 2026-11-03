# Repository dependency candidate — 2026-11-03

This candidate upgrades current product inputs from main
`8586a3efe4627144f4750e6e513ac6c9a2dbd973`. It does not upgrade the running
installation. Source integration, release publication and instance acceptance
remain distinct. The new permission/configuration differences are reviewed in
[Proposed ADR-0026](adr/0026-dependency-upgrade-20261103.md).

## Selected versions

The preceding review covered 44 component groups and the capability dependency
graph. These are the selected coordinated upgrades, not floating latest inputs.

| Dependency | Previous | Candidate |
| --- | --- | --- |
| Go / YAML module | 1.27.2 / v3.0.5 | 1.27.3 / v3.0.6 |
| Helm / kubectl | 4.3.0 / 1.37.1 | 4.3.1 / 1.37.2 |
| kubectl bundled Kustomize | 5.8.1 | 5.8.1 |
| Argo CD / chart | 3.5.4 / 10.10.2 | 3.5.7 / 10.11.6 |
| Redis, Argo and shared service | 8.10.2-alpine | 8.10.3-alpine |
| Cilium agent, operator and chart | 1.20.2 | 1.20.4 |
| cert-manager chart and four images | 1.21.2 | 1.21.3 |
| Envoy Gateway / chart / egctl | 1.9.2 | 1.9.3 |
| Envoy proxy / ratelimit | distroless-v1.39.3 / 0482748e | distroless-v1.39.6 / 5ae0682e |
| Sealed Secrets / kubeseal / chart | 0.40.0 / 0.40.0 / 2.20.0 | 0.41.2 / 0.41.2 / 2.22.0 |
| SeaweedFS | 4.48 | 4.52 |
| Flink Operator / chart | 1.16.0 | 1.16.1 |
| kube-prometheus-stack | 92.3.0 | 92.17.1 |
| Prometheus Operator / config-reloader | 0.94.1 | 0.94.2 |
| Prometheus / Alertmanager | 3.15.0-distroless / 0.34.1 | 3.15.1-distroless / 0.34.2 |
| Grafana / bundled chart | 13.2.3-distroless / 13.5.0 | 13.3.0-distroless / 14.3.1 |
| Loki / Alloy | 3.7.8 / 1.20.1 | 3.7.9 / 1.21.3 |

Argo chart's default app is 3.5.6; Seed and self-management explicitly use the same
3.5.7 digest. Gateway's exact 1.9.3 chart selects ratelimit 5ae0682e. Envoy stays
on the supported 1.39 line, rather than selecting the independent latest minor.
Upstream archives and bundled Chart.lock/subcharts are unmodified. Monitoring
includes kube-state-metrics chart 8.8.0 and node-exporter chart 4.60.1 with their
unchanged runtime images; Windows exporter remains disabled.

## Retained versions and compatibility

- Kind 0.33.0 and its official `kindest/node:v1.37.0` digest remain fixed.
  Official node tags for 1.37.1 and 1.37.2 returned HTTP 404 during review;
  kubectl 1.37.2 does not upgrade the cluster's Kubernetes version.
- Gateway API standard CRDs remain 1.6.3. Version 1.7 is outside the selected
  Envoy Gateway 1.9 compatibility line.
- Strimzi 1.3.0, Kafka 4.3.1 and its complete required image directory remain
  fixed. CloudNativePG 1.30.1/chart 0.29.1, PostgreSQL 18.6 and Flink 2.3.0 Java17
  remain fixed; the Flink Operator patch supports the existing runtime line.
- Task 3.54.0, yq 4.54.1, Lua 5.5.1, actionlint 1.7.12, GitHub Action SHA pins,
  Kind storage images, Grafana sidecar, node-exporter and kube-state-metrics
  runtime images retain the reviewed current versions.
- BusyBox 1.38.0 retains the prior owner exception. Upstream labels it unstable;
  it is not reclassified as a stable release.
- Kubernetes 1.37 remains outside formal support in the selected Cilium, Envoy
  Gateway, cert-manager and CNPG matrices (CNPG distinguishes tested from
  supported). Local checks cannot establish that this combination works live.

Host Docker/OrbStack, Git, gh, Python and global tool installations are outside
this change. Frozen OT-1 uses its original Helm 4.2.3, kubectl 1.36.3 and yq 4.53.6.
Its source snapshots, old STOP records and original evidence are not regenerated.

## Preparation and verification

[The input and render inventory](evidence/dependency-upgrade-20261103.json)
records publisher checksum checks, anonymous OCI manifest/config checks for
Linux ARM64, unchanged resource identities and the exact RBAC increment.
Tool archives/binaries are pinned. The YAML module was downloaded with Go's
checksum database enabled; only its module vendor subtree was updated, leaving
Helm/CRD vendor inputs separately managed and verified. Builds remain offline.
These checks do not claim image signature verification or release attestation.

The first render correctly refused Flink's old RBAC digest. Independent old/new
renders confirmed only six objects' version labels changed; the reviewed digest
was updated without weakening the shape gate. Existing tamper rejection tests
and the new finite monitoring-finalizer regression cover these boundaries.
The subsequent platform check rejected the old ACME solver digest embedded in
cert-manager arguments; all four explicit cert-manager image values were then
updated together. The unlocked-image rejection remains enabled.
YAML merge/alias rejection remains covered by the strict parser tests.

Logging and Redis isolated validators now read the current authored YAML through
the version-checked local yq; their stale JSON paths were exposed during upgrade
preparation. These tests use no Kubernetes access. Any Redis test identity is
ephemeral synthetic material, never an instance credential.

Run `task quality` for the complete repository contract: source/workflow checks,
race tests, real deterministic Helm/Kustomize rendering, frozen OT-1, experiments,
data contracts, packaging and Web OCI reproducibility. Additional checks use
`task build`, `task build:matrix`, locked Loki/Alloy config validators, Redis
TLS/auth probes and a local D1 development package. Actual execution results and
the exact candidate binding are retained in the local quality report and PR;
this document describes required coverage, not a predeclared PASS.

No cluster creation, live credential read, deployment, migration, key rotation,
cleanup or Bootstrap re-entry is part of the repository upgrade. Live adoption
requires a separate exact-target plan, backups and validation of data/services,
especially Grafana persistence and Alertmanager's stricter cross-origin behavior.

## Upstream references

- [Kind 0.33.0](https://github.com/kubernetes-sigs/kind/releases/tag/v0.33.0),
  [Kubernetes version skew](https://kubernetes.io/releases/version-skew-policy/).
- [Cilium support](https://docs.cilium.io/en/stable/network/kubernetes/compatibility/),
  [Gateway matrix](https://gateway.envoyproxy.io/news/releases/matrix/),
  [cert-manager releases](https://cert-manager.io/docs/releases/),
  [CNPG support](https://cloudnative-pg.io/docs/1.30/supported_releases/).
- [Argo chart](https://github.com/argoproj/argo-helm/releases/tag/argo-cd-10.11.6),
  [Gateway 1.9.3](https://github.com/envoyproxy/gateway/releases/tag/v1.9.3),
  [Monitoring chart](https://github.com/prometheus-community/helm-charts/releases/tag/kube-prometheus-stack-92.17.1).
- [Alertmanager 0.34.2](https://github.com/prometheus/alertmanager/releases/tag/v0.34.2),
  [Loki 3.7.9](https://github.com/grafana/loki/releases/tag/v3.7.9),
  [Alloy 1.21.3](https://github.com/grafana/alloy/releases/tag/v1.21.3).
