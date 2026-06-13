# moox 与 xData PB 协议重设计草案

本文记录 moox 与 xData 在新概念体系下的 PB 协议设计方向。本文只描述协议边界、命名和语义，不涉及具体实现。

## 设计目标

新协议要服务个人量化金融数据系统，支持多市场、多交易场所、多数据源和动态因子。

核心目标：

- 使用 `Workspace` 替代旧的 `Project`。
- 保留 `DataSet` 作为数据集合概念。
- 使用 `Instrument` 表达金融交易标的。
- 废弃 `object_id` 作为泛化对象 ID。
- 使用 `DataView` 表达查询视图。
- 使用 `DataViewColumn` 表达查询视图的输出列。
- 使用 `DataRef` 表达一次数据读写中的逻辑定位。
- moox 负责控制面和编排面，xData 负责元数据事实面和数据执行面。

## 系统边界

moox 是控制面和编排面：

- 管理 Workspace。
- 管理采集规则。
- 管理节点和任务调度。
- 管理 DataSet、Field、Factor、DataView 和 StorageRoute 的配置入口。
- 负责把管理台操作转成 xData 元数据变更。

xData 是数据事实面和执行面：

- 保存元数据事实。
- 执行在线写入和查询。
- 管理 Pebble、DuckDB、Bleve、Parquet 和 CSV 等存储执行器。
- 根据元数据路由读写请求。
- 执行 DataView 查询和文本查询。

## PB 文件划分

xData 侧协议不宜拆得过细。建议保留 5 个核心文件：

```text
common.proto
metadata.proto
data.proto
query.proto
adapter.proto
```

职责如下：

| 文件 | 职责 |
| --- | --- |
| `common.proto` | 通用类型、错误码、分页、时间范围、TypedValue、AuthInfo、WriteMode |
| `metadata.proto` | Workspace、Market、Exchange、Instrument、DataSet、Field、Factor、DataView、StorageRoute |
| `data.proto` | 在线数据读写，例如时间序列、因子值、普通记录、最新快照 |
| `query.proto` | 分析查询、DataView 查询、文本检索、查询解释 |
| `adapter.proto` | xData 内部执行协议，不对 moox 和普通用户暴露 |

moox 侧协议建议保留控制面聚合接口：

```text
control.proto
collector.proto
node.proto
task.proto
```

`control.proto` 的包名可以是 `trpc.moox.control`，但文件名和 service 不再使用 `moox_control` 这种重复命名。

## 命名决策

| 旧概念 | 新概念 | 说明 |
| --- | --- | --- |
| Project | Workspace | 用户或业务隔离空间 |
| Dataset | DataSet | 保留数据集合概念 |
| DataType | DataKind | 数据形态分类 |
| DataCategory | DataDomain | 业务语义分类 |
| Object | 废弃 | 金融主路径使用 Instrument |
| object_id | 废弃 | 不再作为泛化对象 ID |
| DataKey / DataAddress | DataRef | 一次读写中的逻辑数据引用 |
| Projection | DataView | 查询视图或查询加速视图 |
| DataViewMetric | DataViewColumn | 查询视图输出列 |
| source_type | column_origin | DataViewColumn 的来源 |
| partition_key | dimension_values | 业务维度值 |
| GetLatestValues | GetLatestSnapshot | 获取最新状态快照 |
| as_of_time | snapshot_time | 截面查询时间 |

## Workspace

`Workspace` 是用户或业务隔离空间，不表达市场或交易场所。

建议核心字段：

```text
workspace_id
name
display_name
description
owner
status
created_at
updated_at
```

## Market、Exchange、Instrument

`Market` 表示市场大类，例如 `US_STOCK`、`HK_STOCK`、`CN_STOCK`、`CRYPTO`。

`Exchange` 表示交易场所，例如 `NASDAQ`、`NYSE`、`HKEX`、`SSE`、`SZSE`、`BINANCE`、`OKX`。

`Instrument` 表示金融交易标的。协议中不再使用 `object_id`，金融数据统一通过 `instrument_id` 定位。

建议模型：

```text
Instrument:
  instrument_id
  internal_symbol
  exchange_id
  market
  instrument_type
  base_asset
  quote_asset
  currency
  status
```

`internal_symbol` 是系统内部标准化后的标的代码。

示例：

```text
BTC-USDT
00700.HK
AAPL
```

外部数据源中的代码由 `InstrumentAlias` 管理：

```text
InstrumentAlias:
  instrument_id
  data_source
  exchange_id
  external_symbol
```

示例：

| instrument | data_source | external_symbol |
| --- | --- | --- |
| BTC-USDT | BINANCE | BTCUSDT |
| BTC-USDT | OKX | BTC-USDT |
| 00700.HK | Yahoo | 0700.HK |

## DataSet、DataKind、DataDomain

`DataSet` 表示一组可读写的数据集合。它不等同于数据形态。

建议核心字段：

```text
dataset_id
workspace_id
name
display_name
data_kind
data_domain
market_scope
exchange_scope
instrument_scope
default_freqs
schema_version
status
```

`DataKind` 表示数据形态：

```text
OBJECT
TIME_SERIES
SNAPSHOT
EVENT
DOCUMENT
TABLE
```

`DataDomain` 表示业务语义：

```text
MARKET_BAR
MARKET_TICK
ORDER_BOOK
TRADE
SYMBOL_PROFILE
COMPANY_PROFILE
NEWS
ANNOUNCEMENT
FACTOR_VALUE
RANKING_LIST
FINANCIAL_REPORT
```

示例：

| 数据 | DataKind | DataDomain |
| --- | --- | --- |
| 公司信息 | OBJECT | COMPANY_PROFILE |
| 日 K | TIME_SERIES | MARKET_BAR |
| tick | TIME_SERIES | MARKET_TICK |
| 订单簿快照 | SNAPSHOT | ORDER_BOOK |
| 公告正文 | DOCUMENT | ANNOUNCEMENT |
| MA20 因子序列 | TIME_SERIES | FACTOR_VALUE |

## Field 与 Factor

Field 和 Factor 不应完全统一管理。

`Field` 管事实数据字段和用户声明字段：

```text
open
close
volume
industry
market_cap
announcement_title
```

`FactorDef` 和 `FactorInstance` 管因子算法和参数化结果：

```text
MA(window=20)
MA(window=60)
RSI(window=14)
```

不要让每个 `FactorInstance` 自动变成全局 `Field`。否则字段管理会被参数化因子污染，字段数量也会快速膨胀。

建议采用：

```text
FieldService:
  管原始字段、对象字段、业务字段。

FactorService:
  管因子定义、因子实例、因子版本。

DataViewColumn:
  在查询层统一 Field、FactorInstance、Expression 和 SystemColumn。
```

## DataView

`DataView` 是查询视图，不是原始 DataSet。它从一个或多个 DataSet 派生，用于分析查询、组合查询或查询加速。

示例：

```text
日 K + 热门因子横截面视图
分钟 K + MA/RSI 因子视图
公司资料 + 行业标签视图
公告文本搜索视图
```

建议模型：

```text
DataViewDef:
  data_view_id
  workspace_id
  name
  source_datasets
  grain
  query_config
  status

DataViewVersion:
  data_view_id
  version
  physical_name
  storage_device_id
  status
  built_at
```

`query_config` 是服务端查询策略配置，不由调用方在请求里控制。

示例：

```text
allow_fallback
max_fallback_rows
max_staleness
preferred_storage
```

## DataViewColumn

`DataViewColumn` 表达 DataView 的输出列。它替代 `DataViewMetric`。

建议字段：

```text
data_view_column_id
column_key
display_name
column_origin
field_id
factor_instance_id
expression
value_type
```

`column_origin` 表示列来源：

```text
COLUMN_ORIGIN_FIELD
COLUMN_ORIGIN_FACTOR
COLUMN_ORIGIN_EXPRESSION
COLUMN_ORIGIN_SYSTEM
```

含义如下：

| column_origin | 含义 | 示例 |
| --- | --- | --- |
| FIELD | 来自 Field 管理的普通字段 | close、volume、industry |
| FACTOR | 来自 FactorInstance | MA20、RSI14 |
| EXPRESSION | DataView 内部派生表达式 | close / ma20 - 1 |
| SYSTEM | 系统列 | instrument_id、exchange_id、timestamp、freq、ingest_time |

## DataRef

`DataRef` 表示一次数据读写中的逻辑数据引用。它替代 `DataKey` 和 `DataAddress`。

建议字段：

```text
workspace_id
dataset_id
instrument_id
record_key
exchange_id
freq
timestamp
dimension_values
```

典型定位方式：

```text
时间序列:
  workspace_id + dataset_id + instrument_id + freq + timestamp

标的资料:
  workspace_id + dataset_id + instrument_id

普通记录:
  workspace_id + dataset_id + record_key

榜单或财报:
  workspace_id + dataset_id + dimension_values
```

## dimension_values

`dimension_values` 表示业务维度值。它替代 `partition_key`。

它适合描述不完全由 `instrument_id` 定位的数据切片：

```text
trade_date = 2026-06-13
ranking_type = amount_top
report_period = 2025Q4
adjust_type = qfq
news_category = macro
```

建议不要把它设计成完全自由的 map。DataSet 应先声明允许哪些维度。

```text
DimensionDef:
  key
  value_type
  required
  indexable
  allowed_values
```

`dimension_values` 是数据定位的一部分，不等同于普通字段，也不一定等同于物理分区。

## DataService

`DataService` 负责在线数据读写。

建议接口：

```text
UpsertRecords
QueryRecords
SetTimeSeries
ScanTimeSeries
SetFactorValues
ScanFactorValues
GetLatestSnapshot
```

说明：

- `UpsertRecords` 写普通结构化记录，例如公司资料、榜单行、公告摘要。
- `QueryRecords` 查普通结构化记录。
- `SetTimeSeries` 写 K 线、tick 等时间序列事实数据。
- `ScanTimeSeries` 按时间范围扫描时间序列。
- `SetFactorValues` 写因子结果。
- `ScanFactorValues` 按时间范围扫描因子结果。
- `GetLatestSnapshot` 获取一批标的的最新状态。

`GetLatestSnapshot` 的语义是“每个 instrument 取最新一份状态”。如果要取最近 N 条，应使用 `ScanTimeSeries`。

## QueryService

`QueryService` 负责分析查询、DataView 查询和文本查询。

建议接口：

```text
QueryFrame
TextSearch
ExplainQuery
```

`QueryFrame` 用于 K 线、字段和因子的组合查询。

建议请求结构：

```text
QueryFrameReq:
  workspace_id
  dataset_id
  instrument_ids
  freq
  query_time
  select_columns
  filter
  order_by
  page
```

`instrument_ids` 由上层应用传入。标的池、选股池、指数成分等概念通常在应用层处理，不放入 xData 第一版协议。

时间条件建议使用结构化 `QueryTime`：

```text
QueryTime:
  snapshot_time
  time_range

TimeRange:
  start_time
  end_time
```

`snapshot_time` 用于截面查询：

```text
某个时间点，全市场 ma20 > ma60 的标的
```

`time_range` 用于区间查询：

```text
某个时间范围内，某批标的的 close、ma20、rsi14
```

时间类型建议使用 `google.protobuf.Timestamp`，不在字段名里写 `_ms`、`_ns` 等单位。

## 查询执行策略

调用方不应控制是否使用 DataView、是否 fallback、是否走长表。执行策略属于服务端配置。

推荐放在：

```text
DataViewDef.query_config
```

如果调用方需要了解执行方式，使用：

```text
ExplainQuery
```

这样请求只表达“我要什么数据”，不表达“系统应该怎么查”。

## ControlService

moox 侧控制面接口建议命名为 `ControlService`，文件为 `control.proto`。

建议职责：

```text
CreateWorkspaceWithDefaults
ConfigureDataSet
ConfigureFields
ConfigureStorageRoutes
ConfigureCollectorBinding
PublishMetadataChange
```

`ControlService` 是管理台聚合接口。它可以调用 xData 的 MetadataService，但不把 xData 内部接口直接暴露给前端。

## CollectorDataSetBinding

采集任务和 DataSet 的关系应配置化，不应硬编码 dataset_id。

建议模型：

```text
CollectorDataSetBinding:
  workspace_id
  data_source
  collector_data_type
  market
  exchange
  instrument_type
  dataset_id
  internal_symbol_rule
  external_symbol_rule
  freq_rule
```

示例：

```text
data_source = BINANCE
collector_data_type = KLINE
market = CRYPTO
exchange = BINANCE
instrument_type = SPOT
dataset_id = binance_spot_bar
```

这样采集器只需要根据绑定关系写入目标 DataSet，不需要在代码里写死 `SPOT -> 101`、`SWAP -> 100`。

## 错误码建议

建议新增面向新协议的错误码：

```text
WORKSPACE_NOT_FOUND
DATASET_NOT_FOUND
INSTRUMENT_NOT_FOUND
FIELD_NOT_FOUND
FACTOR_INSTANCE_NOT_FOUND
DATA_VIEW_NOT_READY
DATA_VIEW_COLUMN_NOT_FOUND
QUERY_SHAPE_UNSUPPORTED
ROUTE_NOT_FOUND
ROUTE_CROSS_DEVICE_UNSUPPORTED
ENGINE_CAPABILITY_UNSUPPORTED
DIMENSION_VALUE_INVALID
```

## 待确认问题

以下问题还需要在真正改 proto 前确认：

- `Record` 是否需要独立 service，还是放在 `DataService.UpsertRecords / QueryRecords` 中即可。
- `DataView` 是否需要支持文本索引视图，还是文本索引单独由 `TextSearch` 管理。
- `dimension_values` 是否需要支持多值，例如 `market=["US_STOCK","HK_STOCK"]`。
- `GetLatestSnapshot` 是否需要同时支持时间序列字段和因子值。
- `QueryFrame` 第一版是否支持表达式列，还是只支持 Field 和 Factor。

## 当前结论

当前推荐方案：

```text
xData:
  common.proto
  metadata.proto
  data.proto
  query.proto
  adapter.proto

moox:
  control.proto
  collector.proto
  node.proto
  task.proto
```

核心命名：

```text
Workspace
DataSet
Instrument
InstrumentAlias
DataRef
DataView
DataViewColumn
dimension_values
snapshot_time
GetLatestSnapshot
```

核心原则：

- Field 和 Factor 分开管理。
- DataViewColumn 在查询层统一 Field、Factor、Expression 和 SystemColumn。
- 调用方不控制 DataView 执行策略。
- 标的池由上层应用处理，xData 只接收明确的 `instrument_ids`。
- 时间字段使用 `google.protobuf.Timestamp`，不在字段名中携带单位。
