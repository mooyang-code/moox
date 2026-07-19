# Storage 字段级 Upsert 与 Dataset Subject 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Storage 重构为单 PrimaryStore、多个单归属 DataNode、字段级 Pebble Upsert、每 Dataset 一个 NATS Subject 和单 View Consumer 的多进程系统，并通过 ActiveView/NewView A/B 数据库平滑完成 View 重建。

**Architecture:** Metadata SQLite v4 通过现有 `snapshotcache` 定时发布不可变内存快照；PrimaryStore 校验 RowKey/Field 后直连 DataNode，DataNode 在同一 Pebble Batch 中 Upsert 独立 Field/Attribute Key 和内部有序 Outbox。每个 Dataset 发布到独立 `moox.storage.fields_changed.v1.<space-token>.<dataset-token>` Subject；`storage-view` 以单条串行方式消费全部 Dataset，重建期间实时双写 ActiveView/NewView，空闲 Backfill 从旧 ActiveView RowKey 补全历史字段。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、SQLite、snapshotcache、Pebble v1.1.5、NATS JetStream、DuckDB、Bleve、Vue 3、TypeScript、Shell。

## Global Constraints

- 设计事实源是 `docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`；本计划取代 2026-07-18 Shard 计划和本文件旧版本的冲突内容。
- 这是全新项目：不迁移旧 SQLite、Pebble、DuckDB、Bleve、Proto、配置或 Seed，不保留 Alias、Deprecated 字段、双读或兼容分支。
- Metadata 只接受 Schema v4；版本不等于 `metadataSchemaVersion` 时启动失败。
- Dataset 创建时必须指定 `data_node_id`，创建后不可修改；系统不提供 Dataset 迁移。
- Schema 只允许追加 Field；已有 `field_id`、`value_type` 和语义不可修改，所有 Field 均可缺失。
- `WriteFields` 是字段级 Upsert：允许新增、覆盖和为历史 RowKey 补写新增 Field。
- 不支持字段删除、Attribute 删除或 DeleteRows。
- TimeSeries 不存在 Dimensions；RowKey 是 `space_id + dataset_id + subject_id + freq + data_time`。
- Record RowKey 是 `space_id + dataset_id + record_id + version`；空 Version 读取字符顺序最大的 Version。
- DataNode 普通读取必须携带明确 RowKey 和 Field ID；不提供完整行读取、普通范围扫描、Snapshot 或 Progress RPC。
- Dataset/View 使用独立 `keep_duration`；Record 必须为 `0`，两者互不比较、互不触发。
- 每个 Dataset 使用独立 NATS Subject，所有 Subject 共用 `MOOX_STORAGE` Stream；不同 Dataset/DataNode 之间不定义顺序。
- View 只有固定 Durable `storage_view`，`MaxAckPending=1`、`FetchBatch=1`；Active 失败时重试同一 Delivery，不创建 Sequence Lane、Source Progress 或第二个 Consumer。
- NewView 实时写或 Backfill 失败时终止整次重建，ActiveView 已成功的事件正常 ACK，等待用户手动重建。
- Backfill 只有在实时 View 队列为空且没有实时 Apply 时才能提交新 Batch；每批最多 100 行或 50ms。
- View 重建由 Desired Revision 变化、View 新关联 Dataset 或覆盖时长超过 `2 * keep_duration` 触发。
- TimeSeries/Record View 都从旧 ActiveView RowKey 重建，不从 DataNode 枚举 Key。
- 实施必须在独立 Worktree 完成，经过两轮相互独立的代码审查，并完成两服务器 E2E 后才能宣告完成。

---

## 文件职责图

### 协议与 Metadata

- `modules/storage/proto/data_node.proto`：DataNode `WriteFields`、`ReadFields`、状态和时间桶清理 RPC。
- `modules/storage/proto/rows.proto`：无 Dimensions、无删除字段、无来源 Sequence 的 `RowKey` 和 FieldValue。
- `modules/storage/proto/dataset_fields_changed.proto`：字段 Upsert 事件，不携带 Node/Dataset Sequence。
- `modules/storage/proto/view_index.proto`：无 Source Progress 的 `LIVE_WRITE/BACKFILL` 写协议。
- `modules/storage/proto/metadata.proto`：DataNode、Dataset/View `keep_duration`、A/B Slot 和简化 ViewBuild。
- `modules/storage/schema/metadata.sql`：全新 Schema v4，不保留旧列。

### Metadata 与 DataNode

- `modules/storage/internal/service/metadata/cache/store.go`：复用 snapshotcache 加载 Dataset、Field、DataNode 和 View。
- `modules/storage/internal/service/datanode/pebble/key.go`：有序 Tuple RowKey、可配置时间桶和 Field/Attribute 命名空间。
- `modules/storage/internal/service/datanode/pebble/store.go`：字段级原子 Upsert 和精确读取。
- `modules/storage/internal/service/datanode/pebble/outbox.go`：内部 `outbox_id` 分配、顺序扫描和连续前缀删除。
- `modules/storage/internal/service/datanode/pebble/event.go`：DatasetFieldsChanged 与稳定 Message ID 编码。
- `modules/storage/internal/service/datanode/outbox_relay.go`：逐条同步发布，首个失败即停止。

### EventBus 与 View

- `packages/jetstream/subject_token.go`：通用 `EncodeSubjectToken/DecodeSubjectToken`。
- `modules/storage/internal/service/viewbuilder/eventconsumer/subject.go`：Dataset Subject 构造、解析和 Payload 一致性校验。
- `modules/storage/internal/service/viewindex/slots.go`：每个 View 的 slot-a/slot-b 路径和角色。
- `modules/storage/internal/service/viewindex/backfill.go`：`LIVE_WRITE` 与只填缺失值的 `BACKFILL`。
- `modules/storage/internal/service/viewbuilder/write_targets.go`：原子 Active/New 写目标。
- `modules/storage/internal/service/viewbuilder/reconcile.go`：三个 View 重建触发条件。
- `modules/storage/internal/service/viewbuilder/backfill.go`：实时优先的空闲 Backfill。
- `modules/storage/internal/service/viewbuilder/build.go`：Build 状态、失败终止、切换和 OldView 清理。
- `modules/storage/internal/service/dataview/active_handle.go`：不可变查询 Handle。

## 正确性不变量

1. PrimaryStore 使用同一份 Metadata Cache Snapshot 完成 Dataset 路由、Field 归属和值类型校验。
2. 每个 Field 使用 `0x01`，Attribute 使用 `0x02`；没有 RowMarker 或整行 Content Hash。
3. Field/Attribute Upsert、Outbox 条目和下一 `outbox_id` 在一个 Pebble Batch 中提交。
4. DataNode 在 Pebble Commit 前使用 Publisher 实际 Max Payload 校验最终 MooxMessage。
5. Outbox Relay 按 ID 逐条同步发布，失败立即停止，只删除连续成功前缀。
6. Dataset Subject 中的 Space/Dataset Token 必须与 Payload 一致。
7. 同一 Dataset 只有一个发布 DataNode；不同 Dataset Subject 不定义相互顺序。
8. View Consumer 每次只获取一条消息；当前事件 ACK 前不获取下一条，Active 失败时本地重试同一 Delivery。
9. ViewIndex 的重复 Field Upsert 结果不变；协议不保存 Sequence 或 Progress。
10. 无重建时所有受影响 ActiveView 成功才 ACK；重建时 ActiveView/NewView 全部成功才 ACK。
11. ActiveView 成功、NewView 失败时必须先移除 NewView 并标记 Build FAILED，再 ACK 当前事件。
12. Backfill 不能覆盖 NewView 中实时写已经提交的非空值。
13. 安装双写目标、读取 ActiveView 基线和切换都使用同一个 View Apply 锁。
14. DataView 每个请求始终使用同一个不可变 ActiveHandle。
15. Pebble Cleanup 与 View A/B 重建分别调度；任何一方都不调用另一方。

## 实施顺序

| 阶段 | 任务 | 阶段退出条件 |
| --- | --- | --- |
| A. 最终契约 | 1-4 | v4、RowKey、Metadata Cache、三进程和直接 HMAC 编译通过 |
| B. 字段事实存储 | 5-7 | 字段 Upsert、内部 Outbox、精确读和独立桶清理通过 |
| C. Dataset Subject 与 View | 8-12 | Dataset Subject、单条消费、A/B、触发器、Backfill 和切换通过 |
| D. 集成与证明 | 13-14 | 全仓命名收敛、两轮审查和两服务器 E2E 通过 |

### 任务 1：创建独立 Worktree 并记录基线

**文件：**
- 读取：`docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`
- 读取：`docs/superpowers/plans/2026-07-19-storage-consistency-review-remediation.md`
- 读取：`scripts/test-storage-boundary-contract.sh`
- 读取：`scripts/test-storage-consistency-contract.sh`

**接口：**
- 输入：当前 `origin/main` 和本设计规范。
- 输出：包含本设计与计划的干净 `codex/storage-field-upsert` Worktree 与基线结果。

- [ ] **步骤 1：创建独立 Worktree**

```bash
git fetch origin
git status --short
git worktree add ../moox-storage-field-upsert -b codex/storage-field-upsert HEAD
cd ../moox-storage-field-upsert
git status --short --branch
```

预期：当前文档分支工作区为空，新 Worktree 基于已经提交的设计文档 HEAD。

- [ ] **步骤 2：运行改动前基线**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
make verify
```

预期：记录每个既有失败及原因；不得把既有失败归因于后续改动。

- [ ] **步骤 3：提交基线记录**

```bash
git status --short
git rev-parse HEAD
```

预期：没有文件改动；保存 HEAD 和测试输出到实施记录。

### 任务 2：定义 RowKey、字段事件与 Schema v4

**文件：**
- 重命名：`modules/storage/proto/store.proto` -> `modules/storage/proto/data_node.proto`
- 修改：`modules/storage/proto/rows.proto`
- 重命名：`modules/storage/proto/rows_committed.proto` -> `modules/storage/proto/dataset_fields_changed.proto`
- 修改：`modules/storage/proto/view_index.proto`
- 修改：`modules/storage/proto/metadata.proto`
- 修改：`modules/storage/schema/metadata.sql`
- 新建：`modules/storage/schema/metadata_schema_version_test.go`
- 生成：`modules/storage/proto/storagegen/*`

**接口：**
- 输入：最终设计规范。
- 输出：`RowKey`、`WriteFields/ReadFields`、无 Sequence 的 `DatasetFieldsChanged` 和 A/B Metadata。

- [ ] **步骤 1：先写协议边界失败测试**

更新 `scripts/test-storage-boundary-contract.sh`，要求活动 Proto 不得出现：

```text
FactKey, RowMarker, content_hash, FACT_VERSION_IMMUTABLE
node_sequence, source_sequence, DatasetProgress, GetDatasetProgress
ViewIndexSourceProgress, expected_last_applied_sequence, base_progress
MergeRows, DeleteRows, ReadRows, ScanRows, Snapshot
keep_days, required, dimensions
```

运行：

```bash
bash scripts/test-storage-boundary-contract.sh
```

预期：FAIL，并报告当前旧符号。

- [ ] **步骤 2：定义最终 Proto**

`rows.proto` 定义：

```proto
message RowKey {
  string space_id = 1;
  string dataset_id = 2;
  oneof kind {
    TimeSeriesRowKey time_series = 3;
    RecordRowKey record = 4;
  }
}

message FieldValue {
  string field_id = 1;
  TypedValue value = 2;
}
```

`data_node.proto` 只保留：

```proto
service DataNode {
  rpc WriteFields(WriteFieldsReq) returns (WriteFieldsRsp);
  rpc ReadFields(ReadFieldsReq) returns (ReadFieldsRsp);
  rpc GetNodeState(GetNodeStateReq) returns (GetNodeStateRsp);
  rpc CleanupExpiredBuckets(CleanupExpiredBucketsReq) returns (CleanupExpiredBucketsRsp);
}
```

`DatasetFieldsChanged` 定义：

```proto
message DatasetFieldsChanged {
  string space_id = 1;
  string dataset_id = 2;
  repeated RowFieldUpsert rows = 3;
}
```

`ViewIndexApplyBatch` 只包含 Row Upserts、View Revision 和写入模式，不包含 Sequence、Progress、Expected Sequence 或删除字段。

- [ ] **步骤 3：定义 Metadata Schema v4**

Metadata 使用：

```text
t_datasets.c_data_node_id
t_datasets.c_keep_duration TEXT NOT NULL DEFAULT '0'
t_views.c_keep_duration TEXT NOT NULL DEFAULT '0'
t_views.c_desired_view_revision
t_views.c_active_view_revision
t_views.c_active_slot
t_view_builds.c_new_slot
t_view_builds.c_status
t_view_builds.c_started_at
t_view_builds.c_backfilled_rows
t_view_builds.c_safe_error
```

`keep_duration` 在 Proto/SQLite 中保存规范化字符串，例如 `90m`、`24h`、`4320h`；服务入口使用 `time.ParseDuration` 校验，`0` 是唯一的永久保存值。

删除 Placement、Route、Device、Required、Build Cursor/Lease、Source Progress 和兼容迁移。版本检查只允许 `version == 4`。

- [ ] **步骤 4：生成代码并运行协议测试**

```bash
make generate
bash scripts/test-storage-boundary-contract.sh
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./schema ./proto/storagegen)
```

预期：PASS，生成代码中无旧符号。

- [ ] **步骤 5：提交最终契约**

```bash
git add modules/storage/proto modules/storage/schema scripts/test-storage-boundary-contract.sh
git commit -m "refactor(storage): define row key and field upsert protocol"
```

### 任务 3：复用 Metadata snapshotcache

**文件：**
- 修改：`modules/storage/internal/service/metadata/cache/store.go`
- 修改：`modules/storage/internal/service/metadata/cache/store_test.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_dataset.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_view.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- 修改：`modules/storage/internal/service/metadata/sqlite/crud_test.go`
- 修改：`modules/storage/internal/service/primarystore/service.go`
- 修改：`modules/storage/internal/service/primarystore/data_test.go`
- 删除：`modules/storage/internal/service/primarystore/shardrouter/*`

**接口：**
- 输入：Metadata SQLite v4。
- 输出：定时原子 Cache Snapshot，以及 `GetDataset/GetField/ListDatasetColumns/GetDataNode/GetView` 索引读。

- [ ] **步骤 1：先写 Cache 刷新测试**

覆盖：启动必须完成首份完整快照；SQLite 新增 Field 后定时刷新可见；CRUD 后主动 Refresh 立即可见；刷新中任一 List 失败时继续使用旧快照；Dataset 的 DataNode 变化被 SQLite 拒绝。

- [ ] **步骤 2：扩展现有 snapshotcache Source 和索引**

复用 `modules/storage/internal/service/metadata/cache.Store`，一次 `fetchEntries` 加载全部写路径需要的 Dataset、DatasetColumn、Field、DataNode、Subject、Frequency、View 和 ViewColumn。只在所有读取成功后发布新 Snapshot。

不得新建 Runtime Catalog、DatasetSchema Clone 或 per-Dataset atomic.Pointer。

- [ ] **步骤 3：实现 Append-only Metadata 规则**

```text
Add Field: 允许
删除 Field: 拒绝
修改 field_id/value_type: 拒绝
修改 Dataset.data_node_id: 拒绝
TimeSeries keep_duration: 0 或合法正 Duration
Record keep_duration: 只能为 0
```

Field 创建成功后更新受影响 View 的 `desired_view_revision`；View 新关联 Dataset 时同样更新 Desired Revision。

- [ ] **步骤 4：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/metadata/... ./internal/service/primarystore)
git add modules/storage/internal/service/metadata modules/storage/internal/service/primarystore
git commit -m "refactor(storage): load routing and schema with snapshotcache"
```

### 任务 4：建立三进程与直接 HMAC 调用边界

**文件：**
- 修改：`modules/storage/cmd/server/main.go`
- 修改：`modules/storage/internal/bootstrap/bootstrap.go`
- 修改：`modules/storage/internal/bootstrap/bootstrap_test.go`
- 新建：`modules/storage/internal/service/primarystore/datanodes.go`
- 新建：`modules/storage/internal/service/primarystore/datanodes_test.go`
- 修改：`modules/storage/config/storage.yaml`
- 重命名：`modules/storage/config/storage.shard.yaml` -> `modules/storage/config/storage.node.yaml`
- 重命名：`modules/storage/config/trpc_go.shard.yaml` -> `modules/storage/config/trpc_go.node.yaml`
- 修改：`modules/storage/config/trpc_go.primary.yaml`
- 修改：`modules/storage/config/storage_view/trpc_go.yaml`

**接口：**
- 输入：Metadata Cache 的 Dataset/DataNode。
- 输出：`primary|node|view` Role、按 node_id 复用的 tRPC Proxy 和 Service HMAC。

- [ ] **步骤 1：先写进程边界失败测试**

覆盖：Primary 启动 Metadata/PrimaryStore；Node 只启动 DataNode/Pebble/Outbox；View 只启动 ViewBuilder/DataView/ViewIndex；未知 Role 失败；DataNode 拒绝非 Primary HMAC；请求不得携带 Endpoint。

- [ ] **步骤 2：实现 DataNode Client Pool**

```go
type DataNodeClientPool interface {
    Client(ctx context.Context, nodeID string) (storagepb.DataNodeClientProxy, error)
}
```

Pool 从当前 Metadata Cache Snapshot 读取 `service_target`。同 Node 复用 Proxy；Cache 更新 Node 状态后新请求读取新快照。

- [ ] **步骤 3：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./cmd/server ./internal/bootstrap ./internal/service/primarystore)
git add modules/storage/cmd/server modules/storage/internal/bootstrap modules/storage/internal/service/primarystore modules/storage/config
git commit -m "refactor(storage): split primary node and view roles"
```

### 任务 5：实现字段级 Pebble Upsert 与内部 Outbox ID

**文件：**
- 重命名：`modules/storage/internal/service/datashard` -> `modules/storage/internal/service/datanode`
- 修改：`modules/storage/internal/service/datanode/pebble/key.go`
- 新建：`modules/storage/internal/service/datanode/pebble/key_test.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store_test.go`
- 修改：`modules/storage/internal/service/datanode/pebble/outbox.go`
- 修改：`modules/storage/internal/service/datanode/pebble/outbox_test.go`
- 新建：`modules/storage/internal/service/datanode/pebble/event.go`
- 新建：`modules/storage/internal/service/datanode/pebble/event_test.go`
- 修改：`modules/storage/internal/service/datanode/outbox_relay.go`
- 修改：`modules/storage/internal/service/datanode/outbox_relay_test.go`

**接口：**
- 输入：PrimaryStore 已校验的 `WriteFieldsReq`。
- 输出：Field/Attribute Upsert、内部有序 Outbox 和无 Sequence 的 DatasetFieldsChanged。

- [ ] **步骤 1：先写 Key Codec 测试**

覆盖：`0x01` Field 与 `0x02` Attribute 不冲突；TimeSeries RowKey 包含可配置 Bucket Start；Record 按 Version 字符排序；Space/Dataset/Field 中含 `%|/`、中文或 NUL 时往返正确；不存在 `0x00 RowMarker`。

- [ ] **步骤 2：先写 Upsert 行为测试**

```text
首次写入 RowKey + close 创建 Field Key
同 RowKey 新增 factor_alpha 保留 close
同 RowKey 覆盖 close 返回新值
同 RowKey 相同值重写允许成功并生成新 Outbox
Field/Attribute 同名分别读取
请求中重复 Field 被 PrimaryStore 拒绝
```

- [ ] **步骤 3：实现内部 Outbox ID 原子提交**

```text
Field/Attribute Batch.Set
__outbox/<20 位补零 outbox_id>
__meta/next_outbox_id
Pebble Batch.Commit
```

分配 ID 和 Commit 使用一个 DataNode `outboxMu`。Outbox ID 不进入 `DatasetFieldsChanged` Payload，不提供 Head/GetProgress RPC。

- [ ] **步骤 4：证明 Outbox 发布顺序**

Relay 必须逐条调用同步 `PublishMessage`。测试让第 2 条失败，断言第 3 条未发布，只删除第 1 条；重试后从第 2 条继续。稳定 Message ID 使用：

```text
storage-<node-token>-<outbox-id>
```

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datanode/...)
git add modules/storage/internal/service/datanode
git commit -m "feat(storage): upsert field keys with ordered outbox"
```

### 任务 6：实现必须指定 RowKey 和 Field 的精确读取

**文件：**
- 修改：`modules/storage/internal/service/datanode/service.go`
- 修改：`modules/storage/internal/service/datanode/service_test.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store.go`
- 修改：`modules/storage/internal/service/datanode/pebble/store_test.go`
- 修改：`modules/storage/internal/service/primarystore/data.go`
- 修改：`modules/storage/internal/service/primarystore/data_test.go`

**接口：**
- 输入：RowKey、非空 Field ID、可选 Attribute Key。
- 输出：只包含实际存在值的精确读取；Record 空 Version 返回字符最大版本。

- [ ] **步骤 1：先写读取契约测试**

覆盖：空 RowKey/Field 列表失败；只返回请求字段；不存在字段返回缺失而不是整行不存在；历史新增字段回填后可读；Record Version `1,2,10` 返回 `2`，定长版本返回业务最大值；请求超过 Key/Field/总组合上限失败。

- [ ] **步骤 2：实现 ReadFields**

DataNode 只执行 RowKey 与 Field/Attribute 的笛卡尔精确 `Get`。PrimaryStore 从同一 Metadata Cache Snapshot 校验并展开 Field ID。TimeSeries 有限时间范围由 PrimaryStore 按 Frequency 生成 RowKey，超过上限返回 `BATCH_TOO_LARGE`。

- [ ] **步骤 3：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datanode/... ./internal/service/primarystore/...)
git add modules/storage/internal/service/datanode modules/storage/internal/service/primarystore
git commit -m "feat(storage): read explicit row keys and fields"
```

### 任务 7：实现独立的 TimeSeries 时间桶清理

**文件：**
- 新建：`modules/storage/internal/service/datanode/pebble/cleanup.go`
- 新建：`modules/storage/internal/service/datanode/pebble/cleanup_test.go`
- 修改：`modules/storage/internal/service/datanode/service.go`
- 新建：`modules/storage/internal/service/primarystore/maintenance.go`
- 新建：`modules/storage/internal/service/primarystore/maintenance_test.go`
- 删除：`modules/storage/internal/service/primarystore/host_metrics_cleanup.go`
- 删除：`modules/storage/internal/service/primarystore/host_metrics_cleanup_test.go`
- 修改：`modules/storage/internal/config/config.go`
- 修改：`modules/storage/config/storage.yaml`

**接口：**
- 输入：Dataset `keep_duration`、`time_bucket_duration` 和当前时间。
- 输出：完整过期桶的 DeleteRange/Compact，不生成业务事件。

- [ ] **步骤 1：先写 Duration 与桶边界测试**

覆盖：`keep_duration=0` 不清理；`keep_duration=90m` 与 `time_bucket_duration=15m` 只删除桶结束时间不晚于 Cutoff 的完整桶；当前半桶保留；Record 永不清理；非法或非正 Bucket Duration 启动失败。

- [ ] **步骤 2：实现 CleanupExpiredBuckets**

```go
cutoff := now.UTC().Add(-keepDuration)
```

按 Bucket Start/End 计算完整过期范围，执行 Pebble `DeleteRange` 后安排 Compact。不得创建 Outbox、修改 View Metadata 或触发 View Build。

- [ ] **步骤 3：证明与 View.keep_duration 无耦合**

设置 Dataset `keep_duration=24h`、View `keep_duration=168h`，运行 Pebble Cleanup 后断言 View 未修改且未启动 Build；触发 View Build 也不得调用 Cleanup RPC。

- [ ] **步骤 4：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/datanode/... ./internal/service/primarystore/... ./internal/config)
git add modules/storage/internal/service/datanode modules/storage/internal/service/primarystore modules/storage/internal/config modules/storage/config/storage.yaml
git commit -m "feat(storage): clean expired time buckets independently"
```

### 任务 8：实现每 Dataset 一个 NATS Subject

**文件：**
- 修改：`packages/jetstream/subject_token.go`
- 修改：`packages/jetstream/subject_token_test.go`
- 新建：`modules/storage/internal/service/viewbuilder/eventconsumer/subject.go`
- 新建：`modules/storage/internal/service/viewbuilder/eventconsumer/subject_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus.go`
- 修改：`modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus_test.go`
- 修改：`modules/eventbus/config/app.yaml`
- 修改：`modules/eventbus/internal/config/config_types.go`
- 修改：`modules/eventbus/internal/config/config_defaults.go`
- 修改：`modules/eventbus/internal/config/config_test.go`
- 修改：`modules/eventbus/internal/registry/registry.go`
- 修改：`modules/eventbus/internal/registry/registry_test.go`
- 修改：`modules/admin/cmd/cli/eventbus_credentials.go`
- 修改：`modules/admin/cmd/cli/eventbus_credentials_test.go`

**接口：**
- 输入：UTF-8 Space/Dataset ID 和 DatasetFieldsChanged。
- 输出：可逆 Dataset Subject、Topic Family、最小权限 ACL 和唯一串行 Consumer。

- [ ] **步骤 1：先写 Subject Token 契约测试**

```go
func EncodeSubjectToken(value string) (string, error)
func DecodeSubjectToken(token string) (string, error)
func DatasetFieldsChangedSubject(prefix, spaceID, datasetID string) (string, error)
func ParseDatasetFieldsChangedSubject(prefix, subject string) (spaceID, datasetID string, err error)
```

覆盖 ASCII、中文、点号、斜杠往返；空值、Padding、大写、非法 Base32、非 Canonical Token 失败。

- [ ] **步骤 2：定义 Subject Family**

```yaml
streams:
  - name: MOOX_STORAGE
    subjects: ["moox.storage.fields_changed.v1.>"]
    retention: interest
    discard: new
    storage: file
    replicas: 1

topic_families:
  - pattern: moox.storage.fields_changed.v1.>
    stream: MOOX_STORAGE
    kind: 1
    payload_content_type: "application/x-protobuf; message=trpc.moox.storage.DatasetFieldsChanged"
    payload_version: 1
    enabled: true
```

`StreamConfig` 新增 `Discard string`，Registry 只接受 `new` 并映射为 `nats.DiscardNew`。删除 TimeSeries/Record RowsCommitted 和按 Shard/Node Token 的旧 Topic。Publisher 在发布前构造 Subject；Consumer 解码 Subject 并拒绝 Payload `space_id/dataset_id` 不一致。

- [ ] **步骤 3：配置唯一 View Consumer**

```yaml
- stream: MOOX_STORAGE
  durable: storage_view
  filter_subject: moox.storage.fields_changed.v1.>
  max_ack_pending: 1
```

Storage Subscriber Options 固定 `MaxInFlight=1`，每次 `Fetch(1)`，处理并 ACK 后才 Fetch 下一条。ActiveView 失败时保持当前 Delivery 未 ACK，定期 `InProgress` 并带 Backoff 重试同一 Handler；进程退出后由 JetStream 重投。不得创建 Active/Rebuild 或每 Dataset Consumer。

- [ ] **步骤 4：更新 ACL**

DataNode Publisher 只能发布 `moox.storage.fields_changed.v1.>`；storage-view 只获得 `storage_view` 的 INFO/NEXT/ACK 权限；Archive/Factor 可按明确 Dataset Subject 授权。测试读取生成 YAML，但不得输出 Token 或 Password。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd packages/jetstream && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/eventbus && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./internal/registry)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder/eventconsumer)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./cmd/cli)
git add packages/jetstream modules/eventbus modules/storage/internal/service/viewbuilder/eventconsumer modules/admin/cmd/cli
git commit -m "feat(storage): publish each dataset on its own subject"
```

### 任务 9：实现无 Progress 的 ViewIndex A/B 槽位

**文件：**
- 修改：`modules/storage/internal/service/viewindex/engine.go`
- 修改：`modules/storage/internal/service/viewindex/engine_test.go`
- 修改：`modules/storage/internal/service/viewindex/batch_write.go`
- 修改：`modules/storage/internal/service/viewindex/batch_write_test.go`
- 修改：`modules/storage/internal/service/viewindex/client.go`
- 修改：`modules/storage/internal/service/viewindex/client_test.go`
- 修改：`modules/storage/internal/service/viewindex/service.go`
- 修改：`modules/storage/internal/service/viewindex/service_test.go`
- 新建：`modules/storage/internal/service/viewindex/slots.go`
- 新建：`modules/storage/internal/service/viewindex/slots_test.go`
- 新建：`modules/storage/internal/service/viewindex/backfill.go`
- 新建：`modules/storage/internal/service/viewindex/backfill_test.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/view_store_apply.go`
- 修改：`modules/storage/internal/service/viewindex/duckdb/view_store_test.go`
- 修改：`modules/storage/internal/service/viewindex/bleve/index.go`
- 修改：`modules/storage/internal/service/viewindex/bleve/index_test.go`

**接口：**
- 输入：ViewIndexApplyBatch、View ID、Slot A/B 和 `LIVE_WRITE/BACKFILL`。
- 输出：无 Sequence/Progress 的幂等字段 Upsert和可删除物理 Slot。

- [ ] **步骤 1：先写跨引擎 Upsert 契约测试**

覆盖：同一 RowKey 重复 Upsert 结果不变；覆盖 Field 得到新值；不同 Dataset 只能写其拥有列；错误 View Revision 提交零数据；DuckDB/Bleve 都没有 Source Progress 表或字段。

- [ ] **步骤 2：先写 Backfill 不覆盖测试**

```text
LIVE_WRITE 先写 close=101，BACKFILL close=100 后仍为 101
BACKFILL 先写 volume=20，LIVE_WRITE close=101 后两列都存在
BACKFILL 可以填充 NULL/缺失列
```

- [ ] **步骤 3：实现 Slot 路径和写入模式**

```go
type Slot string
const (SlotA Slot = "a"; SlotB Slot = "b")

type WriteMode uint8
const (LiveWrite WriteMode = iota + 1; Backfill)
```

每个 View 独占 `slot-a.duckdb/slot-b.duckdb` 或对应 Bleve 目录。

- [ ] **步骤 4：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewindex/...)
git add modules/storage/internal/service/viewindex
git commit -m "feat(storage): add progress-free view slots"
```

### 任务 10：实现单 Consumer 的 ActiveView/NewView 实时写

**文件：**
- 新建：`modules/storage/internal/service/viewbuilder/write_targets.go`
- 新建：`modules/storage/internal/service/viewbuilder/write_targets_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/service.go`
- 修改：`modules/storage/internal/service/viewbuilder/service_test.go`
- 修改：`modules/storage/internal/service/viewbuilder/apply.go`
- 修改：`modules/storage/internal/service/viewbuilder/apply_test.go`
- 删除：`modules/storage/internal/service/viewbuilder/checkpoint.go`
- 删除：`modules/storage/internal/service/viewbuilder/deletes.go`
- 修改：`modules/storage/test/view_derivation_reliability_test.go`

**接口：**
- 输入：单条串行 DatasetFieldsChanged、Subject 中的 Dataset 和原子 ViewWriteTargets。
- 输出：实时双写、ACK 保证和失败 Build 隔离。

- [ ] **步骤 1：先写写目标状态测试**

```go
type ViewWriteTargets struct {
    Active *ViewHandle
    New    *ViewHandle
}
```

覆盖：没有 Build 时只写 Active；有 Build 时写 Active/New；同一个 Subject 的下一事件在当前事件 ACK 前不 Fetch；不同 Dataset 不做 Sequence 比较；切换后 New 成为 Active。

- [ ] **步骤 2：先写 ACK/失败测试**

```text
所有受影响 Active 成功 -> ACK
任一 Active 失败 -> 当前 Delivery 本地重试并 InProgress，下一条未 Fetch
Active/New 全部成功 -> ACK
Active 成功、New 失败 -> Build FAILED、移除 New、当前事件 ACK
New 失败后的下一事件只写 Active
重复投递同一字段事件结果不变
```

- [ ] **步骤 3：实现 Build 失败终止顺序**

```text
Metadata/ViewBuild 标记 FAILED
原子发布 ViewWriteTargets{Active: old, New: nil}
关闭 New Writer
安排删除 New DB
向事件层返回成功以 ACK
```

错误日志包含 View ID、New Slot、Space/Dataset、Subject 和底层 Cause；RPC/Metadata 只保存安全错误。ActiveView 失败不走该 Build 失败分支，而是重试同一 Delivery。

- [ ] **步骤 4：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder ./test -run 'TestViewDerivation')
git add modules/storage/internal/service/viewbuilder modules/storage/test/view_derivation_reliability_test.go
git commit -m "feat(storage): serialize active and new view writes"
```

### 任务 11：实现重建触发器与空闲 Backfill

**文件：**
- 新建：`modules/storage/internal/service/viewbuilder/reconcile.go`
- 新建：`modules/storage/internal/service/viewbuilder/reconcile_test.go`
- 新建：`modules/storage/internal/service/viewbuilder/backfill.go`
- 新建：`modules/storage/internal/service/viewbuilder/backfill_test.go`
- 新建：`modules/storage/internal/service/viewbuilder/build.go`
- 新建：`modules/storage/internal/service/viewbuilder/build_test.go`
- 删除：`modules/storage/internal/service/viewbuilder/recovery.go`
- 删除：`modules/storage/internal/service/viewbuilder/source_reader.go`
- 删除：`modules/storage/internal/service/viewbuilder/source_reader_test.go`
- 新建：`modules/storage/internal/service/primarystore/view_build.go`
- 新建：`modules/storage/internal/service/primarystore/view_build_test.go`

**接口：**
- 输入：Metadata Cache、ActiveView 时间范围、ActiveView 一致性只读事务和实时队列状态。
- 输出：三个确定触发条件、历史 RowKey/旧列复制、新 Field/新 Dataset 精确补全和人工重试 Build。

- [ ] **步骤 1：先写 Reconcile 触发测试**

覆盖：Dataset 新增 Field 导致 Desired Revision 变化时触发关联 View；View 新关联次级 Dataset 时触发；ActiveView 时间跨度 `<= 2*keep_duration` 不触发、超过时触发；`keep_duration=0` 不因时间触发；BUILDING/FAILED 不自动触发；固定倍数 2 不从配置读取。

- [ ] **步骤 2：先写双写边界测试**

获取 View Apply 锁时等待当前实时 Apply 完成；在锁内开启 ActiveView 一致性事务并发布 New Target；释放后下一条消息同时写两边。证明不存在“未进入旧事务、又未写 NewView”的事件，不使用 Sequence 或 Progress。

- [ ] **步骤 3：实现旧 ActiveView 基线读取**

TimeSeries 按 `started_at - keep_duration` 过滤 RowKey，Record 全部保留。View 新关联 Dataset 只能是共享相同 View Grain 的次级 Dataset，使用旧 ActiveView RowKey 精确读取，不从新 Dataset 枚举 Key。

- [ ] **步骤 4：实现新增字段和新 Dataset 补全**

比较 Active/Desired Revision 得到新增列和来源 Dataset/Field。按 Dataset 分组旧 RowKey，经 PrimaryStore 分批调用 DataNode `ReadFields`；Pebble 中缺失的历史 Field 保持 NULL。

- [ ] **步骤 5：实现实时优先调度**

有实时事件排队或 Apply 中时不开始下一 Backfill Batch。每批最多 100 行或 50ms；NewView 单 Writer；BACKFILL 只填缺失值。失败时标记 FAILED、移除并删除 NewView，等待用户手动重建。

- [ ] **步骤 6：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder ./internal/service/primarystore -run 'Test.*Reconcile|Test.*Backfill|Test.*Build')
git add modules/storage/internal/service/viewbuilder modules/storage/internal/service/primarystore
git commit -m "feat(storage): reconcile and backfill view builds"
```

### 任务 12：实现原子切换与 OldView 整库删除

**文件：**
- 修改：`modules/storage/internal/service/viewbuilder/build.go`
- 修改：`modules/storage/internal/service/viewbuilder/build_test.go`
- 新建：`modules/storage/internal/service/dataview/active_handle.go`
- 新建：`modules/storage/internal/service/dataview/active_handle_test.go`
- 修改：`modules/storage/internal/service/dataview/service.go`
- 修改：`modules/storage/internal/service/dataview/service_test.go`
- 修改：`modules/storage/test/view_index_switch_test.go`

**接口：**
- 输入：完成 Backfill 的 NewView、View Apply 锁和 Metadata CAS。
- 输出：最多 2 秒的 Active Slot 切换、不可变 ActiveHandle 和延迟删除 OldView。

- [ ] **步骤 1：先写切换并发测试**

覆盖：获取 Apply 锁后没有实时 Apply 在途；Build 仍为 BUILDING 且 Targets 指向同一 New；旧查询跨切换继续使用旧 Handle；新查询使用新 Slot；新字段切换前不可见、切换后一次可见；OldView 在引用归零前不删除。

- [ ] **步骤 2：实现 ActiveHandle 生命周期**

```go
type ActiveHandle struct {
    IndexID string
    Slot Slot
    Revision uint64
    Columns []Column
}
```

Handle 不保存 Source Progress。请求开始 Acquire，结束 Release；发布后内容不可修改。

- [ ] **步骤 3：实现 2 秒有界 CAS**

```text
获取 View Apply 锁
验证 Build/Targets
Metadata CAS active_slot + active_view_revision
发布新 ActiveHandle
移除旧 Active Target
释放锁
```

进入 CAS 前超过 2 秒返回 `VIEW_SWITCH_TIMEOUT` 并保持双写；CAS 响应不确定时重新读取 Metadata 决定是否完成本地发布。

- [ ] **步骤 4：删除 OldView DB**

旧 Handle 引用归零并经过 Grace Period 后关闭并删除旧 DuckDB/Bleve 整库；删除失败记录安全错误并由维护任务重试，不回滚已完成切换。

- [ ] **步骤 5：运行测试并提交**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/dataview ./internal/service/viewbuilder ./internal/service/viewindex/... ./test -run 'TestViewIndexSwitch')
git add modules/storage/internal/service/dataview modules/storage/internal/service/viewbuilder modules/storage/internal/service/viewindex modules/storage/test/viewindex_switch_test.go
git commit -m "feat(storage): switch view slots atomically"
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

**接口：**
- 输入：任务 2-12 的最终 API。
- 输出：没有旧 Shard、不可变 Fact、Sequence/Progress、Scan/Delete 和旧 Topic 残留的完整仓库。

- [ ] **步骤 1：更新 Seed、CLI 和 UI**

```yaml
data_nodes:
  - node_id: data-node-market
    service_target: ip://127.0.0.1:20106
    status: active

datasets:
  - space_id: crypto
    dataset_id: binance_spot_kline
    data_node_id: data-node-market
    keep_duration: 4320h
```

删除 Required、Dimensions、Route、Device 和可修改 Dataset Node 的 UI。Dataset/View 页面显示“保存时长”；Record 固定为 0。Field 页面只允许新增字段。

- [ ] **步骤 2：更新调用方语义**

Collector/Factor 使用 `WriteFields` 部分 Upsert；因子新增后允许按历史 RowKey 补数。Archive/Factor 按明确 Dataset Subject 订阅，范围读取通过 PrimaryStore 有限 RowKey 生成接口；Record 用户文档明确 Version 字符排序规则。

- [ ] **步骤 3：执行零残留扫描**

```bash
rg -n 'DataShard|datashard|data_shard|storage-shard|storage_shard_|shard_id|source_shard_id|source_sequence' modules packages web scripts examples docs --glob '!docs/superpowers/**'
rg -n 'FactKey|RowMarker|content_hash|FACT_VERSION_IMMUTABLE|node_sequence|DatasetProgress|GetDatasetProgress|ViewIndexSourceProgress|base_progress|expected_last_applied_sequence' modules packages web scripts examples --glob '!**/storagegen/**'
rg -n 'MergeRows|WriteRows|ReadRows|ScanRows|DeleteRows|BeginNodeSnapshot|ScanNodeSnapshot|EndNodeSnapshot' modules packages web scripts examples --glob '!**/storagegen/**'
rg -n 'attributes_to_delete|removed_column_names|removed_columns|dimensions|dimension_hash|reservedDimensionsAttribute|keep_days' modules/storage modules/cli/config web/src/api/storage web/src/views/data scripts examples --glob '!**/storagegen/**'
rg -n 'rows_committed|time_series\.rows_committed|record\.rows_committed|storage_view_active|storage_view_rebuild' modules packages web scripts examples --glob '!**/storagegen/**'
rg -n 'record\.required|form\.required|DatasetColumn.*Required|GetRequired\(\)|c_required|required_column_names' modules web scripts examples --glob '!**/storagegen/**'
```

预期：活动代码零匹配。普通英文输入提示中的 `required` 必须逐条确认。

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
git add modules packages web scripts examples docs
git commit -m "refactor(storage): integrate field upsert architecture"
```

### 任务 14：完成两服务器 E2E、两轮审查和最终交付

**文件：**
- 新建：`scripts/e2e/storage-field-upsert.sh`
- 新建：`scripts/e2e/storage-field-upsert_test.sh`
- 修改：`.gitignore`
- 修改：`modules/storage/README.md`
- 修改：`docs/运维/数据保留与磁盘空间.md`
- 更新：`docs/superpowers/plans/2026-07-19-storage-consistency-review-remediation.md`

**接口：**
- 输入：最终候选 HEAD、忽略的 `custom.toml` 和两个远程节点。
- 输出：本地门禁、两轮独立审查、两服务器 E2E 和同步远程分支。

- [ ] **步骤 1：先写 E2E 脚本契约测试**

覆盖：缺少 custom.toml 时安全失败；必须选择两个不同启用节点；日志不包含密码/Token/私钥/展开 SSH 命令；任一阶段失败返回非零；退出清理自身远程目录和进程。

- [ ] **步骤 2：实现安全 E2E Harness**

只输出 Run ID、逻辑主机名、阶段、耗时和结果。远端目录为随机 `/tmp/moox-storage-e2e-<run-id>`；不得打印凭证。

- [ ] **步骤 3：运行最终本地门禁**

```bash
make verify
make test-docs-architecture
pnpm docs:build
bash scripts/e2e/storage-field-upsert_test.sh
git diff --check
```

预期：全部 PASS；文档构建允许既有非致命 Highlight/Chunk Warning。

- [ ] **步骤 4：执行第一轮独立审查**

重点审查 RowKey、snapshotcache、Field Upsert、内部 Outbox ID、Dataset Subject/Token、单条 ACK 和时间桶清理。修复全部 P1/P2 后提交：

```bash
git add -A
git commit -m "fix(storage): address field storage review"
```

- [ ] **步骤 5：执行第二轮独立审查**

使用新的 Reviewer，重点审查 View 触发器、双写边界、Backfill 实时优先、无 Progress 切换、OldView 生命周期和跨模块调用方。修复全部 P1/P2 后提交：

```bash
git add -A
git commit -m "fix(storage): address view rebuild review"
```

- [ ] **步骤 6：从最终 HEAD 运行两服务器 E2E**

```bash
bash scripts/e2e/storage-field-upsert.sh --config custom.toml
```

必须验证：两个 DataNode 上的 Dataset 发布不同 Subject；Token/Payload 校验；历史 Field 补写和覆盖；Outbox 失败不越序；Consumer Batch 1/MaxAckPending 1；三个重建触发条件；实时双写；空闲 Backfill；失败 Build 人工重试；查询无感切换；独立 Cleanup；OldView 删除。

- [ ] **步骤 7：记录最终证据并更新计划勾选**

记录最终 HEAD、命令、时间、两个逻辑节点、阶段结果和安全错误摘要。不得记录凭证或远程展开命令。

- [ ] **步骤 8：推送已验证分支**

```bash
git status --short
git push -u origin codex/storage-field-upsert
git status --short --branch
git rev-parse HEAD
git rev-parse '@{upstream}'
```

预期：工作区干净，HEAD 等于 Upstream，两轮审查关闭，两服务器 E2E 从最终 HEAD 通过。

## 最终验收清单

- [ ] Metadata 只接受 v4，`metadataSchemaVersionCompatible` 已删除。
- [ ] Dataset 直接保存不可修改的 `data_node_id`，没有 Placement、Route、Hash 或迁移工具。
- [ ] Metadata 读取复用现有 snapshotcache，没有 Runtime Catalog。
- [ ] 所有 Field 可缺失且只增不减，没有 Required 或 Dimensions。
- [ ] RowKey 取代 FactKey；Field/Attribute 分别使用 `0x01/0x02`，不存在 RowMarker。
- [ ] `WriteFields` 允许新增、覆盖和补写历史 Field。
- [ ] DataNode 只提供指定 RowKey/Field 的精确读取，没有完整行、Scan、Snapshot、Progress 或 Delete RPC。
- [ ] 字段 Upsert、内部 Outbox 条目和下一 ID 在一个 Pebble Batch 中提交。
- [ ] TimeSeries 使用可配置时间桶和 `keep_duration`；Record 不自动清理。
- [ ] Dataset/View `keep_duration` 分别生效且互不调用。
- [ ] 每个 Dataset 发布到 `moox.storage.fields_changed.v1.<space-token>.<dataset-token>`。
- [ ] Subject Token 可逆，Subject 与 Payload 不一致时拒绝消费。
- [ ] 事件没有 Node/Dataset Sequence，ViewIndex 没有 Source Progress。
- [ ] JetStream 只有一个 `storage_view` Durable，`MaxAckPending=1`、`FetchBatch=1`。
- [ ] 每个 View 使用独立 slot-a/slot-b DB，角色按 ActiveView/NewView/OldView 转换。
- [ ] Field/关联 Dataset/`2 * keep_duration` 三种变化能触发重建。
- [ ] 实时事件优先；Backfill 每批最多 100 行或 50ms，且只补缺失值。
- [ ] NewView/Backfill 失败只终止 Build，ActiveView 继续并等待人工重建。
- [ ] 重建 RowKey 只来自旧 ActiveView；新关联 Dataset 按相同 Grain 精确补列。
- [ ] 切换持锁不超过 2 秒，查询不中断，OldView 在引用归零后整库删除。
- [ ] 本地完整门禁、两轮独立审查和最终两服务器 E2E 全部通过。
