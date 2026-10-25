# v1.1 迁移记录（合并前）

范围：产品源码 `main`，PR-full / standard / v1，带所有者已批准的 E1。
设计采用见 [ADR-0025](adr/0025-usage-driven-engineering-workflow.md)。
[候选 PR #15](https://github.com/snkio027/atlas/pull/15) 必须由所有者确认最终候选后才能合并。
[失败探针 PR #16](https://github.com/snkio027/atlas/pull/16) 仅用于受控拒绝验证，绝不合并。

本记录固定已观察事实及处置，不将早期结果当成最终候选绿灯。后续 head/base/test、CI run/attempt、
实际规则与最终确认应随候选交付；源码合并后的 main 检查属于后续事实。
原始脱敏 API 快照、运行日志和 artifact 保存在本机 `.state/workflow-v11/`，不是 Git 输入。
运行日志也可通过下表链接复核；GitHub artifact 保留期为30天，不能当永久证据。

## 基线和迁移窗口

2026-10-25 读取时，main 为 `d0cb3a692b1f4934cd2d8b85ccdcb2278a3c53a9`，无保护、无 ruleset、无开放 PR，
只有 snkio027 一位维护者。原质量入口包含完整检查，PR无 Draft/Ready 分阶段。

迁移锁 ruleset `25816669` 只匹配 `refs/heads/main`，启用 update/deletion/non-fast-forward，bypass 为空。
已读取实际 branch rules 确认生效。解除条件是新门禁实测通过、生产者与规则绑定核对、当前候选经所有者确认；
未完成时保持锁定，不靠管理员 bypass 放行。实例部署分支不匹配这些规则。

已核对仓库合并设置：仅 Squash、标题来自 PR title、正文 BLANK、关闭 auto-merge。
保留 delete_branch_on_merge=false，按 E2 单独审查短期源码分支的清理。
新 required check 及 strict up-to-date 的配置结果必须保存实际 API 回读，不能只用目标 JSON 证明生效。

## 已观察的验证

| 对象 | 实际结果与证据 |
| --- | --- |
| 本地初始 Full | 9项完整检查通过，包括 race、真实渲染、OT-1、实验入口、数据契约、包装与 OCI 一致性；有未提交输入，不冒充 CI 提交证据 |
| `57273935` 本地 Full | 干净提交的完整检查通过；不证明 GitHub 可以调度工作流 |
| `57572af2` 本地 Full | 修复调度问题及加入 actionlint 后，9项完整检查通过；工作流15个回归、包装5个测试均通过 |
| Draft push | [run 40181861806 / attempt 1](https://github.com/snkio027/atlas/actions/runs/40181861806/attempts/1)：Fast成功、Full skipped、聚合failure |
| 同 SHA 无新提交转 Ready | [run 40182056476](https://github.com/snkio027/atlas/actions/runs/40182056476)：Fast成功、Plan成功，进入完整执行；随后因受控旧run重跑而取消，不计Full通过 |
| 旧 Draft run 重跑 | [run 40181861806 / attempt 2](https://github.com/snkio027/atlas/actions/runs/40181861806/attempts/2)：取消在途Ready运行，Fast成功、Full skipped、聚合failure；不得按完成时间选“最新绿灯” |
| 9种拒绝案例 | [run 40182034045](https://github.com/snkio027/atlas/actions/runs/40182034045)：计划命令失败、计划缺失/坏JSON/空对象/错误候选、未知策略/模式、执行步骤失败、artifact缺失；每项因预定原因失败，聚合failure |
| 改目标分支 | 探针PR临时改向独立源码分支，真实GitHub PR元数据被本地未修改的当前候选校验器拒绝；恢复main产生新的运行。这不是远端Full通过证据 |
| 源变更与报告语义 | 本地回归和GitHub Fast覆盖真实临时Git仓库中的删除/重命名/未跟踪文件、缺历史保守Full、缺项/重复项、对象/环境/策略/attempt错配、报告缺失和读取失败；fixture与mock不宣称线上独立策略防御 |

最终交付仍须核对：Ready更新、退回Draft/取消、恢复当前候选验证、完整报告跨Job传递、当前完整CI通过、
required context实际生产者和规则生效。最终合并与合并后的main验证不能在此提前填写为成功。
Queue及assured的专属案例不适用；没有启用队列，也没有独立准入发布者。

## 失败处置

- 本地首次Fast因构建缓存目录访问受限失败；保留原始记录，使用所需本机权限后通过。没有修改检查范围。
- [run 40181595283](https://github.com/snkio027/atlas/actions/runs/40181595283) 在调度前失败：job-level env错误引用runner上下文，零Job，不是预期Draft拒绝。
  修复为分配runner后写入环境文件；将固定版本/摘要的actionlint加入Fast，并保留非法runner上下文回归。
  随后的真实Draft运行已成功调度并按阶段拒绝。
- 主动注入的9种失败及阶段取消是验收用负例，不作为产品Full失败重跑到绿灯；探针分支不合入。
- 最终候选若出现新的非预期失败，应先记录原因、影响和处置；不能复用这里的结论自动重跑、跳过检查或解除维护锁。

## 确认与使用

代理准备 Diff、head/base/test、策略、CI run/attempt 与失败处置；所有者作最终合并决定。
E1 是受控的单维护者例外，没有把本人账号或代理的操作伪称独立 approval。
实际反馈表单及贡献指南随候选提供；默认分支尚未合入时，不能宣称默认分支反馈入口已经上线。
合并、发布与实际使用验收分别记录。没有新发行版或集群写入授权随本次仓库迁移产生。
