# Time-Series `series_tag` 统一设计

**日期：** 2026-07-29
**状态：** 已确认，待实施
**适用范围：** Storage、View、Archive、Factor，以及所有时序读写方

## 1. 背景与目标

同一 Dataset、Subject、频率和时间点，可能存在多条需要独立保存的序列。例如：

- 同一交易对在 Binance 和 OKX 的行情；
- 同一主机在多个磁盘或网卡上的指标；
- 跨交易所价差因子的合成结果。

原模型使用 `map<string, string> dimensions` 区分这些行。它能表达多维组合，但会把
Map 规范化、查询语义、索引和 Python 分组复杂度带入整个链路。个人量化场景中，
实际需求大多只是给一条序列附加一个稳定身份，不需要通用多维分析引擎。

本设计将整个项目统一为一个可选的不透明字符串 `series_tag`：

```text
venue:binance
venue:okx
device:sdb
venue_pair:binance-okx
```

目标是让同一 Dataset 可以安全存放多条序列，同时保持存储、查询、归档和因子计算
契约简单、明确、可测试。

## 2. 核心决策

### 2.1 唯一模型

TimeSeries 行身份为：

```text
space_id
+ dataset_id
+ subject_id
+ freq
+ data_time
+ series_tag
```

`series_tag` 是 UTF-8 标量字符串：

- 空字符串表示默认、未标记序列；
- 非空字符串区分同一时间点的不同序列；
- 推荐使用 `<namespace>:<value>`，例如 `venue:binance`；
- Storage、View 和 Archive 不解析冒号，也不知道 `venue`、`device` 的含义；
- `series_tag` 的完整字符串才是身份，不存在独立的 tag name/value；
- Dataset 不增加 `series_tag_name`、允许值列表或 tag registry。

### 2.2 明确删除

新项目不保留历史兼容。以下概念和持久化结构全部删除：

- `dimensions`、`dimension`；
- `dimensions_json`；
- `dimension_name`、`dimension_value`；
- `dimension_tags` 或任意 tag 数组；
- Map 规范化与 JSON 编解码；
- 新旧协议双读、双写或数据迁移。

旧 Pebble、DuckDB、Archive Parquet 和依赖旧 Factor SQLite schema 的本地数据直接
清理后重建。服务发现旧 schema 时应明确拒绝启动，不能静默把旧行当作空 tag。

### 2.3 校验边界

框架只校验字符串形态，不校验业务含义：

- 空字符串合法；
- 必须是有效 UTF-8；
- 最长 128 bytes；
- 不允许 NUL、ASCII 控制字符；
- 不允许首尾空白；
- 大小写敏感，`venue:OKX` 与 `venue:okx` 是两条不同序列。

拼写错误会创建新的序列，这是选择不透明标量换取简洁性的明确代价。业务写入方负责
集中定义常量和验证自身使用的 tag。

Dataset 仍只绑定一个 `data_source_id`。当一个 Dataset 接收 Binance、OKX 等多个
物理来源时，它绑定一个逻辑聚合 DataSource（例如 `crypto_market`），物理来源由
`series_tag` 和必要的普通 Field 记录。不要为 tag 改成 Dataset 多 DataSource
绑定，也不要把凭据或 Provider 配置塞入 tag。

## 3. 写入与事件契约

Storage 私有 Proto 和公共 Dataset 事件统一使用：

```proto
message TimeSeriesRowKey {
  string subject_id = 1;
  string freq = 2;
  string data_time = 3;
  string series_tag = 4;
}
```

所有 TimeSeries 写入、读取结果和 `DatasetRowsUpserted` 事件必须原样携带
`series_tag`。Event mapper 只复制字符串，不做解析或格式转换。

这是 wire-breaking 变更，`storage.dataset.rows.upserted` 升级到 `@2`。旧 v1
Stream/Consumer/Outbox 状态必须清空，不能让 v1 payload 被新消费者解释。

DataNode Pebble 物理键为：

```text
value_kind
| time_series
| space_id
| dataset_id
| bucket_start
| subject_id
| freq
| data_time
| series_tag
| field_id
```

同一逻辑行的 Field 与 Attribute 共用完整 RowKey。空 tag 仍编码为空 tuple component，
不能省略键位，避免新旧键形状或不同字段类型产生歧义。

## 4. 查询契约

精确行身份和范围筛选必须分开表达，不能再让空值同时承担“空 tag”和“全部 tag”：

```proto
message TimeSeriesKey {
  string space_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string freq = 4;
  string data_time = 5;
  string series_tag = 6;
}

message TimeSeriesSelector {
  string space_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string freq = 4;
  optional string series_tag = 5;
}
```

语义如下：

| 场景 | 表达 | 语义 |
| --- | --- | --- |
| 精确读一行 | `TimeSeriesKey.series_tag=""` | 精确读取默认序列 |
| 精确读一行 | `TimeSeriesKey.series_tag="venue:okx"` | 精确读取 OKX 序列 |
| 范围读 | selector 未设置 `series_tag` | 返回所有 tag |
| 范围读 | selector 设置 `series_tag=""` | 只返回默认序列 |
| 范围读 | selector 设置非空值 | 只返回该完整 tag |

`ReadTimeSeriesRows` 与 `QueryTimeSeriesRows` 的范围条件使用 selector；点查使用
`ReadFields(RowKey)`，查询结果使用完整 `TimeSeriesKey`。调用方不能通过传空 Map、
空对象或特殊通配字符串表达“全部 tag”。

所有时序结果稳定排序为：

```text
subject_id ASC, freq ASC, data_time ASC, series_tag ASC
```

倒序查询按同一组合键整体倒序。公开查询首版继续使用稳定全序的 offset page，不承诺
并发写入下的跨请求快照；当前未实现的 page token 字段直接删除。View Backfill 等
内部 keyset cursor 必须包含 `series_tag`，否则同一时间点有多个 tag 时会漏行。

在 View 中查询某个 tag 直接使用普通字符串等值条件 `series_tag = ?`，不再解析
JSON。首版不增加 tag 二级索引；个人数据量下先依赖 DuckDB 过滤，只有 profiling
证明扫描成为瓶颈时再加索引。

## 5. View 设计

DuckDB TimeSeries View 使用系统列：

```sql
series_tag VARCHAR NOT NULL
```

主键为：

```sql
PRIMARY KEY (subject_id, freq, data_time, series_tag)
```

实时写、A/B 双写、Backfill、缺失列补写、点查、范围筛选和分页均保留完整 tag。
`series_tag` 是保留系统列，不能被 Dataset Field 或 ViewColumn 重名覆盖。

View 查询响应继续返回 `served_indexed_from`、`served_indexed_to` 和 `complete`。
PrimaryStore 代理 DataView 时必须透传这些完整性信息，供 Factor 对异步物化做有限
重读。

## 6. Archive 设计

归档逻辑行身份为：

```text
space_id + dataset_id + subject_id + freq + data_time + series_tag
```

Archive 将 `series_tag` 提升为物理分区键，每个 tag 每月一个 Parquet 文件。分区
身份为：

```text
space_id + dataset_id + freq + subject_id + series_tag + YYYYMM
```

目录和文件名都携带可逆编码后的完整 tag：

```text
{root}/{space}/{dataset}/{freq}/{subject}/series_tag={encoded_tag}/
  {space}__{dataset}__{subject}__{freq}__series_tag={encoded_tag}__{YYYYMM}.parquet
```

`encoded_tag` 只做路径安全的 percent-encoding，不解析业务含义。空 tag 的目录固定
为 `series_tag=`；例如 `venue:binance` 对应
`series_tag=venue%3Abinance`。文件名重复携带 tag，使文件离开原目录后仍能恢复完整
分区身份。本项目接受 tag 拆分产生的小文件，不为减少文件数量把不同 tag 合并到同一
Parquet。

Parquet 系统列使用：

| 列 | 类型 | 要求 |
| --- | --- | --- |
| `candle_begin_time` | `TIMESTAMP(NANOS, UTC)` | 非空 |
| `series_tag` | UTF-8 string | 非空、文件内为常量，并与路径解码值一致 |

单个文件只包含一个 tag，因此文件内按：

```text
candle_begin_time ASC
```

排序并以 `candle_begin_time` 唯一；全局逻辑身份由分区中的 `series_tag` 和行内
`data_time` 共同组成。事件消费、journal 分区、月文件物化、ArchiveFile、COS
object key、全量 Backfill 和 Parquet reader 必须原样保留 tag。Archive 不再生成、
解析或校验 `dimensions_json`。

## 7. Factor 设计

### 7.1 任务范围

事件聚合和 scheduler scope 为：

```text
space_id + source_dataset + target_dataset + subject_id + freq
```

scope 有意不包含 `series_tag`。任意 tag 在某个时间点变化时，同一 scope 的全部 tag
都要参与重算，才能支持 Binance/OKX 价差等跨 tag 因子。

每个执行任务只运行一个 Factor。不同 Factor 可以输出不同的 tag 和行数；将多个
Factor 强行合并为一个按物理输入行对齐的结果，会再次引入隐式行粒度约束。

### 7.2 Python 输入

Python DataFrame 包含：

```text
data_time | series_tag | <声明的 input_columns...>
```

并按 `data_time, series_tag` 稳定排序。框架读取当前 Subject/Frequency 下的全部 tag；
算法可通过 `params_json` 声明并筛选所需完整 tag，例如：

```json
{
  "left_tag": "venue:binance",
  "right_tag": "venue:okx",
  "output_tag": "venue_pair:binance-okx"
}
```

Factor 核心不解释这些参数，也不提供跨 Dataset join。缺少任一必需 tag 时，算法
不能用其他 tag 静默替代；若目标行身份已知，应返回该身份的 `null` 来清除旧结果。
空 DataFrame 表示“不写任何行”，不会自动删除以前的结果。

### 7.3 Python 输出

`compute(df, params)` 返回 DataFrame，而不是与物理输入行等长的列 Map：

```python
def compute(df, params):
    # 算法自行 pivot/filter。
    return pandas.DataFrame({
        "data_time": output_times,
        "series_tag": output_tags,
        "spread": output_values,
    })
```

输出契约：

- 必须包含 `data_time`、`series_tag` 和 Factor 声明的全部 `outputs`；
- 不允许多余业务列；
- `(data_time, series_tag)` 必须唯一；
- 输出 tag 可以与输入 tag 不同；
- 只写回目标半开范围 `[start_time, end_time)` 内的行；
- 数值只能是有限数值或 `null`，`NaN`/无穷归一化为 `null`；
- 空 DataFrame 是合法结果，不执行写入；
- 写回 RowKey 使用输出中的 `data_time` 和 `series_tag`。

### 7.4 `lookback_periods`

原 `lookback_rows` 改名为 `lookback_periods`。一个 period 是一个不同的
`data_time`，而不是一条物理行。同一时间点存在 10 个 tag 仍只算一个 period。

读取目标 chunk、历史上下文和历史修正的向后影响范围，都必须在完整 tag 集合上按
不同时间点计数：

- 计算目标第一个 period 时，最多补充前 `lookback_periods - 1` 个实际时间点；
- chunk 最多包含 2000 个目标 period，不能在同一时间点的 tag 集合中间截断；
- 历史时间点修正后，有限窗口因子向后扩展 `lookback_periods - 1` 个实际时间点；
- 无限窗口或递归状态因子不自动实时修复，使用显式全范围补算。

## 8. 内置写入方约定

内置模块使用集中常量生成 tag：

- Market 多 venue 数据：`venue:<provider>`；
- Monitor disk/network：`device:<device>`；
- Monitor filesystem：由生产者把 device 与 mountpoint 可逆编码为一个
  `filesystem:<identity>` 标量；`mountpoint` 同时保留为普通 Field；
- 无需区分序列的 Dataset 写空字符串。

Web 导入页提供一个普通 `Series Tag` 文本框，不再接受 Dimensions JSON。浏览页用
“未设置 selector”表达全部 tag，并允许按完整 tag 精确过滤。

## 9. 失败与重建策略

这是破坏性 schema 变更：

1. 停止 Storage、View、Archive、Factor 和所有写入方。
2. 部署同一提交生成的 Proto、`DatasetRowsUpserted@2` 服务和客户端，禁止新旧
   二进制混跑。
3. 删除旧 Pebble 数据、DuckDB View、Archive journal/Parquet 和旧 Factor SQLite。
4. 重新初始化 metadata，重新导入 seed。
5. 从数据源重新采集或导入；View 和 Archive 从新事件/新事实重建。

启动时若发现 `dimensions`、`dimensions_json`、`c_lookback_rows` 等旧 schema，
应返回明确错误并提示清理，不做自动迁移。

## 10. 明确不做

- 多 tag、Map、多列维度或 tag 表达式查询；
- 按 tag namespace 建索引或做 schema registry；
- Dataset 级 `series_tag_name` 或 allowed values；
- 跨 Dataset join、Factor DAG、拓扑调度；
- 旧 Proto、Pebble、DuckDB、Parquet、SQLite 兼容；
- 持久化 Factor 调度、Exactly-once、DLQ；
- 因 DataNode 清理自动删除 View 行。本问题仍作为独立 Storage 生命周期事项处理，
  不混入本次 `series_tag` 改造。

## 11. 验收不变量

1. 同一时间点写入空 tag、`venue:binance`、`venue:okx` 后三行均可精确读取。
2. 未设置 selector 返回全部三行，设置空 tag 只返回默认行。
3. DataNode、事件、View、Archive 和 Factor 写回中的 tag 字节完全一致。
4. View 按 `data_time, series_tag` 稳定排序；Archive 按 tag 分区，每个文件按
   `data_time` 稳定排序且不重复。
5. Factor 可在单 Dataset 内读取两个 venue tag，输出
   `venue_pair:binance-okx` 的价差行。
6. 同一时间点 tag 数量变化不改变 `lookback_periods` 的时间窗口含义。
7. 代码、Proto、SQL、Parquet schema、Web 类型和活跃文档中不再存在旧 dimensions
   契约。
