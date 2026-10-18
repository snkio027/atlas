# ADR-0019：本机平台管理 HTTPS 入口

Status: Proposed — 2026-10-11；仅单所有者 OrbStack/Kind 开发实例，待审查部署。

## 范围与依据

用户要求使用旧 Atlas 的网络边界，以常驻域名访问所有需要的现有平台界面/API，
本轮只实现清单、离线验证和部署审查。依据原 Atlas Network v1.0 §§4、6、8：
Gateway API 管理北南向路由/TLS；内部数据流直接通过 CNI；权限使用 namespace/Pod
身份；既有 IPv4 Envoy 和 NodePort/loopback 映射保持不变。

## 决定候选

固定五个入口：Grafana、Argo CD、Prometheus、Alertmanager 和 S3 API。复用
`atlas-gateway/development`，每个主机名独立 listener、Certificate、HTTPRoute。
listener 仅允许后端所在 namespace 的 HTTPRoute，backend 使用同 namespace Service。
不创建 ReferenceGrant，不开放通配域名/namespace，不新增组件、控制器或代理进程。

用户确认 Prometheus/Alertmanager 入口只允许 GET/HEAD，无认证，仅限本机开发。
它们不提供告警静默等管理写入；不把只读等同于数据保密或多租户隔离。
Grafana、Argo CD 仍用现有账户；S3 仍用已有 SigV4 凭据，HTTPS endpoint 使用
path-style bucket。HTTP 仅重定向到实例的 HTTPS host port，不能代理后端明文请求。

- 证书由现有 `development-ca` Issuer 生成，仅新增叶子密钥，不改变 CA/Sealed Secrets
  Trust Root、备份等级、应用凭据或密文。
- 既有 `local-pki` 继续拥有 Certificate；`edge` 拥有新增路由/NetworkPolicy 和
  同一个 Gateway；`monitoring` 仅增加 Grafana 外部 URL/secure cookie 配置并更新
  Prometheus/Alertmanager externalUrl，保证域名、端口和生成链接一致。
- `project-bootstrap` 原位增加 `platform-project` 的 HTTPRoute kind 权限，不增加
  destination，不授予 workload-project 平台权限。AppProject 仍是共享的 namespace ×
  kind 权限并集，此次增加会作用于它已有的全部 destinations，不能宣称逐路由隔离。
- 新增网络规则仅允许既有 Gateway Pod 身份到实际后端端口。Argo Seed 现有
  server NetworkPolicy 为 allow-all，additive policy 不能收紧它；本轮不改写 Seed。
  这个历史限制在审查中明确保留，不宣称实现了管理面零信任。

## 实现与发布边界

`atlas-platform management-access` 是本地、固定范围的清单投影器：输入精确 deployment
Git export，输出两个阶段的文件差异和摘要。没有 live apply、Git push、credential
读取、恢复、自动激活或长期调谐。它不自动纳入 D1 Base/Full，避免首装在后端尚未
安装时等待管理 route 健康、破坏有限 Bootstrap。已有业务 listener/资源/owner 不变。

1. prerequisites：增加五个 Certificate 和精确 AppProject kind 权限。
2. 人工授权后的运行 Gate：证书 Ready 对应当前 generation；权限已由 Argo 接管。
3. routes：增加十个 HTTP/HTTPS listener、十个 HTTPRoute、五条 NetworkPolicy，
   更新三项 monitoring 工作负载配置。由 Argo 和组件 controller 调谐。
4. 观察 Gateway/Routes/Pods 与 HTTPS 功能，拒绝 HTTP 写方法负例，核对旧业务。

完整候选在写出前通过 scope、唯一 ownership、现有权限、CRD schema/default-stability
校验；默认值依据已部署 CRD，版本不一致失败。发布必须另行批准 exact parent/target
与两个 Git commits 的派生规则。发布不重写 D1 FullCommit/S2 inventory/final 或旧计划。

## 兼容性限制

当前业务工作区的 status/open/deploy 绑定历史 publication receipt 和 exact remote HEAD；
平台维护提交后它们将拒绝新 HEAD。现有业务仍由 GitOps 运行，但不能把旧工作区标记为
已接纳平台维护，也不能用新清单伪造旧 Runtime PASS。当前没有受支持的工作区 rebase
命令。本轮不放宽该 fence；部署审查必须明确这一使用影响，不能静默发布再声称日常
业务更新不受影响。解决跨平台版本的工作区兼容性属于独立实现范围。

## 验证与未证明项

定向回归覆盖五个入口、方法限制、证书/namespace/Pod 端口、旧身份保留、默认值稳定、
缺失/不兼容后端拒绝、权限和源 schema 拒绝、真实本地 Kustomize；并运行 task quality。
运行时尚未部署；浏览器登录、GET/HEAD 拒绝边界与原业务无回归须由批准后的实测证明。
主机 hosts/证书信任不由编译器改变；无公网/LAN/共享主机/生产支持声明。
