> 当前四节点 dev02 已通过全新自动启动验收。本文包含 dev01 单节点首轮设计与历史操作。
> schema 3 四节点目标和命令见
> [四节点开发工作流](development-four-node.md)，不能用旧启动命令运行当前清单。

# Web/API 开发平台：实现与部署审查

状态：**独立开发集群已启动；经人工介入完成 GitOps 交接与本机 HTTPS 验证；不是原 Atlas authority cutover。**
实际过程、限制和命令见[本机启动记录](development-runtime-20260927.md)。
适用范围是新的、可丢弃的 OrbStack / 单节点 Kind / IPv4 开发集群。
采用原 Atlas Architecture v1.0.2、GitOps v1.0.3、Network v1.0 的权责模型；
参考源固定为原仓库 `aca4ff137a1d254cfeceaec24526e0699b585e92`。
部署范围提案见 [ADR-0004](adr/0004-local-web-development-platform.md)，保持 Proposed。

第一轮先完成清单与本地验证；用户随后授权提交审核，并在本机启动新的开发集群
`atlas-refactor-test-dev01`。第四轮配置、报告和证据保持不变；开发环境的结论单独记录。
ADR-0004 保持 Proposed，实验启动授权不等于生产替换或主分支合并批准。

## 实现内容与目的

| 能力 | 当前实现 | 用途与边界 |
| --- | --- | --- |
| CNI | Cilium 1.20.4，IPv4，Kubernetes IPAM，保留 kube-proxy | 承载 Pod 通信和标准 NetworkPolicy；关闭 Cilium Ingress/Gateway/L7 proxy/Hubble/TLS secret 同步，东西向流量不强制经过 Envoy |
| 入口 API | Gateway API 1.6.3 standard CRDs | 业务使用 HTTPRoute；不用 Ingress，也不手工维护代理 Endpoints |
| 入口控制器 | Envoy Gateway 1.9.3，Envoy distroless 1.39.6 | 平台声明 GatewayClass/EnvoyProxy/Gateway，控制器生成代理 Deployment/Service；单副本适配本地开发 |
| 本地访问 | `web.atlas.test`；HTTP `127.0.0.1:8080`；HTTPS `127.0.0.1:8443` | Kind 映射 NodePort 30080/30443；HTTP 跳转到 HTTPS 8443；只映射 loopback，不依赖云 LoadBalancer、MetalLB 或修改系统 DNS |
| IPv4 DNS | EnvoyProxy listener IPv4；xDS bootstrap `V4_ONLY`；BackendTrafficPolicy `lookupFamily: IPv4` | 兼顾控制面连接和 FQDN 后端解析，避免 OrbStack/Kind 中 IPv6 解析选择造成延迟 |
| TLS | cert-manager 1.21.3；namespaced self-signed Issuer → CA Certificate → CA Issuer → leaf Certificate | 自动生成开发 CA 和 `web.atlas.test` 证书；Git 只保存 Secret 引用；私钥在运行时生成，不自动安装主机信任 |
| Namespace 治理 | 5 个 namespace，业务 namespace 启用 restricted Pod Security、ResourceQuota、LimitRange | 业务有明确资源预算和安全默认值；控制器使用各自平台 namespace |
| 网络隔离 | `workload-web` 双向默认拒绝；允许 CoreDNS；只允许指定 Gateway 代理标签访问 Web 8080 | 通过 namespaceSelector 与 podSelector 授权；新外部 API、数据库连接须显式增加策略，不能依赖 Pod CIDR 放行 |
| 本地存储 | 接管 Kind 0.33.0 的 local-path provisioner 定义，锁定 controller/helper 镜像；新增 `atlas-local-retain` | PVC 明确指定 Retain + WaitForFirstConsumer；保留 Kind 自带 `standard`，不依赖它的默认选择；本地卷不提供跨节点或集群删除后的数据恢复 |
| Web 示例 | 非 root BusyBox 1.38.0 HTTP 服务、PVC、Service、HTTP/HTTPS HTTPRoute | 首次启动写入实例标识，重建 Pod 后应读取同一文件；验证完整网络/TLS/存储路径；不是业务框架或数据库平台 |
| GitOps 编排 | 3 个 canonical AppProject、3 个 Root macro child、平铺能力 Application | 业务权限限制在 `workload-web`；不能创建 Argo、RBAC、namespace、CRD 或其他集群对象 |
| 健康 Gate | 原 Application 健康脚本；新增 Issuer/Certificate/GatewayClass/Gateway/HTTPRoute/BackendTrafficPolicy Lua | 要求当前 generation 的成功条件；Gateway 检查每个 listener，路由检查每个 parent 与 controller；波次不是单纯目录顺序 |
| 本地工具 | 独立 Go `atlas-platform render/check`，只使用标准库 | Helm 仅渲染固定本地 chart；Kustomize 只构建本地目录；工具没有 apply、集群 client、下载或恢复命令 |

Gateway API 版本按 [Envoy 1.9 兼容矩阵](https://gateway.envoyproxy.io/news/releases/matrix/) 选择。
当前源码候选使用 Kind 官方 Kubernetes 1.37.0 镜像与 kubectl 1.37.2。
[Cilium 1.20 兼容矩阵](https://docs.cilium.io/en/stable/network/kubernetes/compatibility/)
尚未列入 1.37；新组合的运行时兼容性未验证。版本来源、例外与验证范围见
[2026-11-03 依赖升级](dependency-upgrade-20261103.md)。历史实例仍绑定原锁。
Network Standard 的 IPv4-only DNS 要求在当前 Envoy API 中映射为上述两个字段；
没有把不存在的 `dnsLookupFamily: V4Only` 字段直接写进 EnvoyProxy。

## 控制图与所有者

```text
External Root（仅 Bootstrap 创建一次，无 parent、无 cascading finalizer）
├── -110 project-bootstrap   [atlas-bootstrap]
│         └── platform-project + workload-project
├── -100 platform-control    [platform-project]
│         ├── -100 foundation
│         ├──  -99 cilium（接管启动 Seed）
│         ├──  -90 argocd-self（接管启动 Seed）、cert-manager
│         ├──  -40 gateway-api、envoy-crds
│         ├──  -30 envoy-gateway、local-storage
│         ├──  -20 local-pki
│         └──  -10 edge
└──    0 workload-control    [platform-project]
          └── web-smoke     [workload-project]
```

Root 只包含三项 trust transition；组件不直接挂在 Root。
`workload-control` 是创建 Application 的平台控制面，必须属于 `platform-project`。
业务叶子才属于 `workload-project`，不因编排需要而获得 argocd namespace 权限。
平台定义业务 namespace、配额与 NetworkPolicy；业务项目不能自己扩大网络白名单。

所有 Application 使用完整 SSA、自动同步、自愈与共享资源冲突检查；首次接管不使用
`ApplyOutOfSyncOnly`，避免相同 Seed 被跳过。Child Application 有 `Prune=confirm,Delete=false`，
没有 cascading finalizer。Namespace、业务 PVC、Retain StorageClass 有独立删除保护。
删除保护不是备份；数据迁移、CA 轮换、灾难恢复和删除集群仍是另行设计的操作。

Cilium 仍用上游 agent/operator 注册自身 CRD；其定义来源是锁定镜像。
本项目不在业务层暴露 Cilium CRD，也不增加手工 Cilium 策略。
Kind 的 local-path controller 在创建 substrate 时已经存在，之后由 GitOps 接管并锁定
动态 helper 镜像；不会安装第二个争抢相同 provisioner 的 controller。

## 文件与复现

- `platform/development/`：独立配置、锁、Helm values、健康脚本和初始 Seed。
- `gitops/root/overlays/development/`：三项 Root macro Application。
- `gitops/platform/applications/overlays/development/`：平铺能力 DAG。
- `gitops/platform/{foundation,management,networking,storage}/`：平台叶子定义。
- `gitops/workloads/`：业务控制目录和 Web 示例。
- `vendor/platform/`：固定上游 chart、Gateway CRDs、Kind storage source。
- `internal/platform/`、`cmd/atlas-platform/`：本地渲染和 conformance 校验。

手写资源使用 JSON（合法 YAML）；`resources.json` 为 Kubernetes List，Kustomize 会展开。
`rendered.yaml` 和两个 `*-seed.yaml` 由 Go 工具生成，不直接编辑。
Cilium 与 Argo Seed 分别与对应 GitOps 叶子字节一致，启动集成会验证这一契约。
`bootstrap/root.json` 和 `bootstrap/project.json` 是独立模板，不在任何 Kustomization 中。
禁止对整个 `platform/development/bootstrap/` 执行递归 apply。

需要预装 Go 1.27.3、Helm 4.3.1、kubectl 1.37.2、yq 4.54.1、Lua 5.5.1、kubeseal 0.41.2（当前 checksum lock 为 darwin/arm64）。
依赖不会由质量命令自动下载。工具已在 `.state/tools/` 或 PATH 且版本匹配时：

```sh
cd /Users/nekoreb/Workspace/03_Projects/atlas-refactor
task platform:render
task quality
```

工具在其他目录时，使用 Task 参数；同一选择同时传给 Bootstrap 和平台真实渲染测试：

```sh
task quality \
  PLATFORM_HELM=/absolute/path/to/helm \
  PLATFORM_KUBECTL=/absolute/path/to/kubectl \
  PLATFORM_YQ=/absolute/path/to/yq \
  PLATFORM_LUA=/absolute/path/to/lua
```

`task quality` 包含格式、vet、race 测试、真实 Lua 健康脚本和 `platform:check`。
后者验证上游制品摘要、镜像锁、渲染与提交候选文件一致、所有本地 Kustomize 构建、
重复资源 identity、每个 Application 的项目权限和三层结构，以及 CRD 的结构、
必填项、枚举与未知字段。它不是 Kubernetes server dry-run，不执行 CEL、Admission 或 controller。
`go test` 独立运行时需要 `ATLAS_TEST_LUA` 才执行 Lua 集成测试，否则明确 skip。

本次结果与候选文件 SHA-256 清单见 [本地验证记录](evidence/development-platform-local-20260927.json)。

## 独立开发集群启动与验收

`profiles/development.json` 使用 schema 2，仍由现有 `cmd/atlas` 执行；schema 1 历史实验不变。
运行时读取已提交的渲染制品，验证 vendor 摘要、平台 bundle 指纹、双 Seed 字节一致性、
锁定镜像和仅 loopback 的 Kind 配置。Kind 禁用默认 CNI，以 `--wait 0s` 创建；预载镜像、
安装 Cilium、等待节点 Ready 后才启动 Argo。两个 Seed 共用同一个 durable latch。
交接观察器要求全部 15 个 Application 对齐同一实际 SHA，并逐项检查两个 Seed 的 tracking/SSA，
再创建 Receipt；latch 后只能观察或补 Receipt，不能重写 Cilium、Argo 或 Root。

1. **提交与审核。** 所有 Application 跟随 `codex/development-platform` 审核分支。
   首次启动必须从干净 checkout 执行，远端分支 SHA 必须等于本地 HEAD；保存精确 SHA 和全部输入摘要。
   首次 commit 和输入摘要快照保留，私有 `.state` 沿用；修订 checkout 的执行 commit 单独记录。
   `bootstrap/baseline.json` 固定初次完整 bundle 摘要；Root、项目、控制图、Kind、锁和两个 Seed
   逐字节绑定初次快照，普通 GitOps 叶子可演进而不改写 Identity/latch/Signal。
   HTTPRoute 显式声明默认 parent group/kind、PathPrefix 和 backend group/kind/weight，避免 API 默认值造成持续 diff。
2. **准备离线输入。** 在启动前独立下载并校验锁定镜像；运行时不自动下载缺失依赖。
   预检查 OrbStack、8080/8443 空闲和 linux/arm64 镜像，保留全部旧集群。
3. **精确目标。** 本次用户授权目标为 `atlas-refactor-test-dev01`，单节点 Kind / OrbStack。
   8080/8443 和 Kubernetes API 仅监听本机 loopback；不修改主机 DNS 或信任库。
4. **运行命令。** 在独立、固定 commit 的执行 checkout 中执行：

   ```sh
   atlas doctor --config profiles/development.json --tool-dir /absolute/path/to/locked-tools
   atlas apply --config profiles/development.json --tool-dir /absolute/path/to/locked-tools \
     --approve-cluster atlas-refactor-test-dev01 --approve-tier0
   atlas status --config profiles/development.json --tool-dir /absolute/path/to/locked-tools --check
   ```

5. **在新集群启动并观察 GitOps。** Root → projects → platform → workload；TLS 各阶段依次就绪。
   记录每个 Application 的实际 SHA、Sync/Health；确认 Seed 被 GitOps 接管、Root UID 未变、
   Bootstrap 不再持续写入；controller 创建的代理 Service/EndpointSlice/helper Pod 归属正确。
6. **验收 Web/API 路径。** 读取开发 CA 的公开 `tls.crt` 到私有工作目录，通过
   `curl --cacert <ca.crt> --resolve web.atlas.test:8443:127.0.0.1 https://web.atlas.test:8443/`
   验证 TLS 和内容；HTTP 8080 应返回跳转至 HTTPS 8443。确认 Envoy 实际 xDS DNS 为 V4_ONLY。
   重建示例 Pod 后内容不变、PVC/PV identity 不变；未获授权 namespace 的 HTTPRoute attachment 被拒绝；
   未获授权 Pod 直连 Web 被网络策略拒绝；业务 DNS 可用。测试探针与 Pod 重建须包含在执行批准内。
7. **保留本轮证据。** 独立目录保存 commit、锁摘要、工具、渲染哈希、集群 UID、Application
   revision/health、ownership、TLS 公钥摘要、HTTP 响应、PVC/PV 和退出码；不能追加到第四轮文件。
   不保存 Secret 私钥、kubeconfig、controller token 或完整 Secret 输出到 Git。

失败时停止推进下一 Gate、保留现场。正常 Bootstrap 不覆盖 drift，不因为不健康而恢复 Seed 权限。
本轮没有授权或实现自动回滚、删除集群或 break-glass；它们不被“先可开发”的优先级隐式启用。
正式 Shell→Go cutover 的 NO_GO 状态仍有效；独立开发环境不是原 Atlas 的替换。

## 本地验证的已知限制

官方 egctl 1.9.3 可离线验证 Gateway/EnvoyProxy API、TLS listener 引用和 Envoy bootstrap 转换。
但其 [YAML loader](https://github.com/envoyproxy/gateway/blob/v1.9.3/internal/gatewayapi/resource/load.go)
构造 Namespace 时只保留 name，丢弃 labels（277–288 行），导致 Selector 路由离线被报告
`NotAllowedByListeners`。这不能作为已证明可用的 route attachment，也不能因此改真实清单为 `All`。
本地 conformance 会检查 selector 与指定 namespace 标签一致；真实跨 namespace 绑定留给上述验收。

本次真实验证已覆盖 Cilium 承载 Web 流量、TLS 初次签发、PVC provision、Argo 接管和本机端口。
证书轮换、PVC rebind、负向网络探针与完整故障恢复仍未证明。后续再补 HA、观测栈、备份、Recovery/Drill、Admission 证据保护、
发布签名/SBOM、跨执行器锁和生产供应链。公网域名/ACME、数据库、消息队列、对象存储也不在第一批范围。
