# 日常使用：浏览器与 Mac 原生客户端

适用范围：单所有者 macOS arm64 / OrbStack 开发实例。以下以现存
`atlas-layout-r2` 为例。数据仍只有单副本和本机持久卷，不是 HA 或独立灾备。

**发布状态：常驻入口增量待部署审查。** r2 已通过数据平台验收，但目前只有
`web.atlas.test:48443` 已开放。下面的管理域名、数据库 NodePort 是本轮候选，
不能把清单编译成功当成已经可连接。完成实测后更新此状态，不需要重建集群。

## 入口与登录

| 能力 | 地址（增量部署后） | 认证与用途 |
|---|---|---|
| Grafana | `https://grafana.atlas.test:48443` | `admin` + 本实例 grafana-admin Secret；指标、告警、Loki Explore |
| Argo CD | `https://argocd.atlas.test:48443` | `admin` + 本实例初始管理员 Secret；首次登录后自行管理密码 |
| Prometheus | `https://prometheus.atlas.test:48443` | 仅本机查询 GET/HEAD；不开放写 API |
| Alertmanager | `https://alertmanager.atlas.test:48443` | 仅本机查询 GET/HEAD；静默/修改通过已授权管理路径 |
| S3 | `https://s3.atlas.test:48443` | S3 签名认证；保留应用 Binding 独立身份 |
| Flink overview | `https://flink.atlas.test:48443` | 只读总览和静态资源白名单；不提供匿名作业控制 |
| PostgreSQL | `atlas-layout-r2-worker3.orb.local:30432` | TLS verify-full，SCRAM；库/用户 `platform` |
| Redis | `atlas-layout-r2-worker3.orb.local:30679` | TLS + ACL；用户 `platform-admin` |
| Kafka bootstrap | `atlas-layout-r2-worker3.orb.local:30993` | TLS 客户端证书 + Kafka ACL |
| Kafka broker 0 | `atlas-layout-r2-worker3.orb.local:30994` | 由 broker metadata 公告，原生客户端必须也能到达 |

浏览器域名在 hosts 指向 `127.0.0.1`；使用本实例的 development CA，不能复用旧集群 CA。
数据库使用 OrbStack 的 `.orb.local` 网络，不是 hosts 回环映射，也不是 `port-forward`。
服务经 `externalTrafficPolicy=Local` 保留源地址，NetworkPolicy 只放行经实测的一个
Mac 来源 IPv4 /32 和精确端口。不能自行扩大为 `0.0.0.0/0`。源地址变化时重新核验。
容器网络可被本机其他进程访问；“本机”不是用户级隔离，TLS 和身份认证仍是必要边界。

这些入口是平台管理员入口，不自动赋予任何项目访问权限。业务继续使用 Binding；
不要把管理员凭据加入项目 Git 或容器镜像。Loki、Operator Webhook、Redis 内部依赖、
Kafka 控制器端口等保持内部访问。

## 选择实例与私有客户端目录

以下命令在自己的终端执行；`kubectl` 需要已安装且与集群兼容。每条命令固定 context，
避免默认上下文变化造成误操作。私有客户端目录不得位于 Git 仓库。

```sh
export ATLAS_CONTEXT=kind-atlas-layout-r2
export ATLAS_HOST=atlas-layout-r2-worker3.orb.local
export ATLAS_CLIENT="$HOME/.config/atlas/layout-r2"
umask 077
mkdir -p "$ATLAS_CLIENT"
chmod 700 "$ATLAS_CLIENT"
k() { kubectl --context "$ATLAS_CONTEXT" "$@"; }
k get nodes
k get applications -n platform-argocd
k get ns -L atlas.local/class,atlas.local/domain,atlas.local/owner
```

不要在开启 `set -x` 的终端操作凭据。以下复制密码到 macOS 剪贴板的命令不会
把密码打印到终端；粘贴后清空剪贴板。账号改密后，旧初始 Secret 不再是有效登录来源。

```sh
# Grafana：admin
k -n platform-monitoring get secret grafana-admin \
  -o jsonpath='{.data.admin-password}' | base64 -D | pbcopy
# Argo CD：admin（仅尚未更改的初始密码）
k -n platform-argocd get secret argocd-initial-admin-secret \
  -o jsonpath='{.data.password}' | base64 -D | pbcopy
# 使用后清空
pbcopy < /dev/null
```

## PostgreSQL

从 Cluster 读取 CA 的实际名称，只导出公有 CA；不读取 CA 私钥。

```sh
PG_CA_SECRET=$(k -n service-postgresql get cluster postgresql \
  -o jsonpath='{.status.certificates.serverCASecret}')
k -n service-postgresql get secret "$PG_CA_SECRET" \
  -o jsonpath='{.data.ca\.crt}' | base64 -D > "$ATLAS_CLIENT/postgresql-ca.crt"
PGPASSWORD="$(k -n service-postgresql get secret postgresql-app \
  -o jsonpath='{.data.password}' | base64 -D)" \
  psql "host=$ATLAS_HOST port=30432 dbname=platform user=platform sslmode=verify-full sslrootcert=$ATLAS_CLIENT/postgresql-ca.crt"
```

DBeaver / DataGrip 使用同样的 host、port、database、username，启用 CA 与主机名验证，
密码从 `postgresql-app` 私下复制。不要使用 `sslmode=disable` 或跳过证书验证。

```sql
SELECT current_database(), current_user, version();
SELECT ssl, version, cipher FROM pg_stat_ssl WHERE pid = pg_backend_pid();
\dt
```

`platform` 是现有开发库 owner，不是 PostgreSQL superuser。创建项目专属身份与数据库
需要独立的声明与权限设计，不把此账号当作多租户隔离。

## Redis

```sh
k -n service-redis get secret server-tls \
  -o jsonpath='{.data.ca\.crt}' | base64 -D > "$ATLAS_CLIENT/redis-ca.crt"
REDISCLI_AUTH="$(k -n service-redis get secret redis-auth \
  -o jsonpath='{.data.password}' | base64 -D)" \
  redis-cli --tls --cacert "$ATLAS_CLIENT/redis-ca.crt" \
  --sni "$ATLAS_HOST" -h "$ATLAS_HOST" -p 30679 --user platform-admin
```

`redis-cli --sni` 负责 SNI 提示，不能把它单独当成主机名验证证明。
上面的 CA 校验适用于本实例独立 Redis CA；需要同时验证 DNS 主机名的客户端，使用
支持 hostname verification 的原生库（如 Go `crypto/tls` 的 `ServerName`）。部署验收
会独立验证服务证书链与 SAN，不使用 `--insecure`。

连接后可使用 `PING`、`INFO memory`、`SCAN 0 MATCH my-project:* COUNT 100`。
不要用 `KEYS *` 做日常扫描。业务测试请自行选择可删除前缀和 TTL；前缀不是 ACL 隔离。
现有 Redis 为单实例、AOF、持久 PVC；永久数据不能仅保存在本机这一个副本。

## Kafka

用原生 `kcat` 查询 metadata，无需 Java 或端口转发。导出的用户私钥属于客户端身份，
只保存在上述 0700 目录，不能上传 Git。导出不轮换服务端或用户凭据。

```sh
k -n service-kafka get secret kafka-cluster-ca-cert \
  -o jsonpath='{.data.ca\.crt}' | base64 -D > "$ATLAS_CLIENT/kafka-ca.crt"
k -n service-kafka get secret platform-admin \
  -o jsonpath='{.data.user\.crt}' | base64 -D > "$ATLAS_CLIENT/kafka-user.crt"
k -n service-kafka get secret platform-admin \
  -o jsonpath='{.data.user\.key}' | base64 -D > "$ATLAS_CLIENT/kafka-user.key"
chmod 600 "$ATLAS_CLIENT"/kafka-*
cat > "$ATLAS_CLIENT/kcat.conf" <<CONFIG
bootstrap.servers=$ATLAS_HOST:30993
security.protocol=ssl
ssl.ca.location=$ATLAS_CLIENT/kafka-ca.crt
ssl.certificate.location=$ATLAS_CLIENT/kafka-user.crt
ssl.key.location=$ATLAS_CLIENT/kafka-user.key
ssl.endpoint.identification.algorithm=https
CONFIG
kcat -F "$ATLAS_CLIENT/kcat.conf" -L
# 查询自己已创建的 topic；不会新建 topic
kcat -F "$ATLAS_CLIENT/kcat.conf" -C -t YOUR_TOPIC -o beginning -e
# 向自己的 topic 写入一条业务消息（明确的数据写入）
printf '%s\n' 'hello atlas' | kcat -F "$ATLAS_CLIENT/kcat.conf" -P -t YOUR_TOPIC
```

自动创建 topic 已关闭。topic 由明确的业务管理操作建立；默认数据保留期为 24 小时。
bootstrap 成功但生产/消费失败时，先检查返回的 broker 地址是否为同一 `.orb.local:30994`。
`platform-admin` 的 ACL 不包含 transactional ID；需要事务时另行设计授权，不关闭 ACL。

## Flink

浏览器提供 overview；具体 job 操作不通过匿名 HTTP 暴露。使用已有 Kubernetes
访问身份进入 JobManager 的 CLI，仍不需要端口转发：

```sh
FLINK_POD=$(k -n service-flink-session get pod -l app=flink-session,component=jobmanager \
  -o jsonpath='{.items[0].metadata.name}')
k -n service-flink-session exec "$FLINK_POD" -- /opt/flink/bin/flink list \
  -m flink-session-rest:8081
k -n service-flink-session logs "$FLINK_POD" --tail=100
```

提交、取消、savepoint 会改变业务状态，使用同一 CLI 的 `run` / `cancel` / `savepoint`
并确认 Job ID 和保存位置。生产型作业、项目独立运行实例、Kafka 连接权限并不因存在
共享 session 而自动获得。当前 session 只有两个 slot，checkpoint/savepoint 位于本机 PVC。

## 指标、日志与故障定位

Grafana 的 Explore 选择 **Prometheus** 查看 `up`、Pod 资源与服务指标；选择 **Loki**
先查询标签，再按实际 namespace 标签过滤，例如 `{namespace="service-postgresql"}`。
Loki 只收集已声明的 namespace；没有日志不一定是采集故障，先检查是否在 allowlist。

```sh
k -n platform-gateway get gateway,httproute
k get httproute -A
k -n service-postgresql get cluster,pod,pvc
k -n service-redis get pod,pvc
k -n service-kafka get kafka,kafkanodepool,kafkauser
k -n service-flink-session get flinkdeployment,pod,pvc
k -n platform-logging get pod
k -n platform-logging logs daemonset/alloy --tail=100
k get events -A --sort-by=.lastTimestamp
```

连接诊断顺序：DNS → 目标节点/端口 → TLS CA/SAN → 身份认证 → ACL → 应用。
读 `kubectl` 状态与日志是正常排障方式；不要为了变绿手改生成清单、Secret、tracking 或
Argo ownership。旧计划和验收证据继续绑定原提交，入口增量完成后不能重新运行旧功能 probe。

## 维护者生成候选

```sh
atlas-platform management-access --deployment-dir EXACT_DEPLOYMENT_EXPORT \
  --https-port 48443 --flink-overview --output NEW_BROWSER_REVIEW
atlas-platform local-data-access --deployment-dir EXACT_DEPLOYMENT_EXPORT \
  --node-host atlas-layout-r2-worker3.orb.local \
  --client-address OBSERVED_PRIVATE_IPV4 --output NEW_DATA_REVIEW
```

这些命令只生成本地文件。浏览器先证书/权限、后路由；数据先证书、后 Service/listener
及必要的 Redis/Kafka 重载。真实源 IP 必须由批准的无凭据探针实测，不能把示例 IP 当作事实。
发布要绑定 exact Git parent、集群 UID、对象身份、端口与证书 Gate。

依据：[OrbStack 容器网络](https://docs.orbstack.dev/docker/network)、
[Kubernetes Local traffic policy](https://kubernetes.io/docs/reference/networking/virtual-ips/)、
[Strimzi listener 配置](https://strimzi.io/docs/operators/latest/configuring.html)。
