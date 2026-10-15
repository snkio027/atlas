# 数据平台首次部署：失败经验

适用范围：单所有者本地开发。首次运行实现 `8d0741f`，不是数据平台 Runtime PASS。
D1 已经完成正常交接续行及功能检查；原中断记录保留，不能描述为未中断的单次安装。
修正实现 `660b96e` 的 exact-head Quality 已通过，Flink CRD 修正 Gate 已通过。
扩展凭据与三个 Operator 已发布，随后因 Strimzi 确定性启动错误 STOP；运行实例、
日志监测扩展与功能探针尚未执行。原 STOP 与本次已发布提交分别保留。

| 现象 | 原因 / 影响 | 修正与验证 |
| --- | --- | --- |
| 新 foundation Application 报 InvalidSpecError | AppProject 与 Application 同一提交发布，两个既有父 Application 独立调谐；消费者早于新 namespace 授权。五个 foundation 后来自主收敛，但原 STOP 保留。 | 每阶段新增权限先独立提交，核验 project-bootstrap 版本及 live AppProject 后再发布消费者；不把真实权限错误统一改成 Pending。 |
| Flink 四个 CRD 持续 OutOfSync / self-heal | 上游 Chart 有八处打印列 priority:0；Kubernetes API 的字段序列化省略零，Argo SSA predicted state 保留零。CRD 已 Established，不代表 Git/live 比较稳定。 | renderer 仅省略新数据 CRD 打印列的数值零；非零 priority、其他资源及 schema default 保留。`TestDataCRDPrinterColumnSerialization` 与真实生成文件检查覆盖此边界。 |
| Strimzi 启动立即退出，缺少 Kafka 4.2.0 image | renderer 将上游五项版本目录裁成当前实例使用的 4.3.1；Strimzi 1.3.0 启动时校验 Kafka、Connect、MirrorMaker2 的完整支持目录。属于确定性配置错误，不是启动时间不足。 | 保留五项版本与真实镜像的对应关系，每项核验并锁定 digest；不把旧版本伪映射到 4.3.1。`TestStrimziSupportedImageCatalog`、真实生成文件检查和离线真实解析器正反例覆盖目录完整性；运行实例仍只声明 4.3.1。 |

Flink 原因来自实际 Argo managed-resource 的 normalizedLiveState / predictedLiveState 对照：
仅移除上述八处零值后，四份比较结果全部相同。没有修改 live spec、tracking、CRD schema
或 Argo ignoreDifferences。原始响应仅留在本机私有证据中。

Strimzi 根因也与锁定的 [官方版本目录](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/kafka-versions.yaml)
及 `ClusterOperatorConfig.parseKafkaVersions` 的完整目录校验一致。离线检查入口为
`python3 scripts/check-strimzi-config.py`：固定镜像、关闭网络、无 kubeconfig/凭据；单版本负例
必须复现异常，完整目录必须到达配置解析后的日志。它不证明 Kubernetes API 访问或 Operator 就绪。

两次外部 mutation 后的 STOP 均保留。Strimzi 源码修复与本地验证不等于已经部署；
后续发布需绑定新实现、当前 cluster/Git/authority 与精确计划。无需删除现有集群或重新获取 Seed 权限。
