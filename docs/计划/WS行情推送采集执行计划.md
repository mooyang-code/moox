# WS 行情推送采集执行计划

> 执行计划 v4（2026-10-21；**已实现**：阶段 1～5a、7、8 的代码已完成（分支 `feat/ws-market-stream`，进度见[实施清单](WS行情推送采集实施清单.md)），阶段 0 的海外机测速与阶段 6 的线上切换随发布进行；同日随设计按 Codex 审查修订阶段 0、3、4、5a、6 的相关条目）。依据 [WS 行情推送采集设计](WS行情推送采集设计.md) v4（下称"设计"）；v4 取消影子运行与上线前比对（方案定了直接切换），采集方式按数据集绑定，补采走香港机本地执行端；v3 去掉 REST 兜底与租约、1h 截止 8 秒、只做手动切换，新增阶段 5a（下游因子连续水位，切换前完成）与阶段 8（切换后的独立任务：统一行情源目录、采集名单干预）。两种获取方式的取舍见[行情获取方式对比](行情获取方式对比：REST拉取与WS推送.md)。设计里已经说明的理由不再重复，本文只写"做什么、改哪里、怎么验收、出问题怎么退"。

## 0. 约定

- **分支与提交**：在 `feature/mooyang` 上按阶段提交，每个阶段至少一个独立提交，提交信息写明阶段号；共享目录里提交时用 `git commit -- <路径>`，只提交自己改的文件。
- **构建与测试**：Go 1.27.2（本机自动优先），构建前去掉 `HTTP(S)_PROXY`；每个阶段跑涉及模块的 `go test ./...`，改并发代码的包加 `-race`。改 `modules/collector/schema/collector.sql` 时按 AGENTS.md 的格式规范，逐个载入空 SQLite 并执行 `git diff --check`。
- **发布**：用 `skills/moox/scripts/native-release.sh`（`build`/`install`/`ledger`），配置原位写入并先备份；服务目录（`packages/servicecatalog/catalog.yaml`）有变化的发布，Admin、各主机 host-gateway、Monitor、Access 与本机 `moox-cli` 用同一提交构建、按"Admin → 网关 → Monitor → 其他"的顺序一起换。线上操作避开整点前后（放在 :02～:10 或分钟第 20～40 秒），避开发布租约占用。
- **兼容**：按 AGENTS.md 不写长期兼容层；SCF 实时采集作为备用保留，与 stream 共存，不删除。
- **文档**：每个阶段完成时同步更新[采集](../模块/采集.md)、[命令行工具](../模块/命令行工具.md)、[监控](../模块/监控.md)、[部署与运维](../部署与运维.md)。

## 1. 阶段总览

| 阶段 | 内容 | 依赖 | 线上影响 |
| --- | --- | --- | --- |
| 0 | 前置核对：合约 WS 限额、海外机出站带宽 | 无 | 无 |
| 1 | SCF 合约数据修复：按市场的拉取参数覆盖、合约推迟到定稿后取；历史数据重拉 | 0 | 改 Collector 配置与函数包 |
| 2 | 框架：`internal/stream`（连接管理、周期闸门、提交）、K 线批次模型与 `KlineCommitter`、本地执行端入口、币安适配器、`Instrument.Pair` | 0 | 无 |
| 3 | Collector：按数据集的采集方式与执行端、stream 分配与版本、对账器执行端抽象、`StreamCollector` 服务、补采按执行端调用、交接、命令 | 2 | 服务目录变化，需同提交发布 |
| 4 | `collector-stream` 组件：二进制、配置、服务目录、派生密钥注入、初始化默认值、监控告警 | 2、3 | 服务目录变化，需同提交发布 |
| 5a | 下游因子连续水位与补齐通知（设计 7.5，单独出详细设计）；打开因子窗口空洞检查 | 无 | 改因子引擎与补采 |
| 6 | 切换：crypto 的数据集按生效周期直接切到 stream；手动切回演练（含 `--force`） | 4、5a | 实时写入方从 SCF 变为 stream；crypto 补采改走本地执行端 |
| 7 | 收尾：SCF 合约推迟随 SCF 停用、文档、观察一周后的复核 | 6 | 无 |
| 8 | 切换后的独立任务：统一行情源目录、采集名单干预接口 | 7 | 按各自计划 |

阶段 1 与阶段 2～4 可以并行；阶段 3、4 的服务目录变化合并成一次发布。阶段 5a 与阶段 1～4 并行，但必须在阶段 6 之前完成。阶段 5（影子运行）已取消，编号保留不用。

## 2. 阶段 0：前置核对

推送的完整性与数值已在 2026-10-20 全量实测通过（设计第 1 节：现货、合约普通 K 线全量；连续合约 K 线 524 个标的、75 分钟、两个整点，与基准 100% 一致），不再做比对。只剩两项会影响实现参数的核对：

| 项 | 做法 | 用途 |
| --- | --- | --- |
| 合约 WS 限额 | 对照币安合约 WS 文档核实入站消息、建连限额（设计 1.3） | 写进适配器 `Limits` |
| 海外机出站 | 在海外机上按两个 1m 数据集、整点四个数据集的数据量（约 150KB／数据集）向存储机方向做一次集中上传测速 | 两个 1m 数据集串行提交的总耗时、两个 1h 数据集的总耗时都要小于提交预留 `commit_reserve_ms`（默认 1000，设计 5.4、7.3）；不满足就按实测值调大预留 |

## 3. 阶段 1：SCF 合约数据修复（独立，先做）

### 3.1 改动

1. **按市场的拉取参数覆盖**：`internal/bootstrap/fetch_plan_config.go` 的 `SCFPlanSpaceConfig` 增加 `Markets map[string]SCFPlanFetchConfig`（`scf_plan.spaces.<空间>.markets.<spot|swap>.fetch`），`fetchFor(spaceID)` 改为 `fetchFor(spaceID, marketType)`；对账器为每个函数生成计划时按函数的市场取参数（`plan_desired.go` 构造 `PlanFetch` 处）。
2. **收盘后最早取数时刻**：`PlanFetch` 增加 `not_before_ms`（相对收盘），`plan_runtime.go` 在错峰等待之后再等到 `scheduled + not_before_ms`。现有 `stagger_base_ms` 是从函数入口算起的（`plan_runtime.go:96-110`），云定时器本身会晚 0.4～1 秒，用它表达"收盘后 8 秒"不准，所以单独加字段。计划用 `DisallowUnknownFields` 解码，**必须先升函数包，再改 Collector 配置**。
3. **推迟到定稿后取**：线上 Collector `app.yaml` 给 `crypto.markets.swap.fetch` 设 `not_before_ms: 8000`、`crypto.markets.spot.fetch` 设 `not_before_ms: 3000`（现货的代价是非整点写入完成约晚 3 秒，见设计第 8 节）。SCF 模式下合约 1m、1h 数据集的 `allowed_lateness_seconds` 放宽到 20 秒（设计第 8 节；现货仍 15 秒，任务参数显式指定的不改），从 8 秒开始到截止还有约 8 秒；`fetchKlinesFromChain` 按 ctx 截止时间截断尝试预算（现有逻辑，确认即可）；来不及的标的按失败上报，由补采补齐。

### 3.2 测试

- `fetch_plan_config` 单元测试：按市场覆盖只影响该市场，未写的字段沿用空间与全局值。
- `plan_desired` 测试：现货与合约函数拿到不同的 `not_before_ms`。
- `plan` 测试：`not_before_ms` 的取值范围校验（0 或 1000～12000）。
- `plan_runtime` 测试：`not_before_ms=8000` 时取数开始不早于收盘 + 8 秒（定时器晚到 1 秒也一样），且 Storage 写入仍在截止前。

### 3.3 发布与验收

1. 先升函数包（`moox-cli collector function upload`，分钟中段），等全部函数升级完成（`plan_manager` 的升级车队无 `stale`）：新字段 `not_before_ms` 随计划下发，旧函数包解码计划时会拒绝未知字段。
2. 安装能解析 `markets` 覆盖与 `not_before_ms` 的新 Collector 二进制（此时配置还没改，行为不变）。
3. 备份并原位修改线上 Collector `app.yaml`，在 :02～:10 重启 Collector；下一轮对账生成新计划下发。新 Collector 按已知字段严格解析配置，所以顺序不能反：旧 Collector 读到 `markets` 会拒绝启动。
4. 验收：CLS `market_fetch_batch` 合约批次 `since_close_start_ms ≥ 8000`、现货 `≥ 3000`；`market_fetch_commit` 合约 `since_close_done_ms` 中位约 9 秒、现货约 3.5 秒，现货在 15 秒内、合约在 20 秒内；用设计第 1.5 节的方法（Storage View 副本导出 vs 币安稳定基准）复核修复后 1 小时，合约 1m 一致率 100%（截止前没取到的标的会由补采补齐，补采后再核对）。
5. 回退：先恢复 `app.yaml` 备份（去掉 `markets` 与 `not_before_ms`）并重启 Collector；如还要回退 Collector 二进制，必须在配置已恢复之后。

### 3.4 历史数据重拉

- 范围：合约 1m、1h 与现货 1m 从数据集最早生效点（10-16）到修复生效的周期（现货 1m 不一致 2.4%）。
- 做法：`moox-cli collector kline backfill --dataset <数据集> --start … --end … --force`，按天分批，避开整点；补采读的是已稳定的 REST 历史 K 线，走普通写入。
- 下游：因子按受影响的集合与区间提交补算（因子模块补算任务）；策略历史结果不回改，在运行记录里注明。
- 验收：抽查 3 个日期各 110 分钟，与币安基准 100% 一致。

## 4. 阶段 2：框架与币安适配器

### 4.0 标的快照带出 `Pair`

- `internal/marketdata` 的 `Instrument` 增加可选 `Pair`，币安合约标的快照填入（缺省同 symbol）；采集计划把它带给 stream。
- 统一行情源目录（设计 5.9）不在这里做，移到阶段 8。

### 4.1 新包 `modules/collector/internal/stream`

| 文件 | 内容 |
| --- | --- |
| `source.go` | `Source`、`Limits`、`Keepalive`、`FallbackPolicy`、`Event`（设计 5.2） |
| `conn.go` | 单条连接：拨号（复用 `dnscache` 解析与 TLS 原主机名校验）、订阅批次与限速、回执校验、保活、读超时、事件分发 |
| `manager.go` | `ConnManager`：按 `StreamsPerConn` 分片、1m 与 1h 分连接、差量订阅、退避重连与端点轮换、`MaxConnAge − 30 分钟`主动轮换（新连接订阅完成并收到消息后再关旧连接） |
| `gate.go` | `PeriodGate`：按（数据集, 频率, 周期）收集定稿、去重、到齐即提交、最晚提交时刻、迟到计数（设计 5.4） |
| `planclient.go` | 心跳、拉计划、上报周期统计（阶段 3 的 `StreamCollector` 接口） |
| `metrics.go` | 连接、到达、截止时仍缺的标的数、提交耗时等指标 |

### 4.2 `KlineCommitter` 抽取（`internal/marketfetch`）

- 把 `KlinePipeline.Execute` 里"`rowFor` 构造行 → 按数据集分组 → `periodCommitForDataset` → `CommitTimeSeriesBatch`/`UpsertFieldsWithSource` → 目标结果"这一段抽成 `KlineCommitter`，入参是 K 线批次 `KlineBatch{SpaceID, DatasetID, Frequency, Period, Copy, Bars, Origin}`（设计 5.12）：`CommitPeriod` 走周期提交，`Upsert` 走普通写入（补采）；`Execute` 改为"取数 + 提交"。
- SCF 行为不变：现有 `kline_pipeline` 测试全部通过，另加"同一组 K 线经 `Execute` 与经 `Commit` 写出的行逐字节相同"的测试。
- stream 构造请求用 `Plan.BuildRequests`（同一套目标、序号、hash、总数）；`MaxPlanItems` 的 200 上限只对 SCF 生效，stream 的计划另设上限（例如 4000）。

### 4.2a 本地执行端入口（`internal/serverless/market_data`）

- 把 `HandleRequest` 里与云函数无关的部分（解码事件、构造 `marketwiring` 处理器、按 action 分发）抽成 `Execute(ctx, event, env)`，`env` 提供空间、函数名、指标；云函数入口与本地入口都调它。
- 本地入口：内部 RPC `MarketFetchExecutor.Invoke`（`modules/collector/proto` 新增），请求体是现有事件 JSON，返回 `model.Response`；独立并发上限（默认 4）与独立 HTTP 客户端（设计 6.7）。
- 测试：同一事件经云函数入口与本地入口得到相同的写入与响应；并发上限生效。

### 4.3 币安适配器（`internal/sources/binance/stream.go`）

- 币安的 `MarketDataAdapter` 实现 `stream.Provider`，返回现货或合约的 `Source`，与标的快照、REST K 线共用同一个适配器实例的配置（端点、符号映射）。
- 现货：`stream.binance.com:9443`（备选 `:443`），流名 `<symbol>@kline_<周期>`；合约：`fstream.binance.com/market`，流名 `<pair>_perpetual@continuousKline_<周期>`（`pair` 取自标的快照，缺省同 symbol），标的取 `ps`。
- 解码用显式声明全部大小写键的结构体（`e/E`、`t/T`、`v/V`、`q/Q`、`l/L`），转成 `marketdata.NormalizedKline`（与 REST 适配器的字段含义一致：`VolumeShares`=v、`AmountCNY`=q、`TradeCount`=n）。
- 配置：`configs/sources/market/binance.yaml` 增加 `stream` 段（地址、限额），与 `spot_base_urls`／`swap_base_urls` 并列。

### 4.4 测试

- 适配器：用调研时录制的真实报文（现货 kline、合约 continuousKline、订阅回执、错误回执）做解码与"REST 同一根 K 线字段一致"的测试；大小写键串值的回归测试。
- 连接管理：本地 WS 测试服务器模拟断线、ping、订阅报错、24 小时轮换；断言轮换期间无重复提交、无缺失；记录"启动到全部订阅完成"的耗时。
- 下架探测：合约 `!contractInfo` 状态变化、连续 3 个 1m 周期没有收盘推送时调标的快照确认（1h 跟随 1m）。
- 周期闸门：用假时钟覆盖"到齐即提交""未到齐时在最晚提交时刻提交已到的（1m 收盘 + 4 秒、1h + 7 秒）""提交后迟到""两条连接重复定稿去重""计划切换（新 copy 生效）"。
- `go test -race ./internal/stream/... ./internal/marketfetch/...`。

## 5. 阶段 3：Collector

### 5.1 数据表（`modules/collector/schema/collector.sql`）

```sql
-- 每个数据集的采集方式：stream（WS 推送）或 pull（定时拉取）；executor 是拉取与补采的执行端
CREATE TABLE IF NOT EXISTS t_collector_ingest_modes (
    c_space_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_mode TEXT NOT NULL,
    c_executor TEXT NOT NULL,
    c_mtime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_space_id, c_dataset_id),
    CHECK (c_mode IN ('stream', 'pull')),
    CHECK (c_executor IN ('scf', 'local'))
);

-- stream 实例的心跳与已应用的计划
CREATE TABLE IF NOT EXISTS t_collector_stream_workers (
    c_space_id TEXT NOT NULL,
    c_instance_id TEXT NOT NULL,
    c_applied TEXT NOT NULL DEFAULT '{}',
    c_status TEXT NOT NULL DEFAULT '{}',
    c_heartbeat_at DATETIME NOT NULL,
    PRIMARY KEY (c_space_id)
);
```

（`c_applied` 是各 stream 执行端已应用的计划记录 ID 与阶段，`c_status` 是连接状态与最近周期统计；同一空间一行（只部署一个实例）；不设租约与写权限到期（设计 6.4）。stream 分配本身不另建表，记录在 `t_collector_fetch_plans` 的计划内容里，见 5.3。）

### 5.2 配置

- `scf_plan.spaces.<空间>.default_ingest: stream|pull` 与 `default_executor: scf|local`：新数据集第一次出现时写入 `t_collector_ingest_modes` 的初值，之后以库为准；任务参数显式指定的以任务为准。crypto 默认 `stream` + `local`，stockcn 默认 `pull` + `scf`。
- `stream`：`heartbeat_timeout_seconds`（180，只用于运行状态与告警）、`lateness_s`（按频率，`1m: 5`、`1h: 8`）、`commit_reserve_ms`（1000）。随计划下发给 stream 进程。不做 REST 兜底，没有兜底相关配置。

### 5.3 对账器

- 抽出执行端接口（名称暂定 `planFleet`）：`Desired(groups) → 分配`、`Push(record, content, final) → 是否全部确认`、`Verify(applied)`。SCF 实现就是现有 `pushFunctions`／`verifyFleet`／`ensureCreated`；stream 实现把分配写进计划内容的 `stream` 执行端项，并比对心跳里已应用的计划记录 ID 与阶段。
- `BuildDesiredState` 增加 stream 分配：每市场一个分配，包含全部标的；快照、hash、序号、生效周期 P 的计算不变。
- 计划内容（`FetchPlanContent.Functions`）增加执行端类型（`scf`／`stream`），stream 分配作为 `stream-<空间>-<市场>` 一项与 SCF 函数并列；SCF 专属的步骤（建函数、环境变量、定时器、代码包升级、读回）只对 `scf` 项执行。
- 截止时间跟随采集方式（设计 5.11）：`plan_desired.go` 取 `LatenessS` 改为"任务值 → 数据集当前采集方式的默认值"（stream 1m 5 秒、1h 8 秒；SCF 现货 15 秒、合约 20 秒），计划内容与 `EnsureAllowedLateness` 都只用这个值；交接计划里旧执行端那一份写旧模式的值（`WindowEnd` 按它算）、新执行端那一份直接写新模式的值，`drain` 在 `WindowEnd` 加收尾余量、下发最终计划之前把 Storage 属性改为新值（合法区间是 P + 旧值到 P + 一个周期，设计 5.11），最终计划写新值。测试：切换前后的最后一个旧周期与第一个新周期分别得到旧模式与新模式的截止（1m、1h 都覆盖）；切换完成后的下一份普通计划不会把属性改回旧值；首次部署时 stream 数据集直接是 5／8 秒。
- 手动切换按设计 6.3 实现（`prepare → switch → drain`），阶段记在计划进度（`FetchPlanProgress`）里，Collector 重启后续跑；不做撤回；`drain` 等到按旧值计算的 `WindowEnd` 加收尾余量就结束，1h 不多等一个整点。`--force` 时不等旧执行端确认；如果已有一份未完成的标的变更计划在等失联端确认，`--force` 也作用于它：按已登记的 hash 与生效周期推进完成，再开始交接。交接计划不包含标的变更，有待处理的标的变化时先完成那份计划再交接。
- 数据集切到 `stream` 后：SCF 实时函数在 `drain` 结束后才把该数据集从计划里去掉；某个 SCF 函数的数据集全部去掉后停用其定时器（函数保留，代码包照常随发布升级）。
- 补采：`klinecheck/backfill.go` 的 `datasetMarket` 遍历三种执行端（`scf`／`stream`／`local`）的计划项解析市场与来源；`Invoker` 按数据集的 `executor` 选实现：`scf` 走 `scfinvoker`（先 `EnsureBackfillFunction`），`local` 调 `MarketFetchExecutor.Invoke`；分块认领、复查、缺失判定不变。
- 下架处理（设计 5.10）：`ReportStreamPeriods` 带疑似下架的标的；Collector 立即对相应自动标签做一次快照同步。标的快照取数在同步内退避重试（3 次），失败或校验不通过时保持原成员。测试：下架后约 5 分钟内计划去掉该标的；快照请求失败、返回残缺时成员不变。

### 5.4 `StreamCollector` 服务（`modules/collector/proto/collector.proto`）

- 新 service `StreamCollector`：`StreamHeartbeat`、`PullStreamPlan`、`ReportStreamPeriods`（设计 6.4）；新 RPC `CollectMgr.SetIngestMode`、`CollectMgr.GetIngestMode`（按数据集，`--space-id` 时批量）。
- 服务目录：`collector` 组件下新增 `trpc.moox.collector.StreamCollector`（`acl` 只放 `collector-stream`），`SetIngestMode` 只放 `moox-cli`，`GetIngestMode` 放 `console`、`moox-cli`。
- `ReportStreamPeriods` 的统计进 Collector 指标（设计 6.5），不落库。

### 5.5 命令与页面

- `moox-cli collector ingest-mode get --space-id <空间> [--dataset <id>]`、`set --dataset <id>|--space-id <空间> --mode stream|pull [--executor scf|local] [--force] [--yes]`；`set` 打印每个数据集将选用的生效周期 P 与当前 stream 实例状态，stream 离线时切到 `stream` 被拒绝；`--force` 用于旧执行端失联的极端故障，跳过旧端确认（设计 6.3）。
- 运行状态页"函数"区：`stream` 时显示实例、连接、最近周期到达／失败与写入完成时间。

### 5.6 测试

- 对账器：数据集 `pull → stream`、`stream → pull`、同一空间里两种方式并存（SCF 函数只拿 pull 的数据集、stream 分配只拿 stream 的数据集）、`--force`（旧端从不确认时只等新端确认即生效）、标的变更计划下发途中 stream 失联后 `--force` 切回（已有计划被推进完成、交接随即开始）、交接同时有标的变更（推迟到交接完成后处理）、交接各阶段 Collector 重启后续跑、1h 的旧周期在 `drain` 结束前不被停用。
- 双写容忍：P 附近两个执行端同时写同一周期（同值），周期结果与单写相同（幂等）。
- 补采：`executor=local` 时调本地执行端、`scf` 时调补采函数；默认 `stream` 的新部署、`stream` 模式下新增的标的，都能解析出市场与交易所符号。
- `StreamCollector`：心跳与计划确认、计划内容通过 `Plan.Validate`。
- stream 进程：收到新计划后，P 起不再属于自己的标的不再提交。

## 6. 阶段 4：`collector-stream` 组件

### 6.1 改动

| 位置 | 内容 |
| --- | --- |
| `modules/collector/cmd/stream` | `moox-collector-stream` 主程序：读配置、`gatewayclient`（内部身份 `collector-stream`）、心跳与拉计划、按计划启动各 `Source`、健康检查 `/healthz`、`/readyz`（全部市场的连接已订阅且已应用计划版本）、`/metrics`；同进程挂本地执行端 `MarketFetchExecutor` |
| `modules/collector/config/stream/app.yaml` | 配置模板：空间、健康端口、Storage 写入超时、本地执行端并发上限、日志 |
| `packages/servicecatalog/catalog.yaml` | 新组件 `collector-stream`（`scope: any`、`replicas: single`、`required_in_default_profile: false`）；调用 Storage `PrimaryStore.CommitTimeSeriesBatch`、补采用的普通写入与 `StreamCollector` 的权限；新服务 `trpc.moox.collector.MarketFetchExecutor`（`acl` 只放 `collector`） |
| `modules/cli/internal/setup/unitdeploy/unit.go` | 部署端在 control 上按 `moox-collector` 派生应用密钥，以 `MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON`（与 SCF 函数相同的格式）注入海外机的 `collector-stream`，不下发主密钥；Storage 请求体的 AppId 用 `moox-collector`，网关身份仍是 `collector-stream`（设计 7.2） |
| `modules/cli/internal/setup/unitpackage` | 单元的组件与资源清单加入 `collector-stream`（二进制、配置模板、运行清单） |
| 部署工具 | 支持向已部署的单元新增组件（现有 `deploy-unit` 只按原始软件包重试、`native-release.sh install` 只替换已有组件），或按单元重新部署的步骤 |
| `scripts/build` | `build.sh collector-stream`（纯 Go，交叉编译） |
| `moox.toml.example` 与 setup 渲染 | `[placements]` 的 `overseas` 默认加 `collector-stream`；`[[scf_plan.spaces]]` 可写 `default_ingest`／`default_executor`，渲染时 crypto 默认 `stream` + `local`、其他空间 `pull` + `scf` |
| Monitor | 告警规则：`collector-stream` 心跳中断 > 3 分钟；连续 3 个周期有失败标的（疑似下架的不计入）；因子水位停滞 > 30 分钟（阶段 5a） |

### 6.2 测试

- 组件启动、读配置严格解析（未知字段拒绝）、健康检查语义。
- setup 渲染：新部署 crypto 为 `stream`、stockcn 为 `scf`；显式 `ingest = "scf"` 覆盖生效。
- 服务目录一致性测试（`packages/servicecatalog` 现有测试）通过。

### 6.3 发布（与阶段 3 合并为一次）

1. 同一提交构建 Admin、各主机 host-gateway、Monitor、Access、Collector、`collector-stream`、本机 `moox-cli`。
2. 线上 Collector `app.yaml` 先显式写 crypto `default_ingest: pull`、`default_executor: scf`（保持现状），再按顺序替换 Admin → 网关 → Monitor → Access → Collector。
3. 用部署工具在海外机单元新增 `collector-stream`（配置、内部身份、派生后的 Storage 应用密钥、运行清单一起生成），确认 `status.sh` ready、Collector 看到心跳（此时没有分配给它的数据集）；之后的二进制升级才用 `native-release.sh install`，并按设计 7.4 的窗口执行（:02～:10、分钟第 20～40 秒）。
4. 回退：**先恢复旧版本能解析的 Collector `app.yaml`**（去掉 `default_ingest`、`default_executor`、`stream` 段；旧 Collector 严格解析配置，读到新字段会拒绝启动），再按 ledger 回滚各组件；`collector-stream` 可单独停用（数据集都还是 `pull` + `scf` 时它没有分配，不写任何数据）。

## 7a. 阶段 5a：下游因子连续水位（切换前完成）

- 依据设计 7.5；因子模块与补采单独出详细设计与执行计划，这里只列范围与验收。
- 范围：因子集合持久化 `committed_through`；收到 T 期事件时从水位下一期到 T 逐期处理，每一期计算前都检查前面有没有还没补到的无效周期（零提交，或未确认缺失的失败标的超过期望标的的阈值，按因子集合配置、默认 10%，用事件里的 `UniverseSubjectIds` 与 `FailedSubjects` 算），有就延迟重投、水位不前进；当期 T 降级且缺失在阈值以内（`FailedSubjects` 里未确认缺失的标的）不阻塞水位：先算已到的标的，实时计划把 `UpstreamFailed` 的标的记进空洞账本（`pipeline/report.go` 的 `RecordGaps` 现在只记窗口空洞与预热），补采补齐后由 gapfill 只填充补算，不重放原事件；标的级"确认没有数据"（`t_collector_source_absences`）不阻塞也不补算；补采补齐零提交周期或 `degraded` 周期的失败标的后发"周期已补齐"事件，因子阻塞期间另按固定间隔查 Storage 保底；阻塞超过 30 分钟告警，不自动跳过；打开 `gap.enabled`（依赖的 Storage、策略、Trade 升级按[因子窗口空洞防护详细设计](因子窗口空洞防护详细设计.md)的顺序）。
- 验收：人为制造一段零提交周期（停 SCF 或 stream 10 分钟），因子水位停在缺口前；补采补齐后按顺序补算、水位连续前进；期间没有任何一期用缺根的窗口算出值。再造一个只缺一个标的的降级周期（连续、无零提交）：水位照常前进，该标的这一期记在空洞账本里；补采后只填充补算出该值。再造一个缺失超过阈值的降级周期（例如只断开一个市场的连接）：水位停在它之前，补采补到阈值以下后整期重算、水位前进。

## 8. 阶段 6：切换

1. 确认 `collector-stream` 在线（心跳正常）、阶段 5a 已上线。分钟第 20～40 秒执行 `moox-cli collector ingest-mode set --space-id crypto --mode stream --yes`（crypto 全部数据集一起切，同时把补采执行端设为 `local`），记下各数据集的生效周期 P。
2. 跟踪交接阶段：`switch`（SCF 函数确认 P 起为空、stream 确认 P 起全部标的、心跳变为"可写"）在 `P − 30 秒`前完成；P 之后 stream 开始提交；`drain` 结束（各数据集 P + 旧迟到窗口 + 收尾余量中最晚的时刻）后 SCF 定时器才停用。
3. 验收（P 之后 2 小时、覆盖 2 个整点）：
   - 周期标记全部 `complete`，`missed` 为 0；
   - `collector-stream` 写入完成相对收盘：现货、合约 p99 ≤ 1 秒（整点同样）；数据集截止时间已是 1m 5 秒、1h 8 秒，降级周期的完成标记按时发出；
   - 策略 1m 整点产出延迟比切换前下降 5 秒以上；
   - 截止时仍缺的标的数每周期中位为 0；
   - 自动补采经本地执行端执行（停 `collector-stream` 1 分钟制造一个缺口，确认补采走 `local` 并补齐、因子水位等补齐后前进）。
4. 回退演练：在验收后的一个非整点时段执行 `ingest-mode set --space-id crypto --mode pull --executor scf`，确认 SCF 在新的 P 起接手、无缺口，然后再切回 `stream`；再演练一次 `--force`（先停掉 `collector-stream` 模拟失联，再切回 SCF）。

## 9. 阶段 7：收尾

- 阶段 1 的合约推迟随 SCF 停用自然失效，配置保留（切回 SCF 时仍要它）。
- 更新模块文档、部署与运维的"故障切回"操作说明；在[计划目录](README.md)把设计与本计划标为"已实现"。
- 一周后复核：截止时仍缺的标的数、重连次数、24 小时轮换是否无缺失、出站带宽峰值。

## 9a. 阶段 8：切换后的独立任务

### 统一的行情源目录（设计 5.9，原阶段 2 第一项）

- `internal/marketdata` 新增 `Catalog`／`CatalogEntry`／`Factory`／`Deps`（设计 5.9）；`Instrument.Pair` 已在阶段 2 加上。
- 币安、A 股各数据源（新浪、腾讯、东方财富、百度、通达信）、指数、可转债数据源登记到目录。
- `marketwiring/subject_listers.go`（`NewSubjectListers`）、`market_runtime.go`（`newMarketProvider`）、`stock_runtime.go`（`newStockCNProviderForSource`）改为从目录按能力取；删除三处 `switch`。
- 测试：重构前后 `NewSubjectListers` 返回的键集合相同；每个原 `switch` 分支对应的 `(market, instrument, provider, source)` 都能从目录构造出同类型的数据源；未登记的组合报同样的错误；`marketwiring`、`subjectsync`、`marketfetch` 现有测试全部通过。

### 采集名单干预接口（设计 6.6）

- 采集名单干预（设计 6.6）：表 `t_collector_subject_exclusions`；RPC `SetSubjectExclusions`／`ClearSubjectExclusions`／`ListSubjectExclusions`；期望状态计算时去掉排除的标的；到期自动失效；`moox-cli collector subjects exclude|unexclude|exclusions`；活动日志。服务目录：三个方法放行 `console`、`moox-cli` 与外部身份 `collector-operator`（旁路程序经 Access 调用，同时放行 `SyncSubjects`）。测试：排除后下一轮生成新计划且 hash 变化；到期自动恢复；交接进行中设置排除时推迟到交接完成。

## 10. 风险与检查点

| 检查点 | 不通过时 |
| --- | --- |
| 阶段 0 连续合约 K 线全量 | 合约改用普通 K 线（冷门标的约 3 秒到达），仍不兜底，延迟目标改为合约 p99 ≤ 3.5 秒 |
| 阶段 1 合约推迟后仍有不一致 | 调大合约的 `not_before_ms`，并核对 1h 是否需要同样推迟 |
| 切换后发现数值错误（适配器解码） | 立即手动切回 `pull` + `scf`，定位适配器；受影响区间 `--force` 补采，因子按补算任务重算 |
| 阶段 5a 未完成 | 不切换：stream 停机或重启会让因子跳期 |
| 阶段 6 写入完成 p99 超标 | 检查出站带宽与提交预留 `commit_reserve_ms`，必要时切回 SCF |
| 服务目录发布 | 漏换 Monitor 会触发"网关超过 2 分钟没有心跳"告警，按部署与运维的顺序一起换 |

## 11. 交付物清单

- 代码：`Instrument.Pair`、`KlineBatch` 与 `KlineCommitter`、本地执行端 `MarketFetchExecutor`、补采按执行端调用、`internal/stream`、`internal/sources/binance/stream.go`、`marketfetch.KlineCommitter`、对账器执行端抽象、`StreamCollector` 服务、`cmd/stream`、按市场的拉取参数覆盖、setup 渲染默认值、Monitor 告警、因子连续水位与补齐通知（阶段 5a）。阶段 8：`marketdata.Catalog` 与三处装配的改造、采集名单干预接口。
- 配置与目录：`collector.sql` 两张表（阶段 8 再加排除表）、`catalog.yaml` 新组件与权限、`binance.yaml` 的 `stream` 段、`moox.toml.example`。
- 文档：采集、命令行工具、监控、部署与运维的更新；本计划与设计的实现状态。
