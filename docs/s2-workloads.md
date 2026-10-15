# S2：把 Web/API 项目编译到已安装平台

最新验收：**r7 Runtime PASS**，实测实现 `d011813ac94559e623f29d54efbde231cab261fc`。
历史 S2 验收仍绑定原提交；当前输入已采用 YAML 与角色 namespace 布局（新布局 Runtime 待验证）。
当前状态：功能冻结；[PR #9](https://github.com/snkio027/atlas-refactor/pull/9) 已合入 main（`6dcc6d3`）。
`fc809be` 的入口/终态修复通过增量复审与 exact-head Quality，不把 r7 结果改绑到新提交。
ADR-0015/0016/0017 已接受，适用范围仅为单所有者、本地开发。
脱敏通过项与证据索引见 [r7 最终验收](s2-r7-validation.md)；历史 STOP 与各自实现绑定
保留在 [S2 clean validation](s2-clean-validation.md)。语义与权限边界见
[S2 契约](s2-semantic-contract.md) 和 [ADR-0015](adr/0015-typed-project-workload-binding.md)。
S2 开发分支从 PR #8 合入后的 `531d234` 开始；S1 与 D1 的历史结果保持冻结。

## 用户输入与编译

用户在自己的 deployment workspace 编写三个严格 JSON 类型：

- `platform/projects/demo.yaml`：owner、quota、允许消费的 capability。
- `platform/workloads/demo/web-api.yaml`：WebService 镜像、端口、资源、TLS 和 metrics。
- `platform/bindings/demo/web-api-storage.yaml`：为 web-api 授权 uploads 的对象读写。

可从 [示例](../examples/s2/platform/projects/demo.yaml) 开始。示例镜像是本地构建的
`atlas.local/s2-web` OCI 制品，不能从公网拉取；`image` 字符串的合法性不证明制品存在。
正式部署流程校验完整 archive、manifest、config、layer 摘要并把明确镜像导入该实例的四个节点。
导入器按 manifest 验证 tag 来源，然后登记 CRI 使用的 `repo@digest` 名称；最后用 authored
`repo:tag@digest` 查询 CRI 并要求精确 repoDigest。既有相同 canonical 名称不重复登记，冲突不 force 覆盖。
纯编译器支持契约中任意合法 pinned WebService 镜像；本次本地验收执行器只接收这一个可核验的 demo 制品。

`internal/workload` 不执行外部工具、不读取集群、不生成随机数、不推送 Git。
它完成严格 parse → validate → resolve → compile → scope/schema/ownership check，输出 canonical GitOps。
Unknown 字段、重复键、大小写别名、null、错误引用、缺失授权、名称冲突与 quota 超额均拒绝。
quota 计入每个 Deployment 的一个 surge Pod。无 Binding 的 Workload 不生成凭据引用或 S3 网络许可。

生成的 Namespace/quota/policy/SA/ServiceMonitor 由新 Tier-1 Project leaf 管理；
Deployment/Service/HTTPRoute 属于 Tier-2 Workload leaf；两端 S3 NetworkPolicy 属于 Tier-1 Binding leaf。
共享 Gateway、TLS、monitoring、Sealed Secrets RBAC 和 provider auth 仍由各自既有 Application 管理。
编译器不创建 Secret 明文，不改变 External Root、Seed、handoff latch、Receipt 或 Signal。

## 编译时拒绝 CRD 默认值遗漏

schema 合法只说明字段可接受，不证明 Git 与 API 返回的表示相同。`atlas-s2-r1` 的
HTTPRoute 故障暴露了这个缺口：省略可选字段能通过原来的 schema 检查，但 API 补入默认值后
仍可能被 Argo 判为 OutOfSync。路由已显式生成已观测的默认字段，编译入口也增加了通用的
**CRD 默认值遗漏检查**，避免其他新增 CRD 对象再次走到发布后才发现同类问题。

检查使用锁定 ResourceModel；每个新增或改动 CRD 对象的 served version schema 与 scope
必须和不可变 D1 public base 中的定义完全相符。新的源码 schema 不能替代已安装产品的规则。
对新对象、变更的子树和列表项，任何会被结构 schema 补入的遗漏字段都会带精确字段路径报错。
编译器要求 renderer 显式表达所需值，不自动补全默认值，不改 live state 或 Argo 比较规则。

未改动的 D1 子树保留原始表示。列表按完整内容匹配旧项并各使用一次，不能把旧下标的遗漏
继承给新增项；重复旧项也视为新增。缺失且没有自身默认值的可选父对象不会被虚构，显式 nullable
`null` 保留，`default: null` 不视为可物化的默认值；`false`、`0`、空字符串默认值仍须显式表达。
这些边界遵循 [Kubernetes CRD defaulting 语义](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/#defaulting)。

`plan` 原来只编译 infrastructure，consumer 的错误可能在 preparation 和首次发布后才暴露。
现在 permissions、project、infrastructure 编译都会先通过同一个 lowering pipeline 检查不依赖密文的 consumer 内容。
这份预检结果在内部丢弃，不输出 consumer 文件，不制造占位密文；正常 consumer 编译仍要求
本实例已登记的真实 artifacts。任何预检错误都阻止返回可发布输出。

该检查不模拟 Kubernetes built-in admission、webhook、CEL 或 Argo 调谐，也不会重写历史 D1
默认值遗漏。静态检查通过仍须执行真实 runtime Gate。回归覆盖已发生的 HTTPRoute 漏项、未来
schema 新增 consumer 默认值、源码与产品 schema 不一致，以及预检不泄漏 consumer 输出。

## 构建与准备

需要已完成的 D1 `darwin/arm64` 本地安装、原始 release package、其私有 installation state，
以及相同实例的有效 sealing certificate 和已有备份 receipt。不会导出或轮换 controller 私钥。

```sh
# 在产品源码根目录；依赖 Go 1.27.2，使用已校验 vendor，无需获取网络构建依赖。
mkdir -p .state/s2/artifacts
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -buildvcs=false \
  -ldflags=-buildid= -o .state/s2/artifacts/atlas-web ./cmd/atlas-web
go run ./cmd/atlas-web-image --binary .state/s2/artifacts/atlas-web \
  --out .state/s2/artifacts/atlas-web.oci.tar
go build -trimpath -o .state/s2/atlas-platform ./cmd/atlas-platform
```

OCI 打包是 create-only；相同 executable 得到相同 archive，不自动覆盖已登记制品。
把输出的完整镜像引用填到 Workload；把 archive SHA256 填到私有配置。
正式执行要求编译二进制绑定一个干净的源码 commit，修改代码后需重新生成并审查计划。

私有 `s2.yaml` 示例，所有路径替换为当前实例的绝对路径：

```yaml
schema: 1
installationConfig: /absolute/instance/installation.yaml
installationPackage: /absolute/instance/package
intentDirectory: /absolute/deployment-workspace
productSource: /absolute/atlas-refactor
stateDirectory: /absolute/s2-state
imageArchive: /absolute/atlas-web.oci.tar
imageSHA256: "<archive SHA256>"
```

S2 state 必须与 D1 state 独立。archive 与私有 state 文件使用 owner-only 权限。
首次验收意图额外包含同 namespace 的第二个 WebService `unbound`，使用同一镜像、不同 hostname，
不声明 Binding；它提供网络隔离负例。该负例不能通过一个根本不存在的 Pod 来证明。

## 计划、发布与观察

```sh
atlas-platform workload plan --config /absolute/s2.yaml
atlas-platform workload compile --config /absolute/s2.yaml --phase infrastructure
# 审查生成资源、namespace/RBAC、公开密文目标、镜像与 probe 范围以后：
atlas-platform workload deploy --config /absolute/s2.yaml --approve-plan <SHA256>
```

`plan` 只下载指定公开 deployment Git 分支到独立 bare repository，读取本地安装元数据并编译计划，
并通过已有 `gh` 登录只读核对精确仓库为 public、active、可写。fresh deploy 在镜像/凭据写入前复核该条件。
S2 publisher 显式使用 D1 已采用的 `gh auth git-credential`，不用临时全局 credential helper；
Git 进程屏蔽 global/system 配置与 hooks，凭据由现有 gh store 提供，不写入参数、日志或 Git。
`compile` 只使用已准备的本地 Git 对象与文件，不联网、不碰集群。
二者都不读取 Secret 或私钥，不启动集群。正式部署不创建/删除集群。

一次 `deploy` 有限完成以下步骤，异常即停止：

1. 核对 cluster UID、D1 authority、四节点和当前 Git；拒绝接管已占用的 namespace；记录相关旧资源 UID。
2. 校验并导入已登记 OCI；校验现有 public certificate/backup receipt，保留 D1 旧 identity，为每个新 Binding 生成独立凭据。
3. 发布 permissions：只扩充既有 AppProject 的精确 destination；只读 Gate 验证 live 内容、ownership 与应用收敛。
4. 发布 project：创建 Project leaf 的 namespace/quota/SA/policy，并由既有 local-pki owner 签发 workload Certificate；等待 Namespace Active、完整安全边界和当前 generation 的 Certificate Ready。
5. 发布 infrastructure：已有 owner 下扩充 controller RBAC、Gateway listeners、monitoring selector；listeners 只引用上一阶段已 Ready 的证书，再次等待完整 Gate。
6. 发布 consumer：新 client SealedSecret、聚合 provider SealedSecret、Binding、Workload/Route 和受控 provider rollout。再次验证。
7. 执行 HTTPS→Web→S3 put/get/delete、无 Binding 网络拒绝、跨桶/管理读取拒绝、SA 跨 namespace Secret 拒绝、metrics 发现、旧 D1 HTTPS/S3 凭据验证。
8. 保存绑定 implementation/binary/plan/Git/cluster/resource UID 的 final evidence。

首次部署的四阶段由 [ADR-0016](adr/0016-s2-publication-prerequisites.md) 定义。
Plan schema 2 将 permissions/project/infrastructure 的确定性输出摘要和固定顺序绑定到审核；
不能用旧二阶段计划授权新流程。每次 standalone `publish` 也必须校验完整前序 receipt 链，
并对紧邻前一阶段重新执行只读 Gate；receipt 本身不等于 Ready。Certificate Ready 还须
匹配当前 metadata.generation，stale Ready 不允许发布 listener；Degraded 仍立即拒绝。STOP 或无 receipt 的 intent
都禁止重推。完成后同一 Project/namespace 的受限 update 仍为 consumer 单阶段，只支持已有
Workload；新增 Workload 会引入未通过前置 Gate 的 TLS 依赖，因此在 plan 阶段拒绝。

观察协议见 [ADR-0017](adr/0017-s2-rollout-gates.md)。每阶段由 exact plan、回执链、
直接 Git parent 和当前编译输出建立只读契约，区分 Ready / Waiting / Rejected：

- 身份、tracking/SSA、完整 spec、错误条件和状态字段类型先核对；全快照 Rejected 优先。
- 本阶段首次声明的 Application 可等待创建和首次 comparison，兼容已知空/部分初始化 status；
  事实仍记录 UNKNOWN。已有对象丢失状态、换 UID、未知 revision 或明确错误立即拒绝。
- 在途更新只接受 target 或 exact predecessor 内容。已观察到 target 后回退 previous、第三种
  spec/资源内容均为漂移。改变 Application source/destination 后还必须取得对应的新 comparison。
- 一次 wait 保留 UID 与首次 comparison 事实；前置 Gate 将新 UID 带入后续阶段。CLI `observe --wait`
  和 deploy 共享同一观察循环；standalone publish 也保存前置 Gate。STOP/final/已完成阶段关闭过渡许可。
- 等待只重复读操作，固定 deadline 不重置，取消/超时保留最后原因；缺少终态证明不能进入下一阶段。
- Probe 前后也复用此只读循环，并共享同一个 receipt/UID 会话和 15 分钟总 deadline（受命令剩余时间限制）。
  功能探测只在前置证明完成后执行一次；后置观察 Pending 只重读，不重放功能操作。
  功能操作本身的 Pending、未知写结果或错误立即失败；终态仍检查完整证明和前后 UID 一致。

原始 S1 facts 和 controller condition.message 只写入 owner-only 本地报告，终端错误/公开总结
仅含分类与原因码。首次 deploy 保存各阶段通过报告，失败时冻结 stop-observation.json。

所有 Git 发布都保留完整 parent tree，仅替换编译器声明的 delta，并用 exact-parent lease 拒绝并发更新。
已有无关文件、可执行位及 Git 历史不重建。重复成功 `deploy` 只观察；重复发布相同 consumer 不产生新 commit。
首次随机密文保存在独立注册文件中，重复编译使用原字节；编译本身不重新加密。
已有对象 identity、hostname、port、Binding 关系与授权不允许通过 update 隐式删除/退役。

`prepare-credentials`、`baseline`、`publish`、`probe` 是同一精确计划的显式分步入口，便于审查诊断；
不构成 recovery/resume 接口。外部状态变更后失败保留 intent/receipt/terminal evidence，不自动回滚或清除 STOP。

独立 `probe` 与 deploy 的功能边界共用持久化防重放保护。已有 STOP、final 或
`authority/<planSHA>/probe-started.json` 均拒绝再次功能调用，即使现场后来恢复 Healthy。
在前置只读 Gate 成功后、第一次功能操作前，以 exclusive create 写入并同步 intent；
已有相同内容也不能取得执行资格。写入或同步失败不执行功能；不完整 intent 保留并阻止重放。
CLI 的安装锁排除正常并发，exclusive intent 额外保证同一 plan 不能取得两次功能执行资格。

功能与后置观察通过后，先更新本地 `provider.json`，最后才提交 `final.json`。
provider 读写失败不能留下本次 PASS；final 写入失败仍保留 intent，不自动重试功能。
Deploy 在任何成功重复路径前检查 final 与 terminal；两者并存明确报矛盾，不让 final 覆盖 STOP。
失败时尽力保留 stop-observation 与 terminal，读/写失败均与原错误一起返回并标明证据不完整。
这些保护不新增阶段、恢复能力或公共 schema，也不改变功能探测内容。

日常只读观察：

```sh
atlas-platform workload observe --config /absolute/s2.yaml \
  --phase consumer --revision <published commit> --wait 10m
```

观察核对 Git 输出、Bootstrap authority、App spec/Sync/Health/idle、live identity/字段/SSA/tracking、
相关旧 UID，并做 closing proof。普通未变 leaf 只允许该有限发布序列中 source closure 相同的 revision；
控制 Application 和所有 mutation fence 保持 exact-current。观察不 refresh、不 sync、不执行 Pod 命令。
其 `Runtime: UNPROVEN` 明确表示没有重做写入型功能探测；完整 Runtime VERIFIED 来自独立 probe 的最终证据。

## 权限与验证边界

新 Binding 使用 SeaweedFS 4.47 的 policy-only identity，授予 uploads 对象 CRUD、Tagging、multipart 和 List，
不使用包含 bucket 配置权限的 legacy `Write:uploads`。原 D1 identity 保持原样。
依据是锁定 [4.47 的 IAM 实现](https://github.com/seaweedfs/seaweedfs/blob/4.47/weed/s3api/auth_credentials.go)
及本仓库的隔离运行回归，而非仅比对权限字符串。

```sh
task quality
# 使用固定镜像、合成凭据、tmpfs 数据和 loopback 随机端口；结束清理本测试容器。
ATLAS_S2_PROVIDER_FIXTURE=1 go test ./internal/workloadrun \
  -run '^TestSeaweed447PolicyFixture$' -count=1 -v
```

`task quality` 包括 S2 四阶段的真实 Kustomize 构建；不会启动 Docker/Kind 或使用实例凭据。
隔离 provider fixture 已验证对象读写、Head/List/Tagging/multipart，以及跨桶、CreateBucket、DeleteBucket、
PutBucketCORS 的 403。危险的 bucket mutation 负例仅作用于这个空的合成 fixture，绝不在真实 uploads 上试删。

## 历史执行与修复记录

以下为各次执行当时的状态，不代表当前最新验收；后续 PASS 不改写历史 STOP。

本机首次执行在只读 baseline 阶段 STOP，尚未导入镜像、生成凭据或发布 Git；真实 slice 尚未通过。
原因是 Helm 的空 annotations、core ServiceAccount 空 apiGroup、API 省略的探针零延迟与 false 字段
被当作 drift。修正只覆盖这些已知 Kubernetes 字段；非默认值、额外 RBAC subject 与网络权限仍拒绝。
回归见 `TestAbsentAuthoredAnnotationsPermitArgoTracking`、`TestKnownAPIOmissionsPreserveAuthoredValues`、
`TestBindingSubjectDefaultDoesNotBroadenPermissions`。这次失败没有外部 mutation，保留本说明与回归，
本地只保留 latest 工作材料。基线也独立核对每个既有对象的 owner tracking 与 Argo SSA。

第二次获批执行 `a6aa7cf` / plan `0e64333d` 的远端 Quality 为 PASS，baseline 通过后在首节点
CRI image inspection STOP。控制平面已导入正确锁定镜像并登记 literal `repo:tag@digest`，
但 CRI 按 `repo@digest` 查找而失败。三个 worker 未导入；没有准备凭据、发布 Git 或执行 probe。
四节点仍 Ready，Git parent 仍为 `70147b2`。这次有外部镜像写入，原 plan、decision、baseline、
terminal 和 post-stop 诊断保留在对应 authority bundle；不清除 STOP，不自动重试。

根因依据锁定 [containerd 2.3.1 LocalResolve](https://github.com/containerd/containerd/blob/v2.3.1/internal/cri/server/images/service.go#L147-L174)
与 [ParseDockerRef](https://github.com/distribution/reference/blob/v0.6.0/normalize.go#L80-L112)。修正不改镜像制品、
compiler、密文、GitOps 清单或 D1 engine；只修正 S2 的 canonical alias，新增精确 manifest 来源检查和
分阶段错误定位。回归 `TestImageImportRegistersCRINameAndChecksAuthoredReference` 覆盖首次及已有同 digest，
`TestImageImportFailsClosedAtEveryBoundary` 覆盖错误 repo/digest 和每步立即停止。
该修正在第三次精确执行（`042b67a` / plan `82613831`，exact-head Quality PASS）完成了四节点真实 CRI
验证及 Binding 私有凭据/密文准备。但 infrastructure push 以 exit 128 STOP：S2 Git 子进程没有 HTTPS
credential helper；现有 gh session 对精确仓库有 push 权限。本地 intent commit `fd2deedd` 已保留，
远端仍为 `70147b2`，没有成功发布 receipt。只读核对 145 个 UID/content/ownership 与 live provider identity
未变；未执行 infrastructure Gate、consumer 或 probe。新凭据、密文及原 executable/plan/terminal 完整私有保留。

用 D1 已有的显式 gh helper 对同一 intent/lease 做 `git push --dry-run` 成功，远端确认未变；没有实际重推。
修正只接入既有认证策略、把仓库可写检查提前到 preparation 前、明确 push 阶段错误；不修改 lease、
publisher 输出、凭据算法或 D1 engine。`TestPublicationAccessRequiresCompleteExactWritableRepository`
覆盖缺失/未知/越权权限，`TestPublicationUsesGHStoreWithoutChangingUserGitConfiguration` 使用合成 helper
验证全局配置不变与 token 不落库。旧 STOP 和旧 intent 不清除；下一次必须审查新实现/plan 与已准备的
镜像、凭据现场，复用原凭据与密文，不因换 binary 重新生成它们。真实两阶段 GitOps 和 Web→S3 仍未通过。

infrastructure 发布前，同一个 Go 基线函数已只读验证实例 145 个资源的内容、UID 与 ownership；
以下 baseline 命令仅适用于 plan 的原 parent 仍在远端、尚未开始发布的状态：

```sh
ATLAS_S2_BASELINE_CONFIG=/absolute/path/to/s2.json go test ./internal/workloadrun \
  -run '^TestS2ReadOnlyBaseline$' -count=1 -v
```

该 opt-in 测试只做实例绑定检查、GET、Git read 和本地编译，不导入镜像、不使用凭据、不写 authority evidence。

后续执行通过 exact-head CI、四节点 CRI 和凭据复用，首次 infrastructure Git 发布成功，但在只读 Gate
的 Application 列表读取处 STOP：适配器传递了空名称位置参数，真实 kubectl 在请求 API 前即拒绝。
修正只在有名称时传入该参数；`TestKubectlCollectionAndNamedReads` 使用锁定 kubectl 与隔离 HTTP API，
覆盖列表、具名读取、managedFields 保留、403 和无效 JSON。该回归在原实现失败、修正后通过。

发布后的现场只读复核确认 145 个旧资源 UID、59 个 D1 冻结文件未变，四节点 Ready。修正后的独立
只读诊断按原 producer identity 重建并比对已发布 infrastructure 摘要，验证 26 个子 Application 和
132 项相关资源，Project VERIFIED；它不修改 mutation approval，也不把历史 STOP 改写成成功。
consumer 发布、功能 probe、重复部署尚未执行，Runtime 仍 UNPROVEN。旧 terminal、发布 intent/receipt、
原 executable 和私有凭据证据完整保留。已有 infrastructure publication 的现场不能重跑原 `deploy`，
也不能用新的 binary 冒用旧 plan；剩余写入须另行审查精确执行决定，不提供自动 continuation。

上述单元/隔离/只读测试不能替代 TLS、Cilium、Argo、Sealed Secrets、Prometheus 的实际组合验证。
S2 不是生产多租户、安全隔离 admission、端到端 mTLS 或 HA 声明。发布与最终验收保持同一个 PR。

最新一次 `atlas-s2-r1` 干净验证以冻结 `f432cfb` 完成 D1、infrastructure Gate 和 consumer 发布，
因 compiler 省略 Gateway API 默认字段导致四个 HTTPRoute 持续 OutOfSync 而 STOP。已将默认字段
显式编译并补真实 API spec 回归；编译入口进一步校验 CRD 默认值遗漏并前置 consumer 预检，
见上文。没有放宽观察器或更新现场。完整绑定、已通过/未执行 Gate 和保留
范围见 [本次验证记录](s2-clean-validation.md)；下一次 live mutation 需要新的执行决定。


后续 r2 已通过 infrastructure 和 consumer Gate，随后因 API Service proxy 被监测网络边界拒绝
而 STOP。metrics 读取改为有限 loopback port-forward，与既有 D1 访问方式一致；不开放网络、
不降低 all-replicas `up=1` Gate。历史结果与回归见 [验证记录](s2-clean-validation.md)。
