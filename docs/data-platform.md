# 数据平台能力与部署审查

实现候选、默认未启用、运行时未验收。参见 [ADR-0021](adr/0021-local-data-platform.md)
与 [本地验证](data-platform-validation.md)。迁移发生在未部署的源码候选中，不接管旧 r3 的资源。

Strimzi 1.3.0 的启动配置必须保留官方支持的 4.2.0、4.2.1、4.2.2、4.3.0、4.3.1
完整镜像目录，分别固定真实 digest。当前 Kafka CR 仍明确选择 4.3.1；目录完整性不等于
启用 Connect、MirrorMaker2 或其他 Kafka 实例。离线制品准备沿用现有规则，包含已激活
清单引用的全部固定镜像。上游支持目录变化需要重新核验渲染与真实配置解析器。

| 能力 | 管理方式 / 版本 | 内部地址 | 数据 |
| --- | --- | --- | --- |
| PostgreSQL | CloudNativePG 1.30.1 / PostgreSQL 18.6 | postgresql-rw.postgresql.svc.cluster.local:5432 | 8Gi，TLS/SCRAM |
| Redis | StatefulSet / 8.10.2 | redis.redis.svc.cluster.local:6379 | 2Gi，TLS/ACL/AOF |
| Kafka | Strimzi 1.3.0 / Kafka 4.3.1 | kafka-kafka-bootstrap.kafka.svc.cluster.local:9093 | 8Gi，KRaft/mTLS/ACL |
| Flink | Operator 1.16.0 / Flink 2.3.0 | flink-session-rest.flink.svc.cluster.local:8081 | 4Gi，1 JM + 1 TM/2 slots |
| Loki | StatefulSet / 3.7.8 | loki.logging.svc.cluster.local:3100 | 8Gi filesystem，72h |
| Alloy | Deployment / 1.20.1 | alloy.logging.svc.cluster.local:12345 | API allowlist 日志采集 |

三个 Operator 只 watch 各自 namespace。CRD、控制器和领域 CR 分开拥有；Argo 不管理
Operator 生成的 Pod/Service/Secret/PVC。Flink 使用 Operator 的 standalone 模式，固定 TM
副本并关闭运行 JobManager 的 Kubernetes RBAC/token；领域控制器仍持续调谐。

## 准备与发布顺序

以下只在源码准备目录执行，不在旧 deployment branch 直接运行：

```bash
task quality
go run ./cmd/atlas-platform plan \
  --capabilities monitoring,object-storage,storage-monitoring,postgresql,redis,kafka,flink,data-observability
```

plan 列出依赖、权限增量和缺失密文；没有密文时 readyToEnable=false，不会自动读取集群凭据。
新实例先从默认能力构建并完成 D1；数据能力是 post-install 扩展，提前选择它们会阻止 D1 打包。
新产品携带领域 CR health 定义；冻结 S1 catalog 不添加这些定义或重写历史输入。

1. 在新 D1 实例完成交接后，先单独发布本阶段的 AppProject 权限增量，并核验
   project-bootstrap 的当前 Git revision、同步完成及 live AppProject 精确 spec。
   再发布五个 foundation：postgresql/redis/kafka/flink/logging。
   保留既有 enabled 集合；Sealed Secrets 按 consumer namespace 投影有限 watch/RoleBinding。
2. 发布 CNPG/Strimzi/Flink CRDs，等待 Established。CRD 与原 canonical AppProject 分工不变。
3. 核验并备份新实例 Trust Root 后，离线生成三个独立扩展凭据：

```bash
atlas-platform prepare-credentials --credential-profile data-services \
  --cert /absolute/reviewed-instance-public.pem --kubeseal /absolute/locked/kubeseal
```

   输出仅为严格 namespace/name 的三个 SealedSecret：postgresql/postgresql-app（username/password，
   basic-auth）、redis/redis-auth（password/users.acl）、flink/flink-webhook-password（password）。
   私有文件留在 0600 `.state/data-services`；D1 三份凭据/密文不重写，重复准备不轮换。
4. 发布 data-credentials、Redis data-pki、三个 Operator；等待 controller/webhook/证书就绪。
5. 发布 Cluster、Kafka/NodePool/KafkaUser、FlinkDeployment，以及 Redis、Loki；最后发布 Alloy
   与 Grafana datasource、ServiceMonitor、Pod/PVC 告警。Strimzi/Flink health 核对当前 generation；
   CNPG 1.30.1 只提供 readiness，另核对子对象的配置与运行结果。
6. 执行协议、权限负例、持久化与幂等验收。未知、漂移、timeout、Gate failure 即 STOP。

后续阶段若增加 namespace 或资源 kind 权限，同样先提交并核验权限，再提交消费者。
不能用同一 commit 中的文件顺序或 External Root 的首次 sync wave 保证后续更新顺序：
project-bootstrap 与 platform-control 会独立观察 Git。权限尚未落地时，新 Application
可能已经被创建并报 InvalidSpecError。已记录的 STOP 不因随后自主收敛而改写为历史 PASS。

新数据 CRD 的打印列按 API 表示投影：省略 additionalPrinterColumns.priority 的数值零，
保留非零值及 CR schema 内的 default。Flink 固定 Chart 中的显式零会被 API 省略，导致
Argo SSA 比较持续 OutOfSync；修复发生在 renderer，不使用 ignoreDifferences。

deployment branch 不带完整 authored/catalog/vendor 输入。执行计划须先生成源清单，再沿用 D1
repository/revision projection 写入实例分支；不能把源码 development branch 当部署目标。
只允许新增能力目录、platform Applications、platform-project 最小增量、Sealed Secrets watch/RBAC。
Root、signal、既有业务与该实例已经冻结的 authority 字节必须保持；不覆盖整套源码 render。

## 权限、监测和容量

- PostgreSQL platform 用户只是 platform 库 owner，enableSuperuserAccess=false。
- Redis platform-admin 与 Kafka platform-admin 都是受审管理身份，不是普通业务 Binding。
- 各数据服务默认不向业务 namespace 开放。当前 S2 Binding 仍只支持 object-storage；新数据库、
  Kafka 用户/topic/ACL 和业务网络关系需独立评审，不能借用平台管理账号。
- Flink REST 不认证、Loki API 无认证，因此都不增加外部 route；日志通过现有认证 Grafana Explore。
- Alloy 仅采集 postgresql/redis/kafka/flink/workload-web，不读 Secret/exec，不用 hostPath/root。
  指标包含 Loki/Alloy scrape 与通用 Pod/PVC 告警；尚未承诺全部数据库业务指标/专用看板。
- Operator、CNPG instance、Alloy 的 API egress 使用 TCP 443/6443 端口限定；webhook ingress
  使用 9443。标准 NetworkPolicy 不提供 API-server identity，不能把此规则描述成目的身份隔离。
- 单实例开发拓扑；新增 PVC 请求合计30GiB。Flink JM/TM 同 data 节点共享 RWO，不能直接跨节点。
  直接声明的容器请求约1.25GiB，另加 PostgreSQL 256Mi、Kafka 512Mi、User Operator 128Mi、
  Flink JM 1024Mi/TM 1536Mi（运行开销及 init 另计）。总稳态约4.6GiB请求，加原D1与节点开销。
  完整 limits/调度容量必须以 Operator 实际生成的 Pod 复核，不能仅加 authored Deployment。
- OrbStack 约16GiB内存由四节点共享，不能将各 node capacity 相加。执行前检查VM/磁盘余量。

## 新集群部署审查（待精确执行绑定）

候选目标与 deployment branch：atlas-data-r1；1 control-plane + gateway/compute/data。
建议旧 r3 删除后复用 loopback18080/18443；新建后设置默认 kubectl context。

用户已明确：**旧集群没有需要保留的数据，可以按需删除重建。** 不再要求数据导出或重复确认
可丢弃性。仍须在执行计划中核对旧 cluster UID/四容器集合，保留 Git、验收/STOP、私钥备份。
这不等于已经执行删除。新的 Trust Root、同机备份例外和公开密文仍需具体实例计划绑定。

拟用私有备份目录：`/Users/nekoreb/Workspace/01_Vault/atlas-refactor-data-r1`，隔离等级待确认。
备份包括 Sealed Secrets key、CNPG CA/复制证书、Strimzi cluster/client CA/User identity、Redis CA、
Flink webhook TLS/keystore。公开分支最多发布六个既定 SealedSecret：D1三份 + 本轮扩展三份。
明文、私钥、kubeconfig 不出站；不是复用旧实例的授权/密钥。

执行 REVIEW 还需锁定：实现/CI、D1 package、全部镜像与chart、旧目标身份、端口/容量、
Git exact-parent、投影 diff、唯一派生 UID/commit/证书规则，以及以上密文/备份范围。
本轮先完成实现与本地验证；此文不是已经获得的集群执行批准。

## 真实验收 Gate

1. 新D1交接、默认上下文、所有CRD Established；控制器watch范围、RBAC与TLS webhook正常。
2. Git拥有CR，生成子对象ownerReference指向相应CR/控制器；不出现Argo双重ownership。
3. PostgreSQL CA/hostname/SCRAM与临时表读写；错误密码/非TLS失败；superuser无远程密码。
4. Redis TLS SET/GET/DEL、默认用户/错误密码/明文失败；AOF重启持久化。
5. Kafka显式临时topic produce/consume；无证书/无ACL失败；重启保留cluster ID与数据。
6. Flink JM/TM/2slots和锁定示例作业；streaming checkpoint持久化。恢复/savepoint操作另行明确，
   没有HA metadata，不由PVC存在或Operator运行推导自动恢复已证明。
7. Loki/Grafana中可查询allowlist测试标记；越权namespace/Secret/exec失败；Loki/Alloy up=1；
   通用告警和原Grafana/S3/Web不回归。
8. 数据Pod重建后的持久化、CR exact spec、子对象配置/镜像与资源预算均实测；
   Strimzi/Flink 条件核对当前 generation，CNPG readiness 不替代 rollout 证明。
9. 重复render逐字节相同；重复准备不轮换；重复发布无新commit；功能probe不放入观察重试。

CNPG1.30对Kubernetes1.37仅为tested。Retain不保护Kind删除，Operator不构成HA/DR/生产就绪。

## 本地配置检查

```bash
# 已预载固定镜像；无pull、无Kubernetes、无端口开放
python3 scripts/check-logging-configs.py --docker-context orbstack
```

完整质量另包含真实Helm/Kustomize、候选schema/default、ownership/权限负例和冻结S1回归。
