# 量化金融数据系统概念说明

本文是当前 moox 量化数据系统的概念索引。详细设计以以下文档为准：

- `docs/storage-concepts-and-design-intent.md`
- `docs/storage-target-architecture-and-metadata.md`
- `docs/pb-protocol-redesign.md`

## 一句话模型

```text
Space 里管理 DataSource、Subject、Field、Factor、DataSet 和 View。
用户按 DataSet 写入与读取事实数据，按 View 查询异步物化后的组合结果。
Pebble 是在线事实主存，DuckDB、Bleve、Parquet 从 Pebble 变更派生。
```

## 概念关系

```mermaid
flowchart TD
    SP["Space"]
    SRC["DataSource"]
    SUB["Subject"]
    SYM["SubjectSymbol"]
    DS["DataSet"]
    DSS["DataSetSubject"]
    FIELD["Field"]
    FACTOR["Factor"]
    DC["DataSetColumn"]
    VIEW["View"]
    VC["ViewColumn"]
    NODE["StorageNode"]
    DEV["Device"]
    ROUTE["StorageRoute"]

    SP --> SRC
    SP --> SUB
    SP --> FIELD
    SP --> FACTOR
    SP --> DS
    SP --> VIEW
    SRC --> DS
    SRC --> SYM --> SUB
    DS --> DSS --> SUB
    DS --> DC
    DC --> FIELD
    DC --> FACTOR
    VIEW --> DS
    VIEW --> VC
    VC --> DC
    ROUTE --> NODE --> DEV
```

## 核心概念

| 概念 | 含义 |
| --- | --- |
| `Space` | 业务命名空间。本文所有“全局”均指 Space 内全局。 |
| `DataSource` | 数据来源，例如交易所、供应商 API、文件导入、内部计算。 |
| `Subject` | Space 内业务对象，例如交易标的、榜单、新闻源、账户。 |
| `SubjectSymbol` | Subject 在某个 DataSource 下的外部代码映射。 |
| `DataSet` | 可写事实数据集，并且只绑定一个 DataSource。 |
| `DataSetSubject` | DataSet 的对象池，用于采集范围和查询范围。 |
| `Field` | Space 内普通字段字典。 |
| `Factor` | Space 内、已参数化的因子结果定义。 |
| `DataSetColumn` | DataSet 下允许写入的列，可来自 Field、Factor 或系统列。 |
| `View` | 查询入口和异步物化结果定义。 |
| `ViewColumn` | View 对用户暴露的列。 |
| `StorageNode` | adapter 存储代理节点。 |
| `Device` | 底层具体存储组件，例如 Pebble、DuckDB、Bleve、Parquet。 |
| `StorageRoute` | 在线事实主存的水平切分路由。 |

## 读写边界

- 写入事实数据：使用 `DataService.WriteRows`，入口是 `DataSet`。
- 读取事实数据：使用 `DataService.ReadRows`，入口是 `DataSet`。
- 组合分析查询：使用 `QueryService.QueryView`，入口是已经登记并构建的 `View`。
- 文本检索：使用 `QueryService.SearchRows`，入口是 `DataSet`。

系统不在线生成临时组合查询计划。调用方请求不存在的组合时，返回 `VIEW_NOT_FOUND`。

## 存储职责

| 组件 | 职责 |
| --- | --- |
| Pebble | 在线事实主存，支持低延迟写入和按 key 范围扫描。 |
| DuckDB | View 的近期物化查询结果。 |
| Bleve | 只索引 `DataSetColumn.text_indexed=true` 的文本列。 |
| Parquet | 从 Pebble 事实主存归档生成冷备文件。 |
| SQLite | 元数据控制面。 |

