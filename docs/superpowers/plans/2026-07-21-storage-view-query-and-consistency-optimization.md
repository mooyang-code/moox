# Storage 模块优化方案：View 组合查询 + 一致性与清理项修复

**日期**：2026-07-21
**范围**：`modules/storage`
**关联设计**：`2026-07-19-storage-consistency-review-remediation.md`
**定位**：个人量化项目的在线数据存储模块。优化取舍以"可靠性与查询能力优先、避免过度设计"为准。

---

## 0. 背景与目标

本方案合并两条工作线：

1. **View 查询引擎条件组合改造**（主线）：让 DuckDB / Bleve 从"当 KV 用"升级为真正的列式 / 倒排组合查询引擎，支持 WHERE 组合、范围、排序、分页下推。
2. **一致性与工程清理项修复**：前期架构评审发现的 P1/P2/P3 问题，与主线一起排期。

### 已确认的设计前提（来自评审讨论）

- 重建 RowKey 来源：旧 ActiveView + 时序取时间子集；Record 与旧 ActiveView 保持一致。**首建无旧 ActiveView，从事件流增量起步**（不做扫描）。
- View 不支持行删除；重建后整库 drop。
- A/B slot 用于控制数据增长（切到新 View，旧 View 直接 drop）；View 是"近期一段数据的在线组合分析"。
- **删除 MemoryEngine**。
- **Bleve 不再存 row_proto**，改存结构化字段，查询结果从 stored fields 重组行。
- FilterSpec 暂不做 `expr_cond` 文本逃生舱（reserved 占位）。
- 记录型 Backfill 的"只填缺失"简化为全量覆盖。
- Bleve 全列 Store。
- **未上线新项目，不考虑兼容**：目录/包/符号可自由改名，不保留任何 `v2` 版本后缀。
- **DataNode 行 DELETE 不做**：删除所有相关设计与代码，恢复"不支持删除"约束的纯粹性。
- **DataNode 范围扫描（ScanFields）全部删除**：View 首建改为纯事件流增量构建，不再从 DataNode 枚举 Key；接受首建初期仅含增量、历史近期数据随事件积累的行为变化（见第三章 C）。
- **SwitchView 不做引用计数**：改用 grace 定时 drop；长查询命中已 drop 的旧库直接报错，由业务方重试（请求方普遍具备重试）。
- **接口合并**：`ViewIndexEngine`/`ViewIndexApplier`/`ManagedEngine`/`QueryEngine` 四层合并为单一 `Engine` 接口；读写采用成对命名 **`Write` / `Query`**（原 Apply/Select）；并**删除 `List()`**（引擎不维护索引清单，存在性以 catalog 为权威）（见第二章 2）。
- **建表同时建索引**（见第二章 3.1）。
- **验收测试本轮补齐**：DuckDB DDL 生成、类型映射、SQL 翻译。

---

## 一、设计不变量对照与本方案的收敛动作

| 不变量 | 评审结论 | 本方案动作 |
|---|---|---|
| 1. 校验与路由共享同一元数据快照 | ❌ 未成立 | 引入 Snapshot 句柄 API，写路径全程用同一句柄（第三章 A） |
| DataNode 不提供范围扫描 | ❌ 已突破 | 删除 ScanFields/PrimaryStoreScanService 全套，View 首建改为事件流增量构建（第三章 C） |
| 不支持删除行/字段 | ❌ 已突破 | 删除 DataNode DELETE 全部相关设计与代码（第三章 D） |
| OldView 引用归零后删除 | ❌ 纯定时删除 | 简化：grace 定时 drop，长查询命中已 drop 报错由业务重试（第三章 E） |
| 13. 旧符号零残留 | ❌ 部分残留 | 清理 slots.go 死代码、legacy build tag、compat 层、目录改名（第四章） |

---

## 二、View 查询引擎条件组合改造（主线）

### 1. Proto 契约改造

借鉴 `xData-mini/storage/proto` 的 `Operator` + `Cond` + `CondGroup` + `Options` 分层布尔结构，做规模收敛。

#### 1.1 common.proto —— 替换 FilterExpr

```proto
// 过滤操作符（借鉴 xData-mini Operator）
enum FilterOp {
  FILTER_OP_UNSPECIFIED = 0;
  FILTER_OP_EQ      = 1;   // =
  FILTER_OP_NE      = 2;   // !=
  FILTER_OP_GT      = 3;   // >
  FILTER_OP_GTE     = 4;   // >=
  FILTER_OP_LT      = 5;   // <
  FILTER_OP_LTE     = 6;   // <=
  FILTER_OP_IN      = 7;   // IN (...)
  FILTER_OP_NOT_IN  = 8;   // NOT IN (...)
  FILTER_OP_LIKE    = 9;   // 子串匹配：bleve match / duckdb LIKE %v%
  FILTER_OP_BETWEEN = 10;  // 闭区间，values 恰好 2 个
}

enum FilterLogical {
  FILTER_LOGICAL_AND = 0;  // 默认
  FILTER_LOGICAL_OR  = 1;
}

// 单个条件：列名 + 操作符 + 值（值走 TypedValue，天然带类型）
message FilterCond {
  string column = 1;               // 必须命中 View schema 列白名单
  FilterOp op = 2;
  repeated TypedValue values = 3;  // EQ/GT 用 1 个；IN 用 N 个；BETWEEN 用 2 个
}

// 条件组：组内 conds 按 logical 组合
message FilterGroup {
  repeated FilterCond conds = 1;
  FilterLogical logical = 2;       // 组内关系，默认 AND
}

// 顶层过滤：groups 间按 group_logical 组合
message FilterSpec {
  repeated FilterGroup groups = 1;
  FilterLogical group_logical = 2; // 组间关系，默认 AND
  reserved 3;                      // 预留 expr_cond 文本逃生舱（暂不实现）
}
```

原 `FilterExpr` 直接删除（无外部消费者）。`SortSpec`（field_name + desc）保留。

#### 1.2 view.proto —— 请求字段调整

```proto
message QueryTimeSeriesRowsReq {
  ...
  FilterSpec filter = 7;   // 替换 repeated FilterExpr filters
  repeated SortSpec sorts = 8;
}
message SearchRecordRowsReq {
  ...
  string text_query = 5;   // bleve 全文
  FilterSpec filter = 7;   // 结构化字段过滤，与 text_query 取 AND
  repeated SortSpec sorts = 8;
}
```

**安全底线**：`column` 一律走 schema 列白名单校验；值全部走参数绑定（DuckDB `?`）或 bleve query 对象；**永不字符串拼 SQL**。

### 2. Engine 接口改造（viewindex/model.go）

**四层接口合并为单一 `Engine`**。原 `ViewIndexEngine`/`ViewIndexApplier`/`ManagedEngine`/`QueryEngine` 的分层对本项目是过度设计——DuckDB / Bleve 都同时实现全部方法，没有"只管理不查询"的独立实现，分层只带来理解成本而无解耦收益。

```go
type Filter struct {
    Column string
    Op     pb.FilterOp
    Values []*pb.TypedValue
}
type FilterGroup struct {
    Conds   []Filter
    Logical pb.FilterLogical
}
type QuerySpec struct {
    Keys         []*pb.RowKey     // 非空 => 精确点查（时间已定的 key）
    TimeRange    *pb.TimeRange    // 时序闭开区间
    VersionRange *pb.VersionRange
    TextQuery    string           // 仅 record/bleve
    Groups       []FilterGroup
    GroupLogical pb.FilterLogical
    Sorts        []*pb.SortSpec
    Offset, Limit int
}

// Engine 是 View 索引引擎的唯一接口（DuckDB / Bleve 各实现一份）
// 读写采用成对命名 Write / Query，语义通用、词性对仗。
type Engine interface {
    Engine() string
    Prepare(ctx context.Context, id string, schema ViewIndexSchema) error  // 重建时建表建索引
    Write(ctx context.Context, id string, batch ViewIndexWriteBatch) error // 批量写入（LiveWrite/Backfill/Replace）
    Query(ctx context.Context, id string, spec QuerySpec) (rows []*pb.RowFieldValues, total int64, err error) // 条件组合查询
    Stat(ctx context.Context, id string) (ViewIndexStats, error)
    Remove(ctx context.Context, id string) error                            // 切换后删库
}
```

- **删除 `List()`**：引擎不再自己维护"我有哪些索引"的清单。索引存在性以 **catalog 元数据（view 定义 + slot 状态）为唯一权威**；reconcile / 崩溃恢复从 catalog 读该建/该删哪些索引，而非反问引擎枚举文件。引擎变为无状态"给 id 就干活"。`Prepare`（重建建表）与 `Remove`（切换删库）是必需的物理操作，保留。

  删除 `List()` 需迁移三处现有调用方到 catalog：
  - `view/service.go:78-86` 启动时用 `engine.List` 回填 `indexEngine[id]→engine name` 映射 → 改为从 catalog 读各 view 的 active/new slot 及其引擎类型；
  - `view/service.go:241-264` `ListViewIndexes` RPC 枚举所有引擎索引 → 改为遍历 catalog 中的 view+slot，再对每个 id 调 `Stat`；
  - `viewindex/service.go:75` 及 `viewindex/client.go:76` 的 `List` 封装 → 删除。
- 删除旧的点查/翻页/删除方法（旧 `Query`/`Scan`/`Delete`，其中 Delete 违反"不删"约束）；新 `Query` 是唯一读入口，点查是 `Query` 中 `Keys != nil` 的特例。
- `total` 支持 `TotalMode`（COUNT(*) 或 -1 不计数）。
- **删除 MemoryEngine**：`NewMemoryEngine`、`memoryIndex`、`persistedIndex`、`load/persistLocked`（model.go:170-419）全部删除；保留 `RowKeyID`、`HashViewIndexSchema`、`ViewIndexSchema`、`WriteMode`，并将 `ViewIndexApplyBatch` 重命名为 **`ViewIndexWriteBatch`**（与 `Write` 方法一致，`RowWrites` 字段名保持）。列式化后 `ApplyRowWriteWithMode`/`mergeFields`（model.go:453-514）不再需要，删除。

### 3. DuckDB 引擎重写（时序 View）

#### 3.1 Prepare：按 schema 建真实列表

A/B 机制保证单索引生命周期内 schema 不变（schema 变 → schema_hash 变 → 新 slot），**无需 ALTER TABLE**。

```sql
CREATE TABLE view_rows (
  subject_id VARCHAR NOT NULL,
  freq       VARCHAR NOT NULL,
  data_time  TIMESTAMP NOT NULL,
  <由 schema.Columns 生成的业务列>,
  PRIMARY KEY (subject_id, freq, data_time)
);
-- 建表同时建索引：PK 覆盖"按 subject 取时间段"主路径；
-- 额外对 data_time 建索引，支撑跨 subject 的时间范围扫描。
CREATE INDEX idx_view_rows_data_time ON view_rows (data_time);
-- 业务列索引按 schema 中标记为常用过滤的列（如 SortOrder 或专门标记）按需创建，
-- 不盲目全建；DuckDB 对未建索引列有 min-max zonemap 兜底。
```

**索引策略**：PK `(subject_id, freq, data_time)` 覆盖最常见的"某标的某频率某时间段"查询；`data_time` 单列索引支撑"全市场某时间段"扫描；业务列（如 close/volume）是否建索引由实际查询模式决定，默认不建、靠 DuckDB zonemap，观察到热点过滤列再补。

类型映射：

| FieldValueType | DuckDB 类型 |
|---|---|
| INT / INT64 | BIGINT |
| DOUBLE / FLOAT | DOUBLE |
| STRING | VARCHAR |
| BOOL | BOOLEAN |
| TIME | TIMESTAMP |
| BYTES / JSON | BLOB / JSON |
| 未知 | VARCHAR（降级兜底 + 告警日志） |

#### 3.2 Write：批量 UPSERT，三种 WriteMode 下沉为 SQL

```sql
-- LiveWrite / Replace：全列覆盖
INSERT INTO view_rows (subject_id,freq,data_time, <cols>)
VALUES (?,?,?, <vals>)
ON CONFLICT (subject_id,freq,data_time) DO UPDATE SET col = excluded.col, ...;

-- Backfill（只填缺失）：
  ... DO UPDATE SET col = COALESCE(view_rows.col, excluded.col), ...;
```

- prepared statement + `tx` 批量执行；`RowWrite.Fields` 按 column_name 定位列位置，缺失列填 NULL；
- 事务提交后更新 view_meta 的 view_version/schema_hash（保留原乐观校验）。

#### 3.3 Query：翻译成 SQL

```
SELECT subject_id,freq,data_time, <includes 或全列>
FROM view_rows
WHERE data_time >= ? AND data_time < ?            -- TimeRange（走 PK 前缀）
  AND/OR ( <group conds join by group.logical> )  -- FilterGroup × GroupLogical
ORDER BY <sorts 映射列名> [DESC]
LIMIT ? OFFSET ?
```

- 每 `FilterCond` → `column op ?`（IN → `IN (?,..)`；BETWEEN → `BETWEEN ? AND ?`；LIKE → `LIKE '%'||?||'%'`）；
- 列名先过白名单，非白名单 `INVALID_PARAM`；值从 `TypedValue` 取原生值绑定；
- 结果行 → `pb.RowFieldValues`（key 从 subject_id/freq/data_time 重组，fields 从列值重组）。

#### 3.4 Stat & 连接管理

- `Stat` → `SELECT count(*), min(data_time), max(data_time)`（替代逐行 Unmarshal）；
- **per-index 缓存 `*sql.DB`**：`IndexManager` 持 `map[id]*sql.DB` + mutex，Prepare / 首次访问 open，Remove / 切换 close。消除每 RPC open/close。

### 4. Bleve 引擎重写（记录 View，不存 row_proto）

#### 4.1 Prepare：按 schema 建 per-field mapping

- 数值列 → NumericFieldMapping；时间列 → DateTimeFieldMapping；文本列 → text（全文）+ keyword（精确/排序）；
- record_id / version → keyword（范围 + 排序）；额外 all_text 聚合字段供 TextQuery；
- **文档只存字段值，不存 row_proto**。需返回的列设 `Store: true`（全列 Store，记录型数据量不大）；行的重组从 stored fields 完成，key 从 record_id/version stored 字段重组。

#### 4.2 Write：Batch + 无 RMW

- `index.NewBatch()`，每行 `batch.Index(RowKeyID, doc)`；
- 记录型 Backfill 全量覆盖（源为旧 ActiveView 一致数据，无需只填缺失判断）；
- **per-index 缓存打开的 `bleve.Index`**，全局 `i.mu` 改 per-index 锁；消除每操作 open/close。

#### 4.3 Search：ConjunctionQuery 组合

- text_query → MatchQuery(all_text)；version_range → TermRangeQuery；
- 每 FilterGroup → Conjunction/Disjunction（按 group.logical）；顶层按 GroupLogical 组合；
- cond → 数值 NumericRangeQuery / 文本 Match 或 Term；
- `SortBy` 下推排序，`Fields = includes` 下推列裁剪，原生 from/size 分页；
- 删除服务层 `filterRecordRows` 二次过滤（service.go:449-476）。

### 5. 服务层改造（viewv2/service.go）

- 删除 `if len(req.GetFilters()) != 0 { return 不支持 }`（service.go:414）；
- 构造 `QuerySpec`（校验列白名单、翻译 filter/sort/page）→ 调 `engine.Query`；
- 删除 `scanAll` + `filterTimeSeriesRows` + `filterRecordRows` 全套内存过滤路径（service.go:399-476）；
- **查询锁缩小**：锁内只取 active indexID 立即放锁（见第三章 E）。

---

## 三、一致性修复（P1）

### A. 校验与路由共享同一元数据快照（P1-2 / 不变量 1）

**问题**：校验（`primarystorev2/service.go:61` → `metadata_validator.go:32`）与路由（`service.go:73` → `main.go:94`）各自读 cache 当前快照指针，中间任何 CRUD 触发 `Refresh()` 都会换快照；`ValidateRow` 内部分页拉列每页可能跨快照。

**方案**：
- 给 metadata cache 增加 `Snapshot()` 句柄 API，返回一个固定快照视图（持有底层 immutable 指针）；
- 写路径入口取一次 `snap := cache.Snapshot()`，校验、分页拉列、路由**全程使用 `snap`**；
- `ValidateRow` 改为接收 snapshot 句柄，内部分页不再各自读 cache。

### B. resolver 并发写 map（P1-1）

**问题**：`cmd/server/main.go:92-111` resolver 闭包对普通 `map[string]pb.DataNodeService` 无锁读写，多 DataNode 并发首写触发 `concurrent map writes` panic。

**方案**：加 `sync.Mutex` 或改 `sync.Map`。**优先立即修**（一行改动，几乎必然在多节点下踩中）。

### C. 删除 DataNode 范围扫描 + 首建改为事件流增量构建（P1-3）

**前置澄清（KV 读写接口是符合设计的，予以保留）**：DataNode 底层 Pebble 是 KV 存储，其 RPC 面（`proto/data_node.proto:83-88`）中 `WriteFields`（字段级 upsert + outbox 原子提交，store.go:106-215）与 `ReadFields`（给定精确 RowKey/fieldID 逐个 `db.Get` 点查，store.go:285）正是设计要求的 KV 写入 + KV 点查接口，**保持不变**。本节删除的对象**仅是范围扫描 `ScanFields` 及其上层 `PrimaryStoreScanService`**，不涉及 KV 读写接口。

**决策已定（方案 A）**：彻底删除范围扫描，View 首建改为**纯事件流增量构建**——不再从 DataNode 枚举 Key，这样"重建 RowKey 只来自旧 ActiveView / 事件流"的约束得以完全自洽（首建时无旧 ActiveView，从事件流起步）。

**行为变化（需接受）**：View 首建不再一次性拉取历史近期数据；首建从订阅 `fields_changed` 事件开始逐步积累。由于 View 定位为"近期一段数据的在线组合分析"，首建初期只含订阅之后到达的增量数据，历史近期数据随事件自然到达（或通过一次事件重放补齐）。`ViewIndexStats.IndexedFrom/IndexedTo` 反映当前已覆盖区间，查询侧据此返回 `complete=false` 提示数据尚在积累。

**删除清单**：
- proto：`data_node.proto` 的 `ScanFields` RPC + `ScanFieldsReq/Rsp`；`primary_store.proto` 的整个 `PrimaryStoreScanService`（`ScanTimeSeriesRows`/`ScanRecordRows`/`GetShardHeads`）；
- 实现：`datanode/pebble/scan.go`（整个文件）、`datanode/service.go:168-182` 的 `ScanFields`；`primarystorev2/scan.go`（整个文件，含 `GetShardHeads` Shard 残留）；
- 装配：`main.go:159-163`（PrimaryStoreScan 服务注册）、`:280`（primaryScanProxy）、`:336-337`（adapter.ScanFields）；
- 配置：`config/loader.go:82,330` 的 `PrimaryStoreScanServiceName` 及相关测试（`loader_test.go:61-62`）；
- reconcile：`reconcile.go` 的 `BackfillInitialView` 及其对 PrimaryStoreScan 的调用删除，首建路径改为"Prepare 新 slot → 订阅事件流增量 Write → 达到覆盖条件后切为 active"；
- 测试桩：`main_test.go:35` 的 `cleanupNode.ScanFields` 桩删除。

### D. 删除 DataNode 行 DELETE（P1-4）

**决策已定**：不做行删除，删除所有相关设计与代码：
- proto：`RowFieldOperation` 的 `ROW_FIELD_OPERATION_DELETE`（rows.proto:40）枚举值及相关字段；
- `datanode/pebble/store.go`：`WriteFieldsEvent` 中的 DELETE 分支（store.go:147-151）、`deleteRowFromBatch`（store.go:217-240）整个删除；
- `datanode/service.go` 及上游对 DELETE 操作的构造/透传；
- 校验逻辑中"delete row must not contain fields"（store.go:246-251）分支删除。

恢复"不支持删除行/字段"约束的纯粹性，Backfill"只填缺失"的前提（行不消失）由此成立。

### E. SwitchView：简化为 grace 定时 drop（P1-5）

**决策已定**：不做引用计数（对个人项目过度）。
- 查询入口锁内只取 `runtime.active` 的 indexID，立即放锁（消除查询与写入互斥，P2-7 一并解决）；
- SwitchView 切 active 后，旧 indexID 等 grace 时长后 drop；
- 长查询若跨过 grace 窗口命中已 drop 的旧库，`Query` 直接返回错误（如 `INDEX_NOT_FOUND`），由业务方重试——请求方普遍具备多次重试能力，重试会命中新 active。

```go
// 查询入口：锁内只取 active indexID，立即放锁；不加引用计数
runtime.mu.Lock()
indexID := runtime.active
runtime.mu.Unlock()
rows, total, err := engine.Query(ctx, indexID, spec)
// err 命中已 drop 旧库 => 返回可重试错误码，业务重试落到新 active
```

---

## 四、架构与工程清理（P2 / P3）

### P2

| # | 问题 | 位置 | 动作 |
|---|---|---|---|
| 6 | viewv2/service.go 1498 行巨型文件 | service.go | 按"消费 / 构建 / 查询"拆三个文件（不拆包） |
| 7 | 查询持 runtime.mu 与写互斥 | service.go:300-350 | 由第三章 E 一并解决（锁内只取 indexID 立即放锁） |
| 8 | Backfill offset 分页可能漏行/重复 | service.go:989-1057 | 改为按 key 游标续传；漏行=永久缺数据，必须修 |
| 9 | DuckDB/Bleve 当 KV 用 | duckdb/bleve | 由第二章 3、4 列式化/字段化解决 |
| 10 | cache 快照装载非一致性时间点 | cache/store.go:487-537 | 16 个分页查询包进一个 SQLite 只读事务 |
| 11 | UpsertDataset TOCTOU / 类型校验可绕过 / 每行全量拉列 | crud_dataset.go:29-45, metadata_validator.go:71 | 不可变检查包事务；UNSPECIFIED 列拒绝写入或强制声明类型；校验用快照句柄避免重复解码 |
| 12 | 跨 Dataset 批量写部分提交无回报 | primarystorev2/service.go:71-89 | 返回已成功 keys 或错误中注明部分成功偏移 |
| 13 | 错误可见性偏弱 | main.go:190/198, reconcile.go, event.go:54 | 所有吞错点补日志（cleanup / reconcile 单 view / BindOutboxID Unmarshal） |
| 14 | 路由目标来自 env 而非 cache | main.go | 从 Snapshot 读 `service_target`，删除 `MOOX_STORAGE_NODE_TARGETS` |
| 15 | cleanup cutoff 硬编码 24h | main.go:203-240 | 用可配置 bucket duration 计算 cutoff |

### P3

- **死代码删除**：`viewindex/slots.go` 的 `SlotCoordinator`/`ViewWriteTargets`/`ActiveHandle`（全模块零引用）；`cache/store.go:111-128` 三个从未使用的索引（补上真实索引或明确接受 O(N)）。
- **旧符号残留删除**：`producer_bus.go` 的 `//go:build legacy_eventconsumer` + 旧 `rows_committed` Topic；`primarystorev2/compat.go` 迁移期 adapter。
- **目录/命名收敛**（去掉所有 `v2` 后缀）：

  | 现名 | 职责 | 新名 |
  |---|---|---|
  | `primarystore` | 元数据 CRUD（dataset/column/view/node 定义） | **`catalog`** |
  | `primarystorev2` | 数据面（字段读写路由到 DataNode） | **`primarystore`**（正名，与设计文档术语 PrimaryStore 一致） |
  | `viewbuilder` | 旧 View 构建 | 删除（残留） |
  | `viewv2` | View 消费 / 构建 / 查询 | **`view`** |

  `catalog` 采用业界标准术语（Iceberg / Hive / Unity Catalog 均以 catalog 指元数据目录），比 `metadata` 更精确。删除 `viewbuilder` 及 `//go:build legacy_eventconsumer`、`compat.go` 等全部残留。
- **小问题**：分页 uint32 溢出（store.go:865）；跨快照分页（store.go:207-220，用快照句柄修）；`CountFieldsByGroup` 绕缓存直查 base（store.go:383，统一走缓存）。

---

## 五、实施顺序

| 阶段 | 内容 | 依赖 |
|---|---|---|
| 0 | P1-1 resolver 加锁（立即修） | 无 |
| 1 | SwitchView 查询锁缩小（锁内只取 indexID，主线前置） | 无 |
| 2 | proto：FilterSpec/FilterCond/FilterGroup/FilterOp + 请求字段 | 无 |
| 3 | 合并为单一 `Engine` 接口（Write/Query），删 MemoryEngine 与 List | 阶段 2 |
| 4 | DuckDB 列式化（Prepare/Write/Query/Stat/连接缓存） | 阶段 3 |
| 5 | Bleve 字段化 + 去 row_proto + ConjunctionQuery + 句柄缓存 | 阶段 3 |
| 6 | 服务层接 `engine.Query`，删 scanAll/filter* 内存过滤 | 阶段 4/5 |
| 7 | P1-2 Snapshot 句柄 API + 写路径改造（含 P2-11/14、P3 跨快照分页） | 无（可并行） |
| 8 | P1-3 删除 ScanFields/PrimaryStoreScanService 全套 + 首建改事件流增量构建 | 阶段 3 |
| 9 | P1-4 删除 DELETE 全部相关设计与代码 | 无 |
| 10 | P2-8 Backfill 游标续传 | 阶段 4/5 |
| 11 | P2-6 拆文件、P2-10 只读事务、P2-12/13/15 | 无 |
| 12 | P3 死代码/残留/改名清理 | 全部功能项完成后 |

---

## 六、决策记录（已全部拍板）

1. **P1-4 DataNode DELETE**：不做，删除全部相关设计与代码。
2. **目录改名**：`primarystore`→`catalog`、`primarystorev2`→`primarystore`、`viewv2`→`view`、删除 `viewbuilder`。
3. **验收测试**：本轮补齐 DuckDB DDL 生成 / 类型映射 / SQL 翻译，扩充 `duckdb/index_manager_test.go`、`bleve/index_test.go`。
4. **Engine 接口**：四层合并为单一 `Engine`。
5. **SwitchView**：不做引用计数，grace 定时 drop + 业务重试。
6. **建表建索引**：PK + data_time 索引，业务列按需。
