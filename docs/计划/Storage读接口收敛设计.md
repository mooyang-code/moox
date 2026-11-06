# Storage 读接口收敛设计

状态：设计稿 v6（2026-10-17），尚未编码。v4：精确读取合并为 `ReadRows`（只返回存在的行），`ReadTimeSeriesRows` 只剩时间区间与最近 N 根；删除 DataView 的按序列取根数模式；DataView 响应字段改名。v5（吸收 Codex 第二轮评审）：覆盖范围改为闭区间的最早 / 最晚行时间；保留行数改为活动索引实际建立的目标；删除 `ReadRecordRows`；服务目录与 ACL 中的方法同步改名。v6（第三轮评审核实）：区间读取与 View 不再处理行属性——因子结果写入本来就拒绝行属性（`primarystore/factor_rows.go:64`），View 重建读取的 `factor.*` 属性键从来没有写入方；行属性只由精确读取 `ReadRows` 返回。项目尚未上线，接口与协议直接修改，不考虑兼容。由[数据归档设计](数据归档设计.md)的讨论引出，归档依赖本设计；实现后以[存储](../模块/存储.md)为准。

## 0. 原则

1. **对外只有两种读**：`PrimaryStore` 读事实（DataNode 的 Pebble，完整、权威）；`DataView` 读视图（DuckDB，近期窗口，用于看板与分析查询）。
   这与 `view.proto` 中已有的说明一致：「View 索引是可重建的近期派生结果，不是完整事实存储，完整历史以 PrimaryStore 为准」。
2. **同一个请求，对谁都返回同样的结果**：不按调用方身份分流，不对某个调用方开特例。
3. **调用方只表达「要哪些数据」**：某个时间范围、或每个序列最近 N 根；不出现「历史」「历史标的」「游标键」这类实现概念。
4. **能复用就复用**：分页用公共的 `common.Page.cursor` / `PageResult.next_cursor`，不另设游标字段；
   「有数据的标的」不单独提供接口，由不指定标的的读取直接覆盖。

## 1. 现状的问题

| 问题 | 位置 | 后果 |
| --- | --- | --- |
| `PrimaryStore.ReadTimeSeriesRows` 按调用方身份分流 | `primarystore/rows_read.go`：`storage-view` 和因子身份的范围读取走 DataNode，其余调用方转给 View | 同一个请求，不同调用方读到的数据范围不同；非因子调用方读超出 View 覆盖（每个序列最近 5000 根）的区间时拿到的是残缺结果，只能靠响应里的 `complete` / `served_indexed_*` 自己发现。CLI 的数据导出、Collector 的读行都属于这种情况 |
| `PrimaryStore.CountTimeSeriesRows` 由 View 回答 | 同上 | 同样受 View 覆盖范围和消费滞后限制。Collector 的历史缺口扫描用它统计「最早生效点之前 500 根」，计数为 0 的标的直接当作「没有历史」跳过，读行也走 View；扫描区间早于 View 覆盖范围时，它什么都看不见。线上核查结果见 1.1 |
| 同一个接口，不同路由返回的格式不同 | DataNode 路径与 View 路径 | 2026-10-17 实测：同一段数据，DataNode 路径的 `data_time` 是 `2026-10-16T12:00:00.000000000Z`，View 路径是 `2026-10-16T12:00:00Z`；DataNode 路径即使数据完整也返回 `complete=false`、覆盖范围为空 |
| 为特定调用方开的接口和参数 | `ListHistorySubjects` 只给 `storage-view`；`latest_per_series` 只能单个标的、只能 View 重建用；因子读取有专门的参数校验 | 每多一个调用方就多一个特例（归档的 v5 设计又要加一个「归档身份」） |
| 自定义游标 | `ReadTimeSeriesRowsReq.after_key`：调用方自己把最后一行的键编码成 `RowKey` 再传回（因子 `storageio/read.go`、View `build.go` 的 `marshalTimeSeriesHistoryCursor`） | 调用方要理解内部键结构；公共分页里已有 `cursor` / `next_cursor` 却没用 |
| 区间读取仍保留页码分页 | DataNode `pageHistory`：页码偏移超过 1 万直接拒绝 | 两种分页并存，页码分页在大区间上不可用 |
| 「先取键、再补字段」两次往返 | View 重建：`latest_per_series` 不带列，只拿键，再调 `ReadFields` 补字段 | 多一次往返，多一个概念 |
| View 为不存在的行属性开了一条读取链 | View 重建第二步 `ReadFields` 带 `factorAttributeKeys` 的 `factor.*` 键补「因子来源属性」（`view/build.go:847`、`view/event_apply.go:678`）；但因子结果写入拒绝行属性（`primarystore/factor_rows.go:64`），这些键从来没有写入方。View 还把事件里的行属性存进内部列 `__moox_attributes` 并在查询中返回，Storage 以外没有读取方 | 区间读取为此要带属性、`TimeSeriesRow.attributes` 要改类型；重建与事件应用对属性的处理也不一致（重建只读 `factor.*`，事件应用合并全部属性） |
| 带 selector 的区间读取扫描整个数据集的时间索引 | `pebble/history.go:244`：只要带时间范围就用时间索引，逐行过滤 selector | Collector 每批 50 个标的，5000 个标的的数据集检查一轮，同一时间范围的索引要遍历约 100 遍 |
| 配置了 gzip 也常常不压缩 | `packages/gatewayclient/trpc.go:125`：请求体不足 1KB 时不设压缩，响应跟随请求 | 「小请求、大响应」的读取（如不带 selector 的最近 N 根、单标的区间读取）在公网上不压缩 |
| 「history」概念外露 | `ListHistorySubjects`、`readHistoricalTimeSeriesRows`、注释与文档中的「历史读取」 | 调用方需要知道 Storage 内部有「历史索引」 |
| 精确读取有两个入口，「存在性」单独返回 | `PrimaryStore.ReadFields` 与 `ReadTimeSeriesRows` 的 `keys` 形态（后者内部就是调前者）；DataNode `ReadFieldsWithPresence` 另外返回 `existing_keys` | 两个入口做同一件事；调用方要同时看行和存在列表才知道一行在不在 |
| 记录数据还有第三个读取入口，且会读 View | `PrimaryStore.ReadRecordRows`（`primarystore/rows_read.go:137`）：版本为空、未指定列或按版本范围读取时转给 View | 又一个 Primary 读 View 的旁路；仓库里没有生产调用方，只有实现和测试 |
| DataView 有一个没人用的按序列取根数模式 | `QueryTimeSeriesRowsReq.rows_per_series` 及配套的 `expected_input_contract_version` / `served_input_contract_version`（`view/series_window_query.go`） | Storage 以外没有调用方 |
| DataView 响应字段命名冗长、含义分散 | `served_indexed_from/to`、`served_active_index_id/revision`、`served_series_bars`，请求里的 `expected_active_index_id/revision` | 「served / expected / active」修饰重复；`bars` 是 K 线的说法 |

### 1.1 线上核查（2026-10-17，只读）

在编译机上用因子引擎的凭据，对同一组请求分别走因子路由（DataNode）和普通路由（View），结合 Collector 库的 `t_collector_history_scans`：

| 数据集 | 历史扫描区间（UTC） | DataNode 行数 | View 行数 | View 覆盖范围 |
| --- | --- | --- | --- | --- |
| crypto 现货 1m | 10-11 22:40 ～ 10-12 07:00 | 0 | 0 | 10-14 11:11 ～ 10-17 03:57 |
| crypto 现货 1h | 09-21 12:00 ～ 10-12 08:00 | 0 | 0 | 10-14 12:00 ～ 10-17 02:00 |
| 对照：crypto 现货 1m 最近 1 小时 | 10-16 12:00 ～ 13:00 | 60 | 60 | 同上 |

（每组抽查 BTC-USDT、ETH-USDT、ADA-USDT，结果一致。线上只有这 4 个 crypto 数据集做过历史扫描，缺口都是 0。）

结论：

- **目前没有表现出问题**：Storage 大约在 10-14 11:00（UTC）清空后重新积累，扫描区间在 Pebble 里本来就没有数据，「0 缺口」碰巧是对的；
- **扫描逻辑本身看不见 View 之外的数据**：1m 数据集积累超过 View 保留的根数（5000 根约 3.5 天，超过 1.2 倍才整理）之后，大约从 10-18 起，
  Pebble 里会有比 View 更早的数据。之后凡是经默认路由读取这段数据（CLI 导出、Collector 读行与计数），都会静默地拿到空结果；
- **另一个与此无关的问题**：清库后，Collector 的「最早生效点」仍取清库前的计划记录（10-12 07:00），历史扫描一直在扫清库前的空区间，
  检查与补采也把 10-12 ～ 10-14 当作计划覆盖期。这属于 Collector 的计划时间线，不在本设计范围内，单独处理。

## 2. 收敛后的接口

### 2.1 `PrimaryStore.ReadTimeSeriesRows`：读事实

所有调用方的请求都由 DataNode 回答。请求字段：

| 字段 | 说明 |
| --- | --- |
| `space_id`、`dataset_id` | 必填 |
| `selectors` | 0–100 个；每个必须带 `subject_id` 和 `freq`（等于数据集频率），`series_tag` 可选（不带表示该标的全部序列）。**不带 selector 表示数据集的全部标的** |
| `column_names` | 必填；返回这些列的值 |
| 以下二选一 | |
| `time_range` | 时间区间，左闭右开，起止都必须给出 |
| `latest_per_series` | 每个序列最近 N 根，N 为 1–10000。按「根数」取，不需要知道时间范围；交易时段、停牌、稀疏序列下无法换算成固定时间段。目前只有 View 重建使用（View 本身就是每个序列最近若干行）；能由调用方算出时间范围的场景（归档、Monitor）一律用 `time_range` |
| `page` | `size` 与 `cursor`；不使用页码 |

返回：

| 形态 | 顺序 | 分页 |
| --- | --- | --- |
| 时间区间 | `data_time, subject_id, series_tag` 升序 | `page.size` 1–2000（默认 1000）；`page_result.next_cursor` 非空时用它作为下一次的 `page.cursor` |
| 每序列最近 N 根 | 按 `subject_id` 升序分组，每个序列内从新到旧 | `page.size` 1–20000（默认 20000），且一页不超过 20 万个值（行数 × 列数），服务端按两者中较小的截断；同一标的可以跨页；`next_cursor` 同上 |

删除：`keys`（精确读取统一用 2.2 的 `ReadRows`）、`order`（区间只有升序；最近 N 根的顺序固定）、`after_key`（由 `page.cursor` 取代）、区间读取的页码。
响应删除 `served_indexed_from`、`served_indexed_to`、`complete`：它们描述的是 View 的覆盖范围，PrimaryStore 的结果总是完整的；
DataView 的响应中保留这三个字段。

游标由服务端生成，内容是不透明的字符串（区间读取为最后一行的键；最近 N 根为当前标的、序列、续读位置和该序列已返回的根数），
调用方原样传回即可；只能顺序向后翻页，不能跳页。

区间读取的执行方式由 DataNode 决定，调用方无感：带 selector 时，在序列索引上逐个序列定位到区间起点，再按 `data_time, subject_id, series_tag` 归并输出，
只扫描所选标的的键；不带 selector 时用时间索引。计数（2.3）复用同一段按序列遍历的代码。

区间读取只返回值，不返回行属性；`TimeSeriesRow.attributes` 删除（DataView 也不再返回行属性，见 2.4）。行属性只由 2.2 的 `ReadRows` 按 `attribute_keys` 返回
（Collector 重采样、行情存储按它读回自己写入的来源信息）。

`data_time` 统一按 RFC3339 输出，没有小数秒时不带小数部分（Go 的 `time.RFC3339Nano` 格式，如 `2026-10-16T12:00:00Z`），与 DataView 一致。

### 2.2 `PrimaryStore.ReadRows`：按精确键读

由 `ReadFields` 改名而来，时序与记录数据都用它，是唯一的精确读取入口：

- 请求：`keys`（最多 1 万个）、`column_names`、`attribute_keys`；
- 只返回**存在的行**：一行有任意字段或属性即为存在；存在但请求的列都没有值时，返回只有键的行；不存在的键不返回。
  「返回了就是存在」，删除响应中的 `existing_keys`；
- 返回顺序与请求中键的顺序一致（跳过不存在的），调用方按键匹配，不依赖位置；
- DataNode 内部对应的函数为 `ReadRows`（替代 `ReadFieldsWithPresence` 与 `ReadFields`）。

### 2.3 `PrimaryStore.CountTimeSeriesRows`：数事实

同样由 DataNode 回答：按标的统计区间内的时间点数（同一时间点多个 `series_tag` 只计一次，与现在的口径相同），一次最多 2000 个标的。
DataNode 只在序列索引上按标的扫描键，不读字段值，代价与行数成正比、与列数无关。

`DataView.CountTimeSeriesRows` 保留，供需要按 View 统计的场景使用。

### 2.4 `DataView.QueryTimeSeriesRows`：读视图

查询能力不变（过滤、排序、页码、TopN），给控制台与 Strategy 这类看板、分析查询用。调整：

- 删除 `rows_per_series` 模式及配套的 `expected_input_contract_version` / `served_input_contract_version`（`view/series_window_query.go` 与测试），
  Storage 以外没有调用方；
- View 不再保存、返回行属性：删除 `factorAttributeKeys`、事件应用中合并行属性的逻辑、DuckDB 索引的 `__moox_attributes` 列（旧索引里的这一列不再读写，
  重建后消失）和查询中的 `rowAttributesToStrings`；重建与事件应用因此一致，都只处理 View 的列；
- 响应字段改名，复用已有消息：
  - `served_indexed_from` / `served_indexed_to` → `min_data_time` / `max_data_time`：回答本次查询的索引中最早、最晚一行的 `data_time`，
    **两端都包含**，是真实存在的行键（Strategy 用 `max_data_time` 判断最后一根，例如 A 股日线是否为上海零点）。不复用 `TimeRange`：
    `TimeRange` 的终点不包含，把最晚行键搬进去，只有一行的索引会变成空区间。`complete` 保留，表示请求区间是否完全落在这两个时间之间；
  - `served_active_index_id`、`served_active_index_revision` → `index`（新消息 `IndexVersion { index_id, revision }`）；
    请求中的 `expected_active_index_id`、`expected_active_index_revision` → `pin`（同一消息），用于多页读取时钉住同一版索引；
  - `served_series_bars` → `retained_rows_per_series`：**活动索引**每个序列至少保留的最近行数。现在返回的是进程配置
    （`view/query.go:141`），调大配置后、重建完成前会偏大，Strategy 可能误判回溯深度足够。改为：索引自己记录建立时使用的保留行数
    （DuckDB 索引的 `view_meta` 增加 `rows_per_series`，与 `indexed_from` / `indexed_to` 放在一起；打开旧索引时补列，默认 0 表示未知），
    响应返回 `min(索引的保留行数, 当前配置)`——调小配置后维护立即按新值裁剪，调大要等重建成功才提高。不改元数据库的 schema。
    Strategy 用它与 `min_data_time` 一起判断活跃序列可回溯的深度。配置项 `[storage_retention] view_bars` 同步改名为 `view_rows_per_series`；
- `SearchRecordRowsRsp` 的覆盖字段同样改为 `min_data_time` / `max_data_time`。

### 2.5 删除的接口

- `PrimaryStore.ListHistorySubjects`：由「不带 selector 的最近 N 根」覆盖（返回的就是有数据的标的及其最近数据）。
- `PrimaryStore.ReadFields`（改名为 `ReadRows`）及 `ReadTimeSeriesRows` 的 `keys` 形态。
- `PrimaryStore.ReadRecordRows` 及其协议消息、转发 View 的逻辑和测试：记录数据的精确读取用 `ReadRows`，搜索用 `DataView.SearchRecordRows`。
- 服务目录（`packages/servicecatalog/catalog.yaml`）中 PrimaryStore 的方法列表、只读方法列表和各调用方 ACL 同步改名、删除
  （`ReadFields` → `ReadRows`，删除 `ListHistorySubjects`、`ReadRecordRows`）；按字符串调用方法名的客户端（Monitor `storagegateway/client.go`、
  Collector `storageio/gateway.go`、CLI `setup_storage.go`）一起改。
- Primary 中按调用方身份分流的逻辑（`storage-view`、因子身份）和因子读取的专门校验：所有调用方使用 2.1 的同一套校验。

### 2.6 内部命名

对外不再出现「history」。DataNode 内部的两个索引改称**时间索引**（`0x03`，时间优先，用于区间读取与翻页）和**序列索引**
（`0x04`，标的优先，用于最近 N 根与计数）；本次改动涉及的文件（`pebble/history.go`、`primarystore/rows_read.go`）中的函数与类型同步改名，
例如 `readHistoricalTimeSeriesRows` → `readDataNodeRows`、`historySelector` → `seriesSelector`。物理键布局与命名空间字节不变，不需要迁移数据。

`ensureHistoryIndex` / `backfillHistoryIndex` 是为「索引命名空间引入之前写入的数据」补建索引的兼容逻辑。两个索引分别在 10-04 和 10-08 引入，
线上 Storage 在 10-14 清空后重新积累，现存数据写入时都已带索引，本次直接删除这段逻辑和对应的补建标记。

## 3. 调用方迁移

| 调用方 | 现在 | 改为 |
| --- | --- | --- |
| 因子引擎、factor-mgr（`factor/internal/storageio`） | 区间读取 + `after_key`，走因子专用分流 | 同一个接口，`page.cursor` 翻页；行为不变 |
| storage-view 重建（`view/build.go`） | `ListHistorySubjects` 列标的，再逐个标的取最近 N 根（只取键），再 `ReadFields` 补字段和来源属性 | 不带 selector 的最近 N 根，带上 View 的列，按 `next_cursor` 逐页读取，一次拿到值；删除 `marshalTimeSeriesHistoryCursor`、补字段步骤与 `factorAttributeKeys` |
| Collector 检查与补采（`klinecheck`） | 计数与读行都由 View 回答 | 接口不变，改由 DataNode 回答；超出 View 覆盖范围的历史扫描从此有效。读行改用 `page.cursor` |
| CLI 数据导出与 K 线查询 | 由 View 回答，可能残缺 | 由 DataNode 回答，总是完整；删除对 `complete` 的判断，改用游标翻页 |
| Monitor 指标曲线（`metrics/query.go` 的 `QueryService.History` → `metrics/storage.go` 的 `QueryHistorySelectors`） | 没有任何生产调用方 | 删除这条读取链，以及只为它存在的类型、依赖和测试 |
| Monitor 主机指标（`hostmetrics/storage_reader.go`） | 倒序、按页码逐页读，由 View 回答 | 改用时间区间读取，按游标顺序翻页（不跳页），由 DataNode 回答 |
| Monitor 行情巡检（`watchdog/market_canary.go`） | `ReadTimeSeriesRows` 的 `keys` 形态 | 改调 `ReadRows`，按键匹配结果 |
| `ReadFields` 的其他调用方（Collector `resample/storage.go`、`marketstorage/storage.go`，Monitor `storagegateway`，CLI `setup_storage.go`，View `event_apply.go`） | `ReadFields`，部分读 `existing_keys` | 改调 `ReadRows`；不再读 `existing_keys`，按「返回即存在」处理；按键匹配，不依赖位置 |
| Strategy（`strategy/internal/input/client.go`、`depth.go`） | 读 DataView 的 `served_indexed_*`、`served_series_bars` 等 | 改读 `min_data_time` / `max_data_time`（含义与原字段相同，最后行键的判断不变）、`index`、`retained_rows_per_series`；多页读取用 `pin` |
| 归档 worker | — | 只用时间区间读取：区间由归档按执行计划算出（见[数据归档设计](数据归档设计.md)） |

## 4. 一并完成的读路径修复

来自 [Storage 读写路径审查](../审查/2026-10-16-Storage读写路径审查.md)，与本设计改的是同一段代码：

| 编号 | 改动 |
| --- | --- |
| S-1 | 字段补全分块大小改为 `min(10000, 100000 / 字段数)`；最近 N 根一页 2 万行，不修必然失败 |
| S-2 | DataNode `ReadRows`：整个请求共用一个快照迭代器，按物理编码排序后每行一次定位、顺序读出各字段并得到存在性；属性命名空间按需查询 |
| S-3 | Primary 元数据快照按代共享、只读，不再每个请求深拷贝；写入校验同一数据集只查一次列映射 |
| S-4 | Outbox 中继用列表读到的数据确定数据集范围，加锁后只确认该条仍在，去掉重复的完整解码校验 |
| S-8 | 过期清理：由节点级写锁改为数据集级互斥；截止桶没有推进时跳过（每小时一次的清理实际每天才有活干）；后台 compaction 串行执行，不再每次起一个 goroutine。删除 `ensureHistoryIndex` 后，与之配套的 `historyBackfillMu` 一并删除 |
| S-9 | 网关客户端：配置 gzip 后总是压缩，不再按请求体大小判断（`packages/gatewayclient/trpc.go`），补「小请求、大响应」的链路测试 |
| S-10 | 带 selector 的区间读取走序列索引（见 2.1） |
| S-11 | 核实后改为不需要：区间读取与 View 不处理行属性，删除为不存在的 `factor.*` 属性开的读取链（见 2.1、2.4） |
| S-13 | 删除 `ReadRecordRows` 这个读 View 的旁路（见 2.5） |

最近 N 根不带 selector 时，DataNode 从游标位置开始在序列索引上逐个标的、逐个序列读取，凑满一页后把续读位置写进游标；
每个序列之间检查 `ctx`。读到的键直接用 S-2 的方式补全字段，不再需要调用方二次读取。

## 5. 测试

- 任何调用方身份的区间读取、最近 N 根、计数都到达 DataNode；结果与 DataNode 直接读取一致；
- 不合规的请求被拒绝：无列、区间缺起止、页大小超限、selector 超过 100、N 超过 10000、同时给出多种形态、使用页码；
- 区间读取用 `next_cursor` 逐页读完的结果，与一次性读取的结果一致，且没有重复和遗漏；
- 不带 selector 的最近 N 根逐页读完，覆盖数据集全部有数据的标的；多个 `series_tag` 的标的每个序列各取 N 根；一个标的跨页时既不重复也不遗漏，
  页大小与值数量上限都生效；
- 带 selector 的区间读取（走序列索引）与不带 selector 再过滤的结果逐行一致，包括多个 `series_tag`、游标翻页；
- `ReadRows` 返回的属性与改造前 `ReadFields` 的结果一致（Collector 重采样读回 `source_hash`）；View 重建与事件应用得到的索引逐行一致（都不含行属性）；
- `ReadRows` 只返回存在的行：存在但请求的列都为空时返回只有键的行，不存在的键不返回；View 事件应用的行为与改造前一致；
- DataView 改名后的字段与 Strategy 的覆盖范围判断一致：只有一行的索引、查询最后一根、A 股日线（上海零点）；
- 调大保留行数配置、活动索引还是旧的时，`retained_rows_per_series` 仍是旧值，Strategy 不会误判历史深度足够；重建切换后才变大；调小配置立即变小；
- 经 host-gateway 与 Access 的真实 `ReadRows` 调用成功（方法名、只读分类、ACL 都已改名）；`ReadRecordRows`、`ListHistorySubjects` 调用被拒；
- Strategy 跨多页读取 DataView 时用 `pin` 钉住同一版索引，中途切换索引时返回冲突并重读；
- 配置 gzip 时，小请求、大响应的调用在链路上被压缩；
- 过期清理期间，其他数据集的写入不被阻塞；截止桶未推进时不发起删除与 compaction；
- 计数与按行读取后去重计数一致，包括多个 `series_tag` 的情况；
- View 重建改造后，重建出的索引与改造前逐行一致；
- S-1～S-4、S-8～S-11 的用例见审查文档；回填场景、写入请求的前后对比 benchmark。

## 6. 发布

项目尚未上线，不考虑兼容：协议、服务目录和所有调用方在同一个提交里改完，一起构建、一起发布。

1. Storage：storage-node → storage-primary → 至少等 30 秒 → storage-view；
2. 调用方：因子引擎（编译机）、factor-mgr、Collector、Monitor、Strategy（S-9 的网关客户端改动随各调用方一起生效；Strategy 依赖 DataView 改名后的响应，
   旧二进制不能继续用）；
3. 服务目录随之变化：Admin、Monitor、各主机 host-gateway、Access 一起换；本机 `moox-cli` 重新构建。

时间选在整点后 :05～:20、分钟中段，避开整点任务；发布后核对因子运行摘要、View 水位和 Strategy 按期触发。

归档（[数据归档设计](数据归档设计.md)）依赖本设计，可以随后发布，也可以合在同一次发布里。
