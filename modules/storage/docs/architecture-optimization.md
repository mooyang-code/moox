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

核心原则是：所有用户侧和业务侧数据访问都先进入 Access Service。Access 按请求语义转发到 PrimaryStore、View、Search 或 Archive 等内部服务。协议不暴露物理表名和具体存储细节。

## 存储组件职责

| 组件 | 角色 | 说明 |
| --- | --- | --- |
| SQLite | 元数据控制面 | 保存 Space、DataSource、Subject、DataSet、Field、Factor、View、PrimaryNode、Device、PrimaryRoute、ArchiveFile。当前表名仍保留 StorageNode/StorageRoute。 |
| Pebble | 在线事实主存 | 由 PrimaryStore Service 管理，接收 `WriteRows` 后的事实数据，支持低延迟写入和范围读取。 |
| DuckDB | 物化查询结果 | 由 View Service 管理，保存 View 的近期查询结果，供 `QueryView` 读取。 |
| Bleve | 文本索引 | 由 Search Service 管理，只同步 `DataSetColumn.text_indexed=true` 的列。 |
| Parquet | 事实冷备 | 由 Archive Service 管理，只从 Pebble 事实主存归档，不从 DuckDB 物化结果归档。 |

## 写入链路

```text
DataService.WriteRows
  -> schema.Validator
  -> PrimaryRoute resolver 选择 PrimaryStore 节点
  -> services/access 调用 PrimaryStore Service
  -> Pebble fact store
  -> eventbus.Bus.PublishRowsChanged
  -> Search Service / View Service / Archive Service 异步派生
```

当前实现优先保证同步写入成功后可从 Pebble 读回。派生层由事件或后台任务异步构建；事件发布失败和 Access 中对 Bleve 的即时索引刷新失败都不作为主写入失败条件。派生失败会影响 Search/View/Archive 的新鲜度，后续由重放、重建或归档任务补偿。

`core/eventbus` 是上层业务事件抽象，负责发布 `DataRowsChangedEvent` 等 storage 领域事件；`infra/transport` 是底层消息传输抽象，负责 NATS 等具体实现；`infra/eventbus` 将底层 producer 适配为业务 `eventbus.Bus`。storage 业务层只依赖 `eventbus.Bus`，不直接依赖 NATS。

`moox-storage` 启动时会初始化 View Service，并注册 tRPC timer 服务 `trpc.storage.view.timer`。定时器通过 `viewBuilderSchedule` 扫描 `build_status` 为空或 `pending` 的 View，发现新 View 或新增 ViewColumn 后重新构建并切换 `active_result`。

## 代码组织

```text
schema/            # storage 相关 SQL 表定义
internal/
  config/          # moox-storage 运行配置加载
  core/            # 领域抽象和纯业务规则
    eventbus/
    metadata/
    router/
    schema/
  infra/           # 底层组件实现
    device/
    eventbus/
    metadata/sqlite/
    transport/
  services/        # 可以独立部署或独立调度的服务层
    access/
    primary/
    search/
    view/
    archive/
```

`services/access` 是唯一公开数据访问入口，负责 tRPC 协议编排、校验、PrimaryRoute 解析和请求转发。`services/primary`、`services/search`、`services/view` 和 `services/archive` 是内部执行服务，均可独立部署。底层存储实现统一放在 `infra/device`，作为设备驱动层。`core` 和 `infra` 不直接作为服务启动，只作为上层服务依赖的领域抽象和底层实现。

## 查询链路

```text
ReadRows:
  Access -> PrimaryRoute -> PrimaryStore Service -> Pebble

SearchRows:
  Access -> Search Service -> Bleve

QueryView:
  Access -> View Service -> DuckDB active_result
```

`SearchRows` 查询集中式派生索引，不走 PrimaryRoute，也不 fan-out 到多个 Pebble 分片。`QueryView` 只查询已经构建的 View。没有可用物化结果时返回 `VIEW_NOT_FOUND`。

## 动态字段与因子

Field 和 Factor 都是 Space 内字典。它们进入 DataSet 时统一成为 `DataSetColumn`，进入 View 时统一成为 `ViewColumn`。

因子参数是 Factor 定义的一部分，例如：

```text
factor_id = ma20_close
algorithm = MA
params_json = {"window":20,"price":"close"}
```

新增因子时新增 Factor 和对应列，不要求在线事实主存改 schema。
