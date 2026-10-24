# First installation — Atlas local development

This guide now describes product schema 2, role-scoped-v1 namespaces and YAML configuration.
The role-scoped layout and data platform passed the [r2 runtime acceptance](https://github.com/snkio027/atlas/blob/v0.1.0/docs/namespace-layout-r2-runtime-validation.md). Permanent component access subsequently passed its [same-instance validation](https://github.com/snkio027/atlas/blob/v0.1.0/docs/local-access-validation.md). The v0.1.0 release archive has a separate source/attestation verification boundary; it is not a new clean-install runtime test.
Use a matching reviewed package. Older products and instance evidence are not upgraded by these commands.

Historical context: the owner-selected same-host runtime acceptance passed for `v0.1.0-d1.4`.
That draft and its evidence retain their original bindings; v0.1.0 is built separately from its own tag.
The measured host was Apple Silicon macOS 26.7 (25G229), OrbStack
2.2.3, 32 GiB host RAM, with Docker reporting 10 CPUs and about 15.66 GiB VM
memory. These measurements are not minimum requirements. Only Apple Silicon
macOS and a running OrbStack are targeted. No Intel/Linux,
production, upgrade, existing-cluster adoption or recovery support is claimed.
Exact artifact identity, measured capacities, timings and evidence are in
[D1 acceptance](https://github.com/snkio027/atlas/blob/v0.1.0/docs/d1-validation.md).

## Host prerequisites

Install and start OrbStack yourself. Install Git and GitHub CLI (`gh`), log in to
GitHub with `gh auth login`, and ensure Docker CLI can reach the `orbstack`
context; the OrbStack `orb` CLI must also be on PATH. Prepare a **public** GitHub repository with push permission and a new dedicated
deployment branch. It may be the product repository; product and deployment
commits remain distinct.
The selected deployment branch must not exist. Atlas does not install OrbStack.
Do not set `DOCKER_*` or `KIND_*` environment overrides. No Atlas source checkout,
Go, Task, Python, Lua, Helm, kubectl, Kind or kubeseal installation is required.
The latter four are fetched by `prepare`, privately and with digest verification.

## Authenticate the package before execution

Download the archive and checksum from the selected published GitHub Release.
Before extracting or executing it, verify both content and build identity:

```sh
shasum -a 256 -c atlas-VERSION-darwin-arm64.tar.gz.sha256
gh attestation verify atlas-VERSION-darwin-arm64.tar.gz \
  --repo snkio027/atlas \
  --signer-workflow snkio027/atlas/.github/workflows/release.yml \
  --source-ref refs/tags/VERSION
mkdir atlas-preview
tar -xzf atlas-VERSION-darwin-arm64.tar.gz -C atlas-preview
cd atlas-preview
shasum -a 256 -c SHA256SUMS
```

Use the real version in place of VERSION. Matching an adjacent checksum alone
is insufficient. See [GitHub's attestation verification reference](https://cli.github.com/manual/gh_attestation_verify).
The runtime manifest is also bound to the executable at build time. A modified
or incomplete package is rejected before preparing or applying resources.

## Configure, prepare, review, install

Copy `installation.example.yaml` to `installation.yaml`. Set your new `atlas-…`
cluster name, repository and new deployment branch, distinct ports, canonical absolute
private state path and backup path. The fixed path inside Git is
`gitops/root/overlays/development`; topology and platform components are fixed.
Choose `backupIsolation: external` for physically separate media. An explicitly
chosen `same-host-development-exception` records reduced backup isolation; it
never silently inherits a maintainer's exception. Keep the backup separate from
installation state and create both directories with owner-only permissions.
The backup filesystem must support Unix permissions and atomic hard links
(such as an appropriately permissioned APFS volume).

```sh
./atlas-install prepare --config installation.yaml
./atlas-install plan --config installation.yaml
./atlas-install install --config installation.yaml --approve-plan PLAN_SHA256
./atlas-install verify --config installation.yaml
```

`prepare` is the online dependency phase. Missing/corrupt dependencies during
`install` stop execution; installation does not replace them by downloading.
Interrupted tool downloads retain a private partial cache and resume with full
digest verification. Review the actual plan and use its displayed SHA256. The one approval covers
base Git publication, four-node creation/Tier-0 handoff, independent Trust Root
backup, three newly generated encrypted credential objects, full-platform
publication, bounded Argo refresh, and documented functional probes. It does
not authorize deleting old clusters, adopting existing ones or rotating keys.
Keep terminal output: it reports the dedicated kubeconfig, private credentials,
CA, backup and supported access commands without printing passwords.

## Permanent local access

The installer archive creates the D1 base. Optional data capabilities and permanent
management/native-client access are post-install changes; they are not silently
included in Tier-0. See [component access](https://github.com/snkio027/atlas/blob/v0.1.0/docs/local-platform-access.md) for the
current r2 endpoints, login methods and deployment status. That path uses no
port-forward process. The commands below remain optional foreground D1 diagnostics.

## Foreground D1 diagnostics and repeat verification

```sh
./atlas-install access --config installation.yaml --service web
./atlas-install access --config installation.yaml --service grafana
./atlas-install access --config installation.yaml --service prometheus
./atlas-install access --config installation.yaml --service s3
```

Each command stays in the foreground, prints a loopback URL, and closes on
Ctrl-C. Use separate terminals for simultaneous connections. These read-only connections
share a lock; stop them before running an installation or functional verification. The Web operation
serves an HTTP loopback proxy whose upstream HTTPS connection checks this
installation's CA and `web.atlas.test` hostname. It does not make the browser
trust that CA. Direct TLS clients can use `--cacert <reported CA>` and
`--resolve web.atlas.test:HTTPS_PORT:127.0.0.1`. Atlas does not change hosts,
system trust or the default kubeconfig. Grafana username is `admin`; its password
and S3 credentials are in the reported owner-only credentials.json file.

Repeating the same approved install after success performs verification without
new credentials, ciphertext, Git commits or Bootstrap writes. `verify` checks
current authority, four-node topology, GitOps, storage and service access.
`verify --functional` additionally writes/removes per-install S3 test objects,
exercises presigned/multipart operations and sends/resolves a scoped test alert.
Alertmanager acceptance covers local delivery/API, not an external notification
channel. Prometheus and S3 access are exposed only by the foreground localhost
connection; no public service exposure is added.

## Interrupted operations

Rerun the same command/configuration. Confirmed publications and private
credentials are retained; a lost Git acknowledgement is checked against the saved
exact commit before retry. No new random credentials are generated over an
existing credential file. Different target identity, repository state, certificate,
missing/invalid backup, or a Bootstrap contradiction stops the installation.
Preserve the reported state for review; never delete files to bypass a failure.
This release does not provide automatic recovery or a general resume interface.

## Historical acceptance (before role-scoped-v1)

The actual attested `v0.1.0-d1.4` package passed the selected current-host run:
17m28s for installation, 5.62s for independent verification and 4.17s for a
completed repeat. All 26 Applications were Synced/Healthy at the exact deployment
commit. Five foreground access commands worked concurrently; 174 checked persistent
UIDs and private credential/cipher digests remained stable on repeat, with no
installer resource writes in the audit. See [the full acceptance record](https://github.com/snkio027/atlas/blob/v0.1.0/docs/d1-validation.md).

This historical candidate remains distinct from v0.1.0; do not replace its bytes or
reinterpret it as a new-tag test. This is not independent-machine, empty-cache or second-user evidence.
Live interruptions at Kind/Root/key-backup/full-publication checkpoints were not
injected in the successful run. Atlas code is distributed
under the bundled MIT LICENSE.
