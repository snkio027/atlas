# 本机管理入口（r3 已发布，验收 STOP，排序修复待审）

本轮为现有平台准备五个常驻入口，复用 Envoy Gateway，无需 port-forward。
架构与限制见 [ADR-0019](adr/0019-local-management-https.md)。

| 组件 | r3 预期 URL | 访问方式 |
| --- | --- | --- |
| Grafana | `https://grafana.atlas.test:18443` | 现有 Grafana 账户 |
| Argo CD | `https://argocd.atlas.test:18443` | 现有 Argo CD 账户，浏览器 UI |
| Prometheus | `https://prometheus.atlas.test:18443` | 无登录，仅 GET/HEAD |
| Alertmanager | `https://alertmanager.atlas.test:18443` | 无登录，仅 GET/HEAD；不支持 silence 写入 |
| S3 | `https://s3.atlas.test:18443` | SigV4 API，path-style；不是文件管理 Web UI |

既有 `web.atlas.test` 和 `experiment-archive.atlas.test` 不变。Hubble UI 没有启用；
SeaweedFS master/filer/volume 管理端口、Redis、exporter、Operator/webhook、Envoy
控制端口不属于外部消费入口，保留内部访问。

只读 Prometheus/Alertmanager 仍可暴露监测数据，限制仅适合单所有者本机开发。
Kind 入口必须绑定 127.0.0.1；不因此声明集群内部或同机其他用户无法访问。

## 版本边界

本文件的 r3 SHA、目标和操作记录属于旧 namespace 布局，须使用其绑定版本。
当前源码渲染器使用 gateway、monitoring、object-storage；不会将旧 r3 布局自动转换。
新安装应提供自己的精确部署 SHA/端口，不能复制下面的 r3 示例 SHA。
参见 [namespace 约定](platform-namespaces.md)。

## 离线生成可审查清单

从被审核的精确部署 commit 导出 GitOps tree，保留现有 S2 业务对象；不要拿源码仓库
的 development 示例覆盖一个已安装实例。下列命令仅生成本地文件：

```sh
# 在源码 checkout 内，DEPLOYED 必须是部署分支当前经核验的完整 SHA。
DEPLOYED=18d517c12f94f535dfbbb71c8f0e9c8865aac300
mkdir -p .state/latest/management-access
# 使用全新目录，避免与旧 export 混合。
mkdir .state/latest/management-access/base
git archive "$DEPLOYED" gitops | tar -x -C .state/latest/management-access/base
go run ./cmd/atlas-platform management-access \
  --deployment-dir .state/latest/management-access/base \
  --https-port 18443 \
  --output .state/latest/management-access/review
```

输出目录必须不存在；它是开发审查材料，不是执行授权或不可变运行证据。

- `01-prerequisites/`：local-pki 五个叶子 Certificate；platform-project HTTPRoute 权限。
- `02-routes/`：edge 路由、listeners、网络策略；monitoring 外部 URL/secure cookie。
- `summary.json`：输入与阶段文件摘要、后端清单；无凭据。

r3 首次运行已发布两阶段，但新增 HTTPRoute 默认 wave 0 先于 Gateway wave 2，
出现 `NoMatchingParent` 并阻止 Gateway 更新。原 STOP 与提交绑定保留；当前修正
将十条新路由固定为 wave 3，Gateway 保持 wave 2。修正尚未发布，不能宣称运行时 PASS。

两阶段必须分开发布：第一阶段证书当前 generation Ready、权限已生效且 tracking/SSA
正确后，才能发布第二阶段。仅改变候选列出的四个文件；不更换任何 Application
source、owner、Root、Seed、controller、存储/凭据。各阶段用 exact-parent/lease
拒绝并发修改；不能把两阶段 diff 一次推送。第二阶段内部保持 NetworkPolicy wave 0 → Gateway wave 2 → HTTPRoute wave 3；
渲染器拒绝 Gateway wave 漂移，避免路由健康状态反向阻塞 listener 创建。
Grafana 会按既有 Recreate 策略重启，
Prometheus/Alertmanager externalUrl 更新也可能触发 Operator 滚动重启。

编译器拒绝不兼容后端/CRD、已存在的 management listeners、identity 冲突或权限不足。
重复渲染同一输入逐字节一致；它不是再次应用后的通用更新/卸载入口。

## 必须先审查的兼容性影响

**当前旧业务工作区不能自动接纳平台维护后的新 Git HEAD。** 发布后，其 status/open/
deploy 会拒绝与历史 receipt 不一致的 remote revision；运行中的业务和旧证据仍保留。
本轮不提供 rebase，不更改旧 final/plan/D1 FullCommit，不削弱 exact-parent 校验。
必须在部署决定中明确接受这个使用限制，或先完成独立的工作区兼容性变更。

## 部署后的浏览器准备（本轮未执行）

管理员审核域名与 CA 摘要后，可将以下记录加入本机 hosts，并信任该实例现有
`development-ca.crt`。这是单独的主机设置，不是服务转发；不要关闭 TLS 验证。

```text
127.0.0.1 grafana.atlas.test argocd.atlas.test prometheus.atlas.test alertmanager.atlas.test s3.atlas.test
```

无需 hosts/系统 trust 的 HTTPS 验证可以使用 curl 的 `--resolve` 和 `--cacert`，例如：

```sh
curl --resolve grafana.atlas.test:18443:127.0.0.1 \
  --cacert /Users/nekoreb/Atlas/app-r3/state/development-ca.crt \
  https://grafana.atlas.test:18443/api/health
```

登录使用已有账户；不要把 Secret 明文写进 Git、命令行参数、聊天或日志。此变更不
读取密码、不轮换账户；Argo CLI 原生 gRPC 并非本轮保证，浏览器与 HTTP API 为目标。
S3 客户端直接配置 HTTPS endpoint，不能依赖 HTTP redirect 重签 SigV4 请求。

## 验收要求

- Gate 1：精确 Cluster UID/Git parent；五份当前 generation Ready 证书、现有 Issuer；
  platform-project 仅增加 HTTPRoute；对象 owner/tracking/SSA 正确。
- Gate 2：Gateway 14 个 listeners（r3 原有 4 + 新 10），当前 generation Accepted/
  Programmed；10 条 route Accepted/ResolvedRefs；五份证书分别覆盖正确 DNS。
- HTTPS：Grafana/Argo 登录页，Prometheus/Alertmanager 页面/API，未签名 S3 请求返回
  AccessDenied 而非允许匿名列桶。登录/签名验证要明确批准既有凭据的使用。
- Prometheus/Alertmanager POST/PUT/PATCH/DELETE/OPTIONS 不得匹配 route；负例用无
  管理副作用的未知路径，确认无后端透传。浏览器只读功能不得依赖未允许的方法。
- 原 Web/归档服务 HTTPS readiness、Ready Pods、PVC/UID/ownership 无回归；不重复
  历史 POST/S3 probe，不伪造新 Runtime PASS。
- 不因 Pending 立即修改参数；仅有界只读等待。超时、未知写结果、漂移停止；没有
  自动 rollback、清锁、直接 patch 或默认重建集群。
