# atlas-layout-r2 常驻访问验收 PASS

2026-10-24 21:34:16 +08:00。适用范围：单所有者、macOS arm64 + OrbStack 本地开发。
这是已有 r2 实例的同集群访问增量，经明确批准的有限修复后完成，**不是新的安装或未经中断的单次 clean run**。
[原数据平台验收](namespace-layout-r2-runtime-validation.md)、历史 STOP 和备份保留原提交绑定。
本次结果整理、PR 合并和发行均不修改或重建现有集群。

## 实现与证据绑定

| 记录 | 固定身份 |
| --- | --- |
| 初始访问实现 | `b597421afde2a7296cc31fdb194296faab06962f` |
| Kafka 修复与最终访问实现 | `931431c44f13820d59d8fe00ff1d933162458342` |
| 最终部署 Git | `7d1efb00754d743ec2171729a67dc6d9f5101188` |
| 原访问计划 SHA256 | `31b1c7483ffa464180147ac096ed0df30ac53c3b83946847e533cd41dad261db` |
| 来源格式纠正 SHA256 | `93760e72fb19a0932b0d2bde1d632282abea305906dff2e73bca8015694c5adb` |
| 最终同集群修复计划 SHA256 | `2593cb6cdcecae8bbb5ee50d1b671450a82afe5d5806bb70181dd8e4fe164d71` |

`931431c` 完整本地 `task quality` 和 [exact-head Quality 40095746656](https://github.com/snkio027/atlas/actions/runs/40095746656)
均成功。最终修复前只读核验 563 项资源；只发布一个固定 Kafka 提交，未重跑已通过的三个阶段。
本次私有执行入口另有六项 STOP anchor/current-generation 回归，不冒充仓库 CI 的测试项。
后续文档、合并提交和发行标签不改写上述运行实现。

## 已证明

| 检查 | 结果 |
| --- | --- |
| GitOps / ownership | 565 项持久资源内容、owner、tracking、Argo SSA 核验通过；49 个 Application Synced/Healthy，四节点 Ready |
| 既有身份 | 原 535 项及修复前 563 项持久资源 UID/归属保持；Bootstrap Identity/Latch/Receipt 未变，未恢复 Seed authority |
| PostgreSQL | Mac 原生 psql verify-full、TLS 1.3、只读 SELECT 成功；错误密码拒绝 |
| Redis | Mac TLS 证书链/主机名、AUTH/PING 成功；错误密码拒绝 |
| Kafka | 两个 Mac 原生端口的 mTLS Metadata、固定 broker advertised 地址、clusterId 正确；无客户端证书拒绝 |
| 浏览器 | Grafana/Argo 原账号登录、六个 HTTPS 入口通过；Prometheus/Alertmanager 写方法及 Flink 危险路径不匹配路由 |
| 监测与原业务 | Grafana 查询 Prometheus/Loki 数据源成功；S3 匿名拒绝、签名 HEAD 成功；原 Web 只读不回归 |
| 本机使用 | hosts 六域名映射、本实例 CA 当前用户 SSL 信任、六份叶子证书校验通过；系统 DNS 和系统 curl 默认信任通过，无 insecure/CA override |
| 数据与凭据 | PVC/卷、Kafka clusterId、账号与 CA 摘要不变；仅原计划允许的服务器叶子证书与 Pod 滚动 |
| 重复操作 | 同一输入重复编译逐字节一致；已发布 tree 无差异，不需要第二次 publication，没有凭据轮换或业务写入 |
| 收尾 | 默认 context 保持本实例；原三个无凭据探针已清理；最终回执 PASS，新修复 attempt 无 terminal STOP |

## 失败知识与修复

来源预检返回 IPv4-mapped 地址，旧辅助检查直接按普通地址解析而误报。
规范化正反样例通过后，仍只接受一个实测 RFC1918 IPv4 /32；三个临时对象按 UID/RV 清理。
该来源是 OrbStack NAT 边界，不是 Mac 登录用户的唯一身份。原 STOP 不删除。

Strimzi 1.3.0 的 NodePort listener 会调谐 init ClusterRoleBinding，即使已有固定 advertisedHost；
`STRIMZI_CREATE_GLOBAL_RESOURCES=false` 不关闭这条每实例路径。首个候选因此被现有 RBAC 拒绝。
修复采用额外 internal mTLS listener 的 advertisedHost/Port 和 SAN 支持，并由既有 kafka Application
独立持有两个新命名的 NodePort Service。Operator 自有对象、原内部 listener、CA、用户和 ACL 保持；
未增加 RBAC、双重所有者或手工 patch tracking/spec。回归覆盖范围、身份、确定性及 rack discovery 拒绝。

依据：[KafkaCluster](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/model/KafkaCluster.java)、
[KafkaReconciler](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/operator/assembly/KafkaReconciler.java)、
[KafkaListenersReconciler](https://github.com/strimzi/strimzi-kafka-operator/blob/1.3.0/cluster-operator/src/main/java/io/strimzi/operator/cluster/operator/assembly/KafkaListenersReconciler.java)。

## 脱敏证据索引与限制

最终私有索引的 29 项文件已逐一重算匹配；这里只公开核心摘要，不上传索引内的私有路径或原始对象。

| 记录 | SHA256 |
| --- | --- |
| final.json | `8a010fab104d0ce1e5192a6a9a19b56cd756009e6904979f80aeced71676e5ae` |
| final-observation.json | `80d6292e44e2bd036257cee2a665ac8d9c982741c7bfb15c43c272a7dfbbfabf` |
| native-verification.json | `7601b54c035a4b00b3a8cc3b18c275ff48c6254d89a20a12de145961b7be01ab` |
| browser-verification.json | `a426a8b272730bc862505aee9ef2db268fc1f080692607276101c074ffb2819c` |
| host-access.json | `a26278966d2fc1a2724426df3270b5eccc06d4d6dac0318dfe8837523ee13cef` |
| system-browser-access.json | `deea96b6b7f19974aa393592cb796cd88679af372bbeb10705805eb15263a35d` |

公开摘要不能让第三方独立重算私有现场。原始日志、客户端配置、凭据摘要、私钥、kubeconfig、
主机路径和 cluster UID 不公开。本轮没有生产/消费 Kafka 消息或写入业务数据；这不是新的持久性测试。
同机备份不等于独立 DR；单副本本机卷不等于 HA。未证明生产、多租户、通用恢复、旧实例升级、
空缓存新主机或新发行包重新安装。访问命令见[使用指南](local-platform-access.md)。
