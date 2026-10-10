# 发布目录准备

`unitinstall` 在软件包与身份消费层之上生成完整、私密的候选发布目录，供后续安装器激活。它不读取操作员的 `moox.toml`，不切换 `current`、复制运行数据或启停现有服务。

`Prepare` 的输入是已构建的软件包及生产端摘要、部署/单元根目录、新发布 ID、已核验身份包的目录与拓扑/信任参数，以及按组件显式提供的运行环境和私密配置覆盖。单元根与部署根必须预先存在且由部署用户拥有；发布 ID 不覆盖已有文件、目录或链接。纯 Go 软件与测试制品在本机构建，Linux 仅运行它们；需要 CGO 的目标仍使用 Linux 编译机。

准备流程如下：

1. 根据目标机实际平台验证并解包软件，检查所选组件属于软件单元和主机完整放置。
2. 取得部署根共用维护锁并核对持久主机身份；已有调用方通过真实继承 FD 复用锁。
3. 只注入所选组件的私密身份。host 单元包含 host-gateway 与 host-agent；TLS 材料只进入 host 单元。Access 的验证表包含其外部调用方密钥，其他业务单元不携带它；操作员身份不进入任何发布。
4. 写入实际 KeyID、组件签名文件路径、健康绑定和 Host Agent 的规范 `host_id`，生成每个组件的 0600 环境文件。Host Agent 的 EventBus 配置必须由部署器显式提供；不会使用软件包中的占位凭据。
5. 主机与业务单元可使用独立根。业务发布的 `host-gateway` 链接指向主机单元的 `current/host-gateway`，主机 `current` 必须属于同主机、同部署根的有界 host 发布。业务单元不复制主机 TLS 私钥。
6. 校验运行计划、环境、二进制与生命周期预算，生成七个共用助手包装脚本，记录全部静态文件的摘要、大小与权限；同步文件和目录后原子发布到 `releases/<ID>`。

配置覆盖只接受所选组件 `config/` 下的 YAML/JSON 文件，并受单文件和总大小限制。host-gateway 配置来自已签发的身份包，不能覆盖。YAML 只接受单个映射文档，拒绝重复键、别名、锚点与 merge。所有未完成的暂存目录在失败时清理；原有 `current` 与部署根的暂停标记保持原状。

`moox-runtime prepare --request /absolute/private/request.json` 读取由 Go JSON 编码器生成、以换行结尾的规范 `PrepareOptions` 文件；文件为部署用户拥有的 0600 常规文件。运行环境和覆盖来源属于私密输入，不写入命令行或输出。成功输出的 `Prepared` 只包含路径、公开身份清单和摘要。

`ReadPrepared` 与 `moox-runtime inspect-release --directory /absolute/unit/releases/ID` 用于安装切换前核验候选发布：要求物理私密目录、规范收据、匹配的主机/组件/运行计划，以及逐文件完整性。未列出的文件、缺失或改动的文件、权限变化和非法链接都会失败。它用于尚未启动或复制运行状态的候选目录；已运行的发布包含可变数据，不能再次视为全新候选。

生成的 `start/stop/restart/pause/resume/healthcheck/status.sh` 只定位实际发布目录并调用 host 包的共用 `moox-runtime`，不复制停止等待或强杀逻辑。脚本将带空格、引号或 shell 特殊字符的主机路径作为字面值传递。安装切换、状态复制、失败回滚、bootstrap 和 `setup pause/resume` 接线仍由后续阶段完成。

验证入口：

```sh
make test-unit-install
```

Linux 完整门禁 `make test-unit-install-linux` 必须提供本机预先构建的 `MOOX_UNIT_INSTALL_TEST_BINARY`、`MOOX_RUNTIME_BINARY`，真实隔离 Admin 生产的 `MOOX_HOST_MATERIAL_FIXTURE`，以及 host/Access 软件包路径和对应 `sha256:<64 位小写十六进制>` 摘要：`MOOX_UNIT_INSTALL_HOST_ARCHIVE`、`MOOX_UNIT_INSTALL_HOST_SHA256`、`MOOX_UNIT_INSTALL_ACCESS_ARCHIVE`、`MOOX_UNIT_INSTALL_ACCESS_SHA256`。七组必需场景全部执行，不接受跳过。普通 Linux 模块测试缺少这些制品时会跳过实际准备场景，不能算完整门禁通过。
