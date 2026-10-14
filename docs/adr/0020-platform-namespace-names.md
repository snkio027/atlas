# ADR-0020：平台 namespace 使用职责或组件名称

Status: Proposed — 2026-10-14。用户已批准命名方向；本提交实现新安装基线，
未执行现有实例迁移，也不批准删除集群、迁移数据或重新发布凭据。

## 决定

新安装采用 gateway、monitoring、sealed-secrets、object-storage，分别替代
atlas-gateway、atlas-monitoring、atlas-secrets、atlas-storage。保留 argocd、
cert-manager、envoy-gateway-system、local-path-storage、workload-web 及业务 Project
名称。Namespace 不编码 Atlas 产品名、版本或验收轮次。

Helm release/resource 名、StorageClass、AppProject canonical 名、Application owner、
域名、端口和控制图保持既有含义。atlas-monitoring 仍是监测 chart 的 release/instance
标签，不是 namespace。平台归属由 GitOps owner 与受控标签表达；名称不授予权限。

数据库、数据处理等新增设施按实际权限、配额和数据生命周期选 namespace：专用
controller 使用组件名，共同运维的服务使用明确职责名；不将数据库、CSI、备份统一
塞入 object-storage。本次不增加这些组件，也不扩展业务工作负载类型。

## 编译与凭据边界

新名称统一用于清单、RBAC subjects、namespace selectors、Gateway references、
Service DNS、监测发现、SealedSecret 目标、安装器与业务工具。业务 Project 拒绝内置
平台保留名；编译器还检查 base 声明的 Namespace 和精确 AppProject destinations，
避免后续新增组件必须靠修改字符串前缀才能防止命名冲突。

旧 SealedSecret 密文严格绑定旧 namespace/name，不能只修改 metadata 后复用。
从新源模板移除实例密文；原版本仍保存在 Git 历史，原实例保持不动。离线 render/check
与 D1 产品构建验证模板及权限投影，不把旧实例密文当成构建前提；select 和正常
Bootstrap activation 继续要求完整密文。D1 仍在实例私钥备份后生成新实例密文，
消费者发布前仍验证凭据就绪，不增加明文 Secret 发布权限。

## 历史与升级

S1/OT-1 固定输入、历史 fixtures、bootstrap snapshots、验收报告不改名。
离线 OT-1 检查继续将 inventory/fixtures 与原 immutable Git SHA 精确比较，
不再要求当前安装模板逐字节等于历史布局；当前模板由 platform:check 单独验证。
冻结实验明确使用其旧 Gateway namespace，不能把新运行解释成旧证据。
新安装工具拒绝与其 namespace 布局不匹配的旧产品包；不自动推断迁移或恢复。
现有实例继续使用其绑定版本。r3 的 namespace、PVC、Trust Root、密文和业务证据
均不因本源码变更而改变。

现有 namespace 迁移不是就地改名：需要数据备份/恢复、目标 namespace 的新密文、
DNS/路由切换及旧资源退役计划；每一步按精确目标审查。此次不提供通用迁移器。

## 验证

验证新完整渲染、namespace/reference/RBAC/selector 一致性、监测 release 名保留、
无旧实例密文、新名称与扩展平台名拒绝被 Project 占用，以及旧产品布局拒绝。
运行真实 Helm/Kustomize 与 task quality；历史 OT-1 契约继续按原名称验证。
本地 PASS 不代表新 namespace 已在真实集群验收。
