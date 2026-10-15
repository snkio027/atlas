# ADR-0023：人工配置使用受限 YAML，机器协议保留 JSON

Status: Design Accepted — 2026-10-16。用户明确批准本次输入格式变更；不构成部署授权。

人工配置（平台目录/启用、values、资源声明、Project/Workload/Binding、安装与应用配置）
使用 YAML。版本锁、制品索引、生成产品、计划、intent/receipt/STOP及历史证据继续JSON。
旧冻结schema/实验输入保留原格式和字节；不能批量改写历史。`profiles/*.json` 和根目录
`config.example.json` 是原 schema 1–3 Bootstrap 参考，当前安装从 installation.yaml 开始；
新产品不兼容旧布局，不新增双布局安装路径。工作区私有快照仍为 JSON；
部署 Git 中的 Project/Workload/Binding 声明仍为 YAML，供人审查与后续编辑。

YAML在现有Go严格类型边界之前转为JSON数据：单文档、字符串键、禁止重复键、
anchor/alias/merge/custom tag、隐式日期与非JSON数字；类型错误和未知字段继续拒绝。
错误包含源路径与解析位置。同一逻辑文件出现JSON/YAML双源即拒绝；不做环境展开或执行。
输入注释不进入语义模型。Helm values 先规范为临时 JSON 再交给锁定 Helm，
避免 YAML 1.1/1.2 对 on/no 等字符串产生不同含义；临时文件用后移除。编译产物沿用确定性序列化和现有默认稳定性/权限检查。

引入官方YAML组织维护的go.yaml.in/yaml/v3 v3.0.5（v4仍为RC），go.sum绑定校验，
vendor保留解析库源码与许可证，质量/发布使用-mod=vendor，离线不下载。模块的唯一用途是
数据解析与序列化，禁止自行实现YAML或用宽松转换绕过现有严格JSON校验。
这替代初期“仅标准库”的依赖限制，其他供应链要求保持。

迁移验证包含JSON/YAML语义等价、重复/未知/类型/多文档反例、确定性输出及历史冻结检查。
namespace布局改动另由ADR-0022定义；格式变化本身不增加集群或恢复权限。
