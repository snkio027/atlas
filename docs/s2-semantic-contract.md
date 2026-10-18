# S2-0 — Project / Workload / CapabilityBinding 语义契约

状态：**Accepted / 单所有者本地开发 S2 契约**。提出：2026-10-01；接受：2026-10-09。

目标：平台所有者用三个严格类型对象声明一个 Web/API 项目，经确定性编译和 Git 审查，
让该应用在自己的 namespace 内通过 HTTPS 提供服务、消费现有 S3、被现有 Prometheus 监测。
语义边界由已接受的 ADR-0015/0016/0017 固定；r7 Runtime PASS 与 `fc809be` 增量回归分别绑定，
PR #9 已合入 main（`6dcc6d3`）。运行授权、实测证据与决策接受仍各自独立。

## 1. 基线与实施入口

PR #8 已经审核并合入 main，merge commit 为 `531d2340867f05097ff9055003da154633bf070c`。
开发分支 `codex/s2-typed-workloads` 从该 main 独立创建，PR #9 已完成 S2-A～D 并合入。
D1 验证产物 `v0.1.0-d1.4` 与 S1 历史证据保持冻结。
原 Atlas Operating Model §§2.2、2.3、5.1、5.3，GitOps §§2、10.3、13、20，Network §§4–6
约束此实现。ADR-0007、0011、0014 的已有权限、authority 和安装身份边界继续适用；
本次配置与权限投影由 [ADR-0015](adr/0015-typed-project-workload-binding.md) 接受。

## 2. 第一条 slice 与排除项

支持一个新 Project `demo`，namespace 派生为 `demo`；其中可声明多个 WebService，首条为
`web-api`，消费现有 `object-storage/uploads`，并启用 metrics。第二个无 Binding 的测试
Workload 用于隔离负例。原 D1 `workload-web/web-smoke` 保持自己的 identity 和 owner。

不引入新的平台组件；不扩展为多租户安全产品。首版只允许一个新增 Project，模型仍显式使用
project-qualified references，避免未来把同名 Workload 当作同一对象。

排除：CRD/Atlas Operator、通用 Workload DSL、rawPodSpec/extraObjects/arbitraryYaml、
StatefulService、PVC 申请、Job/CronJob/Worker、GPU、多集群、自动扩缩容、service mesh、
数据库抽象、通用密钥管理、自动修复、删除/解绑/退役/垃圾回收。
首条 WebService 使用现有 S3 作为业务存储，不预留一个无法履行的通用 storage 字段。

## 3. 三个 authored 类型

共同规则：JSON 对象；`schema: 1`；`kind` 精确区分类型。所有字段必须列在本契约中，
拒绝未知字段、重复键、大小写变体、null、尾随值、错误数字类型、溢出和未知 schema/kind。
名称是 1–20 字节 DNS label（小写字母/数字/内部连字符）；不进行隐式小写、截断或自动改名。
输入文件名必须与规定的 identity 路径一致；拒绝 symlink、路径逃逸和同 identity 多文件。

### Project

| 字段 | 语义与约束 |
| --- | --- |
| schema / kind | `1` / `Project` |
| name | 部署内唯一；namespace 恰为 name，首版无 namespace 字段 |
| owner | 必填的 1–128 字节团队标识；不授予 Kubernetes/Git/RBAC 权限 |
| quota | pods、requestsCpuMillicores、limitsCpuMillicores、requestsMemoryMiB、limitsMemoryMiB；均正整数；requests ≤ limits |
| capabilityAccess | 明确的授权集合；元素只有 capability、bucket、access；无通配符；首版仅 object-storage / uploads / read-write |

`Project` 是受平台所有者审查的 Tier-1 治理输入，不是 Workload 自行扩权的请求。
授权还必须与锁定产品提供的 capability contract 及部署策略求交；列入此数组不能启用未安装能力。
禁止 `default`、`argocd`、`kube-*`、`atlas-*`、`workload-web`，以及
[平台保留名](platform-namespaces.md)（含 gateway、monitoring、sealed-secrets、object-storage）。
编译时还拒绝 base inventory 已声明的 Namespace 及 AppProject 精确 destination，
即使该 destination 的 Namespace 尚未创建。
未知既有 namespace 不自动接管：离线与 deployment inventory 比较，执行前再只读检查 live UID/owner。

网络基线固定为 default-deny ingress/egress，加按 namespace + Pod selector 的 DNS 许可；
用户不填写任意 CIDR、NetworkPolicy、RBAC、labels 或 annotations。资源预算不含隐式零值和无限值。

### Workload

| 字段 | 语义与约束 |
| --- | --- |
| schema / kind / type | `1` / `Workload` / `WebService` |
| project / name | 指向已定义 Project；identity 为 (project, name) |
| image | 完整镜像引用，必须带 `@sha256:`；runtime gate 另要求制品存在、受支持架构、摘要核验，字符串合法不等于供应链可信 |
| port | 整数 1024–65535；普通 HTTP 监听端口；固定命名为 http |
| replicas | 正整数；受 Project quota 和滚动更新峰值约束 |
| resources | requests/limits 各含 cpuMillicores、memoryMiB；正整数；逐项 requests ≤ limits |
| exposure | hostname、tls；hostname 是唯一的具体 `<label>.atlas.test`，不允许 wildcard/IP/URL；tls 必须 true |
| observability | metrics 为显式 bool；true 时同端口 `/metrics`，固定 scrape 契约 |

运行契约固定：镜像以非 root、只读 root filesystem、无提权/hostNetwork/hostPath/hostPort，
drop ALL capabilities、RuntimeDefault seccomp 运行；compute worker 调度；显式 request/limit。
由平台生成专用 ServiceAccount，Pod 和 SA 均禁用默认 token mount，且不给该 SA RoleBinding。
固定 HTTP `/healthz` 与 `/readyz` 探测，不开放命令、任意 env、任意挂载或 probe DSL。
固定 emptyDir `/tmp` 有容量上限且计入 memory 预算；应用需兼容此 runtime contract。
滚动更新 `maxSurge: 1, maxUnavailable: 0`；静态预算按所有 Workload 各多 1 个 Pod 的保守峰值计算，
并核对 requests/limits 总和，不仅比较稳态 replicas。资源预算只声明限制，不证明宿主机有容量。

### CapabilityBinding

| 字段 | 语义与约束 |
| --- | --- |
| schema / kind | `1` / `CapabilityBinding` |
| project / name | identity 为 (project, name) |
| workload | 同一 Project 内已存在的 Workload 名称；不接受任意 namespace |
| capability | 首版精确为 object-storage；必须已启用且有版本匹配的 contract |
| bucket / access | 精确 uploads / read-write，并属于 Project capabilityAccess |

一个 Workload 首版最多一个 object-storage Binding。Binding 是消费授权，不创建桶、
不授予桶管理/IAM 管理权限，不轮换或撤销既有凭据。`read-write` 明确定义为该桶的
ListObjects、Put/Get/Head/DeleteObject、multipart 和 Tagging；允许删除对象不等于删除桶。
只接受锁定实现能实际满足的权限；不能仅凭 provider action 字符串声称 Create/DeleteBucket 已被拒绝，
必须纳入负向实测。若 provider 无法表达该边界，则停止该 Binding 的启用并审查契约，不静默放宽权限。

Workload 只获 `ATLAS_S3_ENDPOINT`、`ATLAS_S3_BUCKET`、`ATLAS_S3_REGION`、
`ATLAS_S3_FORCE_PATH_STYLE` 以及 SecretRef 注入的 AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY。
消费端必须看见协议端点，但不选择 SeaweedFS、namespace、Service 名、认证文件或实现标签。
本地 contract 的 endpoint 是集群内 HTTP；不能称为端到端 TLS/mTLS 或密码学工作负载身份。

## 4. Identity、编译过程与确定性

| 不变量 | 冻结的语义 |
| --- | --- |
| I1 严格类型 | 所有边界严格解码；未知、重复、null、无效枚举 fail closed |
| I2 引用完整 | Project/Workload/Capability 均解析到实际存在且版本匹配的定义 |
| I3 能力接口 | Workload 通过 Binding 得到协议参数，不选择 provider 实现资源 |
| I4 确定性 | 完整固定输入产生 byte-identical 输出；随机准备不属于 compile |
| I5 唯一归属 | 全量 runtime identity 只属于一个 Application；共享输出聚合后生成一次 |
| I6 权限推导 | 以 Project 授权、Binding 和产品策略求交；不接受自填 RBAC/NetworkPolicy |
| I7 零明文 | authored/generated Git 和公共证据不包含明文或 base64 Secret 数据 |
| I8 观察只读 | Observation 不暗含 publish、凭据生成、S3 写入或诊断 Pod 创建 |
| I9 有限编译 | Git 定义、Argo 调谐、Kubernetes/领域 controller 运行；无常驻 S2 controller |
| I10 生命周期分离 | Create/Update 拒绝隐式删除、改名、撤权；不包含 retire/recover |

Semantic identity：Project(name)、Workload(project,name)、Binding(project,name)。
Kubernetes identity：group/kind/namespace/name，cluster-scoped namespace 为空；version 不制造新 identity。
全部输出和保留的 base inventory 共用一张 identity → 唯一 Application owner 表。
共享对象由一个 platform adapter 聚合全部贡献后生成一次；不由每个 Binding 各生成一份共享对象。
源文件顺序、大小写、路径碰撞不能改变引用解析；所有冲突均拒绝。

对象命名：namespace=project；Deployment/Service/SA 名=workload；Project policy 有固定前缀。
定义 H 为 compact UTF-8 JSON identity 数组的 SHA-256 小写十六进制前 12 字符，数组分别为
`["Project",name]`、`["Workload",project,name]`、`["CapabilityBinding",project,name]`。
Application 名分别为 `s2-p-<project>-<H>`、`s2-w-<project>-<name>-<H>`、
`s2-b-<project>-<name>-<H>`；Binding Secret 名为 `s3-<H>`，两端策略为
`s3-<H>-egress` / `s3-<H>-ingress`，provider identity 为 `atlas-<H>`。
Pod 的保留标签为 atlas.io/project、atlas.io/workload，以及存在 Binding 时的 atlas.io/s3-binding=H。
不能让 Workload 自填这些标签。短 hash 不是授权凭据；编译器仍对完整 identity 检查碰撞和唯一归属。
先校验名称长度与冲突，不靠截断去重；改变命名算法属于 identity migration，普通 update 拒绝。

```text
Authored JSON + exact product/capability contracts + deployment base tree
             + registered sealed artifacts (publishable mode only)
  → Parse → Validate → Resolve references / authorizations
  → ResolvedProject / ResolvedWorkload / ResolvedBinding
  → Compile ownership-partitioned resource inventory
  → existing version-bound ResourceModel / static contracts
  → canonical files + public inventory / content digests
```

中间模型使用明确的 Go structs/enums；Kubernetes map 只允许出现在最终 adapter 层。
建议放入 `internal/workload`，沿用 `atlas-platform` CLI；首版不做公共 SDK/插件系统。
CLI 的确切子命令名在实现时统一选择，不把本稿示例当成已存在命令。

确定性输入必须包含：意图集合、编译器/产品内容摘要、capability contract、部署绑定、
基线 tree/inventory 及已登记密文。版本名称相同而内容不同不能视为同一产品。
输出按稳定文件路径、resource identity、集合顺序排列；JSON 统一缩进和结尾换行；
无时间戳、随机值、本地绝对路径、Git commit 时间、live API 默认值或网络查询。
同一完整输入重复编译得到 byte-identical 文件。规范化后等价的输入顺序也必须一致。

凭据尚未准备时只能输出只读 review inventory/credential requirements，不生成可发布 consumer tree。
publishable 编译要求匹配安装身份/Binding/namespace/name 的已登记密文；编译本身不能 kubeseal、
生成随机密钥、访问集群或推送 Git。密文随机性由单独准备步骤负责，同一准备记录重复使用原字节。
失败不改已有输出，先在 staging 完整检查再替换输出集合；不从空 inventory 猜测这是首次部署。

## 5. 文件与控制权映射

AUTHORED 位于用户 deployment repo 的 `platform/projects/<project>.json`、
`platform/workloads/<project>/<workload>.json`、`platform/bindings/<project>/<binding>.json`。
不放在产品源码树作为用户的实例定义；产品仅包含示例、编译器和版本绑定 contract。

| 生成对象 | Git 路径/owner | 权限边界 |
| --- | --- | --- |
| Namespace、Quota、LimitRange、DNS/default-deny、Workload SA | gitops/platform/projects/demo；新 s2-p-* leaf | platform-project；platform-control 的直接子项 |
| Deployment、Service、HTTPRoute | gitops/workloads/projects/demo/web-api；新 s2-w-* leaf | workload-project；workload-control 的直接子项 |
| Binding 两端 NetworkPolicy（demo egress、object-storage ingress） | gitops/platform/bindings/demo/*；新 s2-b-* leaf | platform-project；不并入已有 storage-foundation 的对象 |
| metrics ingress、ServiceMonitor | project platform leaf | 平台持有监测策略；不让 workload-project 获得监测 CR 写权限 |
| Gateway listener / Certificate | 现有 edge / TLS owner 内增量聚合 | 保留 Gateway/证书原 owner；不生成第二个相同 Gateway |
| AppProject destinations | 现有 platform/management/projects owner | 只增加明确 demo destination；三个 canonical AppProject 不变 |
| Sealed Secrets 对 demo 的监听与 namespaced RBAC | 现有 secrets-controller leaf | 仍由该 controller owner 管；不转移给 foundation |
| 新 consumer SealedSecret、provider auth 密文增量 | 现有 platform-credentials leaf | Secret 由 Sealed Secrets controller 物化；业务 leaf 不生成 Secret/SealedSecret |
| Prometheus namespace/target 选择增量 | 现有 monitoring owner | 限定 demo；禁止全 namespace 通配 |
| provider 认证配置生效所需的受限 rollout 声明 | 现有 SeaweedFS leaf | 保持 StatefulSet/PVC/Service identity，禁止直接重启命令 |

以上是对象归属而非目录依赖顺序。Binding 的 ingress 是“允许 demo 客户端进入存储 Pod”，
不是新增“存储主动连接 demo”的反向许可；TCP 返回流量不产生额外授权关系。
策略同时约束 namespace + 生成的 Workload/Binding 标签 + TCP 端口，不对整个项目开放 S3。
无 Binding 的同 namespace Workload 不能获得该标签、SecretRef 或 S3 egress。

标签不是密码学凭证。该结论以受审查输入/受控部署者为前提；共享 workload-project 权限并集不构成
恶意租户的服务端强隔离。平台管理员仍可绕过这些约束，S2 不声称多租户安全或 admission 完备。

Namespace、平台生成的基础资源和新 Application 保留删除保护；新 leaf 保持 SSA、
FailOnSharedResource=true、无 cascading finalizer。不改变 Root anchor、Root macro DAG、
canonical AppProject 名称、Cilium/Argo Seed、durable latch、Receipt 或 Signal。
AppProject destination 扩展走原有 project-bootstrap 管理链，不能因 S2 读取状态就自动获准发布。

## 6. 凭据与依赖发布：S2 必须完成的窄整合

现状限制：D1 密文严格绑定 workload-web/s3-client；SeaweedFS auth 是包含明文凭据的聚合 Secret；
secrets-controller additionalNamespaces 固定；TLS host、监测 namespace 选择和路由检查也固定。
仅生成 SecretRef 并不能使 demo 可运行。S2 必须一并解决这些已有边界的精确扩展，不能要求用户手改。

凭据准备是 owner 授权的有限本地操作，与纯编译分开。绑定同一个已安装实例的证书、
Trust Root 备份 receipt 和目标 namespace/name；不读取或导出 controller 私钥。
每个 Binding 使用独立新凭据，不复制 D1 的 web-uploads 客户端凭据。
通过受验证的安装私有凭据记录保留既有 provider identities，再添加新 identity、严格封装；
原私有输入缺失或与当前 provider 配置关系不明则 STOP，不从公开密文猜测或覆盖其他身份。
既有 D1 私有凭据、sealed record、backup 和 evidence 原样保留；S2 新材料进入自己的私有目录。
Secret plaintext/base64、私有路径和秘密日志不得进入 authored/generated Git 或报告。
离线校验不能证明密文内容正确，运行门禁必须校验物化与实际认证行为。

首版使用两次受审查 Git publication，复用 exact-parent / outcome reconciliation 原则：

1. **Infrastructure**：新增 demo namespace/SA/quota/baseline，扩充明确的 AppProject destinations、
   sealing controller namespace/RBAC、Gateway/TLS 与 monitoring 选择。尚不发布业务 consumer。
   确认 namespace 先存在，controller 实际监听 demo，证书与相关 controller Ready；跨 Application
   wave 不单独证明依赖已满足。对 D1 已存在对象只由原 owner 生成完整增量。
2. **Consumer**：发布已登记的新 consumer 密文、保留旧 identity 的 provider auth 密文、
   Binding 两端策略、Workload、Route/ServiceMonitor 及有必要的 provider Git rollout 声明。
   所有 Secret 与策略先于 consumer ready；secretKeyRef 非 optional、缺失立即阻断就绪。
   provider 若启动时读配置，按 Git 中的配置摘要触发受限 rollout；若依赖 hot reload，必须有
   对锁定版本的实验证据。不能用 sleep 或 Pod Ready 代替新凭据已生效，也不能用强制重启掩盖失败。

compiler 只输出；publisher 只写审查过的 Git 路径；Argo 执行资源调谐。
实时试验前另给出精确目标、Git 写入、凭据公开密文范围及探测清理清单。
失败停止且保持实际已经发生的外部状态；不自动 rollback、删除集群或恢复 Bootstrap authority。

## 7. D1 兼容与发布接口

D1 的 publisher 当前仅接收 gitops/，并构造完整 tree，不能直接用于保存 S2 authored 文件。
S2 publisher 的输入是“已核对的完整 parent tree + 明确 owned-path 增量”，保留其他文件，
只允许本节 authored 路径、生成路径和受审查平台 adapter 修改；隐藏文件/任意 .state 不可带入。
Git commit 只有在 tree 真正变化时创建；remote parent 不匹配或结果未知先核验，不 force 覆盖。
共享对象读取完整 prior model 后聚合，禁止用局部 Binding patch 取代完整 provider state。

D1.4 Verify/checkGit 要求安装 Record.FullCommit。S2 在同分支发布新 commit 后，原二进制
会 fail closed；这是明确的旧版本边界。**不得更新 D1 FullCommit 或重封旧密文让旧验收伪装通过。**
S2 observation 绑定原 installation identity/证书、已完成 handoff 和新的 deployment commit，
分别报告 platform authority、S2 rollout 与 runtime。S1 的任意历史 revision 等价逻辑不推广到普通 S2。
首版受影响的 S2/平台 owner 必须验证目标 commit；超时/旧状态保持未通过，不以 Healthy 放行。
不能拿旧 install/verify 作为 S2 发布器；其失败不能触发 re-install 或修复。

“D1 不回归”的验收含义：原产物和历史证据不变；未扩展的新安装及重复安装仍通过；
S2 扩展后 authority 不变，旧 D1 安全拒绝越界，新 S2 验收独立通过。
若产品要承诺“旧 D1.4 仍验收任意后续业务 commit”，那是另一项兼容承诺，不在本稿中悄悄实现。

## 8. Create/Update、Observation 与验收语义

Create/Update 需要已知 predecessor inventory；首次通过 D1 receipt + 完整树证明初始边界。
允许 image 更新、replicas/resources 在预算内更新、quota 在当前声明峰值以上调整，以及新增 Workload/Binding。
首版 identity、namespace、owner、port、hostname、Binding target/bucket/access 不可改变。
metrics false→true 可启用；true→false、capabilityAccess 缩减、任何对象遗漏/改名/删除均拒绝，
明确为未实现的 retirement/revocation。不可因“没有 delete 命令”就允许删 JSON 隐式 prune。
拒绝会缩减已声明访问的 update，不把权限撤销混入本次生命周期。

| 观察结论 | 必需证据 |
| --- | --- |
| Project VERIFIED | 精确 namespace UID/owner、Quota/LimitRange/default-deny/SA 当前 spec 与声明一致 |
| Workload VERIFIED | 目标 desired identity、Application owner、无 active operation、Deployment 当前 generation Ready、Service/Route 当前状态、TLS host/CA |
| Binding VERIFIED | 两端策略/selector/端口、密文/Secret 元数据与目标绑定、consumer SecretRef；还需有效的有限功能探测证明认证和隔离 |
| Runtime VERIFIED | 同一验证目标的 HTTPS→Web→S3 put/get 返回一致、metrics target、拒绝访问结果全部满足 |

沿用 internal/observation 已有状态词，不引入 controller、通用状态机或新 evidence schema 家族。
只读 observation 本身不创建 Pod、不生成凭据、不 PUT S3、不刷新 Application。
写 S3、建立诊断 Pod 和删除测试对象是另一个显式授权的 bounded probe；结果绑定 Git/source digest、
cluster UID、对象 UID/generation、credential generation 和完成时间。相关对象/状态变更使证据失效；
不能把旧 PASS 自动复用于新部署。新一次最终验收重新执行必要探测；观察可展示历史结果及其目标，
但不能把历史探测时间伪装成当前测量。UNKNOWN、缺证据或权限拒绝不等于 VERIFIED。

S3 put/get 用独立随机测试前缀；只清理 probe 创建的对象。诊断 Pod 不属于用户 Workload 的退役，
必须在具体 probe 计划中逐项授权并按创建时 identity 清理；失败不能删除业务 namespace/数据。
桶管理权限的破坏性负例先在锁定镜像、等价认证配置的隔离空桶 fixture 验证；不得对既有 uploads
执行 DeleteBucket 来赌它会被拒绝。非空桶错误也不能冒充权限拒绝。真实目标的探测限定获批测试对象。
跨 namespace Secret 检查用 workload SA 的实际受限身份，不打印真实 Secret 内容；不能仅用管理员
“能读到 Secret”或 auth can-i 的一个成功输出代替完整拒绝验证。

## 9. 正例与负例

以下 JSON 是**语义示例**，不是部署清单。image 使用保留域名和合成摘要，仅用于格式/引用/预算讨论；
artifact provenance/runtime gate 必须拒绝它。S2-D 需真实构建/锁定 Web API 镜像并验证 digest；
不能把静态 web-smoke 镜像当作已经实现 S3 API 的业务程序。

```json
{
  "schema": 1,
  "kind": "Project",
  "name": "demo",
  "owner": "demo-team",
  "quota": {
    "pods": 6,
    "requestsCpuMillicores": 1000,
    "limitsCpuMillicores": 2000,
    "requestsMemoryMiB": 1024,
    "limitsMemoryMiB": 2048
  },
  "capabilityAccess": [
    {"capability": "object-storage", "bucket": "uploads", "access": "read-write"}
  ]
}
```

```json
{
  "schema": 1,
  "kind": "Workload",
  "type": "WebService",
  "project": "demo",
  "name": "web-api",
  "image": "registry.example.invalid/atlas/s2-web-api:0.1.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "port": 8080,
  "replicas": 1,
  "resources": {
    "requests": {"cpuMillicores": 100, "memoryMiB": 128},
    "limits": {"cpuMillicores": 500, "memoryMiB": 256}
  },
  "exposure": {"hostname": "api-demo.atlas.test", "tls": true},
  "observability": {"metrics": true}
}
```

```json
{
  "schema": 1,
  "kind": "CapabilityBinding",
  "project": "demo",
  "name": "web-api-storage",
  "workload": "web-api",
  "capability": "object-storage",
  "bucket": "uploads",
  "access": "read-write"
}
```

解析后应得到 1 个 Project、1 个 WebService、1 个同项目 S3 Binding；namespace demo；
滚动峰值 2 Pods、200m/1000m requests/limits CPU、256Mi/512Mi requests/limits memory，均在 quota 内。
资源来源分别进入三个 owner 类别以及共享平台 adapter；没有 raw Kubernetes escape hatch。
这证明模型能表达消费意图；凭据、镜像和集成缺口仍须 S2 实现解决，不能记作 compiler 或 runtime PASS。

| 反例输入/状态 | 必须拒绝的阶段与原因 |
| --- | --- |
| Workload 增加 rawPodSpec / namespace / secretName | Parse；未知字段，拒绝输出 |
| 重复 image 键、schema=2、replicas=1.5、tls=null | Parse/Validate；严格类型，不默认修正 |
| project=missing 或 Binding 指向不存在的 workload | Resolve；悬空引用 |
| Binding 在 demo 引用另一 project 的 Workload | Resolve；跨 Project 引用 |
| Project 未授权 uploads，或 capability 未启用 | Resolve；未授权/未满足依赖，不能自动 enable |
| access=admin、bucket=*、endpoint 自填 | Validate；超出 capability contract |
| Project 名 argocd / 已被其他 owner 使用的 namespace | Inventory/preflight；平台越权或隐式接管 |
| 两个文件声明同一 Workload；两个输出 identity 相同 | Inventory；重复 semantic/runtime identity |
| 第二个 Workload 申请相同 hostname | Resolve；路由冲突 |
| requests > limits 或 rollout 峰值超过 quota | Validate；资源契约无法满足 |
| 两个 Binding 各自生成 seaweedfs-auth | Ownership；共享对象必须经唯一 owner 聚合 |
| 基线 inventory 缺失；生成目录被手改；remote parent 改变 | Compile/publish；未知基线或漂移，零发布 |
| 重新标注 workload-web 密文为 demo | Credential gate；namespace/name 不匹配，禁止复制封装 |
| 无登记密文就输出可发布 consumer；输出 Secret.data | Publishable/static check；凭据缺失/泄漏 |
| 删除 Binding JSON、改 bucket、关闭已启用 metrics | Lifecycle；隐式撤销/退役未实现 |
| Application Healthy 但 source/revision/owner 不符合目标 | Observation；不能证明当前定义 |
| 无 Binding Pod 能访问 S3；合法客户端能访问未授权桶 | Live probe；隔离/权限失败，不能降级通过 |

## 10. 一次 S2 交付的 Gate

| 内部阶段 | 完成标准 |
| --- | --- |
| A 语义模型 | 正例可解析/解析引用，所有字段和 update 语义明确；负例 fail closed；无 renderer 旁路 |
| B 确定性编译 | 冻结完整输入，重复/重排序 byte equality；ownership 唯一；平台/业务输出分开；无网络/凭据副作用 |
| C 静态契约 | locked ResourceModel、权限投影、无 plaintext、既有 owner 不变、diff 无删除；真实 Helm/Kustomize/结构校验；task quality |
| D 真实 slice | 从已完成 D1 的目标开始，经审核的 permissions → project → infrastructure → consumer 发布，HTTPS→Web→S3 put/get；无 Binding 拒绝；跨桶和管理操作拒绝；SA 跨 namespace Secret 拒绝；metrics 被发现；重复 compile/publish 零变更 |

还必须验证：无 Binding 的 Workload 不能靠自行提供保留 label/SecretRef 绕过编译；平台 namespace
不能挂载业务 route；D1 既有 S3 凭据在增量后仍有效；旧 Workload/平台对象 UID/owner 不变；
Bootstrap ADOPTED 不恢复 Seed 写权限；S1 冻结契约通过；原 D1 安装/重复路径保持原语义。

测试分层：表驱动 parse/resolve/权限/生命周期负例；有意义的 golden 与排列测试；
共享对象聚合/credential replay/publication fence 回归；真实 API/业务功能门禁。
不以模拟成功代替实时证据，不为无实现的文档示例制作看似通过的 compiler 测试。

S2-D 的最小业务程序只需固定 health/readiness/metrics 与一个 S3 round-trip API；请求触发
有界 put/get，校验回读内容，提供 metrics，超时和错误可观察。它不是平台 controller。
镜像准备/发布和测试 credential generation 在受审查执行计划中列明；本稿不授权 live mutation。

S2 完成只证明单所有者开发平台上的 typed Web/API capability consumption。
不宣称多租户强隔离、东西向 mTLS、分布式存储、HA、备份恢复或生产就绪；原网络标准中
SeaweedFS mini/领域 Operator 与密码学身份的差距继续显式记录，不借 S2 悄悄改写标准。

## 11. 审查判定与证据状态

三种类型、resolved semantic model、确定性编译器、固定前置条件发布和有限观察/探测入口已实现。
命令、字段映射、凭据策略与验证边界见 [S2 工作流](s2-workloads.md)。本地静态/单元检查与
隔离 SeaweedFS 合成权限测试通过；[r7](s2-r7-validation.md) 在 `d011813` 完成真实 Gate-D。
`fc809be` 的入口/终态修复通过增量回归、完整质量检查与维护者复审，未重跑或改绑 r7。
S1/D1 历史 evidence 和各次 STOP 保持原绑定。

本次接受三项产品语义：单新增 Project 的开发范围；每 Binding 独立凭据与现有 provider
聚合配置的受限增量；S2 后使用独立扩展验收，旧 D1.4 安全拒绝后续 commit。
代码合并/发布、密文公开发布、具体集群执行分别保持其原有门禁；无需为 A/B/C 各开一个 PR。

主要实现依据（本次核对的 `24bf7ad`）：

- `docs/typed-platform-contracts.md`：S2 范围与 Binding 两端语义。
- `internal/platform/network.go`：现有 workload-web、两条 Route 和固定 hostname 检查。
- `internal/platform/check.go` / `scope.go`：版本绑定 scope 与 AppProject 检查。
- `internal/installation/credentials.go`：三份密文及聚合 provider identity。
- `internal/installation/git.go`：gitops-only tree、exact-parent、FullCommit 检查。
- `platform/development/capabilities/core-projects.json`：三个 canonical Project 的现有权限。
- `platform/development/capabilities/values/sealed-secrets.json`：固定 namespace/RBAC 范围。
- `platform/development/capabilities/values/monitoring.json`：固定 ServiceMonitor namespace/label selector。
- `docs/d1-validation.md`：已验证范围与未证明项。
