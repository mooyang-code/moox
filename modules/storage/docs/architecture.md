# Storage 架构设计

本文描述 storage 模块当前落地的架构。更完整的概念定义、元数据表结构和协议设计见仓库根目录：

- `docs/storage-concepts-and-design-intent.md`
- `docs/storage-target-architecture-and-metadata.md`
- `docs/pb-protocol-redesign.md`

## 设计目标

storage 服务在同一套元数据与访问入口下，统一承接多种形态的金融数据：

- 在线时序数据：tick、K 线、行情快照。
- 静态结构化数据：公司信息、证券基础资料、交易对资料。
- 动态因子结果：MA20、MA60、RSI14 等。
- 文本数据：公告、新闻、公司简介。
- 冷数据与备份：历史行情归档。

核心原则：**所有数据访问都先进入 Access Service**。Access 按请求语义把请求编排到 PrimaryStore、View、Search、Archive 等内部能力上。对外协议只暴露 Space/Dataset/Subject/View 等业务概念，不暴露物理表名、分片和具体存储引擎。

## 分层与代码组织

```text
proto/             # tRPC/protobuf 协议定义与生成代码（对外契约）
schema/            # 元数据 SQL 表定义
config/            # 部署配置（trpc_go.yaml 框架配置，storage.yaml 业务配置）
cmd/moox-storage/  # 服务入口：装配服务、注册 tRPC service 与 timer
internal/
  config/          # 运行配置加载（RuntimeConfig）
  core/            # 领域抽象与纯业务规则（不依赖具体设备）
    eventbus/      # 业务事件总线抽象 + MemoryBus
    metadata/      # 元数据存储接口（Store / Reader）
    router/        # PrimaryStoreTarget 路由解析器
    schema/        # 写入列契约校验器
    factvalue/     # TypedValue 处理、时间/过滤/排序/分页通用工具
    response/      # RetInfo 构造（统一错误码与响应）
  infra/           # 底层实现
    device/        # 存储设备驱动层
      pebble/      #   在线事实主存（KV，时序/点查）
      duckdb/      #   视图物化结果（OLAP，必须启用 CGO）
      bleve/       #   全文索引
      parquet/     #   冷归档文件
      factkey/     #   事实主键/维度编码
    metadata/
      sqlite/      #   元数据控制面实现
      cache/       #   元数据读快照（snapshotcache）
    eventbus/      # 把底层 producer 适配为业务 eventbus.Bus
    transport/     # 消息传输抽象
      nats/        #   NATS 实现
  services/        # 可独立部署/调度的服务层
    access/        #   唯一公开数据访问入口
    primary/       #   在线事实主存服务（local / remote client）
    search/        #   全文检索服务
    view/          #   视图物化构建与清理
    archive/       #   Parquet 归档服务
  testutil/        # 测试辅助
tests/e2e/         # 端到端测试（本地部署整套服务驱动）
```

分层约束：

- `services/access` 是唯一公开入口，负责 tRPC 协议编排、写入校验、路由解析与请求转发。
- `services/primary`、`services/search`、`services/view`、`services/archive` 是内部执行服务，均可独立部署，也可在同进程内装配。
- `infra/device` 是设备驱动层，所有具体存储引擎实现集中于此。
- `core` 与 `infra` 不直接作为服务启动，只作为上层服务依赖的领域抽象和底层实现。

## 核心概念

| 概念 | 含义 |
| --- | --- |
| Space | 业务命名空间，其下所有概念唯一性都限定在 Space 内 |
| DataSource | 数据来源（交易所、行情接口、文件导入、内部计算） |
| Subject | 数据对象（交易标的、榜单、新闻源、账户等），交易标的只是其中一种 |
| SubjectSymbol | Subject 在某个 DataSource 下的外部代码映射 |
| Dataset | 可写事实数据集，定义一类数据及其列契约，绑定唯一 DataSource |
| DatasetColumn | Dataset 下允许写入/读取的列，可声明 required 等写入契约 |
| Field / Factor | Space 内的字段字典 / 已参数化因子定义，进入 Dataset 即成为 DatasetColumn |
| View | 用户查询入口，也是可异步物化的结果定义；查询不存在的 View 返回 `VIEW_NOT_FOUND` |
| ViewColumn | View 对用户暴露的列 |
| PrimaryStoreNode / Device | 在线主存节点 / 底层存储设备 |
| PrimaryStoreRoute | 在线事实主存的水平切分路由 |
| ArchiveFile | 从 Pebble 归档出来的 Parquet 事实文件记录 |

## 存储组件职责

| 组件 | 角色 | 说明 |
| --- | --- | --- |
| SQLite | 元数据控制面 | 保存全部元数据（Space/DataSource/Subject/Dataset/Field/Factor/View/PrimaryStoreNode/Device/PrimaryStoreRoute/ArchiveFile）。元数据 CRUD 直接读写 SQLite。 |
| snapshotcache | 服务侧元数据读快照 | Access 的校验、路由、Search 列解释等**读路径**使用快照（`infra/metadata/cache`）。CRUD 不在写时更新缓存；启动加载与周期刷新（默认 10s）由 snapshotcache 负责。 |
| Pebble | 在线事实主存 | 由 PrimaryStore 管理，承接事实数据写入；内部按时序 `t|` 与记录 `r|` 两类 key 空间保存，支持低延迟写入、时间范围读取、记录按 `record_id/version` 读取、截面最新读，区间读支持游标分页。 |
| DuckDB | TimeSeries View 结果 | 由 View 管理，按 `view_version` 保存版本化物化结果供 `QueryTimeSeriesRows` 读取；结果表名以 `view_{view_id}` 开头，按 `ViewColumn` 展开为 `dataset_id.column_name` 物理列，并为行键、`subject_id + freq + data_time`、各视图字段建索引；必须使用 `CGO_ENABLED=1` 的真实磁盘 DuckDB 实现。 |
| Bleve | Record View 索引 | 由 Search 管理，按 `view_version` 保存版本化全文索引供 `SearchRecordRows` 读取；索引字段由 ViewColumn 决定。 |
| Parquet | 事实冷备 | 由 Archive 管理，只从 Pebble 事实主存归档，不从 DuckDB 物化结果归档。 |

## 写入链路

```text
AccessService.WriteTimeSeriesRows / WriteRecordRows
  -> schema.Validator        读 metadata cache：校验 Dataset 与列契约
  -> router.Resolver         读 metadata cache：解析 PrimaryStoreTarget（可按 dataset/subject 分组）
  -> services/access         调用 primary.Client（local 或 remote）
  -> Pebble fact store        写入主存
  -> eventbus.Bus             PublishTimeSeriesRowsChanged / PublishRecordRowsChanged
  -> Search / View / Archive  异步派生（事件或后台 timer）
```

要点：

- **事实行统一模型为 `space_id + dataset_id + data_key + version`**：同一 key 再次写入只替换本次携带的列与 attributes，未携带的旧列保留；写入协议不提供整行删除或范围清空语义，底层不用 `DeleteRange` 做范围清空。
- **时序数据与记录数据对外分开表达**：时序数据必须是固定 `subject_id + freq` 下按 `data_time` 演进的数据，例如 K 线；其逻辑 `data_key=subject_id|freq|dimhash`，`version=data_time`。非固定 `subject_id + freq` 的数据都归为记录数据，即使有时间线（新闻、研报、榜单版本），也通过 `record_id + version` 表达。
- **Pebble 物理 key 空间区分数据形态**：时序使用 `t|space|dataset|data_key|version`，记录使用 `r|space|dataset|data_key|version`。其中时序 `data_key=subject_id|freq|dimhash`，记录 `data_key=record_id`。`TimeRange` 的边界时间统一解析 RFC3339/RFC3339Nano，并归一化为 UTC 固定 9 位纳秒，保证 key 字典序与时间序一致。
- **主链路只对主存负责**：派生层（Search/View/Archive）异步构建。事件发布失败或派生消费失败都不作为主写入失败条件，只影响派生新鲜度，由重放、重建、归档任务补偿。
- **跨 target 非原子**：Access 按路由把同一批 rows 分组写入多个 `PrimaryStoreTarget`。`PrimaryStoreTarget` 是内部执行目标，不是事务边界；某 target 失败不回滚已成功 target，且仍会为已成功写入的 rows 发布变更事件，让派生侧追上成功部分。

### 事件总线

- `core/eventbus` 是上层业务事件抽象，发布 `TimeSeriesRowsChangedEvent` / `RecordRowsChangedEvent` 等 storage 领域事件。
- `infra/transport` 是底层消息传输抽象（NATS 等具体实现在 `infra/transport/nats`）。
- `infra/eventbus` 把底层 producer 适配为业务 `eventbus.Bus`。业务层只依赖 `eventbus.Bus`，不直接依赖 NATS。

行变更事件只携带发生变更的用户侧 key，不要求携带完整行。派生消费者收到事件后，必须**通过 Access 读接口回读最新完整行**再覆盖写入派生结果；消费者不直接请求某个 `PrimaryStoreTarget`，也不理解分片/路由/设备细节，从而保证消费重试幂等、Access 始终是唯一访问入口。

部署形态：

- `MemoryBus` 支持进程内订阅，Access 据此驱动 Search 索引增量更新。Search 是异步派生结果，**不提供写后立即可搜契约**。
- 生产环境可将 eventbus 配置为 NATS，`infra/eventbus.SubscriberBus` 通过统一前缀订阅，`consumer_name` 可配置 durable consumer。订阅失败是启动阶段显式错误，不能只记录日志后继续运行。
- 事件 subject 统一前缀默认 `moox.storage`，时序行变更默认 `moox.storage.time_series.rows_changed.v1`，记录行变更默认 `moox.storage.record.rows_changed.v1`；NATS stream 可用 `moox.storage.>` 前缀通配，便于扩展 View/Archive 等派生事件。

## 查询链路

```text
ReadTimeSeriesRows : Access -> router -> primary.Client -> Pebble
ReadRecordRows     : Access -> router -> primary.Client -> Pebble
SearchRecordRows   : Access -> Search Service -> Bleve
QueryTimeSeriesRows: Access -> View 物化结果（DuckDB active_result）
```

- `ReadTimeSeriesRows` 使用 `TimeSeriesKey + TimeRange` 读取固定 Subject/Freq 下的时序数据。`TimeRange` 是闭区间 `[start_time, end_time]`，两端为空表示无界；时间格式必须是 RFC3339/RFC3339Nano。
- `ReadRecordRows` 使用 `RecordKey + VersionRange` 读取记录数据。`VersionRange` 是闭区间 `[start_version, end_version]`，两端为空表示无界。
- `SearchRecordRows` 查询 Record View 的 active Bleve 索引，不走路由，也不 fan-out 到多个 Pebble 分片；支持全文 `text_query` 与结构化过滤。
- `QueryTimeSeriesRows` 只查询 TimeSeries View 的 active DuckDB 结果；没有可用 `active_result` 时返回 `VIEW_NOT_FOUND`。DuckDB 结果表使用 UTC 固定 9 位纳秒时间字符串存储 `data_time`，因此 `time_range` 可以安全下推到 SQL；`subject_id`、`freq`、filter、sort 与分页也优先在 DuckDB 执行。
- `RebuildTimeSeriesView` / `RebuildRecordView` 异步受理并返回 `rebuild_id`，后台按当前 `view_version` 从 PrimaryStore 全量扫描构建新结果；构建期间事件消费者会同时写 active 与 building 结果，切换前再 drain dirty key。

## 元数据控制面与缓存

- 元数据 CRUD（Create/Update/Get/List）直接读写 SQLite，读自身写入立即一致。
- Access 的**数据面读路径**（路由解析、写入校验、Search 列解释、视图查询暴露 `active_result`）走 snapshotcache 快照，默认每 10s 刷新一次。因此新建路由/列契约后，写读路径可见前存在短暂延迟（端到端测试以轮询等待覆盖这一行为）。

## 路由与水平分片

`router.Resolver` 依据 `PrimaryStoreRoute` 把 `space_id + dataset_id + subject_id` 解析为 `PrimaryStoreTarget`。路由可按精确 `subject_id`、`subject_pattern`、哈希规则匹配，并带优先级。`PrimaryStoreTarget` 描述目标节点、引擎与设备表，是内部执行目标，对上层不可见。

## 派生与后台调度

`cmd/moox-storage` 启动时装配 Access、PrimaryStore、View、Archive，并注册 tRPC timer handler：

| Timer | 调度器 | 行为 |
| --- | --- | --- |
| `trpc.storage.view.timer` | `viewBuilderSchedule` | 扫描 `view_version > active_view_version`、`pending`、残留 `building` 或 `failed` 的 View，构建并切换 `active_result` |
| `trpc.storage.view.cleanup.timer` | `viewBuilderSchedule`（`op=cleanup`） | 清理不再被任何 active View 引用的旧 DuckDB 结果表 |
| `trpc.storage.view.retry_failed.timer` | `viewBuilderSchedule`（`op=retry_failed`，默认关闭） | 显式重试 `failed` 的 View |
| `trpc.storage.archive.timer` | `archiveSchedule` | 按 `space_id`/`dataset_id`/`partition_key`/`start_time`/`end_time` 触发 Parquet 归档并登记 ArchiveFile |

说明：

- 归档 `dataset_id=*` 表示归档该 Space 下所有 active Dataset。
- timer 框架的 `params` 先按 `&` 拆分，因此 archive 业务参数用 `;` 分隔；默认配置为 `disable=1` 的按 Space 启用模板，需要时显式开启。
- View 构建采用按 View 加锁，避免并发重复构建；崩溃残留的 `building` 状态会被重新拾起重试。失败或构建中的结果不会改变 `active_result`，只有 `CompleteViewBuild` 成功后才切读。

## 动态字段与因子

Field 和 Factor 都是 Space 内字典：进入 Dataset 统一成为 `DatasetColumn`，进入 View 统一成为 `ViewColumn`。因子参数是 Factor 定义的一部分：

```text
factor_id   = ma20_close
algorithm   = MA
params_json = {"window":20,"price":"close"}
```

新增因子时只新增 Factor 及对应列，不要求在线事实主存改 schema。

## 部署形态

- 默认在同进程内用 `primary.LocalClient` 访问 Pebble；Access 与 PrimaryStore 通过进程级共享的 Pebble 实例（引用计数）协作，不会重复打开同一目录。
- 仅当显式配置 `storage.primary.service_name` 时，Access 才通过 `primary.RemoteClient` 经远程 PrimaryStore Service 转发，支持主存独立水平扩展。
- 运行配置由 `internal/config.RuntimeConfig` 加载，覆盖元数据路径、各设备目录、primary 服务名与 eventbus 类型（memory/nats）。

## 测试

- 单元测试：分布在各包 `*_test.go`，覆盖 device、core、services 等；`make test` 运行（CGO 开启以使用真实 DuckDB）。
- 端到端测试：`tests/e2e` 在本地真实部署整套子服务（独立进程/端口/目录），以 tRPC 客户端依次驱动 Metadata/Data/Query/Archive 各接口，测试数据使用 K 线 CSV。`make e2e` 运行，详见 `tests/README.md`。
