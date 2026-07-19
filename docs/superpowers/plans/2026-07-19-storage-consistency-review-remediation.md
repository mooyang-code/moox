# Storage 字段级存储与单 Consumer View 重建实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Storage 重构为单 PrimaryStore、多个单归属 DataNode、字段级 Pebble Key 和单 View Consumer 的多进程系统，并用 ActiveView/NewView A/B 数据库在不影响实时查询和写入的前提下完成低优先级 View 重建。

**Architecture:** `storage-primary` 持有 Schema v4 Metadata、不可变 Dataset 路由和动态追加的 Runtime Schema；DataNode 通过 `WriteFields` 创建不可变事实版本，把每个 Field/Attribute 保存成独立 Pebble Key，并原子提交 Sequence、Dataset Progress 和 Outbox。`storage-view` 只有一个 `storage_view` Durable Consumer，重建期间实时事件同时写 ActiveView/NewView，历史 Backfill 只在实时队列空闲时从旧 ActiveView Key 集合补入 NewView。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、SQLite、Pebble v1.1.5、NATS JetStream、DuckDB、Bleve、Vue 3、TypeScript、Shell。

## Global Constraints

- 设计事实源是 `docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`；本计划取代 2026-07-18 Shard 计划和本文件旧版本的冲突内容。
- 这是全新项目：不迁移旧 SQLite、Pebble、DuckDB、Bleve、Proto、配置或 Seed，不保留 Alias、Deprecated 字段、双读或双写兼容代码。
- Metadata 只接受 Schema v4；删除 `metadataSchemaVersionCompatible`，版本不等于 `metadataSchemaVersion` 时启动失败。
- Dataset 创建时必须指定 `data_node_id`，创建后不可修改；系统不提供 Dataset 迁移。
- Schema 只允许追加 Field；已有 `field_id`、`value_type` 和语义不可修改，所有 Field 均可缺失，不存在 Required。
- 完整 FactKey 对应的事实版本不可修改；相同内容是幂等重试，不同内容返回 `FACT_VERSION_IMMUTABLE`。
- 不支持字段删除、Attribute 删除、DeleteRows、历史事实修正或为旧版本追加缺失 Field。
- TimeSeries 不存在 Dimensions；身份只有 `space_id + dataset_id + subject_id + freq + data_time`。
- Record 写入 Version 必填；读取 Version 为空时返回字符顺序最大的 Version。
- DataNode 普通读取必须携带明确 Field ID；不提供完整行读取、普通范围扫描或 Snapshot RPC。
- TimeSeries Dataset 和 TimeSeries View 分别使用自己的 `keep_days`；两者互不比较、互不触发。Record 的 `keep_days` 必须为 `0`。
- View 只有一个固定 Durable Consumer `storage_view`；不创建 Active/Rebuild 双 Consumer 或每 Build Consumer。
- NewView 实时写或 Backfill 失败时终止整次重建，ActiveView 已成功的事件正常 ACK，等待用户手动重建。
- Backfill 只有在实时 View 队列为空且没有实时 Apply 时才能提交新 Batch；每批最多 100 行或 50ms。
- TimeSeries 和 Record View 都必须在来源 Dataset 开始写入前创建；重建继承旧 ActiveView Key，不能发现或修复旧 View 缺失的 Key。
- 实施必须在独立 Worktree 完成，经过两轮相互独立的代码审查，并完成两服务器 E2E 后才能宣告完成。

---

## 文件职责图

### 协议与 Metadata

- `modules/storage/proto/data_node.proto`：DataNode `WriteFields`、`ReadFields`、Progress、状态和时间桶清理 RPC。
- `modules/storage/proto/rows.proto`：无 Dimensions、无删除字段、无行级来源信息的 FactKey/FieldValue 公共模型。
- `modules/storage/proto/dataset_fields_changed.proto`：不可变事实版本首次提交事件。
- `modules/storage/proto/view_index.proto`：单 Sequence Source Progress、实时写和 Backfill 写协议。
- `modules/storage/proto/metadata.proto`：DataNode、Dataset/View `keep_days`、A/B Slot 和简化 ViewBuild。
- `modules/storage/schema/metadata.sql`：全新 Schema v4。

### DataNode

- `modules/storage/internal/service/datanode/pebble/key.go`：有序 Tuple Key、TimeSeries 日桶和 Field/Attribute 命名空间。
- `modules/storage/internal/service/datanode/pebble/store.go`：RowMarker Hash 幂等、字段级原子写和精确读取。
- `modules/storage/internal/service/datanode/pebble/event.go`：`DatasetFieldsChanged` 确定性编码。
- `modules/storage/internal/service/datanode/pebble/cleanup.go`：TimeSeries 过期桶 DeleteRange/Compact。
- `modules/storage/internal/service/datanode/outbox_relay.go`：按 Node Sequence 发布 Outbox。

### View

- `modules/storage/internal/service/viewindex/slots.go`：每个 View 的 slot-a/slot-b 路径和角色。
- `modules/storage/internal/service/viewindex/backfill.go`：`LIVE_WRITE` 与只填缺失值的 `BACKFILL`。
- `modules/storage/internal/service/viewbuilder/write_targets.go`：原子 Active/New 写目标。
- `modules/storage/internal/service/viewbuilder/backfill.go`：实时优先的空闲 Backfill。
- `modules/storage/internal/service/viewbuilder/build.go`：Build 状态、失败终止、切换和 OldView 清理。
- `modules/storage/internal/service/dataview/active_handle.go`：只含 Index、Revision、Columns 的不可变查询 Handle。

## 正确性不变量

1. RowMarker Hash 不存在时才能创建新事实版本；Hash 相同是无事件幂等成功，Hash 不同是不可变冲突。
2. RowMarker、Field、Attribute、Node Sequence、Dataset Progress 和 Outbox 在一个 Pebble Batch 中提交。
3. Field 物理命名空间是 `0x01`，Attribute 是 `0x02`，同名永不冲突。
4. DataNode 在 Pebble Commit 前使用 Publisher 的实际 Max Payload 校验最终 `MooxMessage`。
5. Outbox 只删除连续发布成功前缀，不允许 force-skip。
6. ViewIndex 行变更与 `{node_id,dataset_id,sequence}` Progress 在一个事务中提交。
7. `sequence <= current` 是幂等成功；协议不携带 Expected Sequence。
8. 无重建时，ActiveView 成功才 ACK；重建时，ActiveView/NewView 都成功才 ACK。
9. ActiveView 成功、NewView 失败时必须先原子移除 NewView 并标记 Build FAILED，再 ACK 当前事件。
10. Backfill 不能覆盖 NewView 中实时写已经提交的非空值。
11. View 重建基线来自同一 ActiveView 只读事务中的行和 Source Progress。
12. 安装 `ViewWriteTargets{Active,New}` 与读取基线 Progress 受同一 View Apply 锁保护。
13. Backfill 完成且 Active/New Progress 相同时才允许切换。
14. DataView 每个请求始终使用同一个不可变 ActiveHandle。
15. Pebble Cleanup 与 View A/B 重建分别调度；任何一方都不调用或修改另一方配置。

## 实施顺序

| 阶段 | 任务 | 阶段退出条件 |
| --- | --- | --- |
| A. 最终契约 | 1-4 | v4、最终 Proto、Runtime Catalog、三进程和直接 HMAC 编译通过 |
| B. 字段事实存储 | 5-7 | 字段 Key、不可变版本、精确读和独立桶清理通过 |
| C. 单 Consumer View | 8-12 | 单 Consumer、A/B Slot、实时双写、空闲 Backfill 和切换通过 |
| D. 集成与证明 | 13-14 | 全仓命名收敛、两轮审查和两服务器 E2E 通过 |

### 任务 1：创建独立 Worktree 并记录基线

**文件：**
- 读取：`docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`
- 读取：`docs/superpowers/plans/2026-07-19-storage-consistency-review-remediation.md`
- 读取：`scripts/test-storage-boundary-contract.sh`
- 读取：`scripts/test-storage-consistency-contract.sh`

**接口：**
- 输入：当前 `origin/main` 和本设计规范。
- 输出：干净的 `codex/storage-field-keys` Worktree 与基线结果。

- [ ] **步骤 1：创建独立 Worktree**

```bash
git fetch origin
git worktree add ../moox-storage-field-keys -b codex/storage-field-keys origin/main
cd ../moox-storage-field-keys
git status --short --branch
```

预期：工作区为空，分支基于当前 `origin/main`。

- [ ] **步骤 2：运行改动前基线**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
make verify
```

预期：记录每个既有失败及原因；不得把既有失败归因于后续改动。

- [ ] **步骤 3：记录起点**

```bash
git rev-parse HEAD
git status --porcelain=v1
```

预期：保存起点 SHA，状态为空。

### 任务 2：定义最终 Proto 与 Schema v4

**文件：**
- 重命名：`modules/storage/proto/store.proto` -> `modules/storage/proto/data_node.proto`
- 重命名：`modules/storage/proto/view.proto` -> `modules/storage/proto/data_view.proto`
- 重命名：`modules/storage/proto/rows_committed.proto` -> `modules/storage/proto/dataset_fields_changed.proto`
- 修改：`modules/storage/proto/common.proto`
- 修改：`modules/storage/proto/rows.proto`
- 修改：`modules/storage/proto/metadata.proto`
- 修改：`modules/storage/proto/view_index.proto`
- 修改：`modules/storage/proto/Makefile`
- 修改：`modules/storage/schema/metadata.sql`
- 修改：`modules/storage/internal/bootstrap/metadata/schema_test.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/store.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/store_test.go`
- 重新生成：`modules/storage/proto/storagegen/*`
- 修改：`scripts/test-storage-boundary-contract.sh`

**接口：**
- 输入：无历史兼容要求的全新 Schema v4。
- 输出：`DataNode.WriteFields/ReadFields`、`DatasetFieldsChanged`、`ViewIndexSourceProgress` 和 A/B View Metadata。

- [ ] **步骤 1：先让边界测试拒绝全部旧契约**

在 `scripts/test-storage-boundary-contract.sh` 增加精确扫描，活动代码中以下符号必须为零：

```text
DataShard, datashard, data_shard, ShardKey, ShardRow, ShardTarget
shard_id, source_shard_id, source_sequence
MergeRows, WriteRows, ReadRows, ScanRows, DeleteRows
BeginNodeSnapshot, ScanNodeSnapshot, EndNodeSnapshot
attributes_to_delete, removed_column_names, removed_columns
dimensions, dimension_hash, reservedDimensionsAttribute
expected_last_applied_sequence
storage_view_active, storage_view_rebuild
DatasetColumn.required, c_required, schema_revision
metadataSchemaVersionCompatible
```

运行：

```bash
bash scripts/test-storage-boundary-contract.sh
```

预期：旧代码稳定失败；`docs/superpowers/**` 历史文字不参与扫描。

- [ ] **步骤 2：写出最终 Fact 与 DataNode 协议**

`rows.proto` 使用无 Dimensions 的 Key：

```proto
message TimeSeriesKey {
  string space_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string freq = 4;
  string data_time = 5;
}

message RecordKey {
  string space_id = 1;
  string dataset_id = 2;
  string record_id = 3;
  string version = 4;
}

message FactKey {
  oneof key {
    TimeSeriesKey time_series = 1;
    RecordKey record = 2;
  }
}

message FieldValue {
  string field_id = 1;
  TypedValue value = 2;
}
```

`data_node.proto` 只保留精确 Field 操作和维护状态：

```proto
service DataNode {
  rpc WriteFields(WriteFieldsReq) returns (WriteFieldsRsp);
  rpc ReadFields(ReadFieldsReq) returns (ReadFieldsRsp);
  rpc GetNodeState(GetNodeStateReq) returns (GetNodeStateRsp);
  rpc GetDatasetProgress(GetDatasetProgressReq) returns (GetDatasetProgressRsp);
  rpc CleanupExpiredBuckets(CleanupExpiredBucketsReq) returns (CleanupExpiredBucketsRsp);
}

message ReadFieldsReq {
  common.AuthInfo auth_info = 1;
  string node_id = 2;
  string dataset_id = 3;
  repeated FactKey keys = 4;
  repeated string field_ids = 5;
  repeated string attribute_keys = 6;
}
```

在 `WriteFields` 注释中明确：同一 FactKey 只允许完全相同内容的幂等重试，修改必须使用新 Version。

`GetDatasetProgress` 只供 PrimaryStore 状态查询、监控和故障诊断使用；它不参与 View 重建基线、双写或切换判定。

- [ ] **步骤 3：定义事件和 ViewIndex Progress**

```proto
message DatasetFieldsChanged {
  string node_id = 1;
  string dataset_id = 2;
  uint64 sequence = 3;
  repeated FactVersion facts = 4;
}

message ViewIndexSourceProgress {
  string node_id = 1;
  string dataset_id = 2;
  uint64 sequence = 3;
}
```

`ViewIndexApplyBatch` 只包含 Row Writes、Source Progress、View Revision 和范围更新。删除 Schema Hash、Required、Expected Sequence、删除字段和行级来源信息。

- [ ] **步骤 4：定义 Metadata v4**

Schema v4 必须包含：

```text
t_data_nodes
t_datasets.c_data_node_id
t_datasets.c_keep_days
t_views.c_keep_days
t_views.c_active_slot
t_views.c_desired_view_revision
t_views.c_active_view_revision
t_view_builds.c_new_slot
t_view_builds.c_base_progress_json
t_view_builds.c_backfilled_rows
t_view_builds.c_safe_error
```

删除 Routes、Devices、Topology Lock、Required、Build Lease、Build Cursor、Snapshot 和 Consumer 字段。`metadataSchemaVersionCompatible` 改为直接严格比较：

```go
if version != metadataSchemaVersion {
    return errIncompatibleMetadataSchema
}
```

- [ ] **步骤 5：运行生成、测试并提交**

```bash
make generate
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap/metadata ./internal/service/metadata/sqlite)
bash scripts/test-storage-boundary-contract.sh
git add modules/storage/proto modules/storage/schema modules/storage/internal/bootstrap/metadata modules/storage/internal/service/metadata/sqlite scripts/test-storage-boundary-contract.sh
git commit -m "refactor(storage): define field storage v4 contracts"
```

预期：生成代码中不存在旧 RPC 和旧字段，Schema v2/v3/v5 启动测试失败，v4 通过。

### 任务 3：实现动态 Runtime Catalog 与 Metadata 规则

**文件：**
- 新建：`modules/storage/internal/catalog/runtime.go`
- 新建：`modules/storage/internal/catalog/runtime_test.go`
- 修改：`modules/storage/internal/service/metadata/store.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_dataset.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_store.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_view.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_test.go`
- 删除：`modules/storage/internal/service/metadata/cache/store.go`
- 删除：`modules/storage/internal/service/metadata/cache/store_test.go`
- 修改：`modules/storage/internal/service/primarystore/metadata_catalog.go`
- 修改：`modules/storage/internal/service/primarystore/metadata_catalog_test.go`

**接口：**
- 输入：Schema v4 DataNode、Dataset、Field、DatasetColumn、View。
- 输出：不可变 Route、原子 DatasetSchema、幂等 Add Field 和 `keep_days` 校验。

- [ ] **步骤 1：先写 Runtime Catalog 失败测试**

覆盖以下测试：

```go
func TestRuntimeRoutesNeverChangeAfterLoad(t *testing.T)
func TestAppendFieldPublishesImmutableSchema(t *testing.T)
func TestInFlightRequestKeepsOldSchema(t *testing.T)
func TestDatasetDataNodeCannotChangeAfterCreate(t *testing.T)
func TestRecordKeepDaysMustBeZero(t *testing.T)
func TestRecordViewKeepDaysMustBeZero(t *testing.T)
```

运行：

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/catalog ./internal/service/metadata/sqlite)
```

预期：Runtime 包不存在或规则尚未实现而失败。

- [ ] **步骤 2：实现最终 Runtime 类型**

```go
type DatasetKey struct{ SpaceID, DatasetID string }

type DatasetRoute struct {
    DataNodeID   string
    ServiceTarget string
}

type DatasetSchema struct {
    DataKind storagepb.DataKind
    Fields   map[string]FieldSchema
    Subjects map[string]struct{}
    Freqs    map[string]struct{}
    KeepDays uint32
}

type DatasetEntry struct {
    Route    DatasetRoute
    schema   atomic.Pointer[DatasetSchema]
    updateMu sync.Mutex
}
```

Route Map 启动后不变。Add Field 在同一 Dataset 锁内完成 SQLite Commit 和新 Schema Pointer Store，成功响应必须晚于两者。

- [ ] **步骤 3：实现不可变 Dataset 与保存天数规则**

`UpdateDataset` 永远拒绝修改 `data_node_id`。TimeSeries 允许 `keep_days >= 0`；Record 只允许 `0`。View 根据 Primary Dataset DataKind 使用同一规则。系统不比较 Dataset/View 的 `keep_days`。

- [ ] **步骤 4：删除通用 Cache，改成 SQL 分页**

所有 List 使用稳定 `ORDER BY`、`LIMIT`、`OFFSET`；测试插入三页数据并断言顺序、`has_more` 和 SQL 查询次数。禁止先加载全表再在 Go 中分页。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/catalog ./internal/service/metadata/... ./internal/service/primarystore)
git add modules/storage/internal/catalog modules/storage/internal/service/metadata modules/storage/internal/service/primarystore
git commit -m "refactor(storage): add immutable runtime catalog"
```

### 任务 4：建立三进程与直接 HMAC 调用边界

**文件：**
- 修改：`packages/gatewayauth/trpc.go`
- 修改：`packages/gatewayauth/trpc_test.go`
- 重命名：`modules/storage/config/storage.shard.yaml` -> `modules/storage/config/storage.node.yaml`
- 重命名：`modules/storage/config/trpc_go.shard.yaml` -> `modules/storage/config/trpc_go.node.yaml`
- 修改：`modules/storage/config/storage.primary.yaml`
- 修改：`modules/storage/config/storage_view/trpc_go.yaml`
- 修改：`modules/storage/internal/config/loader.go`
- 修改：`modules/storage/internal/config/loader_test.go`
- 修改：`modules/storage/cmd/server/main.go`
- 修改：`modules/storage/cmd/server/view_runtime.go`
- 新建：`modules/storage/internal/service/primarystore/datanodes.go`
- 新建：`modules/storage/internal/service/primarystore/datanodes_test.go`

**接口：**
- 输入：Runtime Catalog Route 和生成的 DataNode Client。
- 输出：`primary/node/view` 三角色、直接签名 RPC 和按 Node 复用的 Client Pool。

- [ ] **步骤 1：先写进程边界测试**

测试必须证明：

```text
role 只接受 primary、node、view
primary 不嵌入 DataNode
node 必须配置 node_id 和 Pebble Path
view 不打开 Metadata SQLite 或 Pebble
DataNode 不进入 Admin Gateway
Storage 内部 Client 不读取 MOOX_SERVICE_GATEWAY_TARGET
```

- [ ] **步骤 2：实现直接 HMAC Client**

```go
func NewDirectTRPCClientOptions(target string, credentials Credentials) []client.Option
```

签名覆盖调用方、被调用方、方法和确定性序列化请求体。DataNode `WriteFields/ReadFields` 只接受 `storage-primary`；`CleanupExpiredBuckets` 只接受 `storage-primary` 维护身份。

- [ ] **步骤 3：实现 DataNode Client Pool**

```go
type DataNodeClientPool interface {
    Client(nodeID string) (datanode.Client, error)
}
```

Pool 只从 Runtime Catalog 读取 `service_target`，请求不得携带 Endpoint。相同 Node 复用 Proxy，不同 Dataset 可路由到不同 Node。

- [ ] **步骤 4：运行测试并提交**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./packages/gatewayauth
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./cmd/server ./internal/service/primarystore)
git add packages/gatewayauth modules/storage/config modules/storage/internal/config modules/storage/cmd/server modules/storage/internal/service/primarystore
git commit -m "refactor(storage): separate storage process roles"
```

### 任务 5：实现字段级 Pebble Key 与不可变事实版本

**文件：**
- 重命名：`modules/storage/internal/service/datashard` -> `modules/storage/internal/service/datanode`
- 修改：`modules/storage/internal/service/datanode/contracts/store.go`
- 修改：`modules/storage/internal/service/datanode/contracts/store_test.go`
- 修改：`modules/storage/internal/service/datanode/pebble/key.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store_test.go`
- 重命名：`modules/storage/internal/service/datanode/pebble/committed.go` -> `modules/storage/internal/service/datanode/pebble/event.go`
- 修改：`modules/storage/internal/service/datanode/pebble/outbox.go`
- 修改：`modules/storage/internal/service/datanode/pebble/outbox_test.go`
- 修改：`modules/storage/internal/service/datanode/service.go`
- 修改：`modules/storage/internal/service/datanode/service_test.go`

**接口：**
- 输入：PrimaryStore 已校验的 `WriteFieldsReq`。
- 输出：字段级 Key、RowMarker Hash、不可变幂等和原子 `DatasetFieldsChanged` Outbox。

- [ ] **步骤 1：先写物理 Key 契约测试**

测试固定字节顺序：

```text
TimeSeries 同一 UTC 日桶连续
跨日进入不同 Bucket Prefix
Field Tag 固定为 0x01
Attribute Tag 固定为 0x02
同名 Field/Attribute Key 不同
Record 按 record_id/version 字符顺序排列
Prefix 边界不受 |、%、NUL 和 UTF-8 影响
```

Tuple Codec 使用长度前缀编码；测试不能只比较可读字符串。

- [ ] **步骤 2：先写不可变版本测试**

覆盖：

```text
首次 WriteFields 创建 Marker/Fields/Attributes
相同完整内容重试不增加 Sequence 和 Outbox
同 Key 改一个值返回 FACT_VERSION_IMMUTABLE
同 Key 增加原来缺失的 Field 也返回 FACT_VERSION_IMMUTABLE
Field 和 Attribute 同名可同时存在
失败的 Payload 校验提交零 Key
```

- [ ] **步骤 3：实现 Marker Hash 与字段 Batch**

```go
type RowMarker struct {
    Key         *storagepb.FactKey
    ContentHash [32]byte
}
```

Hash 输入必须是确定性编码后的完整 Key、排序 Field 和排序 Attribute。DataNode 在 Row Lock 下只读取 Marker；不存在时把 Marker 和所有独立值加入 Batch，存在时只比较 Hash。

- [ ] **步骤 4：原子提交 Sequence、Progress 和事件**

使用 `commitMu` 包住 Sequence 分配、最终事件编码、Payload 校验和 Batch Commit。一个成功 Batch 同时包含：

```text
RowMarker + Field + Attribute
__meta/node_sequence
__meta/dataset_progress/<dataset-id>
__outbox/<big-endian-sequence>
```

事件只在首次创建版本时产生；没有 Delete Operation。

- [ ] **步骤 5：运行测试和基准并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datanode/...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -run '^$' -bench 'Benchmark(FieldKey|ImmutableWrite)' -benchmem ./internal/service/datanode/pebble)
git add modules/storage/internal/service/datanode
git commit -m "refactor(storage): store immutable fields in pebble"
```

基准必须至少覆盖 8 Field 完整 K 线、100 Field 因子版本、同内容幂等和 1/3 Field 精确读取；记录结果但不保留双布局生产开关。

### 任务 6：实现必须指定 Field 的精确读取

**文件：**
- 修改：`modules/storage/internal/service/datanode/pebble/store.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store_test.go`
- 修改：`modules/storage/internal/service/datanode/service.go`
- 修改：`modules/storage/internal/service/datanode/service_test.go`
- 修改：`modules/storage/internal/service/primarystore/factreader.go`
- 修改：`modules/storage/internal/service/primarystore/factreader_test.go`
- 修改：`modules/storage/internal/service/primarystore/data.go`
- 修改：`modules/storage/internal/service/primarystore/data_test.go`
- 修改：`modules/storage/internal/service/primarystore/key_adapter.go`

**接口：**
- 输入：FactKey、非空 Field ID、可选 Attribute Key。
- 输出：精确 Field 结果、`row_exists` 和 Record 最新字符 Version。

- [ ] **步骤 1：先写读取边界测试**

覆盖：

```text
field_ids 为空被拒绝
只返回请求 Field，不读取其他 Field
缺失 Field 不等于行不存在
同名 Field/Attribute 分别返回
Record 指定 Version 精确读取
Record Version 为空返回字符顺序最大版本
Version 1、2、10 的最大值是 2
不存在的 record_id 返回 row_exists=false
```

- [ ] **步骤 2：实现 DataNode ReadFields**

精确 Version 对每个请求 Field 生成 Point Key 并批量读取；使用 Marker 判断 `row_exists`。Record Version 为空时在 `record_id` Prefix 内 `SeekLT(nextPrefix(prefix))`，跳过 Field/Attribute Suffix 后得到最大 Version，再读取明确 Field。

- [ ] **步骤 3：让 PrimaryStore 组装业务响应**

PrimaryStore 从 Runtime Catalog 取得调用方需要的字段：显式字段列表直接校验；“当前全部生效字段”由 PrimaryStore 展开成明确 Field ID 后调用 DataNode。TimeSeries 时间范围按 Metadata Frequency 生成有限 FactKey，超过 Key/跨度上限返回 `BATCH_TOO_LARGE`。Record 不提供任意范围读取。

- [ ] **步骤 4：删除普通扫描和完整行内部接口**

删除 `ReadRows`、`ScanRows`、`FactPrefixScanner`、Snapshot Reader 和所有调用方。Archive/Factor 需要时序范围时必须通过 PrimaryStore 的有限 Key 展开，不得直连 DataNode Scan。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datanode/... ./internal/service/primarystore/...)
git add modules/storage/internal/service/datanode modules/storage/internal/service/primarystore
git commit -m "refactor(storage): require exact field reads"
```

### 任务 7：实现独立的 TimeSeries 时间桶清理

**文件：**
- 新建：`modules/storage/internal/service/datanode/pebble/cleanup.go`
- 新建：`modules/storage/internal/service/datanode/pebble/cleanup_test.go`
- 修改：`modules/storage/internal/service/datanode/service.go`
- 修改：`modules/storage/internal/service/datanode/service_test.go`
- 新建：`modules/storage/internal/service/primarystore/cleanup.go`
- 新建：`modules/storage/internal/service/primarystore/cleanup_test.go`
- 修改：`modules/storage/internal/config/loader.go`
- 修改：`modules/storage/internal/config/loader_test.go`
- 修改：`modules/storage/internal/observability/view_metrics.go`
- 修改：`modules/storage/internal/observability/view_metrics_test.go`

**接口：**
- 输入：TimeSeries Dataset `keep_days`、当前 UTC 日和内部维护身份。
- 输出：按完整 UTC 日桶清理的 Pebble 范围和独立清理指标。

- [ ] **步骤 1：先写清理测试**

固定当前时间 `2026-07-19T12:00:00Z`，覆盖：

```text
keep_days=0 不清理
keep_days=30 只清理 2026-06-19 之前完整日桶
边界日桶完整保留
Record 调用被拒绝
清理不改变 Node Sequence/Dataset Progress/Outbox
Field/Attribute/Marker 同时不可见
```

- [ ] **步骤 2：实现 CleanupExpiredBuckets**

PrimaryStore 只传明确 Dataset 和 `expire_before_day`；DataNode 验证请求为 UTC 日期，对每个完整 Bucket Prefix 执行 `DeleteRange`，提交后异步 `Compact(start,end,true)`。RPC 不允许浏览器、Collector 或普通服务调用。

- [ ] **步骤 3：证明与 View.keep_days 无耦合**

测试中设置 Dataset `keep_days=7`、View `keep_days=30`，运行 Pebble Cleanup 后断言 Metadata 不修改 View 配置、不启动 View Build；反向触发 View Build 也不得调用 Cleanup RPC。

- [ ] **步骤 4：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datanode/... ./internal/service/primarystore/... ./internal/observability)
git add modules/storage/internal/service/datanode modules/storage/internal/service/primarystore modules/storage/internal/config modules/storage/internal/observability
git commit -m "feat(storage): clean expired timeseries buckets"
```

### 任务 8：把 JetStream 收敛为一个 View Consumer

**文件：**
- 修改：`modules/eventbus/internal/config/config_types.go`
- 修改：`modules/eventbus/internal/config/config_validation.go`
- 修改：`modules/eventbus/internal/config/config_defaults.go`
- 修改：`modules/eventbus/internal/config/config_test.go`
- 修改：`modules/eventbus/internal/registry/registry.go`
- 修改：`modules/eventbus/internal/registry/registry_test.go`
- 修改：`modules/eventbus/config/app.yaml`
- 修改：`modules/admin/cmd/cli/eventbus_credentials.go`
- 修改：`modules/admin/cmd/cli/eventbus_credentials_test.go`
- 修改：`modules/storage/internal/bootstrap/eventbus/factory.go`
- 修改：`modules/storage/internal/bootstrap/eventbus/factory_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/eventconsumer/bus.go`
- 修改：`modules/storage/internal/service/viewbuilder/eventconsumer/bus_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus.go`
- 修改：`modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus_test.go`

**接口：**
- 输入：`MOOX_STORAGE` 和 `DatasetFieldsChanged` Subject。
- 输出：唯一 Durable `storage_view` 和真实 ACK/NAK 生命周期。

- [ ] **步骤 1：先写 Stream/Consumer 契约测试**

断言：

```text
MOOX_STORAGE = FileStorage + 1 Replica + InterestPolicy + DiscardNew
只存在 storage_view 一个 View Durable
AckExplicit + DeliverAll + MaxDeliver=-1
配置中出现 storage_view_active/storage_view_rebuild 时失败
```

- [ ] **步骤 2：更新最小权限凭证**

`storage-view` 只获得 `storage_view` 的 INFO/NEXT/ACK 权限。DataNode Publisher 只能发布 Storage Subject，不能 Fetch View Consumer。测试读取生成 YAML，但不得输出 Token 或 Password。

- [ ] **步骤 3：修复 ACK 真实完成语义**

Event Handler 必须等待 ViewBuilder 返回最终持久化结果；入队成功不等于 ACK。Active Apply 错误返回 NAK，相同 Node Sequence 重投后 Lane 可以恢复。

- [ ] **步骤 4：运行测试并提交**

```bash
(cd modules/eventbus && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./internal/registry)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./cmd/cli)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap/eventbus ./internal/service/viewbuilder/eventconsumer)
git add modules/eventbus modules/admin/cmd/cli/eventbus_credentials.go modules/admin/cmd/cli/eventbus_credentials_test.go modules/storage/internal/bootstrap/eventbus modules/storage/internal/service/viewbuilder/eventconsumer
git commit -m "refactor(storage): use one view consumer"
```

### 任务 9：实现 ViewIndex Source Progress 与 A/B 物理槽位

**文件：**
- 修改：`modules/storage/internal/service/viewindex/engine.go`
- 修改：`modules/storage/internal/service/viewindex/engine_test.go`
- 修改：`modules/storage/internal/service/viewindex/batch_write.go`
- 修改：`modules/storage/internal/service/viewindex/batch_write_test.go`
- 修改：`modules/storage/internal/service/viewindex/client.go`
- 修改：`modules/storage/internal/service/viewindex/client_test.go`
- 新建：`modules/storage/internal/service/viewindex/slots.go`
- 新建：`modules/storage/internal/service/viewindex/slots_test.go`
- 新建：`modules/storage/internal/service/viewindex/backfill.go`
- 新建：`modules/storage/internal/service/viewindex/backfill_test.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/view_store_apply.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/view_store_test.go`
- 修改：`modules/storage/internal/service/viewindex/bleve/index.go`
- 修改：`modules/storage/internal/service/viewindex/bleve/index_test.go`

**接口：**
- 输入：`ViewIndexApplyBatch`、View ID、Slot A/B 和写入模式。
- 输出：独立 Source Progress、`LIVE_WRITE/BACKFILL` 和可删除的物理 DB Slot。

- [ ] **步骤 1：先写跨引擎 Progress 契约测试**

DuckDB/Bleve 共用测试表：

```text
行与 Progress 原子提交
sequence <= current 幂等成功且不改行
sequence > current 允许跨其他 Dataset Sequence
错误 View Revision 提交零数据
协议没有 Expected Sequence
```

- [ ] **步骤 2：先写 Backfill 不覆盖测试**

```text
LIVE_WRITE 先写 close=101，BACKFILL close=100 后仍为 101
BACKFILL 先写 volume=20，LIVE_WRITE close=101 后两列都存在
BACKFILL 可以填充 NULL/缺失列
BACKFILL 不推进 Source Progress
```

- [ ] **步骤 3：实现 Slot 路径和角色**

```go
type Slot string

const (
    SlotA Slot = "a"
    SlotB Slot = "b"
)

func InactiveSlot(active Slot) Slot
func DuckDBPath(root, viewID string, slot Slot) string
func BlevePath(root, viewID string, slot Slot) string
```

每个 View 独占 `slot-a.duckdb/slot-b.duckdb` 或对应目录，不把多个 View 混在同一个 A/B 文件中。

- [ ] **步骤 4：实现两种写入模式**

```go
type WriteMode uint8

const (
    WriteModeLive WriteMode = iota + 1
    WriteModeBackfill
)
```

DuckDB Backfill 使用“仅当目标列为 NULL 时写入”的单事务 UPSERT；Bleve 在一个串行 Writer 内读取当前 Document 后只补缺失字段。实时写优先级由上层调度保证。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewindex/...)
git add modules/storage/internal/service/viewindex
git commit -m "feat(storage): add view slots and source progress"
```

### 任务 10：实现单 Consumer 的 ActiveView/NewView 实时写

**文件：**
- 新建：`modules/storage/internal/service/viewbuilder/write_targets.go`
- 新建：`modules/storage/internal/service/viewbuilder/write_targets_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/service.go`
- 修改：`modules/storage/internal/service/viewbuilder/service_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/apply.go`
- 修改：`modules/storage/internal/service/viewbuilder/apply_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/checkpoint.go`
- 修改：`modules/storage/internal/service/viewbuilder/options.go`
- 修改：`modules/storage/internal/service/viewbuilder/options_test.go`
- 删除：`modules/storage/internal/service/viewbuilder/deletes.go`
- 修改：`modules/storage/test/view_derivation_reliability_test.go`

**接口：**
- 输入：单 `storage_view` Consumer、原子 ViewWriteTargets 和 `DatasetFieldsChanged`。
- 输出：实时双写、Active ACK 保证和失败 Build 隔离。

- [ ] **步骤 1：先写实时写目标状态测试**

```go
type ViewWriteTargets struct {
    Active *ViewHandle
    New    *ViewHandle
}
```

覆盖：没有 Build 时只写 Active；有 Build 时 Active/New 都写；同一 View Apply 锁下读取 Active Progress 并发布 New；切换后 New 成为 Active、原 Active 成为 Old。

- [ ] **步骤 2：先写 ACK/失败测试**

覆盖：

```text
所有受影响 Active 成功 -> ACK
任一受影响 Active 失败 -> NAK
所有受影响 Active/New 都成功 -> ACK
所有受影响 Active 成功、New 失败 -> Build FAILED、移除 New、当前事件 ACK
New 失败后的下一事件只写 Active
失败 Build 不自动创建新 NewView
```

- [ ] **步骤 3：实现有界 Node Lane 与实时优先入口**

每个 Node 一个有界顺序队列和一个 Worker；失败 Sequence 成功重投后 Lane 解锁。View 事件进入实时优先队列，不能创建每事件 Goroutine。

- [ ] **步骤 4：实现 Build 失败终止顺序**

Active 成功、New 失败时必须按顺序执行：

```text
Metadata/ViewBuild 标记 FAILED
原子发布 ViewWriteTargets{Active: old, New: nil}
关闭 New Writer
安排删除 New DB
向事件层返回成功以 ACK
```

错误日志包含 View ID、New Slot、Node/Dataset/Sequence 和底层 Cause；RPC/Metadata 只保存安全错误。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder ./test -run 'TestViewDerivation')
git add modules/storage/internal/service/viewbuilder modules/storage/test/view_derivation_reliability_test.go
git commit -m "feat(storage): dual write active and new views"
```

### 任务 11：实现实时优先的空闲 Backfill

**文件：**
- 新建：`modules/storage/internal/service/viewbuilder/backfill.go`
- 新建：`modules/storage/internal/service/viewbuilder/backfill_test.go`
- 新建：`modules/storage/internal/service/viewbuilder/build.go`
- 新建：`modules/storage/internal/service/viewbuilder/build_test.go`
- 修改：`modules/storage/internal/service/dataview/maintenance.go`
- 修改：`modules/storage/internal/service/dataview/maintenance_test.go`
- 删除：`modules/storage/internal/service/dataview/build_cursor.go`
- 删除：`modules/storage/internal/service/dataview/build_cursor_test.go`
- 修改：`modules/storage/internal/service/dataview/schedule.go`
- 修改：`modules/storage/internal/service/dataview/schedule_test.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_test.go`

**接口：**
- 输入：ActiveView 一致性只读事务、NewView、当前 Metadata 字段和实时队列状态。
- 输出：`base_progress`、历史 Key/旧列复制、新字段精确补全和可人工重试的 Build。

- [ ] **步骤 1：先写 Build 启动原子性测试**

测试在 Active Progress 10 与 11 的边界注入事件，证明获取 View Apply 锁后：只读事务中的行和 `base_progress=10` 一致；NewView 先初始化到 Progress 10；发布 New 后 Sequence 11 同时写两个目标；不存在未写 New 却已 ACK 的事件。另测启动后没有实时事件时，Backfill 完成后 Active/New Progress 仍相等并可切换。

- [ ] **步骤 2：先写空闲调度测试**

使用可控时钟覆盖：

```text
实时队列非空时 Backfill 不开始 Batch
实时 Apply 进行中时 Backfill 等待
空闲时最多提交 100 行
单 Batch 到 50ms 后主动让出
实时事件在当前 Batch 完成后优先执行
连续实时流量下 Build 保持 BUILDING 而不阻塞 Active
```

- [ ] **步骤 3：实现旧 ActiveView 基线读取**

在安装 NewView 时开启 ActiveView 一致性只读事务，保存其中的 Source Progress 为 `base_progress`，并在发布双写目标前用它初始化 NewView Source Progress。Backfill 从该事务分页读取 Key 和旧列：TimeSeries 按 `started_at - keep_days` 过滤，Record 全部保留。

- [ ] **步骤 4：只读取新增 View 字段**

比较 Active Revision Columns 与 Desired Revision Columns，得到新增列及其来源 Field ID。按 Dataset 分组旧 FactKey，经 PrimaryStore 分批调用 DataNode `ReadFields`。已经被 Pebble 清理的历史 Fact 返回缺失时，新列保持 NULL，旧列仍从 ActiveView 复制。

- [ ] **步骤 5：实现失败后人工重建**

Backfill 任一步失败时调用与实时 New 写失败相同的 `FailBuild`：标记 FAILED、移除 New、清理 DB、不自动重调度。只有显式 `RebuildView` 命令可以从 FAILED 创建下一次 Build。

- [ ] **步骤 6：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder ./internal/service/dataview ./internal/service/metadata/sqlite)
git add modules/storage/internal/service/viewbuilder modules/storage/internal/service/dataview modules/storage/internal/service/metadata/sqlite
git commit -m "feat(storage): backfill views only while idle"
```

### 任务 12：实现原子切换与 OldView 整库删除

**文件：**
- 新建：`modules/storage/internal/service/dataview/active_handle.go`
- 新建：`modules/storage/internal/service/dataview/active_handle_test.go`
- 修改：`modules/storage/internal/service/dataview/service.go`
- 修改：`modules/storage/internal/service/dataview/service_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/build.go`
- 修改：`modules/storage/internal/service/viewbuilder/build_test.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_test.go`
- 修改：`modules/storage/test/view_index_switch_test.go`

**接口：**
- 输入：Backfill 完成、Active/New Progress 相同和 Metadata Expected Old Slot/Revision。
- 输出：最多 2 秒的 Active Slot CAS、不可变 ActiveHandle 和延迟删除 OldView。

- [ ] **步骤 1：先写查询与切换竞态测试**

覆盖：旧查询跨切换继续使用旧 Handle；新查询使用新 Slot；新字段切换前不可见、切换后一次可见；切换期间查询零错误；OldView 在引用归零前不删除。

- [ ] **步骤 2：实现最小 ActiveHandle**

```go
type ActiveHandle struct {
    IndexID  string
    Slot     viewindex.Slot
    Revision uint64
    Columns  []*storagepb.ViewColumn
}
```

Handle 不保存 Source Progress。请求开始时 Acquire，结束时 Release；发布后内容不可修改。

- [ ] **步骤 3：实现 2 秒切换协议**

```text
获取 View Apply 锁
确认 Backfill Completed
确认 Active/New Progress 相同
Metadata CAS(expected active slot/revision -> new slot/revision)
原子发布 New ActiveHandle
发布 ViewWriteTargets{Active: new, New: nil}
释放 Apply 锁
```

进入 CAS 前超过 2 秒则返回 `VIEW_SWITCH_TIMEOUT`，Active/New 双写保持不变，等待用户再次触发切换。CAS 已成功但响应不确定时读取 Metadata，若 New 已 Active 必须完成本地 Handle 发布。

- [ ] **步骤 4：删除 OldView DB**

旧 Handle 引用归零并经过 Grace Period 后，关闭旧 DuckDB/Bleve Handle，删除整个旧 Slot 文件或目录。删除失败只上报清理错误，不回退已经成功的 Active 切换。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/dataview ./internal/service/viewbuilder ./internal/service/viewindex/... ./test -run 'TestViewIndexSwitch')
git add modules/storage/internal/service/dataview modules/storage/internal/service/viewbuilder modules/storage/internal/service/viewindex modules/storage/internal/service/metadata/sqlite modules/storage/test/view_index_switch_test.go
git commit -m "feat(storage): switch view database slots atomically"
```

### 任务 13：完成全仓命名、调用方和 UI 集成

**文件：**
- 修改：`modules/storage/config/metadata.seed.yaml`
- 修改：`modules/storage/internal/bootstrap/metadata/seed.go`
- 修改：`modules/storage/internal/bootstrap/metadata/seed_test.go`
- 修改：`modules/cli/internal/command/metadata_implementation.go`
- 修改：`modules/cli/internal/command/metadata_quant_seed_test.go`
- 修改：`modules/cli/config/fields.yaml`
- 修改：`modules/monitor/internal/metrics/storage.go`
- 修改：`modules/monitor/internal/metrics/storage_test.go`
- 修改：`modules/archive/internal/consumer/decode.go`
- 修改：`modules/archive/internal/consumer/decode_test.go`
- 修改：`modules/factor/internal/storageio/client.go`
- 修改：`modules/factor/internal/storageio/client_test.go`
- 修改：`web/src/api/storage/metadata.ts`
- 修改：`web/src/api/storage/types.ts`
- 修改：`web/src/views/data/datasets/components/dataset-column-panel.vue`
- 修改：`scripts/storage-start.sh`
- 修改：`scripts/storage-stop.sh`
- 修改：`scripts/deploy-moox.sh`
- 修改：`scripts/test-storage-boundary-contract.sh`
- 修改：`scripts/test-storage-consistency-contract.sh`
- 修改：`docs/存储层架构.md`
- 修改：`docs/存储目标架构与元数据.md`
- 修改：`docs/存储引擎架构.md`
- 修改：`docs/架构总览.md`
- 修改：`docs/协议设计.md`
- 修改：`docs/行情数据归档模块设计.md`
- 修改：`docs/因子计算模块设计.md`
- 修改：`docs/策略模块架构设计.md`
- 重命名：`packages/jetstream/subject_token.go` -> `packages/jetstream/node_subject_token.go`
- 重命名：`packages/jetstream/subject_token_test.go` -> `packages/jetstream/node_subject_token_test.go`

**接口：**
- 输入：任务 2-12 的最终 API。
- 输出：没有旧 Shard、Merge/Delete/Scan/Snapshot/Dimensions/Required/双 Consumer 残留的完整仓库。

- [ ] **步骤 1：更新 Seed、CLI 和 UI**

Seed 使用：

```yaml
data_nodes:
  - node_id: data-node-market
    service_target: ip://127.0.0.1:20106
    status: active

datasets:
  - space_id: crypto
    dataset_id: binance_spot_kline
    data_node_id: data-node-market
    keep_days: 180
```

删除 Required、Dimensions、Route、Device 和可修改 Dataset Node 的 UI。Dataset/View 页面显示“保存天数”；Record 保存天数固定为 0。Field 页面只允许“新增字段”。

- [ ] **步骤 2：更新调用方语义**

Collector/Factor 写入必须提供完整不可变 Version 内容；冲突旧 Version 不得自动覆盖。Archive/Factor 范围读取通过 PrimaryStore 的有限 Key 生成接口；不再调用 Scan。Record 用户文档明确 Version 字符排序规则。

- [ ] **步骤 3：执行零残留扫描**

```bash
rg -n 'DataShard|datashard|data_shard|storage-shard|storage_shard_|shard_id|source_shard_id|source_sequence' modules packages web scripts examples docs --glob '!docs/superpowers/**'
rg -n 'MergeRows|WriteRows|ReadRows|ScanRows|DeleteRows|BeginNodeSnapshot|ScanNodeSnapshot|EndNodeSnapshot' modules packages web scripts examples --glob '!**/storagegen/**'
rg -n 'attributes_to_delete|removed_column_names|removed_columns|dimensions|dimension_hash|reservedDimensionsAttribute' modules/storage modules/cli/config web/src/api/storage web/src/views/data scripts examples --glob '!**/storagegen/**'
rg -n 'storage_view_active|storage_view_rebuild|expected_last_applied_sequence|metadataSchemaVersionCompatible' modules packages web scripts examples --glob '!**/storagegen/**'
rg -n 'record\.required|form\.required|DatasetColumn.*Required|GetRequired\(\)|c_required|required_column_names' modules web scripts examples --glob '!**/storagegen/**'
```

预期：活动代码零匹配。普通英文输入提示中的 “required” 和非 TimeSeries 业务中的通用 “dimension” 必须逐条确认，不得用宽泛排除掩盖 Storage 残留。

- [ ] **步骤 4：运行集成测试并提交**

```bash
make generate
make check-boundaries
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/archive && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/factor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/monitor && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
CI=true pnpm -C web vue-tsc --noEmit
pnpm -C web test
pnpm docs:build
git add modules packages web scripts examples docs
git commit -m "refactor(storage): finish field storage integration"
```

### 任务 14：完成两服务器 E2E、两轮审查和最终交付

**文件：**
- 新建：`modules/cli/internal/command/storage_e2e.go`
- 新建：`modules/cli/internal/command/storage_e2e_test.go`
- 新建：`modules/storage/test/field_storage_e2e_test.go`
- 新建：`scripts/test-storage-two-node-e2e.sh`
- 修改：`modules/cli/internal/command/setup.go`
- 修改：`modules/cli/internal/command/setup_test.go`
- 修改：`modules/cli/README.md`
- 修改：`docs/运维/MooX-EventBus运维.md`
- 修改：`docs/运维/数据保留与磁盘空间.md`

**接口：**
- 输入：仓库根被忽略的 `custom.toml`、两个 SSH 节点和最终发布包。
- 输出：无凭证泄漏的 E2E 报告、两轮独立审查和已推送分支。

- [ ] **步骤 1：实现凭证安全的 E2E 命令**

```text
moox-cli setup storage-e2e --file ./custom.toml --primary-host-index 0 --factor-host-index 1
```

命令按稳定名称选择两个不同启用节点，只输出 Run ID、逻辑主机名、阶段、耗时和结果。远端目录固定为随机 `/tmp/moox-storage-e2e-<run-id>`；不得打印密码、Token、私钥或展开后的 SSH 命令；退出时清理自身进程和目录。

- [ ] **步骤 2：执行两服务器场景**

Server A 启动 EventBus、storage-primary、market DataNode 和 storage-view；Server B 启动 factor DataNode。E2E 必须证明：

```text
两个 Dataset 分属不同 DataNode
Field/Attribute 同名不冲突
相同 Fact 幂等且冲突 Version 被拒绝
Record 空 Version 返回字符最大版本
只有 storage_view 一个 Durable Consumer
重建期间 ActiveView/NewView 实时双写
持续写入时 Backfill 不增长，停止写入后继续
NewView 写失败后 Build FAILED、当前事件 ACK、ActiveView 继续
Backfill 失败后等待人工重建
TimeSeries View.keep_days 只影响新 DB
Dataset.keep_days 清理 Pebble 时不触发 View Build
View Build 时不触发 Pebble Cleanup
切换查询零错误并删除 OldView DB
DataNode 不暴露 Scan/Snapshot/Delete RPC
```

- [ ] **步骤 3：运行完整本地门禁**

```bash
make generate
make verify
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
(cd modules/eventbus && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./...)
CI=true pnpm -C web vue-tsc --noEmit
pnpm -C web test
pnpm docs:build
git diff --check
```

预期：全部通过，只记录命令摘要和失败数量，不记录完整环境变量。

- [ ] **步骤 4：提交 E2E Harness**

```bash
git add modules/cli/internal/command/storage_e2e.go modules/cli/internal/command/storage_e2e_test.go modules/cli/internal/command/setup.go modules/cli/internal/command/setup_test.go modules/storage/test/field_storage_e2e_test.go scripts/test-storage-two-node-e2e.sh modules/cli/README.md docs/运维/MooX-EventBus运维.md docs/运维/数据保留与磁盘空间.md
git commit -m "test(storage): add field storage e2e"
```

- [ ] **步骤 5：执行第一轮独立代码审查**

使用 `superpowers:requesting-code-review`，审查完整分支 Diff。第一位 Reviewer 专注 Proto、Schema v4、Runtime Catalog、Field/Attribute Key、不可变 Hash、Sequence/Progress/Outbox 原子性、精确读取和清理边界。修复全部 P1/P2，增加回归测试后提交：

```bash
git add -A
git commit -m "fix(storage): address field storage review"
```

- [ ] **步骤 6：执行第二轮独立代码审查**

使用全新 Reviewer 上下文，不复用第一轮结论。第二位 Reviewer 专注单 Consumer ACK、Active/New 失败语义、实时优先 Backfill、A/B Slot、查询 Handle、2 秒切换、配置独立性和 E2E 清理。修复全部 P1/P2 后提交：

```bash
git add -A
git commit -m "fix(storage): address view rebuild review"
```

- [ ] **步骤 7：从最终 HEAD 重新执行全部证明**

重复步骤 2 和步骤 3。审查修复前的绿色结果不得作为最终证据。

- [ ] **步骤 8：推送已验证分支**

```bash
git status --short
git push -u origin codex/storage-field-keys
git status --short --branch
git rev-parse HEAD
git rev-parse '@{upstream}'
```

预期：工作区干净，HEAD 等于 Upstream，全部任务勾选，两轮独立审查关闭，两服务器 E2E 从最终 HEAD 通过。

## 最终验收清单

- [ ] Metadata 只接受 v4，`metadataSchemaVersionCompatible` 已删除。
- [ ] Dataset 直接保存不可修改的 `data_node_id`，没有 Placement、Route、Hash 或迁移工具。
- [ ] 所有 Field 可缺失且只增不减，没有 Required。
- [ ] TimeSeries 没有 Dimensions，Record Version 规则已写入用户文档。
- [ ] Field/Attribute 分别使用 `0x01/0x02`，每个值独立 Pebble Key。
- [ ] `WriteFields` 实现完整内容 Hash、严格不可变和无事件幂等。
- [ ] DataNode 只提供指定 Field 的精确读取，没有完整行、Scan、Snapshot 或 Delete RPC。
- [ ] 首次事实提交原子包含 Marker、Field、Attribute、Sequence、Progress 和 Outbox。
- [ ] TimeSeries 使用 UTC 日桶，Record 不自动清理。
- [ ] Dataset/View `keep_days` 分别生效且互不调用。
- [ ] JetStream 只有一个 `storage_view` Durable Consumer。
- [ ] ViewIndex Progress 只有 `{node_id,dataset_id,sequence}`，没有 Expected Sequence。
- [ ] 每个 View 使用独立 slot-a/slot-b DB，角色按 ActiveView/NewView/OldView 转换。
- [ ] 实时事件优先；Backfill 每批最多 100 行或 50ms，且只补缺失值。
- [ ] NewView/Backfill 失败只终止 Build，ActiveView 继续并等待人工重建。
- [ ] 重建 Key 只来自旧 ActiveView；首次创建约束和历史不可恢复限制已写入用户文档。
- [ ] 切换持锁不超过 2 秒，查询不中断，OldView 在引用归零后整库删除。
- [ ] 本地完整门禁、两轮独立审查和最终两服务器 E2E 全部通过。
