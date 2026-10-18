# Data acceptance contracts

This is finite experiment tooling, not a Bootstrap engine, publication controller,
namespace migration tool or recovery API. It creates only a reviewed pair of
short-lived, credential-free S3 connectivity Pods. The outer reviewed runner owns
cluster/plan/source binding and the execution decision; existing STOPs are not
cleared and previous stages are not replayed.

Run offline regression tests with `task data:contracts`. They also run in
`task quality`, including CI. Tests execute the actual `isolation.run` entry point
against a fault-injected transport and read the current rendered GitOps manifests.
They do not access a cluster and are not runtime proof.

## Why these contracts exist

A Pod's application name does not imply capability access. The current web-smoke
Deployment intentionally has no S3 client label. An earlier private acceptance
script assumed it was an authorized positive control and timed out correctly.
Adding an authorization label to that workload would change its intended access
solely to satisfy an incorrect test.

`isolation.pods` builds two identical Pods whose only authorization difference is
`atlas.local/s3-client=true`. `preflight` checks their exact identities, image,
credential-free runtime and effective ingress **and** egress against the current
Namespace/NetworkPolicy inventory. An unknown selector/port construct is rejected;
this is a finite adapter for Atlas' policies, not a Kubernetes policy simulator.
Both denial directions are required by this recipe. Additional allow policies are
included, so an unbound Pod accidentally gaining access cannot pass the plan.

The reviewed runner calls `Kubectl.snapshot`, `preflight`, and freezes the resulting
`Plan` and source hashes before requesting approval. Snapshot also verifies the
Service, its port, Ready backend and EndpointSlice identity. No mutation is needed
to discover an invalid positive control.

After approval, use one fresh `Journal` and call `isolation.run(plan, api, journal)`.
The finite sequence is:

1. Revalidate the exact network contract and endpoint.
2. Record and fsync a create-only intent before each of the two Pod creates.
3. Observe readiness; execute authorized HTTP → unbound timeout → authorized HTTP
   against the **same Service IP**, preserving exit code/stdout/stderr separately.
4. Require an explicit authorization denial for the workload default account
   reading the provider Secret; do not read that Secret.
5. Delete only the recorded Pod UIDs with current resourceVersion preconditions;
   wait for absence and write final PASS last.

The paired positive controls prevent treating a generally unavailable endpoint as
an isolation success. HTTP 403 proves network reachability, not network denial.
DNS errors, command-not-found, connection refused, unknown exit and timeouts of the
outer exec are not accepted denial evidence. This remains local-development
network evidence, not a multi-tenant security or fault-isolation guarantee.

Unknown writes, failed intent durability, policy/endpoint changes or cleanup
failures stop the attempt. There is no automatic retry of effects or rollback.
Existing journal paths fail, even when their contents would be identical. Exact
cluster UID, context, tool/kubeconfig digests and the original deadline are checked
by the target adapter. It never reads Secret objects or uses ambient kubeconfig.

## Versioned CLI boundary

`kafka_contract` fixes the locked Kafka image and modern consumer flags. Its
preflight validates command capabilities before create/produce operations. The
transaction CLI prints the exception **message** to stderr, not its class name.
The adapter consumes the non-TTY `kubectl exec` capture: empty stdout, Kafka
stderr followed by kubectl’s exact remote-exit diagnostic, and exit code 1. It
checks the exact transaction identity and both process layers without merging
streams, stripping warnings, or treating an arbitrary nonzero exit as denial.
Unknown or changed output fails closed. Do not add substring allowances to an
execution script. Upgrade this adapter and its capture fixture alongside the
image and kubectl.

A clean-install run exposed an incorrect stdout-only fixture. The broker had
returned the intended authorization denial; the verifier rejected its stderr
representation. The sanitized captured-result regression preserves the actual
stream/exit envelope. Original STOP evidence remains unchanged; correcting this
read-only classifier does not authorize replaying any probe or continuing a run.

Sources: [Kafka 4.3.1 TransactionsCommand](https://github.com/apache/kafka/blob/4.3.1/tools/src/main/java/org/apache/kafka/tools/TransactionsCommand.java),
[CoordinatorStrategy](https://github.com/apache/kafka/blob/4.3.1/clients/src/main/java/org/apache/kafka/clients/admin/internals/CoordinatorStrategy.java),
[kubectl error handling](https://github.com/kubernetes/kubectl/blob/v0.37.1/pkg/cmd/util/helpers.go).
Only the sanitized denial envelope is committed as a fixture. Credentials,
kubeconfig and installation-specific identifiers remain private.

## Operator installation contract

`WATCH_NAMESPACE` is not the complete API footprint. CNPG 1.30.1 has installation
PKI discovery, leader election, configuration, events and cached Service/EndpointSlice
reads in addition to instance reconciliation. `ValidateOperatorPermissions` checks
the full reviewed installation permission set, including inactive paths; the
omission matrix removes each permission independently. Rendered roles are unchanged.

Sources: [manager setup](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/internal/cmd/manager/controller/controller.go),
[namespace cache](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/multicache/multinamespaced_cache.go).
Static permission checks do not establish controller startup or runtime readiness.
Neither successful current-process evidence nor these tests proves every optional
CNPG plugin configuration is supported.
