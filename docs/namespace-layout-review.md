# YAML 输入与 namespace 归属重构：部署审查

状态：本地实现审查候选；**未获部署授权、Runtime NOT RUN**。
本轮不访问或操作现存 atlas-data-r1，不执行其 readiness-repair，不删除任何集群。
旧 STOP、私钥备份、部署分支和历史验收绑定保留。

## 实现范围

- 人工维护的平台目录、values、资源声明、安装配置、应用工作区及 Project/Workload/Binding 使用 YAML。
- 锁、制品/产品、计划、工作区不可变快照、intent/receipt/STOP 和历史证据保留 JSON。
- 新产品只接受 role-scoped-v1 布局，绑定布局版本与内容 SHA256；不新增旧布局兼容入口。
- 15 个内置 namespace 由唯一注册表管理。系统名称不变，三类 Operator 与 service 实例分离，Gateway 合并；Project ID 与实际 project-<id> 分开。
- Bootstrap、Seed ownership、Application 观察、S3/凭据、DNS/TLS、RBAC、Gateway/网络与监测引用一起更新。
- 不增加数据库 Binding、多项目生命周期、通用迁移/恢复或新平台组件。

设计依据：[ADR-0022](adr/0022-role-scoped-namespace-layout.md)、[ADR-0023](adr/0023-authored-yaml.md)、[Layout Contract](namespace-layout-contract.md)。

## 来源与本地验证

输入基线：`78967ec7fe53a17f6787349e749844c4ff38f751`。
分支：`codex/namespace-ownership-layout`。实现提交、最终文件摘要及验证结果见本文件末尾的候选绑定。

已通过的本地检查（完整 `task quality`，退出码 0）：严格 YAML 正反例、确定性编译、Product 旧/混合布局拒绝、完整离线 Helm/Kustomize、
预算守恒/Operator 滚动峰值、完整有效 RBAC、NetworkPolicy 同 peer、业务更新、Bootstrap 交接与冻结 OT-1 回归。
本地日志放在 `.state/latest/namespace-layout/`；不把这些结果写成新布局 Runtime PASS。
源模板不携带实例密文，`activationReady=false` 是预期安全状态。

## 有限 Operator 投影

投影前固定 Chart 摘要及完整上游 RBAC/ServiceAccount 结构摘要；对象缺失、重复、主体或权限变化均拒绝。
CNPG 与 Strimzi 收窄到唯一 service watch；Flink 校验已配置的唯一 watch，安装域不保留完整实例权限。

| SA / 安装域 | 实例域 | 必要集群权限（当前实际渲染） |
| --- | --- | --- |
| cnpg / platform-cnpg-operator | service-postgresql | nodes 与 clusterimagecatalogs get/list/watch；仅指定 CNPG 两个 webhook configuration get/patch |
| strimzi-cluster-operator / platform-strimzi-operator | service-kafka | storageclasses get；nodes get/list（含上游 delegation bindings） |
| flink-operator / platform-flink-operator | service-flink-session | 无 ClusterRoleBinding；namespaced 的 lease/状态读取与实例管理分别绑定 |

所有实际 RoleBinding 与 ClusterRoleBinding 的权限累加验证。不能以某个 RoleBinding 看起来收窄为通过依据。
运行验收还需 `auth can-i --as=system:serviceaccount:...` 正反验证；静态矩阵不替代现场授权事实。

## 拟议的新安装边界（不是可执行授权）

| 项 | 候选范围 |
| --- | --- |
| 新目标 / deployment branch | atlas-layout-r1 / atlas-layout-r1，执行前确认均不存在 |
| 拓扑 | control-plane + gateway / compute / data，四节点 |
| 对外入口 | 仅 127.0.0.1:38080 / 38443；确认空闲后冻结，否则重新审查 |
| 默认上下文 | 创建成功后设为 kind-atlas-layout-r1；记录此前上下文供人工查阅 |
| 旧目标 | atlas-data-r1 不读写、不停止、不删除；不复用 kubeconfig/身份/凭据 |
| 产品 | 本实现干净 commit 构建；baseline D1 产品 schema 2，默认能力完整交接后再发布 data capabilities |
| 远端质量 | 新 exact-head CI SUCCESS 后才具备执行前提；本轮未推送/查询远端 |
| 私钥备份 | 独立外置目录待指定；如需同机开发例外，必须针对新实例明确接受，旧实例例外不沿用 |

没有授权清理旧 namespace、修改现有 live spec/tracking、修改外部 hosts/CA 信任或改原 Atlas。
新布局不能通过原地修改 namespace 实现；如果后续决定释放容量，旧集群删除必须单列具体 UID/容器集合。

## 新凭据与公开密文目标

全部是新实例生成并 strict 绑定的 Secret 身份，不重用或修改旧密文 metadata。

| 阶段 | namespace/name | 内容 |
| --- | --- | --- |
| D1 | platform-monitoring/grafana-admin | Grafana 登录 |
| D1 | service-object-storage/seaweedfs-auth | S3 provider 认证 |
| D1 | project-platform-smoke/s3-client | smoke S3 client |
| Data | service-postgresql/postgresql-app | 数据库普通管理用户 |
| Data | service-redis/redis-auth | Redis 用户/ACL |
| Data | platform-flink-operator/flink-webhook-password | webhook keystore 密码 |

公开发布需在最终执行 REVIEW 中锁定这些六个目标、deployment 仓库/分支及 exact-parent/lease。
明文、私钥、kubeconfig 和关联私有材料仅保存在 owner-only 本地位置。
CNPG/Strimzi/Redis/Flink 运行时生成的 CA/证书也纳入独立备份范围；同机备份不表述为灾备。
额外业务 Project/Binding 的凭据及功能写入在其意图冻结后单列，不能由这六项自动扩权。

## 执行顺序与 Gate（待形成唯一精确总计划）

1. 校验实现/CI、产品/工具/镜像摘要、目标不存在、端口/VM容量与备份目录；禁止复制旧私有状态。
2. 创建四节点；设置默认 kubectl context；D1 有限 Bootstrap 创建 platform-argocd、Seed、一次 Root。
3. 核验新增 Namespace 与全部持久 Seed 的 UID、spec、tracking/SSA/同步证据；完成 Receipt 后 Bootstrap 只读。
4. 备份 Trust Root，发布三份新 D1 密文；原 Web/S3/Grafana 与平台只读 Gate 通过。
5. 独立发布精确 AppProject 权限并等待，随后发布服务和 Operator foundation，再 CRD/证书/凭据/Operator。
6. 等待 CRD Established、Webhook/证书与 controller 当前状态；发布服务实例，最后日志/监测。
7. 按下表做有限正负验收，完成 owner/预算/namespace 布局核验。
8. 相同输入重复编译字节一致，重复发布无新 commit；凭据不轮换、功能操作不进入观察重试。

| 验证面 | 正例 | 反例/边界 |
| --- | --- | --- |
| Operator 权限 | 实例域 CR/Pod/Secret；自身必要 lease/webhook/config | 无关 service/project CR 写入、Secret get/list/watch、全局 RBAC mutation 拒绝 |
| 项目 | logical ID 生成精确 project namespace；Route/S3/metrics 引用一致；smoke listener 同时约束既有访问标签与精确 namespace | 保留 ID/前缀绕过/其他项目 destination/Secret 拒绝 |
| 网络 | 指定 Operator→JM/DB、监控、DNS/TLS、授权 S3 | 同类前缀不能通配访问；无 Binding S3 拒绝 |
| PostgreSQL / Redis | TLS、认证、临时读写、受限 Pod 重建后的数据 | 错误身份、明文连接和越权操作失败 |
| Kafka / Flink | mTLS/ACL produce-consume；JM/TM、固定示例作业/checkpoint | 无证书/无ACL失败；不据此声称 HA、恢复或项目 Flink 交付 |
| 日志 / 监控 | Loki/Alloy 授权 namespace 标记、scrape 与 Grafana | 非 allowlist namespace、Secret/exec 读取拒绝；PSA 边界不混合 |
| 生命周期 | Namespace 唯一 GitOps owner，Delete=false + Prune=false | 普通同步/删除 Application 不成为 namespace 退役授权 |

任何目标、UID、Git parent、制品、密文目标不匹配，未知写结果、超时或 Gate 失败即 STOP；
不自动清锁、回滚、改 timeout、放宽权限或改 live 字段制造 PASS。

## 执行前仍需补齐

这份材料完成实现审查范围，不代替可执行 plan。实际执行前须冻结新产品与二进制摘要、CI、
备份目录及隔离等级、Git exact parent、资源/镜像清单、各阶段预算、有限功能写入与清理对象，
以及新 UID/证书/D1 commit 的唯一派生规则，再取得一次完整授权。
现存 data-r1 的待审 repair plan 不属于本变更。

## 候选绑定

| 项 | 固定值 |
| --- | --- |
| 实现 commit | `af32f9ac8e65c103d765659f2cc5653bbab122a8` |
| 本地包 | `atlas-v0.1.0-layout.1-darwin-arm64.tar.gz`（未发布、未取得远端 attestation） |
| Archive SHA256 | `1f155bc8848346638b677053e94613459611bb5f0922851cd9ef5ebab14cd317` |
| Product SHA256 | `55e7549bf8d0a89fce1493341059304f78eb97bd66d9fffbb500fff04de43a1c` |
| Binary SHA256 | `45fa17a3faa6ca4aa056b9d1e050c34b5798e32aaeba18d2fccc90001c679b74` |
| Layout SHA256 | `58ed261b4f13c31a96d791bd660a7e7dfff10697f314542d97461f8d6ce3d2bd` |
| GitOps tree SHA256 | `9122e26687318a0dc13b3b6d5673a1e342a8173fe59113333f82256e75cd390b` |
| 本地质量 | PASS；[验证摘要](namespace-layout-validation.md) |
| Runtime / 远端 CI | NOT RUN |

包位于本地 `.state/latest/namespace-layout/candidate-package/`。此摘要绑定实现候选，不是授权摘要；
未来执行总计划还需包含前述确切运行输入与备份决定。任何候选变更重新生成审查材料。
