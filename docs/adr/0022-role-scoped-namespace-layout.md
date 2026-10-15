# ADR-0022：按平台管理、共享服务与项目划分 namespace

Status: Design Accepted — 2026-10-16。用户批准按复审后的方案实施；Runtime 尚未验证。
本记录替代 ADR-0020 的新安装命名决定，
并替代 ADR-0021 中三个领域 Operator 与实例共 namespace 的决定。

## 问题与决定

无前缀组件名混合了安装单元、控制器和服务实例三个维度。当前 `postgresql`、`kafka`、
`flink` 又同时承载领域 Operator 与其运行实例，单靠 owner 标签无法解释生命周期与权限。

新安装采用三个自定义类别：`platform-<unit>`、`service-<service-id>`、
`project-<project-id>`。Kubernetes 的四个初始 namespace 与 `local-path-storage`
保留原名；Cilium 留在 kube-system。业务不得落入 default。
前缀只是分类，不创建父子 namespace，不提供授权或多租户隔离。

精确映射、声明所有者、预算、引用与验收要求见
[Namespace Layout Contract](../namespace-layout-contract.md)。该契约是本次设计的一部分。

- Argo CD、证书、Sealed Secrets、Gateway、监控、日志和三个领域 Operator 各有独立的
  platform 管理单元；没有一个容纳全部控制器的大 namespace。
- CNPG、Strimzi Cluster Operator、Flink Operator 与运行实例分离。Kafka Entity
  Operator 属于 Kafka 实例；Argo CD 内部 Redis 属于 Argo CD；Prometheus Operator
  属于监控管理单元，均不按技术名称机械搬走。
- 共享 Gateway 及 Envoy controller/data plane 归入 platform-gateway。仍采用锁定版本
  的标准部署模式；业务 HTTPRoute 留在项目 namespace，allowedRoutes 按精确项目授权。
- platform-monitoring 与 platform-logging 分开，保留 privileged / restricted 的策略边界。
- 当前 Flink Session 定义为单所有者开发平台的共享验收实例 service-flink-session。
  本次不让 Operator 自动监听 project-*；项目专属 Flink 的交付与授权仍是后续能力。
- 平台 Web smoke 成为 project-platform-smoke，是受平台维护的验收项目。
  普通 Project 的逻辑 ID 保持 `archive` 等名称，运行 namespace 由编译器生成
  `project-archive`。用户不能自由指定 namespace 或用前缀绕过保留名检查。

## 所有权与权限

Namespace 及其 quota/limits/baseline policy 有唯一 GitOps 声明所有者。
`atlas.local/owner` 表达责任主体；Application tracking 标识 GitOps 声明管理归属，
接管仍按资源类型核验完整证据；ownerReferences 表达对象依赖与垃圾回收关系。
Operator 的调谐范围与操作权限分别由配置和有效 RBAC 决定。以上四者分别验证，
不得互相替代；禁止跨 namespace 的 namespaced ownerReferences。

首次 Bootstrap 可创建 platform-argocd，交接后该 Namespace 必须由固定的 argocd-self
定义接管，新增对象纳入 Seed ownership inventory。不得依靠 Healthy 推导 authority transfer。
这个增加 Namespace 接管的行为需要新的首次安装证据，不能借用旧 D1/r7 PASS。

Operator 的安装位置、CR 监听集合、namespaced RBAC 和必要 cluster-scoped RBAC 分别
核验。监听集合不得为通配符；namespaced 资源写权限仅绑定审查的 service namespace。
自身管理 namespace 的 leader election / webhook / 配置权限单独保留，不能复制全套
实例管理权限过去。项目的 AppProject 永远不能部署到 platform-* 或 service-*。
platform-project 仍是受信任的平台权限边界；本次不声称它已变成多个相互隔离的管理主体。

已存在的授权标签（如 atlas.io/gateway-access、atlas.io/project）保持原语义。
新增 class/domain/owner/project 仅作为分类资料，除非单独设计，否则不得成为授权条件。
拆分预算按原总额分配，不能把原 quota 原样复制两份。

## 实现约束

实现时以一个版本化的 namespace 布局定义管理 logical unit → name/class/domain/
declarative owner/security/lifecycle；相应声明策略和编译 inventory 绑定 writer 与预算。名称、服务地址、watch 集合、RBAC subjects、
AppProject destinations 和网络引用由语义关系派生；禁止跨仓库文本替换组件名。
Helm release 标识、canonical AppProject、Capability ID、显式声明且与 namespace 无关
的资源名及外部域名/端口不主动改名。内部 Service DNS 与相关配置由新布局派生；
Chart/controller 派生名称以锁定版本渲染和运行证据核验。
投影验证输入结构、精确匹配与输出不变量；未知版本、漂移或部分投影均失败关闭。

新安装产品必须绑定布局版本并拒绝旧/混合布局。冻结 schema 1–3、OT-1 fixtures、历史
snapshots、源提交与验收记录不重写。Schema-4 的新安装通过验证过的产品布局取得 Argo
namespace；不能让普通 CLI 任意指定管理 namespace，也不能让缺省值静默解释旧包。
Observation 的 Application 所在 namespace 必须来自已验证契约，不能只改变写路径。

锁定 Chart 的离线核验已发现：CNPG 单域模式把 WATCH_NAMESPACE 与 namespaced
RoleBinding 绑定安装 namespace；Strimzi 把安装 namespace 自动加入 watch 集合。
因此两个组件需要受版本限制的显式投影与回归，而不只是改 values。
Flink 的 watchNamespaces 会生成跨 namespace RoleBinding，但仍需真实运行证明。

## 交付与现有实例

第一交付是新的布局契约、完整编译/静态检查及一个独立新安装的运行验证；源码设计、
本地验证与真实 Runtime PASS 分开记录。第二交付才是具体旧实例的数据/凭据/客户端迁移
与退役计划。本记录不授权创建目标集群、使用新 Trust Root、发布新密文或删除旧 namespace。

当前 atlas-data-r1 与所有 STOP、备份及 Git 历史保留。78967ec 对应 readiness-repair
仍处于审查状态，用户已明确暂不执行；该计划不能混入新 namespace 布局。
现有私有脚本、二进制和计划继续绑定原名称。新布局完成前不向该部署分支推送改名清单。

此决定不增加通用迁移器、namespace 通配授权、项目数据库 Binding、Flink workload DSL、
凭据轮换、自动恢复或生产就绪声明。
