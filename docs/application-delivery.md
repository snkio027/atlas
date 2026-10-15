# 接入自己的 Web/API 应用

当前是单所有者、macOS arm64 + OrbStack 的本地开发平台。普通业务入口是
`atlas-platform app`；安装仍使用 [D1 首装](first-install.md)。工作区需要一个已完成
的 D1 实例，且该部署分支尚未创建别的业务 Project。已有 Project 只支持原 Workload
的更新；不能把 r7 的 demo 直接变成另一个业务或往里追加项目。

本使用层正在审查，设计见 [ADR-0018](adr/0018-ordinary-application-delivery.md)。
本地验证与真实业务验收分别记录；S2 历史 Runtime PASS 不代表普通路径已实测。

## 应用运行契约

- linux/arm64 固定摘要镜像，单一非 root 程序；只读根文件系统，`/tmp` 上限 32 MiB。
- 监听 `PORT`（模板 8080），提供 `/healthz`、`/readyz`。声明 metrics 时提供 `/metrics`。
- CPU/内存 requests 与 limits；每个服务预留一个滚动更新 Pod 的 quota。
- HTTPS 名称为 `<name>.atlas.test`；当前一个 Project/namespace、一份同版本 OCI 制品。
- 可选 object-storage Binding 提供 `ATLAS_S3_ENDPOINT`、`ATLAS_S3_BUCKET`、
  `ATLAS_S3_REGION`、`ATLAS_S3_FORCE_PATH_STYLE` 与 AWS 两个凭据环境变量。
  应用不写 SecretRef、NetworkPolicy 或 provider 实现字段。
- 无 Binding 也能部署。不需要 unbound 陪测、`/roundtrip` 或平台专用 probe 子命令。
- 目前不支持增加/移除已发布 Workload、改名、改变 hostname/port、解绑、凭据轮换、
  自动恢复或重要数据的灾备保证。

## 第一次接入

P0 仍从经过审核的 Atlas 源码构建 `bin/atlas-platform`，Go/Task 仅为开发工具。
该二进制执行发布时要求 clean commit。匹配产品源码作为编译资源保留；完整业务
发行包属于下一批，不影响 D1 已有的无源码安装路径。

应用示例是独立仓库 experiment-archive，业务为上传文件和 JSON 描述、查询、
下载及 SHA-256 校验。其默认启动方式已符合上述契约。

先在应用仓库构建自己的二进制，再在 Atlas 仓库打包（所有路径换成自己的）：

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /absolute/build/archive ./cmd/experiment-archive

/absolute/atlas-refactor/bin/atlas-platform app image \
  --binary /absolute/build/archive \
  --tag atlas.local/experiment-archive:v1 \
  --out /absolute/artifacts/archive-v1
```

离线打包确定性生成 image.oci.tar + image.json；目标目录必须新建，最多 64 MiB。
只含静态程序的 scratch 镜像没有系统 CA 包；本例使用 Binding 内网 HTTP S3。
需要额外运行文件的应用当前不适用此打包入口。编译来源由业务仓库负责记录。

初始化一次，之后无需重复指定内部路径：

```sh
bin/atlas-platform app init \
  --workspace /absolute/workspaces/archive \
  --instance /absolute/instances/archive-dev \
  --product-source /absolute/atlas-refactor \
  --artifact /absolute/artifacts/archive-v1/image.json \
  --owner your-name --project archive --name experiment-archive --s3
```

instance 目录需要 installation.yaml，已核验安装包的 runtime.json 可位于目录本身
（公开 D1 首装文档的默认布局）或 package/ 子目录；同时存在两份则拒绝歧义。
init 创建新工作区，不复制旧实例状态、不创建集群、不使用明文凭据。

进入工作区，查看或编辑 platform/projects、platform/workloads、platform/bindings
中的 YAML。不要手改生成的 GitOps 清单：

```sh
cd /absolute/workspaces/archive
/absolute/atlas-refactor/bin/atlas-platform app check
/absolute/atlas-refactor/bin/atlas-platform app plan
/absolute/atlas-refactor/bin/atlas-platform app deploy
```

check 校验类型、引用、预算与离线制品；plan 核对远端 parent/发布权限及完整 consumer
静态前置检查。plan 对远端只读，写本地私有计划。deploy 展示精确目标、资源/权限变化、
公开密文目标后，要求输入显示的 `deploy <cluster>/<project>` 确认语句。
确认后绑定摘要、重读输入；任何目标或输入变化拒绝。正常步骤在一个批准范围内执行。

首次发布依次完成 permissions → project（含证书）→ infrastructure → consumer；
每一步等待其受保护的只读 Gate。不会让使用者手动触发单独阶段。
部署完成显示 DEPLOYED 与 functional=UNPROVEN：平台就绪，业务测试尚需由应用执行。

## 更新两次，不改平台源码

1. 修改业务，构建新二进制，使用新版本 tag 和新目录运行 app image。
2. 在原工作区运行 `app select-image --artifact /absolute/artifacts/archive-v2/image.json`。
   它仅更新 authored 镜像和登记制品；原凭据与旧计划保留。
3. `app check → app plan → app deploy`，审查旧/新镜像、replicas、resources 和权限。
4. 下一次正常更新同样操作。副本或预算调整只编辑原 Workload YAML，然后重新计划。

consumer-only 更新保留身份/TLS/Binding，使用已登记独立凭据，不要求重新安装或 probe。
重复已成功的 deploy 只观察；不要为了查看状态重新生成计划。
未完成或 STOP 的 attempt 不能以新 plan 覆盖；按下表诊断。

## 状态、访问与排障

```sh
atlas-platform app status
atlas-platform app status --json
atlas-platform app open --service experiment-archive
atlas-platform app logs --service experiment-archive
```

这些命令使用记录的意图，而不是未发布的编辑；无需填写 phase 或 revision。
status 分别显示当前 authority、发布/就绪、历史结果、服务名和凭据位置（不输出凭据）。
JSON stdout 只含 JSON，进度/错误走 stderr。业务日志由显式 logs 输出，应用自身必须
遵守不记录凭据的约束；当前读取一个匹配 Pod 最近 100 行，无日志平台。

open 前台显示实际 localhost 地址、目标实例及 TLS 模式，Ctrl-C 关闭。
`--port 8090` 固定本地端口；被占用就失败，默认 0 明确分配随机端口。
本地浏览器访问 HTTP；代理核验上游 HTTPS 的实例 CA。不会导入系统 CA 或修改 hosts。
访问连接持有共享锁，执行部署前先关闭。

在应用接口制造可控 400（非法描述）或 404（不存在的记录），利用返回状态、logs、
status 定位业务层问题。平台命令不会替你执行上传/删除或宣称业务已通过。

| 状况 | 安全下一步 |
| --- | --- |
| authored 字段/预算或制品错误 | 修改报错文件，重新 check/plan；不需要重装 |
| 等待当前证书或应用就绪 | 在原只读预算内等待，进度变化时输出原因；无写入重试 |
| Git intent 没有 receipt | outcome unknown，保留现场，只读核对 Git/receipt |
| 部分发布、STOP、身份/owner 违例 | status/logs；不清 STOP、不重发 publication、不生成新凭据 |
| 已成功部署 | status/open、自己的业务测试；功能历史不随当前 Ready 自动更新 |

工作区 app.yaml 和 .atlas/ 由 .gitignore 排除：含实例路径、计划、凭据与 authority
evidence，不能作为模板分享。只把 authored platform/ 提交到业务仓库。
不提供 cache clean，私钥/备份/证据不自动清理。

## 自动化

```sh
atlas-platform app plan --json > review.json
atlas-platform app deploy \
  --plan /absolute/workspaces/archive/.atlas/run/plan.json \
  --approve-plan <reviewed-plan-sha256> --json
```

plan JSON 含 plan 与 impact（完整摘要在 impact.planSHA256）。自动化提供的是精确
保存的 plan.json；不提供无条件 --yes。人只需在自动化边界批准具体影响一次。
正常部署不获得安装、恢复、旧集群清理或新增实例的授权。

## 验证状态

本批使用真实 experiment-archive 静态二进制进行了离线打包、工作区生成与类型校验；
普通路径首次部署、两次更新、业务功能和隔日回访仍待独立目标的实际验证。
不可用已有 S2 r7 验收填补这些结果。详细开发检查见[本地验证记录](application-delivery-validation.md)。
