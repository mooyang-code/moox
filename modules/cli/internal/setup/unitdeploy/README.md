# 原生核心初始化与主机部署入口

`moox-cli setup bootstrap --stage core --file ./moox.toml --source-dir /path/to/worktree`
由本机 CLI 读取一次私有配置，核验 SSH 主机指纹，按目标 Linux 架构准备 host/control
软件包，再传输规范请求并调用目标 `moox-runtime`。缺少预构建目录时，前端和这两个
包内的纯 Go 组件均在本机构建，强制 `CGO_ENABLED=0`；该构建器没有远端编译能力。
只有 Storage CGO 构建使用 `setup build-linux` 和 `compile_host`。

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
`MOOX_BOOTSTRAP_HOST_ARCHIVE`、`MOOX_BOOTSTRAP_CONTROL_ARCHIVE` 和只含公开模板的
合成 Git 仓库 `MOOX_CORE_SOURCE_ROOT`。真实 SSH/SFTP 场景从首次制品准备开始，使用
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

完整业务单元与外层 bootstrap 编排、生产历史状态迁移、云资源/防火墙、完整系统验收、
全部编码后的独立 Agent 审查和正式发布仍待接通。
