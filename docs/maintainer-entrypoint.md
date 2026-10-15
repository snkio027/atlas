> This page preserves the historical maintainer entry point. For everyday use, see [application delivery](application-delivery.md).

# atlas-refactor

从 Atlas 的架构和失败经验出发，用 Go 独立实现 Bootstrap。

已冻结的架构验证基线是 [S1 单次 29/29 验收与冻结](s1-final-validation.md)。
下文的最小 Bootstrap 与 dev02 数字属于各自历史基线；不能据此判断当前本机集群。
进入 S2 前的代码与操作质量检查见 [工程审查](pre-s2-engineering-review.md)。
**D1 可安装预览版已通过本机全新首装验收**，PR #8 已审核合入。入口与验收边界见
[首装文档](first-install.md)、[D1 验收记录](d1-validation.md) 和
[Proposed ADR-0014](adr/0014-installable-development-preview.md)。本轮按所有者决定，使用当前主机和当前仓库的新部署分支验收；不能称为独立机器验证。实际 CI 包已完成四节点首装、服务功能、访问和重复安装验证；发布 Gate 仍待审核。

**S2 已合入 main，功能冻结**：Project / WebService / CapabilityBinding、确定性编译和 Web→S3 slice。
[r7 Runtime PASS](s2-r7-validation.md) 绑定 `d011813`；`fc809be` 完成入口/终态增量修复、回归与复审，
PR #9 以 `6dcc6d3` 合入。ADR-0015/0016/0017 已按单所有者本地开发范围接受。
语义见 [S2 契约](s2-semantic-contract.md)，命令与验证边界见 [S2 工作流](s2-workloads.md)。

项目代码采用 [MIT License](../LICENSE)，第三方组件保留各自许可证。

当前版本包含本地可构建的 `doctor`、`render`、`status`、`apply`，以及完整流程的
模拟契约测试。公开远端为 `snkio027/atlas-refactor`。第四次真实运行已通过最小
Bootstrap 闭环：External Root / GitOps 接管、中断续跑、39 个持久 Seed 的证据、
重复 apply 零写入，以及三类失败拒绝与恢复。见[第四轮结果](integration-20260927-04-result.md)。
此结论限于下述可丢弃环境；ADR 保持 Proposed，尚未进行原 Atlas 权威切换。
第一阶段只支持独立的、可丢弃的 OrbStack / 单节点 Kind 测试集群。

近期开发优先级是独立的 **Web/API 开发平台**：Cilium、Gateway API / Envoy Gateway、
cert-manager、本地存储和受限业务 namespace。清单、本地验证和 Cilium-first 启动已实现；
`profiles/development.json` 现采用独立的 schema 3 四节点开发 profile。
四节点全新启动、15 个 App 接管、HTTPS/PVC 与重复 apply 零写入已真实通过。
当前启动与日常命令见[四节点开发工作流](development-four-node.md)。见[开发平台与部署审查](development-platform.md)和[本机启动记录](development-runtime-20260927.md)。

当前定位仍是 **successor candidate**；原 Atlas 的 Decision / Cutover Gate 保留。
已验证实现与报告按 SHA 冻结；下一 Gate 是外部契约与迁移过程，当前切换结果为 **NO_GO**。
见 [Go Bootstrap Cutover Contract](go-bootstrap-cutover-contract.md)、
[静态 behavioral parity audit](bootstrap-behavioral-parity-audit.md) 和
[基线绑定清单](evidence/bootstrap-baseline-20260927.json)。默认语言决定与实际替换
分别提案、分别审批；此文档不授予原 Atlas 的 Tier-0 权限，也不启动新一轮集群测试。

## 构建与检查

需要 Go **1.27.2**。人工配置解析使用锁定的 YAML 模块，其余模块边界见 ADR-0023；源码内 vendor 支持离线构建。

```sh
task quality
task build
./bin/atlas --help
```

`task quality` 检查格式、运行 `go vet`、race-enabled 契约测试、Lua 健康检查和开发清单校验，并编译/vet 三个冻结的 OT-1 专用入口；不访问集群。
现在还需本地 Helm 4.3.0、kubectl 1.37.1、yq 4.54.1、Lua 5.5.1、kubeseal 0.40.0（当前 checksum lock 为 darwin/arm64）；
Task 优先使用现有 `.state/tools/{helm,kubectl,yq,kubeseal}`，缺少时使用 PATH；
显式 `PLATFORM_HELM`、`PLATFORM_KUBECTL`、`PLATFORM_YQ`、`KUBESEAL` 参数优先，Lua 使用 `PLATFORM_LUA` 或 PATH。不会自动安装工具。
工具路径参数与清单渲染命令见[开发平台说明](development-platform.md#文件与复现)。
冻结 OT-1 渲染另用原 Helm 4.2.3 / kubectl 1.36.3 / yq 4.53.6，位于 `.state/tools/ot1`
或 `OT1_TOOLS` 指定目录；`packaging/prepare-ci.py` 从固定提交读取并校验这些测试工具。
新产品包不包含它们。见[升级说明](dependency-upgrade-20261010.md)。
Go 自动工具链获取与模块网络访问被关闭。若本机没有 Task，也可运行：

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=vendor go test -race ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=vendor go vet ./...
GOTOOLCHAIN=local CGO_ENABLED=0 go build -trimpath -o bin/atlas ./cmd/atlas
```

真实 Helm 渲染校验使用预装的 **4.3.0** 可执行文件：

```sh
task quality PLATFORM_HELM=/absolute/path/to/helm
```

该校验渲染两次并比较结果，同时检查固定镜像、CRD、Secret 边界及 Seed/接管一致性。
Task 将选定工具传给真实测试；单独运行 `go test` 时，未设置 `ATLAS_TEST_HELM` 才会跳过该项。`task build:matrix` 交叉构建三种目标；
交叉构建不等于已支持或已验证 Linux 运行环境。

## 首次集成验证

第一次固定配置见 `profiles/integration.json`，对应不可移动的第一次基线。
第二、三次固定配置分别为 `profiles/integration-02.json` 和 `profiles/integration-03.json`。
已通过的固定配置为 `profiles/integration-04.json`，基线为 `integration-20260927-04`
（`0ef918801dac84ac913416f4c1756bf08045a485`）。目标与证据要求见
[第四次集成 Gate](integration-fourth-slice.md)。重跑须使用对应基线的干净 checkout；
main 后续的文档提交不改变已测试的 GitOps revision。
`status --check` 是严格退出码的显式写法。schema 3 在有效 Receipt 后，`ADOPTED`
只证明持久交接与 Seed ownership；平台 rollout/runtime 需独立验证，不能仅看 status 退出码。
现有 `atlas-dev verify` 仍独立验证开发 rollout 与节点；OT-1 完整验证使用 Gate-B，见
[ADR-0011](adr/0011-desired-identity-and-durable-handoff.md)。
新建测试节点启用 Metadata-only API 写入审计，日志位于私有 `.state/audit/`；
记录中不包含请求/响应正文。ADR 保持 Proposed。

## 配置与渲染

```sh
cp config.example.json config.local.json
```

编辑 `repositoryURL`，填入 Argo CD 可匿名读取的 HTTPS Git URL。
本阶段没有私有仓库凭据配置功能。`config.local.json` 被 Git 忽略。

```sh
./bin/atlas doctor --config config.local.json --tool-dir /absolute/path/to/locked-tools
./bin/atlas render --config config.local.json --tool-dir /absolute/path/to/locked-tools
./bin/atlas render --config config.local.json --tool-dir /absolute/path/to/locked-tools --output gitops/test
```

`--tool-dir` 可指向预装的 Helm、Kind、kubectl 所在目录；未指定时从 PATH 查找。
工具版本必须与 `versions.lock.json` 一致。Docker 与 Git 从 PATH 查找。
编译和运行不自动安装工具，也不拉取镜像。当前节点平台限定 `linux/arm64`；
预载通过本地 Docker archive 流式导入 containerd，并保留锁定的镜像 digest。

`doctor` 检查制品摘要、工具版本、OrbStack 上下文绑定和本地镜像存在性。
它不证明远端 Git、真实集群或接管已准备好。
`render` 默认只写 `.state/rendered/`；指定 `gitops/test` 时输出可提交的 GitOps 树。
配置、资源和镜像改变后必须重新渲染、审查和提交。

## 后续真实闭环

以下是操作说明，本地测试不会执行它们。

1. 准备锁定的工具和三种镜像；核对 `doctor`。
2. 渲染 `gitops/test`，审查并提交新仓库，将提交发布到配置中的远端分支。
3. 单独确认集群名、Docker 上下文、Git 提交和 Tier-0 目标。
4. 使用两个显式批准参数执行：

```sh
./bin/atlas apply --config config.local.json \
  --tool-dir /absolute/path/to/locked-tools \
  --approve-cluster atlas-refactor-test --approve-tier0
./bin/atlas status --config config.local.json --tool-dir /absolute/path/to/locked-tools
```

`apply` 要求干净的 Git 工作树、已提交且与当前渲染一致的产物，以及远端 revision
恰好指向本地 HEAD。首次安装创建独立 Kind 集群并绑定本地私有 kubeconfig；
随后预载镜像、安装 Seed、创建最小 AppProject 与 External Root，等待 GitOps
接管并创建一次性 Receipt。它不会删除或替换集群。

默认集群名为 `atlas-refactor-test`，也可使用该名字加短后缀。旧 Atlas 名称被拒绝。
继承的 `DOCKER_*` / `KIND_*` 环境变量被拒绝，避免命令目标被环境覆盖。

## 状态、重试与限制

`status` 输出 JSON：成功接管返回 `0`；缺失、接管中或退化返回 `1`；
漂移、不可读或不确定状态返回 `2`。其他命令的失败返回非零。

`HANDOFF_PENDING` 开始于 Root 创建前写入的不可变 latch。此后只允许观察和
一次性 Receipt 提交，不再安装 Seed、修复 Root 或修改 Bootstrap AppProject。
Root 创建结果不确定或交接后 Root 丢失会拒绝正常重试。Receipt 存在时的健康
退化也不会恢复 Bootstrap 权限。四个 Application 的精确 Git revision 和每个
持久 Seed 对象的接管证据都是持续检查条件：普通对象检查 tracking/SSA，
CRD 检查 SSA spec、当前资源清单和精确 revision 的成功同步结果。健康全绿和
Receipt 本身不能替代这些证据。Git 源或 ownership 读取不可用时返回非零。

`.state/apply.lock` 防止同一工作树并发执行。进程被强制终止后可能保留该锁；
必须先确认旧进程结束并检查集群状态，再由所有者处置。不要盲删 `.state/`：
其 kubeconfig 与摘要是目标绑定证据，丢失后本版本不会重新绑定已有集群。

这个版本不实现跨主机锁、独立恢复命令或对管理员删除证据的 Admission 防护。
`immutable` 防止内容修改，不能防止删除重建。因此它是可丢弃环境中的验证实现，
不能作为生产恢复或安全隔离保证。更广的运行环境、私有 Git 支持、发布二进制的
签名/SBOM/来源证明，以及工具分发摘要验证仍待后续工作。

## 文档

- [Web/API 开发平台与部署审查](development-platform.md)
- [ADR-0004：受限本地开发平台（Proposed）](adr/0004-local-web-development-platform.md)
- [架构与生命周期](architecture.md)
- [新项目 ADR-0001：实验基础（Proposed）](adr/0001-go-bootstrap-foundation.md)
- [ADR-0002：默认 Go 语言提案（Proposed）](adr/0002-go-default-control-plane-language.md)
- [ADR-0003：独立切换 Gate 提案（Proposed）](adr/0003-gated-bootstrap-successor-cutover.md)
- [Atlas 经验采纳清单](atlas-lessons.md)
- [测试与验证记录](verification.md)

平台能力扩展、监测与 S3 候选的本地命令和部署 Gate，见
[平台能力扩展](platform-capabilities.md)。

## S1 Observation 与 Ownership Rehearsal

[实现说明](s1-observation-ownership.md) 介绍共享 Go 只读观察器、Evidence envelope、
OT-1 独立 profile、29 阶段 plan/checker 与有限 run。`dff5a20` 已在干净四节点集群通过普通单次 29/29，S1 runtime 收口，功能冻结。
见 [最终验证](s1-final-validation.md) 和 [证据索引](s1-evidence-index.md)。
PR 审核、ADR Accepted、CI/发布与原 Atlas cutover 仍为独立 Gate；ADR 保持 Proposed。
观察成功不授权 mutation，运行需完整精确计划的独立批准。
