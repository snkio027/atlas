# 数据平台首次部署：失败经验

> 最新结果：合并主线的新实例已完成 [r2 运行验收 PASS](namespace-layout-r2-runtime-validation.md)；[r1 组合候选结果](namespace-layout-runtime-validation.md)保留。以下原阶段记录保留其日期、提交与当时状态；NOT RUN/STOP 不倒改为历史 PASS。

适用范围：单所有者本地开发。首次运行实现 `8d0741f`，不是数据平台 Runtime PASS。
D1 已经完成正常交接续行及功能检查；原中断记录保留，不能描述为未中断的单次安装。
修正实现 `660b96e` 的 exact-head Quality 已通过，Flink CRD 修正 Gate 已通过。
扩展凭据与三个 Operator 发布后曾因 Strimzi 确定性启动错误 STOP。
`adcaf5d` 的 exact-head Quality 与修正后的 Operator Gate（101项）均已通过；
权限 Gate 通过后已发布运行实例，随后在 PostgreSQL 初始化暂态处 STOP。
PostgreSQL/Kafka/Flink/Loki 后来自然 Healthy；Redis 探针仍有确定性参数错误。
日志监测扩展与功能探针尚未执行。各次 STOP 与实际发布提交分别保留。

| 现象 | 原因 / 影响 | 修正与验证 |
| --- | --- | --- |
| 新 foundation Application 报 InvalidSpecError | AppProject 与 Application 同一提交发布，两个既有父 Application 独立调谐；消费者早于新 namespace 授权。五个 foundation 后来自主收敛，但原 STOP 保留。 | 每阶段新增权限先独立提交，核验 project-bootstrap 版本及 live AppProject 后再发布消费者；不把真实权限错误统一改成 Pending。 |
| Flink 四个 CRD 持续 OutOfSync / self-heal | 上游 Chart 有八处打印列 priority:0；Kubernetes API 的字段序列化省略零，Argo SSA predicted state 保留零。CRD 已 Established，不代表 Git/live 比较稳定。 | renderer 仅省略新数据 CRD 打印列的数值零；非零 priority、其他资源及 schema default 保留。`TestDataCRDPrinterColumnSerialization` 与真实生成文件检查覆盖此边界。 |
| Strimzi 启动立即退出，缺少 Kafka 4.2.0 image | renderer 将上游五项版本目录裁成当前实例使用的 4.3.1；Strimzi 1.3.0 启动时校验 Kafka、Connect、MirrorMaker2 的完整支持目录。属于确定性配置错误，不是启动时间不足。 | 保留五项版本与真实镜像的对应关系，每项核验并锁定 digest；不把旧版本伪映射到 4.3.1。`TestStrimziSupportedImageCatalog`、真实生成文件检查和离线真实解析器正反例覆盖目录完整性；运行实例仍只声明 4.3.1。 |
| PostgreSQL 初始化即 Degraded，24秒后自然 Healthy | CNPG 1.30.1 在所有非 Healthy phase 设置 Ready=False/ClusterIsNotReady。原Lua将正常首次primary初始化也当作终态错误。 | 仅两个已知启动phase与精确reason的组合返回Progressing；未知phase、其他失败reason仍Degraded。真实Lua正反例防回归，不把未就绪判作Healthy。 |
| Redis 三个探针均报 PING 参数数量错误 | --sni 位于 ping 后，被redis-cli作为业务命令参数；旧隔离测试另写了简化PING，没有执行清单里的真实argv。 | TLS选项置于ping前；生成清单回归加 `scripts/check-redis-probes.py` 执行三个真实探针、旧顺序负例和错误密码负例。 |
| CNPG live spec 与 Git 出现30项默认补充 | 只读预检发现 admission webhook 注入initdb、监测、GUC、复制槽与liveness默认值，CRD structural default检查不覆盖这些行为。尚未触发后续Gate写入。 | 将锁定版本的实际默认值显式声明；已有值不变。脱敏admitted-spec fixture约束完整生成spec，不删除或忽略live字段。 |

Flink 原因来自实际 Argo managed-resource 的 normalizedLiveState / predictedLiveState 对照：
仅移除上述八处零值后，四份比较结果全部相同。没有修改 live spec、tracking、CRD schema
或 Argo ignoreDifferences。原始响应仅留在本机私有证据中。

Strimzi 根因也与锁定的 [官方版本目录](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/kafka-versions.yaml)
及 `ClusterOperatorConfig.parseKafkaVersions` 的完整目录校验一致。离线检查入口为
`python3 scripts/check-strimzi-config.py`：固定镜像、关闭网络、无 kubeconfig/凭据；单版本负例
必须复现异常，完整目录必须到达配置解析后的日志。它不证明 Kubernetes API 访问或 Operator 就绪。

CNPG默认值与Ready语义另核对锁定版本的
[admission defaulting](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/api/v1/cluster_defaults.go)、
[phase定义](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/api/v1/cluster_types.go)及
[GUC默认值](https://github.com/cloudnative-pg/cloudnative-pg/blob/v1.30.1/docs/src/postgresql_conf.md)。

全部外部 mutation 后的 STOP 均保留。readiness修正的源码和本地验证不等于已经部署。
新的Lua进入后续产品渲染；当前实例保持原D1 Seed/record，不借修复改写authority。
后续只发布经审查的Redis/CNPG资源差异及原日志监测阶段，重新核对实际目标和计划。
无需删除现有集群或重新获取Seed权限；没有数据组件Runtime PASS。

## Acceptance tooling is part of the implementation

Three private-script failures shared one coverage gap: the code actually used for
acceptance was outside repository CI. A Kafka consumer used obsolete options;
transaction authorization was inferred from an exception class that the locked
CLI never prints; a create-only evidence file was written twice. These are tool
contract and execution-order failures, not reasons to broaden accepted runtime
states or permissions.

A further S3 test selected web-smoke as a supposedly authorized peer. Both its
rendered and live identity lack the S3 client label, so default-deny correctly
blocked the request. The positive control was invalid before any network call.
No workload label or policy is changed to make that test pass.

The finite [data acceptance contracts](../experiments/data-platform/README.md)
now validate probe identities and both policy directions before effects, separate
process exit/stdout/stderr, persist exclusive intents before operations, and test
the actual entry point with injected failures. `task quality` runs these tests.
CNPG installation checks cover its complete reviewed control-plane permission set,
rather than only the informer that most recently failed. Existing runtime STOPs
remain historical facts; these local tests do not retroactively turn them into PASS.

## r2 收口经验（2026-10-24）

[r2 最终验收](namespace-layout-r2-runtime-validation.md)经批准的有限续行完成；以下问题不改写历史 STOP。

| 问题 | 根因与影响 | 修正与验证边界 |
| --- | --- | --- |
| 正确的 Kafka 授权拒绝被判失败 | 非 TTY kubectl exec 把 Kafka stderr 与自身退出诊断放在 stderr；旧 fixture 只模拟了错误的 stdout。 | `2308a1b` 修正 `transactional_denial`；`test_captured_kubectl_authorization_denial` 与 `test_authorization_result_is_version_bound_and_fail_closed` 读取脱敏真实捕获并拒绝错 ID、错退出码、缺失/重复诊断、混合流及未知文本。仓库 `task data:contracts` / `task quality` 覆盖。 |
| 旧消息不能用于后续持久性测试 | 执行暂停已超过 24 小时保留期；测试数据生命周期短于验收间隔。在 broker 替换前停止。 | 私有计划使用独立 topic / 新消息连续完成前读、一次替换、后读，再精确清理；不修改服务保留策略、不补写旧消息。持久性证据仅针对新 fixture。 |
| 清理前 Git 只读查询连接重置 | 真实网络传输错误；不代表 parent 漂移，也不代表写入结果未知。 | 私有收尾入口仅对明确的只读传输错误作最多三次有界重试；复核已完成证据后只做剩余清理与验收，不重放已完成的 Kafka 操作。 |

私有编排的本地回归不等于仓库 CI 覆盖，不能以运行成功声称已经交付通用恢复能力。
