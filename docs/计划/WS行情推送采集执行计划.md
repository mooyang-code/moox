# WS 行情推送采集执行计划

> 执行计划 v2（2026-10-21，尚未编码）。依据 [WS 行情推送采集设计](WS行情推送采集设计.md) v2（下称"设计"）；两种获取方式的取舍见[行情获取方式对比](行情获取方式对比：REST拉取与WS推送.md)。设计里已经说明的理由不再重复，本文只写"做什么、改哪里、怎么验收、出问题怎么退"。

## 0. 约定

- **分支与提交**：在 `feature/mooyang` 上按阶段提交，每个阶段至少一个独立提交，提交信息写明阶段号；共享目录里提交时用 `git commit -- <路径>`，只提交自己改的文件。
- **构建与测试**：Go 1.27.2（本机自动优先），构建前去掉 `HTTP(S)_PROXY`；每个阶段跑涉及模块的 `go test ./...`，改并发代码的包加 `-race`。改 `modules/collector/schema/collector.sql` 时按 AGENTS.md 的格式规范，逐个载入空 SQLite 并执行 `git diff --check`。
- **发布**：用 `skills/moox/scripts/native-release.sh`（`build`/`install`/`ledger`），配置原位写入并先备份；服务目录（`packages/servicecatalog/catalog.yaml`）有变化的发布，Admin、各主机 host-gateway、Monitor、Access 与本机 `moox-cli` 用同一提交构建、按"Admin → 网关 → Monitor → 其他"的顺序一起换。线上操作避开整点前后（放在 :02～:10 或分钟第 20～40 秒），避开发布租约占用。
- **兼容**：按 AGENTS.md 不写长期兼容层；SCF 实时采集作为备用保留，与 stream 共存，不删除。
- **文档**：每个阶段完成时同步更新[采集](../模块/采集.md)、[命令行工具](../模块/命令行工具.md)、[监控](../模块/监控.md)、[部署与运维](../部署与运维.md)。

## 1. 阶段总览

| 阶段 | 内容 | 依赖 | 线上影响 |
| --- | --- | --- | --- |
| 0 | 前置验证：连续合约 K 线全量、合约 REST 定稿时间、海外机出站带宽（调研工具，不入库） | 无 | 无（只读、独立数据） |
| 1 | SCF 合约数据修复：按市场的拉取参数覆盖、合约推迟到定稿后取；历史数据重拉 | 0 | 改 Collector 配置与函数包 |
| 2 | 框架：`internal/stream`（连接管理、周期闸门、兜底）、`KlineCommitter` 抽取、币安适配器 | 0 | 无 |
| 3 | Collector：采集方式、stream 分配与版本、对账器执行端抽象、`StreamCollector` 服务、交接、命令 | 2 | 服务目录变化，需同提交发布 |
| 4 | `collector-stream` 组件：二进制、配置、服务目录、密钥投影、初始化默认值、监控告警 | 2、3 | 服务目录变化，需同提交发布 |
| 5 | 影子运行：stream 只收不写，与 SCF 和稳定基准比对 | 1、4 | 新进程上线，不写 Storage |
| 6 | 切换：crypto 按生效周期交接到 stream；回退演练 | 5 | 实时写入方从 SCF 变为 stream |
| 7 | 收尾：SCF 合约推迟随 SCF 停用、文档、观察一周后的复核 | 6 | 无 |

阶段 1 与阶段 2～4 可以并行；阶段 3、4 的服务目录变化合并成一次发布。

## 2. 阶段 0：前置验证（闸门）

用香港机器上的调研工具（`wsresearch`，草稿目录，数据只写本地文件，不进 Storage）完成：

| 项 | 做法 | 通过标准 |
| --- | --- | --- |
| 连续合约 K 线全量 | 订阅全部合约标的的 `<pair>_perpetual@continuousKline_1m` 与 `_1h`，至少 75 分钟、覆盖一个整点；结束后用收盘 1 分钟后的 REST 历史 K 线逐条比对 | 每个标的每个周期都有 `x=true`（含没有成交的分钟）；到达时刻 p99 ≤ 500 毫秒；与基准 100% 一致 |
| 合约 REST 定稿时间 | 收盘后 3、5、6、7、8、12 秒取 REST，覆盖至少 3 个整点 | 找出全部一致的最早时刻；设计取 8 秒，若 6、7 秒也 100% 可提前 |
| 现货 REST 定稿时间 | 同上，现货 1、2、3 秒 | 设计取 3 秒；验证 3 秒在多个整点下仍 100% |
| 海外机出站 | 在海外机上按 1m 两个数据集、整点四个数据集的数据量（约 150KB／数据集）向存储机方向做一次集中上传测速 | 整点四个数据集串行提交 ≤ 2 秒（stream 模式截止 5 秒，1m 与 1h 截止相同）；不满足就提高有效吞吐或放宽 1h 数据集的截止（先提交 1m 只能保住 1m） |
| 现货收盘推送 | 已有全量数据（511 个标的，含 16:00 整点）复核一个更多整点的样本 | 每分钟 511 个全到；p99 ≤ 500 毫秒 |

阶段 0 不通过的处理：连续合约 K 线不满足时，合约改用普通 K 线（`@kline_`），冷门标的约 3 秒到达；REST 定稿时间不随推送流改变，仍不做 REST 兜底（若要恢复 8 秒兜底，必须同时把合约数据集截止放宽到 10 秒以上并重新定验收预算）；合约延迟目标改为 p99 ≤ 3.5 秒，并在设计里改写第 5.7 节。

2026-10-20 已完成的部分（见设计第 1 节）：现货与合约普通 K 线全量（15:50～17:07）；连续合约 K 线 40 个标的 44 分钟（16:32～17:18，含 17:00 整点，1840 个样本，101～106 毫秒收盘，与基准 100% 一致）；REST 0.6～30 秒（含 17:00 整点）；全量连续合约 K 线（16:59～18:14、524 个标的、1m 与 1h、两个整点）**已通过**：每周期 524 个全部到齐，最晚 191 毫秒，1m 38252 根、1h 1048 根与基准 100% 一致。阶段 0 剩余：多个整点的 REST 定稿时间复测（合约 6、7 秒，现货 1～3 秒）与海外机出站实测。

## 3. 阶段 1：SCF 合约数据修复（独立，先做）

### 3.1 改动

1. **按市场的拉取参数覆盖**：`internal/bootstrap/fetch_plan_config.go` 的 `SCFPlanSpaceConfig` 增加 `Markets map[string]SCFPlanFetchConfig`（`scf_plan.spaces.<空间>.markets.<spot|swap>.fetch`），`fetchFor(spaceID)` 改为 `fetchFor(spaceID, marketType)`；对账器为每个函数生成计划时按函数的市场取参数（`plan_desired.go` 构造 `PlanFetch` 处）。
2. **收盘后最早取数时刻**：`PlanFetch` 增加 `not_before_ms`（相对收盘），`plan_runtime.go` 在错峰等待之后再等到 `scheduled + not_before_ms`。现有 `stagger_base_ms` 是从函数入口算起的（`plan_runtime.go:96-110`），云定时器本身会晚 0.4～1 秒，用它表达"收盘后 8 秒"不准，所以单独加字段。计划用 `DisallowUnknownFields` 解码，**必须先升函数包，再改 Collector 配置**。
3. **推迟到定稿后取**：线上 Collector `app.yaml` 给 `crypto.markets.swap.fetch` 设 `not_before_ms: 8000`、`crypto.markets.spot.fetch` 设 `not_before_ms: 3000`（现货的代价是非整点写入完成约晚 3 秒，见设计第 8 节）。核对单项重试预算：从 8 秒开始到截止（收盘 + 15 秒 − 4 秒写入预算）只剩约 3 秒，`fetchKlinesFromChain` 按 ctx 截止时间截断尝试预算（现有逻辑，确认即可）；来不及的标的按失败上报，由补采补齐。

### 3.2 测试

- `fetch_plan_config` 单元测试：按市场覆盖只影响该市场，未写的字段沿用空间与全局值。
- `plan_desired` 测试：现货与合约函数拿到不同的 `not_before_ms`。
- `plan` 测试：`not_before_ms` 的取值范围校验（0 或 1000～12000）。
- `plan_runtime` 测试：`not_before_ms=8000` 时取数开始不早于收盘 + 8 秒（定时器晚到 1 秒也一样），且 Storage 写入仍在截止前。

### 3.3 发布与验收

1. 先升函数包（`moox-cli collector function upload`，分钟中段），等全部函数升级完成（`plan_manager` 的升级车队无 `stale`）：新字段 `not_before_ms` 随计划下发，旧函数包解码计划时会拒绝未知字段。
2. 安装能解析 `markets` 覆盖与 `not_before_ms` 的新 Collector 二进制（此时配置还没改，行为不变）。
3. 备份并原位修改线上 Collector `app.yaml`，在 :02～:10 重启 Collector；下一轮对账生成新计划下发。新 Collector 按已知字段严格解析配置，所以顺序不能反：旧 Collector 读到 `markets` 会拒绝启动。
4. 验收：CLS `market_fetch_batch` 合约批次 `since_close_start_ms ≥ 8000`、现货 `≥ 3000`；`market_fetch_commit` 合约 `since_close_done_ms` 中位约 9 秒、现货约 3.5 秒，全部在 15 秒内；用设计第 1.5 节的方法（Storage View 副本导出 vs 币安稳定基准）复核修复后 1 小时，合约 1m 一致率 100%（截止前没取到的标的会由补采补齐，补采后再核对）。
5. 回退：先恢复 `app.yaml` 备份（去掉 `markets` 与 `not_before_ms`）并重启 Collector；如还要回退 Collector 二进制，必须在配置已恢复之后。

### 3.4 历史数据重拉

- 范围：合约 1m、1h 与现货 1m 从数据集最早生效点（10-16）到修复生效的周期（现货 1m 不一致 2.4%）。
- 做法：`moox-cli collector kline backfill --dataset <数据集> --start … --end … --force`，按天分批，避开整点；补采读的是已稳定的 REST 历史 K 线，走普通写入。
- 下游：因子按受影响的集合与区间提交补算（因子模块补算任务）；策略历史结果不回改，在运行记录里注明。
- 验收：抽查 3 个日期各 110 分钟，与币安基准 100% 一致。

## 4. 阶段 2：框架与币安适配器

### 4.0 统一的行情源目录（先做，行为不变）

- `internal/marketdata` 新增 `Catalog`／`CatalogEntry`／`Factory`／`Deps`（设计 5.9）；`Instrument` 增加可选 `Pair`，币安合约标的快照填入。
- 币安、A 股各数据源（新浪、腾讯、东方财富、百度、通达信）、指数、可转债数据源登记到目录。
- `marketwiring/subject_listers.go`（`NewSubjectListers`）、`market_runtime.go`（`newMarketProvider`）、`stock_runtime.go`（`newStockCNProviderForSource`）改为从目录按能力取；删除三处 `switch`。
- 测试：重构前后 `NewSubjectListers` 返回的键集合相同；每个原 `switch` 分支对应的 `(market, instrument, provider, source)` 都能从目录构造出同类型的数据源；未登记的组合报同样的错误；`marketwiring`、`subjectsync`、`marketfetch` 现有测试全部通过。

### 4.1 新包 `modules/collector/internal/stream`

| 文件 | 内容 |
| --- | --- |
| `source.go` | `Source`、`Limits`、`Keepalive`、`FallbackPolicy`、`Event`（设计 5.2） |
| `conn.go` | 单条连接：拨号（复用 `dnscache` 解析与 TLS 原主机名校验）、订阅批次与限速、回执校验、保活、读超时、事件分发 |
| `manager.go` | `ConnManager`：按 `StreamsPerConn` 分片、1m 与 1h 分连接、差量订阅、退避重连与端点轮换、`MaxConnAge − 30 分钟`主动轮换（新连接订阅完成并收到消息后再关旧连接） |
| `gate.go` | `PeriodGate`：按（数据集, 频率, 周期）收集定稿、去重、到齐即提交、兜底时刻、提交截止、迟到计数（设计 5.4） |
| `fallback.go` | 从统一行情源目录取同一数据源的 `KlineFetcher` 补缺的标的，只接受晚于 `StableAfter` 的结果；`StableAfter` 晚于截止减提交预留与一次 REST 预算时自动不兜底（WS 模式下的币安合约） |
| `planclient.go` | 心跳、拉计划、上报周期统计（阶段 3 的 `StreamCollector` 接口） |
| `metrics.go` | 连接、到达、兜底、提交耗时等指标 |

### 4.2 `KlineCommitter` 抽取（`internal/marketfetch`）

- 把 `KlinePipeline.Execute` 里"`rowFor` 构造行 → 按数据集分组 → `periodCommitForDataset` → `CommitTimeSeriesBatch`/`UpsertFieldsWithSource` → 目标结果"这一段抽成 `KlineCommitter.Commit(ctx, req Request, bars []marketdata.NormalizedKline) (results, error)`；`Execute` 改为"取数 + `Commit`"。
- SCF 行为不变：现有 `kline_pipeline` 测试全部通过，另加"同一组 K 线经 `Execute` 与经 `Commit` 写出的行逐字节相同"的测试。
- stream 构造请求用 `Plan.BuildRequests`（同一套目标、序号、hash、总数）；`MaxPlanItems` 的 200 上限只对 SCF 生效，stream 的计划另设上限（例如 4000）。

### 4.3 币安适配器（`internal/sources/binance/stream.go`）

- 币安的 `MarketDataAdapter` 实现 `stream.Provider`，返回现货或合约的 `Source`，与标的快照、REST K 线共用同一个适配器实例的配置（端点、符号映射）。
- 现货：`stream.binance.com:9443`（备选 `:443`），流名 `<symbol>@kline_<周期>`；合约：`fstream.binance.com/market`，流名 `<pair>_perpetual@continuousKline_<周期>`（`pair` 取自标的快照，缺省同 symbol），标的取 `ps`。
- 解码用显式声明全部大小写键的结构体（`e/E`、`t/T`、`v/V`、`q/Q`、`l/L`），转成 `marketdata.NormalizedKline`（与 REST 适配器的字段含义一致：`VolumeShares`=v、`AmountCNY`=q、`TradeCount`=n）。
- `Fallback().StableAfter`：现货 3 秒，合约 8 秒（阶段 0 可调整）。
- 配置：`configs/sources/market/binance.yaml` 增加 `stream` 段（地址、限额），与 `spot_base_urls`／`swap_base_urls` 并列。

### 4.4 测试

- 适配器：用调研时录制的真实报文（现货 kline、合约 continuousKline、订阅回执、错误回执）做解码与"REST 同一根 K 线字段一致"的测试；大小写键串值的回归测试。
- 连接管理：本地 WS 测试服务器模拟断线、ping、订阅报错、24 小时轮换；断言轮换期间无重复提交、无缺失。
- 下架探测：合约 `!contractInfo` 状态变化、连续 3 个周期无推送且兜底取不到时调标的快照确认。
- 兜底自动判断：截止 5 秒时币安合约不兜底、现货兜底；把合约截止放宽到 12 秒时合约自动恢复兜底。
- 周期闸门：用假时钟覆盖"到齐即提交""兜底补齐""兜底超时按失败""提交后迟到""两条连接重复定稿去重""计划切换（新 copy 生效）"。
- `go test -race ./internal/stream/... ./internal/marketfetch/...`。

## 5. 阶段 3：Collector

### 5.1 数据表（`modules/collector/schema/collector.sql`）

```sql
-- 每个空间的实时采集方式：scf（SCF 定时拉取）或 stream（WS 推送）
CREATE TABLE IF NOT EXISTS t_collector_ingest_modes (
    c_space_id TEXT NOT NULL PRIMARY KEY,
    c_mode TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (c_mode IN ('scf', 'stream'))
);

-- stream 实例的租约与心跳
CREATE TABLE IF NOT EXISTS t_collector_stream_workers (
    c_space_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_applied TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT '{}',
    c_heartbeat_at DATETIME NOT NULL,
    PRIMARY KEY (c_space_id)
);
```

（`c_applied` 是各 stream 执行端已应用的计划记录 ID 与阶段，`c_status` 是连接、观察／可写状态与最近周期统计；同一空间一行，表示当前持有租约的实例；写权限到期时间按 `c_heartbeat_at + 90 秒` 计算。stream 分配本身不另建表，记录在 `t_collector_fetch_plans` 的计划内容里，见 5.3。）

### 5.2 配置

- `scf_plan.spaces.<空间>.ingest: scf|stream`：首次启动时写入 `t_collector_ingest_modes` 的初值，之后以库为准。
- `stream`：`lease_takeover_seconds`（45）、`heartbeat_timeout_seconds`（180）、`lateness_s`（5，stream 模式下数据集的截止时间）、按市场的 `fallback_after_ms`（现货 800、合约 800）与 `commit_reserve_ms`（1000）。是否兜底不设开关，由闸门按"截止 − 提交预留 − 一次 REST 预算 ≥ `StableAfter`"自动判断（设计 5.2），截止放宽后自动恢复。随计划下发给 stream 进程。

### 5.3 对账器

- 抽出执行端接口（名称暂定 `planFleet`）：`Desired(groups) → 分配`、`Push(record, content, final) → 是否全部确认`、`Verify(applied)`。SCF 实现就是现有 `pushFunctions`／`verifyFleet`／`ensureCreated`；stream 实现把分配写进计划内容的 `stream` 执行端项，并比对心跳里已应用的计划记录 ID 与阶段。
- `BuildDesiredState` 增加 stream 分配：每市场一个分配，包含全部标的；快照、hash、序号、生效周期 P 的计算不变。
- 计划内容（`FetchPlanContent.Functions`）增加执行端类型（`scf`／`stream`），stream 分配作为 `stream-<空间>-<市场>` 一项与 SCF 函数并列；SCF 专属的步骤（建函数、环境变量、定时器、代码包升级、读回）只对 `scf` 项执行。
- 截止时间跟随采集方式（设计 5.11）：交接的 `drain` 步骤在 P + 30 秒调用 `EnsureAllowedLateness` 把数据集属性改为新模式的值（stream 5 秒、SCF 15 秒）；任务参数显式指定了 `allowed_lateness_seconds` 的不改。测试：切换前后的最后一个旧周期与第一个新周期分别得到 15 秒与 5 秒（或反之）的截止，1m、1h 都覆盖。
- 交接按设计 6.3 的状态机实现（`prepare → release → grant → drain`），阶段记在计划进度（`FetchPlanProgress`）里，Collector 重启后续跑；只有 `grant` 开始下发之前可以撤回（`P − 60 秒`前 `release` 未全部确认），`grant` 开始后只向前推进。交接计划不包含标的变更，有待处理的标的变化时先完成那份计划再交接。
- `ingest=stream` 时：SCF 实时函数在 `drain` 结束后才下发空的最终计划并停用定时器（函数保留，代码包照常随发布升级）；补采函数不受影响。
- 补采解析（`klinecheck/backfill.go` 的 `datasetMarket`）遍历两种执行端的计划项，按执行端类型选补采函数的市场与来源。
- 观察计划：`ingest=scf` 且配置开启 `stream.observe` 时，给 stream 下发全部标的的观察计划（不授予写权限），用于阶段 5。
- 下架处理（设计 5.10）：`ReportStreamPeriods` 带疑似下架的标的；Collector 立即对相应自动标签做一次快照同步。标的快照取数在同步内退避重试（3 次），失败或校验不通过时保持原成员。测试：下架后约 5 分钟内计划去掉该标的；快照请求失败、返回残缺时成员不变。
- 采集名单干预（设计 6.6）：表 `t_collector_subject_exclusions`；RPC `SetSubjectExclusions`／`ClearSubjectExclusions`／`ListSubjectExclusions`；期望状态计算时去掉排除的标的；到期自动失效；`moox-cli collector subjects exclude|unexclude|exclusions`；活动日志。服务目录：三个方法放行 `console`、`moox-cli` 与外部身份 `collector-operator`（旁路程序经 Access 调用，同时放行 `SyncSubjects`）。测试：排除后下一轮生成新计划且 hash 变化；到期自动恢复；交接进行中设置排除时推迟到交接完成。

### 5.4 `StreamCollector` 服务（`modules/collector/proto/collector.proto`）

- 新 service `StreamCollector`：`StreamHeartbeat`、`PullStreamPlan`、`ReportStreamPeriods`（设计 6.4）；新 RPC `CollectMgr.SetIngestMode`、`CollectMgr.GetIngestMode`。
- 服务目录：`collector` 组件下新增 `trpc.moox.collector.StreamCollector`（`acl` 只放 `collector-stream`），`SetIngestMode` 只放 `moox-cli`，`GetIngestMode` 放 `console`、`moox-cli`。
- `ReportStreamPeriods` 的统计进 Collector 指标（设计 6.5），不落库。

### 5.5 命令与页面

- `moox-cli collector ingest-mode get|set --space-id <空间> [--mode scf|stream] [--yes]`；`set` 打印将选用的生效周期 P 与当前 stream 实例状态，stream 离线时切到 `stream` 被拒绝。
- 运行状态页"函数"区：`stream` 时显示实例、连接、最近周期到达／兜底／失败与写入完成时间。

### 5.6 测试

- 对账器：`scf → stream`、`stream → scf`、交接同时有标的变更、stream 离线时切回（P 晚于写权限到期 + 迟到窗口）、交接各阶段 Collector 重启后续跑、**只有一端确认**（`release` 未全部确认 → 撤回且新端从未被授予；`grant` 部分生效、结果未知 → 不撤回旧端，继续重试，没有任何周期出现两个写入方）、交接期间出现标的变化（推迟到交接完成后处理）、1h 的旧周期在 `drain` 结束前不被停用。
- 断言同一周期最多一个执行端的 copy 含标的（属性测试：任意周期、任意下发确认顺序与失败，两个执行端被授予的标的集合不相交）。
- 补采：默认 `stream` 的新部署、`stream` 模式下新增的标的，都能解析出市场与交易所符号并调用补采函数。
- `StreamCollector`：写权限租约（第二个实例在原实例写权限到期前被拒）、观察／可写状态、计划内容通过 `Plan.Validate`。
- stream 进程：写权限过期后不再提交、在途提交被取消（假时钟）。

## 6. 阶段 4：`collector-stream` 组件

### 6.1 改动

| 位置 | 内容 |
| --- | --- |
| `modules/collector/cmd/stream` | `moox-collector-stream` 主程序：读配置、`gatewayclient`（内部身份 `collector-stream`）、心跳与拉计划、按计划启动各 `Source`、健康检查 `/healthz`、`/readyz`（全部市场的连接已订阅且已应用计划版本）、`/metrics` |
| `modules/collector/config/stream/app.yaml` | 配置模板：空间、健康端口、Storage 写入超时、日志 |
| `packages/servicecatalog/catalog.yaml` | 新组件 `collector-stream`（`scope: any`、`replicas: single`、`required_in_default_profile: false`）；调用 Storage `PrimaryStore.CommitTimeSeriesBatch` 与 `StreamCollector` 的权限 |
| `modules/cli/internal/setup/unitdeploy/unit.go` | `unitEnvironment` 的 Storage 主密钥白名单加入 `collector-stream`；确认海外机单元的安装材料能拿到主密钥 |
| `modules/cli/internal/setup/unitpackage` | 单元的组件与资源清单加入 `collector-stream`（二进制、配置模板、运行清单） |
| 部署工具 | 支持向已部署的单元新增组件（现有 `deploy-unit` 只按原始软件包重试、`native-release.sh install` 只替换已有组件），或按单元重新部署的步骤 |
| `scripts/build` | `build.sh collector-stream`（纯 Go，交叉编译） |
| `moox.toml.example` 与 setup 渲染 | `[placements]` 的 `overseas` 默认加 `collector-stream`；`[[scf_plan.spaces]]` 可写 `ingest`，渲染时 crypto 默认 `stream`、其他空间 `scf` |
| Monitor | 告警规则：`collector-stream` 心跳中断 > 3 分钟；连续 5 个周期兜底率 > 10%；连续 3 个周期有失败标的 |

### 6.2 测试

- 组件启动、读配置严格解析（未知字段拒绝）、健康检查语义。
- setup 渲染：新部署 crypto 为 `stream`、stockcn 为 `scf`；显式 `ingest = "scf"` 覆盖生效。
- 服务目录一致性测试（`packages/servicecatalog` 现有测试）通过。

### 6.3 发布（与阶段 3 合并为一次）

1. 同一提交构建 Admin、各主机 host-gateway、Monitor、Access、Collector、`collector-stream`、本机 `moox-cli`。
2. 线上 Collector `app.yaml` 先显式写 crypto `ingest: scf`（保持现状），再按顺序替换 Admin → 网关 → Monitor → Access → Collector。
3. 用部署工具在海外机单元新增 `collector-stream`（配置、内部身份、Storage 主密钥、运行清单一起生成），确认 `status.sh` ready、Collector 看到心跳且为观察状态；之后的二进制升级才用 `native-release.sh install`。
4. 回退：**先恢复旧版本能解析的 Collector `app.yaml`**（去掉 `ingest`、`stream` 段；旧 Collector 严格解析配置，读到新字段会拒绝启动），再按 ledger 回滚各组件；`collector-stream` 可单独停用（`ingest` 仍为 `scf` 时它没有写授权，不写任何数据）。

## 7. 阶段 5：影子运行

- Collector 配置开启 `stream.observe`，给 `collector-stream` 下发观察计划（全部标的、不授予写权限）：照常接收、闸门、兜底、构造请求，但不调 `CommitTimeSeriesBatch`，把每个周期构造出的行写本地 JSONL（只保留 24 小时）。心跳报告"观察"。
- 运行至少 24 小时、覆盖全部整点。每天用脚本把影子行与 Storage 里 SCF 写入的行、币安稳定基准三方比对。
- 通过标准：影子行与币安基准 100% 一致；每个周期写入就绪时刻（闸门决定提交的时刻）现货、合约 p99 ≤ 1 秒；兜底率 < 1%；没有连接导致的缺失；进程内存稳定。
- 期间如阶段 1 已上线，SCF 合约数据也应与基准一致，三方应完全相同。

## 8. 阶段 6：切换

1. 确认 `collector-stream` 在线、持有实例租约（心跳状态"观察"即可，写授权由交接的 `grant` 给出）；关闭 `stream.observe` 不是前提，交接完成后观察计划自然被正式计划取代。分钟第 20～40 秒执行 `moox-cli collector ingest-mode set --space-id crypto --mode stream --yes`，记下各频率的生效周期 P。
2. 跟踪交接阶段：`release`（SCF 函数确认 P 起为空）→ `grant`（stream 确认 P 起全部标的、心跳变为"可写"）在 `P − 30 秒`前完成；P 之后 stream 开始提交；`drain` 结束（1h 要过 P 之后的整点 + 迟到窗口）后 SCF 定时器才停用。
3. 验收（P 之后 2 小时、覆盖 2 个整点）：
   - 周期标记全部 `complete`，`missed` 为 0；
   - `collector-stream` 写入完成相对收盘：现货、合约 p99 ≤ 1 秒（整点同样）；数据集截止时间已是 5 秒，降级周期的完成标记在收盘后 5 秒内发出；
   - 策略 1m 整点产出延迟比切换前下降 5 秒以上；
   - 自动补采没有新增作业；
   - 用设计第 1.5 节方法复核 1 小时数据，与基准 100% 一致。
4. 回退演练：在验收后的一个非整点时段执行 `ingest-mode set --mode scf`，确认 SCF 在新的 P 起接手、无缺口，然后再切回 `stream`。

## 9. 阶段 7：收尾

- 阶段 1 的合约推迟随 SCF 停用自然失效，配置保留（切回 SCF 时仍要它）。
- 更新模块文档、部署与运维的"故障切回"操作说明；在[计划目录](README.md)把设计与本计划标为"已实现"。
- 一周后复核：兜底率、重连次数、24 小时轮换是否无缺失、出站带宽峰值。

## 10. 风险与检查点

| 检查点 | 不通过时 |
| --- | --- |
| 阶段 0 连续合约 K 线全量 | 合约改用普通 K 线（冷门标的约 3 秒到达），仍不兜底，延迟目标改为合约 p99 ≤ 3.5 秒 |
| 阶段 1 合约推迟后仍有不一致 | 加长 `stagger_base_ms`，并核对 1h 是否需要同样推迟 |
| 阶段 5 影子行与基准不一致 | 不切换，定位适配器或闸门问题 |
| 阶段 6 写入完成 p99 超标 | 检查出站带宽与提交分片，必要时切回 SCF |
| 服务目录发布 | 漏换 Monitor 会触发"网关超过 2 分钟没有心跳"告警，按部署与运维的顺序一起换 |

## 11. 交付物清单

- 代码：`marketdata.Catalog`（统一行情源目录）与三处装配的改造、`internal/stream`、`internal/sources/binance/stream.go`、`marketfetch.KlineCommitter`、对账器执行端抽象、`StreamCollector` 服务、`cmd/stream`、按市场的拉取参数覆盖、setup 渲染默认值、Monitor 告警。
- 配置与目录：`collector.sql` 三张表、`catalog.yaml` 新组件与权限、`binance.yaml` 的 `stream` 段、`moox.toml.example`。
- 文档：采集、命令行工具、监控、部署与运维的更新；本计划与设计的实现状态。
