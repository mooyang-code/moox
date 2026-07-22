# Storage Dataset 单归属与字段级存储简化设计

## 状态与优先级

本设计已于 2026-07-19 完成讨论确认。它是 Storage 当前实现的目标事实源，取代此前关于 DataShard、多 Consumer、Pebble 行级 Merge、不可变整行、字段级删除、Snapshot Scan、Sequence Progress、Dataset 迁移和通用 Schema 演进的设计。

DataNode 管理、Dataset 激活和绑定锁定规则已由《Storage DataNode 管理模型收敛设计》（2026-07-22）修订；两份文档冲突时，以 2026-07-22 设计为准。

MooX 是个人量化交易系统。本设计优先保证数据正确、边界清楚和长期可维护，不建设高可用、在线迁移和通用分布式存储平台。

## 设计原则

1. 一个 Dataset 只属于一个 DataNode，一个 DataNode 可以承载多个 Dataset。
2. 默认多进程；同一套进程可以部署在一台或多台机器。
3. 每个 Field 和 Attribute 独立保存为 Pebble Key，写入语义是字段级 Upsert。
4. DataNode 只提供精确 RowKey、精确 Field 读写，不提供普通扫描和删除接口。
5. Metadata SQLite 是配置事实源，进程通过现有 `snapshotcache` 定时加载不可变内存快照。
6. 每个 Dataset 使用独立 NATS Subject，不定义 Dataset 或 DataNode 之间的业务顺序。
7. View 只有一个 JetStream Consumer；同一 Dataset 的事件按 Subject 发布顺序串行应用。
8. View 重建只使用旧 ActiveView 的 RowKey，不扫描 DataNode。
9. 重建期间同一实时事件同时写 ActiveView 和 NewView；低频 Backfill 只在实时队列空闲时运行。
10. Pebble 与 View 的保存时长分别配置，互不联动。

## 业务不变量

### Dataset 单归属

- Dataset 创建时必须指定 `data_node_id`。
- Dataset 首次激活前可以通过专用命令原子更换 `data_node_id`；首次激活后永久不可修改。
- Dataset 不按 Subject、RowKey、时间范围或权重继续分片。
- 系统不提供 Dataset 迁移、复制、切换、回滚或 Rebalance。
- 用户的停机复制和重新部署属于系统外人工操作。

### Metadata 预注册

以下信息必须先在 Metadata 注册，才能进行业务读写：

- Space、Dataset、DataNode；
- Field、DatasetColumn；
- TimeSeries Subject 和 Frequency；
- View、ViewColumn 和 View Grain。

Record 的 `record_id` 和 `version` 不在 Metadata 枚举。Record View 的 RowKey 集合由已有 ActiveView 保存。

### Append-only Schema

- 已有 Field 永不删除。
- `field_id` 永不修改、复用或迁移。
- `value_type` 永不修改。
- 所有 Field 都允许缺失，不存在 Required 字段。
- Schema 变更只允许为 Dataset 追加 Field。
- 新增 Field 不要求重启进程；Metadata Cache 刷新后即可写入。
- 新增 Field 可以追溯补全历史 RowKey，例如新增因子字段后回填历史时点。

### 字段级 Upsert

TimeSeries RowKey 是：

```text
space_id + dataset_id + subject_id + freq + data_time
```

Record RowKey 是：

```text
space_id + dataset_id + record_id + version
```

`WriteFields` 对每个 `RowKey + FieldID` 或 `RowKey + AttributeKey` 直接执行 Upsert：

- Key 不存在时新增；
- Key 已存在时覆盖旧值；
- 一次请求可以只写部分 Field；
- 可以为历史 RowKey 补写新增 Field；
- 不提供字段删除、Attribute 删除或 DeleteRows；
- 重复写入相同值允许产生新的 Outbox 事件，消费端 Upsert 后结果不变。

DataNode 不保存 RowMarker，不计算整行 `content_hash`，也不返回 `FACT_VERSION_IMMUTABLE`。

### Record Version

- Record 写入时 `version` 必填，服务端不生成默认 Version。
- 读取指定 Version 时返回该版本。
- 读取未指定 Version 时，返回同一 `record_id` 下按 UTF-8 字节顺序最大的 Version。
- 系统不解析数字、SemVer 或业务时间。
- 用户应使用 ISO 时间、定长补零数字或其他字符顺序与业务顺序一致的格式。

例如 `00000000000000000042` 可正确排序；`1, 2, 10` 不可得到数字顺序。

### TimeSeries 无 Dimensions

TimeSeries 不定义 Dimensions。身份只由 `subject_id + freq + data_time` 决定：

- 参与 Schema 的数据放普通 Field；
- 不参与 Schema 的附加信息放 Attribute；
- Attribute 不参与 RowKey、View Grain 或时间桶计算。

## 进程架构

```text
Browser
  -> Admin Gateway
       |-> storage-primary
       `-> storage-view

storage-primary
  Metadata SQLite v4
  Metadata snapshotcache
  PrimaryStore
       | direct tRPC + Service HMAC
       |-> storage-node-market -> Pebble + Outbox
       `-> storage-node-factor -> Pebble + Outbox

storage-node-*
  -> JetStream MOOX_STORAGE
       |-> storage_view
       |-> Archive
       `-> Factor

storage-view
  Single View Consumer
  ViewBuilder
  DataView
  ViewIndex A/B
```

`storage-primary` 和 `storage-view` 各运行一个实例；每个 `node_id` 只有一个写 Owner。不实现 Leader Election、Replica、Quorum 或自动 Failover。

Storage 内部 RPC 直接访问 `service_target`，不经过 Node Service Gateway。DataNode 写接口只允许 `storage-primary` 身份，ViewIndex 内部接口不进入 Admin Gateway。

## 最终命名

Storage 不保留 Shard 概念：

| 旧名 | 最终名 |
| --- | --- |
| `DataShard` | `DataNode` |
| `datashard` | `datanode` |
| `data_shard.proto` | `data_node.proto` |
| `storage-shard` | `storage-node` |
| `role: shard` | `role: node` |
| `shard_id` | `node_id` |
| `FactKey` | `RowKey` |
| `keep_days` | `keep_duration` |
| `source_shard_id/source_sequence` | 删除 |
| `node_sequence/DatasetProgress` | 删除 |
| `storage_shard_*` | `storage_node_*` |

代码、Proto、配置、Seed、CLI、指标、测试和现行文档必须一次性更新，不保留 Alias、Deprecated RPC 或旧 YAML 字段。

## Metadata Cache

Metadata 只接受 Schema v4。版本检查直接执行：

```go
if version != metadataSchemaVersion {
    return errIncompatibleMetadataSchema
}
```

删除 `metadataSchemaVersionCompatible` 和所有兼容版本分支。版本错误时启动失败，由用户清理数据库并重新部署。

核心模型：

```text
DataNode
  node_id
  service_target
  status

Dataset
  data_node_id
  keep_duration
  binding_locked
  revision

View
  keep_duration
  desired_view_revision
  active_view_revision
  active_slot
```

`keep_duration` 是可解析的持续时间：

- `Dataset.keep_duration` 只用于 TimeSeries Pebble 数据；`0` 表示永久保存；
- Record 不自动清理，Record Dataset 的 `keep_duration` 必须为 `0`；
- `View.keep_duration` 只用于 TimeSeries View；`0` 表示永久保存；
- Record View 的 `keep_duration` 必须为 `0`；
- Dataset 与 View 的 `keep_duration` 独立配置，系统不比较、不调整、不联动。

Proto 和 SQLite 都保存规范化 Duration 字符串，例如 `90m`、`24h`、`4320h`；`0` 是唯一的永久保存值。服务入口使用 `time.ParseDuration` 校验并规范化。

`storage-primary` 复用现有 `modules/storage/internal/service/metadata/cache`：

```text
SQLite Metadata
  -> snapshotcache Source 定时全量加载
  -> 原子发布不可变 Snapshot
  -> PrimaryStore 按索引读取 Dataset、Field、DataNode 和 View
```

不新增 Runtime Catalog、DatasetSchema Clone 或单 Dataset 原子指针。Metadata CRUD 提交后可以主动 `Refresh` 缩短本进程生效时间；其他进程通过配置的刷新周期看到新快照。刷新失败继续使用上一份完整快照，不发布半快照。

## Pebble 字段级存储

### 物理 Key

每个 Field 和 Attribute 独立保存。`value_kind` 使用固定单字节命名空间：

```text
0x01 = Dataset Field
0x02 = Attribute
```

Field 和同名 Attribute 因命名空间不同而不会冲突。

TimeSeries 使用 UTC 时间桶。桶宽由维护配置决定，不绑定一天：

```text
time_series
| space_id
| dataset_id
| bucket_start
| subject_id
| freq
| data_time
| value_kind
| field_or_attribute_id
```

Record 不做自动清理，不使用时间桶：

```text
record
| space_id
| dataset_id
| record_id
| version
| value_kind
| field_or_attribute_id
```

实际实现使用保持字节排序的长度前缀二进制 Tuple Codec，不继续使用字符串分隔和手工 `%` 转义。

### WriteFields

```text
PrimaryStore
  1. 从 Metadata Cache 获取 Dataset、Fields 和 DataNode
  2. 校验 RowKey、Field 归属、重复 Field、TypedValue 和请求上限
  3. direct tRPC 调用 DataNode.WriteFields

DataNode
  4. 将每个 Field/Attribute 编码为独立 Pebble Key
  5. 编码 DatasetFieldsChanged 和 MooxMessage
  6. 校验最终 JetStream Payload
  7. 分配内部 outbox_id
  8. 在一个 Pebble Batch 中 Upsert Field/Attribute 并写入 Outbox
```

DataNode 不读取 Metadata，不理解 Field 类型，也不组装完整业务行。

### ReadFields

DataNode 不提供“读取完整行”的 RPC。每次读取必须指定 Field：

```proto
message ReadFieldsReq {
  string node_id = 1;
  string dataset_id = 2;
  repeated RowKey keys = 3;
  repeated string field_ids = 4;
  repeated string attribute_keys = 5;
}
```

`field_ids` 必须非空。Response 对每个 RowKey 只返回请求字段中实际存在的值，不返回依赖 RowMarker 的 `row_exists`。

上层需要当前 Dataset 的全部生效字段时，由 PrimaryStore 从 Metadata Cache 取得 Field ID 集合，再分批调用 DataNode。DataNode 不提供普通 `ReadRows`、`ScanRows`、`ScanNodeSnapshot`、`GetDatasetProgress` 或 `DeleteRows`。

Record 未指定 Version 时，DataNode 在已知 `record_id` 的 Prefix 内使用反向 Seek 返回字符顺序最大的 Version；这不是对外范围扫描。

### 原子提交与 Outbox

每次 `WriteFields` 在一个 Pebble Batch 中提交：

```text
Field Keys
Attribute Keys
__outbox/<outbox_id>
__meta/next_outbox_id
```

`outbox_id` 是 DataNode 内部递增编号，只用于按 Pebble Key 顺序读取和删除 Outbox。它不写入业务事件，不对 RPC 暴露，也不参与 View 幂等或重建。

每个 DataNode 只有一个 Outbox Relay。Relay 按 `outbox_id` 逐条同步发布，发布失败立即停止，后面的条目不得越过失败条目；只删除连续发布成功前缀。

`DatasetFieldsChanged` 携带：

```text
space_id
dataset_id
本次 Upsert 的 RowKey、Field 和 Attribute
```

事件不携带 `node_sequence`、`source_sequence` 或 Dataset Progress。

## 数据保存与磁盘清理

### Pebble

`storage-primary` 根据 TimeSeries Dataset 的 `keep_duration` 和配置的桶宽计算完整过期桶，调用 DataNode 内部 `CleanupExpiredBuckets`。DataNode 对整个桶执行 `DeleteRange`，并在后台对删除范围 Compact。

清理是物理维护，不属于业务字段变更：

- 不生成 DatasetFieldsChanged；
- 不修改 Outbox ID 之外的任何业务进度；
- 不提供给普通外部调用方；
- Record 永不自动清理。

### ViewIndex

每个 View 使用两个独立物理槽位：

```text
<view-id>/slot-a.duckdb
<view-id>/slot-b.duckdb
```

Bleve 使用对应的 `slot-a/` 和 `slot-b/` 目录。Metadata 的 `active_slot` 决定当前角色。DuckDB/View 清理不调用 Pebble 清理；它只在 A/B 重建时按 `View.keep_duration` 过滤旧 ActiveView RowKey，切换成功后删除整个 OldView DB。

增加 `keep_duration` 不会恢复已经从 ActiveView 删除的 RowKey。需要恢复历史时由用户重新写入字段。

## JetStream Subject 与单 Consumer

所有 Dataset Subject 共用一个 Stream：

```text
Stream: MOOX_STORAGE
Subjects:
  - moox.storage.fields_changed.v1.>
```

`MOOX_STORAGE` 使用 JetStream `InterestPolicy` 和 `DiscardNew`。

每个 Dataset 使用独立 Subject：

```text
moox.storage.fields_changed.v1.<space-token>.<dataset-token>
```

`space-token` 和 `dataset-token` 使用统一的 `EncodeSubjectToken`：把非空 UTF-8 ID 编码为小写、无 Padding 的 Base32 单个 NATS Token。Consumer 使用 `DecodeSubjectToken` 解码，并校验 Subject 中的 Space/Dataset 与 Payload 一致。

同一 Dataset 只由其唯一 DataNode 发布，因此该 Subject 内的发布顺序就是 Dataset 写入顺序。不同 Dataset 和不同 DataNode 之间不定义顺序。Archive、Factor 可以订阅明确 Dataset Subject；View 订阅通配符。

这里的“有序”只在单 Dataset Subject 内成立：DataNode 在同一提交中先落 Field/Attribute，再落 Outbox，Relay 按 Outbox ID 发布；由于 Dataset 不迁移且只有一个 DataNode 发布者，不需要额外的全局 `node_sequence` 或跨 Dataset Sequence Lane。多个 DataNode 并发发布不同 Dataset 不会互相阻塞，也不需要比较它们的序号。虽然多个 Subject 共用一个 Stream，Stream 的交错顺序不作为业务依据；单 `storage_view` Consumer 的 `MaxAckPending=1` 只控制当前消息的处理/ACK 生命周期。

View 只创建一个固定 Durable Consumer：

```text
Durable: storage_view
FilterSubject: moox.storage.fields_changed.v1.>
MaxAckPending: 1
FetchBatch: 1
```

处理完当前事件并 ACK 后才获取下一条。ActiveView 临时失败时不释放当前 Delivery，也不获取下一条；Handler 对同一 Delivery 执行带 Backoff 的本地重试并定期发送 `InProgress`，进程退出后由 JetStream 重投未 ACK 消息。字段级 Upsert 使重复投递结果不变；Publisher 同时设置稳定 `Nats-Msg-Id`，减少“Broker 已持久化但确认丢失”导致的重复消息。

不创建 Node/Dataset Sequence Lane、ViewIndex Source Progress、`storage_view_active`、`storage_view_rebuild` 或每 Build Consumer。Outbox 只允许 inspect 和 retry，不允许 force-skip。

## View A/B 重建

### 角色生命周期

```text
重建前：slot-a = ActiveView，slot-b = 空
重建中：slot-a = ActiveView，slot-b = NewView
切换后：slot-b = ActiveView，slot-a = OldView
清理后：slot-b = ActiveView，slot-a = 空
```

下一次重建反向复用空槽位。

### 首次创建和关联约束

TimeSeries 和 Record View 都必须在主 Dataset 开始写入前创建。首次创建空 ActiveView，后续 RowKey 由实时事件持续加入。

系统不从 DataNode 枚举历史 RowKey。A/B 重建继承旧 ActiveView 的 RowKey 集合，不能修复旧 ActiveView 已经缺失的 RowKey。View 新关联的 Dataset 必须作为共享同一 View Grain 的次级 Dataset；它按旧 ActiveView RowKey 精确读取字段，不单独产生新 View Row。

### 重建触发

`storage-view` 定时基于 Metadata Cache 和 ActiveView 状态执行 Reconcile：

1. Dataset 新增 Field，且关联 View 的 Desired Revision 发生变化；
2. View 新关联 Dataset；
3. TimeSeries ActiveView 覆盖时长超过 `2 * View.keep_duration`。

`keep_duration=0` 时不因时间范围触发重建。倍数固定为 2，不增加配置字段。重建生成的 NewView 只保留最近 `keep_duration`，因此切换后至少再积累约一个 `keep_duration` 才会再次触发。

状态为 BUILDING 或 FAILED 时不自动发起新 Build。FAILED 由用户处理后手动重建。

### 开始双写

ViewBuild 只保存：

```text
started_at
new_slot
status
backfilled_rows
safe_error
```

启动步骤：

```text
1. 创建并清空 NewView
2. 获取该 View 的 Apply 锁，等待当前实时 Apply 完成
3. 在 ActiveView 开启一致性只读事务
4. 原子发布 ViewWriteTargets{Active, New}
5. 释放 Apply 锁
6. 启动 Backfill 协程
```

安装双写目标之前的事件已经包含在 ActiveView 事务基线中；安装后的事件由同一个 Handler 同时写两个目标。不需要 Rebuild Consumer、Pebble Snapshot、Build Barrier、Sequence Progress 或 Event Catch-up。

### 实时写与 ACK

没有重建时：

```text
写所有受影响 ActiveView -> 全部成功后 ACK
```

重建时：

```text
写所有受影响 ActiveView -> 写正在重建 View 的 NewView -> 全部成功后 ACK
```

任一 ActiveView 写失败时保持当前 Delivery 未 ACK，并重试同一事件，不允许后续事件越过。所有 ActiveView 成功但 NewView 失败时：立即停止本次重建、标记 FAILED、移除 NewView、清理失败 DB，并正常 ACK 当前事件；ActiveView 继续服务。系统保存安全错误并等待用户手动重新触发，不自动重建。

### 空闲 Backfill

Backfill 使用启动时 ActiveView 的一致性只读事务：

1. 分页读取旧 ViewRowKey 和已有列；
2. TimeSeries 按 `started_at` 和 `View.keep_duration` 过滤；Record 不过滤；
3. 把已有列写入 NewView；
4. 从 Metadata Cache 获取当前 Revision 的新增 Field 和新关联 Dataset；
5. 使用旧 RowKey 和明确 Field ID 调用 DataNode.ReadFields；
6. 只填充 NewView 当前缺失的值。

实时事件优先。只要实时 View 事件排队或正在写入，Backfill 就不提交新 Batch。每个 Backfill Batch 最多 100 行或 50ms，完成后重新检查实时队列。已经开始的 DuckDB 事务不被强制中断；实时事件最多等待当前小 Batch 完成。

NewView 只有一个串行 Writer。实时写使用 `LIVE_WRITE`，Backfill 使用 `BACKFILL`；`BACKFILL` 只能填充缺失值，不能覆盖实时写已经提交的非空值。

Backfill 失败时停止本次重建、标记 FAILED、移除并清理 NewView；ActiveView 不受影响，用户手动重建。

### 原子切换

Backfill 完成后：

```text
1. 获取 View Apply 锁，等待当前实时 Apply 完成
2. 确认 Build 仍是 BUILDING 且 ViewWriteTargets 仍指向该 NewView
3. Metadata CAS 切换 active_slot 和 active_view_revision
4. 原子发布新的 ActiveHandle
5. 从 ViewWriteTargets 移除旧 ActiveView
6. 释放 Apply 锁
7. 等旧查询引用归零并经过 Grace Period
8. 删除 OldView DB
```

Apply 锁保证 Backfill 完成到切换之间没有只写 ActiveView 的事件，因此不需要比较 Source Progress。切换持锁时间最多 2 秒；超时前未进入 Metadata CAS 时恢复原状态并保留 NewView，等待用户再次触发切换。DataView 查询从请求开始到结束持有同一个不可变 ActiveHandle，因此不会看到半成品或跨 Slot 结果。

## 故障语义

| 故障 | 行为 |
| --- | --- |
| DataNode 离线 | 只影响该节点 Dataset；其他节点继续服务 |
| storage-primary 离线 | 事实读写和 Metadata 管理暂停；已打开 View 查询继续 |
| storage-view 离线 | View 查询暂停；事实写入继续，JetStream 保留事件 |
| Metadata Cache 刷新失败 | 继续使用上一份完整快照并报警 |
| JetStream 离线或满 | Outbox 重试；达到本地上限后写入明确失败 |
| ActiveView Apply 失败 | 同一 Delivery 本地退避重试并发送 InProgress；后续消息不获取 |
| NewView 实时写失败 | 当前重建 FAILED，Active 成功的事件 ACK，等待人工重建 |
| Backfill 失败 | 当前重建 FAILED，ActiveView 继续服务，等待人工重建 |
| Backfill 长期没有空闲时间 | 保持 BUILDING，不影响实时事件 |
| 切换超过 2 秒 | 不切换 Active，返回明确错误 |
| Schema 非 v4 | 启动失败，用户清理数据库后重新部署 |

## 两服务器 E2E

E2E 从仓库根被忽略的 `custom.toml` 读取两个服务器节点，不打印或提交凭证。

推荐拓扑：

```text
Server A: EventBus, storage-primary, storage-node-market, storage-view
Server B: storage-node-factor
```

必须验证：

1. K 线和因子 Dataset 分属两个 DataNode，并发布不同 Dataset Subject；
2. Subject Token 可逆，Subject 与 Payload 不一致时拒绝消费；
3. Field 和 Attribute 使用独立物理命名空间；
4. 同一 RowKey 可以新增字段、覆盖字段和补写历史因子值；
5. Record 空 Version 读取字符顺序最大版本；
6. 内部 Outbox ID 有序发布但不进入事件；
7. 单 Consumer 以 Batch 1、MaxAckPending 1 更新 ActiveView；
8. 重建开始后实时事件同时写 ActiveView/NewView；
9. 持续实时写入时 Backfill 暂停，空闲后继续；
10. NewView 或 Backfill 故障只终止 Build，ActiveView 继续并 ACK；
11. Dataset 新增 Field、View 新关联 Dataset 时触发重建；
12. ActiveView 覆盖时长超过 `2 * keep_duration` 时触发重建；
13. Pebble 时间桶清理与 DuckDB 重建分别触发、互不联动；
14. 切换期间查询零错误，新字段原子可见；
15. 切换后删除 OldView DB；
16. 无 DataNode Scan、Snapshot、Progress 或 DeleteRows RPC。

## 验收标准

- Metadata 只接受 Schema v4，代码中不存在 `metadataSchemaVersionCompatible`。
- Dataset 创建后不可修改 DataNode；不存在 DatasetPlacement 和迁移工具。
- Metadata 读取复用现有 `snapshotcache`，不存在独立 Runtime Catalog。
- 不存在 Required、Dimensions、字段删除、DeleteRows、ReadRows、ScanRows 或 Snapshot RPC。
- TimeSeries 使用可配置时间桶；Record 不自动清理。
- Field 使用 `0x01`，Attribute 使用 `0x02`，不存在 RowMarker。
- `WriteFields` 对 Field/Attribute 直接 Upsert，允许补写历史新增字段和覆盖旧值。
- DataNode `ReadFields` 必须指定 RowKey 和 Field；Record 空 Version 返回字符最大版本。
- 字段和内部 Outbox 条目在一个 Pebble Batch 中提交。
- 每个 Dataset 使用 `moox.storage.fields_changed.v1.<space-token>.<dataset-token>`。
- 事件不携带 Node/Dataset Sequence，ViewIndex 不保存 Source Progress。
- JetStream 只有一个 `storage_view` Durable Consumer，`MaxAckPending=1`、`FetchBatch=1`。
- 每个 View 使用独立 A/B DB；ActiveView/NewView/OldView 生命周期明确。
- Backfill 只在实时队列空闲时小批运行，不能覆盖实时值。
- NewView 或 Backfill 失败不阻塞 ActiveView，整次重建等待人工重试。
- Dataset Field、View Dataset 关联和 `2 * keep_duration` 能触发对应重建。
- TimeSeries/Record View 都从旧 ActiveView RowKey 重建，不扫描 DataNode。
- 本地门禁、两轮独立代码审查和两服务器 E2E 全部通过。

## 明确不实现

- Dataset 内分片、Replica、Leader Election、Quorum 和自动 Failover。
- Dataset 迁移、复制、双写、回滚和 Rebalance。
- Required、Dimensions、Schema Fence、Schema Revision 和旧协议兼容。
- RowMarker、整行 Content Hash、不可变 Fact 冲突和字段级删除。
- DataNode 普通范围扫描、Snapshot Scan、Progress RPC 和完整行读取。
- Record 自动过期清理。
- Node/Dataset Sequence、ViewIndex Source Progress 和顺序 Lane。
- 第二个 View Consumer、每 Build Consumer、Build Lease 和 Event Catch-up 状态机。
- 从 DataNode 枚举历史 RowKey，或修复旧 ActiveView 缺失的 RowKey。
- 自动重试失败的整次 View 重建。
