# 平台能力扩展与首批监测 / S3

当前源模板采用 [无产品前缀的 namespace](platform-namespaces.md)，不包含实例密文。
新增或启用组件先审查 catalog、权限和依赖；实际安装仍使用 D1 实例流程。
能力投影设计见 [ADR-0006](adr/0006-declarative-platform-capabilities.md) 与
[ADR-0007](adr/0007-platform-contract-hardening.md)。

历史 dev02 启用记录及下文历史命令保持当时 namespace/提交绑定，
不能直接用于当前新安装或旧实例迁移。

## 日常入口

```sh
cd /Users/nekoreb/Workspace/03_Projects/atlas-refactor
export PATH="$PWD/.state/tools:$PATH"

# 只读本地目录，显示依赖闭包、wave、缺少的加密凭据和 AppProject 权限增量
task platform:plan
task platform:plan CAPABILITIES=object-storage

# 前置条件满足后，生成本地启用清单和 GitOps diff；不会 push 或 apply
task platform:select CAPABILITIES=monitoring,object-storage,storage-monitoring
ATLAS_TEST_HELM="$PWD/.state/tools/helm" \
ATLAS_TEST_KUBESEAL="$PWD/.state/tools/kubeseal" task quality
git diff --stat
```

当前源模板不含 SealedSecret 密文。新集群必须先建立自己的 Trust Root，完成
备份后按实例 namespace/name 封装；不能复制 dev02/r3 密文或只修改其 metadata。
离线 `render` / `check` 与 D1 产品构建验证模板和权限投影，不要求实例凭据。
此时 `plan` 明确报告缺失的 5 个键，`readyToEnable=false`；`select` 和正常 Bootstrap
activation 仍拒绝缺失凭据。模板检查通过不代表可以直接发布。
`readyToEnable` 表示静态凭据要求齐备且选择未缩减；不能替代控制器实际解密与运行时健康检查。
`select` 比较依赖闭包，只允许保持或增加能力；退役在运行工具或写文件前拒绝。
旧生成投影也参与比较，手改 enabled.json 后 render 不能隐式移除 Application。
其他验证失败不会更新选择和控制投影，但可能已刷新候选渲染文件；文件写入不是跨文件事务。
所有命令只操作本地文件，具体退役流程尚未实现。

配置入口是 `platform/development/capabilities/catalog.json` 和 `enabled.json`。
一个组件声明 permissionDomain、路径、目标 namespace、readiness wave、依赖和 Secret 引用；依赖自动展开。
security / observability / storage 目前都映射到 platform-project，仅用于分类与审核，
不构成域间强隔离。AppProject 的 destination × kind 仍是共享权限并集。
Helm jobs 声明本地 chart、values 和资源分组，镜像在扩展锁文件中固定 digest。
新普通叶子不用修改 Go Bootstrap，不用手工把数量从 15 改为另一个常数。
手写资源放在同目录的 `resources/<name>.json`，Helm values 放在 `values/`。
生成的 GitOps 文件应通过 render 更新，不手工编辑。

Application 和精确 AppProject 类型/namespace 增量由目录生成，需在 diff 中审查。
组件叶子禁止创建 Application / ApplicationSet / AppProject / Secret；没有第四层控制树。
旧 workload-project 保持原边界，业务 Secret 由平台的 Sealed Secrets 控制器物化。
发布仍沿用 GitOps 审核流程；推送当前被 Argo 监听的分支会实际部署，不能当成纯代码备份。

## 已实现能力

| 能力 | 实现及用途 | 调度 / 持久化 |
| --- | --- | --- |
| 指标 | kube-prometheus-stack 92.3.0；Prometheus、kube-state-metrics、node-exporter；节点、Pod、kubelet/cAdvisor、API Server、CoreDNS 指标 | Operator/KSM 在 compute；exporter 覆盖 4 节点；Prometheus 在 data，8Gi PVC，24h / 4GB retention |
| 告警 | 上游 Kubernetes/Prometheus 规则、Alertmanager、S3 指标端点缺失与卷可用空间规则 | Alertmanager 在 data，1Gi PVC；当前 receiver 为 local-only，尚无外部通知渠道 |
| 看板 | Grafana 及上游 Kubernetes 看板；Atlas 节点、Pod、PVC、S3 抓取状态看板 | data，2Gi PVC；登录使用 SealedSecret 引用；禁用匿名登录和在线插件安装 |
| 对象存储 | SeaweedFS 4.48 mini，S3 path-style API，预建 uploads 桶；上传、下载、附件、预签名和分段上传 | data，16Gi Retain PVC；单副本；bucket 范围 Read/Write/List/Tagging 权限 |
| 凭据物化 | Sealed Secrets 0.40.0 / chart 2.20.0；基础范围为 sealed-secrets、monitoring、object-storage、workload-web；业务 Project 另经受限发布授权 | compute；不授予 argocd / kube-system Secret 写权限；自动 key renewal 关闭，变更另行审核 |

此 profile 没有 Loki、Tempo、HPA、数据库、HA、远端备份或生产发布承诺。
Kind 的 etcd、scheduler、controller-manager、kube-proxy 私有指标端点未启用抓取，避免把不可达目标
包装成覆盖完整控制面。node-exporter 观察 Kind 节点容器所在的 Linux 环境，不代表独立物理机器。
hostPID/只读 hostPath 是监测域的显式权限例外；不使用 hostNetwork/hostPort。

所有 PVC 使用 `atlas-local-retain`，删除保护不能替代备份。
local-path 的申请容量不构成目录硬配额；SeaweedFS 另外固定 128MB volume / 最多 96 volumes，
不按虚拟磁盘报告容量自动扩展，但元数据与日志仍须监测宿主机空间。

## 网络和权限

- S3 Service 只发布 8333 和 9324，均为 ClusterIP。管理、filer、master 等内部端口没有 Service。
  WebDAV、Admin UI、Iceberg、Lance、IAM 写入口关闭。mini 仍包含内部管理进程，不宣称只监听两个端口。
- `object-storage` 默认拒绝 ingress/egress。只有 `workload-web` 中标记
  `atlas.local/s3-client=true` 的 Pod 可访问 S3；其对应 egress 也由平台显式授予。
  Prometheus 按 namespace + Pod 身份访问指标。没有内部 IP/CIDR 授权。
- 监测服务的 ingress 仅允许监测 namespace 内部访问；Operator webhook 保留 API Server 访问路径。
  监测平台因抓取 node/API 指标保留出站能力；没有把这项权限扩散给 workload。
- API/kubelet ServiceMonitor 使用自动投射、轮换的 ServiceAccount token。Git 不保存长期 SA token Secret。
  Kind kubelet 自签证书沿用 chart 的 skip-verify 开发设置；API Server 验证集群 CA。
- Grafana sidecar 只读取监测 namespace 的 ConfigMap，不跨 namespace 读取 Secret。

浏览器与 S3 的常驻 HTTPS 入口见 [管理入口](management-access.md)。namespace 名不决定
外部域名或端口；入口仍需通过专门的 GitOps 变更生成和发布，不能靠新名称推断已开放。

新安装的集群内业务端点是 `http://seaweedfs.object-storage.svc.cluster.local:8333`，
region `us-east-1`、bucket `uploads`、path-style；业务通过 Binding 获得配置与自己的
Secret 引用，不应手写 provider namespace。旧实例仍使用其原端点。
预签名必须使用客户端实际访问的 host/port，不能把集群内 DNS 签名 URL 原样交给浏览器。
凭据保存在实例私有目录，不通过 CLI stdout 输出。

## 历史首次接入流程（dev02 旧布局已执行，不适用于新布局 owner 迁移）

1. 完成 ADR/代码审核，记录将部署的精确 commit SHA、目标 dev02 与权限 diff。
   Bootstrap 身份兼容修正必须先经过审核；不能仅 cherry-pick 控制清单。
2. 获得新 Sealed Secrets trust root 的明确授权后，选择 `secrets-controller`。
   依赖展开为 foundation → secrets-crds → secrets-controller，经已有 platform-control 发布。
   无需重建集群、重建 Root、重发 Seed、修改 atlas-bootstrap 或调用恢复命令。
3. 确认 controller Healthy，按原架构把私钥备份到物理隔离介质；本次 dev02 由所有者明确批准临时使用 w1
   本机备份，见启用记录，不能记为物理隔离要求已满足。
   只有公开证书传入本地封装工具，私钥不得进入本仓库、截图或日志。
4. 使用锁定 kubeseal 获取该目标 controller 公共证书，再执行：

   ```sh
   # 下面的公有证书读取属于经批准的首次凭据流程
   kubeseal --context kind-atlas-refactor-test-dev02 --controller-namespace atlas-secrets \
     --controller-name sealed-secrets --fetch-cert > .state/capabilities-public.pem
   task platform:credentials CERT="$PWD/.state/capabilities-public.pem"
   task platform:select CAPABILITIES=monitoring,object-storage,storage-monitoring
   task quality
   ```

   工具自动生成随机开发凭据，明文只写入 0600 私有文件；kubeseal 用严格 namespace/name 绑定。
   相同证书、凭据和输出有匹配 receipt 时重复执行零改动。已有密文没有对应 receipt 或证书变化时，
   拒绝自动覆盖；凭据轮换仍是独立操作。封装工具不会联网下载依赖或创建 Kubernetes Secret。
5. 审核并发布第二次 Git 变更。SealedSecret 同步并成功物化后才推进消费服务；Prometheus CRDs
   → Operator → 服务的就绪顺序由既有 Argo Application health/wave 控制。
   先用 atlas-artifacts 准备锁定镜像，运行期沿用既定离线输入，不让 Bootstrap 在线拉取替代品。
6. 真实验收：全部新增 App 当前 SHA Synced/Healthy；Secret 实际物化；PVC Bound/Retain；
   Prometheus targets/rules、测试告警 firing/resolved、Grafana 登录/看板、签名 S3 CRUD/预签名/分段上传；
   无权限 Pod 与匿名访问拒绝；重启服务后的数据；Root/Identity/Signal/Receipt UID 不变；重复 apply 零写入。
   当前实测范围见启用记录；其中 Kubernetes 存储重启恢复、完整 Prometheus 规则触发链路
   和外部通知仍未验证，不以容器测试或 Alertmanager API 注入替代。

首次 trust root 有两个发布阶段，后续已建立依赖的普通组件扩展只需一次选择和审核发布。
不自动创建外部通知渠道、不自动发送邮件/消息，也不自动导出已有信任根。

## 本地验证记录

- `task quality` 包含 race tests、go vet、实际 Helm 渲染、Kustomize 与 CRD 结构子集验证。
- 测试验证新增能力不改变原 Signal、不恢复 Seed 权限。F13/ADR-0011 后，schema 3 的
  完整交接保持 ADOPTED；扩展是否收敛由独立 rollout/runtime 验证，不能以 ADOPTED 代替验收。
- 真实 kubeseal 离线测试验证严格绑定、无明文泄漏、0600 文件与重复运行不轮换。仅使用一次性测试证书，未部署信任根。
- 隔离 Docker 容器验证锁定 SeaweedFS 与清单参数的签名 CRUD、预签名、分段上传、匿名/跨桶 403、重启保留数据。
  初次重启探测因 Docker 动态端口改变而失败；修正测试器重新查询端口后通过，未修改存储数据掩盖结果。

本轮已完成实际四节点调度、NetworkPolicy 正/负向探测、GitOps 收敛及服务 API 验收。
完整结果见 [运行证据](evidence/platform-dev02-enabled-20260927.json)。这不是完整 admission/CEL、
HA、离线恢复或生产就绪证明。后续基础能力优先级见 [组件评估](platform-next-components.md)。

## 上游依据

- [kube-prometheus-stack chart](https://github.com/prometheus-community/helm-charts/tree/kube-prometheus-stack-92.3.0/charts/kube-prometheus-stack)
- [Prometheus Operator compatibility](https://prometheus-operator.dev/docs/getting-started/compatibility/)
- [SeaweedFS 4.47](https://github.com/seaweedfs/seaweedfs/releases/tag/4.47)
- [Sealed Secrets 0.40.0](https://github.com/bitnami/sealed-secrets/releases/tag/v0.40.0)
