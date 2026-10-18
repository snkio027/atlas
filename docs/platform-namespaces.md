# 平台 namespace 与归属

新安装统一使用 [ADR-0022](adr/0022-role-scoped-namespace-layout.md) 的
`platform-* / service-* / project-*` 布局。完整映射与边界见
[布局契约](namespace-layout-contract.md)。新产品只接收当前布局，不提供旧命名双轨运行。

| 类别 | 当前单元 |
| --- | --- |
| 系统 | default、kube-node-lease、kube-public、kube-system、local-path-storage |
| 平台管理 | platform-argocd、platform-cert-manager、platform-sealed-secrets、platform-gateway |
| 运维 | platform-monitoring、platform-logging |
| 领域 Operator | platform-cnpg-operator、platform-strimzi-operator、platform-flink-operator |
| 共享实例 | service-postgresql、service-redis、service-kafka、service-flink-session、service-object-storage |
| 项目 | project-platform-smoke；业务 Project archive 生成 project-archive |

控制器与实例有独立 quota、LimitRange、NetworkPolicy 和 GitOps owner。
拆分的 Operator/实例预算之和保持原领域预算。监控 privileged 与日志 restricted
边界保留；Kafka Entity Operator 属于 service-kafka，Argo 内部 Redis 属于 platform-argocd。

版本化定义位于 `internal/platformnames/layout.yaml`，新产品记录布局版本与摘要。
Namespace 的 class/domain/owner 标签用于分类；权限由 AppProject、有效 RBAC 和网络策略
实施。Namespace 只有一个声明 owner，禁止自动 prune/delete；退役需要独立决定。
Bootstrap 创建的 platform-argocd 纳入 argocd-self 的完整 Seed adoption 核验。

```bash
kubectl get ns -L atlas.local/class,atlas.local/domain,atlas.local/owner --sort-by=.metadata.name
kubectl get ns -l atlas.local/class=service
```

人工配置使用 YAML，锁、产品、计划、证据和生成清单继续确定性 JSON，见
[ADR-0023](adr/0023-authored-yaml.md)。普通业务不再直接用 Project.name 作为 namespace。
Project owner 的原文本保存于 annotation；超过标签语法的文本生成稳定 owner 摘要标签，
不会截断原文。外部 hostname/port、release 名及逻辑组件 ID 不改。

现有 atlas-data-r1、STOP、凭据和部署分支没有修改。新布局不是原地改名方案；
新工具拒绝旧产品布局。旧运行记录仅用于追溯，不能当成新实现的 Runtime PASS。
本轮仅实现和本地验证；部署、Trust Root、公开密文及旧资源退役仍须单独审查。
