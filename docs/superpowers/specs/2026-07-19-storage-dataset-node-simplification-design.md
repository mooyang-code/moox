# Storage Dataset 单归属与多进程简化设计

## 状态与优先级

本设计已于 2026-07-19 完成讨论确认。它保留 Storage 的事实原子提交、JetStream、跨 Dataset View 和 A/B Index 能力，但以个人量化交易系统的长期可维护性为第一原则，删除 Dataset 内分片、高可用、在线迁移和动态 Schema 协调。

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
3. 用重新部署和重新计算替代运行时兼容、恢复和迁移状态机。
4. 默认多进程，同一套进程既可部署在一台机器，也可分布到多台机器。
5. 不为未来可能出现的 Dataset 内分片预留抽象。
6. 所有错误显式返回；系统不静默降级、不跳过事件。

## 业务不变量

### Dataset 单归属

1. 每个 Dataset 在任一时刻只属于一个 DataNode。
2. 一个 DataNode 可以承载多个 Dataset。
3. Dataset 不按 Subject、RowKey、时间或权重继续分片。
4. Dataset 的公开读写请求只包含一个 `dataset_id`，不提供跨 Dataset 原子批次。
5. 变更 Dataset 所属节点需要停写、复制、校验、切换和重新部署；系统不实现在线迁移、双写或自动回滚。

### Append-only Schema

MooX 的业务只追加字段，不改变已有字段定义：

1. 已有字段永不删除。
2. 已有字段的 `field_id` 永不修改，也不会被其他字段复用。
3. 已有字段的 `value_type` 永不修改。
4. 已有字段的 Required 属性永不修改。
5. Schema 变更只允许追加 Optional 字段。
6. Required 字段只允许在 Dataset 创建时定义。
7. 系统不实现字段删除、字段重命名、类型迁移、双读或双写兼容。

显示名称、描述等非 Schema 展示信息可以修改，但不能改变 `field_id`、类型、Required 或数据语义。

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

Dataset 直接保存归属节点和 Schema Revision，不单独建立 `DatasetPlacement`：

```text
Dataset 新增
  data_node_id
  schema_revision
```

字段字典和列约束继续由现有 Field、DatasetColumn 模型表达，不把它们重复嵌入 Dataset。PrimaryStore 读取 Dataset 时组装 Schema，并同时获得目标节点。`data_node_id` 在 Dataset 有数据后不能在线修改。

### 不可变 Runtime Catalog

`storage-primary` 启动时从 SQLite 加载写路径需要的不可变 Catalog：

```text
RuntimeCatalog
  datasets[dataset_id] -> schema_revision, fields, data_node_id
  data_nodes[node_id]  -> service_target, status
```

写路径只读取该 Catalog，不使用 TTL、后台 Refresh 或通用 Metadata Cache。Schema、字段、DataNode 或 Placement 发生变化时，当前进程把相应 Dataset 标记为 `RESTART_REQUIRED` 并拒绝后续写入。用户重新部署后，进程加载新 Catalog。

其他低频 Metadata CRUD 和 List 直接访问 SQLite。List 使用 SQL `ORDER BY + LIMIT + OFFSET`，禁止先全表加载再在内存分页。ArchiveFile、Build History 和审计记录不进入全量 Cache。

## Schema 生命周期

### Metadata 更新

Schema 更新接口只接受 Optional 字段追加。它逐项比较旧 Schema：

- 旧 Field ID 必须仍然存在。
- 旧字段的 Type 和 Required 必须完全相同。
- 新 Field ID 必须从未使用。
- 新字段必须为 Optional。

更新成功后 `schema_revision + 1`。当前 Runtime Catalog 不热更新；PrimaryStore 对该 Dataset 返回 `SCHEMA_RESTART_REQUIRED`。

### DataNode Dataset Manifest

DataNode 在 Pebble 中持久化每个本地 Dataset 的 Manifest：

```text
DatasetManifest
  dataset_id
  schema_revision
  fields
```

PrimaryStore 重启后，在该 Dataset 的第一次写入前调用幂等 `EnsureDatasetManifest`。DataNode 只接受新 Dataset 或严格 Append-only 的 Revision 升级；任何删除、类型变化、Field ID 变化或 Required 变化都失败。

普通写请求只携带 `dataset_id + schema_revision + rows`。DataNode 比较整数 Revision，不接收 Schema Hash，也不在每次写入中接收完整 Column Constraints。

若 PrimaryStore 与 DataNode 没有同时重新部署，DataNode 返回 `SCHEMA_REVISION_MISMATCH`。PrimaryStore 可以清除本进程的 Manifest 确认标记并重试一次 `EnsureDatasetManifest`；第二次仍不一致则直接返回错误，不进入循环重试。

## 事实写入与 DataNode

### 写入流程

```text
Caller
  -> PrimaryStore
       1. 从 Runtime Catalog 获取 Dataset 和 DataNode
       2. 校验批次、字段和值类型
       3. 确认 DataNode Dataset Manifest
       4. direct tRPC MergeRows
  -> DataNode
       5. 校验 node_id、dataset_id 和 schema_revision
       6. 在 Pebble 中读取旧行并 Merge
       7. 校验 Merge 后 Required 和 TypedValue
       8. 编码完整 RowsCommitted
       9. 校验最终 JetStream Payload 大小
      10. 原子提交事实、Sequence、Dataset Head 和 Outbox
```

DataNode 是 Merge 后完整行和最终 Payload 的正确性边界。PrimaryStore 的校验用于尽早返回友好错误，但不能代替 DataNode 在 Pebble 原子提交前的检查。

### Sequence

每个 DataNode 维护一个全局连续 `node_sequence`，所有本节点 Dataset 共用。每个 Dataset 同时保存最后一次提交的 `dataset_head_sequence`。

```text
sequence 100 -> kline_1m
sequence 101 -> trade_tick
sequence 102 -> kline_1m
sequence 103 -> factor_value

kline_1m head     = 102
trade_tick head   = 101
factor_value head = 103
```

`node_sequence` 用于 Outbox 和同 DataNode 消费顺序。`dataset_head_sequence` 用于 View Snapshot Barrier 和新鲜度判断。由于中间可能包含其他 Dataset 的事件，两个 Head 的数值差不能解释为事件条数。

### 原子提交

以下内容必须在同一个 Pebble Batch 中提交：

```text
事实行
node_sequence
dataset_head_sequence
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
schema_revision
完整 Merge 后事实行或 Delete Key
```

## 活动 View 物化

`storage_view_active` 是固定 Durable Consumer。ViewBuilder 按 `node_id` 维护有界顺序 Lane：

- 同一 DataNode 的 Sequence 串行。
- 一个 DataNode 失败不阻塞其他 DataNode。
- 失败 Sequence 重投成功后 Lane 自动恢复。
- Handler 在 ViewIndex 持久化成功后才 ACK。

View 可以组合不同 DataNode 的 Dataset。正常事件只把发生变化的 Dataset 映射成它拥有的 View 列，并用 `MERGE` 更新当前 Active Index。目标 View 行缺失时，Builder 通过 PrimaryStore 分别读取所有来源 Dataset，生成完整行后用 `REPLACE` 恢复。

ViewIndex Source Checkpoint 使用：

```text
{node_id, dataset_id, last_applied_sequence}
```

Checkpoint 只为 View 实际依赖的 Dataset 更新。Source Sequence 可以跨过同 DataNode 其他 Dataset 的 Sequence；CAS 要求 Expected 等于当前值且 Last 更大，不要求 `Last = Expected + 1`。

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

```text
1. 获取全局 Rebuild Lock
2. 暂停 Rebuild Consumer 并等待在途处理结束
3. 对每个来源 DataNode 创建 Pebble Snapshot
4. 从 Snapshot 读取各来源 dataset_head_sequence
5. 使用 Snapshot 全量 REPLACE 新 Index
6. 恢复 Rebuild Consumer
7. 对每个来源丢弃 sequence <= snapshot_head 的事件
8. 应用 snapshot_head 之后的 MERGE/DELETE
9. 追平当前 Dataset Head Vector
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
2. 读取最新 Dataset Head Vector
3. Rebuild Consumer 追平该 Vector
4. 在一个 Metadata 事务中切换 active_index_id 和 active_view_revision
5. storage-view 原子替换 Active Index Handle
6. 恢复 Active Consumer
```

`max_switch_pause` 固定为 2 秒。2 秒内不能完成时，系统取消本次切换、恢复 Active Consumer，旧 Index 继续更新，新 Index 保留并稍后重试。

暂停的是增量 Consumer，不是 DataView 查询。重建和切换期间：

- 旧 Active Index 持续服务现有字段查询。
- 事实写入持续进入 DataNode 和 JetStream。
- 新字段只在原子切换后一次性可见。
- 已在旧 Index 上执行的查询继续完成。
- 新请求在切换后使用新 Index。
- 旧 Index 等待在途查询归零并经过 Grace Period 后删除。

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

## Dataset 离线迁移

本设计不实现在线迁移。操作流程为：

```text
1. 停止目标 Dataset 的写入
2. 等待 DataNode Outbox 发布完成
3. 为源 Dataset 创建一致 Snapshot 或停止源 DataNode
4. 复制 Dataset Keyspace 到目标 DataNode
5. 校验行数、校验和、Head 和抽样值
6. 更新 Dataset.data_node_id
7. 重新部署 storage-primary 和相关 storage-node
8. 恢复写入
9. 重建受影响 View
```

迁移失败由用户恢复旧 Placement 和备份。系统不双写、不自动回滚，也不在运行时修改 Placement。

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
| Schema 在运行期变化 | 该 Dataset 写入返回 `SCHEMA_RESTART_REQUIRED` |
| Primary/DataNode Revision 不同 | 写入返回 `SCHEMA_REVISION_MISMATCH` |
| Outbox 永久错误 | 保持失败和背压，等待用户修复；不跳过消息 |

## 错误码

至少提供以下稳定类型化错误：

```text
DATA_NODE_UNAVAILABLE
DATASET_NOT_ASSIGNED
SCHEMA_RESTART_REQUIRED
SCHEMA_APPEND_ONLY_VIOLATION
SCHEMA_REVISION_MISMATCH
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
6. 为 Dataset 追加 Optional 字段并重新部署相关进程。
7. 更新 View Desired Revision 并触发后台重建。
8. 验证重建期间旧字段查询零错误、零半成品响应。
9. 验证切换后新字段原子可见且数据与事实源一致。
10. 注入超过 2 秒的切换延迟，验证自动回退。
11. 停止 Server B DataNode，验证 Server A Dataset 继续读写。
12. 停止 JetStream，验证 Outbox 保留、恢复后按序补发。

E2E 不修改两台服务器的现有业务目录和服务。测试完成后删除独立测试进程、目录和临时 Metadata；清理命令不得包含从 `custom.toml` 展开的敏感信息。

## 验收标准

1. K 线和因子 Dataset 可以部署在不同 DataNode，且每个 Dataset 只有一个 Owner。
2. PrimaryStore 只按 `Dataset.data_node_id` 路由，不存在 Hash、权重或跨 Target 聚合。
3. 已有字段删除、Field ID 变化、类型变化和 Required 变化全部被拒绝。
4. 新增 Optional 字段在重新部署后可写入。
5. DataNode 原子提交事实、Node Sequence、Dataset Head 和 Outbox。
6. 跨 DataNode View 的稳态 MERGE、缺行 REPLACE 和 DELETE 正确。
7. 活动消费按 DataNode 保序，单节点失败不阻塞其他节点。
8. 全系统同时只运行一个 View Build，并只使用两个固定 View Consumer。
9. View 重建期间旧 Active Index 持续提供无错误查询。
10. 新字段只在 Active Index 和 Active Revision 原子切换后可见。
11. 最终切换暂停不超过 2 秒；超时自动恢复旧 Active 更新。
12. Build 崩溃后从头重建，不恢复旧 Snapshot、Lease 或 Cursor。
13. Metadata 写路径使用启动时不可变 Runtime Catalog；其他查询直接访问 SQLite。
14. Storage 内部服务使用直接 tRPC，不经过 Node Service Gateway。
15. 两服务器 E2E 覆盖跨节点事实、View 重建、切换回退、节点故障和 Outbox 恢复。

## 明确不实现

- Dataset 内分片、Hash Pool、权重和 Rendezvous Hash。
- DataNode 副本、Leader Election、自动 Failover 和 Quorum。
- 在线 Dataset 迁移、双写、自动回滚和 Rebalance。
- 多 PrimaryStore、多 storage-view 和多 Build 并行。
- 运行时 Schema 热更新、Schema Hash、字段迁移和兼容读写。
- 每 Build JetStream Consumer、Build Lease 和可恢复 Snapshot Cursor。
- Metadata 通用 Cache、全量 Snapshot Refresh 和无界对象缓存。
- Storage 内部 Node Service Gateway Hop。
- Outbox force-skip 和静默事件丢弃。
