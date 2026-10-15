# Atlas

Atlas 是一个 Go 实现的本地 Web/API 开发平台：有限 Bootstrap 建立控制面，
Git 定义平台与业务，Argo CD 持续调谐。代码采用 [MIT](LICENSE)。

当前支持单所有者、macOS arm64 + OrbStack 四节点环境，HTTPS、指标/看板和
独立 S3 Binding。不是生产或多租户托管平台；不支持自动恢复、业务退役和跨集群。

## 开始使用

1. [安装一个平台实例](docs/first-install.md)：锁定发行包、独立凭据与备份、GitOps 交接。
2. [接入自己的 Go Web/API 应用](docs/application-delivery.md)：工作区模板、离线镜像、
   可读计划、部署、更新、状态、访问与日志。
3. [应用排障与支持边界](docs/application-delivery.md#状态访问与排障)。
4. [常驻 HTTPS 管理入口候选](docs/management-access.md)：Grafana、Argo CD、Prometheus、Alertmanager、S3；部署前需审查。

普通业务入口：

```sh
atlas-platform app --help
atlas-platform app check
atlas-platform app plan
atlas-platform app deploy
atlas-platform app status
atlas-platform app open --service experiment-archive
```

首次使用先按接入文档创建工作区。app 命令当前是源码构建候选，配置/范围由
[Proposed ADR-0018](docs/adr/0018-ordinary-application-delivery.md) 单独审查；
本地检查不代表真实业务交付闭环已经通过。普通部署完成与业务功能验收分开报告。

新安装的平台 namespace 使用职责或组件名，不带 `atlas-` 前缀；名称对应、组件扩展
与旧实例边界见 [平台 namespace 约定](docs/platform-namespaces.md)。

## 维护与验证

```sh
task quality
task build
```

构建需要 Go 1.27.2 和锁定工具；运行时不下载依赖。详细要求、原始 Bootstrap
命令、历史验收和 cutover 状态放在 [维护者入口](docs/maintainer-entrypoint.md)。

- [当前依赖候选与验证边界](docs/dependency-upgrade-20261010.md)
- [架构与权责](docs/architecture.md)、[贡献约束](AGENTS.md)、[ADR](docs/adr/)
- [S1 冻结基线](docs/s1-final-validation.md)、[D1 首装验证](docs/d1-validation.md)
- [S2 r7 Runtime PASS](docs/s2-r7-validation.md)：绑定 d011813；fc809be 是增量代码修复
- [S2 高级验收工作流](docs/s2-workloads.md)：保留验收专用 demo 与不可重放 probe
- [原 Atlas cutover](docs/go-bootstrap-cutover-contract.md)：独立决策，尚未执行

历史证据保持原提交绑定；新业务路径不改写 r7，不默认新建一轮验收集群。

数据基础设施候选：见 [PostgreSQL / Redis / Kafka / Flink / Loki](docs/data-platform.md)。默认未启用；本地验证与部署审查分开。
