# 主线 namespace / 数据平台：r2 运行验收 PASS

2026-10-24 11:26:40 +08:00。适用范围：单所有者、本机四节点开发集群。
`atlas-layout-r2` 从合并后的主线完成安装，并在同一实例经明确批准的有限续行后完成剩余验收。
这是完整检查项的最终 PASS，**不是未经中断的单次 clean run**。此前 STOP、intent、receipt 和备份均保留。
[r1 组合候选报告](namespace-layout-runtime-validation.md)继续绑定其原提交与历史结果。

## 实现与证据绑定

| 记录 | 固定身份 |
| --- | --- |
| 部署 / Go 实现 | `7f37f4fd19d528f3704f1d971267065c65487149` |
| Kafka CLI 证据判定修复 | `2308a1b464eff1163a3d8fe03bb280a7501ccb36` |
| 最终部署 Git | `e05d7dfd672b67575ddb967e6496d083131f6ebe` |
| 原总计划 SHA256 | `00ef9c4d5352722958d7e0fa70f524f001097ab728382c200b290189a78972d4` |
| 独立 Kafka 持久性测试计划 SHA256 | `4c8cc20321048ec41dcd7d82f16bb9f746de4e85d4cf71a88e1f4cee3769cd4d` |
| 最终清理与收尾计划 SHA256 | `dddf90694160857503d52093aa1bd1ee1ff358d7a4916775660b41118098895c` |
| 最终 PASS 回执 SHA256 | `f4ab85c7a856066365fe36d68723842e66c8b38b3be6920e0cf12d122db7dd34` |
| 原始与追加执行索引 SHA256（2197 份文件） | `73f749e48417be86c05da300dba2b57c9fbbf62a5baf79334d078bbca1e671b7` |
| 当前收尾索引 SHA256（51 份文件） | `1195930b0fec2e290adb35491ef1d80d883a20cd8de05dcab05afdf7805a32bf` |

[Quality 39157913244](https://github.com/snkio027/atlas-refactor/actions/runs/39157913244)
绑定 `2308a1b`，结果为 SUCCESS；该修复的完整本地 `task quality` 也已通过。
收尾私有编排另有 47 项本地回归通过，**不属于上述产品提交，也不宣称运行过仓库远端 CI**。
文档提交的 CI 与这里的历史 Runtime 绑定分开记录，不将新提交冒充现场执行实现。

## 已证明

| 检查 | 实际结果 |
| --- | --- |
| D1 / Bootstrap | 完成有限 Bootstrap 到持久 GitOps 的交接；后续扩展未恢复 Seed authority，Identity/Latch/Receipt、External Root 和原基线资源身份保持 |
| 最终 GitOps Gate | 535 项持久资源的内容、owner、tracking 与 Argo SSA 符合预期；49 个 Application Synced/Healthy，四节点 Ready |
| 权限与治理 | 原 159 项 RBAC 正反验收保留；最终精确资源 Gate 核对当前定义；收尾实际执行的 Alloy / Secret 授权拒绝检查通过 |
| PostgreSQL / Redis | 原已完成的 PostgreSQL TLS/认证/SQL/授权负例，以及 Redis TLS/ACL/AOF、Pod 替换后持久性与清理证据保持；收尾没有重跑这些操作 |
| Kafka | 协议、mTLS 与授权负例已证明；新的独立测试消息在一次受保护的优雅 broker 替换前后完全一致，PVC/PV 与 clusterId 保持；两个测试 topic、临时客户端与策略清理完成 |
| Flink | 固定示例作业运行、checkpoint 元数据、取消与限定路径清理通过 |
| 日志与监测 | Grafana 认证访问 Loki、Alloy 日志标记与 RBAC 负例、Prometheus targets 和告警规则检查通过 |
| S3 隔离 | 无凭据配对探针证明授权正例 HTTP 可达、未绑定负例网络拒绝及 Secret 读取拒绝；探针已清理 |
| D1 不回归 | authority、installation 记录与凭据身份保持；CA 验证的 HTTPS / redirect、S3 签名只读与匿名拒绝通过 |
| 私有备份 | 原范围内 11 份运行时 CA/TLS/user Secret 备份与读回通过，采用本实例明确批准的同机开发例外 |
| 确定性 | 两次离线编译的 674 个文件逐字节一致 |
| 重复发布核验 | 对已发布 Git 树作只读精确比较；没有 push、新 commit、凭据生成或功能重放；这不是再次运行发布器的测试 |
| 收尾 | 验收临时 Pod 全部清理；默认上下文核对为本实例；执行器退出 0，最终回执为 PASS，本次收尾无 terminal STOP |

535 项核验包含持久资源身份与 ownership 检查，不能简化成只看 Pod 或 Application 健康。
功能结果按各自执行记录引用；已完成的 PostgreSQL、Redis、Kafka 生产/消费及 broker 替换没有在最终收尾重复执行。
674 文件映射摘要为 `e1da609567eac0a3116d4835811ee6e757e53e54c7e98a92423fa4d6fd475d8e`。

## 失败、修复与续行边界

1. Kafka 正确拒绝了越权事务请求，但旧验证器把输出误认成 stdout，未包含非 TTY `kubectl exec` 的远端退出诊断。
   `2308a1b` 以脱敏真实捕获修复 exit/stdout/stderr 的精确契约，保留原 STOP；没有修改 Kafka 权限或重新解释未知错误。
2. 等待期间，旧消息超过既定 24 小时保留期。再次核验在 broker 替换前停止，没有补写旧消息，也没有宣称跨五天持久性通过。
   经独立授权，以新 topic 和一条新消息连续完成替换前读取、一次优雅替换、替换后读取。
3. 持久性已通过，清理前精确 Git 只读查询遭遇连接重置，尚未开始 topic 删除便 STOP。
   最终计划只为明确的只读传输错误增加最多三次有界重试，并使用独立短时清理客户端完成剩余工作。
   写请求和语义不匹配没有获得重试许可；生产、消费及 broker 替换没有重放。

后两项属于经审查的本次私有执行编排，不是新增产品恢复接口。
全部历史 STOP 保持原结果；最终回执明确 `singleAttempt=false`，不清锁拼接单次成功。
版本化回归与简短经验见 [Failure Journal](data-platform-failures.md)及
[验收契约](../experiments/data-platform/README.md)。

## 证据公开范围与限制

公开内容仅包含提交、计划及证据摘要、通过项和限制。不公开明文凭据、私钥、kubeconfig、
Secret 数据、原始日志、主机路径、集群 UID 或带私有路径的索引。
上述两个索引的条目已逐一重算并匹配；索引与原始 bundle 仍保存在所有者私有证据中。
第三方可审阅本摘要和仓库源码，不能仅凭公开摘要独立重算现场证据。

Metadata audit 不覆盖全部 GET/streaming；覆盖内零管理写入、功能执行记录和源码防重放约束是不同证据。
缺少 exec 审计事件不能证明功能操作没有发生。同机备份不等于物理隔离或灾难恢复证明。
本次不证明生产就绪、HA、独立 DR、多租户隔离、通用恢复、凭据轮换或数据服务业务 Binding。
既有 CNPG/Kubernetes 支持范围、Flink 本地 RWO 与网络端口规则限制不变。

PR #13 收口只修复验收判定并记录结果；合并不部署、不替换实例实现、不重建集群，也不改变 ADR 的决策状态。
