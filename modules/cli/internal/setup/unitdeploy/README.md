# 原生核心初始化与单元部署入口

`moox-cli setup bootstrap --stage core --file ./moox.toml --source-dir /path/to/worktree`
由本机 CLI 读取一次私有配置，核验 SSH 主机指纹，按目标 Linux 架构准备 host/control
软件包，再传输规范请求并调用目标 `moox-runtime`。缺少预构建目录时，前端和这两个
包内的纯 Go 组件均在本机构建，强制 `CGO_ENABLED=0`；该构建器没有远端编译能力。
只有 Storage CGO 构建使用 `setup build-linux` 和 `compile_host`。

核心请求从同一可信快照携带初始管理员，离线建表和身份签发后通过 stdin 调用
`moox-admin-cli user ensure`，密码不进入 argv、环境或公开收据。已有用户名只保留原密码，
完成重试也不重复初始化帐号。用户名须满足实际登录接口的 1～20 位 ASCII 字母/数字限制，
密码按 bcrypt 的 72 字节上限校验，非法输入在 SSH 和本机私密状态创建前拒绝。

部署、材料导出与恢复统一采用 `paths.control_root`，可位于部署根下的嵌套目录。host 与
control 必须相互独立，且不能占用 `bootstrap`、`bootstrap-input`、`identity`、`run` 状态
命名空间；使用自定义路径不会额外创建硬编码的 `control/`。Storage 根仍须位于部署根下，
其跨部署根布局与跨主机角色路由继续由 G13 完成。

核心阶段保留全部主机、放置和签名身份，只安装控制机的 Admin、EventBus、Host Gateway、
Host Agent。输出 `stage=core-ready`，不能据此认定完整部署成功。待 Storage、Access、
出口代理、交易和计算依赖就绪后，外层流程才升级完整 control 放置并激活 Console Proxy。
已有的完整控制服务不能通过核心请求被缩减。

`--state-dir` 默认为 `~/.local/state/moox/setup/<部署绑定摘要>`；其中保留 0700 目录、
0600 私有运行凭据、原请求和制品清单。软件制品首次准备后独立校验、持久保存，重试
复用原始字节，不重新构建。此入口用于核心初始化及恢复，软件升级由外层发布流程负责。
目标文件按内容摘要命名，上传前后独立校验，已有不同内容拒绝覆盖。

目标 `identity/runtime.json` 在启停之前持久绑定全局健康/JWT 身份的摘要；本机状态丢失
不授权替换它们。SSH 断连或超时保留原请求与目标日志，不能据此认定远端工作已退出。
重试须使用原状态目录与配置，由目标维护锁和恢复日志判断状态。

成功后只下载操作员配置、签名密钥及公开 MooX CA，通过原始清单和 CA/拓扑/快照绑定
校验后原子安装本机身份。服务私钥和网关 TLS 私钥留在目标机。`--operator-dir` 默认
`~/.config/moox`，配置为 `gateway-client.yaml`，与 `gatewayio` 的读取入口一致。
本机已有 CA 绑定不匹配时拒绝覆盖，身份轮换使用显式流程。

本机验证为 `make test-unit-core`。Linux 门禁 `make test-unit-core-linux` 只执行预构建的
测试与软件，必须提供 `MOOX_CORE_TEST_BINARY`、`MOOX_RUNTIME_BINARY`、
`MOOX_BOOTSTRAP_HOST_ARCHIVE`、`MOOX_BOOTSTRAP_CONTROL_ARCHIVE`、只含公开模板的
合成 Git 仓库 `MOOX_CORE_SOURCE_ROOT`，以及 `MOOX_BUSINESS_BINARY_DIRECTORY` 中预构建的
Access、出口代理、Trade/Trade CLI 和四个 Storage 二进制（仅三个服务需要 CGO）。真实 SSH/SFTP 场景从首次制品准备开始，使用
完整三主机拓扑核验四个核心服务、主机指纹拒绝、操作员身份范围、重复执行 PID 保持、
本机状态丢失拒绝替换凭据。其余主机只参与拓扑与签发，未在此场景启动。

核心成功后在本机保存 `core-ready.json` 完成收据。`setup deploy-host --host HOST` 使用同一
`state-dir` 和 `operator-dir`，经签名 SSH 隧道调用 `SyncHostPlacements`，再由控制机的
`moox-runtime export-host` 在维护锁内调用运行中 Admin 的离线 CLI 导出身份。导出绑定原始
运行身份摘要和 MooX CA，只签发目标主机材料；EventBus 只取 `hostagent-publisher`、
`metrics-publisher` 与公共 CA。操作员身份、Admin 主密钥、MooX CA 私钥和 broker 私钥不随
主机部署扩散。导出请求、主机清单、客户端清单和最终收据均持久化，重试复用已签发字节。

本机逐文件核对并缓存导出，上传原始软件和规范请求后调用 `moox-runtime deploy`。目标端
持锁准备候选、独立复制旧状态和激活；中断先恢复，已回滚候选不能复用其可变数据，重试
创建新候选。相同完成请求修复缺失进程并保留暂停状态；旧完成请求与 current 不符时拒绝。
只有两项主机组件实际就绪才返回 `host-ready`，存在暂停组件时返回 `host-paused`。
同架构复用核心阶段验证过的 host 软件；其他架构仅在本机关闭 CGO 构建 host 软件，不构建
前端或 control。跨架构的真实机器运行验收仍待完成。

Linux 场景还在运行中的控制主机验证真实主机部署、按角色导出及检查点恢复、重复执行
PID 保持、暂停保留、缺失进程修复、激活完成窗口接管及回滚后新建候选。其他两台主机仍
只参与拓扑；这不是三主机实际安装验收。

`setup deploy-unit --host HOST --profile access|egress-proxy|trade|storage` 使用同一持久
状态与操作员身份，只选择该主机在对应软件包内实际放置的组件。出口配置和 Storage
策略由可信配置快照产生，配置字节及私有环境摘要绑定原始操作；重试拒绝变更后重新接管。
客户端材料只包含所需签名身份、EventBus 角色和公开 CA，运行时使用发布内的独立副本。
同架构助手复用原始 core 制品，其他架构只接受原目标 host 包验证过的助手。

Access、出口代理和 Trade 默认在本机关闭 CGO 构建。Storage 必须先执行
`setup build-linux --module storage --source-dir /path/to/worktree`，再以 `--binary-dir` 提供
三个 CGO Linux 服务制品和本机关闭 CGO 编译的 Storage CLI。新建 core 的私有运行身份保存独立的 node、primary、view 认证密钥，
按 Storage 角色分发；旧 core 身份缺少这些密钥时拒绝 Storage 部署，不在重试中补生成。
当前模板的内部 RPC 只监听 loopback，原生入口要求三个 Storage 角色位于同一主机；跨主机角色路由未接通时，在远端操作前拒绝部署。Storage 主库初始化在旧写入者退出、候选独立复制完成后执行，强制候选内的相对数据根，
子进程继承维护锁并在父进程死亡时退出；取消后先等待写入者退出，再释放维护锁。
初始化关闭数据库后只同步元数据树和祖先目录，不扫描全部数据集，也不输出子进程私密日志。

业务单元只有实际就绪才返回 `<profile>-ready`；暂停返回 `<profile>-paused`。Linux 场景在
隔离网络内启动四类实际业务单元（包括 Storage 三个角色及首次建库），核验重复执行保持
发布/PID、签名身份范围和核心服务 PID。全部服务集中在合成控制主机，另外两台只参与拓扑；
独立主机运行、跨架构运行和真实业务读写仍须验收。此入口负责一次部署操作的恢复，通用
版本升级、整个发布回滚和旧部署命令替换仍由外层流程完成。

完整 control 与外层 bootstrap 编排、生产历史状态迁移、云资源/防火墙、完整系统验收、
全部编码后的独立 Agent 审查和正式发布仍待接通。
