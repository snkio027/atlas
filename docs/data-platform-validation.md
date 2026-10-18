# 数据基础设施候选：本地验证

日期：2026-10-15。基于 namespace 候选 `d046c7b`，分支 `codex/data-platform-foundations`。
本记录对应 Operator 迁移后的源码候选。首次新集群执行已完成 D1，`660b96e` 修正后
CRD Gate 已通过；`adcaf5d`的Strimzi修正及权限Gate也已通过。运行实例已发布，
但初始化健康判断触发STOP，另发现Redis探针参数错误及CNPG admission默认值差异；尚无数据组件Runtime PASS。原因与修复见
[失败记录](data-platform-failures.md)。下表是本地验证，不能替代运行验收。

| 验证 | 结果与范围 |
| --- | --- |
| 完整 task quality | PASS / exit 0：Go race/vet、真实Helm/Kustomize、冻结OT-1契约、实验入口build/vet |
| 真实 Helm / Kustomize | 三个锁定官方 Chart 离线渲染；CRD、控制器、领域 CR 分开投影，候选资源均校验 |
| 重复渲染 | PASS：46个新增生成文件逐字节相同 |
| Capability inventory | 20 个新增可选能力，138 个资源（空密文占位，不含平台 Application/权限增量）；全选闭包31项 |
| 默认安装 | 仍启用原345个资源；新增领域 health 同时进入新产品 Seed/自管理清单，未声称字节不变 |
| Supply chain | 三个Chart验证发布摘要；镜像固定index SHA-256，核验ARM64/AMD64，来源记录进入仓库 |
| Strimzi 支持目录 | 五个版本逐项固定真实镜像；真实上游解析器离线验证：单版本配置拒绝、完整目录通过。无 Kubernetes 访问，不作为控制器就绪证明 |
| 凭据 | 真实离线kubeseal验证三个data-services目标；basic-auth类型、独立profile、重复不轮换、明文/宽scope拒绝 |
| RBAC / namespace | 五个无atlas前缀的保留namespace；默认deny；operator固定watch范围，global权限仅经审查的发现/自身webhook |
| Runtime ownership | PostgreSQL/Kafka/Flink由领域CR定义；Argo不同时声明其Deployment/StatefulSet；Flink runtime无API RBAC/token |
| CRD 默认值 | CNPG/Strimzi/Flink、Certificate、ServiceMonitor、PrometheusRule通过schema/default-stability校验 |
| Operator health | 真实Lua验证Strimzi/Flink当前generation；CNPG锁定版本readiness及过期/缺失/矛盾负例 |
| 数据节点调度 | 三个Operator的Pod模板显式容忍D1 data节点污点，回归防止仅nodeSelector导致永久Pending |
| Redis | 锁定镜像、无网络的 `scripts/check-redis-probes.py` 执行清单中的三个真实TLS/认证探针；旧参数顺序与错误密码负例均拒绝。旧简化PING测试未覆盖实际argv，不能作为此前探针正确性的证据 |
| CNPG admission | 脱敏真实admitted spec固定30项额外默认值；显式声明后保持完整spec比较，不将未知live字段视为等价 |
| Loki / Alloy | 锁定镜像执行verify-config / validate，无网络；脚本见scripts/check-logging-configs.py |

本地证据索引：`.state/latest/data-platform/operators/{quality.log,quality.exit,render.log,plan.json,inventory.json}`。
它们是可替换的开发证据；最终集群验收另保留不可变记录。

## 本轮固化的实际兼容边界

- CNPG v1.30.1 `SetClusterReadyCondition` 不填 `observedGeneration`。不能等待不存在的字段，
  也不能假称完整rollout已证明。health核对Ready、phase、实例数、镜像、primary；实际部署需
  独立核对CR exact spec、生成子对象owner/Pod配置、SQL和证书。
- Flink固定1个TaskManager/2slots采用Operator的standalone模式；native模式不会按同一方式
  使用显式TM副本数。standalone的JM/TM不需要Kubernetes写权限。
- D1 data节点有NoSchedule污点，Operator领域模板必须显式提供精确toleration。
- 冻结S1 catalog不引入新health输入，历史Bootstrap/OT-1快照不改写。
- Redis探针保留 `redis-cli -e`，避免错误认证/命令返回成功。

此前手写PostgreSQL/Kafka清单的进程启动结果不作为Operator候选的运行证据。
当前Operator启动Gate已通过；仍未完整验证：修正后的运行资源Gate、真实生成Pod完整审计、PVC持久化、NetworkPolicy执行、
SQL/Redis/Kafka协议负例、Flink作业与checkpoint、Loki查询链路，以及原D1服务不回归。
CRD静态默认值检查不模拟mutating webhook或API Server。没有Runtime PASS、HA/DR或生产声明。

CNPG1.30对Kubernetes1.37仅列为tested；本地16GiB VM共享预算需在执行前核验。
旧集群数据已获用户确认可丢弃；本轮代码验证未删除或修改旧集群。
