---
name: moox
description: Use for everything about the MooX quant system (MooX 量化系统) — installing and deploying it from release artifacts (安装, 部署, 初始化, 免密登录, moox-cli config init, setup bootstrap/deploy-host/deploy-unit/init), upgrading or rolling back production units (发布, 上线, 升级, 回滚, native release, service catalog rollout, 全新部署, 清空重装), and day-to-day management through moox-cli and gateway call (采集任务, 补采, K线合成, 标的排除, 存储 View 修复, 因子任务与补算, 策略定义与实例, 回放, 归档, 系统暂停与恢复, 空间, 主机登记). Also covers querying MooX采集数据 such as BTC-USDT crypto market queries and K-line/K线行情 queries, factor expression DSL (因子表达式, 因子 DSL, Python 因子转换, wasm UDF 自定义算子), strategy DSL (策略 DSL, rank/signal/tranche 规则, ValidateStrategy, 策略模板, 策略迁移, 选币策略), 首页“需要处理”/监控告警排查 (moox-cli doctor alerts, monitor-db-check), Linux Host Agent monitoring, EventBus credentials, EventBus rotate, Authorization Violation, FIN-WAIT-2, certificate signature failure, View watermark catch-up, repair-view, Factor period diagnostics and Recalc, Tencent Cloud firewall, CLS log queries, CCN/云联网, SCF 公网, SCF VPC, 内网组网, private-network, MOOX_ACCESS_ADDRESS, and control-plane maintenance.
---

# MooX

MooX 是自托管、单用户的量化系统：行情采集 → 数据存储 → 因子计算 → 策略目标 → 交易执行，配全链路监控、Web 管理台和 `moox-cli`。
部署在用户自己的云服务器上（可加一台局域网因子机）；需要调用云厂商接口的操作目前对接腾讯云 API，其他云厂商后续接入。用户只装这个 skill，部署、初始化和日常管理都由 Agent 用 `moox-cli` 完成。

## 安全边界（每次都要遵守）

- **`moox.toml` 是加密的私有配置**：Agent 只检查它是否存在，不读取、不打印、不复制、不 `source`，也不打开 `config init` / `config edit` / `config restore` 的本机页面；
  只有 `moox-cli` 自己读取其中的密钥。用户要看或改配置时运行 `./bin/moox-cli config edit`，让用户在自己的浏览器里操作。
- **密码与密钥不经过 Agent**：SSH 密码只在 `config init` 的免密登录页面里由用户输入一次；交易所、云账户等密钥由用户在管理台“设置 › 密钥”自己填写。
- **写操作先预览、后执行**：`setup` 类命令默认只预览，用户确认后才加 `--yes`；`gateway call` 的写方法先把请求给用户看。删除、清空、重置、回滚等破坏性操作必须得到用户明确同意。
- **只在用户明确要求时发布或升级**，并且只发布用户点名的组件。
- 交易模块尚未完工：不要代用户创建实盘账户、下单或清仓。

## 按场景找文档

| 用户想做什么 | 读这个 |
| --- | --- |
| 第一次安装、部署、初始化（下载产物，不用编译） | [`references/install.md`](references/install.md) |
| 了解各模块、日常管理命令速查 | [`references/modules.md`](references/modules.md) |
| 管理台上的操作在命令行怎么做（`gateway call` 配方） | [`references/api-recipes.md`](references/api-recipes.md) |
| 取采集的 K 线等数据 | [`references/data-query.md`](references/data-query.md) |
| 首页“N 项需要处理”、告警、数据长时间未更新 | [`references/monitor-attention.md`](references/monitor-attention.md) |
| 新增、部署、启停、移除机器；因子引擎与归档 Worker | [`references/host-registry.md`](references/host-registry.md) |
| 升级、回滚已安装的单元 | [`references/native-release.md`](references/native-release.md) |
| 清空生产后按默认配置重装 | [`references/fresh-deploy.md`](references/fresh-deploy.md) |
| 写因子、转换 Python 因子、修改已有因子 | [`references/factor-dsl.md`](references/factor-dsl.md) |
| 自定义算子（Rust → wasm） | [`references/udf.md`](references/udf.md) |
| 写策略 DSL、创建策略定义与实例 | [`references/strategy-dsl.md`](references/strategy-dsl.md) |
| 迁移整套 Python 选币策略 | [`references/strategy-migration.md`](references/strategy-migration.md) |
| 导入历史 K 线 | [`references/initial-import.md`](references/initial-import.md) |
| View / 因子水位落后、修复 View | [`references/view-catchup.md`](references/view-catchup.md)、[`references/cli-operations.md`](references/cli-operations.md) |
| Storage 职责、保留期与内存 | [`references/storage.md`](references/storage.md)、[`references/protocol.md`](references/protocol.md) |
| EventBus 凭据与轮换 | [`references/eventbus-credentials.md`](references/eventbus-credentials.md) |
| 主机资源监控（Host Agent） | [`references/host-agent.md`](references/host-agent.md) |
| 查询腾讯云 CLS 日志 | [`references/cls-query.md`](references/cls-query.md)、[`references/cls-api.md`](references/cls-api.md) |
| 从源码构建（维护者） | [`references/build.md`](references/build.md) |

## 命令行约定

- 用户解压的产物包目录（`moox-<版本>/`，或源码仓库根目录）就是工作目录：命令写成 `./bin/moox-cli …`，默认读取当前目录的 `moox.toml`。
- 产物包里有 `moox-bundle.json`：`setup` 命令自动使用包内预编译的 linux/amd64 程序，不需要 git、Go、Docker 或 Node。
- 没有专用命令的管理接口：`./bin/moox-cli gateway methods [关键字]` 查有哪些方法，`./bin/moox-cli gateway call <服务> <方法> --data @请求.json` 调用。
- 本机有 HTTP 代理时，执行前 `env -u HTTP_PROXY -u HTTPS_PROXY`，否则到主机的直连会被挡住。
- 耗时的命令（部署、初始化、导入）放后台运行，盯输出里的 `*-ready` 与报错。

## 几条容易踩的规则

- **View 内存**：`moox-storage-view` 常驻内存约为全部 View 索引文件大小的 2.5～3 倍，重建时再加最大索引的 1～1.5 倍。用户新建 View、给 View 加标的或因子列、
  调大 `[storage_retention] view_rows_per_series` 或放宽 `[storage_view.max_age]` 时，按 [`references/storage.md`](references/storage.md) 估算并提醒余量；
  办法是换更大的机器、减少根数、缩短年龄上限或减少 View，不要建议调高单个 DuckDB 的内存上限。
- **配置变更**：`moox.toml` 的改动（保留期、View 设置等）只有重新安装对应单元才生效（`setup release build` 加该单元的命令带 `--release`）；只换二进制保留已安装的配置。
  缩短保留期会在下次清理时删数据，先跟用户确认。不要手改主机上的配置文件。
- **View 与因子恢复**：先分别量 Primary、View、Factor 三层水位；不要手删 durable consumer 或 View 索引，用 `moox-cli` 的对应命令（先 dry-run）。
- **主机与部署**：`t_hosts` / `t_deployments` 是唯一登记处，增删启停只走 `moox-cli setup …`，管理台只读。
- **监控告警**：先判断是真问题、已删除对象的残留，还是重启造成的瞬时现象；不要手改 `monitor.db`，不要把重启当修复手段。
- **初始化后**：macOS 上管理台用内部证书时，提醒用户让浏览器信任管理台 CA（[`references/install.md`](references/install.md) 第 4 节），不要替用户清理钥匙串里的这张证书。

### EventBus 凭据

EventBus 凭据只通过 `skills/moox/scripts/eventbus-credentials.sh` 签发与轮换，发布包和仓库里的 YAML 不含凭据。
CA 或角色令牌轮换后要同步到所有客户端（控制面服务、Storage、每台主机的 Host Agent、SCF）才算完成，按 [`references/eventbus-credentials.md`](references/eventbus-credentials.md) 执行。
Control `export` does not update Host Agents on other machines：控制机上的 export 不会更新其他主机的 Host Agent。

### Tencent Private Network

SCF 函数运行在各地域的 VPC 里，只经同地域 Access 主机的私网地址（`11004`）访问 MooX，不直连 Storage 或 EventBus。
`[scf_plan]` 里的每个地域都要有一台配置了 `region` 与 `private_address` 的 Access 主机；不要创建云联网（CCN）；函数需要访问公网行情源时保持 `public_net_status=ENABLE`。
配置规则与验收证据见 [`references/private-network.md`](references/private-network.md)。

### 腾讯云 CLS 日志

查询、核对 CLS 日志用 `skills/moox/scripts/cls_search.py`（[`references/cls-query.md`](references/cls-query.md)）。先用 `--query '*'` 和较窄的时间范围看原始记录，
确认 `service_name` 字段建了索引再按它过滤。汇报 Topic、时间范围、条数、RequestId，不打印 CLS 凭据；缺 SDK 时安装 `tencentcloud-sdk-python-cls`。

## 开发者（在源码仓库里工作时）

- 仓库是 Go Workspace：`modules/*` 是各服务，`packages/*` 是共享包，`web/` 是管理台，`docs/` 是设计文档。模块之间只经 RPC 与事件交互。
- `make build`、`make test`、`make proto`、`make verify`（提交前的完整检查）；构建环境见 [`references/build.md`](references/build.md)。
- 对外发布：`scripts/release/build-release.sh vX.Y.Z` 生成五个平台的 `moox-cli` 与 linux/amd64 部署产物包。
- 协议与命名：用 Space、DataSource、Subject、Dataset、View、Field、Factor；时间字段用 `start_time` / `end_time` / `snapshot_time`；`series_tag` 是单个可选标签。细节见 [`references/protocol.md`](references/protocol.md)。
