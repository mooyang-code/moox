# Storage 读接口收敛设计

状态：设计稿 v1（2026-10-16），尚未编码。由[数据归档设计](数据归档设计.md)的讨论引出，归档依赖本设计；实现后以[存储](../模块/存储.md)为准。

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
| `PrimaryStore.CountTimeSeriesRows` 由 View 回答 | 同上 | 同样受 View 覆盖范围和消费滞后限制。Collector 的历史缺口扫描用它统计「最早生效点之前 500 根」，1m 数据集这一段通常早于 View 覆盖范围，计数为 0 时被当作「没有历史」，**很可能根本扫不到缺口**（需要用线上 `t_collector_history_scans` 的结果核实） |
| 为特定调用方开的接口和参数 | `ListHistorySubjects` 只给 `storage-view`；`latest_per_series` 只能单个标的、只能 View 重建用；因子读取有专门的参数校验 | 每多一个调用方就多一个特例（归档的 v5 设计又要加一个「归档身份」） |
| 自定义游标 | `ReadTimeSeriesRowsReq.after_key`：调用方自己把最后一行的键编码成 `RowKey` 再传回（因子 `storageio/read.go`、View `build.go` 的 `marshalTimeSeriesHistoryCursor`） | 调用方要理解内部键结构；公共分页里已有 `cursor` / `next_cursor` 却没用 |
| 区间读取仍保留页码分页 | DataNode `pageHistory`：页码偏移超过 1 万直接拒绝 | 两种分页并存，页码分页在大区间上不可用 |
| 「先取键、再补字段」两次往返 | View 重建：`latest_per_series` 不带列，只拿键，再调 `ReadFields` 补字段 | 多一次往返，多一个概念 |
| 「history」概念外露 | `ListHistorySubjects`、`readHistoricalTimeSeriesRows`、注释与文档中的「历史读取」 | 调用方需要知道 Storage 内部有「历史索引」 |

## 2. 收敛后的接口

### 2.1 `PrimaryStore.ReadTimeSeriesRows`：读事实

所有调用方的请求都由 DataNode 回答。请求字段：

| 字段 | 说明 |
| --- | --- |
| `space_id`、`dataset_id` | 必填 |
| `selectors` | 0–100 个；每个必须带 `subject_id` 和 `freq`（等于数据集频率），`series_tag` 可选（不带表示该标的全部序列）。**不带 selector 表示数据集的全部标的** |
| `column_names` | 必填；返回这些列的值 |
| 以下三选一 | |
| `keys` | 精确键读取，最多 1 万个（现有能力，不变） |
| `time_range` | 时间区间，左闭右开，起止都必须给出 |
| `latest_per_series` | 每个序列最近 N 根，N 为 1–10000 |
| `page` | `size` 与 `cursor`；不使用页码 |

返回：

| 形态 | 顺序 | 分页 |
| --- | --- | --- |
| 精确键 | 与请求顺序一致，只返回存在的行 | 无 |
| 时间区间 | `data_time, subject_id, series_tag` 升序 | `page.size` 1–2000（默认 1000）；`page_result.next_cursor` 非空时用它作为下一次的 `page.cursor` |
| 每序列最近 N 根 | 按 `subject_id` 升序分组，每个序列内从新到旧 | 按标的分页：一页最多 2 万行，同一标的不拆到两页；`next_cursor` 同上 |

删除：`order`（区间只有升序；最近 N 根的顺序固定）、`after_key`（由 `page.cursor` 取代）、区间读取的页码。
响应删除 `served_indexed_from`、`served_indexed_to`、`complete`：它们描述的是 View 的覆盖范围，PrimaryStore 的结果总是完整的；
DataView 的响应中保留这三个字段。

游标由服务端生成，内容是不透明的字符串（内部为最后一行的键或下一个标的），调用方原样传回即可。

### 2.2 `PrimaryStore.CountTimeSeriesRows`：数事实

同样由 DataNode 回答：按标的统计区间内的时间点数（同一时间点多个 `series_tag` 只计一次，与现在的口径相同），一次最多 2000 个标的。
DataNode 只在序列索引上按标的扫描键，不读字段值，代价与行数成正比、与列数无关。

`DataView.CountTimeSeriesRows` 保留，供需要按 View 统计的场景使用。

### 2.3 `DataView.QueryTimeSeriesRows`：读视图

不变。需要「最近一段时间、倒序、按页码翻页」这类看板查询的调用方，直接调用 DataView。

### 2.4 删除的接口

- `PrimaryStore.ListHistorySubjects`：由「不带 selector 的最近 N 根」覆盖（返回的就是有数据的标的及其最近数据）。
- Primary 中按调用方身份分流的逻辑（`storage-view`、因子身份）和因子读取的专门校验：所有调用方使用 2.1 的同一套校验。

### 2.5 内部命名

对外不再出现「history」。DataNode 内部的两个索引改称**时间索引**（`0x03`，时间优先，用于区间读取与翻页）和**序列索引**
（`0x04`，标的优先，用于最近 N 根与计数）；本次改动涉及的文件（`pebble/history.go`、`primarystore/rows_read.go`）中的函数与类型同步改名，
例如 `readHistoricalTimeSeriesRows` → `readDataNodeRows`、`historySelector` → `seriesSelector`。物理键布局与命名空间字节不变，不需要迁移数据。

`ensureHistoryIndex` / `backfillHistoryIndex` 是为「索引命名空间引入之前写入的数据」补建索引的兼容逻辑。线上如果每个数据集都已经有补建完成的标记，
可以一并删除；这需要先在 storage-node 上只读核对，本次不做。

## 3. 调用方迁移

| 调用方 | 现在 | 改为 |
| --- | --- | --- |
| 因子引擎、factor-mgr（`factor/internal/storageio`） | 区间读取 + `after_key`，走因子专用分流 | 同一个接口，`page.cursor` 翻页；行为不变 |
| storage-view 重建（`view/build.go`） | `ListHistorySubjects` 列标的，再逐个标的取最近 N 根（只取键），再 `ReadFields` 补字段 | 不带 selector 的最近 N 根，带上列，按 `next_cursor` 逐页读取，一次拿到值；删除 `marshalTimeSeriesHistoryCursor` 和补字段步骤 |
| Collector 检查与补采（`klinecheck`） | 计数与读行都由 View 回答 | 接口不变，改由 DataNode 回答；超出 View 覆盖范围的历史扫描从此有效。读行改用 `page.cursor` |
| CLI 数据导出与 K 线查询 | 由 View 回答，可能残缺 | 由 DataNode 回答，总是完整；删除对 `complete` 的判断，改用游标翻页 |
| Monitor 指标曲线与主机指标（`metrics/storage.go`、`hostmetrics/storage_reader.go`） | 倒序、页码、可以不给起点，由 View 回答 | 改调 `DataView.QueryTimeSeriesRows`（服务目录已允许 Monitor 调用）；View 按数据集从 Metadata 查得 |
| Monitor 行情巡检（`watchdog/market_canary.go`） | 精确键读取 | 不变 |
| 归档 worker | — | 最近 N 根不带 selector；补读与回补用时间区间 |

## 4. 一并完成的读路径修复

来自 [Storage 读写路径审查](../审查/2026-10-16-Storage读写路径审查.md)，与本设计改的是同一段代码：

| 编号 | 改动 |
| --- | --- |
| S-1 | 字段补全分块大小改为 `min(10000, 100000 / 字段数)`；最近 N 根一页 2 万行，不修必然失败 |
| S-2 | DataNode 读字段：整个请求共用一个快照迭代器，按物理编码排序后每行一次定位、顺序读出各字段并得到存在性；属性命名空间按需查询 |
| S-3 | Primary 元数据快照按代共享、只读，不再每个请求深拷贝；写入校验同一数据集只查一次列映射 |
| S-4 | Outbox 中继用列表读到的数据确定数据集范围，加锁后只确认该条仍在，去掉重复的完整解码校验 |

最近 N 根不带 selector 时，DataNode 从游标指向的标的开始在序列索引上逐个标的读取，凑满一页（最多 2 万行，单个标的不拆页）后返回下一个标的作为游标；
每个标的之间检查 `ctx`。读到的键直接用 S-2 的方式补全字段，不再需要调用方二次读取。

## 5. 测试

- 任何调用方身份的区间读取、最近 N 根、计数都到达 DataNode；结果与 DataNode 直接读取一致；
- 不合规的请求被拒绝：无列、区间缺起止、页大小超限、selector 超过 100、N 超过 10000、同时给出多种形态、使用页码；
- 区间读取用 `next_cursor` 逐页读完的结果，与一次性读取的结果一致，且没有重复和遗漏；
- 不带 selector 的最近 N 根逐页读完，覆盖数据集全部有数据的标的；多个 `series_tag` 的标的每个序列各取 N 根；单个标的超过一页上限时整页只含这一个标的；
- 计数与按行读取后去重计数一致，包括多个 `series_tag` 的情况；
- View 重建改造后，重建出的索引与改造前逐行一致；
- S-1、S-2、S-3、S-4 的用例见审查文档；回填场景、写入请求的前后对比 benchmark。

## 6. 发布

协议有删除的字段（`after_key`、`order`、响应中的 View 覆盖字段）和删除的方法（`ListHistorySubjects`），服务目录随之变化，
因此需要一次协调发布，同一个提交构建：

1. 先发布 Storage：storage-node → storage-primary → 至少等 30 秒 → storage-view；
2. 紧接着发布调用方：因子引擎（编译机，`systemctl --user restart moox-factor-engine`）、factor-mgr、Collector、Monitor；
3. 服务目录变化要求 Admin、Monitor、各主机 host-gateway、Access 一起换；本机 `moox-cli` 重新构建。

时间选在整点后 :05～:20、分钟中段：避开 1h 因子计算和整点任务。Storage 与因子引擎之间的几分钟里，旧引擎的 `after_key` 翻页不可用，
这几期的 1m 因子可能计算失败；发布后核对因子运行摘要，必要时用因子管理 CLI 的 `recalc` 补算。

归档（[数据归档设计](数据归档设计.md)）在这次发布之后单独发布，它的服务目录改动是第二次协调发布。
