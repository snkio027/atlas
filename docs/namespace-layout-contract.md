# Namespace Layout Contract — role-scoped-v1

状态：设计已采纳，当前实现用于新安装；本地验证见部署审查，Runtime 尚未验证。对应 [ADR-0022](adr/0022-role-scoped-namespace-layout.md)。
输入审计基线为 `78967ec7fe53a17f6787349e749844c4ff38f751`。下表是目标布局，
不是当前 atlas-data-r1 的资源清单；本轮没有访问或更改集群。

## 分类与身份

| 类别 | 名称 | 意义 |
| --- | --- | --- |
| 底座 | default、kube-system、kube-public、kube-node-lease、local-path-storage | 保留系统/底座身份；禁止普通业务使用 |
| 平台 | platform-<unit> | 一个明确的管理与运维单元 |
| 共享服务 | service-<service-id> | 一份由平台统一运维、供项目消费的实例 |
| 项目 | project-<project-id> | 一个业务项目的运行、预算与交付边界 |

项目逻辑 ID、Capability ID、Application 名与 Kubernetes namespace 是不同字段。
`Project.name=archive` → `namespace=project-archive`；Authored 文件仍是
`platform/projects/archive.yaml`。Binding 的逻辑身份继续绑定 Project/Workload，
其 Secret target 必须使用生成后的 namespace。不会把 `archive` 原地更新成 `project-archive`。

Project ID 仍采用现有长度/字符限制；保留 platform-smoke 以及当前内置身份，拒绝用户
通过 namespace override、raw YAML 或修改 owner 标签进入管理/共享服务区域。
集群本身是开发环境时，不为每个 namespace 重复添加 -dev。多个环境的模型另行设计。

## 精确布局及声明归属

下列“Namespace owner”是目标 Application 名；已存在的 owner 名尽量保留，
新 operator foundation 是明确的新叶子，不增加 Application 嵌套层级。

| 当前区域 | 目标 namespace | class / domain | Namespace owner | 主要资源及排除项 |
| --- | --- | --- | --- | --- |
| argocd | platform-argocd | platform / delivery | argocd-self | Argo CD 全套与内部 Redis；Bootstrap 创建后 GitOps 接管 |
| cert-manager | platform-cert-manager | platform / security | foundation | cert-manager 及 webhook |
| sealed-secrets | platform-sealed-secrets | platform / security | secrets-foundation | controller、Trust Root；业务 Secret 在消费区域 |
| envoy-gateway-system + gateway | platform-gateway | platform / networking | foundation | controller、Envoy、Gateway、入口证书/TLS；业务 Route 不搬入 |
| monitoring | platform-monitoring | platform / observability | observability-foundation | Prometheus Operator、Prometheus、Grafana、Alertmanager、采集器 |
| logging | platform-logging | platform / observability | logging-foundation | Loki、Alloy；独立于 privileged 监控区域 |
| postgresql 中 CNPG | platform-cnpg-operator | platform / database | postgresql-operator-foundation（新） | controller、webhook、配置与管理凭据 |
| postgresql 中实例 | service-postgresql | service / database | postgresql-foundation | Cluster、数据库 Pod/PVC、实例用户与TLS |
| kafka 中 Cluster Operator | platform-strimzi-operator | platform / streaming | kafka-operator-foundation（新） | Cluster Operator 与 leader election |
| kafka 中实例 | service-kafka | service / streaming | kafka-foundation | Kafka/KafkaNodePool、broker、Entity Operator、KafkaUser、CA/PVC |
| flink 中 Operator | platform-flink-operator | platform / processing | flink-operator-foundation（新） | controller、webhook、keystore password |
| flink 中 Session | service-flink-session | service / processing | flink-foundation | 现有共享验收 Session、JM/TM、checkpoint PVC；不是任意项目工作区 |
| redis | service-redis | service / cache | redis-foundation | 共享 Redis、ACL/TLS、PVC；不包含 Argo 内部 Redis |
| object-storage | service-object-storage | service / storage | storage-foundation | SeaweedFS/S3；不包含 provisioner 或全部数据库 |
| workload-web | project-platform-smoke | project / platform-validation | foundation | 平台维护的 Web smoke、PVC及消费凭据 |
| 新业务 Project | project-<id> | project / application | 该 Project 的现有生成 Application | 项目部署、服务、Route、消费 Secret；不自动承接数据 Operator |

namespace 数量从当前13个自定义区域变为15个：入口合并减少1个，三类 Operator 拆分增加3个。
系统/底座名称数量不变。项目新增时仅增加对应项目区域。

每个 Namespace 在完整生成 inventory 中只能出现一次；不能同时由 foundation 与 Helm
chart 声明。`platform-argocd` 的 Bootstrap create 与后续 argocd-self SSA 是顺序交接，
必须由 Receipt 前的 ownership 检查证明，不是两个持续写入者。
服务 CR 由 Argo 所有；Operator 生成的 Pod、Service、Secret 等继续由 Operator 所有，
不得通过把这些子对象写入 Git 来制造归属一致。

## Namespace 管理档案

编译模型至少保存以下内容。布局表是平台产品输入，不能由普通业务 intent 任意覆盖。

| 字段 | 语义及验证 |
| --- | --- |
| logical unit / namespace | 稳定管理单元与生成名称；二者唯一，拒绝重复/未知单元 |
| class / domain | 三类自定义前缀与用途；名称和class一致 |
| owner | 责任主体；平台区、共享服务和smoke为platform |
| declarative owner | 唯一Namespace Application；同时绑定baseline policy/预算 |
| writer | AppProject、实际controller SA与精确目标namespace的关系 |
| security | Pod Security级别/版本、默认拒绝、DNS与必要双向网络许可 |
| budget | ResourceQuota/LimitRange和归属；拆分需守恒检查 |
| lifecycle | controller、shared-data或project；数据保留/凭据/退役限制 |

分类标签：`atlas.local/class`、`atlas.local/domain`、`atlas.local/owner`；项目另外有
`atlas.local/project=<logical-id>`。自由文本 Project.owner 不能直接塞进 label：
先验证其符合label value约束，原有自由文本责任信息保存在annotation中；不得静默截断。
`atlas.io/project` 等既有授权标签仍绑定逻辑ID，不能误换成project-<id>。

## 预算与安全配置

当前 PostgreSQL/Kafka/Flink 的每个组合区域都有同样的预算。第一版按以下方式分配，
每个领域分别守恒；不得乘以两个namespace复制旧预算。

| 配额 | 原组合区域 | 新Operator区域 | 新service区域 |
| --- | --- | --- | --- |
| requests.cpu | 3 | 500m | 2500m |
| requests.memory | 6Gi | 1Gi | 5Gi |
| limits.memory | 8Gi | 1Gi | 7Gi |
| requests.storage | 32Gi | 0 | 32Gi |
| persistentvolumeclaims | 8 | 0 | 8 |
| pods | 16 | 4 | 12 |

这些是quota上限，不是宿主机预留量，也不证明所有最大值可同时运行。
本地编译须用锁定Chart实际Pod/Job的requests与滚动峰值验证可行性；不足时重新审查分配，
不能在运行中提高总额。Operator不持有PVC，Trust Root备份也不是Operator PVC。
锁定Chart的离线渲染给出的当前峰值如下：CNPG和Strimzi按单副本更新时旧/新各一Pod
计，Flink按Chart声明的Recreate计算。这只验证Operator侧的计划可容纳性，service侧
及实际调度仍需在完整编译/运行Gate核验。

| 管理单元 | 更新模型 | requests.cpu峰值 | requests.memory峰值 | limits.memory峰值 |
| --- | --- | --- | --- | --- |
| CNPG | 旧/新各一Pod | 200m | 256Mi | 512Mi |
| Strimzi | 旧/新各一Pod | 200m | 512Mi | 1Gi |
| Flink Operator + webhook | Recreate，单Pod两容器 | 200m | 384Mi | 768Mi |

原LimitRange按角色继承；service侧保留数据资源约束。Redis、S3、监控、日志、smoke
保留原预算数值。核心Argo/证书/Gateway目前缺少完整quota的事实必须在预算报告中列为
显式现状，不能将“改名”写成“全平台已有预算治理”；新增预算是独立可审查增量。

platform-monitoring保持现有privileged例外，platform-logging保持restricted。
新拆分的数据管理区和service区域均restricted，继续保留非root、seccomp与有限资源。
Gateway合并不是扩大平台入口权限：Route接入、listener/TLS引用、代理Pod selector仍精确。
分类标签不替代NetworkPolicy；不因为prefix相同默认互通。

## 三类 Operator 的实际适配

当前锁定：CNPG Chart0.29.1/Operator1.30.1、Strimzi1.3.0、Flink Operator1.16.0。
核验使用仓库锁摘要与vendored模板，不以latest文档代替当前行为。

| Operator | CR监听集合 | 实现中必须处理的事项 |
| --- | --- | --- |
| CNPG | service-postgresql | 单域Chart将Deployment WATCH_NAMESPACE及Role/RoleBinding写成安装ns；须显式改目标监听、目标Role与管理域有限Role。保留cluster-scoped webhook管理的原限制，更新webhook namespaceSelector与CA/Service引用；验证default monitoring ConfigMap传播 |
| Strimzi | service-kafka | Chart的strimzi.watchNamespacesList自动追加安装ns；不能误认为values只列service就已限定监听。需要固定版本投影收窄env/运行域RoleBinding，同时保留管理域leader election；Kafka Entity Operator留在service |
| Flink | service-flink-session | watchNamespaces已生成目标Role/RoleBinding及webhook selector；校验跨namespace SA、webhook证书/密码引用、Operator→JM8081与JM/TM侧对应许可 |

三个安装域都不能因watch变化得到`*`或全部project-*权限。CNPG/Strimzi仍有必要的集群级
读权限和CRD/webhook能力；分namespace不等于没有集群级权限。
Operator生成NetworkPolicy与Atlas默认拒绝要组合验证，不能只检查一侧。
未来项目专属Flink需先增加明确Project能力与目标列表，不通过namespace前缀自动发现。

离线设计探针已实际渲染三个锁定Chart，复现CNPG/Strimzi上述行为，并确认Flink
目标RoleBinding的subject来自安装域。原始结果在
`.state/latest/namespace-layout/chart-boundaries.json`，不是集群运行证据。

上游说明：[CNPG 1.30安全模型](https://cloudnative-pg.io/docs/1.30/security/)、
[Flink 1.16部署模型](https://nightlies.apache.org/flink/flink-kubernetes-operator-docs-release-1.16/docs/deployment/overview/)。
具体投影必须继续以仓库锁定模板为准。

## 编译与工具链改动面

| 层 | 必须闭合的引用 |
| --- | --- |
| Bootstrap/D1 | platform-argocd、Root/AppProject/Signal、Seed对象、Secret创建、namespace接管、Receipt；旧产品拒绝与新布局身份绑定 |
| GitOps图 | Application metadata.namespace与destination、唯一foundation owner、各阶段先权限后消费者；canonical AppProject/owner名不改 |
| Helm投影 | release名保持；安装ns、RBAC subject、webhook service/CA引用、controller flags、watch集合及监测选择器 |
| 服务与网络 | service.<namespace>.svc地址、TLS SAN、Gateway parentRef、ReferenceGrant、NetworkPolicy双向peer；外部hostname/端口保持 |
| 凭据 | D1三份密文新target；数据凭据中的Flink密码进入Operator域，PG/Redis用户进入service域；strict密文不可改metadata复用 |
| 业务编译器 | logical Project ID → namespace；quota/SA/policy/Secret/route/metrics、权限与S3反向许可统一使用生成ns |
| Observation/CLI | Argo Application Ref、tracking namespace、对象读取、logs/status/open、默认context与错误信息；不能仍查询旧argocd |

`internal/platformnames/layout.yaml` 集中声明当前布局；平台策略 YAML 与生成 inventory 补充 writer、网络和预算。
其中 `internal/atlas/{apply,state,ownership,installation}.go`、
`internal/installation/{install,notification,product}.go`、
`internal/observation/{collect,classify,model}.go`、
`internal/workload/{compiler,adapters}.go` 与 managementaccess 必须一起审计。
Project.Name 是逻辑 ID；Namespace() 单独派生运行 namespace，不能全局把 Project ID 加前缀。
这些路径已随本次实现一并调整；原冻结实验按自己的输入快照验证，不构成旧产品安装兼容。

## 完成条件与执行边界

源码交付作为一个完整布局变更：ADR与管理档案 → 语义模型/编译引用 → 新安装包/观察工具
→ 定向回归和完整task quality。不得交付部分新namespace、部分旧硬编码的混合产品。

本地Gate至少验证：

1. 当前完整profile每个Namespace恰有一个声明owner，资源引用闭合；系统名保持。
2. Project namespace派生确定、保留项目拒绝；AppProject不能把业务送入platform/service区。
3. Operator部署位置、监听env/config、RoleBinding、webhook、网络和TLS SAN精确一致；未知域拒绝。
4. 三组拆分预算逐项守恒并覆盖实际启动/滚动峰值；monitoring/logging PSA边界保留。
5. 相同输入生成逐字节相同输出；没有明文Secret、旧实例密文或旧/新混合namespace。
6. 旧包明确拒绝；冻结OT-1/schema1–3与历史证据未改写，现有scopegen/default-stability等Gate不降级。

随后才为一个独立新目标生成精确REVIEW：新集群、D1/Tier-0、独立Trust Root/备份、
新密文目标与Git父提交、有限功能测试都明确绑定，获批准后执行。
真实Gate须证明新namespace的Bootstrap handoff、三个Operator跨域管理、服务TLS/认证/
持久化、Flink作业、日志/指标、项目Web→S3正负例与重复操作。失败停止，不能改旧证据宣告PASS。

现有atlas-data-r1不在本次写入范围；readiness-repair继续待审。后续若选择迁移，必须另列
数据/PVC恢复、CA与凭据重封装、DNS客户端切换和旧资源退役。新安装通过不会自动授权删除旧实例。
