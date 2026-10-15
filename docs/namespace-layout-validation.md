# YAML / namespace 布局本地验证

2026-10-16。实现：`af32f9ac8e65c103d765659f2cc5653bbab122a8`；分支 `codex/namespace-ownership-layout`。
**Local PASS，Runtime NOT RUN。** 未访问或修改当前 atlas-data-r1，未发布 GitOps、生成私钥或使用凭据。

## 已执行

- 完整 `task quality` 退出 0：gofmt、go vet、全包 race 回归、真实离线 Helm/Kustomize、
  scope/CRD default-stability、权限与 ownership、冻结 OT-1 的 29 阶段契约及 9 个 Python 回归、实验入口构建/vet。
- `platform:render` + `platform:check` 通过；主图检查输出 345 个 GitOps 资源，
  catalog 中的可选数据组件也经过渲染/schema/权限检查。未启用的模板不等于已部署服务。
- 严格 YAML：重复键、类型/未知字段、alias/anchor/merge/tag、多文档及双源拒绝；
  JSON 私有快照保留；工作区与发布 Git 声明使用 YAML，重复编译字节一致。
- 锁定 Helm 4.3.0 实际将 plain `on/no` 当作布尔值。新增回归验证严格解析后传入规范 JSON，
  使其保持字符串；显式 `true` 仍为布尔值。人工文件不改为 JSON。
- 三组 Operator/实例配额逐项守恒，并检查锁定 Deployment 滚动峰值；watch/subjects、
  有效累加 RBAC、未知 Chart/RBAC 形状及跨域具名 Secret 负例通过。
- Namespace 唯一声明、删除保护、NetworkPolicy 同 peer、Project ID 派生/保留名、
  精确 smoke Gateway、未知/旧/混合 Product 布局拒绝与普通业务更新回归通过。
- 空 `GOMODCACHE`、`GOPROXY=off`、`GOSUMDB=off`、`-mod=vendor` 的本地开发包构建通过，
  包中包含 YAML 示例与解析器许可证。随后从干净实现 commit 构建本地候选包，未发布。

工具：Go 1.27.2、Helm 4.3.0、kubectl 1.37.1（本地 Kustomize）、yq 4.54.1；
冻结 OT-1 使用自身锁定工具。当前基础节点仍锁定 Kubernetes 1.37.0；本轮未升级平台组件。

## 结果身份

| 项 | SHA256 |
| --- | --- |
| layout.yaml | `58ed261b4f13c31a96d791bd660a7e7dfff10697f314542d97461f8d6ce3d2bd` |
| 124 个 GitOps 文件的内容映射 | `9122e26687318a0dc13b3b6d5673a1e342a8173fe59113333f82256e75cd390b` |
| 完整 quality.log | `38b6f9b9bdad7c90df0d46278a9fa746609628d25801499220264b0d5019d3e8` |
| 本地候选 archive | `1f155bc8848346638b677053e94613459611bb5f0922851cd9ef5ebab14cd317` |
| Product | `55e7549bf8d0a89fce1493341059304f78eb97bd66d9fffbb500fff04de43a1c` |
| installer binary | `45fa17a3faa6ca4aa056b9d1e050c34b5798e32aaeba18d2fccc90001c679b74` |

日志与文件索引位于 `.state/latest/namespace-layout/`，属于可替换的开发证据。
全量质量检查期间输入摘要未变化；之后仅补充 README/首装文档的历史验证边界以及本审查记录，
没有修改已验证的 Go 代码、YAML 输入或生成清单。未运行远端 exact-head CI。

历史 schema 3 测试输入从 `78967ec7fe53a17f6787349e749844c4ff38f751` 冻结为
`internal/atlas/testdata/development-legacy.zip`，179 个条目已逐字节与该提交核对，
archive SHA256 `a8bd244b772119553196afda41521ac364c43817389f9680e807a8dcfb51b32a`。
它用于验证历史不变量，不为新产品提供旧布局兼容，不改写历史 Runtime 结论。

## 仍需独立新安装证明

新增 platform-argocd 的 Seed/GitOps 接管；三个 Operator 跨域调谐、Webhook 与有效权限；
服务 TLS/认证、持久化、负向网络访问、metrics/logs 与业务链路；真实重复执行行为。
本地静态检查不模拟 admission 或 controller，也不证明生产多租户隔离、HA/DR 或自动恢复。

下一步是 [部署审查](namespace-layout-review.md)。现存 data-r1 readiness-repair 仍待审，
新布局与其旧 STOP/计划不合并。
