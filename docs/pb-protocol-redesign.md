# moox-storage PB 协议设计

本文记录 storage 在新架构下的 PB 协议边界、命名和接口语义。当前项目未上线，协议以清晰和可演进为优先，不考虑旧接口兼容。

## 设计目标

- 用户写入和读取事实数据时只感知 `DataSet`。
- 用户做组合分析查询时只感知已登记的 `View`。
- 文本检索按 `DataSet` 维度执行，由元数据控制哪些列进入 Bleve。
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
adapter.proto
message.proto
```

职责如下：

| 文件 | 职责 |
| --- | --- |
| `common.proto` | 通用类型、错误码、分页、时间范围、TypedValue、AuthInfo、WriteMode |
| `metadata.proto` | Space、View、DataSet、Subject、Field、Factor、Device、Route 等元数据 |
| `data.proto` | 用户侧事实数据写入和读取 |
| `query.proto` | 用户侧组合查询和文本检索 |
| `adapter.proto` | 接入层到 adapter 服务的内部执行协议 |
| `message.proto` | 主存变更事件，供 DuckDB、Bleve、Parquet 异步派生使用 |

## 核心命名

| 概念 | 含义 |
| --- | --- |
| `Space` | 用户可见 View 的集合，只承载权限和视图选择 |
| `View` | 面向查询的宽表定义和查询入口 |
| `DataSet` | 可写的事实数据集 |
| `Subject` | 数据源下的数据对象，例如交易标的、榜单、新闻源或账户 |
| `Field` | 全局普通字段字典 |
| `Factor` | 全局、已参数化的因子结果定义 |
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

`ColumnSourceType` 统一表达字段、因子、表达式和系统列：

```text
FIELD
FACTOR
EXPRESSION
SYSTEM
```

`FIELD` 表示普通字段。

`FACTOR` 表示已参数化后的因子结果，例如 `ma20_close`。

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
slice
data_time
row_id
columns
attrs
```

`DataSlice` 结构：

```text
dataset_id
subject_id
freq
dimensions
```

`dimensions` 是参与逻辑定位的低基数业务维度，不是普通查询过滤条件。只有当某个值决定“是否为同一条事实序列或事实切片”时才放在这里，例如 `adjust_type=qfq`、`report_period=2025Q4`、`ranking_type=amount_top`。如果一个值只是展示、筛选或排序字段，应放在 `DataRow.columns`。

语义约束：

- `dataset_id` 必填。
- 时序数据应填写 `subject_id`、`freq` 和 `data_time`。
- 对象型或表格型数据可以使用 `row_id` 表示逻辑行。
- `columns.column_name` 必须登记在 `DataSetColumn` 中。
- 写入成功只返回 `ret_info`。

写入请求不包含额外写入选项对象。写入响应不返回行变更、不返回旧值、不返回写入数量。

### ReadRows

`ReadRows` 按 DataSet 维度读取事实数据。请求结构：

```text
auth_info
slice
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

### SearchRows

`SearchRows` 按 DataSet 维度执行全文和结构化搜索。请求结构：

```text
auth_info
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

## AdapterService

`AdapterService` 是内部执行接口，不对普通用户暴露。

```text
WriteDeviceRows
ReadDeviceRows
```

`DeviceRef` 结构：

```text
entity_id
device_id
engine
dataset_id
device_table
```

`device_table` 表示设备内部表、索引或键空间名称。接入层根据路由和元数据生成它，用户请求中不携带它。

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
t_datasets
t_dataset_subjects
t_dataset_columns
t_views
t_view_columns
t_storage_routes
t_storage_devices
```

其中 `t_dataset_columns.c_text_indexed` 是 Bleve 同步开关。

View 的 DuckDB 宽表由后台任务根据 `t_views.c_query_window` 回扫 Pebble 主存并异步构建。新增列或因子时，不原地修改当前宽表，而是新建宽表后切换 `c_active_table`。
