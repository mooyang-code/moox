# Storage Dataset 单归属与在线 View 重建 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Storage 重构为单 PrimaryStore、多个单归属 DataNode、单 storage-view 的多进程系统，支持 Dataset 按机器部署、字段运行时追加、JetStream 可靠派生，以及查询无感的 View A/B 重建与切换。

**Architecture:** `storage-primary` 持有 Schema v4 Metadata SQLite、不可热变的 Dataset 路由和可原子追加的 DatasetSchema；每个 Dataset 只属于一个 DataNode，DataNode 在一个 Pebble Batch 中提交事实、Node Sequence、Dataset Progress 和 Outbox。`storage-view` 使用固定 `storage_view_active` 与 `storage_view_rebuild` 两个 Durable Consumer，通过 DataNode Snapshot、Source Checkpoint、Catch-up 和最多 2 秒的有界切换维护跨 DataNode View。

**Tech Stack:** Go 1.25、tRPC-Go、Protocol Buffers、SQLite、Pebble、NATS JetStream、DuckDB、Bleve、Vue 3、TypeScript、Shell。

## Global Constraints

- 本计划的设计事实源是 `docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`；本文件取代 2026-07-18 Shard 计划和本文件旧版本的冲突内容。
- 这是全新项目：不迁移旧数据库、Pebble、Proto、RPC、配置或 Seed；不保留 Alias、Deprecated 字段、双读、双写和兼容分支。
- Metadata Schema 版本固定为 `4`；启动遇到非 v4 数据库必须报错，由用户清理数据并重新部署。
- 每个 Dataset 直接保存一个 `data_node_id`；一个 DataNode 可承载多个 Dataset，同一个 Dataset 不继续分片。
- Dataset 产生过事实后永久禁止修改 `data_node_id`；系统不提供 Dataset 迁移、复制、校验、切换或回滚工具。
- 已有字段不删除，`field_id` 不修改或复用，`value_type` 不修改；所有字段均可缺失，不存在 Required 字段或 Required 校验。
- 只有字段列表可以运行时追加；DataNode、`service_target`、Dataset 和 `data_node_id` 的变化要求重启 `storage-primary`。
- PrimaryStore 是字段归属和值类型的唯一业务 Schema 校验入口；DataNode 不保存 Schema Manifest、Schema Revision 或 Schema Fence。
- `storage-primary`、`storage-view` 各只运行一个实例；每个 `node_id` 只运行一个写 Owner，不实现 Leader Election、Replica、Quorum 或自动 Failover。
- Storage 内部 RPC 使用直接 tRPC 和 Service HMAC，不经过 Node Service Gateway；DataNode 写 RPC 只允许 `storage-primary` 身份。
- `MOOX_STORAGE` 使用 `InterestPolicy + DiscardNew`；Outbox 只允许 inspect 和 retry，不允许 force-skip。
- View 全系统同时只运行一个 Build，只创建 `storage_view_active` 和 `storage_view_rebuild` 两个固定 Durable Consumer。
- View 切换最多暂停 Active Consumer 2 秒；DataView 查询不能暂停，超时必须恢复旧 Active 更新。
- E2E 可以读取仓库根 `custom.toml`，但不得打印、提交或写入快照任何账号、密码、Token、私钥或展开后的 SSH 命令。
- 实施必须在独立 Worktree 完成，至少经过两轮相互独立的代码审查，并完成两服务器 E2E 后才能宣告完成。

---

## File Map

### Protocol and Metadata

- Rename `modules/storage/proto/store.proto` to `modules/storage/proto/data_node.proto`: DataNode facts, progress and snapshot RPC.
- Rename `modules/storage/proto/view.proto` to `modules/storage/proto/data_view.proto`: public DataView query RPC.
- Modify `modules/storage/proto/common.proto`: `source_node_id` and typed errors.
- Modify `modules/storage/proto/metadata.proto`: DataNode, Dataset placement, append-only fields, desired/active View revisions and simplified Build state.
- Modify `modules/storage/proto/rows.proto`: public facts, Dataset Progress API and final naming.
- Modify `modules/storage/proto/rows_committed.proto`: `node_id + dataset_id + sequence`, without Schema Revision.
- Modify `modules/storage/proto/view_index.proto`: Source Checkpoint and revision-only atomic Apply.
- Modify `modules/storage/schema/metadata.sql`: fresh Schema v4 only.
- Regenerate `modules/storage/proto/storagegen/*`.

### Runtime Boundaries

- Create `modules/storage/internal/catalog/runtime.go`: immutable routing plus atomic per-Dataset Schema pointers.
- Create `modules/storage/internal/catalog/runtime_test.go`: load, append, concurrency and immutability tests.
- Rename `modules/storage/internal/service/datashard` to `modules/storage/internal/service/datanode`.
- Delete `modules/storage/internal/service/primarystore/shardrouter`.
- Create `modules/storage/internal/service/primarystore/datanodes.go`: direct tRPC client pool keyed by `node_id`.
- Create `modules/storage/internal/service/datanode/snapshot.go`: bounded in-process Pebble Snapshot registry.
- Create `modules/storage/internal/service/viewbuilder/build.go`: single rebuild orchestration.
- Create `modules/storage/internal/service/dataview/active_handle.go`: atomic active Index/Revision/Columns/Checkpoint handle.

### Cross-Module Integration

- Modify `modules/eventbus/internal/config/*`, `modules/eventbus/config/app.yaml` and `modules/eventbus/internal/registry/registry.go`: Interest/Discard configuration and two fixed View consumers.
- Modify `modules/admin/cmd/cli/eventbus_credentials.go`: least-privilege ACL for both View consumers.
- Modify `modules/cli/internal/command/metadata_implementation.go`: v4 DataNode/Dataset seed import.
- Modify `modules/monitor/internal/metrics/storage.go`: remove Required and PrimaryStoreRoute assumptions.
- Modify `modules/archive` and `modules/factor`: consume Node-named RowsCommitted and call Storage directly.
- Modify `web/src/api/storage/*` and `web/src/views/data/datasets/components/dataset-column-panel.vue`: append-only field UI with no Required/edit mode.
- Modify Storage deploy/release scripts, configs, examples, metrics and current architecture docs in the same rename sweep.

## Superseded CR Decisions

| Previous finding | Final disposition |
| --- | --- |
| Worker error was dropped after enqueue ACK | Retained: Active/Rebuild handlers ACK only after durable ViewIndex success or checkpoint no-op |
| A failed Shard lane became permanently poisoned | Retained, renamed: retry of the same Node Sequence can recover its Node lane |
| DataShard accepted a larger payload than JetStream | Retained: DataNode validates the final `MooxMessage` with the publisher's exact Max Payload before Pebble commit |
| Required-after-merge and Schema Fence were proposed | Removed: all fields are optional; PrimaryStore validates incoming field attributes, DataNode is Schema-agnostic |
| Per-Build Durable Consumer and resumable lease/cursor were proposed | Removed: one fixed Rebuild Consumer, one global Build, crash means FAILED and rebuild from a fresh Snapshot |
| Cross-Target pagination needed K-way merge | Removed: one Dataset has one DataNode; PrimaryStore delegates one bounded cursor scan |
| Force-skip plus DeliveryGap was proposed | Removed: Outbox exposes inspect/retry only and remains backpressured until repaired |
| Node Service Gateway was retained | Removed for Storage internals: use direct tRPC with Service HMAC |

## Correctness Invariants

1. A public write request contains rows for exactly one Dataset and resolves to exactly one DataNode.
2. PrimaryStore rejects unknown fields, duplicate Field IDs, value-type mismatches, invalid TypedValue payloads and public batch-limit violations before DataNode RPC.
3. DataNode atomically commits complete post-Merge facts, `node_sequence`, `DatasetProgress.last_committed_sequence` and RowsCommitted Outbox.
4. DataNode rejects the whole batch before commit when the final deterministic `MooxMessage` exceeds the configured JetStream Max Payload.
5. Outbox publishes in Node Sequence order and only deletes a contiguous successful prefix.
6. `RowsCommitted` never contains Schema Revision and always identifies `{node_id, dataset_id, sequence}`.
7. Each physical ViewIndex owns an independent `{node_id, dataset_id}` Source Checkpoint.
8. `sequence <= last_applied_sequence` is a successful per-ViewIndex no-op; the event is ACKed only after every affected ViewIndex applied or skipped it.
9. MERGE writes only columns owned by the changed Dataset; a missing row fails the whole Apply without advancing Checkpoint, then retries a full-source REPLACE.
10. Primary-Dataset DELETE removes the View row; secondary-Dataset DELETE removes only that Dataset's owned columns.
11. Rebuild Snapshot rows and each source's baseline Checkpoint come from the same Pebble Snapshot.
12. Rebuild Consumer ignores events at or below the source Snapshot barrier and applies later events until it reaches the Dataset Progress Vector.
13. Active Index activation switches Index, Revision, Columns and Source Checkpoints as one logical handle; old events cannot regress the new Index.
14. Add Dataset Field returns success only after SQLite commit and atomic Runtime Schema replacement.
15. A View rebuild never changes the public query schema before activation and never makes a half-built Index queryable.

## Implementation Order

| Phase | Tasks | Exit condition |
| --- | --- | --- |
| A. Final contracts | 1-4 | v4 schema, final names, runtime catalog and direct process boundaries compile |
| B. Authoritative facts | 5-7 | single-node routing, strict Primary validation, atomic DataNode commit and Snapshot pass |
| C. Event and View consistency | 8-12 | two fixed consumers, Source Checkpoints, rebuild and 2-second activation pass |
| D. Integration and proof | 13-14 | repository rename sweep, two reviews, local gates and two-server E2E pass |

### Task 1: Create the Isolated Worktree and Freeze the Baseline

**Files:**
- Read: `docs/superpowers/specs/2026-07-19-storage-dataset-node-simplification-design.md`
- Read: `docs/superpowers/plans/2026-07-19-storage-consistency-review-remediation.md`
- Read: `scripts/test-storage-boundary-contract.sh`
- Read: `scripts/test-storage-consistency-contract.sh`

**Interfaces:**
- Consumes: approved design and current `origin/main`.
- Produces: clean `codex/storage-dataset-node` Worktree and baseline evidence.

- [ ] **Step 1: Create a clean Worktree from current main**

```bash
git fetch origin
git worktree add ../moox-storage-dataset-node -b codex/storage-dataset-node origin/main
cd ../moox-storage-dataset-node
git status --short --branch
```

Expected: clean branch at current `origin/main`. Do not implement this refactor in the documentation branch.

- [ ] **Step 2: Run the pre-change baseline**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
bash scripts/test-storage-boundary-contract.sh
bash scripts/test-storage-consistency-contract.sh
make verify
```

Expected: record every existing failure before editing. A baseline failure must be understood and either repaired in its owning Task or documented as unrelated before proceeding.

- [ ] **Step 3: Record the implementation base without editing generated artifacts**

```bash
git rev-parse HEAD
git status --porcelain=v1
```

Expected: a recorded base SHA and empty status.

### Task 2: Replace the Protocol and Metadata Schema with the Final v4 Model

**Files:**
- Rename: `modules/storage/proto/store.proto` -> `modules/storage/proto/data_node.proto`
- Rename: `modules/storage/proto/view.proto` -> `modules/storage/proto/data_view.proto`
- Modify: `modules/storage/proto/common.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/rows.proto`
- Modify: `modules/storage/proto/rows_committed.proto`
- Modify: `modules/storage/proto/view_index.proto`
- Modify: `modules/storage/proto/Makefile`
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/bootstrap/metadata/schema_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store_test.go`
- Regenerate: `modules/storage/proto/storagegen/*`
- Modify: `scripts/test-storage-boundary-contract.sh`

**Interfaces:**
- Consumes: none.
- Produces: `DataNode`, `DatasetProgress`, `ViewIndexSourceCheckpointUpdate`, `AddDatasetField`, desired/active revisions and Schema v4.

- [ ] **Step 1: Make the boundary test reject every superseded contract**

Add exact scans that reject active references outside historical `docs/superpowers/**`:

```text
DataShard, datashard, data_shard, ShardKey, ShardRow, ShardTarget
shard_id, source_shard_id, GetShardState, GetShardHeads, ShardCheckpoint
schema_revision, DatasetSchemaFence, required_column_names
DatasetColumn.required, c_required, PrimaryStoreRoute, t_primary_store_routes
role: shard, storage-shard, storage_shard_
```

Run:

```bash
bash scripts/test-storage-boundary-contract.sh
```

Expected: FAIL on current active code, not on the migration table inside the approved spec.

- [ ] **Step 2: Define the final DataNode wire contract**

Use the following contract shape in `data_node.proto`; request messages carry logical identity, never endpoint, device, weight or hash rules:

```proto
message FactKey {
  string space_id = 1;
  string dataset_id = 2;
  DataKind data_kind = 3;
  string key = 4;
  string version = 5;
}

message FactRow {
  FactKey key = 1;
  repeated ColumnValue columns = 2;
  map<string, string> attributes = 3;
  repeated string attributes_to_delete = 4;
  repeated string removed_column_names = 5;
  string source_node_id = 6;
  uint64 source_sequence = 7;
  repeated ColumnRemoval removed_columns = 8;
}

message DatasetProgress {
  string node_id = 1;
  string dataset_id = 2;
  uint64 last_committed_sequence = 3;
}

service DataNode {
  rpc MergeRows(MergeRowsReq) returns (MergeRowsRsp);
  rpc ReadRows(ReadRowsReq) returns (ReadRowsRsp);
  rpc ScanRows(ScanRowsReq) returns (ScanRowsRsp);
  rpc DeleteRows(DeleteRowsReq) returns (DeleteRowsRsp);
  rpc GetNodeState(GetNodeStateReq) returns (GetNodeStateRsp);
  rpc GetDatasetProgress(GetDatasetProgressReq) returns (GetDatasetProgressRsp);
  rpc BeginNodeSnapshot(BeginNodeSnapshotReq) returns (BeginNodeSnapshotRsp);
  rpc ScanNodeSnapshot(ScanNodeSnapshotReq) returns (ScanNodeSnapshotRsp);
  rpc EndNodeSnapshot(EndNodeSnapshotReq) returns (EndNodeSnapshotRsp);
}
```

All DataNode requests include `node_id`; row requests also include one `dataset_id`. Delete every physical target field and generated old service.

- [ ] **Step 3: Define the final Metadata and View contracts**

Apply these exact semantics:

```proto
message DataNode {
  string node_id = 1;
  string service_target = 2;
  string status = 3;
}

message Dataset {
  // existing business fields retain their v4 numbers
  string data_node_id = 12;
}

message AddDatasetFieldReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string dataset_id = 3;
  Field field = 4;
  DatasetColumn column = 5;
}

message ViewIndexSourceCheckpointUpdate {
  string node_id = 1;
  string dataset_id = 2;
  uint64 expected_last_applied_sequence = 3;
  uint64 last_applied_sequence = 4;
}
```

`DatasetColumn` has no `required`. Rename `view_version` to `desired_view_revision`, `active_view_version` to `active_view_revision`; delete View Schema Hash. `ViewIndexBuild` retains only identity, target revision, `BUILDING/CATCHING_UP/FAILED/ACTIVE`, columns, coverage, counts, timestamps and safe error text; delete PREPARING, READY, owner, lease, cursor and snapshot-end persistence.

- [ ] **Step 4: Replace Metadata SQL with fresh Schema v4**

`metadata.sql` must store `schema_version=4`, add `t_data_nodes`, add non-empty `t_datasets.c_data_node_id`, remove `t_primary_store_nodes`, `t_primary_store_routes`, topology locks and `t_dataset_columns.c_required`, and simplify View/Build columns to match Proto.

`metadataSchemaVersionCompatible` becomes strict:

```go
const metadataSchemaVersion = "4"

func metadataSchemaVersionCompatible(version string) bool {
    return version == metadataSchemaVersion
}
```

Tests must prove v2/v3/v5 fail and v4 passes. Do not add ALTER statements or migration code.

- [ ] **Step 5: Regenerate and verify the protocol**

```bash
make -C modules/storage/proto clean all
(cd modules/storage/proto/storagegen && env GOCACHE=/tmp/moox-gocache go test -count=1 ./...)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap/metadata ./internal/service/metadata/sqlite)
git diff --check
```

Expected: generated code compiles; Schema v4 tests pass; old contract scan still fails only because later runtime Tasks are not complete.

- [ ] **Step 6: Commit the contract atomically**

```bash
git add modules/storage/proto modules/storage/schema modules/storage/internal/bootstrap/metadata modules/storage/internal/service/metadata/sqlite scripts/test-storage-boundary-contract.sh
git commit -m "refactor(storage): define dataset node v4 contracts"
```

### Task 3: Implement Direct SQLite Metadata and the Dynamic Runtime Catalog

**Files:**
- Create: `modules/storage/internal/catalog/runtime.go`
- Create: `modules/storage/internal/catalog/runtime_test.go`
- Modify: `modules/storage/internal/service/metadata/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_dataset.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_test.go`
- Delete: `modules/storage/internal/service/metadata/cache/*`
- Modify: `modules/storage/internal/service/primarystore/service.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_catalog.go`
- Modify: `modules/storage/internal/service/primarystore/metadata_catalog_test.go`

**Interfaces:**
- Consumes: v4 `DataNode`, `Dataset`, `Field`, `DatasetColumn`, `View`.
- Produces: `catalog.Runtime`, `catalog.DatasetSchema`, transactional `AddDatasetField`, direct SQL pagination.

- [ ] **Step 1: Write failing Runtime Catalog tests**

Cover these cases:

```go
func TestRuntimeCatalogRoutingIsImmutable(t *testing.T)
func TestRuntimeCatalogAppendFieldSwapsSchemaAtomically(t *testing.T)
func TestRuntimeCatalogInFlightReaderKeepsOldSchema(t *testing.T)
func TestRuntimeCatalogConcurrentAppendIsSerializedPerDataset(t *testing.T)
func TestRuntimeCatalogRejectsUnknownDatasetAppend(t *testing.T)
```

Run:

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/catalog)
```

Expected: FAIL because the package does not exist.

- [ ] **Step 2: Implement immutable entries with atomic Schema pointers**

Use focused types:

```go
type DatasetKey struct { SpaceID, DatasetID string }
type DatasetRoute struct { DataNodeID, ServiceTarget string }
type DatasetSchema struct {
    DataKind storagepb.DataKind
    Columns map[string]ColumnSchema
}
type DatasetEntry struct {
    Route DatasetRoute
    schema atomic.Pointer[DatasetSchema]
    updateMu sync.Mutex
}
type Runtime struct {
    datasets map[DatasetKey]*DatasetEntry
    nodes map[string]DataNodeRoute
}
```

Maps and routes are built once at startup and never mutated. `AppendDatasetField` clones the old Schema and stores a new pointer; it never mutates maps held by in-flight requests.

- [ ] **Step 3: Replace generic Metadata Cache with direct SQL**

Delete Cache construction and refresh calls. Every List method must execute deterministic SQL with `ORDER BY`, `LIMIT` and `OFFSET`; no List may load the full table and page in Go. Add a query-count test with more than two pages and assert the returned order and `has_more`.

- [ ] **Step 4: Implement transactional, idempotent Add Dataset Field**

Inside one per-Dataset update lock:

1. Read current Field, DatasetColumn and Runtime Schema.
2. Validate stable Field ID/type and DatasetColumn identity.
3. Build the next immutable Schema before beginning the transaction.
4. In one SQLite transaction, insert the Field when absent and insert the DatasetColumn.
5. Commit SQLite.
6. Store the new Schema pointer.
7. Return success only after pointer replacement.

Same Dataset/Field/Type is success; conflicting type, origin or semantics returns `FIELD_IMMUTABLE`. Update/Delete of a bound column returns `FIELD_IMMUTABLE`. A test must simulate failure after SQLite commit but before response, reopen the service, retry, and obtain the same field without duplication.

- [ ] **Step 5: Enforce placement immutability through an injected progress reader**

Define:

```go
type DatasetProgressReader interface {
    GetDatasetProgress(context.Context, string, string) (*storagepb.DatasetProgress, error)
}
```

`UpdateDataset` may change `data_node_id` only when the current Owner responds with `last_committed_sequence == 0`. Owner unavailable, unknown progress or positive progress returns a typed immutable-placement error. Deleting rows never resets progress.

- [ ] **Step 6: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/catalog ./internal/service/metadata/sqlite ./internal/service/primarystore)
git add modules/storage/internal/catalog modules/storage/internal/service/metadata modules/storage/internal/service/primarystore
git commit -m "refactor(storage): add dynamic runtime catalog"
```

### Task 4: Establish the Three Process Roles and Direct HMAC RPC

**Files:**
- Modify: `packages/gatewayauth/trpc.go`
- Modify: `packages/gatewayauth/trpc_test.go`
- Rename: `modules/storage/config/storage.shard.yaml` -> `modules/storage/config/storage.node.yaml`
- Rename: `modules/storage/config/trpc_go.shard.yaml` -> `modules/storage/config/trpc_go.node.yaml`
- Modify: `modules/storage/config/storage.primary.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/view_runtime.go`
- Create: `modules/storage/internal/service/primarystore/datanodes.go`
- Create: `modules/storage/internal/service/primarystore/datanodes_test.go`

**Interfaces:**
- Consumes: Runtime Catalog routes and generated DataNode client.
- Produces: `primary`, `node`, `view` roles and direct authenticated RPC clients.

- [ ] **Step 1: Write failing deployment-boundary tests**

Tests must assert:

```text
roles accepts only primary, node, view
primary cannot embed DataNode
node requires node_id and a Pebble path
view cannot open Metadata SQLite or Pebble facts
DataNode has no HTTP listener or Admin Gateway route
Storage internal clients do not call ServiceGatewayTarget
```

- [ ] **Step 2: Add direct tRPC signing without a Gateway hop**

Reuse the existing canonical HMAC envelope, but add a direct option constructor that does not resolve `MOOX_SERVICE_GATEWAY_TARGET`:

```go
func NewDirectTRPCClientOptions(target string, credentials Credentials) []client.Option
```

Add a server filter that verifies serialized request body, caller, callee and method against a configured key resolver. DataNode write methods allow only `storage-primary`; Snapshot methods allow only `storage-view`; PrimaryStoreScan allows `storage-view` and `archive`.

- [ ] **Step 3: Replace the single Primary client with a DataNode client pool**

Implement:

```go
type DataNodeClientPool interface {
    Client(nodeID string) (datanode.Client, error)
}
```

The pool is built from immutable Runtime Catalog nodes, lazily creates direct tRPC proxies by `service_target`, and never accepts an endpoint from a request. Unit tests must prove two Dataset IDs resolve to different clients and the same `node_id` reuses one proxy.

- [ ] **Step 4: Register only role-owned services**

`primary` registers Metadata, PrimaryStore and PrimaryStoreScan; `node` registers DataNode; `view` registers DataView and ViewIndex and starts both View consumers. Remove embedded DataNode creation from PrimaryStore. Single-machine development still launches three processes against loopback targets.

- [ ] **Step 5: Run tests and commit**

```bash
env GOCACHE=/tmp/moox-gocache go test -count=1 ./packages/gatewayauth
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./cmd/server ./internal/service/primarystore)
git add packages/gatewayauth modules/storage/config modules/storage/internal/config modules/storage/cmd/server modules/storage/internal/service/primarystore
git commit -m "refactor(storage): split primary node and view roles"
```

### Task 5: Make PrimaryStore the Sole Business Schema Boundary

**Files:**
- Modify: `modules/storage/internal/service/primarystore/schema/validator.go`
- Modify: `modules/storage/internal/service/primarystore/schema/validator_test.go`
- Modify: `modules/storage/internal/service/primarystore/data.go`
- Modify: `modules/storage/internal/service/primarystore/data_test.go`
- Modify: `modules/storage/internal/service/primarystore/factreader.go`
- Modify: `modules/storage/internal/service/primarystore/factreader_test.go`
- Modify: `modules/storage/internal/service/primarystore/errors.go`
- Modify: `modules/storage/internal/retinfo/retinfo.go`
- Modify: `modules/storage/internal/retinfo/retinfo_test.go`
- Delete: `modules/storage/internal/service/primarystore/shardrouter/*`

**Interfaces:**
- Consumes: `catalog.Runtime.Dataset(spaceID,datasetID)` and `DataNodeClientPool.Client(nodeID)`.
- Produces: one-Dataset reads/writes with typed validation errors.

- [ ] **Step 1: Add the complete failing validation matrix**

Tests must cover empty batch, more than 1,000 rows, more than 4 MiB public request, more than 1 MiB per row, nil key, mixed Dataset, duplicate row key, duplicate Field ID, unknown field, wrong `value_type`, wrong TypedValue oneof, invalid TIME, invalid JSON and NaN/Inf. A row omitting every Dataset field must pass because all fields are optional.

- [ ] **Step 2: Validate only through the Runtime DatasetSchema**

Implement exact entry points:

```go
func (v *Validator) ValidateTimeSeriesBatch(rows []*storagepb.TimeSeriesRow, schema *catalog.DatasetSchema) error
func (v *Validator) ValidateRecordBatch(rows []*storagepb.RecordRow, schema *catalog.DatasetSchema) error
```

Do not query SQLite per row. Do not perform Required-after-Merge checks. Validation stops before DataNode RPC.

- [ ] **Step 3: Delete multi-target routing and aggregation**

Each public request resolves one Dataset entry, obtains one DataNode client and sends one RPC. Dataset scans delegate one bounded cursor to one DataNode. Remove target grouping, weighted routing, Hash/Pattern matching, 10,000-row cross-target aggregation and partial `written_keys` caused by multi-target writes.

- [ ] **Step 4: Return stable safe errors**

Use typed errors for `DATA_NODE_UNAVAILABLE`, `DATASET_NOT_ASSIGNED`, `FIELD_IMMUTABLE`, `FIELD_NOT_FOUND`, `FIELD_TYPE_MISMATCH`, `BATCH_TOO_LARGE` and `OUTBOX_BACKPRESSURE`. `RetInfo.Msg` contains a stable safe message; the wrapped cause is logged with Node/Dataset identifiers and is never copied to the client. No classification may use `strings.Contains(err.Error())`.

- [ ] **Step 5: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/primarystore/... ./internal/retinfo)
git add modules/storage/internal/service/primarystore modules/storage/internal/retinfo
git commit -m "refactor(storage): centralize dataset field validation"
```

### Task 6: Implement Atomic DataNode Facts, Progress and Outbox

**Files:**
- Rename: `modules/storage/internal/service/datashard` -> `modules/storage/internal/service/datanode`
- Modify: `modules/storage/internal/service/datanode/contracts/store.go`
- Modify: `modules/storage/internal/service/datanode/pebble/key.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store.go`
- Modify: `modules/storage/internal/service/datanode/pebble/committed.go`
- Modify: `modules/storage/internal/service/datanode/pebble/outbox.go`
- Modify: `modules/storage/internal/service/datanode/service.go`
- Rename: `modules/storage/internal/service/datashard/contracts/store_test.go` -> `modules/storage/internal/service/datanode/contracts/store_test.go`
- Rename: `modules/storage/internal/service/datashard/pebble/store_test.go` -> `modules/storage/internal/service/datanode/pebble/store_test.go`
- Rename: `modules/storage/internal/service/datashard/pebble/outbox_test.go` -> `modules/storage/internal/service/datanode/pebble/outbox_test.go`
- Rename: `modules/storage/internal/service/datashard/service_test.go` -> `modules/storage/internal/service/datanode/service_test.go`
- Rename: `modules/storage/internal/service/datashard/outbox_relay_test.go` -> `modules/storage/internal/service/datanode/outbox_relay_test.go`
- Rename: `modules/storage/internal/service/datashard/local_test.go` -> `modules/storage/internal/service/datanode/local_test.go`
- Rename: `modules/storage/internal/service/datashard/remote_test.go` -> `modules/storage/internal/service/datanode/remote_test.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/observability/view_metrics.go`

**Interfaces:**
- Consumes: schema-validated `MergeRowsReq`, configured `node_id`, JetStream `max_payload_bytes`.
- Produces: `DatasetProgress.last_committed_sequence`, ordered RowsCommitted Outbox and backpressure errors.

- [ ] **Step 1: Write the atomicity and Schema-ignorance tests**

Prove in Pebble tests:

```text
fact + node_sequence + dataset progress + outbox commit together
failed deterministic message encoding commits none of them
oversized final MooxMessage commits none of them
two Datasets share one increasing node_sequence but keep separate progress
delete updates sequence, progress and outbox atomically
DataNode accepts a structurally valid field without loading Field metadata
```

- [ ] **Step 2: Use final Pebble keys**

```text
__meta/node_id
__meta/node_sequence
__meta/dataset_progress/<escaped-dataset-id>
__outbox/<big-endian-node-sequence>
```

The Dataset Progress key remains after all rows are deleted. Opening a directory with a different `node_id` fails.

- [ ] **Step 3: Validate only generic structure at DataNode**

DataNode checks request `node_id`, one Dataset per batch, key identity, supported DataKind, duplicate Field IDs, valid TypedValue wire structure, batch size and final payload. It does not read Metadata, compare value types, enforce Required, or store a Schema revision.

- [ ] **Step 4: Encode the final event before committing**

Merge old and incoming rows, assign the new Node Sequence to the complete facts and event, deterministically marshal `RowsCommitted` and `MooxMessage`, call `jetstream.ValidateMessage(msg, maxPayloadBytes)`, then stage facts, progress and Outbox in one Pebble Batch.

- [ ] **Step 5: Keep Outbox recovery strict**

The relay retries the head with bounded exponential backoff and deletes only the contiguous published prefix. When Entries, Bytes or Oldest Age reaches its configured bound, new fact writes return `OUTBOX_BACKPRESSURE`. Provide read-only stats and retry-now; do not add skip/delete operations.

- [ ] **Step 6: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/datanode/...)
git add modules/storage/internal/service/datanode modules/storage/internal/config modules/storage/internal/observability
git commit -m "refactor(storage): make datanode commits atomic"
```

### Task 7: Add Bounded DataNode Snapshots

**Files:**
- Create: `modules/storage/internal/service/datanode/snapshot.go`
- Create: `modules/storage/internal/service/datanode/snapshot_test.go`
- Modify: `modules/storage/internal/service/datanode/service.go`
- Modify: `modules/storage/internal/service/datanode/client.go`
- Modify: `modules/storage/internal/service/primarystore/factreader.go`
- Modify: `modules/storage/internal/service/primarystore/factreader_test.go`

**Interfaces:**
- Consumes: Pebble facts and Dataset Progress.
- Produces: `BeginNodeSnapshot`, `ScanNodeSnapshot`, `EndNodeSnapshot` with TTL and bounded registry.

- [ ] **Step 1: Write snapshot consistency tests**

Create a Snapshot at progress 10, commit progress 11 afterward, then prove Snapshot Scan returns only state through 10 and reports progress 10. Also cover unknown ID, expired ID, wrong Node, wrong Dataset, maximum open snapshots and idempotent End.

- [ ] **Step 2: Implement an in-process Snapshot registry**

```go
type SnapshotRegistry struct {
    mu sync.Mutex
    items map[string]*snapshotEntry
    ttl time.Duration
    maxOpen int
}
```

Default `snapshot_ttl=10m`, `max_open_snapshots=4`. Begin creates one Pebble Snapshot per DataNode request and reads requested Dataset Progress from that same Snapshot. Cursor is opaque and valid only for that live `snapshot_id`; it is never stored in Metadata.

- [ ] **Step 3: Expose Snapshot only to storage-view identity**

Apply the Service HMAC allowlist from Task 4. Normal callers and `storage-primary` write identity cannot open Snapshot RPC unless explicitly granted the maintenance method set.

- [ ] **Step 4: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/datanode ./internal/service/primarystore)
git add modules/storage/internal/service/datanode modules/storage/internal/service/primarystore
git commit -m "feat(storage): add bounded datanode snapshots"
```

### Task 8: Configure Interest JetStream and Two Fixed View Consumers

**Files:**
- Modify: `modules/eventbus/internal/config/config_types.go`
- Modify: `modules/eventbus/internal/config/config_validation.go`
- Modify: `modules/eventbus/internal/config/config_defaults.go`
- Modify: `modules/eventbus/internal/config/config_test.go`
- Modify: `modules/eventbus/internal/registry/registry.go`
- Modify: `modules/eventbus/internal/registry/registry_test.go`
- Modify: `modules/eventbus/config/app.yaml`
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/bus.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/bus_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus_test.go`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory.go`
- Modify: `modules/storage/internal/bootstrap/eventbus/factory_test.go`

**Interfaces:**
- Consumes: `MOOX_STORAGE`, RowsCommitted subjects.
- Produces: controllable `storage_view_active` and `storage_view_rebuild` subscriptions.

- [ ] **Step 1: Add failing EventBus registry tests**

Assert `MOOX_STORAGE` reconciles to FileStorage, one replica, `InterestPolicy`, `DiscardNew`, `moox.storage.>` and both fixed Durable Consumers with explicit ACK, DeliverAll, MaxDeliver `-1`. Existing stream with LimitsPolicy or DiscardOld must fail startup with a safe instruction to recreate the stream; do not mutate retention in place.

- [ ] **Step 2: Extend Stream config explicitly**

`StreamConfig` accepts `retention: interest` and `discard: new`; registry maps them to `nats.InterestPolicy` and `nats.DiscardNew`. Other streams retain their declared policies.

- [ ] **Step 3: Make Durable identity an explicit subscriber option**

Replace the hard-coded consumer with:

```go
type SubscriptionControl interface {
    Pause(context.Context) error
    Resume(context.Context) error
    Drain(context.Context) error
    Close() error
}
```

Create one SubscriberBus bound to `storage_view_active` and another to `storage_view_rebuild`. Pause stops new Fetch calls, Drain waits for handlers already delivered, and Resume restarts pulling from the same Durable state.

- [ ] **Step 4: Keep Rebuild Consumer caught up while idle**

When no Build exists, Rebuild handler ACKs valid RowsCommitted without applying it. Build start pauses and drains this consumer before opening Snapshots, preventing an idle Durable from pinning all InterestPolicy messages forever.

- [ ] **Step 5: Update EventBus credentials**

Grant `storage-view` only Consumer INFO/NEXT/ACK subjects for `storage_view_active` and `storage_view_rebuild`. DataNode publisher credentials publish RowsCommitted but cannot fetch either consumer. Tests must inspect generated YAML without printing generated secrets.

- [ ] **Step 6: Run tests and commit**

```bash
(cd modules/eventbus && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/config ./internal/registry)
(cd modules/admin && env GOCACHE=/tmp/moox-gocache go test -count=1 ./cmd/cli)
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/bootstrap/eventbus ./internal/service/viewbuilder/eventconsumer)
git add modules/eventbus modules/admin/cmd/cli/eventbus_credentials.go modules/admin/cmd/cli/eventbus_credentials_test.go modules/storage/internal/bootstrap/eventbus modules/storage/internal/service/viewbuilder/eventconsumer
git commit -m "feat(storage): add fixed view consumers"
```

### Task 9: Make ViewIndex Source Checkpoints and Revisions Atomic

**Files:**
- Modify: `modules/storage/internal/service/viewindex/engine.go`
- Modify: `modules/storage/internal/service/viewindex/client.go`
- Modify: `modules/storage/internal/service/viewindex/batch_write.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_apply.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_schema.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index.go`
- Modify: `modules/storage/internal/service/viewindex/engine_test.go`
- Modify: `modules/storage/internal/service/viewindex/client_test.go`
- Modify: `modules/storage/internal/service/viewindex/batch_write_test.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/view_store_test.go`
- Modify: `modules/storage/internal/service/viewindex/bleve/index_test.go`
- Modify: `modules/storage/test/view_index_switch_test.go`

**Interfaces:**
- Consumes: `ViewIndexApplyBatch` with `view_revision` and Source Checkpoint updates.
- Produces: atomic row/checkpoint/range Apply and idempotent stale-event behavior.

- [ ] **Step 1: Add the cross-engine checkpoint contract tests**

Run the same table against DuckDB and Bleve:

```text
Apply row + checkpoint commits together
wrong expected checkpoint commits neither
same sequence returns success without row mutation
lower sequence returns success without row mutation
Node sequence gaps caused by other Datasets are accepted
wrong View revision is rejected
MERGE missing row commits neither row nor checkpoint
REPLACE creates the complete row and advances checkpoint
DELETE and checkpoint commit together
```

- [ ] **Step 2: Use Dataset-aware Source Checkpoint keys**

Checkpoint identity is `node_id + NUL + dataset_id`. CAS requires `expected == stored` and `last > expected`; it does not require `last == expected + 1`. A stale message bypasses CAS and returns success without touching rows or range.

- [ ] **Step 3: Remove Schema Hash and Required columns from Apply**

Physical Index metadata stores View Revision and Columns. `ViewIndexApplyBatch` carries row writes, checkpoint updates, revision and optional range update only. DuckDB and Bleve must have identical semantics.

- [ ] **Step 4: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/viewindex/... ./test -run 'TestViewIndex')
git add modules/storage/internal/service/viewindex modules/storage/test/view_index_switch_test.go
git commit -m "refactor(storage): checkpoint views by dataset source"
```

### Task 10: Rebuild the Active View Consumer with Recoverable Node Lanes

**Files:**
- Modify: `modules/storage/internal/service/viewbuilder/service.go`
- Modify: `modules/storage/internal/service/viewbuilder/options.go`
- Modify: `modules/storage/internal/service/viewbuilder/apply.go`
- Modify: `modules/storage/internal/service/viewbuilder/checkpoint.go`
- Modify: `modules/storage/internal/service/viewbuilder/recovery.go`
- Modify: `modules/storage/internal/service/viewbuilder/deletes.go`
- Modify: `modules/storage/internal/service/viewbuilder/time_series.go`
- Modify: `modules/storage/internal/service/viewbuilder/record.go`
- Modify: `modules/storage/internal/service/viewbuilder/service_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/options_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/apply_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/completion_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/source_reader_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/time_series_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/record_test.go`
- Modify: `modules/storage/test/view_derivation_reliability_test.go`

**Interfaces:**
- Consumes: Active Consumer, Metadata active handles, PrimaryStore full-source reads, ViewIndex Apply.
- Produces: bounded per-Node active lanes that ACK only durable outcomes.

- [ ] **Step 1: Reproduce the old silent-loss and permanent-poison failures**

Add tests where ViewIndex first returns a transient error after delivery, then succeeds on redelivery of the same Node Sequence. Assert the first delivery NAKs, later Node events do not pass it, the same Sequence retry succeeds, the lane unblocks, and a failure on Node B does not block Node A.

- [ ] **Step 2: Replace permanent `blockedByShard` with bounded Node lanes**

Each Node lane owns a bounded queue and one in-order worker. It records the failed Sequence only until that same Sequence succeeds. Queue count and worker count are bounded by configuration; no goroutine is created per event.

- [ ] **Step 3: Apply only Active Indexes**

For each event, load the current immutable Active Handle set. For every View depending on its Dataset:

1. If event Sequence is at/below that Index checkpoint, mark this View skipped.
2. Map only the changed Dataset's owned columns and MERGE.
3. If MERGE reports missing row, read every source Dataset for the ViewRowKey and retry a complete REPLACE without advancing checkpoint on the failed attempt.
4. Apply Primary/secondary DELETE semantics.
5. Return success only after all affected Views applied or skipped.

The Active path never discovers or writes BUILDING/CATCHING_UP Indexes.

- [ ] **Step 4: Verify ACK lifecycle end to end**

The event handler returns the real completion error to `processDelivery`; enqueue success alone is not ACK success. Correct the Reliability test so the first Apply genuinely persists before any injected post-persist error scenario.

- [ ] **Step 5: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/viewbuilder ./test -run 'TestViewDerivation')
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/viewbuilder)
git add modules/storage/internal/service/viewbuilder modules/storage/test/view_derivation_reliability_test.go
git commit -m "fix(storage): make active view lanes recoverable"
```

### Task 11: Replace Lease/Cursor Maintenance with One Snapshot Rebuild

**Files:**
- Create: `modules/storage/internal/service/viewbuilder/build.go`
- Create: `modules/storage/internal/service/viewbuilder/build_test.go`
- Modify: `modules/storage/internal/service/dataview/maintenance.go`
- Modify: `modules/storage/internal/service/dataview/maintenance_test.go`
- Delete: `modules/storage/internal/service/dataview/build_cursor.go`
- Delete: `modules/storage/internal/service/dataview/build_cursor_test.go`
- Modify: `modules/storage/internal/service/dataview/schedule.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_test.go`

**Interfaces:**
- Consumes: Rebuild Consumer control, DataNode Snapshot clients, ViewIndex, Metadata Build state.
- Produces: one global non-resumable `BUILDING -> CATCHING_UP -> ACTIVE/FAILED` workflow.

- [ ] **Step 1: Write the rebuild state-machine tests**

Cover one global Build, idle Rebuild ACK, pause/drain before Snapshot, multi-Node Snapshot vector, full REPLACE backfill, baseline checkpoint initialization, discard through Snapshot barrier, catch-up beyond barrier, Snapshot expiry, process restart and build failure cleanup.

- [ ] **Step 2: Simplify persistent Build state**

Metadata stores no owner, lease, cursor or resumable Snapshot ID. On `storage-view` startup, any BUILDING/CATCHING_UP row becomes FAILED, its inactive Index is removed, and a later scheduler run starts a new Build. ACTIVE retains the last successful Build summary until the next Build replaces it.

- [ ] **Step 3: Implement the exact Snapshot flow**

```text
lock global build mutex
pause and drain storage_view_rebuild
open one Snapshot per source DataNode
read each Dataset's last_committed_sequence from that Snapshot
REPLACE all retained source rows into the inactive Index
set each physical Index Source Checkpoint to its Snapshot sequence
resume storage_view_rebuild
ACK and ignore source events <= Snapshot sequence
apply later MERGE/DELETE until caught up
always End every Snapshot
```

Do not persist the scan cursor. A crash, lost Snapshot or TTL expiration marks FAILED and deletes the inactive Index.

- [ ] **Step 4: Keep Build selection deterministic**

Select the first active View where `desired_view_revision > active_view_revision`, ordered by `(space_id, view_id)`. One scheduler tick advances only the global current Build; it never starts a second Build.

- [ ] **Step 5: Run tests and commit**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/dataview ./internal/service/viewbuilder ./internal/service/metadata/sqlite)
git add modules/storage/internal/service/dataview modules/storage/internal/service/viewbuilder modules/storage/internal/service/metadata/sqlite
git commit -m "refactor(storage): rebuild views from datanode snapshots"
```

### Task 12: Implement the Two-Second Query-Safe Activation

**Files:**
- Create: `modules/storage/internal/service/dataview/active_handle.go`
- Create: `modules/storage/internal/service/dataview/active_handle_test.go`
- Modify: `modules/storage/internal/service/dataview/service.go`
- Modify: `modules/storage/internal/service/dataview/service_test.go`
- Modify: `modules/storage/internal/service/viewbuilder/build.go`
- Modify: `modules/storage/internal/service/viewbuilder/build_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_index.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/test/view_index_switch_test.go`

**Interfaces:**
- Consumes: ready inactive Index, Active/Rebuild Consumer controls, latest Dataset Progress Vector.
- Produces: idempotent Metadata CAS and atomic in-memory Active Handle.

- [ ] **Step 1: Write activation race tests before implementation**

Prove continuous old-schema queries have zero errors during rebuild; new field is invisible before switch and visible after switch; an old in-flight query completes on the old handle; post-switch queries use the new handle; delayed old Active messages are no-op ACKs; and a catch-up delay beyond 2 seconds restores Active Consumer without changing Metadata.

- [ ] **Step 2: Make Active Handle one atomic value**

```go
type ActiveHandle struct {
    IndexID string
    Revision uint64
    Columns []*storagepb.ViewColumn
    SourceCheckpoints map[SourceKey]uint64
}
```

DataView obtains one pointer at request start and uses it for the full query. The pointer is never mutated after publication. Old handle/index deletion waits for reference count zero plus configured Grace Period.

`SourceCheckpoints` is the immutable activation baseline published with that handle; the physical ViewIndex checkpoint remains authoritative as Active events advance. Event application reads and CASes the physical checkpoint and never treats the activation-baseline map as current progress.

- [ ] **Step 3: Implement the bounded activation protocol**

```text
pause and drain storage_view_active
read latest Dataset Progress Vector
let rebuild consumer catch the candidate Index to that vector
before the 2-second deadline, call idempotent Metadata CAS with expected old revision/index
atomically publish the new ActiveHandle
resume storage_view_active
```

If the deadline expires before entering CAS, resume the old Active path and keep the candidate for retry. After Metadata confirms the candidate Active, always finish the local handle swap; if the RPC result is unknown, read Metadata to decide. Restart rebuilds the handle from Metadata.

- [ ] **Step 4: Run race and switch tests**

```bash
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -count=1 ./internal/service/dataview ./internal/service/viewbuilder ./test -run 'TestViewIndexSwitch')
(cd modules/storage && env GOCACHE=/tmp/moox-gocache go test -race -count=1 ./internal/service/dataview ./internal/service/viewbuilder ./internal/service/viewindex/...)
git add modules/storage/internal/service/dataview modules/storage/internal/service/viewbuilder modules/storage/internal/service/metadata/sqlite modules/storage/test/view_index_switch_test.go
git commit -m "feat(storage): switch rebuilt views without query downtime"
```

### Task 13: Complete the Repository-Wide Rename and Caller Integration

**Files:**
- Modify: `modules/storage/config/*`, `modules/storage/README.md`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go` and tests
- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/cli/internal/command/metadata_implementation.go` and tests
- Modify: `modules/cli/config/fields.yaml`
- Modify: `modules/monitor/internal/metrics/storage.go` and tests
- Modify: `modules/archive`, `modules/factor` and their tests/configs/docs
- Modify: `web/src/api/storage/metadata.ts`, `web/src/api/storage/types.ts`
- Modify: `web/src/views/data/datasets/components/dataset-column-panel.vue`
- Modify: `scripts/storage-start.sh`, `scripts/storage-stop.sh`, `scripts/deploy-moox.sh`
- Modify: `scripts/test-storage-boundary-contract.sh`, `scripts/test-storage-consistency-contract.sh`
- Modify: `examples/*.yaml`, `examples/e2e/*`
- Modify: `docs/存储层架构.md`, `docs/存储目标架构与元数据.md`, `docs/存储引擎架构.md`, `docs/架构总览.md`, `docs/协议设计.md`
- Modify: `docs/行情数据归档模块设计.md`, `docs/因子计算模块设计.md`, `docs/策略模块架构设计.md`
- Rename: `packages/jetstream/subject_token.go` -> `packages/jetstream/node_subject_token.go`
- Rename: `packages/jetstream/subject_token_test.go` -> `packages/jetstream/node_subject_token_test.go`
- Modify: `modules/storage/internal/service/datanode/pebble/committed.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus.go`
- Modify: `modules/storage/internal/service/viewbuilder/eventconsumer/producer_bus_test.go`

**Interfaces:**
- Consumes: all final APIs from Tasks 2-12.
- Produces: one compiling repository with no active DataShard or Required-field surface.

- [ ] **Step 1: Update seeds and CLI import**

Replace `primary_store_nodes + devices + primary_store_routes` fact routing with:

```yaml
data_nodes:
  - node_id: data-node-market
    service_target: ip://127.0.0.1:20106
    status: active

datasets:
  - space_id: crypto
    dataset_id: binance_spot_kline
    data_node_id: data-node-market
```

Remove every DatasetColumn `required` key. Import rejects an unknown `data_node_id` and does not create default routes or devices.

- [ ] **Step 2: Update Admin/Monitor/Archive/Factor callers**

Monitor validates that its Dataset has a DataNode and required column *names* for its own business use, but it does not read `DatasetColumn.required`. Archive and Factor decode `node_id/source_node_id`, use direct PrimaryStore RPC, and preserve their own Durable semantics.

- [ ] **Step 3: Make the Dataset Field UI append-only**

Remove the Required column, Required checkbox and edit action. The command is “新增字段” and calls `AddDatasetField`; immutable conflicts display the stable server message. Existing fields remain readable but cannot be deleted, disabled, renamed or type-edited from the Dataset panel.

- [ ] **Step 4: Rename deployment and metrics atomically**

Apply the final map everywhere:

```text
DataShard -> DataNode
datashard -> datanode
storage-shard -> storage-node
role shard -> node
shard_id -> node_id
source_shard_id -> source_node_id
ShardCheckpoint -> ViewSourceCheckpoint
storage_shard_* -> storage_node_*
```

Rename packages, config files, release archive names, service deployment seeds, process names, CLI flags and Prometheus metrics in one commit. Do not keep old environment-variable fallbacks.

- [ ] **Step 5: Remove Storage internal Gateway routes**

Delete storage-view-to-primary and primary-to-node Node Service Gateway route declarations and special allowlist logic. Keep Admin Gateway's public Metadata/PrimaryStore/DataView methods; DataNode, ViewIndex, Snapshot and Build methods remain unreachable from the browser.

- [ ] **Step 6: Run exact residue scans**

```bash
rg -n 'DataShard|datashard|data_shard|storage-shard|storage_shard_|shard_id|source_shard_id|GetShardState|GetShardHeads|ShardCheckpoint' modules packages web scripts examples docs --glob '!docs/superpowers/**'
rg -n 'required:' modules/storage modules/cli/config examples --glob '*.{yaml,yml}'
rg -n 'record\.required|form\.required|DatasetColumn.*Required|GetRequired\(\)|c_required|required_column_names' modules web scripts examples --glob '!**/storagegen/**'
rg -n 'PrimaryStoreRoute|primary_store_routes|ShardTarget|hash_rule|subject_pattern' modules/storage modules/monitor modules/cli web scripts examples --glob '!**/storagegen/**'
```

Expected: zero active matches. Generic English “is required” input errors and HTML form-required markers are not Dataset Required-field concepts and may remain.

- [ ] **Step 7: Run integration builds and commit**

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
git commit -m "refactor(storage): finish datanode integration"
```

### Task 14: Prove the System with Reviews, Local Gates and Two-Server E2E

**Files:**
- Create: `modules/cli/internal/command/storage_e2e.go`
- Create: `modules/cli/internal/command/storage_e2e_test.go`
- Create: `modules/storage/test/dataset_node_e2e_test.go`
- Create: `scripts/test-storage-two-node-e2e.sh`
- Modify: `modules/cli/internal/command/setup.go`
- Modify: `modules/cli/README.md`
- Modify: `docs/运维/MooX-EventBus运维.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`

**Interfaces:**
- Consumes: repository-root ignored `custom.toml`, two configured SSH hosts, release artifacts.
- Produces: sanitized E2E report, two independent review reports and final merge-ready evidence.

- [ ] **Step 1: Build a credential-safe E2E command**

Add:

```text
moox-cli setup storage-e2e --file ./custom.toml --primary-host-index 0 --factor-host-index 1
```

It reuses the existing secure config loader and SSH client, selects two distinct enabled hosts by stable sorted name, and prints only run ID, logical host names, phase, duration and pass/fail. It creates random remote directories under `/tmp/moox-storage-e2e-<run-id>` and random ports, never interpolates credentials into command output, and always cleans its own processes/directories.

- [ ] **Step 2: Run the complete local verification matrix**

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

Expected: all pass. Record command summaries and failure counts, not secrets or complete environment dumps.

- [ ] **Step 3: Execute the two-server topology**

The E2E command starts a dedicated EventBus, `storage-primary`, market DataNode and `storage-view` on Host 0, plus factor DataNode on Host 1. It imports fresh Schema v4 metadata with K-line and factor Datasets assigned to different DataNodes, then proves:

1. continuous writes and reads on both Datasets;
2. one cross-DataNode K-line + factor View;
3. dynamic Add Dataset Field while writes continue, with no process restart;
4. old-field queries report zero errors during non-active Index rebuild;
5. new field becomes visible only after activation and matches authoritative facts;
6. Active backlog older than the new Source Checkpoint cannot regress the new Index;
7. after polling the Build into activation-ready state, `SIGSTOP` of the dedicated storage-primary before activation CAS for more than 2 seconds causes timeout and old Active recovery;
8. stopping the factor DataNode does not break market Dataset reads/writes;
9. stopping the dedicated E2E EventBus retains Outbox, and restart publishes the ordered backlog;
10. no E2E process or directory remains afterward.

Run:

```bash
./bin/moox-cli setup storage-e2e --file ./custom.toml --primary-host-index 0 --factor-host-index 1
```

Expected: sanitized JSON with `status=ok`, all ten phases passed and `cleanup=ok`.

- [ ] **Step 4: Commit the E2E harness before review**

```bash
git add modules/cli/internal/command/storage_e2e.go modules/cli/internal/command/storage_e2e_test.go modules/cli/internal/command/setup.go modules/storage/test/dataset_node_e2e_test.go scripts/test-storage-two-node-e2e.sh modules/cli/README.md docs/运维/MooX-EventBus运维.md docs/运维/数据保留与磁盘空间.md
git commit -m "test(storage): add two-node rebuild e2e"
```

- [ ] **Step 5: Request independent review round one**

Use `superpowers:requesting-code-review` with the approved spec and full branch diff. Reviewer one focuses on protocol, Metadata v4, Catalog concurrency, PrimaryStore/DataNode authority, atomic Pebble commit, Outbox and direct RPC security. Fix every P1/P2 finding, add regression tests, rerun affected packages, and commit:

```bash
git add -A
git commit -m "fix(storage): address consistency review"
```

- [ ] **Step 6: Request independent review round two**

Use a fresh reviewer context. Reviewer two focuses on JetStream ACK lifecycle, fixed consumers, Snapshot barriers, per-ViewIndex stale-event handling, 2-second activation, DataView races, configuration/deploy residue and E2E cleanup. It must not reuse reviewer one's conclusion. Fix every P1/P2 finding, add regression tests, rerun affected packages, and commit:

```bash
git add -A
git commit -m "fix(storage): address rebuild review"
```

- [ ] **Step 7: Re-run final proof after the last review fix**

Repeat Step 2 and Step 3 from the final HEAD. Previous green results do not count after review changes.

- [ ] **Step 8: Push the verified branch**

```bash
git status --short
git push -u origin codex/storage-dataset-node
git status --short --branch
git rev-parse HEAD
git rev-parse '@{upstream}'
```

Expected: working tree clean, HEAD equals upstream, all Tasks checked, two independent reviews closed and fresh two-server E2E green.

## Final Acceptance Checklist

- [ ] Schema v4 is the only accepted Metadata schema; no migration or compatibility path exists.
- [ ] Dataset routes directly by `data_node_id`; no Hash, weight, PrimaryStoreRoute or caller-supplied physical target remains.
- [ ] K-line and factor Datasets run on different DataNodes and form one correct View.
- [ ] No active DataShard/datashard/shard_id/storage-shard/storage_shard symbol remains.
- [ ] No Dataset Required field exists in Proto, SQL, Go, Seed, UI or validation.
- [ ] Add Dataset Field is idempotent and hot-swaps Runtime Schema before success response.
- [ ] PrimaryStore alone validates field identity/type; DataNode contains no Schema Revision, Manifest or Fence.
- [ ] Facts, Node Sequence, Dataset Progress and Outbox commit in one Pebble Batch.
- [ ] Final event payload is validated before fact commit and Outbox cannot force-skip.
- [ ] `MOOX_STORAGE` is InterestPolicy/DiscardNew with only the two fixed View consumers.
- [ ] Active event failure NAKs and can recover the same Node lane without blocking other Nodes.
- [ ] ViewIndex stores `{node_id,dataset_id}` checkpoints and stale delivery is a per-Index no-op.
- [ ] Snapshot rebuild uses full REPLACE, then catches up MERGE/DELETE from the barrier.
- [ ] Only one Build runs; crash restarts from a fresh Snapshot with no lease/cursor recovery.
- [ ] View queries continue during rebuild and switch; new fields appear atomically.
- [ ] Switch timeout is at most 2 seconds and restores old Active updates before CAS.
- [ ] Dataset placement change after any committed fact is rejected; no migration tool exists.
- [ ] Storage internal RPC bypasses Node Service Gateway and enforces direct Service HMAC.
- [ ] Local full gates, two independent reviews and fresh two-server E2E all pass from final HEAD.
