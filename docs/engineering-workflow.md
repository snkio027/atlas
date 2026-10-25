# Atlas 工程工作流

采用目标为 **PR-full / standard / v1**，参考固定的 Daedalus Git/GitHub Workflow v1.1。
完整参考文件 SHA-256：`de77bfd55da3811ed553985db8d61938467a2a3b2c0215e225b607428b667a86`。
适用决策、单维护者例外和部署分支例外见 [ADR-0025](adr/0025-usage-driven-engineering-workflow.md)。
**E1 设计已获所有者批准；当前实现仍为接入候选，最终合并必须交所有者确认。**
GitHub 迁移、候选验证及合并后 main 验证各自留证，不能用设计批准代替运行结果。

日常流程见 [贡献指南](../CONTRIBUTING.md)。优先从使用反馈选择下一改进，不预建未被需求证明的框架。

## 检查入口及覆盖

| 入口 | 实际职责 |
| --- | --- |
| `task quality:fast` | Go格式/vet、锁定actionlint工作流检查、计划与门禁回归；PR标题规范在CI中检查 |
| `CI_PLAN_PATH=... task quality:plan` | 生成JSON计划，记录作者变更范围和当前候选，所有路径保守Full，不执行测试 |
| `task quality:full` / `task quality` | Fast覆盖 + race测试 + 真实Helm/Kustomize与平台校验 + OT-1契约 + 实验入口 + 数据契约 + 包装检查 + Web OCI一致性 |

本地证据位于 `.state/quality/<run>/`；不会访问运行集群、准备凭据或下载依赖。
Go1.27.2、macOS arm64及锁定工具沿用仓库要求。CI准备工具与运行时下载严格分开；
Draft准备器仅下载并校验Task与actionlint，Full准备器校验完整工具集。每个检查保存日志与耗时。单个命令预算40分钟，CI验证作业总预算45分钟；
Go测试仍保留每包25分钟限制。预算包含冷编译与包调度，不能用较短的全局预算误截正常Full。

本地可包含未提交修改，但报告明确标记并绑定实际文件摘要。计划不能跨源修改、base、环境、策略或验证器复用。
计划缺失/非法拒绝执行；读取历史失败可生成明确Full回退，不能解释为空检查。
没有 Affected 入口、缓存命中绿灯、Nightly替代覆盖或“仅改文档就不跑Full”的隐含路径。

## GitHub 阶段

| 事件 | Fast | Plan/Full | 聚合结论 |
| --- | --- | --- | --- |
| Draft及退回Draft | 执行 | 不执行 | 明确非成功，不准入 |
| Ready / 更新 / 编辑 | 执行 | Plan后只执行一次Full | 报告完整、候选仍为当前Ready且所有必需项通过才成功 |
| main push | 执行 | 完整Full | 记录集成后事实，不能替代合并前验证 |
| merge-group | 执行 | 独立Full | 保留诊断/迁移兼容；当前没有启用队列 |

Required context 为 `Delivery / Quality / pr-full-standard-v1`，实际生产者与规则绑定以迁移报告为准。
PR按 workflow/event/PR 分组取消过时运行；merge-group与main按run ID隔离。
描述编辑目前保守重跑；记录成本后再决定是否分流。重跑旧run不保证候选最新，聚合必须重读当前PR及main实际Git ref；PR内base SHA可能仍是旧快照。

报告包括实际test SHA/tree、head/base、环境、策略与验证器摘要、run/attempt、每项结果和耗时；
CI artifact默认保留30天。采用/发布/失败处置的长期摘要另记仓库，必要的完整私有证据继续保留。
不能依赖临时artifact永久存在，也不能以最后一次绿灯掩盖同候选未处理失败。
标准级依赖可信维护者审查策略和测试含义；GitHub Actions App绑定不是独立workflow来源认证。

## 主线与权限

目标约束：PR-only、Squash-only、线性历史、完整检查、最新base、讨论解决、禁止force/delete、无日常bypass。
CODEOWNERS涵盖CI、质量计划、工具准备、Taskfile、ADR与贡献规则。当前单维护者限制由ADR E1显式处理，
required approval 为 0、关闭 auto-merge；不能声称已经强制独立批准。
代理准备候选与证据，所有者作最终合并决定；此职责约定尚无独立身份隔离。业务部署、集群恢复和清理继续使用各自精确授权；源码合并不操作实例。

失败必须记录原因、影响和处理决定；代码修复后验证新候选。基础设施瞬态失败也需解释与证据，
不自动重跑、静默隔离测试或放宽Required Check。例外须有范围、责任和补偿措施。

## 采用与测量

远端基线：2026-10-25读取时没有ruleset、main未保护、没有开放PR；仅一名admin维护者。
允许merge/rebase/squash三种合并，auto-merge关闭，源码CI每次PR运行全部检查。
最近四个Quality run（40090352313、40095746656、40099718247、40102232018）的创建至结束约30–37分钟；
这是不同候选的历史观察，不是受控前后性能比较，也不是runner计费分钟。

已观察事实和失败处置见 [迁移记录](engineering-workflow-migration.md)。最终候选仍须逐项核对：

- 本地Fast/Full、计划与失败回归的实际结果和输入绑定。
- Draft Fast与非成功聚合；无新commit转Ready产生Plan/Full。
- Ready更新、退回Draft与取消、错误计划/必需执行失败/报告缺失的实际GitHub行为。
- 新context的生产者、实际check名称、当前head/base/test和artifact完整性。
- 主线规则实际生效、禁止未通过候选集成、无绕过；策略候选合并后的main验证。
- 单维护者例外接受记录；实际使用反馈入口可用。

以后按Draft/Ready/main/release分别比较排队、准备、检查、上传、取消/重跑成本和使用中的逃逸缺陷。
一次有效变更少等待但不减覆盖才算收益；不为达到某个耗时数字削减必需验证。

依据：[GitHub事件及受测SHA](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows)、
[Required Check与skipped语义](https://docs.github.com/en/pull-requests/how-tos/merge-and-close-pull-requests/troubleshooting-required-status-checks)、
[并发与取消](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/control-workflow-concurrency)、
[Merge Queue适用范围](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)。
