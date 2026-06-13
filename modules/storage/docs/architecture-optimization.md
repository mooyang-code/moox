# Storage 架构优化方案

本文记录 storage 服务在实时量化数据、动态因子、分析查询和冷数据归档上的优化方向。项目尚未上线，因此本文优先追求清晰的长期模型，不考虑向后兼容。

## 目标

storage 服务需要同时支持几类数据：

- 在线时序数据，例如 tick、K 线、行情快照。
- 静态结构化数据，例如股票公司信息、证券基础资料。
- 动态因子数据，例如 `MA(20)`、`MA(60)`、`RSI(14)`。
- 文本数据，例如公告、新闻、公司简介。
- 冷数据和备份数据，例如历史行情归档。

核心原则是：上层协议表达“用户要什么数据”，底层执行器再选择 RocksDB、DuckDB、Bleve、CSV 或 Parquet。协议不暴露物理表名、宽表版本和具体存储引擎。

## 存储组件职责

| 组件 | 角色 | 适合场景 | 不适合场景 |
| --- | --- | --- | --- |
| Memory | 热缓存 | 最新价、最新 K 线、最近 N 条、写入缓冲 | 唯一事实存储、崩溃恢复 |
| MQ / WAL / JetStream | 写入日志 | 削峰、重放、异步 fan-out、解耦写入和投影 | 直接查询业务数据 |
| RocksDB | 在线事实层 | `object_id + freq + time_range` 范围查询、最新值、低延迟读写、崩溃恢复 | SQL、复杂过滤、横截面聚合 |
| DuckDB 长表 | 分析事实层 | 动态因子、动态字段、批量分析、回测查询 | 高频小批量并发写、在线 schema 频繁变更 |
| DuckDB 宽表投影 | 查询加速层 | K 线 + 热门因子的组合筛选、排序、横截面查询 | 唯一事实存储、频繁原地改表 |
| Bleve | 文本索引 | 股票名、公司简介、公告、新闻、标签检索 | 数值时序查询 |
| CSV / Parquet | 冷归档 | 降冷、备份、离线导出、长期保存 | 在线随机查询 |
| SQLite / 元数据 DB | 控制面 | schema、dataset、field、route、projection 版本 | 高频行情数据 |

RocksDB 仍应保留。它填补 Memory 和 DuckDB 之间的空位：Memory 快但不可靠，DuckDB 好查但不适合承担高频在线写入事实库。RocksDB 适合做可恢复、低延迟、按 key 有序扫描的在线时序存储。

## 数据集类型

建议给 dataset 增加明确的数据集类型：

```text
TIME_SERIES    // K 线、tick、行情序列
STATIC_OBJECT  // 公司信息、股票基础资料
SNAPSHOT       // 某个时点的截面数据
RANKING        // 成交榜、涨跌幅榜、资金榜
DOCUMENT       // 公告、新闻、研报
EVENT          // 事件流
FACTOR         // 因子值
```

不同类型可以共享访问层和元数据系统，但不应被迫共享同一种物理表模型。

## 写入链路

建议支持两种写入确认语义。

### 同步写入

```text
API -> RocksDB 成功 -> 返回成功
                   -> 发布变更事件
                   -> 异步同步 DuckDB / Bleve / CSV / Parquet
```

同步写适合低频数据、修正数据、手工录入数据，以及要求写后立刻可查的场景。

### 异步接入

```text
API -> durable MQ / WAL 成功 -> 返回 accepted
                          -> 后台 batch writer -> RocksDB
                                                -> DuckDB / Bleve / CSV / Parquet
```

异步接入适合 tick、高频行情和批量导入。异步写不是“不实时”，而是接收实时，查询可见性允许短暂滞后。

协议需要明确返回语义：

```text
write_mode: SYNC_COMMIT | ASYNC_INGEST
request_id: 幂等键
ingest_seq: 接入序号
visible_after_seq: 可选，可见性水位
```

## 动态因子模型

动态因子不应通过动态字段直接表达。建议把因子拆成“定义”和“实例”。

```text
factor_def:
  factor_id
  name              // MA, RSI, MACD
  version
  source_dataset
  expression_hash / code_hash
  value_type        // double, int, string, json, vector

factor_instance:
  factor_instance_id
  factor_id
  params_json       // {"window":20,"price":"close"}
  output_name       // ma_20_close
  freq
  enabled
```

`MA(20)`、`MA(60)` 和 `MA(120)` 是三个不同的 factor instance。新增因子时新增元数据，不要求修改在线事实库 schema。

## RocksDB 因子存储

RocksDB 使用窄模型存在线因子事实：

```text
key:
  factor:{project_id}:{object_id}:{freq}:{ts}:{factor_instance_id}

value:
  typed_value
  calc_version
  source_ts
  updated_at
```

该模型支持：

- 动态新增因子。
- 按标的、周期、时间范围查询某个因子。
- 因子重算和覆盖。
- 崩溃后恢复在线查询能力。

如果常见查询是“某个时点取一个标的的全部因子”，可以增加辅助快照 key：

```text
factor_row:{project_id}:{object_id}:{freq}:{ts}
  -> map<factor_instance_id, value>
```

该快照适合作缓存，不建议作为唯一事实。

## DuckDB 因子存储

DuckDB 侧保留两种表。

### 长表

长表是动态因子的分析事实表：

```sql
factor_values_long (
  project_id BIGINT,
  dataset_id BIGINT,
  object_id VARCHAR,
  freq VARCHAR,
  ts TIMESTAMP,
  factor_instance_id BIGINT,
  value_double DOUBLE,
  value_int BIGINT,
  value_string VARCHAR,
  value_json JSON,
  calc_version VARCHAR,
  updated_at TIMESTAMP
)
```

动态新增因子只会新增 `factor_instance_id`，不需要 `ALTER TABLE ADD COLUMN`。

### 宽表投影

宽表是查询加速表，不是唯一事实表：

```text
bar_factor_snapshot_1d_v4
bar_factor_snapshot_1m_v2
```

宽表包含 K 线字段和热门因子字段：

```text
object_id
freq
ts
open
high
low
close
volume
ma20
ma60
rsi14
...
```

多因子组合查询优先命中宽表：

```sql
SELECT object_id
FROM bar_factor_snapshot_1d_v4
WHERE ts = ?
  AND close > ma20
  AND ma20 > ma60
  AND rsi14 < 30
ORDER BY volume DESC
LIMIT 100;
```

## DuckDB 宽表版本策略

DuckDB 支持 `ALTER TABLE ADD COLUMN`，但不建议在用户查询路径上频繁原地改宽表。schema 变更会影响查询计划、字段缓存和连接中的 schema 可见性。

推荐使用新版本宽表：

```text
bar_factor_snapshot_1d_v3  // 当前 active
bar_factor_snapshot_1d_v4  // 后台构建
```

构建流程：

1. 新建下一版本宽表。
2. 从 K 线事实表和 `factor_values_long` 回填数据。
3. 校验行数、时间范围和字段完整性。
4. 原子切换 projection 元数据中的 active version。
5. 延迟清理旧版本。

元数据示例：

```text
projection_name: bar_factor_snapshot_1d
active_version: v4
physical_table: bar_factor_snapshot_1d_v4
metrics: close, volume, ma20, ma60, rsi14
status: ACTIVE
```

协议只表达逻辑投影，不暴露物理表和版本。

## K 线和因子的关系

K 线和因子应事实分离，查询投影合并。

事实层：

```text
bar_values:
  object_id
  freq
  ts
  open
  high
  low
  close
  volume
  amount

factor_values_long:
  object_id
  freq
  ts
  factor_instance_id
  value_double / value_json / ...
```

投影层：

```text
bar_factor_snapshot_wide:
  object_id
  freq
  ts
  open
  high
  low
  close
  volume
  ma20
  ma60
  rsi14
```

这样可以避免因子重算污染原始行情，也可以让宽表随时从事实层重建。

## 查询协议分层

建议新增明确的查询类型，而不是让 `SearchData` 承担所有语义。

```text
ScanTimeSeries:
  在线时序读取。优先走 RocksDB。

QueryFrame / ScreenData:
  K 线 + 多因子组合查询。优先走 DuckDB 宽表投影，可回退长表。

TextSearch:
  文本检索。走 Bleve。

GetObject / QueryObject:
  股票公司信息、基础资料和其他静态结构化数据。
```

## ScanTimeSeries 草案

`ScanTimeSeries` 用于查单个或少量标的的时间范围数据。

```protobuf
message ScanTimeSeriesReq {
  int64 project_id = 1;
  int64 dataset_id = 2;
  string object_id = 3;
  string freq = 4;

  int64 start_ts_ms = 5;
  int64 end_ts_ms = 6;

  repeated MetricRef metrics = 7;
  int32 limit = 8;
  bool desc = 9;
}

message MetricRef {
  MetricKind kind = 1;
  string field_key = 2;              // open, high, low, close, volume
  int64 factor_instance_id = 3;      // ma20, ma60, rsi14 等动态因子
}

enum MetricKind {
  METRIC_KIND_UNSPECIFIED = 0;
  METRIC_KIND_BAR_FIELD = 1;
  METRIC_KIND_FACTOR = 2;
  METRIC_KIND_OBJECT_FIELD = 3;
}
```

该接口适合：

- 查某个标的最近 N 条 K 线。
- 查某个标的一段时间内的 close、volume、ma20、ma60。
- 查写后立即可见的在线数据。

## QueryFrame 草案

`QueryFrame` 用于多标的、多因子、多条件的组合查询。

```protobuf
message QueryFrameReq {
  int64 project_id = 1;
  string universe = 2;        // 全市场、沪深300、自选池等
  string freq = 3;

  QueryTimeMode time_mode = 4;
  int64 as_of_ts_ms = 5;      // 横截面查询
  int64 start_ts_ms = 6;      // 区间查询
  int64 end_ts_ms = 7;

  repeated MetricRef select_metrics = 8;
  Expr filter = 9;
  repeated OrderBy order_by = 10;

  Page page = 11;
  ProjectionPolicy projection_policy = 12;
}

enum QueryTimeMode {
  QUERY_TIME_MODE_UNSPECIFIED = 0;
  QUERY_TIME_MODE_SNAPSHOT = 1;
  QUERY_TIME_MODE_RANGE = 2;
}

enum ProjectionPolicy {
  PROJECTION_POLICY_STRICT = 0;
  PROJECTION_POLICY_AUTO_FALLBACK = 1;
}
```

`ProjectionPolicy` 控制字段缺失时的行为：

- `STRICT`：宽表没有所需字段时返回 `FACTOR_NOT_MATERIALIZED`。
- `AUTO_FALLBACK`：从长表 join 或 pivot 查询，速度较慢，但可用。

条件表达式建议使用结构化 AST，不直接开放 SQL 字符串：

```protobuf
message Expr {
  oneof node {
    BinaryExpr binary = 1;
    LogicExpr logic = 2;
    ValueExpr value = 3;
    MetricExpr metric = 4;
  }
}

message BinaryExpr {
  string op = 1; // >, >=, <, <=, =, !=
  Expr left = 2;
  Expr right = 3;
}

message LogicExpr {
  string op = 1; // AND, OR, NOT
  repeated Expr children = 2;
}
```

示例查询：

```text
某日全市场：
close > ma20
ma20 > ma60
rsi14 < 30
按 volume 倒序取前 100
```

执行器根据元数据选择 active 投影表。如果 active 宽表无法满足条件，则按 `ProjectionPolicy` 决定是否回退长表。

## 协议收窄原则

短期内应明确以下约束：

- RocksDB 只承诺在线时序范围查询和点查，不承诺复杂 search。
- DuckDB 负责多字段过滤、排序、横截面和分析查询。
- Bleve 负责文本检索。
- `SearchData` 不再承载所有查询语义，应逐步拆成更明确的接口。
- 多字段查询必须能映射到同一个查询执行器；跨执行器查询需要显式 planner。

建议增加错误码：

```text
ROUTE_CROSS_DEVICE_UNSUPPORTED
FACTOR_NOT_MATERIALIZED
PROJECTION_NOT_READY
QUERY_FALLBACK_DISABLED
UNSUPPORTED_QUERY_SHAPE
```

## 冷数据归档

CSV 可用于人工可读备份，但长期冷存储建议优先使用 Parquet：

- Parquet 保留类型信息。
- Parquet 支持列式读取。
- DuckDB 可以直接查询 Parquet。
- Parquet 更适合历史行情和因子归档。

推荐归档路径：

```text
archive/
  dataset=bar/
    freq=1d/
      date=2026-06-13/
        part-000.parquet
  dataset=factor/
    freq=1d/
      factor_instance_id=10020/
        date=2026-06-13/
          part-000.parquet
```

## 落地顺序

建议按以下顺序推进：

1. 在元数据中增加 dataset kind、factor definition、factor instance 和 projection metadata。
2. 在协议中新增 `ScanTimeSeries` 和 `QueryFrame`，并收窄 `SearchData` 的职责。
3. 将 RocksDB 明确为在线事实层，统一 K 线和因子的 key 设计。
4. 在 DuckDB 中建立 `bar_values` 和 `factor_values_long`。
5. 增加宽表投影构建器，采用新版本表构建和 active version 切换。
6. 增加写入模式：`SYNC_COMMIT` 和 `ASYNC_INGEST`。
7. 增加 engine conformance tests，验证 RocksDB、DuckDB 长表和宽表投影的查询语义。

## 当前结论

推荐架构是：

```text
API
  -> RocksDB 在线事实层
  -> MQ / WAL 变更日志
       -> DuckDB 长表
       -> DuckDB 宽表投影
       -> Bleve 文本索引
       -> Parquet / CSV 冷归档
```

动态因子不要依赖动态字段表达，而要通过 `factor_instance_id` 表达。DuckDB 宽表只服务热门组合查询，采用新版本表构建和元数据切换，避免在线原地改表影响用户查询。
