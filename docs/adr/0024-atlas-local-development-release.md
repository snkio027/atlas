# ADR-0024：Atlas 正式项目身份与本地开发稳定版

Status: Accepted（2026-10-24）。所有者已批准项目更名、同集群常驻访问计划，以及完成
最终审查后合并并发布首个稳定版本。采用范围限于本文的单所有者本地开发模型；
不包含现有实例升级、生产 cutover 或新的集群操作。

## 项目身份

Go 项目正式使用 `snkio027/atlas`，Go module 为 `github.com/snkio027/atlas`。
旧 Shell 仓库改名 `snkio027/atlas-legacy` 并归档；本地旧目录保留全部未提交修改。
仓库 ID、提交、标签、PR、历史 STOP 与证据不重写。禁止重新占用 `atlas-refactor` 仓库名，
现有实例冻结的 Git URL 通过 GitHub 更名重定向继续读取同一仓库。新产品使用 canonical URL。
旧实例的 Root、Seed 名称、release 标识与安装记录是资源身份，不因项目更名强行重建。
这不构成原生产环境 cutover 或自动迁移授权。

## v0.1.0 的支持范围

首个非 prerelease 版本表示单所有者 macOS arm64 + OrbStack 本地开发基线稳定，
不表示生产、HA、独立 DR、多租户、自动恢复或任意旧版本升级已受支持。
保留有限 Bootstrap、Git 定义、Argo 调谐、Operator 管理运行实例的边界。
发行包绑定干净提交、固定工具链与依赖，发布 checksum 和 GitHub build attestation；
先生成 draft，完成对应检查后提升同一个不可变制品，不重新构建后冒充已验收制品。
当前安装器负责 D1；数据能力及原生入口是明确的 post-install 平台变更，不混入 Tier-0。

## 普通使用入口

浏览器复用现有 Gateway、HTTPS、证书管理及有限 HTTPRoute。Grafana/Argo/S3 保留自身认证；
Prometheus/Alertmanager 仅 GET/HEAD。日志通过 Grafana Explore 的 Loki datasource 查询。
Flink 浏览器界面仅提供只读查询，作业管理继续使用 Kubernetes 授权的集群内 CLI。

PostgreSQL/Redis/Kafka 的 Mac 原生客户端使用 OrbStack 原生容器网络及固定 NodePort，
不新增端口转发进程、不重建 Kind、不把数据库协议冒充 HTTPRoute。
TLS 及 PostgreSQL SCRAM、Redis ACL、Kafka mTLS/ACL 保留；Kafka 必须显式声明可达的
bootstrap 与 broker advertised address。Operator 生成的 Service 由 Operator 管理。
Kafka 使用额外的内部 TLS listener，加上由现有 kafka Application 独立拥有的两个
NodePort Service；固定 advertisedHost/Port，同时将 advertisedHost 纳入服务器证书 SAN。
不使用 Strimzi 的 `type: nodeport`：锁定的 1.3.0 即使已有固定地址，也会调谐
kafka-init ClusterRoleBinding 并启用节点地址发现。这超出当前 Operator 的权限边界。
不能只补一个 GET 再逐项扩大权限，也不能让 Argo 与 Operator 共同维护同一个绑定。
本方案保留原 9093 listener、CA、用户、ACL、Operator RBAC 与其生成 Service 的归属；
只支持已核验的单节点池 single、broker 0、无 rack discovery 拓扑。
原生入口 opt-in；仅限已审核单数据节点拓扑。网络权限由实际源地址和精确服务端口定义，
不能因开启 NodePort 取消 namespace 默认拒绝或开放所有业务 Pod。
Inventory 的 IP policy 禁止规则只对 `postgresql`/`redis` 两个已有 owner 在各自
固定 source path、固定 namespace/name、完整精确 ingress spec 上增加外部 /32 例外；
项目 policy、egress、任意 namespace/selector/port 仍拒绝。Kafka 同一 /32 通过锁定
Strimzi listener 的 networkPolicyPeers 声明，由 Operator 生成策略。

发布计划必须绑定当前目标、Git parent、端口、真实客户端源地址、证书及对象差异。
任何为了确定网络源地址而使用的临时对象也应列入该计划，之后精确清理。
凭据只写入本机用户私有目录，不写入 Git、URL、进程参数或聊天；不将平台管理身份当成业务 Binding。
不更改宿主机 LAN 设置来静默扩大或缩小其他项目的网络范围。

## 验证与发布顺序

本地回归、真实渲染/CRD 默认值检查和完整质量检查 → PR CI → 精确同集群部署审查 →
实际 TLS/认证/负例/地址及旧业务不回归验证 → 使用指南核对 → 稳定制品与来源验证 → 发布。
历史 r2 PASS 仍绑定其原实现；新入口不能借历史结果声称已经部署或验证。

实际访问验收见 [r2 常驻访问报告](../local-access-validation.md)：`931431c` 同集群增量 PASS，
旧 STOP 保留。新标签的制品来源校验与该运行记录分别保存；发布不能改写历史实测身份。

锁定上游依据：[内部 listener 的 advertised 地址、端口及 SAN 处理](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/operator/assembly/KafkaListenersReconciler.java)，
[NodePort/rack 触发 ClusterRoleBinding](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/model/KafkaCluster.java)。
