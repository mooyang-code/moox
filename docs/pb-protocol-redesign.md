# Storage PB Protocol Design

本文记录当前 storage 模块的 PB 协议边界。项目尚未上线，不保留历史兼容接口。

## 文件边界

| 文件 | 职责 |
| --- | --- |
| `common.proto` | 通用错误码、分页、排序、时间范围、版本范围、列值、鉴权信息 |
| `metadata.proto` | Space、Dataset、Subject、Field、Factor、View、Device、PrimaryStore 拓扑等元数据 |
| `access.proto` | 用户侧事实数据读写入口 |
| `store.proto` | Access 到 PrimaryStore 的内部执行协议 |
| `view.proto` | DuckDB/Bleve 等 View 读模型 |
| `message.proto` | storage 领域事件 |

## 命名约定

- `Dataset` 是唯一写法；字段名保持 `dataset_id`。
- TimeSeries 表示固定 `subject_id + freq` 的连续时序数据。
- Record 表示其他事实数据；即使新闻、公告、研报有时间线，也通过 Record 的 `version` 表达。
- 用户侧协议只暴露 `TimeSeriesKey` 和 `RecordKey`，不暴露底层拼接 key。
- 内部主存协议统一使用 `PrimaryStoreKey`、`PrimaryStoreRow`、`PrimaryStoreTarget`。
- 主存拓扑统一使用 `PrimaryStoreNode`、`PrimaryStoreRoute`。

## 时间与版本

`TimeRange` 是闭区间：

```text
start_time <= data_time <= end_time
```

`start_time`、`end_time`、`TimeSeriesKey.data_time` 必须使用 RFC3339 或 RFC3339Nano。服务端内部统一归一化为 UTC 固定 9 位纳秒格式：

```text
2006-01-02T15:04:05.000000000Z
```

`VersionRange` 也是闭区间：

```text
start_version <= version <= end_version
```

Record 的 `version` 可以是业务版本字符串，也可以是 RFC3339/RFC3339Nano。时间型版本在底层会按同一规则归一化，保证范围比较稳定。

## AccessService

AccessService 是唯一公开事实数据读写入口：

```text
WriteTimeSeriesRows
ReadTimeSeriesRows
WriteRecordRows
ReadRecordRows
```

TimeSeries 写入使用：

```text
TimeSeriesKey(space_id, dataset_id, subject_id, freq, dimensions, data_time)
TimeSeriesRow(key, columns, attributes)
```

Record 写入使用：

```text
RecordKey(space_id, dataset_id, record_id, version)
RecordRow(key, columns, attributes)
```

写入语义固定为列级 patch/upsert：同一个 key + version 再次写入时，只更新本次携带的列和 attributes；未携带的旧列保留。协议不提供整行替换、范围删除或按 scope 清空能力。

服务侧写入校验只要求 Dataset 存在、key 必填字段合法、携带列已登记且类型匹配。DatasetSubject 绑定关系由应用层、管理台或 CLI 独立维护，事实写入链路不自动写绑定，也不要求绑定先于数据写入。

## PrimaryStoreService

PrimaryStoreService 是内部执行协议，只在 Access 与主存节点之间使用：

```text
WritePrimaryRows
ReadPrimaryRows
```

内部主存行使用：

```text
PrimaryStoreKey(space_id, dataset_id, kind, key, version)
PrimaryStoreRow(key, columns, attributes)
PrimaryStoreTarget(space_id, dataset_id, node_id, device_id, engine, endpoint, device_table)
```

Access 根据 `PrimaryStoreRoute` 解析出 `PrimaryStoreTarget`，再把同批 rows 按 target 分组写入。跨 target 写入不提供全局原子性；已经成功写入的 target 不因后续 target 失败而回滚。

Pebble 物理 key 空间区分 TimeSeries 与 Record：

```text
t|space|dataset|subject|freq|version|dimhash
r|space|dataset|record_id|version
```

## ViewService

ViewService 承载异步派生读模型：

```text
QueryTimeSeriesRows
SearchRecordRows
RebuildTimeSeriesView
RebuildRecordView
```

`QueryTimeSeriesRows` 面向 TimeSeries + DuckDB。请求以 View 为入口，返回 `TimeSeriesRow` 和 View columns。它只查询已登记、已异步构建的 View；不存在的组合返回 `VIEW_NOT_FOUND`。

`SearchRecordRows` 面向 Record + Bleve。请求同样以 View 为入口，使用全文 query、filters、sorts、keys 或 version range 查询 Record 派生索引，返回 `RecordRow` 和 View columns。

`RebuildTimeSeriesView` 与 `RebuildRecordView` 是异步控制面接口，只负责受理重建任务并返回 `rebuild_id`。后台任务必须通过 Access 读接口回读主存，不能绕过 Access 直接请求主存 target。

## Event Messages

主存写入完成后发布领域事件：

```text
TimeSeriesRowsChangedEvent
RecordRowsChangedEvent
```

事件 subject 使用统一前缀：

```text
moox.storage.time_series.rows_changed.v1
moox.storage.record.rows_changed.v1
```

事件中的 rows 是变更提示，不要求携带完整行。派生消费者收到事件后通过 Access 读接口回读最新完整行，再覆盖写入 DuckDB、Bleve 或其他派生存储，使重放和重试保持幂等。

## MetadataService

MetadataService 管理控制面元数据。底层可以使用 SQLite，但主服务和 CLI 只依赖 metadata store / service 抽象。

主要拓扑对象：

```text
PrimaryStoreNode
PrimaryStoreRoute
Device
```

`PrimaryStoreNode` 表示在线主存服务节点。`PrimaryStoreRoute` 表示 Dataset/Subject 到 PrimaryStore 节点的水平切分规则。`Device` 表示节点上的具体存储设备或目录，例如 Pebble、DuckDB、Bleve、Parquet。
