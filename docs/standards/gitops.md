# Atlas GitOps Control Plane Design

**Version:** v1.0.4
**Status:** Frozen Production Baseline (Bootstrap-Critical CNI Adoption Amendment)
**Domain:** GitOps Control Plane / Platform Engineering

---

## 0. 文档职责与规范语义

本文档是：

> **Atlas Architecture Design v1.0.3**

在 GitOps / Argo CD 领域中的规范性执行设计。

本文档负责定义：

- External Root Anchor；
- AppProject Trust Chain；
- Root Macro DAG；
- Platform Capability DAG；
- Workload Control Model；
- Sync Wave；
- Application Health Gate；
- Environment Hydration；
- Child Automated Sync；
- Deletion Protection；
- Bootstrap Adoption；
- Break-Glass Recovery。

本文不得改变 Architecture Design 中定义的平台级不变量。

---

## 1. GitOps Ownership & Trust Boundary

Atlas GitOps 控制面采用四阶控制权模型。

```text
Git
 │ Definition
 ▼
Bootstrap
 │ Instantiation
 ▼
Argo CD
 │ Desired-State Reconciliation
 ▼
Kubernetes Controllers / Operators
   Runtime Behavior
```

---

### 1.1 Git owns Definition

Git 保存：

- Root Anchor Template；
- Application Graph；
- AppProject；
- Platform Desired State；
- Workload Desired State；
- Policy；
- Overlay；
- Version Lock。

Git 是 Desired State Definition Authority。

---

### 1.2 Bootstrap owns Instantiation

Bootstrap 负责：

1. 建立 Kubernetes API substrate；
2. 根据 ADR-0006，在需要时有限实例化精确 Git-defined primary CNI；
3. 等待 Kubernetes Nodes Ready；
4. 安装最小 Argo CD Seed；
5. 创建 `atlas-bootstrap`；
6. 注入与 Full Desired State 相同的 Application Health Capability；
7. 根据环境上下文渲染 Root Anchor；
8. 将 External Root Anchor 注入 Kubernetes。

Bootstrap 完成后退出。

Primary CNI 是 Atlas v1 唯一允许的 bootstrap-critical substrate
capability。Bootstrap 对它的 mutation authority 仅限同一 invocation 新建的
cluster，并在 Seed 成功后永久终止。稳态 Cilium 仍由
`platform-project` 中的单一 Leaf Application 管理。

---

### 1.3 Argo CD owns GitOps Reconciliation

Argo CD 对所有进入 GitOps Domain 的资源负责：

- Sync；
- Drift Detection；
- Reconciliation；
- Health；
- Prune。

External Root Anchor 本身除外。

External Root Anchor 不存在 Parent Application，因此不属于 parent-driven steady-state reconciliation。

---

### 1.4 Operators own Runtime Behavior

例如：

```text
Argo CD
 ↓
FlinkDeployment
 ↓
Flink Operator
 ↓
Runtime
```

Argo CD 不尝试取代 Domain Operator。

---

## 2. Canonical AppProject Model

Atlas 正式冻结三个 Canonical AppProject Identifier。

```text
atlas-bootstrap
platform-project
workload-project
```

任何实现、图示、Manifest 与 Conformance Audit MUST 使用上述准确名称。

禁止使用以下模糊别名代替实际资源名称：

```text
platform
workload
bootstrap-project
```

---

## 3. Three-Tier Trust Isolation

---

### 3.1 Tier-0 — `atlas-bootstrap`

`atlas-bootstrap` 由 Bootstrap 命令式创建。

其唯一职责是承载完成 Trust Chain 自举所必需的 Application。

允许：

- External Root Anchor；
- Project Bootstrap。

禁止：

- 普通 Platform Application；
- Workload Application；
- Developer Workload；
- 日常平台组件。

`atlas-bootstrap` 是 Bootstrap Capability Sandbox，不是普通租户 Project。

---

### 3.2 Tier-1 — `platform-project`

由 Project Bootstrap 声明式创建。

负责：

- Platform Control；
- Argo CD Self-Management；
- Infrastructure；
- Governance；
- Operator；
- Controller；
- Workload Orchestrator；
- 需要高权限的管理组件。

权限必须采用显式 allow-list，而不是无约束开放。

---

### 3.3 Tier-2 — `workload-project`

由 Project Bootstrap 声明式创建。

用于最终业务工作负载。

其原则是：

> Restricted by default.

必须禁止或严格限制：

- CRD；
- ClusterRole；
- ClusterRoleBinding；
- Cluster-wide privileged resources；
- 任意 Platform Namespace；
- Argo CD Control Namespace。

Namespaced：

- ServiceAccount；
- Role；
- RoleBinding；

按平台策略受控开放。

---

## 4. Bootstrap Trust Chain

Atlas 的完整无环 Bootstrap Chain 为：

```text
                 Bootstrap
                     │
         Kubernetes API substrate
                     │
          bootstrap-critical CNI
             Seed, if required
                     │
                Nodes Ready
                     │
          ┌──────────┴──────────┐
          ▼                     ▼
     Argo CD Seed       atlas-bootstrap
                          AppProject
                               │
                               ▼
                  External Root Anchor
                  project: atlas-bootstrap
                               │
                               ▼
                 Project Bootstrap App
                      wave: -110
                  project: atlas-bootstrap
                               │
                ┌──────────────┴──────────────┐
                ▼                             ▼
        platform-project               workload-project
                │                             │
                └──────────────┬──────────────┘
                               ▼
                    Normal GitOps Control
```

不存在：

```text
Application requires Project
        ↑
Project requires same Application
```

的循环依赖。

---

## 5. External Root Anchor

External Root Anchor 是整个 GitOps Control Graph 的外部根节点。

其模板位于：

```text
bootstrap/argocd/root-app.yaml.tpl
```

Bootstrap 根据：

```text
env/
```

中的环境上下文进行渲染，并将实例写入：

```text
.state/root-app.yaml
```

随后注入 Kubernetes。

其 Desired Definition 来源于 Git，但该活体对象不存在 Parent Application。

因此：

> Git owns its contract, Bootstrap owns its instantiation, but no parent Application owns its reconciliation.

这是有意设计，不属于配置缺陷。

---

### 5.1 Root Anchor Security Consequence

由于 Root Anchor 不被父级 Argo CD Application 修复：

```text
kubectl edit Application <root>
```

理论上可以导致：

```text
Live Root
≠
Git Template
```

因此 External Root Anchor 必须通过其他控制机制保护：

- Kubernetes RBAC；
- Argo RBAC；
- Admission Policy；
- Audit；
- Tier-0 Human Judgment；
- Drift Detection。

未来 Kyverno / 等价准入治理应成为该 Trust Boundary 的重要补强。

---

## 6. Two-Level Reconciliation DAG

Atlas v1.0.4 正式放弃：

> Flat Global Root Component DAG

同时禁止：

> Filesystem-shaped Deep Nested DAG.

正式模型为：

> **Minimal Root Macro DAG + Flat Capability DAG**

---

## 7. Level 1 — Root Macro DAG

`gitops/root/` 只负责 Trust Transition。

标准结构：

```text
gitops/root/
├── base/
│   ├── kustomization.yaml
│   ├── project-bootstrap-app.yaml
│   ├── platform-control-app.yaml
│   └── workload-control-app.yaml
│
└── overlays/
    ├── local-orbstack/
    │   └── kustomization.yaml
    └── prod/
        └── kustomization.yaml
```

Root SHALL NOT 直接列举：

```text
flink
envoy
prometheus
redpanda
minio
loki
tempo
...
```

---

### 7.1 Root Macro Waves

Root Synchronization Scope 定义：

```text
-110  Project Bootstrap
-100  Platform Control
   0  Workload Control
```

控制流程：

```text
Project Bootstrap Healthy
          ↓
Platform Control starts
          ↓
Platform Control Healthy
          ↓
Workload Control starts
```

Root 的 Wave 与 Platform Control 内部 Wave 属于不同 Sync Scope。

数字相同不表示同一全局时间点。

---

## 8. Level 2 — Platform Capability DAG

Platform Control Application 指向：

```text
gitops/platform/applications/overlays/<environment>/
```

例如：

```text
gitops/platform/applications/
├── base/
│   ├── kustomization.yaml
│   ├── foundation-app.yaml
│   ├── argocd-self-app.yaml
│   ├── sealed-secrets-app.yaml
│   ├── cert-manager-app.yaml
│   ├── flink-operator-app.yaml
│   ├── envoy-gateway-app.yaml
│   ├── kube-prometheus-stack-app.yaml
│   ├── redpanda-app.yaml
│   └── minio-app.yaml
│
└── overlays/
    ├── local-orbstack/
    └── prod/
```

这里保存的是：

> Application Graph Metadata

而不是组件实现。

---

## 9. Platform Capability Wave DAG

Platform Control Synchronization Scope 内采用：

```text
-100 Foundation
 -90 Management Plane
 -50 Operators
 -30 Infrastructure Controllers
 -10 Platform Services
```

逻辑如下：

```text
                    -100
                 Foundation
                     │
                     ▼
                     -90
               Management Plane
                     │
                     ▼
                     -50
                  Operators
                     │
                     ▼
                     -30
           Infrastructure Controllers
                     │
                     ▼
                     -10
              Platform Services
```

---

### 9.1 Wave -100 — Foundation

包括：

- Namespaces；
- ResourceQuotas；
- LimitRanges；
- 其他 Kubernetes 原生逻辑边界。

Bootstrap-critical primary CNI 的稳态 Application 也使用 Foundation
dependency role，同时其领域目录仍为 `platform/networking/`。它必须属于
`platform-project`，不得成为新的 Root child 或 Tier-0 Application。该
Application 在接管 Bootstrap Seed 后成为唯一稳态 Desired-State
Reconciler。

环境强绑定资源必须通过 Environment Hydration 进入。

---

### 9.2 Wave -90 — Management Plane

包括：

- `argocd-self`；
- Sealed Secrets；
- cert-manager；
- OIDC；
- Policy Engine；
- ESO；
- 其他平台自身管理与治理能力。

其中 Secret Management Controller 必须早于其 Consumer。

---

### 9.3 Wave -50 — Operators

包括：

- Flink Kubernetes Operator；
- Spark Operator；
- 其他扩展 Kubernetes API 的 Domain Operator。

必须在对应 CR 出现前确保：

- CRD 已注册；
- Controller Ready；
- Webhook Ready；
- Application Healthy。

---

### 9.4 Wave -30 — Infrastructure Controllers

包括：

- Envoy Gateway Controller；
- Prometheus Operator；
- 其他底层 Controller。

“Controller”是依赖角色，不是目录领域。

例如：

```text
Envoy Gateway
```

仍然属于：

```text
platform/networking/
```

而不是：

```text
platform/controllers/
```

---

### 9.5 Wave -10 — Platform Services

包括：

- Redpanda / Kafka；
- MinIO；
- Database；
- 其他 Stateful Platform Services。

“Platform Service”同样是 DAG Role，不是 Domain Directory。

---

## 10. Domain Ontology

目录只表达 Domain Ownership。

```text
gitops/platform/
├── applications/
├── foundation/
├── management/
├── operators/
├── networking/
├── observability/
├── messaging/
└── storage/
```

---

### 10.1 `applications/`

不是业务领域。

仅保存：

- Application；
- Kustomization；
- Application DAG Overlay；
- Wave Metadata；
- Parent-level protection metadata。

---

### 10.2 `foundation/`

Kubernetes 原生逻辑边界。

---

### 10.3 `management/`

平台自身治理能力：

- Argo CD Self Management；
- AppProjects；
- RBAC；
- OIDC；
- Sealed Secrets；
- ESO；
- cert-manager；
- Policy Engine。

AppProject Desired State 必须归属：

```text
platform/management/projects/
```

Project Bootstrap Application 只是使用 `atlas-bootstrap` 去同步这些对象。

因此：

> Bootstrap lifecycle ≠ AppProject domain ownership.

---

### 10.4 `operators/`

领域扩展 API Controller：

- Flink Operator；
- Spark Operator；
- 其他平台领域 Operator。

---

### 10.5 `networking/`

网络与流量能力：

- Envoy Gateway；
- Gateway API；
- Gateway；
- HTTPRoute；
- NetworkPolicy-related resources。

---

### 10.6 `observability/`

统一遥测：

- Kube-Prometheus-Stack；
- Alloy；
- OpenTelemetry Collector；
- Loki；
- Tempo；
- OTLP Transport。

---

### 10.7 `messaging/`

异步事件总线：

- Redpanda；
- Kafka。

---

### 10.8 `storage/`

数据持久化平台：

- MinIO；
- 未来其他存储服务。

---

## 11. Leaf Application Contract

每个可独立：

- 失败；
- 暂停；
- 升级；
- 回滚；
- 观察；
- 恢复；

的平台能力 SHOULD 成为独立 Leaf Application。

Leaf Application 是：

> Minimum Reconciliation and Failure Isolation Unit.

禁止仅因为：

```text
platform/observability/
```

存在子目录，就形成：

```text
platform-app
 ↓
observability-app
 ↓
prometheus-app
```

这种目录镜像式 Application 链。

默认最大结构为：

```text
External Root
      ↓
Platform Control
      ↓
Leaf Application
      ↓
Resources
```

任何额外 Application 层 MUST 由 ADR 证明其具有真实控制边界价值。

---

## 12. Application Health Gate Contract

Sync Wave 只有结合 Health Gate 才构成真正的依赖图。

Atlas 的 DAG Contract 是：

```text
Sync Wave
+
Child Automated Sync
+
Application Health
=
Deterministic Application DAG
```

三者缺一不可。

---

### 12.1 Application Health Capability

Bootstrap Seed 与 `argocd-self` 必须注入同一份：

```text
argoproj.io/Application
```

健康评估能力。

该 Capability MUST 来自同一份受版本控制资产。

禁止：

```text
Bootstrap health semantics
≠
Steady-state health semantics
```

---

### 12.2 Child Automated Sync

所有作为 DAG 前置条件的 Child Application：

> MUST enable Automated Sync

或者使用一个外部确定性触发机制。

禁止出现：

```text
Wave -50 App
Healthy only after manual sync
```

却被用作：

```text
Wave -30 prerequisite
```

否则 DAG 会永久阻塞。

---

### 12.3 Macro Health Gate

Root 不需要理解 Platform 内部组件。

Root 只判断：

```text
platform-control = Healthy
```

因此：

```text
Foundation
+
Management
+
Operators
+
Controllers
+
Platform Services
        ↓
Converged
        ↓
Platform Control Healthy
        ↓
Workload Control may proceed
```

这将：

> Platform Ready

从人类经验判断转化为机器可判定状态。

---

## 13. Workload Control Model

Workload Control 必须区分：

> Control-plane Orchestrator

与：

> Tenant Payload Application.

这是 Tier-1 / Tier-2 权限隔离的核心。

---

### 13.1 Workload Control Application

Root 中：

```text
workload-control-app.yaml
```

如果其职责是创建：

- Application；
- ApplicationSet；
- 其他 Argo CD Control Objects；

则其本质属于：

> Tier-1 Control Plane.

因此：

```text
project: platform-project
```

而不是：

```text
project: workload-project
```

---

### 13.2 Tenant Leaf Applications

由 Workload Control 生成或管理的最终业务 Application：

```text
project: workload-project
```

其资源只能进入被允许的 Workload Namespace。

控制关系：

```text
Workload Control
project: platform-project
          │
          ▼
Application / ApplicationSet
          │
          ▼
Tenant Leaf Application
project: workload-project
          │
          ▼
Developer Namespace
```

---

### 13.3 Why

禁止让 `workload-project` 为了创建 Argo CD Application/ApplicationSet 而获得：

- Argo CD Namespace 写权限；
- Platform CR 写权限；
- 高危 Cluster Capability。

原则是：

> WHO creates tenant Applications → Tier-1
> WHAT tenant Applications may deploy → Tier-2

---

## 14. Workload Directory

建议标准：

```text
gitops/workloads/
├── applications/
│   ├── base/
│   └── overlays/
│
└── apps/
    ├── clickstream-processor/
    ├── example-streaming-job/
    └── ...
```

其中：

```text
applications/
```

保存 Workload Orchestration Metadata。

```text
apps/
```

保存真正的业务 Desired State。

Phase 2B 可以将：

```text
applications/
```

演进为 ApplicationSet Tenant Model。

---

## 15. Data Bootstrap Jobs

数据初始化逻辑不得因为某个 Wave 到达就默认具有破坏性执行权。

安全初始化必须：

> 100% Idempotent.

例如：

- create-if-not-exists；
- schema initialization；
- non-destructive seed。

---

### 15.1 Safe Initialization

允许进入自动 DAG。

推荐作为 Workload Control Scope 中：

```text
+10
```

级别的初始化单元。

---

### 15.2 Destructive Migration

例如：

- DROP；
- destructive schema rewrite；
- irreversible state conversion；
- mass data deletion；

不得依赖普通 AutoSync 自动执行。

必须转化为：

> Human-gated Workflow.

---

## 16. Sync Wave Scope

Atlas 正式规定：

> Sync Wave is parent synchronization-scope local.

因此：

Root：

```text
-110 Project Bootstrap
-100 Platform Control
   0 Workload Control
```

与 Platform Control：

```text
-100 Foundation
 -90 Management
 -50 Operators
 -30 Controllers
 -10 Services
```

虽然存在相同数字：

```text
-100
```

但它们不属于同一个全局调度空间。

禁止将所有 Wave 理解成：

> Cluster-wide absolute sequence number.

---

## 17. Environment Hydration

Environment-specific state 与通用 Platform Domain 必须分离。

标准结构：

```text
gitops/
├── root/
│   └── overlays/
│       ├── local-orbstack/
│       └── prod/
│
├── environments/
│   ├── local-orbstack/
│   └── prod/
│
└── platform/
    └── applications/
        └── overlays/
            ├── local-orbstack/
            └── prod/
```

---

### 17.1 Root Hydration

Bootstrap 选择：

```text
root/overlays/<environment>
```

作为 External Root Source。

---

### 17.2 Platform Hydration

`platform-control-app` 选择：

```text
platform/applications/overlays/<environment>
```

作为 Platform DAG。

---

### 17.3 Environment-specific K8s State

例如：

- local StorageClass；
- Cloud-specific CSI；
- environment-specific Gateway address；
- substrate-specific configuration；

必须位于：

```text
gitops/environments/<environment>/
```

或由对应 Overlay 显式引用。

不得污染通用 Platform Domain。

---

## 18. Bootstrap Adoption Contract

Bootstrap 恢复必须满足：

```text
Object Identity Parity
+
Version Parity
+
Health Capability Parity
+
CRD Compatibility
=
Bootstrap Adoption Contract
```

---

### 18.1 `versions.lock`

Bootstrap 与 GitOps Self-management 必须共同消费：

```text
versions.lock
```

禁止：

```text
bootstrap Argo version
≠
argocd-self desired version
```

所导致的隐式：

- downgrade；
- upgrade；
- CRD incompatibility。

---

### 18.2 Recovery Seed

Break-Glass Seed 的目标是：

> Minimal but adoption-compatible.

不是重新建立另一套独立控制平面。

### 18.3 Bootstrap-critical CNI adoption

ADR-0006 允许 Bootstrap 在 Argo CD 尚不能运行时，仅为同一 invocation
新建的 cluster 实例化精确 Git-defined primary CNI。Seed 成功后 Bootstrap
不得再次写入该 cluster 的 Cilium 对象。

初始 Seed 与稳态 Cilium Application 必须绑定相同的 Git revision、vendored
Chart/tree authority、Atlas values、release name、namespace 与 rendered
object identity set。`CILIUM_ADOPTED` 至少要求单一 `platform-project`
Application、精确 source authority、`Synced`、`Healthy`、精确对象
ownership/inventory，以及不存在并发 Bootstrap writer。Health 单独不构成
接管证明。

现有 cluster 上缺失、不完整、未知或漂移的 Cilium 状态必须失败关闭；普通
Bootstrap 不得重新安装、修复或迁移。

---

## 19. Dual Deletion Protection

Atlas 实施两层独立保护。

---

### 19.1 Parent-level Prune Protection

所有由 Parent Application 管理的 Tier-0 / Tier-1 Child Application CR 应具有：

```text
argocd.argoproj.io/sync-options: Prune=confirm
```

保护链：

```text
Git accidentally removes child Application
                 ↓
Parent attempts prune
                 ↓
Prune suspended
                 ↓
Human confirmation
```

---

### 19.2 No Cascading Finalizer

核心 Tier-0 / Tier-1 Application 默认不得携带级联资源删除 finalizer。

因此：

```text
delete Application
        ↓
Application CR deleted
        ↓
Managed resources survive
```

---

### 19.3 Protection Boundary

上述保护主要针对：

> Application Control Graph.

并不意味着：

```text
StatefulSet
PVC
Kafka Cluster CR
Database
Namespace
```

自动获得相同保护。

对于高价值 Leaf Resources，必须根据风险显式配置：

- resource-level `Prune=confirm`；
- `Delete=false`；
- Admission Policy；
- Domain-specific retention mechanism。

---

## 20. Secret Dependency Contract

Phase 1 使用 Sealed Secrets 时：

```text
Sealed Secrets Controller
          ↓
Controller Healthy
          ↓
SealedSecret Reconciliation
          ↓
Secret Exists
          ↓
Consumer Application
```

Consumer 不得在 Secret Capability Ready 前进入强依赖路径。

Phase 2A 切换 ESO 时同样遵守：

```text
ESO
 ↓
External Secret Materialization
 ↓
Consumer
```

---

## 21. Emergency Recovery & Blast Radius Freeze

Atlas 使用：

> Freeze Outside-In, Resume Inside-Out.

---

### 21.1 Detect

确认：

- 错误 Revision；
- 受影响 Application；
- 数据面状态；
- 爆炸半径；
- 是否仍有危险 AutoSync。

---

### 21.2 Freeze Outside-In

首先冻结：

```text
External Root Anchor
```

然后依次关闭受影响 Control Application 的 AutoSync。

重点包括：

- Platform Control；
- argocd-self；
- 受影响 Leaf Applications；
- Workload Control。

目标不是删除资源，而是：

> Stop propagation.

---

### 21.3 Snapshot

备份：

- Application 状态；
- AppProject 状态；
- 当前 Revision；
- Controller Configuration；
- 关键 Event / Audit Evidence。

---

### 21.4 Restore Seed

使用 Bootstrap 恢复满足 Adoption Contract 的 Argo CD Seed。

---

### 21.5 Repair Git

Git 必须回到：

> Known-Good Revision.

不得依赖长期 Live Edit 作为恢复终态。

---

### 21.6 Resume Inside-Out

推荐顺序：

```text
Argo Seed
   ↓
argocd-self
   ↓
Management Capability
   ↓
Platform Capability DAG
   ↓
Platform Control Healthy
   ↓
Workload Control
   ↓
Tenant Workloads
   ↓
External Root normal reconciliation
```

必须逐级验证 Health。

---

## 22. ApplicationSet Boundary

ApplicationSet 是 Atlas Phase 2B Tenant Platform 的核心候选机制，但不作为 v1.0.4 平台 Bootstrap Correctness 的必要基础。

当前 Frozen Baseline 的平台启动正确性依赖：

```text
Application
+
App-of-Apps
+
Sync Wave
+
Application Health
```

ApplicationSet 的主要目标为：

- Tenant Expansion；
- Multi-cluster Workload Generation；
- Developer Self-Service；
- Fleet-style Application Generation。

ApplicationSet 不应取代 Tier-0 Root Trust Model。

---

## 23. AI & Human Control Boundary

`gitops/root/`：

```text
READ      allowed
ANALYZE   allowed
PROPOSE   allowed
GENERATE  allowed
MERGE     human-gated
APPLY     human-gated
```

Tier-1：

允许未来在 Policy Guardrail 成熟后开放更高程度 Agent Autonomy。

Tier-2：

是最适合 AI Agent 自治执行的主要区域。

治理演进顺序必须是：

```text
Guardrail
   ↓
Auditability
   ↓
Policy Enforcement
   ↓
Agent Autonomy
```

而不是先开放自治，再补安全机制。

---

## 24. Phase 2 Governance Roadmap

---

### Phase 2A — Safety Foundation

引入：

- Kyverno / equivalent policy engine；
- NetworkPolicy baseline；
- ESO；
- Root Anchor admission protection；
- stronger audit controls。

---

### Phase 2B — Tenant Platform

建设：

- ApplicationSet；
- Tenant Model；
- Namespace lifecycle；
- workload-project policy；
- multi-tenant boundaries。

---

### Phase 2C — Developer Experience

建设：

- Backstage；
- Golden Path；
- Templates；
- Platform API；
- developer self-service。

---

### Phase 2D — Agent Automation

在 Guardrail 完成后，开放：

- Agent-generated PR；
- Policy-verified changes；
- limited auto-merge；
- bounded autonomous remediation；
- declarative platform operation。

Agent 自治权不得跨越 Tier-0 Human Judgment Boundary。

---

## 25. GitOps Control Plane Invariants

Atlas GitOps Control Plane v1.0.4 冻结以下领域不变量。

### GITOPS-01

External Root Anchor MUST remain outside parent Application reconciliation.

### GITOPS-02

Canonical AppProjects MUST be:

```text
atlas-bootstrap
platform-project
workload-project
```

### GITOPS-03

Root MUST contain only macro trust/readiness control Applications.

### GITOPS-04

Platform components MUST be orchestrated by Platform Control rather than enumerated directly under Root.

### GITOPS-05

Directory hierarchy MUST NOT be mechanically mirrored as Application hierarchy.

### GITOPS-06

Leaf Application SHOULD be the minimum failure-isolation unit.

### GITOPS-07

Sync Wave MUST be interpreted within its parent synchronization scope.

### GITOPS-08

Any Child Application acting as a DAG prerequisite MUST have deterministic synchronization.

### GITOPS-09

Bootstrap and argocd-self MUST share Application Health semantics.

### GITOPS-10

Bootstrap and argocd-self MUST share `versions.lock`.

### GITOPS-11

Workload orchestrators creating Argo CD control objects MUST operate in Tier-1.

### GITOPS-12

Tenant payload Applications MUST operate in `workload-project`.

### GITOPS-13

Tier-0 / Tier-1 control graph deletion MUST NOT implicitly destroy descendant runtime resources.

### GITOPS-14

Destructive data mutation MUST NOT be triggered merely by automatic Wave progression.

### GITOPS-15

The primary CNI MAY be finitely instantiated before Argo CD only under the
creation-scoped ADR-0006 contract and MUST be adopted by exactly one
`platform-project` Leaf Application. Bootstrap MUST NOT remain or later become
a steady-state Cilium reconciler.

---

## 26. Final Frozen Control Graph

Atlas v1.0.4 的最终规范拓扑为：

```text
                         Bootstrap
                             │
                 ┌───────────┴───────────┐
                 ▼                       ▼
             Argo Seed             atlas-bootstrap
                                        │
                                        ▼
                             External Root Anchor
                                        │
                         Root Macro Sync Scope
                                        │
                 ┌──────────────────────┼─────────────────────┐
                 │                      │                     │
                 ▼                      ▼                     ▼
       -110 Project Bootstrap   -100 Platform Control    0 Workload Control
          atlas-bootstrap          platform-project        platform-project
                 │                      │                     │
        ┌────────┴────────┐             │                     │
        ▼                 ▼             ▼                     ▼
platform-project   workload-project   Platform DAG       Workload Orchestration
                                        │                     │
                           Platform Sync Scope                  │
                                        │                     ▼
                    ┌───────────────────┼──────────────┐  Tenant Applications
                    │                   │              │   workload-project
                    ▼                   ▼              ▼
               Foundation          Management       Operators
                  -100                -90             -50
                                                           │
                                                           ▼
                                                      Controllers
                                                         -30
                                                           │
                                                           ▼
                                                   Platform Services
                                                         -10
                                                           │
                                                           ▼
                                                     Platform Healthy
                                                           │
                                                           ▼
                                                     Workloads Enabled
```

这张图是 Atlas GitOps Control Plane v1.0.4 的架构冻结点。

未来：

- 增加 Redpanda；
- 增加 ClickHouse；
- 增加 Spark；
- 增加 Loki；
- 增加 Tempo；
- 增加新的 Operator；

均应优先修改：

```text
gitops/platform/
```

而不是：

```text
gitops/root/
```

只有当：

- Trust Domain 改变；
- Control Ownership 改变；
- Macro Readiness Boundary 改变；
- Bootstrap Chain 改变；

时，才允许修改 Tier-0 Root Architecture。

---

## 27. Architecture Freeze Statement

Atlas GitOps v1.0.4 正式冻结以下模式：

> **External Root Anchor
>
> - Minimal Root Macro DAG
> - Flat Platform Capability DAG
> - Tier-1 Workload Orchestration
> - Tier-2 Workload Execution**

其核心目标不是创建最复杂的 GitOps 拓扑，而是创建：

> **最容易证明、最容易恢复、最容易审计、最不容易被人类或 AI Agent 误操作的控制图。**

从本版本开始，任何将 Root 重新扩张为 Platform Component Catalog，或仅为了匹配目录层次而引入深层 App-of-Apps 的实现，应默认视为对 Frozen Baseline 的偏离，并进入正式 Architecture Review。
