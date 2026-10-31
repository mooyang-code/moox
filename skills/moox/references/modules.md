# 各模块怎么用（Agent 日常管理速查）

每个模块列出：做什么、在哪台机器、常用命令、细节文档。管理台上的操作和各模块的主要操作都有专用命令；
没有专用命令的底层接口才用 `moox-cli gateway call`，请求写法见 [`api-recipes.md`](api-recipes.md)。
命令都在产物包（或仓库）根目录执行，默认读取当前目录的 `moox.toml`（`--file` 指定），不需要手工建隧道或设置环境变量。
交易模块尚未完工：只能查询（`trade …`），不代用户创建账户、下单或清仓。

**通用约定**

- 删除、重启、改防火墙、切换线上版本这类破坏性命令，不带 `--yes` 只预览（输出 `preview: true` 的 JSON），把预览给用户确认后再加 `--yes`。
- 命令输出是 JSON；业务失败（`ret_info.code` 非 0）时退出码非 0。
- 每个命令的 `--help` 写明对应的接口；字段多的写操作可以用 `--data @文件` 给出完整请求，各参数优先于它。
- 带 `--space-id` 的命令默认空间是 `crypto`。

## 数据模型（先理解这几个词）

| 概念 | 含义 |
| --- | --- |
| Space（空间） | 一组相互隔离的业务数据，默认有 `crypto`（加密货币）、`stockcn`（A 股，默认停用）等 |
| DataSource（数据来源） | 一个上游，如币安现货、币安合约 |
| Subject（标的） | 空间里的对象，如 `BTC-USDT`；Tag（标签）是一组标的，采集任务按标签选标的 |
| Dataset（数据集） | 某来源的一类数据（某频率的 K 线、因子结果等），绑定到 Storage 的 DataNode |
| Field / 列 | 数据集的字段；因子结果是结果数据集的列 |
| View | 面向查询的宽表，由 Storage 异步构建，可重建 |

## 采集

- **做什么**：按采集任务定时拉取或经 WebSocket 推送 K 线，写入 Storage；巡检缺口、补采、K 线合成（重采样）、每小时同步交易所标的。
- **在哪**：Collector 在管理台机；`collector-stream`（WS 推送与本地执行端）在海外机；可选的腾讯云 SCF 函数按地域部署。
- **常用**：

```bash
./bin/moox-cli collector status --space-id crypto                     # 总体健康、各数据集巡检进度与缺口
./bin/moox-cli collector task list|show|update|disable|inventory      # 任务查询、改名与启停
./bin/moox-cli collector task create --help                           # 新建 K 线采集任务
./bin/moox-cli collector kline check|backfill|backfill-status ...     # 检查与补采
./bin/moox-cli collector resample preview|backfill|jobs|cancel ...    # 历史 K 线合成
./bin/moox-cli collector subjects exclude|unexclude|exclusions|sync|status|listers
./bin/moox-cli collector ingest-mode get|set                          # WS 推送 / 定时拉取
./bin/moox-cli collector fetch-plan pause|resume                      # 暂停或恢复整个空间的采集
./bin/moox-cli collector data-type list|show                          # 可采集的数据类型
```

- 删除任务与结果不可恢复：`collector task delete`、`delete-result` 默认只预览，`--yes` 才执行。
- 云函数：`cloud function list|job|invoke|create|deploy|update-config|delete`、`cloud package list|show|url|delete`、`cloud scf preview|import`、`cloud account list|regions|delete`；
  创建、部署、改配置、删除会自动持有采集发布租约；代码包上传用 `collector function upload`；云账户的新建与修改带密钥，请用户在管理台填写。
- 长历史导入：[`initial-import.md`](initial-import.md)。取 K 线数据：[`data-query.md`](data-query.md)。

## 存储（Storage）

- **做什么**：元数据目录（primary，SQLite）、字段级事实（node，Pebble）、可重建的 View（view，DuckDB/Bleve）。
- **在哪**：存储机，三个角色同机，DataNode ID 固定 `storage-node-0`（`setup init` 自动登记）。
- **常用**：
  - 元数据：`metadata source|subject|tag|dataset|field|field-group|view|device list|show|create|update|delete`；种子导入 `metadata apply|import`；手动标签 `metadata tag members|clone`。
  - 读数据：`data kline get`、`data rows export|count`、`data view query`、`data records search`。
  - 维护：`storage repair-view|force-rebuild-view|reset-view-consumers|reset-metadata|delete-view|purge-dataset-events|period-inventory`
    （经 SSH 在存储机上执行，不带 `--yes` 只预览；见 [`cli-operations.md`](cli-operations.md)、[`view-catchup.md`](view-catchup.md)）；节点登记 `storage node list|show|update|delete`。
- **注意**：新建 View、加标的、调大 `view_rows_per_series` 都会增加存储机内存，按 [`storage.md`](storage.md) 估算并提醒用户。保留期等配置改在 `moox.toml` 后要重装对应单元才生效。
- 协议与命名规则：[`protocol.md`](protocol.md)。

## 因子

- **做什么**：用 YAML 表达式定义因子，按计算任务（源数据集 + 频率 → 结果数据集）在每个采集周期增量计算；支持历史补算和 Rust 写的 wasm 自定义算子。
- **在哪**：因子管理（factor-mgr）在管理台机；因子引擎在因子机（systemd 用户服务）。
- **常用**：写因子与转换 Python 因子按 [`factor-dsl.md`](factor-dsl.md)；
  - 定义与分类：`factor list|show|create|update|delete|validate|preview|functions`、`factor category list|upsert|delete|assign`；
  - 任务与成员：`factor task list|show|create|enable|disable|delete|params`、`factor member add|remove|enable|disable`；批量配置写进 `moox.toml` 的 `[factors]` 后 `./bin/moox-cli setup factors`；
  - 补算与状态：`factor recalc submit|list|show|cancel`（左闭右开）、`factor status`；
  - 阶段控制 `./bin/moox-cli lifecycle status|check|init|start|confirm|pause|resume --task-id <任务>`（默认连因子引擎，`--module` 换模块）；
  - 自定义算子 `./bin/moox-cli udf …`（[`udf.md`](udf.md)）。
- **注意**：被启用成员引用的定义不能改；长回看因子受保留期、`window.lookback`、`compute.max_memory` 三道限制。

## 策略

- **做什么**：声明式 DSL（`rank` / `signal` / `tranche` 规则、止损止盈、模板），每个周期在因子结果上求值，输出截面目标权重；可历史回放。
- **在哪**：管理台机。
- **常用**：写与校验 DSL、创建定义与实例按 [`strategy-dsl.md`](strategy-dsl.md)；
  `strategy list|show|validate|create|update|delete`、`strategy instance list|show|create|update|enable|disable`、`strategy result list|show`、`strategy target list`、
  `strategy template list|show|save|delete|render`、`strategy display get|set`；策略服务里的历史回放 `strategy replay start|list|show|bars|cancel`；
  逐期回放（因子 → 策略 → 纸面成交）用 `./bin/moox-cli replay start|status|report …`。迁移整套 Python 选币策略：[`strategy-migration.md`](strategy-migration.md)。
- **注意**：一个因子任务只能关联一个实例；改定义前先停用引用它的实例。不挂账户的实例只计算目标、不下单。

## 监控

- **做什么**：组件健康、数据新鲜度、主机资源、告警推送（企业微信 / 飞书）。管理台首页的“需要处理”来自这里。
- **常用**：`./bin/moox-cli doctor alerts`（[`monitor-attention.md`](monitor-attention.md)）、`doctor bootstrap`、`doctor diagnose`；
  `monitor health|arrival|hosts|host-metrics` 看总览、数据到达历史与主机资源曲线；`monitor mute|unmute|mute-status` 临时屏蔽推送；
  告警渠道以 `moox.toml` 为准，`config notification` 同步。

## 网关与接入

- **主机网关**：每台主机一个，校验签名和服务目录 ACL 后转发到本机服务；管理台与 CLI 的所有调用都经过它。
- **Access**：外部签名调用入口（SCF、因子引擎、Skill 读数据），存储机与海外机各一个。
- **EventBus**：模块间异步事件（NATS JetStream），默认在存储机；`eventbus overview|streams|consumers|consumer|events` 只读查看。凭据轮换必须按 [`eventbus-credentials.md`](eventbus-credentials.md) 同步到所有客户端。
- **出口代理**：海外机上为采集提供受限的 HTTPS 出口与 DNS。
- SCF 与私有网络：[`private-network.md`](private-network.md)。

## 主机与运维

- 新增、部署、启停、移除主机和部署，外部程序（因子引擎、归档 Worker）的安装与退役：[`host-registry.md`](host-registry.md)。管理台对这些只读；查询用 `host list|deployments|routes|catalog|directory`、`host agent status|snapshot|run-once --host <主机>`。
- 要在某台主机上执行一条命令或传文件：`host exec --host <ID> -- <命令>`、`host get|put`（经 SSH，退出码透传；只能出站的主机不能 SSH）。会改动主机的命令先把完整命令给用户确认。
- 升级已安装的单元：[`native-release.md`](native-release.md)；主机代理：[`host-agent.md`](host-agent.md)。
- 腾讯云：`setup firewall`（防火墙）、`setup reboot-host`（重启无响应的主机）、`ops tencent cls …`（日志服务，查询见 [`cls-query.md`](cls-query.md)）；这些写操作默认只预览，`--yes` 才执行。

## 归档（只有命令行）

定时把事实数据按日 / 月导出成 CSV（或 CSV.GZ），Worker 跑在因子机。

```bash
./bin/moox-cli archive tasks prepare --space-id crypto --dataset-id <数据集>   # 看频率、保留期与推荐执行计划
./bin/moox-cli archive tasks create --space-id crypto --dataset-id <数据集> --cron "0 30 2 * * *"
./bin/moox-cli archive overview | runs list | partitions list|get
./bin/moox-cli archive tasks backfill|run|update|delete ...
```

## 系统暂停、恢复与重置（只有命令行）

```bash
./bin/moox-cli system status     # 按 moox.toml 自动连各模块，没有部署的模块跳过
./bin/moox-cli system pause      # 从下游到上游逐个暂停
./bin/moox-cli system resume     # 先追赶补采，全部追平后统一放行
./bin/moox-cli system reset plan|apply|status|abort   # 删除运行数据回到“刚部署完”的状态，破坏性，必须先得到用户明确确认
```

只能出站的主机（如局域网因子机）读不到模块自己的重置专用密钥，`system reset` 需要用户提供环境变量 `MOOX_<模块>_HEALTH_RESET_ACCESS_KEY` / `_SECRET_KEY`。

## 空间、密钥与配置

- 空间：`space list|show|create|update|delete`；新空间还要在 Storage 建元数据（数据来源、数据集、字段），用 `metadata apply` 从 `config/setup/metadata.yaml` 的写法导入。
- 密钥（交易所、云账户等）：请用户在管理台“设置 › 密钥”自己填写，Agent 只用 `secret list|show` 看清单（值已打码）。`gateway call` 拒绝返回明文的方法。
- `moox.toml`：Agent 用 `config get` / `config set` 读写（[`config-access.md`](config-access.md)），不直接读文件；用户要看或改时运行 `config edit`，让用户在自己的浏览器里操作；`config backup` 备份到管理台，换电脑 `config restore`。
