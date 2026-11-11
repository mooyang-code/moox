# 各模块怎么用（Agent 日常管理速查）

每个模块列出：做什么、在哪台机器、常用命令、细节文档。管理台上的操作和各模块的主要操作都有专用命令；
没有专用命令的底层接口才用 `moox-cli gateway call`，请求写法见 [`api-recipes.md`](api-recipes.md)。
命令都在产物包（或仓库）根目录执行，默认读取当前目录的 `moox.toml`（`--file` 指定），不需要手工建隧道或设置环境变量。
交易模块已支持现货、永续多市场执行与人工下单，但 Agent 只用 `trade …` 查询：不代用户创建或修改账户、下单、撤单、清仓或解除下架禁止，也不向交易所下真实订单；实盘前须先跑测试网冒烟，币安统一账户的现货买单资金来源等要等用户本人小额实测（`docs/模块/交易.md` 第 12 节）。

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

## 事件中心

- **做什么**：接收 EventBus 里可以配置规则的事件（数据集一期完成、View 数据就绪、K 线补写、标的即将下架），按用户配置的规则向用户自己的接口发通知（带 HMAC 签名，失败自动重试），或者执行系统动作（标的下架时禁止买入、下架前平仓；处理器随交易计划 T27 之后上线）。管理台叫「事件中心」。
- **在哪**：独立单元 `callback-dispatcher`，默认海外机（`[deployments]` 决定）；该主机必须能访问到用户的通知地址。
- **常用**：

```bash
./bin/moox-cli event catalog                                      # 可配置的事件与可限定的范围
./bin/moox-cli event rules list|get|update|delete|test            # 事件规则；新建规则返回验证密钥明文，Agent 不新建，请用户在管理台「事件中心」的“订阅”页签新建
./bin/moox-cli event executions list|get|retry|cancel|ack         # 执行记录：失败重试、取消（下架处理会同时解除禁止）、确认
./bin/moox-cli collector delist register --exchange binance --market SPOT --symbol FOOUSDT --delist-at-ms <毫秒> [--paper-only] --yes   # 手工登记下架（会触发系统规则）
```

- 手工登记下架的后果：登记后立即禁止买入，到下架前 24 小时（T−24h，已不足 24 小时的立即）会自动卖出策略持有的实盘仓位；`--yes` 前先向用户确认标的和下架时间；演练用 `--paper-only`（只平模拟盘组合，但同样全局禁止买入这个标的，直到演练记录结束）。管理台「事件中心」“事件”页签里“标的即将下架”一行的“手工登记”效果相同。

- 库文件没有迁移：事件中心启动时发现表结构是旧的（报“数据库是旧版本的结构”）就拒绝启动，要先停服务、删库文件（`data/moox_callback_dispatcher.db*`）再启动，和用户确认后再做。
- 设计与排查：[`docs/模块/事件中心.md`](../../../docs/模块/事件中心.md)；告警（`event_center_attention`）见 [`monitor-attention.md`](monitor-attention.md)。

## 采集

- **做什么**：按采集任务定时拉取或经 WebSocket 推送 K 线，写入 Storage；巡检缺口、补采、K 线合成（重采样）、每小时同步交易所标的。
- **在哪**：Collector 在管理台机；`collector-stream`（WS 推送与本地执行端）在海外机；可选的腾讯云 SCF 函数按地域部署。
- **常用**：

```bash
./bin/moox-cli collector status --space-id crypto                     # 总体健康、各数据集巡检进度与缺口
./bin/moox-cli collector task list|show|update|disable|inventory      # 任务查询、改名与启停
./bin/moox-cli collector task create --help                           # 新建 K 线采集任务
# 币安永续 1h/1d 可选资金费率：--output-field 会替换默认字段，要把默认字段一起列出，例如
# --output-field open,high,low,close,volume,quote_volume,trade_num,funding_rate；建完用 ingest-mode get 确认是 stream 加 local
./bin/moox-cli collector kline check|backfill|backfill-status ...     # 检查与补采
./bin/moox-cli collector kline backfill-cancel|backfill-retry ...     # 取消补采作业；重试失败的分块
./bin/moox-cli collector kline history-confirm|absences-clear ...     # 按历史扫描的缺口补采并确认“行情源本来没有”的 K 线；行情源后来补上数据时清除这些确认
./bin/moox-cli collector resample preview|backfill|jobs|cancel ...    # 历史 K 线合成
./bin/moox-cli collector subjects exclude|unexclude|exclusions|sync|status|listers
./bin/moox-cli collector ingest-mode get|set                          # WS 推送 / 定时拉取
./bin/moox-cli collector fetch-plan pause|resume                      # 暂停或恢复整个空间的采集（pause 默认只预览，--yes 才执行）
./bin/moox-cli collector data-type list|show                          # 可采集的数据类型
```

- 删除任务与结果不可恢复：`collector task delete`、`delete-result` 默认只预览，`--yes` 才执行。`collector task purge` 清理 Collector 本机库里的任务运行数据与结果引用（默认只输出清单，`--yes` 才执行），属于重置前后的维护动作，先问用户。
- Storage 数据被清空后，要让采集重新登记序列快照并重置补采状态：`collector fetch-plan reregister --space-id <空间> --yes`（系统重置后的必做步骤，见 [`fresh-deploy.md`](fresh-deploy.md)）。
- SCF 函数包与发布：`collector function package`（构建代码包）、`upload`（构建或使用现成的包并只上传，不创建函数）、`delete`（按条件异步删除函数，默认只列目标）、
  `probe-egress`（逐个调用短时函数诊断公网出口与行情源连通性）、`timer-inventory`（只读盘点 Timer 函数并校验云侧读回）。
- 云函数：`cloud function list|job|invoke|create|deploy|update-config|delete`、`cloud package list|show|url|delete`、`cloud scf preview|import`、`cloud account list|regions|delete`；
  创建、部署、改配置、删除会自动持有采集发布租约；代码包上传用 `collector function upload`；云账户的新建与修改带密钥，请用户在管理台填写。
- 附加字段（K 线行情之外的可选列：币安永续的资金费率，A 股日线的复权因子、申万行业、财务）只能在建任务时用 `--output-field` 选全，任务建好后只能减少、不能新增；`collector data-type show` 的 `extra_fields` 列出每个字段适用的市场、品种和频率。A 股日线的完整步骤见 [`ashare-daily.md`](ashare-daily.md)。
- 长历史导入：[`initial-import.md`](initial-import.md)。取 K 线数据：[`data-query.md`](data-query.md)。

## 存储（Storage）

- **做什么**：元数据目录（primary，SQLite）、字段级事实（node，Pebble）、可重建的 View（view，DuckDB/Bleve）。
- **在哪**：存储机，三个角色同机，DataNode ID 固定 `storage-node-0`（`setup init` 自动登记）。
- **常用**：
  - 元数据：`metadata source|subject|tag|dataset|field|field-group|view|device list|show|create|update|delete`；种子导入 `metadata apply|import`；手动标签 `metadata tag members|clone`。
  - 读数据：`data kline get`、`data rows export|count`、`data view query`、`data records search`。
  - 维护：`storage repair-view|force-rebuild-view|reset-view-consumers|reset-metadata|delete-view|purge-dataset-events|period-inventory`
    （经 SSH 在存储机上执行，不带 `--yes` 只预览；见 [`cli-operations.md`](cli-operations.md)、[`view-catchup.md`](view-catchup.md)）；节点登记 `storage node list|show|update|delete`。
- **其他**：`metadata spaces` 列出一份种子文件里可导入的业务空间；`setup storage-auth` 经 SSH 从存储机读一次 Primary 角色密钥存到本机操作员目录（`import`、`data rows export` 要用，只需一次，不打印）。
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
  - 生命周期的其余命令：`lifecycle catchup`（恢复追赶进度）、`release`（追平后手动放行，配合 `resume --manual-release`）、`shadow --space-id <空间> --id <实例>`（读 Trade 为试运行实例记录的影子目标，固定连 trade 模块）；
    单个模块的重置 `lifecycle reset-plan|reset-apply|reset-status|reset-abort`：先 `pause` 模块，`reset-plan --scope data|all` 只读列出每一步与阻止条件并给 10 分钟内有效、只能用一次的令牌，再 `reset-apply --token … --scope … --yes`；
    会删除运行数据，必须先得到用户明确同意，整套系统的重置用 `system reset`；
  - 自定义算子 `./bin/moox-cli udf new|build|publish|preview|activate|list|show`（[`udf.md`](udf.md)；`activate` 默认只看影响清单，`--yes` 才切换）。
- **注意**：被启用成员引用的定义不能改；长回看因子受保留期、`window.lookback`、`compute.max_memory` 三道限制。

## 策略

- **做什么**：声明式 DSL（`rank` / `signal` / `tranche` 规则、止损止盈、模板），每个周期在因子结果上求值，输出截面目标权重；可历史回放。
- **在哪**：管理台机。
- **常用**：写与校验 DSL、创建定义与实例按 [`strategy-dsl.md`](strategy-dsl.md)；
  `strategy list|show|validate|create|update|delete`、`strategy instance list|show|create|update|enable|disable`、`strategy result list|show`、`strategy target list`、
  `strategy template list|show|save|delete|render`、`strategy display get|set`；策略服务里的历史回放 `strategy replay start|list|show|bars|cancel`；
  逐期回放（因子 → 策略 → 纸面成交）用 `./bin/moox-cli replay start|status|report …`。迁移整套 Python 选币策略：[`strategy-migration.md`](strategy-migration.md)。
- **市场模式**：DSL 顶层 `market` 的 `select`（从现货、永续还是两边选币）与 `prefer`（两边都能交易时优先在哪个市场下单）只有 5 种合法组合，
  决定实例要绑几个因子任务（[`strategy-dsl.md`](strategy-dsl.md) 的“market：市场模式”）。
- **双任务与统一数据集**：同时涉及现货和永续时有两种绑法——现货、永续各绑一个因子任务（`--factor-task spot=… --factor-task swap=…`，两个结果 View 本期都到齐才求值，
  一个市场的因子本期被跳过时只让这个市场不可用）；或绑统一数据集（同一数据集里放现货、永续两条序列）上的一个任务，写 `--factor-task multi=…`，只能单独绑定，到达即求值，
  不支持 `factor_params`、不能进策略池、不支持闭环回放。
- **策略池**：定义里写 `strategy_pool`（与 `rules` 互斥），成员引用已有策略定义，各自在模拟账本里算净值，按 `fixed` 比例或 `rotation` 轮动分配资金；
  启用后先预热，预热完成前不求值、不能 `confirm`；研究回放与闭环回放不支持（[`strategy-dsl.md`](strategy-dsl.md) 的“strategy_pool：策略池”）。
- **注意**：一个因子任务只能关联一个实例，绑定在创建实例时选定、之后不能改；改定义前先停用引用它的实例。不挂账户的实例只计算目标、不下单。

## 交易（只读）

- **做什么**：执行账户与组合账户、目标收敛下单、订单成交持仓、模拟盘；实盘风控：下架名单（名单标的只减不增，下架前由事件中心清仓）、保证金率监控（目前只监控币安生产环境的实盘账户，跌破阈值自动暂停组合并按比例减仓）。
- **在哪**：独立单元 `trade`，默认海外机（`[deployments]` 决定）。
- **常用**（都是只读，写操作由用户在管理台做）：

```bash
./bin/moox-cli trade logical list|show|target                      # 组合账户与当前策略目标
./bin/moox-cli trade order list --logical-account-id <ID> [--owner-type TARGET|OPERATOR|EXTERNAL|SYSTEM]   # 订单，可按来源过滤
./bin/moox-cli trade actions --logical-account-id <ID> [--action-type REDUCE]   # 人工操作记录（清仓、减仓、下架清仓…）
./bin/moox-cli trade pnl --logical-account-id <ID>                 # 收益拆解：手续费、已实现盈亏、资金费（时间范围最长 90 天，取不全时标明截断）
./bin/moox-cli trade blocked list                                  # 下架名单（全局）
./bin/moox-cli trade holders --exchange EXCHANGE_BINANCE --market-type MARKET_TYPE_SPOT --exchange-symbol FOOUSDT   # 策略持有某标的的执行账户（跨空间）
```

- **注意**：Agent 不创建实盘账户、不下单、不清仓、不解除下架禁止（用户在管理台交易页的下架名单里查看与解除）；保证金率监控的阈值在组合的资金设置里，触发后组合会自动暂停，等用户确认安全后在管理台手动恢复。
- 设计与排查：[`docs/模块/交易.md`](../../../docs/模块/交易.md)；告警（`trade_attention`，含 `margin_ratio_low`）见 [`monitor-attention.md`](monitor-attention.md)。

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
- 腾讯云：`setup firewall`（按拓扑放行所需端口）、`ops tencent lighthouse firewall add|open`（单独加规则；`open` 用控制面登记的云账户凭证，凭证不打印）、`setup reboot-host`（重启无响应的主机）、
  `setup attach-scf-vpc`（为函数绑定 VPC 子网）、`setup inspect-scf|invoke-scf`（检查函数网络配置、直接调用函数）、`ops tencent cls bootstrap|prepare|resolve`（日志服务，查询见 [`cls-query.md`](cls-query.md)）；写操作默认只预览，`--yes` 才执行。
- 排查与准备：`setup host-diagnostics --host <ID>` 检查远端主机磁盘与服务进程；`setup render-runtime-config` 从 `moox.toml` 渲染 Egress、Collector 运行配置；`setup package inspect <包>` 校验部署包的组件边界、平台与摘要。

## 归档

把事实数据按日 / 月导出成 CSV（或 CSV.GZ），Worker 跑在因子机。不填执行计划：每收盘 N 根（`--pull-every-bars`，默认 1）拉一次新收盘的几根，
低于 1d 的另有每小时第 34 分的复核（推进水位），1d 及以上每天一次。读取避开采集触发时刻：规则由 archive-mgr 按有任务的空间的采集频率推出并下发给 worker，`archive overview` 的 `avoidance` 可见。
管理台“数据归档 › 归档任务”管任务、下载分区（远端已校验的）、看最新数据，“归档存储”看执行器所在主机、根目录、磁盘与避让规则；CSV 导入与按时间区间清除无法补齐区间只有命令行（`archive import`、`archive tasks clear-unrecoverable`）；整体清除任务累积的无法补齐区间在管理台任务详情“清除这些记录”和 `archive tasks update --clear-unrecoverable` 都能做。
分区路径 `<空间>/<用途>/<目录名>/<yyyy>/<yyyymmdd>.csv.gz`（1d 及以上是月分区 `<yyyymm>`；worker 配置 `export_format: csv` 时扩展名为 `.csv`），用途 `collect`（采集结果）/ `factor`（因子结果）/ `data`，目录名建任务时定（`tasks prepare` 给建议值，`tasks create --dir-name` 可改），建好后不改；归档根目录的 `datasets.csv` 是目录与数据集的对照表。

```bash
./bin/moox-cli archive tasks prepare --space-id crypto --dataset-id <数据集>   # 看频率、保留期与推荐的拉取间隔
./bin/moox-cli archive tasks create --space-id crypto --dataset-id <数据集> [--pull-every-bars 5]
./bin/moox-cli archive tasks list|preview               # 列出任务；预览接下来的拉取、复核时刻与分区导出时间
./bin/moox-cli archive overview | runs list|get|cancel | partitions list|get
./bin/moox-cli archive tasks backfill|run|update|delete ...
./bin/moox-cli archive worker retire --worker <ID> --yes # 退役换机后遗留的旧 worker 登记
./bin/moox-cli archive import template --space-id crypto --dataset-id <数据集>        # 导入文件的表头
./bin/moox-cli archive import --space-id crypto --dataset-id <数据集> --csv <文件.csv|.csv.gz> [--overwrite] --wait --yes
./bin/moox-cli archive tasks clear-unrecoverable --space-id crypto --dataset-id <数据集> --start <RFC3339> --end <RFC3339> --yes
```

导入：经 SSH 把文件传到 archive-worker 所在主机的 `<归档根目录>/.import/<import_id>/`，worker 先整份校验（有一行不对就整份拒绝、什么都不写，消息列前 20 条带行号的错误），再分块写入；默认同键已存在时只补空缺，`--overwrite` 覆盖（必须带齐全部值列）。
导入不推进水位、不自动清无法补齐区间，补齐历史缺口后确认再用 `clear-unrecoverable` 清。规则细节见 `docs/模块/归档.md` 第 9 节“CSV 导入”。

对象存储副本：默认用随 archive-worker 部署的内置 SeaweedFS（`moox-seaweedfs`，只听 127.0.0.1，和归档文件同盘，不防整盘故障）；要异地副本就在 `moox.toml` 写 `[archive_object_store]` 改用外部 S3（只写 endpoint、region、bucket、url_style、storage_class 与管理台密钥的 `secret_id`，不写密钥值；第一次加只能用 `config edit` 的“归档对象存储”一组）。
worker 每次导出就把分区单次 PUT 到对象存储、读回时本地缺文件从远端取回；用外部 S3 时换机器不用拷目录。对象存储连续不可用超过 5 分钟，Monitor 推送“归档对象存储”告警。看状态：`archive tasks list` 的 `remote_pending_count` / `remote_verified_count`，
`archive partitions get` 的 `remote_state`（off / none / verified / failed / pending），`archive runs list` 的 `partitions_uploaded`。导出超过 24 小时没有已校验的远端副本算需关注、超过 3 天算异常。
密钥、桶与权限的准备见 `docs/部署与运维.md` 的“归档对象存储”；worker 侧细节见 `docs/模块/归档.md` 第 9 节。

## 系统暂停、恢复与重置（只有命令行）

```bash
./bin/moox-cli system status     # 按 moox.toml 自动连各模块，没有部署的模块跳过
./bin/moox-cli system pause      # 从下游到上游逐个暂停（默认只预览将要暂停的模块，--yes 才执行）
./bin/moox-cli system resume     # 先追赶补采，全部追平后统一放行
./bin/moox-cli system reset plan|apply|status|abort   # 删除运行数据回到“刚部署完”的状态，破坏性，必须先得到用户明确确认
```

只能出站的主机（如局域网因子机）读不到模块自己的重置专用密钥，`system reset` 需要用户提供环境变量 `MOOX_<模块>_HEALTH_RESET_ACCESS_KEY` / `_SECRET_KEY`。

## 空间、密钥与配置

- 空间：`space list|show|create|update|delete`；新空间还要在 Storage 建元数据（数据来源、数据集、字段），用 `metadata apply` 从 `config/setup/metadata.yaml` 的写法导入。
- 密钥（交易所、云账户等）：请用户在管理台“设置 › 密钥”自己填写，Agent 只用 `secret list|show` 看清单（值已打码）。`gateway call` 拒绝返回明文的方法。
- `moox.toml`：Agent 用 `config get` / `config set` 读写（[`config-access.md`](config-access.md)），不直接读文件；用户要看或改时运行 `config edit`，让用户在自己的浏览器里操作；`config backup` 备份到管理台，换电脑 `config restore`。
