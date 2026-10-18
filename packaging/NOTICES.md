# Preview package notices

Atlas Refactor is licensed under the MIT License included as `LICENSE`.
Third-party components retain their original licenses and notices; the MIT grant
does not relicense them. MIT license text: https://opensource.org/license/mit

The binary statically links the Go standard library (Go BSD license, included).
The runtime manifest includes Helm-rendered Kubernetes resources and the locked
Argo CD chart. The corresponding upstream Apache-2.0 license is included.
Upstream attribution and source locations are recorded below and in the source
repository's pinned artifact locks:

- Argo CD / argo-helm: https://github.com/argoproj/argo-helm
- Cilium: https://github.com/cilium/cilium
- cert-manager: https://github.com/cert-manager/cert-manager
- Envoy Gateway: https://github.com/envoyproxy/gateway
- Gateway API: https://github.com/kubernetes-sigs/gateway-api
- Local Path Provisioner: https://github.com/rancher/local-path-provisioner
- Sealed Secrets: https://github.com/bitnami/sealed-secrets
- kube-prometheus-stack: https://github.com/prometheus-community/helm-charts

Runtime tools and OCI images are downloaded separately from their locked upstream
sources; they are not relicensed by Atlas. Image and tool references/digests are
included in runtime.json. Review upstream notices for those separate artifacts.

The strict authored YAML parser vendors `go.yaml.in/yaml/v3 v3.0.5`.
Its MIT and Apache-2.0 license notices are in `vendor/go.yaml.in/yaml/v3/`.
`go.sum` records module provenance; `packaging/go-modules.lock.json` records the
vendored file digests. Quality and release builds use the offline vendor tree.
