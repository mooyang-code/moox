# 发布准备、激活与恢复

`unitinstall` 在软件包与身份消费层之上准备、激活、恢复和回滚私密发布目录，不读取操作员的 `moox.toml`。`Prepare` 只生成候选；`Activate` 在同一主机维护锁下停止旧单元、复制状态、切换 `current` 并启动所选组件。

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

生成的 `start/stop/restart/pause/resume/healthcheck/status.sh` 只定位实际发布目录并调用 host 包的共用 `moox-runtime`，不复制停止等待或强杀逻辑。脚本将带空格、引号或 shell 特殊字符的主机路径作为字面值传递。生命周期按依赖排序：Admin、EventBus 等服务先于 Web Host 和 Console Proxy 启动，停止时反序。实际 `setup` 部署入口、原生 CLI 的 bootstrap 和 `setup pause/resume` 接线仍由后续阶段完成；目标主机编排由相邻 `unitbootstrap` 包调用这些接口。

`moox-runtime activate --directory DIR` 激活完整单元，`--components ID,...` 可指定启动子集，`--no-start` 只切换目录，供五步 bootstrap 分阶段启动。重复激活已完成的当前发布会核验静态内容，不重新启动服务。`ReadInstalled` 验证已运行发布的静态清单，同时允许组件的 data/var/log/logs 与代理 certs 目录；`ReadPrepared` 仍要求未复制状态的候选。

单元根的 0600 `activation.json` 在停止、复制、切换、启动和恢复阶段持久化旧/新发布及原运行选择。部署根 `run/installation.json` 在安装器进程退出、flock 释放后仍阻止自动启动；只有同单元/发布的恢复可接管。`healthcheck` 跳过，外部 start/restart/resume 拒绝，stop/pause 仍可执行。成功激活或完整恢复后才删除此标记，用户暂停标记保持独立。

状态在全部旧进程退出后复制到新目录，拒绝链接/特殊文件，逐文件同步，不使用可变数据硬链接。旧目录保留独立的停机快照；新启动失败自动恢复旧 current 和此前运行的组件。取消后的自动恢复继续使用各进程持久化的完整停止预算；恢复失败保留持久标记，等待 `moox-runtime recover --unit-root ROOT`。`rollback --unit-root ROOT` 显式恢复上次升级前的快照，升级后的数据保留在被退役的新目录，不向旧 schema 合并。首次安装没有可回滚的先前快照。

离线状态通过 `SealState` / `moox-runtime seal-state --request PATH` 封存。0600 规范 JSON 请求对应 `SealStateOptions`，显式指定 `directory`、`release_directory`、`previous_directory` 和 `paths`；首装的 previous_directory 为空。源目录必须是 `<UnitRoot>/state-imports/<ID>` 下的物理 0700 目录，仅接受所选组件的 data/var、代理 certs 或全局 data，文件为拥有者的 0600 常规文件，拒绝链接、硬链接、配置、身份及无关内容。生产者须先结束离线初始化，或完成一致性备份并关闭数据库；封存不执行运行中数据库的文件复制。清单至多 1 MiB、4096 个文件、8192 个对象，文件内容流式校验且不限制数据字节总量。

封存结果仅输出源目录和收据摘要。`activate --directory DIR --state-seed SOURCE --state-seed-sha256 SHA256` 在停机前核对收据、逐文件内容、目标发布和预期旧 current；复制后再次核对新发布里的全部导入字节。导入目录替代相应旧状态，其余状态仍从停机后的旧发布独立复制。源或 current 变化都会拒绝，失败恢复不依赖源目录；升级前快照保持独立。重复激活已完成发布可复用原摘要，不能用不同摘要替换已激活的数据。Admin 主密钥、MooX CA 私钥和操作员材料继续存放在各自的持久私密目录，不通过状态导入扩散到发布。目标主机 `unitbootstrap` 已串联停止、关闭数据库的独立复制、离线初始化、封存及分阶段激活；原生 CLI 部署入口仍须接通。

代理升级拒绝仍开启 initialize_ca 的候选，将状态路径限制在可复制的组件 data/certs 内；使用真实 `moox-console-proxy check-state` 在停机前后、复制后和启动后校验 CA 指纹，首次导入代理状态即使不启动也必须通过只读检查。检查命令不生成证书或写入旧基线。历史 CA 迁移编排、首次初始化授权的一次性持久消费，以及正式部署流程仍属于后续 G7/G11 接线。

验证入口：

```sh
make test-unit-install
```

Linux 完整门禁 `make test-unit-install-linux` 必须提供本机预先构建的 `MOOX_UNIT_INSTALL_TEST_BINARY`、`MOOX_RUNTIME_BINARY`，真实隔离 Admin 生产的 `MOOX_HOST_MATERIAL_FIXTURE`，以及 host/Access/control 软件包路径和对应 `sha256:<64 位小写十六进制>` 摘要：`MOOX_UNIT_INSTALL_HOST_ARCHIVE`、`MOOX_UNIT_INSTALL_HOST_SHA256`、`MOOX_UNIT_INSTALL_ACCESS_ARCHIVE`、`MOOX_UNIT_INSTALL_ACCESS_SHA256`，以及 `MOOX_UNIT_INSTALL_CONTROL_ARCHIVE`、`MOOX_UNIT_INSTALL_CONTROL_SHA256`。十八组必需场景全部执行，不接受跳过。普通 Linux 模块测试缺少这些制品时会跳过实际准备场景，不能算完整门禁通过。状态导入场景执行真实 Admin 离线 bootstrap 和重跑，验证 CA/KeyID 复用、封存数据库导入、独立快照及回滚；仅启动 Web Host，不替代五步 bootstrap 的 Admin/Gateway 启动验收。

门禁启动真实 Web Host 和 Console Proxy，占用合成健康端口。Linux 主机已有业务时，使用 `bwrap --unshare-net --bind / / --dev /dev --proc /proc -- bash scripts/test/gates/test-unit-install-linux.sh` 隔离网络，不停止现有业务。

复合初始化使用 `ReadActivation` 检查单元阶段、`Abort` 撤销日志指定的候选，可撤销首次安装但不会删除退役数据；普通 `Rollback` 仍拒绝没有旧快照的首次安装。`CopyOfflineState` 是关闭后状态的独立复制接口，调用者必须持有维护锁并保证所有写入者已退出，不提供在线数据库备份。
