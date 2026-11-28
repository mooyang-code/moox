---
name: moox
description: 凡是与 MooX 量化系统（MooX quant system）有关的事都用这个 skill：用发布产物安装和部署（安装, 新安装, 部署, 初始化, 免密登录, moox-cli config init, setup install/validate/deploy/init/status/logs, 检测是否已部署）；对生产主机做升级、保留数据更新、备份、卸载或回滚（发布, 上线, 升级, 覆盖更新, 保留数据, 卸载, 回滚, setup install --keep, setup install --wipe, setup backup, setup wipe, setup deploy, service catalog rollout, 全新部署, 清空重装）；通过 moox-cli 和 gateway call 做日常管理（采集任务, 补采, K线合成, 标的排除, 存储 View 修复, 因子任务与补算, 策略定义与实例, 回放, 归档, 系统暂停与恢复, 空间, 主机登记）。还包括：查询 MooX采集数据（如 BTC-USDT 等加密货币行情查询 crypto market queries、K-line/K线行情查询 K-line queries），因子表达式 DSL（因子表达式, 因子 DSL, Python 因子转换, wasm UDF 自定义算子），策略 DSL（策略 DSL, rank 规则, ValidateStrategy, 策略迁移, 选币策略），首页“需要处理”/监控告警排查（moox-cli doctor alerts, monitor-db-check），Linux Host Agent 监控，EventBus 凭据（EventBus credentials）, EventBus rotate, Authorization Violation, FIN-WAIT-2, certificate signature failure, View 水位追赶（View watermark catch-up）, repair-view, Factor 周期诊断与补算（Recalc），腾讯云防火墙，CLS 日志查询，CCN/云联网, SCF 公网, SCF VPC, 内网组网, private-network, MOOX_ENTRY_ADDRESS, 外部入口, external_entry，控制面维护，以及覆盖管理台的 moox-cli 命令集（空间, 元数据, 采集任务, 云函数, 因子, 策略, EventBus, 主机与部署, 交易查询与写操作, host exec, 命令覆盖）。
---

# MooX

MooX 是自托管、单用户的量化系统：行情采集 → 数据存储 → 因子计算 → 策略目标 → 交易执行，配全链路监控、Web 管理台和 `moox-cli`。
部署在用户自己的云服务器上（可加一台局域网因子机）；**当前所有服务器（因子机除外，它可以是家里或局域网的 Linux 机器）必须部署在腾讯云上**（轻量应用服务器或 CVM），云厂商接口目前只对接腾讯云 API；其他云厂商正在支持中；小众云厂商的用户可以手动部署，参考 GitHub 源码（`docs/部署与运维.md`）自行初始化各模块配置，`config init` 不适用。用户只装这个 skill，部署、初始化和日常管理都由 Agent 用 `moox-cli` 完成。

## 安全边界（每次都要遵守）

- **配置是两份文件**：`moox.toml` 是明文的拓扑与部署参数（没有秘密，可以直接看和改）；同目录的 `moox.secrets.toml` 是加密的秘密，Agent 不直接读取、打印、复制或 `source` 它；要看用 `moox-cli config get --file ./moox.secrets.toml <路径>`（默认打码），改单个值用 `moox-cli config set 表.键=值`（见 [`references/config-access.md`](references/config-access.md)）。Agent 也不打开 `config init` / `config edit` 的本机页面；
  只有 `moox-cli` 自己读取其中的密钥。用户要看或改配置时运行 `./bin/moox-cli config edit`，让用户在自己的浏览器里操作。
- **密码与密钥不经过 Agent**：SSH 密码只在 `config init` 的免密登录页面里由用户输入一次；交易所、云账户等密钥由用户在管理台“设置 › 密钥”自己填写。
- **写操作先预览、后执行**：删除、重启、改防火墙、切换线上版本这类破坏性命令（`setup firewall`、`setup reboot-host`、`factor task delete`、`collector function delete`、`storage repair-view` 等）不带 `--yes` 只预览，把预览给用户确认后才加 `--yes`；
  `setup install` 不带 `--yes` 是只读预检：CLI 不做交互式提问；没装过时把输出的步骤、防火墙放行内容与 `manual_steps` 一次给用户确认后带 `--yes` 执行；装过（没装完或已装好）时把预检并排列出的两个选项（`--keep` 保留数据与 `--wipe <摘要>` 全部覆盖）、数据格式比对结果与待备份条数给用户看，问清保留还是覆盖后再带 `--keep --yes` 或 `--wipe <摘要> --yes` 执行，只在计划变化或失败时再问；单独的 `setup deploy`、`setup init` 没有预览，执行前同样先向用户说明会动哪台主机、哪些组件会重启；单独卸载（`setup wipe`）要按 [`references/install.md`](references/install.md) 第 3 节询问用户，并带预览里的 `--expect` 执行；`gateway call` 的写方法同样默认只预览，确认后加 `--yes`。删除、清空、重置、回滚等破坏性操作必须得到用户明确同意。
- **`gateway call` 取不到密钥明文，也不能改密钥**：它拒绝返回密钥明文或完整 webhook 的方法，以及密钥的新建、修改、启停、删除；云账户等凭证由专用命令在进程内使用，不会打印。`secret list|show` 只给打码后的信息。
- **本机身份和线上密钥不碰**：不读取、不打印、不手工拷贝或恢复 `~/.config/moox/` 下的操作员身份，也不读线上部署根的 `secrets/`；不在目标机器上手动执行 `moox-admin-cli bootstrap`、手工改 `version.json` 或单元文件；不手工执行 `rm` 清除部署，只用 `setup wipe`。遇到这些情况就停下，带着现状和选项问用户。
- **只在用户明确要求时发布或升级**，并且只发布用户点名的组件。
- **部署方式由检测决定**：`config init` 填完机器后立即 `setup validate`，再跑不带 `--yes` 的 `setup install` 预检。目标机没装过（`fresh`）直接走新安装；装过（`incomplete` 没装完或 `installed` 已装好）就把预检里的部署状态、数据格式比对结果、`--keep`（保留数据）与 `--wipe`（全部覆盖）两个选项各自会做什么给用户看，问清保留还是覆盖后再执行（[`references/install.md`](references/install.md) 第 3 节），不替用户选。
- 交易模块已支持现货、永续多市场执行（按策略目标自动下单，管理台上人工下单、撤单、清仓；保证金率风控触发时系统自动减仓），写操作也有 `trade …` 命令（账户与组合的创建修改、杠杆、成员、暂停恢复、人工下单撤单、清仓、模拟账户开关），默认只预览，`--yes` 才执行；实盘账户上的写操作（下单、撤单、清仓、暂停恢复、改设置、改杠杆）执行前要先得到用户在对话里的明确同意，模拟账户可以直接做。解除下架禁止仍由用户在管理台交易页操作。实盘前必须先跑测试网冒烟（`modules/trade/scripts/testnet-smoke.sh`，由用户或维护者执行，Agent 不执行）；币安统一账户的现货买单资金来源等未核对项要等用户本人小额实测（见 `docs/模块/交易.md` 第 12 节）。

## 按场景找文档

| 用户想做什么 | 读这个 |
| --- | --- |
| 第一次安装、部署、初始化，覆盖更新或卸载已装好的系统，或者在新电脑上接管已经装好的机器（下载产物，不用编译；一开始先测 GitHub 与 CNB 的网络，选更快的下载源） | [`references/install.md`](references/install.md) |
| 了解各模块、日常管理命令速查 | [`references/modules.md`](references/modules.md) |
| 全部命令一览（含低频命令，每个命令一行） | [`references/command-index.md`](references/command-index.md) |
| 管理台上的操作在命令行怎么做（专用命令对照表；少数底层接口用 `gateway call`） | [`references/api-recipes.md`](references/api-recipes.md) |
| 取采集的 K 线等数据 | [`references/data-query.md`](references/data-query.md) |
| 首页“N 项需要处理”、告警、数据长时间未更新 | [`references/monitor-attention.md`](references/monitor-attention.md) |
| 新增、部署、启停、移除机器；因子引擎与归档 Worker | [`references/host-registry.md`](references/host-registry.md) |
| 从源码目录部署、升级、回滚（维护者） | [`references/source-deploy.md`](references/source-deploy.md) |
| 清空生产后按默认配置重装 | [`references/fresh-deploy.md`](references/fresh-deploy.md) |
| 写因子、转换 Python 因子、修改已有因子 | [`references/factor-dsl.md`](references/factor-dsl.md) |
| 自定义算子（Rust → wasm） | [`references/udf.md`](references/udf.md) |
| 写策略 DSL、回放（结果 View 或因子现算）、创建策略定义与实例 | [`references/strategy-dsl.md`](references/strategy-dsl.md) |
| 迁移整套 Python 选币策略 | [`references/strategy-migration.md`](references/strategy-migration.md) |
| 导入历史 K 线 | [`references/initial-import.md`](references/initial-import.md) |
| 打开 A 股日线及其附加数据（复权因子、申万行业、财务），在策略里按行业、财务选股 | [`references/ashare-daily.md`](references/ashare-daily.md) |
| View / 因子水位落后、修复 View | [`references/view-catchup.md`](references/view-catchup.md)、[`references/cli-operations.md`](references/cli-operations.md) |
| Storage 职责、保留期与内存 | [`references/storage.md`](references/storage.md)、[`references/protocol.md`](references/protocol.md) |
| EventBus 凭据与轮换 | [`references/eventbus-credentials.md`](references/eventbus-credentials.md) |
| 主机资源监控（Host Agent） | [`references/host-agent.md`](references/host-agent.md) |
| 查询腾讯云 CLS 日志 | [`references/cls-query.md`](references/cls-query.md)、[`references/cls-api.md`](references/cls-api.md) |
| 从源码构建（维护者） | [`references/build.md`](references/build.md) |

## 命令行约定

- 用户解压的产物包目录（`moox-<版本>/`，或源码仓库根目录）就是工作目录：命令写成 `./bin/moox-cli …`，默认读取当前目录的 `moox.toml`。
- 产物包里有 `moox-bundle.json`：`setup` 命令自动使用包内预编译的 linux/amd64 程序，不需要 git、Go、Docker 或 Node。
- 管理台上的操作和各模块的主要操作都有专用命令（`factor`、`strategy`、`space`、`metadata`、`collector`（含云账号、代码包与云函数）、`monitor`、`eventbus`、`host`、`secret`、`trade`（查询与写操作）等），先用 `./bin/moox-cli <命令> --help` 看参数；命令对照表见 [`references/api-recipes.md`](references/api-recipes.md)。
- 没有专用命令的底层接口：`./bin/moox-cli gateway methods [关键字]` 查有哪些方法，`./bin/moox-cli gateway call <服务> <方法> --data @请求.json` 调用；业务失败时退出码非 0。
- 命令按 `moox.toml` 里的 SSH 配置连接各台主机，以 `moox-cli` 的身份签名调各模块的 RPC；`lifecycle`、`system`、`import` 也一样，隧道由命令自己建。要在某台主机上执行命令或传文件用 `host exec|get|put`。
- 带 `--space-id` 的命令默认空间是 `crypto`；时间范围用 `--from` / `--to`（RFC3339，左闭右开）。
- 本机有 HTTP 代理时，执行前 `env -u HTTP_PROXY -u HTTPS_PROXY`，否则到主机的直连会被挡住。
- 耗时的命令（部署、初始化、导入）放后台运行，盯输出里的 `*-ready` 与报错。

## 几条容易踩的规则

- **排查顺序固定**：先看 CLI 给出的错误码和原因，查 [`references/install.md`](references/install.md) 的常见问题；再用 `setup logs --host <主机> --component <组件>` 看日志（只读，不用再次授权）；还看不清就停下，向用户说清停在哪一步、已确认的事实和不确定的地方。不把产物包源码当主要排查手段，路线走不通时不要自己绕路。
- **重试**：重试前先说明这次改变了什么；什么都没变就不重试。只有 `ssh_disconnected` 可以直接用同样的参数重跑。
- **下结论要有证据**：没看到错误码或日志，不说“产物有问题”，不建议回退版本，不把资源不足、端口竞态这类猜测写成结论。
- **不手工处理进程**：不 `kill`、不上机 `stop`，也不用重启机器解决部署问题，这些都绕过了维护锁和身份核对。端口被占时按占用者处理：非 MooX 程序问用户；装过的机器让用户通过 `setup install` 预检选保留数据（`--keep`）或全部覆盖（`--wipe`），或单独卸载（`setup wipe`）；失败尝试的残留由 CLI 自动清理。
- **View 内存**：`moox-storage-view` 常驻内存约为全部 View 索引文件大小的 2.5～3 倍，首次建索引时再加最大索引的 1～1.5 倍。用户新建 View、给 View 加标的或因子列、
  调大 `[storage_retention] view_rows_per_series` 或放宽 `[storage_view.max_age]` 时，按 [`references/storage.md`](references/storage.md) 估算并提醒余量；
  办法是换更大的机器、减少根数、缩短年龄上限或减少 View，不要建议调高单个 DuckDB 的内存上限。
- **配置变更**：`moox.toml` 的改动（保留期、View 设置等）要重新部署对应主机才生效（`setup deploy --host <主机>`，配置每次部署都重新渲染）；个别组件要调的值写在 `[overrides]` 里。
  缩短保留期会在下次清理时删数据，先跟用户确认。不要手改主机上的配置文件，下次部署会被覆盖。
- **View 与因子恢复**：先分别量 Primary、View、Factor 三层水位；不要手删 View 索引或 Factor 的 durable consumer，用 `moox-cli` 的对应命令（不带 `--yes` 先预览）；View 跟随变更日志，没有 consumer。
- **主机与部署**：`t_hosts` / `t_deployments` 是唯一登记处，增删启停只走 `moox-cli setup …`，管理台只读。
- **监控告警**：先判断是真问题、已删除对象的残留，还是重启造成的瞬时现象；不要手改 `monitor.db`，不要把重启当修复手段。
- **采集或计划有问题**：不删任务、不用 `host exec` 改数据库、不用维护者脚本。先看 `collector status --space-id <空间>` 和 `setup verify` 给出的原因，走不通就停下来汇报。
- **重启单个组件**：用 `setup restart --host <主机> --component <组件> --yes`，不用重新部署整台主机；临时停掉用 `setup stop`，恢复用 `setup start-component`。
- **初始化后**：提醒用户在自己的终端让浏览器信任管理台 CA（[`references/install.md`](references/install.md) 第 5 节），不要替用户清理钥匙串里的这张证书。

### EventBus 凭据

EventBus 凭据只通过 `skills/moox/scripts/eventbus-credentials.sh` 签发与轮换，发布包和仓库里的 YAML 不含凭据。
角色令牌轮换后要同步到所有客户端（控制面服务、Storage、因子机等其他主机上用 EventBus 的组件、SCF；Host Agent 不用 EventBus）才算完成，按 [`references/eventbus-credentials.md`](references/eventbus-credentials.md) 执行。
控制机上的 `export` 不会更新其他主机上的组件。

### 腾讯云私有网络

SCF 函数运行在各地域的 VPC 里，只经同地域入口主机网关的外部入口（私网地址的 `11004`）访问 MooX，不直连 Storage 或 EventBus。
`[scf_plan]` 里的每个地域都要有一台配置了 `region`、`private_address` 并写了 `external_entry = true` 的主机；不要创建云联网（CCN）；函数需要访问公网行情源时保持 `public_net_status=ENABLE`。
配置规则与验收证据见 [`references/private-network.md`](references/private-network.md)。

### 腾讯云 CLS 日志

查询、核对 CLS 日志用 `skills/moox/scripts/cls_search.py`（[`references/cls-query.md`](references/cls-query.md)）。先用 `--query '*'` 和较窄的时间范围看原始记录，
确认 `service_name` 字段建了索引再按它过滤。汇报 Topic、时间范围、条数、RequestId，不打印 CLS 凭据；缺 SDK 时安装 `tencentcloud-sdk-python-cls`。

## 开发者（在源码仓库里工作时）

- 仓库是单根 `go.mod` 大仓：`modules/*` 是各服务，`packages/*` 是共享包，`web/` 是管理台，`docs/` 是设计文档。模块之间只经 RPC 与事件交互（静态门禁守护边界）。
- `make build`、`make test`、`make proto`、`make verify`（提交前的完整检查）；构建环境见 [`references/build.md`](references/build.md)。
- 对外发布：`scripts/release/build-release.sh vX.Y.Z` 生成五个平台的 `moox-cli` 与 linux/amd64 部署产物包。
- 协议与命名：用 Space、DataSource、Subject、Dataset、DatasetColumn、View（数据集的查询索引）、Factor；时间字段用 `start_time` / `end_time` / `snapshot_time`；`series_tag` 是单个可选标签。细节见 [`references/protocol.md`](references/protocol.md)。
