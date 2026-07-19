# Storage Dataset 单归属与多进程简化设计

## 状态与优先级

本设计已于 2026-07-19 完成讨论确认。它保留 Storage 的事实原子提交、JetStream、跨 Dataset View、字段动态追加和 A/B Index 能力，但以个人量化交易系统的长期可维护性为第一原则，删除 Dataset 内分片、高可用、Dataset 迁移和通用 Schema 演进。

本设计取代 `2026-07-18-storage-primary-shard-boundary-design.md` 中关于多 Shard 路由、Node Service Gateway、Schema Fence、Metadata Cache 和 View Build Consumer 的目标设计。现有 `2026-07-19-storage-consistency-review-remediation.md` 执行计划必须按本设计重写后才能实施。

## 背景

MooX 是个人量化交易系统。它需要把行情、因子、账户等 Dataset 分别部署到不同服务器，以隔离磁盘、计算和故障；它不需要把同一个 Dataset 按 RowKey、Subject 或时间范围分片，也不需要副本、自动故障转移或在线再平衡。

典型部署如下：

```text
kline_1m      -> data-node-market
trade_tick    -> data-node-market
factor_value  -> data-node-factor
account       -> data-node-private
```

View 仍需组合不同 DataNode 上的 Dataset。例如一个策略 View 可以同时包含 `kline_1m` 和 `factor_value`。因此事实存储按 Dataset 分机，ViewBuilder 集中消费所有事实事件并物化跨节点 View。

## 设计原则

1. 数据正确性优先，但不以高可用为目标。
2. 一个职责只保留一个事实源和一条写路径。
3. 用重新计算替代复杂的恢复状态机；字段追加通过内存快照原子生效。
4. 默认多进程，同一套进程既可部署在一台机器，也可分布到多台机器。
5. 不为未来可能出现的 Dataset 内分片预留抽象。
6. 所有错误显式返回；系统不静默降级、不跳过事件。

## 业务不变量

### Dataset 单归属

1. 每个 Dataset 在任一时刻只属于一个 DataNode。
2. 一个 DataNode 可以承载多个 Dataset。
3. Dataset 不按 Subject、RowKey、时间或权重继续分片。
4. Dataset 的公开读写请求只包含一个 `dataset_id`，不提供跨 Dataset 原子批次。
5. Dataset 创建后，Metadata API 永久拒绝修改 `data_node_id`。MooX 不提供 Dataset 迁移能力；用户自行处理的停机和数据复制属于系统外操作。

### Append-only Schema

MooX 的业务只追加字段，不改变已有字段定义：

1. 已有字段永不删除。
2. 已有字段的 `field_id` 永不修改，也不会被其他字段复用。
3. 已有字段的 `value_type` 永不修改。
4. 所有字段都允许缺失；系统不定义 Required 字段。
5. Schema 变更只允许追加字段。
6. 新字段在 Add Field 成功返回后动态生效，不需要重启或重新部署。
7. 系统不实现字段删除、Field ID 迁移、类型迁移、双读或双写兼容。
8. 事实 Merge 只新增或覆盖字段值和 Attribute；已经写入的单个字段值或 Attribute 不能单独清除。
9. `DeleteRows` 保留，用于删除完整事实行；它不是字段级删除接口。

显示名称、描述等非 Schema 展示信息可以修改，但不能改变 `field_id`、类型或数据语义。

### 单实例协调

1. `storage-primary` 只运行一个实例。
2. `storage-view` 只运行一个实例。
3. 每个 `data_node_id` 只运行一个写入 Owner。
4. 系统不实现 Leader Election、Quorum、Replica 或自动 Failover。

## 目标进程架构

```text
Browser
  -> Admin Gateway
       |-> storage-primary
       `-> storage-view

storage-primary
  Metadata SQLite
  PrimaryStore
       | direct tRPC
       |-> storage-node-market -> Pebble + Outbox
       |-> storage-node-factor -> Pebble + Outbox
       `-> storage-node-private -> Pebble + Outbox

storage-node-*
  -> JetStream MOOX_STORAGE
       |-> storage_view_active
       |-> storage_view_rebuild
       |-> Archive
       `-> Factor

storage-view
  ViewBuilder
  DataView
  ViewIndex
       |-> DuckDB
       `-> Bleve
```

浏览器继续通过 Admin Gateway。Storage 内部进程不经过 Node Service Gateway；它们使用带 Service HMAC 的直接 tRPC。DataNode 不进入 Admin Gateway 方法表，也不暴露给浏览器。

同一个 Storage Binary 可以通过 Role 启动 `primary`、`node` 或 `view`，但生产默认使用独立进程。开发环境可以在同一台机器上启动全部进程，不需要修改数据和协议模型。

### DataNode 最终命名

Storage 不再保留 Shard 概念。实施时一次性替换所有活动符号：

| 旧名 | 最终名 |
| --- | --- |
| `DataShard` | `DataNode` |
| `datashard` | `datanode` |
| `data_shard.proto` | `data_node.proto` |
| `storage-shard` | `storage-node` |
| `role: shard` | `role: node` |
| `shard_id` | `node_id` |
| `GetShardState` | `GetNodeState` |
| `BeginShardSnapshot` | `BeginNodeSnapshot` |
| `EndShardSnapshot` | `EndNodeSnapshot` |
| `ShardCheckpoint` | `ViewSourceCheckpoint` |
| `storage_shard_*` metrics | `storage_node_*` metrics |

代码、Proto、配置、Seed、CLI、指标、测试和现行文档必须原子更新，不保留 Alias、兼容 RPC 或旧 YAML 字段。历史计划文件可以保留原文，但不作为活动命名来源。

## Metadata 模型

### DataNode

```text
DataNode
  node_id
  service_target
  status
```

`service_target` 是 PrimaryStore 的直接 tRPC 目标。系统不再定义 `ShardNode`、`ShardRoute`、`ShardTarget`、Hash Pool、权重或 Rendezvous Hash。

### Dataset

Dataset 直接保存归属节点，不单独建立 `DatasetPlacement`：

```text
Dataset 新增
  data_node_id
```

字段字典和列约束继续由现有 Field、DatasetColumn 模型表达，不把它们重复嵌入 Dataset。PrimaryStore 读取 Dataset 时组装 Schema，并同时获得目标节点。`data_node_id` 从 Dataset 创建成功开始永久不可修改，不以是否写入过事实为条件。

Metadata 只接受 Schema v4。版本检查直接比较 `version == metadataSchemaVersion`；删除 `metadataSchemaVersionCompatible` 及所有兼容版本分支。版本不匹配时启动失败，由用户清理数据库并重新部署。

### Runtime Catalog

`storage-primary` 启动时从 SQLite 加载写路径需要的 Catalog：

```text
RuntimeCatalog
  routing.datasets[dataset_id] -> data_node_id
  routing.data_nodes[node_id]   -> service_target, status
  schemas[dataset_id]           -> atomic DatasetSchema pointer
```

Routing 在进程运行期间不变，不使用 TTL、后台 Refresh 或通用 Metadata Cache。字段列表允许动态追加：Metadata 事务提交后，PrimaryStore Clone 对应 DatasetSchema、加入新字段，再原子替换指针。已经取得旧指针的在途请求继续完成；因为新 Schema 只是旧 Schema 的字段超集，旧请求仍然合法。

DataNode 和 `service_target` 的变化不热加载。Metadata API 始终拒绝修改已创建 Dataset 的 `data_node_id`。

其他低频 Metadata CRUD 和 List 直接访问 SQLite。List 使用 SQL `ORDER BY + LIMIT + OFFSET`，禁止先全表加载再在内存分页。ArchiveFile、Build History 和审计记录不进入全量 Cache。

## 字段生命周期

### Metadata 更新

Field 是 Space 级稳定字典，DatasetColumn 是 Dataset 对 Field 的引用。字段接口只接受向 Dataset 追加 DatasetColumn；可引用已有的同类型 Field，也可在同一事务中创建新 Field 后引用。它逐项比较旧 Schema：

- 旧 Field ID 必须仍然存在。
- 旧字段的 Type 必须完全相同。
- 同一 Dataset 内不能重复绑定 Field ID。
- Space 中已有 Field ID 只能按原 Type 和原语义被其他 Dataset 引用，不能作为另一种字段复用。
- Update/Delete Dataset Field API 对活动 Schema 返回 `FIELD_IMMUTABLE`。

Add Field 是幂等操作：相同 Dataset、Field ID 和 Type 已存在时返回当前字段并重新确认 Runtime Schema；相同 Field ID 但 Type 或数据语义不同则返回 `FIELD_IMMUTABLE`。

新增字段在一个 SQLite 事务中提交。Add Field 在同一 Dataset 的更新锁内完成 SQLite Commit 和 DatasetSchema 指针替换，只有两步都完成后才返回成功；因此调用方收到成功响应后即可写入新字段，不暂停 Dataset，也不要求重新部署。若进程在 SQLite Commit 后、指针替换前退出，本次调用不会返回成功；重启时从 SQLite 加载完整字段集合，调用方可安全重试幂等 Add Field。

### PrimaryStore 与 DataNode 的 Schema 边界

PrimaryStore 是唯一业务 Schema 校验入口：

```text
字段是否属于 Dataset
Field ID 是否重复
TypedValue 是否匹配字段 value_type
行数、字段数和公共请求大小限制
```

DataNode 不保存 Dataset Manifest，不理解 Field 类型，也不比较 Schema Revision。它只校验 Node/Dataset/RowKey、TypedValue Protobuf 结构、重复 Field ID、Batch 大小和最终 JetStream Payload，并负责原子 Merge。

DataNode 写 RPC 只允许唯一 PrimaryStore 的 Service HMAC 身份调用。普通服务和浏览器不能绕过 PrimaryStore 写入。`schema_revision` 不进入 DataNode 写协议、Pebble 事实或 RowsCommitted 事件。

## 事实写入与 DataNode

### 写入流程

```text
Caller
  -> PrimaryStore
       1. 从 Runtime Catalog 获取 Dataset 和 DataNode
       2. 校验批次、字段和值类型
       3. direct tRPC MergeRows
  -> DataNode
       4. 校验 node_id、dataset_id、RowKey 和通用 TypedValue 结构
       5. 在 Pebble 中读取旧行并 Merge
       6. 编码完整 RowsCommitted
       7. 校验最终 JetStream Payload 大小
       8. 原子提交事实、Sequence、Dataset Progress 和 Outbox
```

PrimaryStore 是 Field ID 和业务类型的唯一正确性边界。DataNode 是 Merge 原子性、通用数据结构和最终 Payload 的正确性边界。

事实协议不混合“当前状态”和“删除指令”：

```text
FactRow
  key
  columns
  attributes
```

协议删除 `attributes_to_delete`、`removed_column_names` 和 `removed_columns`。Merge 中没有出现的字段和 Attribute 保持原值；调用方不能把某个既有值单独恢复为缺失。需要删除时只能使用 `DeleteRows` 删除完整事实行。

事实行、ViewIndex 行和字段 Tombstone 都不保存 `source_node_id` 或 `source_sequence`。事件来源只由 `RowsCommitted` 顶层的 `{node_id, dataset_id, sequence}` 表达；消费顺序和防旧事件回写由物理 ViewIndex 的 Source Checkpoint 负责。

### Sequence

每个 DataNode 维护一个全局连续 `node_sequence`，所有本节点 Dataset 共用。每个 Dataset 同时保存一个 `DatasetProgress.last_committed_sequence`，表示该 Dataset 最近一次成功提交所使用的 Node Sequence。

```text
sequence 100 -> kline_1m
sequence 101 -> trade_tick
sequence 102 -> kline_1m
sequence 103 -> factor_value

kline_1m last_committed_sequence     = 102
trade_tick last_committed_sequence   = 101
factor_value last_committed_sequence = 103
```

`node_sequence` 用于 Outbox 和同 DataNode 消费顺序。`last_committed_sequence` 用于 View Snapshot Barrier 和新鲜度判断。由于中间可能包含其他 Dataset 的事件，`last_committed_sequence` 与 View Checkpoint 的数值差不能解释为事件条数。

### 原子提交

以下内容必须在同一个 Pebble Batch 中提交：

```text
事实行
node_sequence
DatasetProgress.last_committed_sequence
RowsCommitted Outbox
```

DataNode 先编码最终 `MooxMessage`，再使用与 JetStream Publisher 相同的 Max Payload 做提交前检查。超限时整批零提交。

## JetStream 与 Outbox

`MOOX_STORAGE` 保持持久 JetStream：

```text
retention: interest
discard: new
```

消息只有在所有匹配 Durable Consumer ACK 后才能释放。达到 Max Bytes、Max Messages 或其他容量限制时，JetStream 拒绝新 Publish；DataNode 保留 Outbox 队首并按退避重试。Outbox 达到本地 Entries、Bytes 或 Oldest Age 上限后，DataNode 拒绝新的事实写入。

Outbox 只提供 inspect 和 retry，不提供 force-skip。无法处理的消息必须修复代码、配置或数据后重新部署；系统不通过跳过事件恢复绿色状态。

`RowsCommitted` 保留 TimeSeries 和 Record 两种明确 Payload，但通过一个逻辑 Storage Subject 集合消费。每条消息携带：

```text
node_id
dataset_id
sequence
完整 Merge 后事实行或 Delete Key
```

RowsCommitted 的行对象不重复携带 Node 或 Sequence。

## 活动 View 物化

`storage_view_active` 是固定 Durable Consumer。ViewBuilder 按 `node_id` 维护有界顺序 Lane：

- 同一 DataNode 的 Sequence 串行。
- 一个 DataNode 失败不阻塞其他 DataNode。
- 失败 Sequence 重投成功后 Lane 自动恢复。
- Handler 在 ViewIndex 持久化成功后才 ACK。

View 可以组合不同 DataNode 的 Dataset。正常 Merge 事件只把发生变化的 Dataset 映射成它拥有的 View 列，并用 `MERGE` 更新当前 Active Index。目标 View 行缺失时，Builder 通过 PrimaryStore 分别读取所有来源 Dataset，生成完整行后用 `REPLACE` 恢复。

DeleteRows 事件不生成字段删除列表。ViewBuilder 收到来源事实行删除后重新读取该 View 行的全部来源：Primary Dataset 已不存在时删除整条 View 行；Primary Dataset 仍存在时按 Metadata 中当前生效的 View 字段生成完整行并执行 `REPLACE`。Secondary Dataset 缺失的列自然不会进入完整行，因此不需要字段 Tombstone 或字段级删除协议。

每个物理 ViewIndex 独立保存 Source Checkpoint：

```text
{node_id, dataset_id, sequence}
```

Checkpoint 只为 View 实际依赖的 Dataset 更新。Source Sequence 可以跨过同 DataNode 其他 Dataset 的 Sequence。`ViewIndexSourceProgress` 只携带 `{node_id, dataset_id, sequence}`；ViewIndex 在事务内读取当前值，`sequence <= current` 时幂等成功且不修改数据，`sequence > current` 时原子提交行变更并把 Checkpoint 推进到该 Sequence。协议不再携带 `expected_last_applied_sequence`。

Consumer 对消息涉及的每个 ViewIndex 分别判断 Checkpoint：事件 Sequence 不大于当前值时，该 ViewIndex 将它视为已经持久化的重复或旧投递，不再修改 Index；其他尚未应用的 ViewIndex 仍正常处理。只有所有受影响 ViewIndex 都成功应用或幂等跳过后才 ACK 消息。该规则既处理普通 JetStream 重投，也保证 View 切换后 Active Consumer 可以安全跳过已经包含在新 Index 中的积压事件。

## View Schema 与在线重建

### 版本可见性

View 保存期望版本和当前在线版本：

```text
desired_view_revision
active_view_revision
active_index_id
```

新增 View 字段只修改 Desired Revision。新 Index 激活前，公开查询继续使用旧 Active Revision、旧列集合和旧 Index。新字段不能提前出现在查询 Schema 中。

### 固定 Consumer 和单 Build

`storage-view` 只使用两个固定 Consumer：

```text
storage_view_active
storage_view_rebuild
```

全系统同时只允许一个 View Build。没有 Build 时，Rebuild Consumer 直接 ACK。Build 开始前暂停 Rebuild Consumer，使随后到达的事件在 JetStream 中等待。

系统不创建每 Build Durable Consumer，不使用 Build Owner Lease，也不并行调度多个 Build。

### Snapshot 与 Catch-up

DataNode 保留批量 `GetDatasetProgress`，只供 View 重建和切换读取同一节点上多个 Dataset 的最新提交位置：

```proto
message GetDatasetProgressReq {
  string node_id = 1;
  repeated string dataset_ids = 2;
}
```

该接口不参与 Dataset 归属修改，也不承担迁移判断。

```text
1. 获取全局 Rebuild Lock
2. 暂停 Rebuild Consumer 并等待在途处理结束
3. 对每个来源 DataNode 创建 Pebble Snapshot
4. 从 Snapshot 读取各来源 `last_committed_sequence`
5. 使用 Snapshot 全量 REPLACE 新 Index；每个来源完成后把 Source Checkpoint 初始化为对应的 snapshot_last_committed_sequence
6. 恢复 Rebuild Consumer
7. 对每个来源丢弃 sequence <= snapshot_last_committed_sequence 的事件
8. 应用 snapshot_last_committed_sequence 之后的 MERGE/DELETE
9. 追平当前 Dataset Progress Vector
```

Snapshot 只在 DataNode 进程内存在，具有 TTL 和并发数量上限。Build 进程崩溃或 Snapshot 丢失后，本次 Build 直接 FAILED；系统删除未激活 Index，并从新 Snapshot 开始完整重建，不恢复旧 Cursor。

持久 Build 状态只保留：

```text
BUILDING
CATCHING_UP
FAILED
ACTIVE
```

### 无感切换

新 Index 基本追平后，系统执行有界切换：

```text
1. 暂停 Active Consumer，等待在途 Apply 完成
2. 读取最新 Dataset Progress Vector
3. Rebuild Consumer 追平该 Vector
4. 使用 Expected Old Revision 做幂等 CAS，在一个 Metadata 事务中切换 active_index_id 和 active_view_revision
5. storage-view 原子替换包含 Index、Revision 和 Columns 的 Active Handle
6. 恢复 Active Consumer
```

`max_switch_pause` 固定为 2 秒。2 秒内不能追平并进入 Metadata CAS 时，系统取消本次切换、恢复 Active Consumer，旧 Index 继续更新，新 Index 保留并稍后重试。一旦 CAS 已确认目标 Index 激活，系统必须完成本地 Handle 替换，不能再回退；若 RPC 结果不确定则读取 Metadata 判定目标是否已激活。`storage-view` 崩溃后按 Metadata 中的 Active Index 重建 Handle。

暂停的是增量 Consumer，不是 DataView 查询。重建和切换期间：

- 旧 Active Index 持续服务现有字段查询。
- 事实写入持续进入 DataNode 和 JetStream。
- 新字段只在原子切换后一次性可见。
- 已在旧 Index 上执行的查询继续完成。
- 新请求在切换后使用新 Index。
- Active Consumer 恢复后，对不大于新 Source Checkpoint 的积压消息直接 ACK，不回写旧状态。
- 旧 Index 等待在途查询归零并经过 Grace Period 后删除。

`ActiveHandle` 不保存 Source Checkpoints。物理 ViewIndex 是 Source Checkpoint 的唯一事实源；把进度复制到不可变查询 Handle 会形成很快过期的第二份状态。DataView 查询只从 Handle 获取 `index_id`、`revision` 和列集合，ViewBuilder 直接读取物理 ViewIndex Checkpoint。

因此调用方不会看到半成品 Index，也不会因重建收到暂停或错误。切换期间其他 View 最多产生不超过 2 秒的增量延迟，查询本身不中断。

## 内部 RPC 与安全

Storage 内部进程直接通过 tRPC 调用：

```text
storage-primary -> storage-node
storage-view    -> storage-primary
archive         -> storage-primary
```

每个调用方使用独立 Service HMAC 身份。DataNode 只接受 PrimaryStore 和受控维护身份；PrimaryStoreScan 只接受 ViewBuilder、Archive 和受控维护身份。浏览器只能通过 Admin Gateway 访问公开 Metadata、PrimaryStore 和 DataView 方法。

系统不为 Storage 内部 RPC 增加 Node Service Gateway Hop、方法路由表或回退直连逻辑。

## Dataset 节点变更

MooX 不实现 Dataset 迁移。Dataset 创建后，Metadata API 永久拒绝修改 `data_node_id`。系统不提供复制、校验、切换、回滚、双写或迁移命令。

用户直接停机、复制文件或修改 SQLite 属于系统外人工操作。MooX 不校验该操作，也不承诺操作期间和操作后的可用性、一致性或可恢复性。

## 故障语义

| 故障 | 行为 |
| --- | --- |
| 单个 DataNode 离线 | 只影响归属于该节点的 Dataset；其他节点继续服务 |
| storage-primary 离线 | 事实读写和 Metadata 管理暂停；已打开的 View 查询可继续 |
| storage-view 离线 | View 查询暂停；事实写入继续，JetStream 保留事件 |
| JetStream 离线或满 | Outbox 重试；达到本地背压上限后事实写入明确失败 |
| Active Apply 瞬时失败 | JetStream NAK；相同 Sequence 成功重投后 Lane 恢复 |
| View Build 失败 | 旧 Active Index 继续服务；新 Index 标记 FAILED |
| View 切换超过 2 秒 | 取消切换并恢复 Active Consumer，稍后重试 |
| Active 切换后收到旧事件 | 不修改新 Index；按 Source Checkpoint 幂等 ACK |
| 追加字段 | Add Field 在 SQLite Commit 和 DatasetSchema 指针替换后才返回成功；在途旧请求继续 |
| 修改或删除已有字段定义 | Metadata 返回 `FIELD_IMMUTABLE`，运行时 Schema 不变 |
| 单独删除事实字段值或 Attribute | 协议不支持；调用方只能 Merge 新值或 DeleteRows 删除完整事实行 |
| Outbox 永久错误 | 保持失败和背压，等待用户修复；不跳过消息 |

## 错误码

至少提供以下稳定类型化错误：

```text
DATA_NODE_UNAVAILABLE
DATASET_NOT_ASSIGNED
FIELD_IMMUTABLE
FIELD_NOT_FOUND
FIELD_TYPE_MISMATCH
BATCH_TOO_LARGE
OUTBOX_BACKPRESSURE
SNAPSHOT_NOT_FOUND
VIEW_BUILD_IN_PROGRESS
VIEW_SWITCH_TIMEOUT
```

RPC 只返回稳定安全消息。日志记录底层 Cause、Node ID、Dataset ID、Sequence 和 Build ID；代码不通过 `strings.Contains(err.Error())` 分类错误。

## 两服务器 E2E

仓库本地 `custom.toml` 已配置两台测试服务器的连接信息。E2E 只读取该文件，不把账号、密码、Token 或展开后的连接命令写入 Git、日志、测试快照和错误消息。

推荐拓扑：

```text
Server A
  EventBus
  storage-primary
  storage-node-market
  storage-view

Server B
  storage-node-factor
```

E2E 使用独立测试目录、端口和 Dataset ID：

1. 把 K 线 Dataset 部署到 Server A。
2. 把因子 Dataset 部署到 Server B。
3. 持续向两个 Dataset 写入数据。
4. 创建跨节点 K 线 + 因子 View。
5. 在旧 Active Index 上持续发起查询并记录错误率。
6. 在持续写入期间为 Dataset 动态追加字段，不重启任何进程。
7. 更新 View Desired Revision 并触发后台重建。
8. 验证重建期间旧字段查询零错误、零半成品响应。
9. 验证切换后新字段原子可见且数据与事实源一致。
10. 注入超过 2 秒的切换延迟，验证自动回退。
11. 切换后恢复 Active Consumer 的旧事件积压，验证不会回写新 Index。
12. 停止 Server B DataNode，验证 Server A Dataset 继续读写。
13. 停止 JetStream，验证 Outbox 保留、恢复后按序补发。

E2E 不修改两台服务器的现有业务目录和服务。测试完成后删除独立测试进程、目录和临时 Metadata；清理命令不得包含从 `custom.toml` 展开的敏感信息。

## 验收标准

1. K 线和因子 Dataset 可以部署在不同 DataNode，且每个 Dataset 只有一个 Owner。
2. PrimaryStore 只按 `Dataset.data_node_id` 路由，不存在 Hash、权重或跨 Target 聚合。
3. 所有字段均可缺失，不存在 Required 字段属性和校验分支。
4. 已有字段删除、Field ID 变化和类型变化全部被拒绝。
5. 新增字段在 Add Field 成功返回后动态生效，不暂停写入、不重新部署。
6. DataNode 原子提交事实、Node Sequence、Dataset Progress 和 Outbox。
7. 跨 DataNode View 的稳态 MERGE、缺行 REPLACE 和整行 DELETE 正确；协议不存在字段级删除。
8. 活动消费按 DataNode 保序，单节点失败不阻塞其他节点。
9. 全系统同时只运行一个 View Build，并只使用两个固定 View Consumer。
10. View 重建期间旧 Active Index 持续提供无错误查询。
11. 新字段只在 Active Index 和 Active Revision 原子切换后可见。
12. 最终切换暂停不超过 2 秒；超时自动恢复旧 Active 更新。
13. Source Checkpoint 只保存在物理 ViewIndex；Active Consumer 的旧积压只产生幂等 ACK。
14. Build 崩溃后从头重建，不恢复旧 Snapshot、Lease 或 Cursor。
15. Runtime Catalog 的 Routing 启动后不变，DatasetSchema 支持原子热追加。
16. Storage 内部服务使用直接 tRPC，不经过 Node Service Gateway。
17. 两服务器 E2E 覆盖跨节点事实、动态字段、View 重建、切换回退、旧事件重投、节点故障和 Outbox 恢复。

## 明确不实现

- Dataset 内分片、Hash Pool、权重和 Rendezvous Hash。
- DataNode 副本、Leader Election、自动 Failover 和 Quorum。
- Dataset 迁移、复制、校验、双写、自动回滚和 Rebalance。
- 多 PrimaryStore、多 storage-view 和多 Build 并行。
- Required 字段、Schema Fence、Schema Revision、字段迁移和兼容读写。
- 单字段事实值删除、Attribute 单项删除、字段 Tombstone 和行级来源 Sequence。
- 每 Build JetStream Consumer、Build Lease 和可恢复 Snapshot Cursor。
- Metadata 通用 Cache、全量 Snapshot Refresh 和无界对象缓存。
- Storage 内部 Node Service Gateway Hop。
- Outbox force-skip 和静默事件丢弃。
