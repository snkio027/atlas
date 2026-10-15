# 平台 namespace 约定

本变更实现新安装的无产品前缀布局。当前 `atlas-app-r3` 仍使用原 namespace，
没有执行集群迁移。决策范围见 [ADR-0020](adr/0020-platform-namespace-names.md)。

| 原名称 | 新安装名称 | 内容与边界 |
| --- | --- | --- |
| atlas-gateway | gateway | Gateway、入口证书、Envoy 配置；代理 Pod 仍在 envoy-gateway-system |
| atlas-monitoring | monitoring | Prometheus、Alertmanager、Grafana、指标收集与看板 |
| atlas-secrets | sealed-secrets | Sealed Secrets controller 与独立 Trust Root |
| atlas-storage | object-storage | SeaweedFS / S3；不承担所有类型存储 |

`argocd`、`cert-manager`、`envoy-gateway-system`、`local-path-storage` 保留组件名。
`workload-web` 保留为平台示例；业务 namespace 由 Project 名决定，例如 `archive`。
命名不表示权限；AppProject、RBAC、NetworkPolicy、Pod Security 和 GitOps owner
共同约束实际行为。

本次只更改 namespace 及引用：AppProject destinations、RBAC subjects、策略选择器、
Gateway parentRefs、Service DNS、监测选择器、密文目标及 Go 工具。Helm release
`atlas-monitoring`、相关 Service/StatefulSet 名与 release 标签、StorageClass
`atlas-local-retain`、外部域名和端口保持原义。不要对所有 `atlas-` 字符串做全局替换。

## 新增数据库、数据处理组件

先确定组件的权限和数据生命周期，再选 namespace：

- 专用 controller 使用组件名，如未来数据库 Operator 的管理 namespace；其控制权
  与实际数据库实例分开审查。
- 同一运维边界的共享数据服务可使用 `databases` 或 `data-processing` 等职责名；
  只有权限、配额、网络和退役边界相同才共用，不能只按“都是数据库”合并。
- 项目独享的数据实例应结合 Project 的生命周期设计；当前业务模型仍只支持 WebService，
  本次命名修改不代表已经支持数据库 Binding 或有状态业务工作负载。
- CSI、本地卷 provisioner、对象存储和备份各自按职责管理，不能全部放进 object-storage。

这些名称是后续设计示例，本次没有预建空 namespace 或安装组件。
平台内置 namespace 是业务 Project 保留名；编译器也检查 deployment base 的
Namespace 和精确 AppProject destinations。因此新组件的目的 namespace 一旦进入
base 权限定义，即使尚未创建，也不能被新 Project 占用。

## 源模板与实例凭据

源模板不携带旧实例 SealedSecret。离线 render/check 与产品打包可在没有凭据时进行；
`plan` 会报告缺失凭据，`select` / Bootstrap activation 继续拒绝启用不完整的实例。
D1 在新实例 Trust Root 备份后生成密文，再完成就绪检查。离线渲染成功不是部署授权。

## 现有实例

namespace 变化会产生新的 Kubernetes identity，不能作为普通原地更新发布到 r3。
旧 SealedSecret 严格绑定旧 namespace/name；PVC 也不能靠改 namespace 指向原数据。
旧安装包、工作区与管理入口工具继续使用其绑定可执行文件；新工具拒绝旧/混合产品布局。

真正迁移需另行确定数据保留范围、备份恢复、新 namespace 的凭据封装、网络/DNS/入口
切换、业务验证和旧资源退役。当前改动不自动执行这些步骤，不改写历史验收结果，
也不删除旧集群。S1/OT-1 固定输入与 Bootstrap 历史快照保持原名称。

## 验证范围

回归覆盖 namespace 引用闭合、Helm release 名不变、业务保留名、未来 AppProject
目的 namespace 冲突、旧产品布局拒绝，以及当前/冻结 Gateway Pod 身份检查。
完整 `task quality` 包含真实 Helm/Kustomize 渲染、CRD 检查、race tests 和冻结实验契约。
这些都是本地验证；新 namespace 的真实集群运行结果需要单独记录。

2026-10-14：本地完整 `task quality` 通过，包括 345 个 GitOps 资源检查、真实
Helm/Kustomize、Go race tests、OT-1 固定 13 对象/29 阶段契约。变更清单中的
185 个对象已比对：除旧实例密文移除外，identity 的变化仅为 namespace；无集群操作。

ADR-0021 数据能力预留 `postgresql`、`redis`、`kafka`、`flink`、`logging`，均不带 atlas 前缀。
它们按选择创建，业务 Project 不得占用；Alloy 与 Loki 共处 logging。当前单所有者候选
将 CNPG/Strimzi/Flink controller 与各自单域实例共处对应 namespace，使用独立
ServiceAccount/Role 和 GitOps owner；不是 controller/数据实例之间的 namespace 隔离。
