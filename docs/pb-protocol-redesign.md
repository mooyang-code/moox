# moox-storage PB 协议设计

本文记录 storage 在新架构下的 PB 协议边界、命名和接口语义。当前项目未上线，协议以清晰和可演进为优先，不考虑旧接口兼容。

协议中的概念边界和设计取舍见 `docs/storage-concepts-and-design-intent.md`。本文只描述接口如何表达这些概念。

## 设计目标

- 用户写入和读取事实数据时只感知 `DataSet`。
- 用户做组合分析查询时只感知已登记的 `View`。
- 文本检索按 `DataSet` 维度执行，由元数据控制哪些列进入 Bleve。
- 所有用户侧和业务侧数据访问都先进入 Access Service；PrimaryStore、Search、View 和 Archive 都是内部服务。
- 接入层和底层存储设备解耦，底层统一称为 `Device`。
- 不提供用户删除数据的能力。
- 写请求保持简单，不返回行变更、旧值或写入统计。
- 查询请求只表达“要什么数据”，不让调用方控制底层执行策略。

## 文件划分

storage 侧协议保留 6 个文件：

```text
common.proto
metadata.proto
data.proto
query.proto
primary.proto
message.proto
```

职责如下：

| 文件 | 职责 |
| --- | --- |
| `common.proto` | 通用类型、错误码、分页、时间范围、TypedValue、AuthInfo、WriteMode |
| `metadata.proto` | Space、View、DataSet、Subject、Field、Factor、Device、Route 等元数据 |
| `data.proto` | 用户侧事实数据写入和读取 |
| `query.proto` | 用户侧组合查询和文本检索 |
| `primary.proto` | Access 到 PrimaryStore Service 的内部执行协议 |
| `message.proto` | 主存变更事件，供 DuckDB、Bleve、Parquet 异步派生使用 |

## 核心命名

| 概念 | 含义 |
| --- | --- |
| `Space` | 业务命名空间和用户可见 View 的集合；本文“全局”均指 Space 内全局 |
| `View` | 面向查询的物化结果定义和查询入口 |
| `DataSource` | Space 内数据来源，例如交易所、API、文件导入或内部计算 |
| `DataSet` | Space 内可写事实数据集，并且只绑定一个 DataSource |
| `Subject` | Space 内业务对象，例如交易标的、榜单、新闻源或账户 |
| `SubjectSymbol` | Subject 在某个 DataSource 下的外部代码映射 |
| `Field` | Space 内普通字段字典 |
| `Factor` | Space 内、已参数化的因子结果定义 |
| `DataSetColumn` | DataSet 下允许写入的列，可来自 Field、Factor 或系统列 |
| `ViewColumn` | View 对用户暴露的查询列 |
| `Device` | 底层具体存储组件，例如 Pebble、DuckDB、Bleve、Parquet |

## 通用协议

### 时间字段

协议字段使用 `start_time`、`end_time`、`snapshot_time`、`data_time` 这类中性命名，不在字段名中带 `_ms`、`_ns` 等单位。具体格式由项目统一约定。

### 写入模式

`WriteMode` 只保留：

```text
UNSPECIFIED
UPSERT
APPEND
OVERWRITE
```

不提供 `DELETE`。用户侧不开放删除数据能力。

### 列来源

`ColumnOriginType` 统一表达字段、因子、DataSetColumn、表达式和系统列：

```text
FIELD
FACTOR
DATASET_COLUMN
EXPRESSION
SYSTEM
```

`FIELD` 表示 Space 内普通字段。

`FACTOR` 表示 Space 内已参数化后的因子结果，例如 `ma20_close`。

`DATASET_COLUMN` 表示 ViewColumn 来自某个 DataSetColumn。

`EXPRESSION` 表示 View 内部登记的表达式列，不由临时查询随意传入执行。

`SYSTEM` 表示系统列，例如 `subject_id`、`data_time`、`freq`。

## DataService

`DataService` 是用户侧事实数据读写服务。

```text
WriteRows
ReadRows
```

### WriteRows

`WriteRows` 写入事实数据行。请求结构：

```text
auth_info
write_mode
rows
```

`DataRow` 结构：

```text
key
columns
attributes
```

`DataKey` 结构：

```text
scope
data_time
row_id
```

`DataScope` 结构：

```text
space_id
dataset_id
subject_id
freq
dimensions
```

`DataScope` 定位一组事实数据。`DataKey` 在 `DataScope` 上增加 `data_time` 和 `row_id`，定位一条事实行。写入时，调用方是在某个 `DataKey` 下写入一组列值。

`dimensions` 是参与逻辑定位的低基数业务维度，不是普通查询过滤条件。只有当某个值决定“是否为同一条事实序列或事实范围”时才放在这里，例如 `adjust_type=qfq`、`report_period=2025Q4`、`ranking_type=amount_top`。如果一个值只是展示、筛选或排序字段，应放在 `DataRow.columns`。

语义约束：

- `dataset_id` 必填。
- `space_id` 必填；DataSet、Subject、Field 和 Factor 的唯一性均限定在 Space 内。
- 时序数据应填写 `scope.subject_id`、`scope.freq` 和 `key.data_time`。
- 对象型或表格型数据可以使用 `key.row_id` 表示逻辑行。
- 事件或 tick 数据可以同时使用 `key.data_time` 和 `key.row_id`，避免同一时间多行冲突。
- `columns.column_name` 必须登记在 `DataSetColumn` 中。
- 写入成功只返回 `ret_info`。

写入请求不包含额外写入选项对象。写入响应不返回行变更、不返回旧值、不返回写入数量。

### ReadRows

`ReadRows` 按 DataSet 维度读取事实数据。请求结构：

```text
auth_info
scope
read_mode
time_range
snapshot_time
row_ids
column_names
page
```

读取模式：

```text
RANGE          // 按时间区间读取
POINT          // 按 row_id 点查
LATEST_BEFORE  // 读取某个截面时间之前的最新行
```

范围查询使用 `scope + time_range`。例如读取 Binance `APT-USDT` 现货 1m K 线最近一天数据：

```text
scope:
  space_id: crypto
  dataset_id: binance_spot_kline
  subject_id: APT-USDT
  freq: 1m
time_range:
  start_time: 2026-06-15T00:00:00+08:00
  end_time: 2026-06-16T00:00:00+08:00
column_names:
  open
  high
  low
  close
  volume
```

这里 `scope` 表示“哪一组数据”，`time_range` 表示“这组数据里的哪一段时间”，`column_names` 表示“要哪些列”。

响应结构：

```text
ret_info
rows
page_result
```

## QueryService

`QueryService` 是用户侧查询服务。

```text
QueryView
SearchRows
```

### QueryView

`QueryView` 查询已登记并异步构建的 View。请求结构：

```text
auth_info
space_id
view_id
subject_ids
query_time
column_names
filters
sorts
page
```

语义约束：

- `view_id` 必填。
- 用户只能查询已登记的 View。
- 临时不存在的字段组合直接返回 `VIEW_NOT_FOUND`。
- 调用方不控制是否 fallback、不控制底层表、不控制执行引擎。
- 表达式列属于 View 元数据和后台构建逻辑，不出现在 `QueryViewColumn` 响应中。

View 创建时必须配置 `primary_dataset_id`。`primary_dataset_id` 决定 View 的 Subject 范围，系统使用主 DataSet 绑定的 Subject 集合作为 View 的行域。协议不提供 `subject_scope_policy`，也不让调用方在创建 View 时逐个选择 Subject。

当 View 关联多个 DataSet 时，其他 DataSet 只提供列。构建宽表时，系统以主 DataSet 的 Subject 集合为准，再按粒度键关联其他 DataSet 的列。

### SearchRows

`SearchRows` 按 DataSet 维度执行全文和结构化搜索。请求结构：

```text
auth_info
space_id
dataset_id
text_query
subject_ids
time_range
filters
sorts
column_names
page
```

语义约束：

- `SearchRows` 查询 Search Service 中的集中式 Bleve 派生索引。
- Search Service 汇聚所有 PrimaryStore 节点的主存变更，查询时不走 PrimaryRoute，也不 fan-out 到 Pebble 分片。
- `text_query` 非空时，使用 Bleve 全文索引召回匹配行。
- `filters` 非空时，做结构化过滤。
- `text_query + filters` 同时存在时，先全文召回，再结构化过滤。
- `text_query` 为空但 `filters` 非空时，作为 DataSet 维度结构化搜索。
- `column_names` 控制返回列，不影响过滤列解析。

Bleve 同步策略由元数据控制：

```text
t_dataset_columns.c_text_indexed = 1
```

只有开启该标记的列会进入文本索引。这样可以避免把数值字段、内部字段和无关扩展字段写入 Bleve。

## PrimaryStore 内部执行协议

`PrimaryStoreService` 是在线事实主存的内部执行接口，不对普通用户暴露。

```text
WritePrimaryRows
ReadPrimaryRows
```

`PrimaryTarget` 结构：

```text
space_id
node_id
device_id
engine
dataset_id
device_table
endpoint
```

`PrimaryTarget` 表示 Access 已完成路由后的主存执行目标。`device_table` 表示设备内部表、索引或键空间名称。`endpoint` 来自目标 PrimaryStore 节点，用于让内部 client 连接正确节点；为空时使用默认 PrimaryStore 服务名。Access 根据 PrimaryRoute 和元数据生成它，用户请求中不携带它。

内部接口不提供创建、删除或解释路由的用户式 RPC。建表、视图构建、归档和索引刷新应由控制面任务或后台任务驱动。

## DataSet 与 View 的边界

读写事实数据使用 DataSet，组合查询使用 View。两者不统一是刻意设计。

```text
写入事实：DataSet
读取事实：DataSet
组合分析：View
全文和结构化搜索：DataSet
```

原因：

- DataSet 是事实契约，保证写入稳定。
- View 是查询产品，允许异步构建宽表和按用户场景裁剪列。
- 文本检索天然属于某个事实数据集，不需要额外创建 View。
- 不存在的组合查询不应临时消耗大量 DuckDB pivot/join 资源。

## 元数据联动

协议需要依赖以下元数据：

```text
t_data_sources
t_subjects
t_subject_symbols
t_datasets
t_dataset_subjects
t_dataset_columns
t_views
t_view_columns
t_storage_routes
t_storage_devices
```

其中 `t_dataset_columns.c_text_indexed` 是 Bleve 同步开关。

View 的物化查询结果由后台任务根据 `t_views.c_query_window` 回扫 Pebble 主存并异步构建。新增列或因子时，不原地修改当前结果，而是新建物化结果后切换 `c_active_result`。
