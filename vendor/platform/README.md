# Development platform inputs

These are unmodified, fixed-version upstream inputs. Rendering never downloads
charts, CRDs or images. Provenance and SHA-256 are recorded in
`platform/development/versions.lock.json`.

- Cilium 1.20.4 and cert-manager 1.21.3 chart tarballs: SHA-256 checked against
  their publishers' Helm indexes on 2026-11-03.
- Envoy Gateway 1.9.3 chart: pulled from the locked OCI manifest documented in
  the lock; local tarball SHA-256 recorded. All installed and dynamically
  configured image references are overridden with resolved digests.
- Envoy Gateway 1.9.3 `install.yaml`: checked against GitHub release asset digest;
  used ONLY to extract `gateway.envoyproxy.io` CRDs. Its experimental Gateway
  CRDs and release-default Deployment are not installed.
- Gateway API 1.6.3 `standard-install.yaml`: checked against GitHub release asset
  digest; supplies Gateway CRDs plus upstream safe-upgrade policies.
- Kind v0.33.0 `const_storage.go`: tagged Apache-2.0 source with original license
  header. Renderer extracts its literal storage manifest, substitutes the two
  locked image references, and excludes Namespace and the default StorageClass.
  Foundation owns the namespace, and a separate Retain class is explicit.

Vendored files retain their upstream licenses and notices. Upstream provenance is
not a claim that image signatures or a release SBOM were verified. OCI image
manifest bytes were anonymously fetched, hashed, compared with registry digest
headers, and checked for linux/arm64 support. The current candidate uses Kind 0.33.0's official Kubernetes 1.37.0 node and
Argo CD 3.5.7 / Redis 8.10.3. Historical locks remain in their original commits.
See [the dependency inventory](../../docs/dependency-upgrade-20261103.md) for
reviewed exceptions and compatibility limitations.

The generated files retain Helm labels/hooks where upstream needs them. No
`helm install`/`upgrade` or Helm release state is introduced: Argo executes the
Envoy certificate-generation Job as its supported Helm hook equivalent.

- Argo CD chart 10.11.6 and kube-prometheus-stack 92.17.1: unmodified GitHub
  release archives, checked against publisher Helm index digests on 2026-11-03.
  Their bundled dependencies are retained as published. Sealed Secrets chart
  2.22.0 is checked against its publisher Helm index digest.

## Data operators (ADR-0021 candidate)

- `cloudnative-pg-0.29.1.tgz`: official cloudnative-pg/charts release; app 1.30.1.
- `strimzi-kafka-operator-helm-3-chart-1.3.0.tgz`: official Strimzi release.
- `flink-kubernetes-operator-1.16.1-helm.tgz`: Apache release archive, SHA-512 verified.

Exact source URLs, SHA-256 and verification methods are in
`platform/development/capabilities/versions.lock.json`. Charts are not modified; upstream
licenses remain in the archives. Helm renders offline. Finite permission/image
projections are in `internal/platform/data_operators.go`; namespaces and values are
reviewable catalog inputs. No live Helm releases are created.
