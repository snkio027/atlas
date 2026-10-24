# atlas-layout-r2 常驻访问验证

当前结果：**STOP，尚无完整访问 PASS**。这是同集群访问增量，不是新的安装验收。
原平台 r2 的历史证据保持原提交绑定，不改写为本次实现的验证结论。

## 已执行候选

- 实现 `b597421afde2a7296cc31fdb194296faab06962f`。
- exact-head Quality `40090352313`：SUCCESS。
- 原计划 `31b1c7483ffa464180147ac096ed0df30ac53c3b83946847e533cd41dad261db`。
- 来源地址格式纠正 `93760e72fb19a0932b0d2bde1d632282abea305906dff2e73bca8015694c5adb`。
- 第一阶段：六份浏览器证书及权限，541 项资源 Gate、SAN/CA 验证 PASS。
- 第二阶段：PG/Redis 节点域名证书，541 项资源 Gate、SAN/CA 验证 PASS；账号/CA 未变。
- 第三阶段：六组 HTTPS 入口，559 项资源及 ownership Gate PASS。
- 第四阶段：Git 提交 `6eaba9f6ef4c0cc1aa4358b9cbd45104e5236076` 已发布，Kafka 调谐因 RBAC 拒绝 STOP。

未完成：原生 TLS/认证、浏览器登录/方法及路径负例、hosts/用户 CA 信任和最终不回归 Gate。
不以 Pod 运行或前三阶段成功替代这些验证。不自动 rollback，不重建集群。

## 根因与修复边界

来源预检的 BusyBox CGI 返回 IPv4-mapped 地址。私有辅助检查此前直接按普通地址解析，
在三项无凭据对象创建后停止，未发布 Git。已验证规范化仍只接受单个 RFC1918 IPv4；
原 STOP 保留，三个临时对象按 UID/RV 清理，未扩大网络范围。

Kafka 的真正缺口不是少了一个 GET 权限。Strimzi 1.3.0 的 NodePort listener 会进入
init ClusterRoleBinding 调谐，即使配置了明确 advertisedHost；内部 listener 原来不需要
该绑定，因此当前最小权限安装能够运行。`STRIMZI_CREATE_GLOBAL_RESOURCES=false`
不能被视为关闭这条每实例调谐路径。

修复候选使用额外内部 mTLS listener 的固定 advertisedHost/Port 和 SAN 支持，两个
新命名的 NodePort Service 由既有 kafka Application 持有。保留原内部 listener、
Operator 生成对象、CA、用户及 ACL，不新增 RBAC 权限或双重资源所有者。
编译回归覆盖精确 broker 0 selector、端口、源地址保留、原 listener/ownership 不变、
确定性输出以及拒绝需要节点发现的 rack 配置。真实修复验证仍需新精确计划。

依据：[Strimzi 1.3.0 KafkaCluster](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/model/KafkaCluster.java)、
[KafkaReconciler](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/operator/assembly/KafkaReconciler.java)、
[KafkaListenersReconciler](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/operator/assembly/KafkaListenersReconciler.java)。

本文只记录脱敏结论。原始现场、账号摘要、私钥和客户端配置保留在本机私有证据目录，
不进入 Git。入口访问完成前，PR #14 保持 Draft，稳定版本尚不发布。
