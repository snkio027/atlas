# ADR-0026：2026-11-03 依赖升级的权限与配置差异

Status: Proposed。用户已授权按依赖检查实施源码升级；本文列出的新权限及配置差异
作为具体候选提交所有者审查。尚无现有实例升级、部署、恢复或合并批准。

## 范围

从主线 `8586a3efe4627144f4750e6e513ac6c9a2dbd973` 更新当前产品锁定输入。
完整版本和制品校验见 [升级记录](../dependency-upgrade-20261103.md)。
Kind 0.33.0 / Kubernetes 1.37.0、Gateway API 1.6.3、Kafka 4.3.1 及历史运行快照保持原绑定。

## 监控 Operator 的有限权限增量

kube-prometheus-stack 92.17.1 的 `atlas-monitoring-operator` ClusterRole
在已有 `monitoring.coreos.com` 主资源 PATCH 规则中加入以下子资源：

- `alertmanagers/finalizers`
- `prometheusagents/finalizers`
- `prometheuses/finalizers`
- `thanosrulers/finalizers`

这些子资源原来已有 UPDATE；本候选增加 PATCH，不增加 API group、主体、绑定、
命名空间、通配权限或 Secret 权限。它沿用上游 chart 的
[finalizer 权限修正](https://github.com/prometheus-community/helm-charts/commit/0eeacf55499960aac1ff6cc766dba53cd0a19933)。
最终渲染权限差异保存在 [机器可读清单](../evidence/dependency-upgrade-20261103.json)。

选择保留这个明确增量并单独审查，不通过修改上游归档、关闭权限检查或任意扩大规则来通过升级。
它允许既有 Operator 维护自己管理的监控 CR finalizer；不是 Atlas 恢复、清理或重新获得 Seed
权限的入口。是否用于运行实例，仍须其精确发布计划和权限阶段审查。

Flink Operator 1.16.1 的完整上游权限集合仅改变版本/chart 标签。
转换器继续校验精确 chart SHA-256 和完整 RBAC/ServiceAccount 摘要；规则、subjects、
roleRef、对象身份与 service-flink-session 监听范围保持不变。
现有负例测试继续拒绝缺失、附加、主体及权限篡改。

## 配置差异

- Argo chart 加入 `ssh.bitbucket.org` 的三个公开 host key，复用已有 `bitbucket.org`
  对应公钥。既有 GitHub key 未变化。当前平台仍使用原 Git URL、AppProject 与目的地约束。
- Cilium chart 不再生成 `envoy-xds-mode=ads`。当前 profile 继续显式关闭 Envoy、L7 proxy、
  Hubble、Ingress 与 Gateway API；不启用新的数据通路或 Secret 同步。
- Grafana 子 chart 14.3.1 增加 values 类型 schema；当前值已通过真实 Helm 渲染。
  镜像仍为 distroless，匿名访问关闭，持久卷与只读根文件系统保留。
- kubelet 指标过滤不再丢弃 `container_memory_swap`，增加该现有端点的指标保留；
  scrape 目标、认证、命名空间选择和存储上限不变。
- Alertmanager 0.34.2 默认拒绝跨源 API 请求。保留这一收紧，不设置通配 CORS。
  已有管理入口为同源 GET/HEAD；其升级后浏览器行为仍需实际实例验收。
- YAML v3.0.6 修正 merge key 识别。Atlas 仍拒绝 merge、anchor、alias 和自定义 tag，
  不因解析库升级放宽输入格式。

## 不变条件与验证边界

真实渲染中的资源身份无增删；Foundation、AppProject、External Root、owner 分工、
删除保护、存储规格、Sealed Secrets 自动续钥关闭及 Bootstrap durable latch 保持原契约。
其他组件没有 RBAC 规则或绑定差异。监控 CRD schema 更新由锁定上游归档提供。

仓库 Full、离线配置校验、打包与运行验收分开留证。Kubernetes 1.37 仍超出 Cilium、
Envoy Gateway、cert-manager 和 CNPG 当前正式支持矩阵的一部分；版本更新不消除这一限制。
本文不能将本地编译/渲染成功解释为集群已升级、数据迁移成功或生产就绪。
