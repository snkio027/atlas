# ADR-0021：以领域 Operator 管理本地数据基础设施

- 状态：Proposed，等待维护者审查；不是部署批准
- 范围：单所有者、四节点 Kind 开发实例；Tier-1 capability 扩展
- 前置：ADR-0006/0007 的目录与权限投影、ADR-0020 的 namespace 约定

## 决策与原因

PostgreSQL 采用 CloudNativePG，Kafka 采用 Strimzi，Flink 采用 Apache Flink Kubernetes
Operator。用户明确要求在首次部署前迁移到 Operator，取代尚未发布的手写运行清单。
没有旧数据库的数据迁移，也不接管 r3 的现有对象。

Git 定义 CRD、Operator 配置与领域 CR；Argo 同步这些声明；领域 Operator 管理其运行子对象。
不同时向 Git 提交 Operator 生成的 Pod、StatefulSet、Service、Secret 或 PVC。
Bootstrap 不增加权限，Atlas 不成为数据库控制器。采用上游生命周期实现，避免自制启动、
证书重载、实例替换和版本协调脚本。Operator 不等于 HA、备份或生产验收已经完成。

| 能力 | 实现 | namespace | 本轮拓扑 |
| --- | --- | --- | --- |
| PostgreSQL | CNPG 1.30.1 / chart 0.29.1 / PostgreSQL 18.6 | postgresql | 1 个实例，8Gi |
| Redis | 官方 8.10.2 StatefulSet | redis | 1 实例，TLS、ACL、AOF，2Gi |
| Kafka | Strimzi 1.3.0 / Kafka 4.3.1 | kafka | 1 个 KRaft broker/controller NodePool，8Gi |
| Flink | Operator 1.16.0 / Flink 2.3.0 Java17 | flink | Operator-managed standalone session，1 JM、1 TM、2 slots，4Gi |
| Loki / Alloy | 3.7.8 monolithic / 1.20.1 collector | logging | filesystem 8Gi、72h retention |

Redis/Loki 没有为本轮需求引入额外 Operator。Kafka 不部署 ZooKeeper、Connect、MirrorMaker、
Bridge 或 Cruise Control；Flink 不创建业务 Job 或自动回滚。新 namespace 无 atlas 前缀。
`permissionDomain=data` 仍映射 canonical platform-project；域元数据不是租户隔离承诺。
单所有者、每域一个实例时，controller 与该域服务共用 postgresql/kafka/flink namespace，
通过独立 ServiceAccount/Role 和 GitOps owner 区分权限，避免为单域控制器增加跨 namespace
授权。这不是 controller/数据实例之间的 namespace 隔离；未来项目独享实例需重新审查布局。

## 供应链与兼容性

三个官方 Helm 包 vendored 并验证上游发布摘要；Helm 只是离线 renderer，没有 release 状态。
Operator 与动态运行镜像都锁定版本和 index SHA-256，核验 ARM64/AMD64。Strimzi 1.3.0
启动时校验完整支持目录，因此保留 4.2.0、4.2.1、4.2.2、4.3.0、4.3.1 与各自真实镜像
的固定映射；当前 Kafka CR 只声明 4.3.1。首次运行证明裁成单项会使 Operator 立即退出，
完整目录不代表启用其他版本实例或 Connect/MirrorMaker2。辅助镜像同样固定；
CNPG 的默认 PostgreSQL 和 init/bootstrap Operator 镜像均固定。
Flink CR 明确指定运行镜像。版本/来源见 capabilities/versions.lock.json、operator-images.json。

CNPG 1.30 的 Kubernetes 正式支持范围为 1.34–1.36，1.37 为 tested；本仓库的 1.37.0
因此仅按开发兼容候选处理。这是部署审查的已知限制，不标记为正式支持或生产可用。
Flink chart 默认开发镜像被显式替换为官方 1.16.0 稳定镜像。

## 权限与网络

- CRD 与 Operator 分成独立能力，foundation → CRD → credential/operator → CR/runtime。
  CRD 删除需确认且不随 Application 删除；不提供本轮 retire/drop 协议。
- CNPG 仅 watch postgresql；namespaced Role 管理实例、Secret、PVC、角色等。
  集群权限只有 nodes/ClusterImageCatalog 只读，以及按 resourceNames 限定的两个自身 webhook
  get/patch。webhook 仅匹配 postgresql namespace。
- Strimzi 仅 watch kafka；namespaced RoleBinding 授权控制器和 User Operator。
  `STRIMZI_CREATE_GLOBAL_RESOURCES=false`，去掉 global ClusterRole 的 ClusterRoleBinding 写权限；
  节点/StorageClass 发现保持只读。没有为业务账号绑定全局角色。
- Flink 仅 watch flink，Operator Role 在 flink；采用上游 standalone deployment mode，
  Operator 负责 JM/TM Deployment，固定 1 TM/2 slots。关闭 jobRole/jobRoleBinding，JM/TM
  不挂载 API token、不持有 Kubernetes 写权限。这是 Operator 管理模式，不是手写运行 Deployment。
- Foundation 仍使用 restricted PSA、quota、LimitRange、默认拒绝和 DNS 许可。
  Operator/实例 API egress 限定 TCP 443/6443；webhook ingress 限定 9443。
  标准 NetworkPolicy 无 API-server identity selector，以上控制端口未限定目的/来源地址，
  这是明确限制。未新增 Cilium 策略或宣称端口限定等于身份隔离。
- PostgreSQL 应用连接要求 TLS/SCRAM；禁用 superuser 网络凭据，platform 是 platform 库 owner。
  Redis 禁用 default 用户，使用独立 TLS/ACL 管理身份；禁止把它作为业务 Binding。
- Kafka 仅内部 TLS/mTLS listener 9093，ACL 未匹配即拒绝。Strimzi 生成 CA 与
  platform-admin KafkaUser 证书；没有 topic/user 的隐式业务授权。listener 只允许同 namespace
  带精确 data-admin 标签的受审管理 Pod。业务用户、topic、ACL、双向网络许可另审。
- Flink REST 与 Loki API 均为内部管理端点；不增加匿名浏览器 route。
  Loki 通过已有认证 Grafana 查询。S2 Binding 本轮仍只支持 object-storage。

## Secret、数据与恢复边界

三个 data-services SealedSecret：postgresql/postgresql-app 的 basic-auth、redis/redis-auth
的 password/ACL、flink/flink-webhook-password 的 PKCS12 密码。离线生成、严格 namespace/name，
与 D1 三份凭据和私有记录独立；重复准备不轮换。Git 永不保存 Secret 明文。

CNPG 管理数据库 TLS/复制证书，Strimzi 管理 Kafka CA/User 证书，cert-manager 管理 Redis TLS
和 Flink webhook TLS。私钥是新实例运行状态，必须纳入部署审查的私有备份/隔离级别，
不能复用旧实例密钥。自动证书维护不等于已经演练 rotation/DR。

PVC 使用 atlas-local-retain。Git 直接拥有的 PVC 有 Delete=false/Prune=confirm；Kafka
NodePool 的 deleteClaim=false，CNPG inheritedMetadata 保护注解随生成对象传递。
后者不是对 Operator/GC 的删除拦截，所以 Cluster/Kafka/NodePool/FlinkDeployment 的删除
也必须确认。Kind 删除仍会销毁节点本地盘；Retain 不是备份。

Flink JM/TM 固定同 data 节点共享 RWO state PVC。没有 HA metadata 配置、跨节点共享卷或
savepoint 自动恢复承诺；所有作业变更/恢复需独立评审。Redis AOF everysec 也不是零丢失保证。

## 观察与日志

Argo 对 Strimzi Ready、Flink STABLE + JM READY 要求当前 observedGeneration；旧条件或
缺失条件不放行，FAILED/NotReady 明确失败。CNPG 1.30.1 的 Ready 条件不提供
observedGeneration：按该版本实际状态检查 Ready、healthy phase、实例数、镜像和稳定 primary；
如条件带有非零 generation 仍拒绝过期值。这只表示 readiness，不证明每个 spec 字段已生效。
运行验收另核对 CR exact spec、子对象 owner、Pod 镜像/配置、SQL 与证书。Flink TaskManager
slots、Kafka produce/consume 等也由真实功能验收证明，不以 CR Healthy 代替。

Alloy 只收集 postgresql/redis/kafka/flink/workload-web 的 Pod 日志；每个 namespace 的 Role
仅 pods get/list/watch、pods/log get，无 Secret、exec、hostPath 或 privileged。
不默认采集 archive、argocd、sealed-secrets；日志内容仍可能包含应用自行输出的敏感信息。
Loki filesystem/72h retention 不使用业务 uploads 桶凭据。没有 exactly-once 日志保证。

## 安装与审查

能力默认未启用，D1 product 拒绝提前纳入 post-install 数据能力。新增 health 定义随新产品
argocd-self Seed/自管理清单一致发布；不对旧 D1 identity 作历史改写。
新实例先完成 D1，再发布 namespace/CRD/凭据/Operator/领域 CR/日志监测。
完整实际执行前必须冻结新目标、Git parent、投影 diff、镜像、私钥备份、密文与旧数据处置。
本地 render/schema/quality 不授权集群写入，也不代表 runtime PASS。

## 上游依据（2026-10-14 至 15 核验）

- [CNPG 支持矩阵](https://cloudnative-pg.io/documentation/current/supported_releases/)、[Operator 配置](https://cloudnative-pg.io/documentation/current/operator_conf/)、[官方 Chart](https://github.com/cloudnative-pg/charts/tree/cloudnative-pg-v0.29.1/charts/cloudnative-pg)。
- [Strimzi 1.3 部署与权限](https://strimzi.io/docs/operators/1.3.0/deploying)、[官方发行版](https://github.com/strimzi/strimzi-kafka-operator/releases/tag/1.3.0)。
- [Flink Operator 官方发布](https://flink.apache.org/downloads/#apache-flink-kubernetes-operator)、[1.16 部署模式](https://nightlies.apache.org/flink/flink-kubernetes-operator-docs-release-1.16/docs/deployment/overview/)。
- [Redis 发布](https://download.redis.io/releases/)、[Loki 存储](https://grafana.com/docs/loki/latest/configure/storage/)、[Alloy API 日志源](https://grafana.com/docs/alloy/latest/reference/components/loki/loki.source.kubernetes/)。

CNPG readiness 语义依据锁定版 [SetClusterReadyCondition](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/pkg/resources/status/transactions.go)，不能用未来版本的条件字段推断当前行为。
