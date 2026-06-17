# Storage 架构优化方案

本文记录当前 storage 模块的落地架构。更完整的概念、表结构和协议说明见仓库根目录：

- `docs/storage-concepts-and-design-intent.md`
- `docs/storage-target-architecture-and-metadata.md`
- `docs/pb-protocol-redesign.md`

## 目标

storage 服务需要同时支持：

- 在线时序数据，例如 tick、K 线、行情快照。
- 静态结构化数据，例如公司信息、证券基础资料。
- 动态因子结果，例如 MA20、MA60、RSI14。
- 文本数据，例如公告、新闻、公司简介。
- 冷数据和备份数据，例如历史行情归档。

核心原则是：上层协议表达用户要写入或查询的数据语义，底层执行器再选择 Pebble、DuckDB、Bleve 或 Parquet。协议不暴露物理表名和具体存储细节。

## 存储组件职责

| 组件 | 角色 | 说明 |
| --- | --- | --- |
| SQLite | 元数据控制面 | 保存 Space、DataSource、Subject、DataSet、Field、Factor、View、StorageNode、Device、StorageRoute、ArchiveFile。 |
| Pebble | 在线事实主存 | 接收 `WriteRows` 后的事实数据，支持低延迟写入和范围读取。 |
| DuckDB | 物化查询结果 | 保存 View 的近期查询结果，供 `QueryView` 读取。 |
| Bleve | 文本索引 | 只同步 `DataSetColumn.text_indexed=true` 的列。 |
| Parquet | 事实冷备 | 只从 Pebble 事实主存归档，不从 DuckDB 物化结果归档。 |

## 写入链路

```text
DataService.WriteRows
  -> schema.Validator
  -> StorageRoute resolver
  -> StorageNode adapter
  -> Pebble fact store
  -> DataRowsChangedEvent
  -> Bleve / DuckDB viewbuilder / Parquet archive
```

当前实现优先保证同步写入成功后可从 Pebble 读回。派生层由事件或后台任务异步构建。

`moox-storage` 启动时会初始化 `viewbuilder`，并注册 tRPC timer 服务 `trpc.storage.viewbuilder.timer`。定时器通过 `viewBuilderSchedule` 扫描 `build_status` 为空或 `pending` 的 View，发现新 View 或新增 ViewColumn 后重新构建并切换 `active_result`。

## 查询链路

```text
ReadRows:
  DataSet + DataScope + TimeRange -> Pebble

SearchRows:
  DataSet + text_query + filters -> Bleve / Pebble

QueryView:
  Space + View -> active_result -> DuckDB
```

`QueryView` 只查询已经构建的 View。没有可用物化结果时返回 `VIEW_NOT_FOUND`。

## 动态字段与因子

Field 和 Factor 都是 Space 内字典。它们进入 DataSet 时统一成为 `DataSetColumn`，进入 View 时统一成为 `ViewColumn`。

因子参数是 Factor 定义的一部分，例如：

```text
factor_id = ma20_close
algorithm = MA
params_json = {"window":20,"price":"close"}
```

新增因子时新增 Factor 和对应列，不要求在线事实主存改 schema。
