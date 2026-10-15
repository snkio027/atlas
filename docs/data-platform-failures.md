# 数据平台首次部署：失败经验

适用范围：单所有者本地开发。首次运行实现 `8d0741f`，不是数据平台 Runtime PASS。
D1 已经完成正常交接续行及功能检查；原中断记录保留，不能描述为未中断的单次安装。
扩展凭据、Operator、数据服务和功能探针尚未发布或执行。

| 现象 | 原因 / 影响 | 修正与验证 |
| --- | --- | --- |
| 新 foundation Application 报 InvalidSpecError | AppProject 与 Application 同一提交发布，两个既有父 Application 独立调谐；消费者早于新 namespace 授权。五个 foundation 后来自主收敛，但原 STOP 保留。 | 每阶段新增权限先独立提交，核验 project-bootstrap 版本及 live AppProject 后再发布消费者；不把真实权限错误统一改成 Pending。 |
| Flink 四个 CRD 持续 OutOfSync / self-heal | 上游 Chart 有八处打印列 priority:0；Kubernetes API 的字段序列化省略零，Argo SSA predicted state 保留零。CRD 已 Established，不代表 Git/live 比较稳定。 | renderer 仅省略新数据 CRD 打印列的数值零；非零 priority、其他资源及 schema default 保留。`TestDataCRDPrinterColumnSerialization` 与真实生成文件检查覆盖此边界。 |

Flink 原因来自实际 Argo managed-resource 的 normalizedLiveState / predictedLiveState 对照：
仅移除上述八处零值后，四份比较结果全部相同。没有修改 live spec、tracking、CRD schema
或 Argo ignoreDifferences。原始响应仅留在本机私有证据中。

首次执行仍保留 STOP。代码回归与本地渲染通过，不等于修复已经部署；真实续行需绑定
修复提交、当前 cluster/Git/authority 与新精确计划。无需删除现有集群或重新获取 Seed 权限。
