# Storage Dataset 单归属与字段级存储简化设计

## 状态与优先级

本设计已于 2026-07-19 完成讨论确认。它是 Storage 当前实现的目标事实源，取代此前关于 DataShard、多 Consumer、Pebble 行级 Merge、字段级删除、Snapshot Scan、Dataset 迁移和通用 Schema 演进的设计。

MooX 是个人量化交易系统。本设计优先保证数据正确、边界清楚和长期可维护，不建设高可用、在线迁移和通用分布式存储平台。

## 设计原则

1. 一个 Dataset 只属于一个 DataNode，一个 DataNode 可以承载多个 Dataset。
2. 默认多进程；同一套进程可以部署在一台或多台机器。
3. 事实版本不可修改，只能追加新版本。
4. 每个 Field 和 Attribute 独立保存为 Pebble Key。
5. DataNode 只提供精确 Key、精确 Field 读写，不提供普通扫描和删除接口。
6. View 重建只使用旧 ActiveView 的 Key 集合，不扫描 DataNode。
7. View 只有一个 JetStream Consumer；重建期间同一事件同时写 ActiveView 和 NewView。
8. 低频 Backfill 不影响实时事件；有实时事件时暂停提交新的 Backfill Batch。
9. Pebble 与 View 数据保存天数由用户分别配置，互不联动。
10. 所有错误显式返回；不静默丢事件，不自动重试失败的整次 View 重建。

## 业务不变量

### Dataset 单归属

- Dataset 创建时必须指定 `data_node_id`。
- `data_node_id` 创建后永久不可修改。
- Dataset 不按 Subject、RowKey、时间范围或权重继续分片。
- 系统不提供 Dataset 迁移、复制、切换、回滚或 Rebalance。
- 用户的停机复制和重新部署属于系统外人工操作。

### Metadata 预注册

以下信息必须先在 Metadata 注册，才能进行业务读写：

- Space、Dataset、DataNode；
- Field、DatasetColumn；
- TimeSeries Subject 和 Frequency；
- View、ViewColumn 和 View Grain。

Record 的 `record_id` 和 `version` 不在 Metadata 枚举。Record View 的 Key 集合由已有 ActiveView 保存。

### Append-only Schema

- 已有 Field 永不删除。
- `field_id` 永不修改、复用或迁移。
- `value_type` 永不修改。
- 所有 Field 都允许缺失，不存在 Required 字段。
- Schema 变更只允许为 Dataset 追加 Field。
- Add Field 在 SQLite Commit 和 Runtime Schema 指针替换后才返回成功。
- 新增 Field 不要求重启进程，但只适用于以后创建的新事实版本。

### 不可变事实版本

TimeSeries 的完整事实身份是：

```text
space_id + dataset_id + subject_id + freq + data_time
```

Record 的完整事实身份是：

```text
space_id + dataset_id + record_id + version
```

完整 FactKey 一旦创建，Field 和 Attribute 永久不变：

- 相同 FactKey、相同完整内容是幂等成功，不产生新 Sequence 和事件；
- 相同 FactKey、不同内容返回 `FACT_VERSION_IMMUTABLE`；
- 不能为旧版本补写一个原本缺失的 Field；
- 业务修正必须写入新的 `data_time` 或 `version`；
- 不提供字段级删除、Attribute 单项删除或整行 DeleteRows。

DataNode 的写 RPC 保持名为 `WriteFields`。接口注释必须明确告诉用户：它创建不可变事实版本，不是修改旧版本。

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
  Runtime Catalog
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
| `source_shard_id` | 删除 |
| `source_sequence` 行字段 | 删除 |
| `storage_shard_*` | `storage_node_*` |

代码、Proto、配置、Seed、CLI、指标、测试和现行文档必须一次性更新，不保留 Alias、Deprecated RPC 或旧 YAML 字段。

## Metadata 与 Runtime Catalog

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
  keep_days

View
  keep_days
  desired_view_revision
  active_view_revision
  active_slot
```

`keep_days` 的统一语义：

- `Dataset.keep_days` 只用于 TimeSeries Pebble 数据；`0` 表示永久保存；
- Record 不自动清理，Record Dataset 的 `keep_days` 必须为 `0`；
- `View.keep_days` 只用于 TimeSeries View；`0` 表示永久保存；
- Record View 的 `keep_days` 必须为 `0`；
- Dataset 与 View 的 `keep_days` 独立配置，系统不比较、不调整、不联动。

`storage-primary` 启动时加载：

```text
RuntimeCatalog
  datasets[dataset_id] -> data_node_id, data_kind, subjects, freqs
  data_nodes[node_id]   -> service_target, status
  schemas[dataset_id]   -> atomic DatasetSchema pointer
```

Routing 在运行期间不变。Field 追加时 Clone DatasetSchema 并原子替换指针；在途请求继续使用旧的不可变指针。低频 Metadata List 直接使用 SQLite `ORDER BY + LIMIT + OFFSET`，不保留通用全量 Cache。

## Pebble 字段级存储

### 物理 Key

每个 Field 和 Attribute 独立保存。`value_kind` 使用固定单字节命名空间：

```text
0x00 = RowMarker
0x01 = Dataset Field
0x02 = Attribute
```

Field 和同名 Attribute 因命名空间不同而不会冲突。

TimeSeries 使用 UTC 日桶：

```text
time_series
| space_id
| dataset_id
| YYYY-MM-DD
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

RowMarker 保存完整 FactKey 和确定性 `content_hash`。Hash 输入是按 Field ID、Attribute Key 排序后的完整事实内容，不包含请求时间、调用方或 Sequence。

### WriteFields

```text
PrimaryStore
  1. 从 Runtime Catalog 获取 DatasetSchema 和 DataNode
  2. 校验 Field 归属、重复 Field、TypedValue 和请求上限
  3. direct tRPC 调用 DataNode.WriteFields

DataNode
  4. 计算确定性 content_hash
  5. 读取小型 RowMarker
  6. Marker 不存在时准备 Field/Attribute Set
  7. Marker Hash 相同则幂等成功
  8. Marker Hash 不同则返回 FACT_VERSION_IMMUTABLE
  9. 编码 DatasetFieldsChanged 和 MooxMessage
 10. 校验最终 JetStream Payload
 11. 原子提交 Marker、Field、Attribute、Sequence、Progress 和 Outbox
```

DataNode 不读取 Metadata，不理解 Field 类型，也不组装完整业务行。

### ReadFields

DataNode 不提供“读取完整行”的 RPC。每次读取必须指定 Field：

```proto
message ReadFieldsReq {
  string node_id = 1;
  string dataset_id = 2;
  repeated FactKey keys = 3;
  repeated string field_ids = 4;
  repeated string attribute_keys = 5;
}
```

`field_ids` 必须非空。Response 对每个 FactKey 返回 `row_exists` 和请求字段中实际存在的值。

上层需要当前 Dataset 的全部生效字段时，由 PrimaryStore 从 Runtime Catalog 取得 Field ID 集合，再分批调用 DataNode。DataNode 不提供普通 `ReadRows`、`ScanRows`、`ScanNodeSnapshot` 或 `DeleteRows`。

Record 未指定 Version 时，DataNode 在已知 `record_id` 的 Prefix 内使用反向 Seek 返回字符顺序最大的 Version；这不是对外范围扫描。

`GetDatasetProgress` 只供 PrimaryStore 状态查询、监控和故障诊断使用；View 重建不调用它，也不使用它决定双写或切换。

### 原子提交与事件

每个 DataNode 维护全局连续 `node_sequence`，每个 Dataset 保存最近一次提交使用的 `DatasetProgress.last_committed_sequence`。首次成功创建事实版本时，在一个 Pebble Batch 中提交：

```text
RowMarker
Field Keys
Attribute Keys
node_sequence
DatasetProgress.last_committed_sequence
DatasetFieldsChanged Outbox
```

完全相同的幂等重试不推进 Sequence、不更新 Progress、不创建事件。DataNode 使用一个简单 `commitMu` 保证 Sequence、Pebble Commit 和 Outbox 顺序一致。

`DatasetFieldsChanged` 顶层携带：

```text
node_id
dataset_id
sequence
新建事实版本及其完整 Field/Attribute
```

事实行和 Field 不保存 `source_node_id` 或 `source_sequence`。

## 数据保存与磁盘清理

### Pebble

`storage-primary` 根据 TimeSeries Dataset 的 `keep_days` 计算已经完整过期的 UTC 日桶，调用 DataNode 内部 `CleanupExpiredBuckets`。DataNode 对整个桶执行 `DeleteRange`，并在后台对删除范围 Compact。

清理是物理维护，不属于业务事实变更：

- 不生成 DatasetFieldsChanged；
- 不推进 Node Sequence 或 Dataset Progress；
- 不提供给普通外部调用方；
- Record 永不自动清理。

### ViewIndex

每个 View 使用两个独立物理槽位：

```text
<view-id>/slot-a.duckdb
<view-id>/slot-b.duckdb
```

Bleve 使用对应的 `slot-a/` 和 `slot-b/` 目录。Metadata 的 `active_slot` 决定当前角色。DuckDB/View 清理不调用 Pebble 清理；它只在 A/B 重建时按 `View.keep_days` 过滤旧 ActiveView Key，切换成功后删除整个 OldView DB。

增加 `keep_days` 不会恢复已经从 ActiveView 删除的 Key。需要恢复历史时由用户重新写入事实。

## JetStream 与单 Consumer

`MOOX_STORAGE` 使用 JetStream 原生策略：

```text
InterestPolicy
DiscardNew
```

View 只创建一个固定 Durable Consumer：

```text
storage_view
```

不创建 `storage_view_active`、`storage_view_rebuild` 或每 Build Consumer。Outbox 只允许 inspect 和 retry，不允许 force-skip。

事件按 DataNode 进入有界顺序 Lane。同一 Node 的 Sequence 串行；一个 Node 失败不阻塞其他 Node。ActiveView 持久化成功后才允许 ACK。

## ViewIndex Progress

每个物理 ViewIndex 独立保存：

```proto
message ViewIndexSourceProgress {
  string node_id = 1;
  string dataset_id = 2;
  uint64 sequence = 3;
}
```

协议不携带 `expected_last_applied_sequence`。ViewIndex 在事务内读取当前 Progress：

- `sequence <= current`：幂等成功，不修改行；
- `sequence > current`：原子提交行变更和新 Progress。

Progress 可以跨过同 Node 上其他 Dataset 的 Sequence。`ActiveHandle` 只保存 `index_id`、`revision` 和列集合，不复制 Source Progress。

## View A/B 重建

### 角色生命周期

角色不是固定文件名：

```text
重建前：slot-a = ActiveView，slot-b = 空
重建中：slot-a = ActiveView，slot-b = NewView
切换后：slot-b = ActiveView，slot-a = OldView
清理后：slot-b = ActiveView，slot-a = 空
```

下一次重建反向复用空槽位。

### 首次创建约束

TimeSeries 和 Record View 都必须在来源 Dataset 开始写入前创建。首次创建空 ActiveView，后续 Key 由实时事件持续加入。

系统不支持从一个已经存在事实、但从未创建过 View 的 Dataset 自动发现历史 Key。A/B 重建继承旧 ActiveView 的 Key 集合，不能修复旧 ActiveView 已经缺失的 Key。

### 开始双写

ViewBuild 保存：

```text
started_at
base_progress[{node_id, dataset_id}] -> sequence
new_slot
status
backfilled_rows
safe_error
```

启动步骤：

```text
1. 创建并清空 NewView
2. 获取该 View 的 Apply 锁
3. 在 ActiveView 开启一致性只读事务
4. 从同一事务读取 ActiveView Source Progress，保存为 base_progress
5. 用 base_progress 初始化 NewView Source Progress
6. 原子发布 ViewWriteTargets{Active, New}
7. 释放 Apply 锁
8. 启动 Backfill 协程
```

只有一个 Consumer，因此不需要 Rebuild Consumer、Pebble Snapshot、Build Barrier 或等待 ActiveView 追平 DataNode。安装双写目标之前的事件已经包含在 ActiveView 基线中；安装后的事件由同一个 Handler 同时写两个目标。

### 实时写与 ACK

没有重建时：

```text
写所有受影响 ActiveView -> 全部成功后 ACK
```

重建时：

```text
写所有受影响 ActiveView -> 写正在重建 View 的 NewView -> 全部成功后 ACK
```

任一 ActiveView 写失败时 NAK。所有 ActiveView 成功但 NewView 失败时：立即停止本次重建、标记 FAILED、移除 NewView、清理失败 DB，并正常 ACK 当前事件；ActiveView 继续服务。系统保存安全错误并等待用户手动重新触发，不自动重建。

### 空闲 Backfill

Backfill 使用启动时 ActiveView 的一致性只读事务：

1. 分页读取旧 ViewRowKey 和已有列；
2. TimeSeries 按 `started_at` 和 `View.keep_days` 过滤；Record 不过滤；
3. 把已有列写入 NewView；
4. 从 Metadata 获取当前 Revision 新增的列；
5. 使用旧 Key 和明确 Field ID 调用 DataNode.ReadFields；
6. 只填充 NewView 当前缺失的值。

实时事件优先。只要实时 View 事件排队或正在写入，Backfill 就不提交新 Batch。每个 Backfill Batch 最多 100 行或 50ms，完成后重新检查实时队列。已经开始的 DuckDB 事务不被强制中断；实时事件最多等待当前小 Batch 完成。

NewView 只有一个串行 Writer。实时写使用 `LIVE_WRITE`，Backfill 使用 `BACKFILL`；`BACKFILL` 只能填充缺失值，不能覆盖实时写已经提交的非空值。

Backfill 失败时停止本次重建、标记 FAILED、移除并清理 NewView；ActiveView 不受影响，用户手动重建。

### 原子切换

Backfill 完成后：

```text
1. 获取 View Apply 锁
2. 确认 ActiveView 与 NewView Source Progress 相同
3. Metadata CAS 切换 active_slot 和 active_view_revision
4. 原子发布新的 ActiveHandle
5. 从 ViewWriteTargets 移除旧 ActiveView
6. 释放 Apply 锁
7. 等旧查询引用归零并经过 Grace Period
8. 删除 OldView DB
```

切换持锁时间最多 2 秒；超时前未进入 Metadata CAS 时恢复原状态并保留 NewView，等待用户再次触发切换。DataView 查询从请求开始到结束持有同一个不可变 ActiveHandle，因此不会看到半成品或跨 Slot 结果。

## 故障语义

| 故障 | 行为 |
| --- | --- |
| DataNode 离线 | 只影响该节点 Dataset；其他节点继续服务 |
| storage-primary 离线 | 事实读写和 Metadata 管理暂停；已打开 View 查询继续 |
| storage-view 离线 | View 查询暂停；事实写入继续，JetStream 保留事件 |
| JetStream 离线或满 | Outbox 重试；达到本地上限后写入明确失败 |
| ActiveView Apply 失败 | NAK，相同 Sequence 重投 |
| NewView 实时写失败 | 当前重建 FAILED，Active 成功的事件 ACK，等待人工重建 |
| Backfill 失败 | 当前重建 FAILED，ActiveView 继续服务，等待人工重建 |
| Backfill 长期没有空闲时间 | 保持 BUILDING，不影响实时事件 |
| 切换超过 2 秒 | 不切换 Active，返回明确错误 |
| Schema 非 v4 | 启动失败，用户清理数据库后重新部署 |
| 旧 FactKey 内容冲突 | 返回 FACT_VERSION_IMMUTABLE |

## 两服务器 E2E

E2E 从仓库根被忽略的 `custom.toml` 读取两个服务器节点，不打印或提交凭证。

推荐拓扑：

```text
Server A: EventBus, storage-primary, storage-node-market, storage-view
Server B: storage-node-factor
```

必须验证：

1. K 线和因子 Dataset 分属两个 DataNode；
2. Field 和 Attribute 使用独立物理命名空间；
3. 相同事实幂等，冲突旧版本被拒绝；
4. Record 空 Version 读取字符顺序最大版本；
5. 单 Consumer 正常更新 ActiveView；
6. 重建开始后实时事件同时写 ActiveView/NewView；
7. 持续实时写入时 Backfill 暂停，空闲后继续；
8. NewView 或 Backfill 故障只终止 Build，ActiveView 继续并 ACK；
9. TimeSeries View 按自身 `keep_days` 生成新 DB；
10. Pebble 时间桶清理与 DuckDB 重建分别触发、互不联动；
11. 切换期间查询零错误，新字段原子可见；
12. 切换后删除 OldView DB；
13. 无 DataNode Scan、Snapshot 或 DeleteRows RPC。

## 验收标准

- Metadata 只接受 Schema v4，代码中不存在 `metadataSchemaVersionCompatible`。
- Dataset 创建后不可修改 DataNode；不存在 DatasetPlacement 和迁移工具。
- 不存在 Required、Dimensions、字段删除、DeleteRows、ReadRows、ScanRows 或 Snapshot RPC。
- TimeSeries 使用 UTC 日桶；Record 不自动清理。
- Field 使用 `0x01`，Attribute 使用 `0x02`，同名不冲突。
- DataNode `WriteFields` 实现不可变版本和内容 Hash 幂等。
- DataNode `ReadFields` 必须指定 Field；Record 空 Version 返回字符最大版本。
- 首次事实提交原子包含字段、Sequence、Progress 和 Outbox。
- JetStream 只有一个 `storage_view` Durable Consumer。
- ViewIndex Progress 只有 `{node_id,dataset_id,sequence}`。
- 每个 View 使用独立 A/B DB；ActiveView/NewView/OldView 生命周期明确。
- Backfill 只在实时队列空闲时小批运行，不能覆盖实时值。
- NewView 或 Backfill 失败不阻塞 ActiveView，整次重建等待人工重试。
- TimeSeries/Record View 都从旧 ActiveView Key 重建，不扫描 DataNode。
- 本地门禁、两轮独立代码审查和两服务器 E2E 全部通过。

## 明确不实现

- Dataset 内分片、Replica、Leader Election、Quorum 和自动 Failover。
- Dataset 迁移、复制、双写、回滚和 Rebalance。
- Required、Dimensions、Schema Fence、Schema Revision 和旧协议兼容。
- 字段级删除、Attribute 删除、DeleteRows 和历史事实修正。
- DataNode 普通范围扫描、Snapshot Scan 和完整行读取。
- Record 自动过期清理。
- 第二个 View Consumer、每 Build Consumer、Build Lease 和 Event Catch-up 状态机。
- 从 DataNode 枚举历史 FactKey，或修复旧 ActiveView 缺失的 Key。
- 自动重试失败的整次 View 重建。
