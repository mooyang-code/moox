# Storage 多角色与异步派生设计

## 背景

当前 `moox-storage` 在同一个 Access Service 内发布并消费主存变更事件。`eventbus.type=memory` 时，事件发布会同步执行 TimeSeries 派生处理，DuckDB 写入会占用写请求耗时。Record 到 Bleve 虽然已有进程内队列，但消费者仍挂在 Access Service 内部。这个模型不适合把接入层、主存层和派生层部署到不同机器。

本设计把运行时职责拆成三个角色：`access`、`primary` 和 `deriver`。三个角色仍由同一个 `moox-storage` 二进制承载，进程通过配置决定启动哪些角色。

## 目标

- `memory` 与 `nats` 两种事件总线都采用异步消费模型。
- 默认事件总线使用 NATS，不静默降级到 memory。
- Access 写入链路只负责校验、路由、主存写入和事件发布。
- DuckDB 与 Bleve 派生写由独立 `deriver` 角色消费事件完成。
- `access`、`primary` 和 `deriver` 可以部署到不同机器。
- Deriver 支持批量消费和批量写入，提高 DuckDB 与 Bleve 的吞吐。

## 非目标

- 本次不拆多个二进制。
- 本次不改变外部 Access/View/Metadata 协议。
- 本次不重做 View 构建算法。
- 本次不把历史数据自动重建到新 View；历史数据仍通过 `RebuildTimeSeriesView` 和 `RebuildRecordView` 手动触发。

## 运行角色

`access` 是对外接入层。它接收用户写入和读取请求，读取元数据，解析路由，调用 PrimaryStore，并在主存写入成功后发布行变更事件。它不直接写 DuckDB 或 Bleve。

`primary` 是主存执行层。它持有 Pebble 数据目录，执行 `PrimaryStoreService.WriteRows`、`ReadRows` 和 `ScanPrimaryRows`。它只理解内部主存 key/row，不理解 View、DuckDB 或 Bleve。

`deriver` 是异步派生层。它订阅主存行变更事件，批量聚合 key，回读 Access 当前行，按 View 定义投影，然后批量写 DuckDB 或 Bleve。TimeSeries View 写 DuckDB，Record View 写 Bleve。

## 默认配置

业务配置新增 `roles` 和 `deriver`：

```yaml
storage:
  roles:
    - access
    - deriver
  eventbus:
    type: nats
    nats_url: nats://127.0.0.1:4222
    stream_name: MOOX_STORAGE
    subject_prefix: moox.storage
    consumer_name: storage_deriver
  deriver:
    access_service_name: trpc.storage.access.AccessService
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

默认使用 NATS。`eventbus.type=nats` 时，`nats_url` 必须存在且可连接；启动失败时进程直接退出并输出明确错误。

单进程开发和测试可以显式配置：

```yaml
storage:
  roles:
    - access
    - primary
    - deriver
  eventbus:
    type: memory
  deriver:
    access_service_name: ""
    batch_size: 100
    batch_wait_ms: 50
    max_workers: 1
```

## 部署形态

单机开发部署启动 `access`、`primary` 和 `deriver` 三个角色。此模式可使用 memory，也可使用本机 NATS。

接入节点只启动 `access`。它对外提供 AccessService、MetadataService 和 ViewService 查询入口，写入时调用远端 PrimaryStoreService，并向 NATS 发布事件。

主存节点只启动 `primary`。它持有 Pebble 数据目录，并对 Access 节点提供 PrimaryStoreService。

派生节点只启动 `deriver`。它连接 NATS，消费行变更事件，通过 `deriver.access_service_name` 回读 AccessService，并写入本机或共享路径下的 DuckDB/Bleve 存储。单进程 memory 模式可以把 `access_service_name` 留空，此时 Deriver 使用进程内 Access 实例。

## 事件主题

事件主题继续按数据形态拆分：

- `moox.storage.time_series.rows_changed.v1`
- `moox.storage.record.rows_changed.v1`

NATS stream 继续使用统一前缀：

- `moox.storage.>`

Deriver 分别订阅 TimeSeries 和 Record 主题。两个主题可以共享同一个 durable consumer 前缀，但运行时应区分 consumer name，例如：

- `storage_deriver_time_series`
- `storage_deriver_record`

## 写入链路

TimeSeries 写入链路：

```text
Client
  -> AccessService.WriteTimeSeriesRows
  -> PrimaryStoreService.WriteRows
  -> Publish TimeSeriesRowsChangedEvent
  -> 返回写入结果

Deriver
  -> 消费 TimeSeriesRowsChangedEvent
  -> 批量回读 AccessService.ReadTimeSeriesRows
  -> 按 ViewColumn 投影
  -> 批量写 DuckDB active/building result
```

Record 写入链路：

```text
Client
  -> AccessService.WriteRecordRows
  -> PrimaryStoreService.WriteRows
  -> Publish RecordRowsChangedEvent
  -> 返回写入结果

Deriver
  -> 消费 RecordRowsChangedEvent
  -> 批量回读 AccessService.ReadRecordRows
  -> 按 ViewColumn 投影
  -> 批量写 Bleve active/building index
```

Access 发布事件失败不改变已经写入主存的数据。Access 需要记录派生新鲜度错误；Deriver 可通过重建任务补偿。

## Memory 异步语义

`MemoryBus` 仍是单进程内存实现，但发布事件时只入队，不直接执行 handler。每类事件有独立 goroutine 消费队列。

MemoryBus 的行为约束：

- `Publish*RowsChanged` 在事件入队后返回。
- handler 在独立 goroutine 中执行。
- `Close` 等待已入队事件处理完成，或在超时策略下返回明确错误。
- 测试可以通过 `Drain` 或 `Wait` 辅助方法等待消费完成。

这让 memory 与 nats 的主链路语义一致：写入请求不执行派生写。

## Deriver 批量消费

第一版使用统一批量配置：

```yaml
storage:
  deriver:
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
```

`access_service_name` 指向 Deriver 回读数据时使用的 AccessService。`batch_size` 控制每次最多聚合多少个变更 key。`batch_wait_ms` 控制未凑满批次时的最大等待时间。`max_workers` 控制每类事件的并发 worker 数。

Deriver 内部仍按 TimeSeries 和 Record 分两条队列。每条队列执行相同批量策略：

1. 从事件总线接收事件。
2. 提取 keys 并放入 buffer。
3. buffer 达到 `batch_size`，或等待达到 `batch_wait_ms` 后生成批次。
4. 按 `space_id + dataset_id` 分组。
5. 批量回读 Access。
6. 按相关 View 分组投影。
7. 批量写 DuckDB 或 Bleve。
8. 全部写入成功后 ack NATS 消息。

如果批次内部分 key 失败，Deriver 应返回错误，让 NATS 重投该批事件。Memory 模式记录错误并保留可观测日志。

## 回读规则

Deriver 不直接理解主存分片，也不直接请求具体 PrimaryStore target。它通过 AccessService 回读当前行。

TimeSeries 使用 `ReadTimeSeriesRows`，Record 使用 `ReadRecordRows`。Deriver 可以在内部按 key 合并请求，减少 RPC 次数。

## View active/building 双写

Deriver 对增量事件同时写 active 和 building 结果：

- `active_result` 存在时写 active。
- `building_result` 存在时写 building。
- View 定义无法投影当前数据时，标记 View pending，让定时重建或手动重建接管。

重建任务仍从 PrimaryStore 全量扫描构建新版本。构建期间 Deriver 负责增量双写；切换前继续执行 dirty drain。

## 错误处理

启动期错误必须显式失败：

- NATS 无法连接。
- NATS stream 或 consumer 初始化失败。
- Deriver 订阅主题失败。
- `deriver` 角色缺少回读 Access 地址。

运行期错误按派生新鲜度处理：

- Access 主写成功但事件发布失败：记录错误，返回主写成功。
- Deriver 消费失败：NATS 不 ack，等待重投。
- DuckDB/Bleve 写失败：记录 View 错误，触发重投或后续重建。

## 测试策略

单元测试覆盖：

- MemoryBus 发布后异步消费。
- MemoryBus `Wait` 能等待已入队事件完成。
- NATS subject 使用统一前缀，并区分 TimeSeries 与 Record。
- Deriver batcher 按数量触发。
- Deriver batcher 按等待时间触发。
- Deriver 按 `space_id + dataset_id` 分组回读。

集成测试覆盖：

- `roles=[access, primary, deriver] + memory` 单进程写入后，等待消费并查询 DuckDB/Bleve。
- `roles=[access]` 不启动派生消费者。
- `roles=[deriver]` 不注册 Access 写入服务。
- 默认配置缺少 NATS 时启动失败。

端到端测试覆盖：

- Access 进程、Primary 进程和 Deriver 进程分开启动。
- 写入 TimeSeries 后通过 View 查询 DuckDB 结果。
- 写入 Record 后通过 View 搜索 Bleve 结果。
- 批量写入时 Deriver 批量回读和批量写入生效。

## 实施顺序

1. 扩展运行配置，增加 `roles` 和 `deriver`。
2. 把默认 eventbus 改为 NATS。
3. 将 MemoryBus 改为异步队列模型。
4. 新增 `internal/services/deriver`。
5. 从 Access Service 迁移事件消费、DuckDB 写入、Bleve 写入和 dirty tracking。
6. 按 roles 控制 main 中注册的 tRPC 服务和后台消费者。
7. 增加 Deriver 批量消费配置与批处理逻辑。
8. 补充单元、集成和 e2e 测试。
