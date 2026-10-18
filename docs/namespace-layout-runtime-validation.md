# Namespace 与数据平台组合候选：运行验收 PASS

2026-10-19 00:23:48 +08:00。适用范围：单所有者、本机 OrbStack 四节点开发集群。
这是 PR #11 数据能力与 PR #12 YAML/namespace 布局的组合验收；经过已审查修正与续行完成，
不是未经中断的单次安装，也不是 PR #11 原共 namespace 布局的独立 Runtime PASS。

## 实现与证据绑定

| 记录 | 固定身份 |
| --- | --- |
| 首次安装候选 | `a2cbeee68cc6b1ba65c7da71de64e8dcc23cfcb8` |
| 最后部署修正实现 | `35f829d3af96d6fabde8ef514eebbb4d9315f493` |
| 版本化验收源码 | `580c8d45e2ab56fdf822c83c7d9dca01e417facd` |
| 最终部署 Git | `027a619a83d4ce8a993fbd167059ba1e2bd90590` |
| 原总计划 SHA256 | `496556d630bcbc2666b13ecdf074bbd4f73696cdc931b3f8361db2e846d0484e` |
| 最终收尾计划 SHA256 | `b6458af39d9dbcff88ef0756c8ceca658008bbe1954f03144392b9ce1a6ce391` |
| 最终 PASS 记录 SHA256 | `24efa47084cc4ebacfbbe04588b26ea081c9203ff1abae77a1d9cc6c7995b07c` |
| 收尾证据索引 SHA256 | `7d485a89ac95c3f7ebddf2fc10cb5ae19e1c3847716aeb334cc9c60404ed8994` |

[验收源码 exact-head Quality](https://github.com/snkio027/atlas-refactor/actions/runs/39140518008)
为 SUCCESS。完整本地 `task quality` 已通过；19 项版本化验收测试和 21 项 CNPG 安装域权限缺失回归
属于源码检查，不冒充集群测试。当前提交若仅更新文档，其 CI 与上述历史 Runtime 绑定分别记录。

## 已证明

| 检查 | 实际结果 |
| --- | --- |
| Bootstrap / GitOps | D1 完成有限交接；后续数据扩展未恢复 Seed authority，Identity/Latch/Receipt、Root 与 D1 原持久资源 UID/owner 保持 |
| 布局与持续控制 | `platform-* / service-* / project-*` 布局，535 项持久资源定义、tracking/Argo SSA 与就绪核验；49 个 Application Synced/Healthy，四节点 Ready |
| Operator 权限 | 安装域与实例域分离，CNPG 当前进程控制器启动证据、159 项有效 RBAC 正反检查；不是只凭 Pod Ready 推断权限完整 |
| PostgreSQL | TLS/SCRAM、SQL、普通数据库身份与授权负例 |
| Redis | TLS/ACL/AOF、Pod 有界替换后的数据持久性与清理 |
| Kafka | mTLS/ACL、主题读写、无证书与越权负例、broker 有界替换后的原消息/PVC 保持与清理 |
| Flink | 固定示例作业、checkpoint 元数据、cancel 与测试数据清理 |
| 日志/监测 | Loki/Alloy/Grafana 授权查询、日志标记、声明的 scrape、告警规则及日志采集权限负例 |
| S3 隔离 | 无凭据正反 Pod 仅授权标签不同；正例前后均到达 HTTP，负例连接超时；显式 Secret 授权拒绝；两个 Pod 按精确 UID/RV 清理 |
| D1 不回归 | CA 验证的 Web HTTPS/跳转、S3 签名只读与匿名拒绝、原凭据身份及内容保持 |
| 私有备份 | 11 份运行时 CA/TLS/user Secret 备份与读回核验；隔离等级明确为同机开发例外 |
| 重复与收尾 | 两次离线编译字节一致；最终仅比较已发布 Git 内容，无新 commit/push/凭据生成/功能探针重放；无残留验收 Pod；默认上下文核验 |

历史功能结果逐项引用原执行记录；最终收尾没有重跑 PostgreSQL、Redis、Kafka、Flink、日志或 S3 探针。
重复发布结果是已发布内容的只读比较，不能表述成再次运行发布器证明零写入。

## 固化的失败经验

- Operator 的安装域 API 需求独立于 CR watch namespace；检查完整已审查权限集合和所有累加绑定。
- API 默认/省略语义需要有限、可解释的比较规则；保留完整 spec、owner 与 tracking 检查。
- 验收应执行锁定工具的真实 argv，并分别解释 exit/stdout/stderr；Kafka 已使用现代参数与版本绑定判定。
- `web-smoke` 没有 S3 客户端标签，不能被当成网络正例。配对探针在任何副作用前核验两侧策略与端点。
- 真实验收入口进入仓库/CI，持久化 create-only intent，未知写入结果拒绝重放。
- 冻结源码和运行入口必须位于持久目录；临时目录只承载可再生成缓存。一次临时源码丢失触发 STOP，
  同提交恢复、摘要一致性核验后，经审查完成余下收尾；没有修复临时目录后冒充原 attempt 连续成功。

详细根因与回归见 [Failure Journal](data-platform-failures.md)、
[版本化验收契约](../experiments/data-platform/README.md)和 [Namespace Contract](namespace-layout-contract.md)。

## 证据与适用限制

此公开摘要只含源码/计划/记录摘要、通过项和限制；不含凭据、私钥、kubeconfig、主机路径、集群 UID、
原始日志或 Secret 数据。原始 bundle 与逐项 STOP/intent/receipt 留在所有者私有证据中，未公开上传。
索引本身不公开，所以第三方只能审阅本摘要与源码，不能据此独立重算全部现场证据。

Metadata audit 不覆盖全部 GET/streaming。覆盖内零管理写入、功能执行记录与源码防重放约束是不同证据，
不能从缺少 exec 审计事件推导没有功能执行。未证明生产就绪、HA、DR、多租户隔离、数据服务业务 Binding、
通用恢复或凭据轮换。CNPG/Kubernetes 支持边界、Flink 本地 RWO 和 API 网络端口规则限制仍按 ADR 保留。

下一次从合并主线创建新实例属于新的完整验证，不改写本记录或旧 STOP，不复用旧实例的身份、凭据或密文。
