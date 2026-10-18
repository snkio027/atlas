# Go Bootstrap Cutover Contract

- Version: 0.1 / 2026-09-27
- Status: **Proposed; CUTOVER BLOCKED; rehearsal NOT RUN**
- Repository role: **successor candidate**
- Runtime authorization: **NONE**
- Decision owners: Atlas repository owner and required CODEOWNERS

本契约定义从 Shell Bootstrap 切换到 Go Bootstrap 的发布条件与操作边界。
下文的“必须”是拟议的迁移验收条件，不修改原 Atlas 的现行权威，也不是运行手册授权。
当前交付是决策文档、静态 parity audit 和历史证据绑定；没有执行目标发现、凭据操作、
集群写入或切换演练。`0ef9188` 是可行性证据基线，不是可直接接管原 Atlas 的版本。

## 依据、基线与决策分离

原 Atlas 的规范顺序仍是 Architecture v1.0.2、GitOps v1.0.3、Network v1.0、
Accepted ADR，再到实现。此次审计使用原仓库远端的干净提交
`aca4ff137a1d254cfeceaec24526e0699b585e92`，包含 Accepted ADR-0001 至 ADR-0005；
没有把较旧且有本地修改的 checkout 当作基线。具体引用与差异见
[behavioral parity audit](bootstrap-behavioral-parity-audit.md)。

| 记录 | 固定身份 | 含义 |
| --- | --- | --- |
| 已验证 Go 实现 | `0ef918801dac84ac913416f4c1756bf08045a485` / `integration-20260927-04` | 独立可丢弃环境的 Bootstrap 闭环 |
| 第四轮报告提交 | `9df41568bafcaa98d6548d2089766d83081c3374` | 绑定实现、工具、渲染、集群身份、退出码和写入审计 |
| 原 Shell 审计快照 | `aca4ff137a1d254cfeceaec24526e0699b585e92` | 静态比较对象；不代表已运行迁移演练 |
| 将来的 cutover candidate | **UNSET** | 必须另行锁定，不能借用第四轮 PASS |

[冻结清单](evidence/bootstrap-baseline-20260927.json)新增绑定报告全文、脱敏证据、
历史报告的 SHA-256，并复制第四轮工具和渲染摘要。原有报告、JSON、实现 tag 不改写。
后续发现用新记录说明，并引用旧 SHA；不得重打 tag 或把新结果写进第四轮。
Git 提交与内容摘要提供可校验身份，不等于签名发布、独立备份或永久存储保证。
原始私有证据由所有者保管，备份和访问仍须保护凭据；不得把 `.state` 整体提交。

[新 ADR-0002](adr/0002-go-default-control-plane-language.md)只提议默认语言选择；
[新 ADR-0003](adr/0003-gated-bootstrap-successor-cutover.md)提议迁移 Gate。
两者均为 Proposed。即使将来接受语言 ADR，也不会自动批准替换现有 Bootstrap。
原 Atlas ADR-0001 仍要求在**原权威仓库**接受 superseding ADR；本仓库不能自行覆盖它。

## 不变量与 authority scope

scope 以实际集群实例及对象集合界定，排他判断绑定 API endpoint、CA 摘要和
`kube-system` UID。计划另行绑定仓库、环境、Identity UID、Root UID 与经批准的
Identity successor 转换；后者逐步记录到 journal，不另开一个可并行写入的 scope。
所有目标事实必须按适用流程重新取得并验证，历史 UID 不能代替现场身份。更换仓库、
profile、工作树、进程或 Identity schema 都不能把同一目标拆成两个 authority scope。

同一 scope 在任意时刻：**可执行正常 Bootstrap Tier-0 mutation 的引擎数量 ≤ 1**。
切换隔离窗口允许为零；GitOps 接管后两个引擎的正常 Seed/Tier-0 写权限都为零。
引擎选择与生命周期权限是两个维度，不能把“Go 已启用”解释为“Go 可重新 Seed”。

| 阶段 | Shell 正常路径 | Go 正常路径 | 专用 migration/recovery |
| --- | --- | --- | --- |
| Shell 被选用，切换未开始 | 仅原有生命周期允许的动作 | 禁止目标写入 | 仅既有独立 Gate |
| 隔离窗口 | 禁止目标写入 | 禁止目标写入 | 两个正常引擎都被隔离后，按独立计划执行 |
| Go 被选用，身份已兼容 | 禁止目标写入 | 仅已接受的状态表允许的动作 | 不与正常写入并发 |
| 已 ADOPTED | 禁止 Seed/Tier-0 写入 | 禁止 Seed/Tier-0 写入 | 降级只进入独立 recovery 流程 |
| 身份、Fence、API 结果不确定 | 禁止写入 | 禁止写入 | 保持隔离，按授权 checkpoint 处置 |

Recovery 不是第二个常驻 Bootstrap 引擎。它保留 ADR-0003 的独立认证、session、
Operation Fence、审计和 Human Gate；主入口不得调用它来让正常 apply“自动成功”。
ADR-0004 的 principal 规则、ADR-0005 的目标 materialization 与独立 Gate 均继续适用。
PERSONAL_LOCAL 的定义完成不能当作 live readiness，更不能升级为 production 证明。

保持以下架构不变量：Git 定义、Bootstrap 实例化、Argo 持续调谐、Operator 运行时调谐；
External Root 不成为子 Application、不加级联 finalizer、正常路径不覆盖 Root drift；
Two-Level DAG、规范 AppProject、namespace 与 trust tier 不因语言改变；Helm 只渲染；
离线制品先验证；未知读取不能当作不存在；恢复与正常路径分离；网络与数据面边界不变。

## 1. Preconditions

正式目标切换要求以下 Gate 全部具备证据。任一必需 Gate 为 BLOCKED、UNSET、过期
或不可验证，结果为 **NO_GO**。所有者承担决策与 runtime Gate；CODEOWNERS 审查治理、Tier-0 和
供应链变更；实现维护者提交证据。人名、证据 SHA 和有效期必须进入目标记录。

| Gate | 必须满足 | 当前结果 |
| --- | --- | --- |
| G0 历史冻结 | 校验实现、报告、工具、渲染及私有证据索引绑定 | 已绑定；范围仅第四轮 disposable PASS |
| G1 决策 | 默认语言决定；原仓库 superseding ADR；配置/状态/authority 差异的明确决议 | PENDING |
| G2 外部契约 | 完成 P01–P12 parity 验收；不兼容项须有版本化契约及调用方迁移 | BLOCKED，见审计 |
| G3 供应链 | 审核 compiler/module/tool/image 与 fallback binary；离线构建和运行证据 | PENDING，版本/本机 hash 不是完整发布证明 |
| G4 迁移能力 | 已实现并验证状态迁移、服务端保护、跨执行器 Fence、独立 recovery、降级阻断 | BLOCKED，当前原型未实现 |
| G5 演练 | 从 Shell 管理的既有目标完成切换、合法 rollback、禁止 downgrade 的故障演练 | NOT RUN |
| G6 目标授权 | 精确 candidate、旧引擎、目标、计划 hash、允许写入、checkpoint、回退与保留期限的 Human Gate | UNSET；既有四轮批准不可复用 |

演练与正式切换必须明确区分：首次 **REHEARSAL** 的入口要求 G0–G4 已完成，以及
仅覆盖该 disposable 演练的 G6；G5 以 NOT RUN 入场，由本次演练产生结果，不要求
“先完成自己”。之后的 **TARGET_CUTOVER** 才要求 G5 已通过，并证明其 candidate、
profile 和迁移步骤仍适用于目标，再取得新的目标 G6。两种 mode 都不能继承第四轮批准。
G4 的验证指独立能力与组件验证，不能预先声称已经完成 G5 的整套迁移演练。

### G1/G4：先解决规范语义，不能以测试覆盖规范

原 Atlas ADR-0002 把 Receipt 成功创建作为 adoption 的线性化点；有效的 protected
Signal 一出现便拒绝 Seed，并提交 Receipt，**不等待 Application health**。
第四轮 Go 在 Root 前写 latch，并等待四个 Application 的健康、精确 revision 与
39 项接管证据后才提交 Receipt。这是明确的语义差异，不能以“更严格”判为等价。

本契约的默认迁移目标是遵守原 Accepted ADR-0002，将 authority proof 与 readiness
报告分开；当前 Go 的健康/ownership 检查可作为独立验收证据。若要采用不同模型，
必须先在原权威仓库接受相应 amendment，并重做受影响的审计与演练。本文不决定该 amendment。

原 Shell 的现行代码仍用 self 存在/健康启发式；ADR-0002 的 Identity v2、Signal、
Receipt 与完整保护并未因 ADR Accepted 而自动实现。迁移不得复制这个已知运行时缺口。
遵守 ADR-0002 rollout：Phase 0 admission escape、Phase 1 protection、Phase 2 完整
Seed recovery、Phase 3 每个支持环境的 legacy migration disposition，完成后才发布
receipt-aware Bootstrap。旧 Phase-0 canary 的 COMPLETE/FROZEN 不替代后续阶段。

### G2：固定兼容对象，而非“两个命令都成功”

首个候选 profile 应是一个经 Shell 建立、身份明确的 disposable 既有目标。
它用于演练原环境的状态转换，不使用第四轮 Go 新建集群冒充迁移起点。
profile 要列明 topology、Registry、Root/self/project 名称、资源 inventory、Git URL/path/ref、
chart/values/release、镜像、配置和所有调用方。不能把单节点 Go 的限制默认为原环境的新规范。

目标 Root/self/Seed 的名称、UID、GitOps ownership 默认保持。语言切换不顺带重命名
`atlas-root`、改 Helm release、切换仓库来源、升级 chart 或裁剪 workload DAG。
需改变其中任一项时单独审查、补充允许的对象转换和回退证据，不能作为差异归一化丢弃。
当前 Go 禁止原 Atlas 名称、拒绝重新绑定已有集群、没有旧 Identity 迁移器；因此不能
通过复制 kubeconfig、改 JSON 或手工伪造 latch/Receipt 来满足这个前提。

### G3：Go 的供应链准入

当前 `go.mod` 固定 Go 1.27.2，ADR-0023 引入唯一的 YAML 解析模块并绑定 vendor/许可证/摘要；
Task 禁用自动工具链和模块获取，
使用 `GOTOOLCHAIN=local`、`GOPROXY=off`、`GOSUMDB=off`、`-mod=vendor`。
这是可复核输入约束，不能替代以下发布 Gate：

1. 固定 Go distribution、OS/arch、来源、发布摘要及可用的签名/证明验证方式；验证整套
   compiler/toolchain 与标准库输入，而不只记录本机 `go` 可执行文件 hash。
2. 维护 toolchain、标准库、工具和镜像的漏洞审查与升级/例外期限。新增 module 须审查
   精确版本、来源、许可证、`go.sum` 及预取 vendor/cache 摘要；运行时不 fetch。
3. 在独立的制品准备阶段完成下载、验证、审查。以断开外部获取的构建环境验证离线构建；
   运行环境允许明确的 Git/API 端点，但禁止隐式拉取 tool/module/chart/image。
4. 发布记录绑定 source SHA、完整构建参数/环境、依赖清单/SBOM、binary hash 和来源证明。
   用两个干净受控构建核对可复现性；若有差异，解释、审查后定义可接受边界。
5. 每个声明支持的 runtime platform 都要执行相应验证；交叉构建不算运行证明。
   fallback binary 同样经过验证，归档可取回不等于授予它目标写权限。

## 2. Cutover

本节是待实现工作流的 checkpoint contract，**不是现成 CLI 命令**。
专用 migration 工具、目标绑定和 fencing 尚未实现，不得照本文拼接 kubectl 变更。
将来的 executable runbook 必须列出实际命令、精确 principal、预期 API 动作与失败点。

| Checkpoint | 动作与完成证据 | 下一步条件 |
| --- | --- | --- |
| C0 固定计划 | 声明 REHEARSAL 或 TARGET_CUTOVER，按上述 mode 校验 Gate；记录旧引擎/candidate/fallback 的 source 与 binary hash、目标 fingerprint、Git revision、配置、render、变更 allowlist 与证据保存位置 | 两个独立维度都有初始状态：选用 Shell；生命周期由原状态证据确定 |
| C1 停止并隔离旧执行器 | 停止 Shell 的 CI、定时任务、包装入口和在途操作；按批准的 principal/capability 计划撤销旧目标写路径；验证在途请求结束、拒绝探针及 API audit | 不能只改 PATH 或拿本地 lock 当完成；无法确认隔离则停止 |
| C2 零正常写入窗口 | 两个正常引擎均无目标 mutation capability；由独立流程取得目标 Operation Fence，重新读取身份和对象 UID/resourceVersion | Fence/身份变化、不可读或不确定即保留隔离；不得自动启用 Go |
| C3 必要状态迁移 | 仅专用、独立批准的 workflow 按 Accepted ADR-0002 处理 Identity v1 → v2、protected Signal、Receipt；保留逐操作 journal | 正常 apply 不执行 migration；v2/Receipt 变化是回退边界，不能还原旧 UID |
| C4 选择 Go | 验证兼容身份、降级 fence、原引擎拒绝证据；移交/释放专用 Fence 按协议完成后，才选择并授予 Go 当前生命周期需要的最小 capability | 禁止双写或 capability 重叠；已接管目标只授予观测，不能为验证而重新 Seed |
| C5 验收 | 完成下一节所有验证和审计；记录可回退 checkpoint 与当前权限 | 未通过不得宣布接管完成；进入显式 rollback/recovery disposition |

目标发现如落在 ADR-0005 PERSONAL_LOCAL v2 流程，先单独完成 materialization Gate，
再产生 final Target/Gate；不能用旧实验批准、历史 UID 或共享 admin 凭据替代。
每个 mutation 根据 ADR-0003 验证 fence/session，隔离旧正常引擎时不能同时撤销独立 recovery escape。
同一 `kubernetes-admin` 被新旧进程共享，无法证明引擎 capability 分离；在此情况下 C1 不通过。
设计必须同时覆盖跨工作树/主机的互斥与旧凭据拒绝，当前 Go 的本地目录锁不满足要求。

仅切换 Bootstrap 可执行文件不要求暂停 Argo。若特定迁移/恢复确实需要冻结 GitOps，
只按既有规范和独立批准的方案 outside-in 冻结、inside-out 恢复，External Root 最后恢复；
不能顺手暂停 controller，或在错误路径自动解除冻结。

## 3. Verification

所有证据绑定 C0 的计划和精确 target；同时保留正向和负向结果。PASS 要求：

- CLI/config/status/exit 满足版本化外部契约，调用方迁移完成；保留 stdout、stderr、原始
  exit code、signal、deadline 与开始/结束时间，不能让管道掩盖非零退出。
- 同一输入重复 render 的摘要稳定；跨引擎比较按 GVK/namespace/name 的语义清单，
  逐项审查差异。仅可按事先批准的规则排除序列化顺序和服务端生成字段；不得忽略
  UID 变化、spec、RBAC、AppProject、镜像、tracking、finalizer 或缺少的对象。
- 既有集群切换期间 Root 成功创建次数为 **0**，Root UID/spec/外部关系不变。
  新建实例的历史“Root 恰好创建一次”证明不能套用为迁移时再创建一次。
- Git-defined inventory 被 Argo 接管，精确 Git revision、AppProject 边界、tracking/SSA
  与同步结果符合当前 controller 的语义；CRD 不以缺少 tracking annotation 自动否决，
  也不以仅有 manager 名称自动放行。检查全部目标持久对象，不能固定套用实验的 39 项。
- adoption proof 与 readiness 分别判定：健康退化不得恢复 Seed authority；有效 proof
  不被健康条件阻塞提交（除非更高权威已正式修改该语义）。Root/self/Signal 丢失、UID
  冲突、读取不可用分别按已接受状态表处理，不退回 FRESH。
- 连续两次合法 Go apply 的允许副作用有明确 allowlist。已接管目标的 Bootstrap Seed、
  AppProject、Root、Receipt 写入均为 **0**。原规范保留的 Registry substrate 行为须
  单独检查、披露并决定兼容方式，不能用 Kubernetes 零写入宣称全系统零副作用。
- Shell 的当前旧版本及 ADR-0002 指定的 receipt-unaware `783e858` 版本分别进行拒绝
  测试；先解析并记录完整 commit/binary hash。隐藏 self 后仍在 Registry/Seed/Tier-0
  mutation 前被阻断。仅测试“操作员没运行 Shell”不算旧引擎退出证明。
- 记录 API Metadata audit 的 principal、verb、对象 UID、response code、audit ID 和
  操作窗口，并记录 Docker、Registry、Git、本地文件副作用；区分授权 fixture、GitOps、
  controller 与引擎。缺失/截断的审计或无法解释的写入均不能判 PASS。

### 必须补做的 cutover rehearsal

使用新建的、单独批准的 rehearsal target，由固定 Shell 版本先建立起点。
旧有四轮集群与证据保留。C0 记录现场证明 Shell 是选用引擎及其实际 Identity/lifecycle；
按原 ADR rollout 准备保护与恢复，再运行正式迁移步骤，而非再次测 Go 从零创建集群。

| Case | 注入/执行 | 通过条件 |
| --- | --- | --- |
| R1 正常切换 | Shell 起点 → C1–C5 → 两次 Go apply | 既有 Root 不重建；符合所有 verification 条件 |
| R2 迁移前回退 | C1/C2 后、任何不可逆身份变化前中断 | 先拒绝 Go，再验证原身份和权限边界，合法恢复原引擎选择；无双写 |
| R3 状态迁移中断 | v1 删除/v2 创建之间、v2 与 Receipt 之间分别中断 | 前者 AMBIGUOUS 停止并进入独立恢复；后者仅按有效 protected Signal 续提 Receipt；不 Seed、不伪造 UID |
| R4 结果不确定 | mutation 超时、连接断开、SIGINT/TERM、执行器失联 | 重读权威对象和 journal，不能因非零退出假定请求未落地；Fence 不盲目释放 |
| R5 接管后退化 | 缺失 artifact、配置/Root drift、GitOps unhealthy、证据缺失/矛盾 | fail closed，Root 不覆盖，Seed 不恢复；修复按独立授权或合法 GitOps 路径 |
| R6 旧引擎与并发 | 当前 Shell、旧 receipt-unaware Shell；跨两个工作树/执行器竞争 | 旧正常路径被阻断，Fence 唯一持有，零未授权成功写入；不能只测单工作树目录锁 |
| R7 接管后 fallback | 切换到预审的兼容 fallback；另测不兼容旧 Shell 被拒绝 | fallback 保持当前 Identity/Receipt 和只读权限；拒绝旧 Shell 只是 downgrade 防护证据，不能冒称 rollback 已通过 |

R2 必须证明一条实际可执行的回退路径；R7 必须证明一条兼容当前状态的 fallback 路径。
若没有该 fallback，记录 NO_GO，不能用“所有回退都被拒绝”代替回退能力。
每个注入动作、恢复动作和 fixture 都在独立目标 Gate 中列明；本契约不授权执行这些测试。

## 4. Rollback

回退是引擎/发布选择的有条件转换，不是撤销 GitOps adoption。任何回退先隔离正在
退出的引擎，再检验目的版本能读取当前身份/证据且无法重新 Seed。自动失败重试不等于回退。

| 当前 checkpoint | 可选处置 | 禁止 |
| --- | --- | --- |
| C0，未改变 capability 或集群 | 取消本次迁移，保留 Shell 原有选择和生命周期限制 | 将取消描述为迁移成功 |
| C1/C2，目标资源与 Identity 未改变 | 重新确认旧状态、无 Go 在途写入、旧权限仍合规后，按批准计划恢复 Shell 选择 | 未先撤销 Go capability 就重启 Shell |
| C3，Identity 已删除或替换，Receipt 未提交 | 保留隔离；按 ADR-0002/0003 与 journal 继续向前或独立 recovery | 恢复旧 v1 schema、伪造旧 UID、把未知状态当 fresh |
| C3/C4，Receipt 已提交或 successor proof 已生效 | 使用已验证且兼容当前状态的 fallback；否则保持隔离并走独立 recovery | 重新启用 receipt-unaware Shell、删除 Receipt/latch 来“解锁”Seed |
| API/Fence/凭据状态不确定 | 保留隔离，先权威重读；新计划或新 Gate 按适用规范生成 | 猜测写入未发生、按过期本地锁清理、自动解除保护/冻结 |

回退不删除集群，不更换 Root UID，不覆盖 Git 管理资源，不恢复含旧 UID 的对象快照，
不把先前已删除的 Receipt 重新创建为原身份。完整恢复能力遵守原 ADR-0003；本文不另造
一个“通用 rollback”权限。保留 evidence、失败退出和恢复处置，不以最终 Healthy 抹掉失败记录。

## 5. Old-engine retirement

退役 Gate 在目标验收、R2/R7 演练及**事先明确的 rollback window**结束后单独签署。
目标记录必须填入时间/触发条件、兼容 fallback、责任人和所有环境 disposition；UNSET 不通过。

退出范围是旧的正常 Bootstrap mutation authority：移除自动入口和发布默认选择，
禁用或移除其目标写凭据与 RBAC，停止向 CI/操作员分发可变更目标的旧入口，并验证旧
版本在所有支持环境均被拒绝。原 `bootstrap/atlas` 的入口更换或删除只能在原仓库
superseding ADR 与审查完成后执行，不在原仓库留下长期可选的 Shell/Go 双引擎开关。

旧 source tag、已验证 binary、文档和证据作为审计档案保留；取回旧 binary 不重新授予
权限。仍承担独立 Recovery/Drill 职责的 Shell 组件按自身生命周期管理，不随正常
Bootstrap 退役而未经审查删除。Go 成为默认语言不强迫重写每个脚本或第三方 controller。

最终记录分别签署：语言决定、parity、rehearsal、目标切换、回退窗口关闭和旧引擎退役。
报告只在对应 Gate 通过后使用 READY / CUTOVER_COMPLETE / RETIRED，不把它们混成
一个“Go ADR Accepted”。当前仅技术可行性已证明，所有迁移状态仍为 NO_GO。

## 目标记录的最小字段

正式执行前必须生成独立、可审查、hash-bound 的记录，至少包括：

- record/schema version、decision、scope/profile、责任人与审查人、有效期、superseding ADR SHA；
- Shell/candidate/fallback 的完整 source SHA、binary SHA-256、构建来源及工具/module/image 清单；
- target fingerprint、endpoint/CA hash、现场 UID、初始 lifecycle、独立 materialization/Gate 引用；
- 配置、版本锁、渲染摘要、Git 源与 revision、对象 inventory 和允许的差异/副作用；
- C0–C5 每步 principal、Fence/session、允许操作、前后状态、journal 与 rollback disposition；
- P01–P12、R1–R7 的命令、时间、exit、audit、结果、恢复证据以及未覆盖范围；
- 回退窗口、退役对象、凭据 disposition、原始证据私有位置与脱敏索引 hash。

未知项写 UNSET，不填推测值；不得把历史集群 UID、审批或第四轮 PASS 复制为新目标 Gate。
