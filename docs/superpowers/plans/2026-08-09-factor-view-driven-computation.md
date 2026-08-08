# Factor View-Driven Computation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Factor 实时触发从“行事件批处理 + 固定等待”切换为“Collector 周期完成 -> Source View 可读 -> Factor 计算 -> Result View 可读”的显式完成事件链。

**Architecture:** 保留单实例、SQLite、JetStream 和 DataNode outbox。Collector 持久化每周期 subject 状态；Storage 将 Dataset/Factor 完成 Marker 与对应行写入放入同一 DataNode outbox；View 用单 durable、`MaxAckPending=1` 串行处理行和 Marker；Factor 只消费 `ViewSourcePeriodReady`，写入独立结果 Dataset，并在 Marker 已交接后 ACK。实时链路采用 latest-wins，不引入 generation、snapshot hash、read fence、reservation、不可变发布历史或 `view_period_generations`。

**Tech Stack:** Go 1.25、Protocol Buffers/tRPC-Go、NATS JetStream、SQLite/GORM、Pebble、DuckDB、Python worker、MooX `packages/events`。

---

## 0. 实施边界

本计划以 `docs/因子视图驱动计算设计.md` v6 为唯一业务设计基线。实现时锁定以下取舍：

- 一个 `(space,dataset,frequency,period)` 只自动上报一次；迟到 K 线不发第二代 ready，需要时显式 Recalc。
- View 和 Factor 各只运行一个活动实例；两个 consumer 都使用 `DeliverAllPolicy`、显式 ACK、`MaxAckPending=1`。
- ready 表示“发布时当前 View 可读”，不是历史快照句柄；下游晚读允许得到最新值。
- Source View 不加入 factor-result Dataset；每个 Source View 自动维护一个独立 Result Dataset 和 Result View。
- 动态 RowKey manifest 只用于清理本 binding 的旧输出，不进入公共事件。
- 不新增 `view_period_generations`、`c_build_fence_json`、generation、previous report、hash、fence、barrier 或发布状态机。
- `DatasetSyncPoint` 只服务 import/catchup 与显式 Recalc，不参与正常实时周期完成。
- 第一轮实现不改 Python 因子执行契约，不增加因子 DAG、横截面因子、多实例协调或前向自动重算。

本计划替代 `docs/superpowers/plans/2026-07-26-factor-best-effort-simplification.md` 中的实时行事件触发部分；Python runtime、sandbox、任务重试等既有正确性约束仍以 `docs/superpowers/plans/2026-07-29-factor-runtime-correctness-hardening.md` 为准。

实施必须在独立 worktree 中完成。当前主 worktree 已有与本功能无关的修改，每个任务只能 `git add` 本任务列出的文件。

## 1. 目标调用链

```text
Collector SQLite readiness
  -> PrimaryStore.ReportDatasetPeriodCollected
  -> DataNode Pebble row outbox 后追加 DatasetPeriodCollected
  -> View 单 consumer 先应用 rows，再聚合 Dataset Marker
  -> 发布 ViewSourcePeriodReady
  -> Factor 单 consumer 读取 Source View 并运行全部 enabled bindings
  -> 写 Result Dataset，按 manifest clear 消失输出
  -> PrimaryStore.ReportFactorPeriodComputed
  -> Result DataNode outbox 追加 FactorPeriodComputed
  -> View 单 consumer 先应用结果 rows，再发布 ViewFactorPeriodReady
```

ACK 边界：

| 消费者 | 消息 | ACK 条件 |
|---|---|---|
| Collector storage-write consumer | `DatasetRowsUpserted` | readiness item 已提交 SQLite |
| View storage consumer | 行事件 | Active/Building 应用成功 |
| View storage consumer | Dataset Marker | 聚合状态已提交，必要的 source-ready 已发布成功 |
| View storage consumer | Factor Marker | result-ready 已发布成功 |
| View storage consumer | SyncPoint | View 应用状态已提交 |
| Factor source-ready consumer | `ViewSourcePeriodReady` | 对应 `FactorPeriodComputed` 已存在或已成功追加 |

## 2. 文件职责

### 公共事件与 Storage RPC

- `packages/storagepb/storage_events.proto`：五类公共事件 payload。
- `packages/events/registry.go`：事件名、版本、owner、stream 和 subject 注册。
- `packages/events/validation.go`：公共事件字段和枚举校验。
- `packages/events/decode.go`：保留通用 `DecodeRaw`，增加必要的 typed decode helper。
- `packages/jetstream/consumer.go`、`packages/events/consumer.go`：支持同一 durable 的多个精确事件 family filter。
- `modules/eventbus/config/app.yaml`、`modules/admin/cmd/cli/eventbus_credentials.go`：扩展 Stream subjects 和 consumer ACL。
- `modules/storage/proto/primary_store.proto`：Collector、Factor、import 调用的公开边界 RPC。
- `modules/storage/proto/data_node.proto`：Primary 到目标 DataNode 的内部 Marker 追加/查询 RPC。
- `modules/storage/proto/metadata.proto`：View 周期聚合和 SyncPoint 查询/等待 RPC。

### Storage / View

- `modules/storage/internal/service/datanode/pebble/marker.go`：在 `outboxMu` 下幂等追加 Marker，并保存 payload hash。
- `modules/storage/internal/service/datanode/pebble/outbox_message.go`：通用 EventMessage 校验和 outbox 绑定。
- `modules/storage/internal/service/datanode/outbox/publisher.go`：允许注册过的 storage 事件发布，不再只接受行事件。
- `modules/storage/internal/service/primarystore/marker.go`：鉴权、结果 Dataset 写权限、dataset -> DataNode 路由。
- `modules/storage/schema/metadata.sql`：`t_view_period_dataset_states`、`t_view_sync_points`。
- `modules/storage/internal/service/metadata/sqlite/crud_view_period_state.go`：两张状态表 CRUD。
- `modules/storage/internal/service/view/eventconsumer/handler.go`：按事件名分派行、Dataset Marker、Factor Marker、SyncPoint。
- `modules/storage/internal/service/view/event_apply.go`：四类存储事件的业务应用。
- `modules/storage/internal/service/view/period_ready.go`：Source/Result ready 构造与发布。
- `modules/storage/internal/service/view/sync_point.go`：SyncPoint 应用和等待。
- `modules/storage/internal/service/view/build.go`：受控暂停、全量重建、双写追平、短暂停顿激活。

### Collector

- `modules/collector/schema/collector.sql`：readiness 父状态和 subject item。
- `modules/collector/internal/domain/period_readiness.go`：父状态、item 和固定 report payload 领域类型。
- `modules/collector/internal/store/period_readiness.go`：状态预建、成功、超时、固定 payload、reported 转换。
- `modules/collector/internal/marketfetch/period.go`：UTC 周期对齐与 deadline。
- `modules/collector/internal/marketfetch/period_readiness.go`：预建和行事件更新。
- `modules/collector/internal/marketfetch/period_reporter.go`：deadline、固定 payload、Storage RPC 重试。
- `modules/collector/internal/bootstrap/bootstrap.go`：组件装配与生命周期。

### Factor

- `modules/factor/proto/factor.proto`：binding 改为 `source_view_id`，Recalc 增加 request identity。
- `modules/factor/schema/factor.sql`：binding schema 和 `t_factor_output_manifests`。
- `modules/factor/internal/registry/binding_contract.go`：直接校验 Source View 和 Result View。
- `modules/factor/internal/registry/metadata_sync.go`：自动 Result Dataset/View。
- `modules/factor/internal/trigger/eventconsumer/*`：只消费 `ViewSourcePeriodReady`。
- `modules/factor/internal/trigger/period_executor.go`：realtime/Recalc 共用的单执行队列。
- `modules/factor/internal/storageio/client.go`：通过 `DataView.QueryTimeSeriesRows` 读取绑定 View。
- `modules/factor/internal/storageio/writeback.go`：稳定 upsert/clear 写计划。
- `modules/factor/internal/store/output_manifest.go`：动态 RowKey manifest。
- `modules/factor/internal/rpc/recalc.go`：显式 Recalc 走同一执行器。

### CLI 与端到端验证

- `modules/cli/internal/command/storage_import.go`：import 写完追加并等待 SyncPoint，再选择是否 Recalc。
- `modules/factor/test/view_driven_e2e_test.go`：完整实时链路和异常恢复。
- `scripts/tests/e2e/test-factor-view-ready-e2e.sh`：二进制级验收入口。

## 3. 实施任务

### Task 1: 定义公共完成事件

**Files:**
- Modify: `packages/storagepb/storage_events.proto`
- Modify: `packages/events/registry.go`
- Modify: `packages/events/validation.go`
- Modify: `packages/events/decode.go`
- Modify: `packages/events/events_test.go`
- Test: `packages/events/validation_test.go`
- Generated: `packages/storagepb/storage_events.pb.go`
- Modify: `modules/eventbus/config/app.yaml`
- Modify: `modules/eventbus/internal/config/config_test.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`

- [ ] **Step 1: 先写事件注册和校验失败测试**

测试固定以下事件名、stream 和 owner：

```go
want := map[string]string{
    "storage.dataset.period.collected":       "MOOX_STORAGE",
    "storage.view.source_period.ready":       "MOOX_STORAGE",
    "storage.dataset.factor_period.computed": "MOOX_STORAGE",
    "storage.view.factor_period.ready":       "MOOX_STORAGE",
    "storage.dataset.sync_point":              "MOOX_STORAGE",
}
```

再为每个 payload 写一组 table test：空 ID、空 frequency、`period_time <= 0`、未知 status、未排序/重复 ID 均失败；合法 payload 可 marshal -> envelope -> `DecodeRaw` round trip。

- [ ] **Step 2: 运行测试确认缺少事件定义**

Run: `cd packages/events && go test ./...`

Expected: FAIL，错误包含未定义的 `storagepb.DatasetPeriodCollected` 或 registry 中缺少新事件。

- [ ] **Step 3: 在 proto 中加入最终字段**

按设计文档第 4 节原样定义：

```protobuf
message DatasetPeriodCollected {
  string dataset_id = 1;
  string frequency = 2;
  int64 period_time = 3;
  string status = 4;
  repeated string subject_ids = 5;
  repeated string failed_subjects = 6;
  google.protobuf.Timestamp collected_at = 7;
}

message ViewPeriodDatasetState {
  string dataset_id = 1;
  string status = 2;
  repeated string failed_subjects = 3;
}

message ViewSourcePeriodReady {
  string source_view_id = 1;
  string frequency = 2;
  int64 period_time = 3;
  string status = 4;
  repeated ViewPeriodDatasetState datasets = 5;
  repeated string primary_subjects = 6;
  google.protobuf.Timestamp ready_at = 7;
}

message FactorBindingPeriodState {
  string binding_id = 1;
  string factor_id = 2;
  string status = 3;
  repeated string skipped_subjects = 4;
  repeated string failed_subjects = 5;
}

message FactorPeriodComputed {
  string source_view_id = 1;
  string result_dataset_id = 2;
  string frequency = 3;
  int64 period_time = 4;
  string status = 5;
  repeated FactorBindingPeriodState bindings = 6;
  google.protobuf.Timestamp computed_at = 7;
  string trigger_event_id = 8;
}

message ViewFactorPeriodReady {
  string source_view_id = 1;
  string result_view_id = 2;
  string frequency = 3;
  int64 period_time = 4;
  string status = 5;
  repeated FactorBindingPeriodState bindings = 6;
  google.protobuf.Timestamp ready_at = 7;
}

message DatasetSyncPoint {
  string sync_point_id = 1;
  string request_id = 2;
  string dataset_id = 3;
  string source = 4;
}
```

`status` 只允许 `complete|degraded`，`source` 只允许 `import|catchup`。所有 repeated ID 由构造方排序去重，validator 拒绝非规范化 payload。

- [ ] **Step 4: 注册事件并生成代码**

Run: `make -C packages/storagepb generate`

Expected: `storage_events.pb.go` 包含五个新 message；命令退出码为 0。

在 `registry.go` 中为五个事件各注册一个唯一 payload 类型和 `MOOX_STORAGE` stream；在 `validation.go` 中加入对应 validator；typed helper 只为 View/Factor 两个消费入口增加，其他调用使用 `DecodeRaw`。同时把 `MOOX_STORAGE.subjects` 明确配置为：

```yaml
subjects:
  - "moox.storage.dataset.rows.upserted.v2.>"
  - "moox.storage.dataset.period.collected.v1.>"
  - "moox.storage.view.source_period.ready.v1.>"
  - "moox.storage.dataset.factor_period.computed.v1.>"
  - "moox.storage.view.factor_period.ready.v1.>"
  - "moox.storage.dataset.sync_point.v1.>"
```

Storage EventBus 凭据可发布这六个 family，保证本提交后的 topology/credential contract 立即一致。

- [ ] **Step 5: 运行事件包测试**

Run: `cd packages/events && go test ./...`

Run: `cd modules/eventbus && go test ./...`

Run: `cd modules/admin && go test ./cmd/cli/...`

Expected: 全部 PASS。

- [ ] **Step 6: 提交**

```bash
git add packages/storagepb/storage_events.proto packages/storagepb/storage_events.pb.go packages/events modules/eventbus/config/app.yaml modules/eventbus/internal/config modules/admin/cmd/cli/eventbus_credentials.go modules/admin/cmd/cli/eventbus_credentials_test.go
git commit -m "feat(events): define storage readiness events"
```

### Task 2: 让 DataNode outbox 支持幂等 Marker

**Files:**
- Modify: `modules/storage/proto/data_node.proto`
- Create: `modules/storage/internal/service/datanode/pebble/marker.go`
- Modify: `modules/storage/internal/service/datanode/pebble/store.go`
- Modify: `modules/storage/internal/service/datanode/pebble/outbox_message.go`
- Modify: `modules/storage/internal/service/datanode/outbox/publisher.go`
- Modify: `modules/storage/internal/service/datanode/service.go`
- Modify: `modules/storage/cmd/server/main.go`
- Test: `modules/storage/internal/service/datanode/pebble/marker_test.go`
- Test: `modules/storage/internal/service/datanode/pebble/outbox_message_test.go`
- Test: `modules/storage/internal/service/datanode/outbox/publisher_test.go`
- Test: `modules/storage/internal/service/datanode/service_test.go`

- [ ] **Step 1: 写 Marker 重试和顺序测试**

覆盖四个事实：

1. 先 `UpsertFieldsEventWithSource`，再 `AppendDatasetMarker`，扫描 outbox 时 row entry 在 marker entry 前；
2. 相同 `event_id + payload` 重试只保留一个 outbox entry；
3. 相同 `event_id + 不同 payload` 返回 `ErrMarkerConflict`；
4. relay 可以发布新 Marker，原 `DatasetRowsUpserted` 测试保持通过。

测试使用固定 envelope：

```go
event := &eventpb.EventMessage{
    EventId:      "dataset-period-space-a-btc-1m-1722470400",
    EventName:    "storage.dataset.period.collected",
    EventVersion: 1,
    SpaceId:      "space-a",
    Subject:      "moox.storage.dataset.period.collected.space-a.kline-1m",
}
```

- [ ] **Step 2: 运行目标测试确认失败**

Run: `cd modules/storage && go test ./internal/service/datanode/...`

Expected: FAIL，缺少 `AppendDatasetMarker` 或 publisher 拒绝非行事件。

- [ ] **Step 3: 定义内部 RPC**

在 `data_node.proto` 增加：

```protobuf
message AppendDatasetMarkerReq {
  common.AuthInfo auth_info = 1;
  string node_id = 2;
  string space_id = 3;
  string dataset_id = 4;
  bytes event_message = 5;
}

message AppendDatasetMarkerRsp {
  common.RetInfo ret_info = 1;
  bool existed = 2;
}

message HasDatasetMarkerReq {
  common.AuthInfo auth_info = 1;
  string node_id = 2;
  string dataset_id = 3;
  string event_id = 4;
}

message HasDatasetMarkerRsp {
  common.RetInfo ret_info = 1;
  bool exists = 2;
  string payload_sha256 = 3;
}
```

`AppendDatasetMarker` 只接受 registry owner 为 `storage` 且事件名属于 `DatasetPeriodCollected|FactorPeriodComputed|DatasetSyncPoint` 的 envelope。

- [ ] **Step 4: 实现 Pebble 原子追加**

`marker.go` 使用与 `writeFieldsEvent` 相同的 `outboxMu`：

```go
func (s *Store) AppendDatasetMarker(ctx context.Context, datasetID string, raw []byte) (bool, error)
func (s *Store) HasDatasetMarker(ctx context.Context, datasetID, eventID string) (exists bool, payloadSHA256 string, err error)
```

dedupe value 保存解码后业务 payload 的规范 protobuf bytes 的 SHA-256，key 包含 dataset ID 和 event ID；不要把 envelope 的 `occurred_at` 或传输序列化差异放进冲突判断。校验 envelope 的 `space_id/dataset_id/subject` 与 RPC 路由一致后，在同一个 Pebble batch 中写 dedupe key 和 outbox entry。不要调用 `BindOutboxID` 改写显式 Marker Event ID。

- [ ] **Step 5: 泛化 outbox 校验**

把 `outbox_message.go`、`PrepareOutboxPublication` 和 `publisher.go` 的行事件专用校验替换为：

```go
message, payload, err := events.DecodeRaw(registry, raw, subject, messageID, events.ContentType)
if err != nil { return err }
if !strings.HasPrefix(message.GetSubject(), "moox.storage.") { return ErrInvalidEvent }
_ = payload
```

保留行事件的 outbox ID 绑定；Marker 使用调用方给出的确定性 ID。

- [ ] **Step 6: 生成 proto 并运行测试**

Run: `make -C modules/storage/proto`

Run: `cd modules/storage && go test ./internal/service/datanode/... ./cmd/server`

Expected: PASS，且现有行事件 relay/redelivery 测试不回归。

- [ ] **Step 7: 提交**

```bash
git add modules/storage/proto modules/storage/internal/service/datanode modules/storage/cmd/server/main.go
git commit -m "feat(storage): append dataset markers through outbox"
```

### Task 3: 增加 Primary Marker API 和结果 Dataset 写权限

**Files:**
- Modify: `modules/storage/proto/primary_store.proto`
- Modify: `modules/storage/proto/Makefile`
- Create: `modules/storage/internal/service/primarystore/marker.go`
- Modify: `modules/storage/internal/service/primarystore/service.go`
- Modify: `modules/storage/internal/service/primarystore/service_test.go`
- Test: `modules/storage/internal/service/primarystore/marker_test.go`
- Modify: `modules/storage/cmd/server/main.go`

- [ ] **Step 1: 写 Primary 路由与权限失败测试**

测试：

- `ReportDatasetPeriodCollected` 根据 payload dataset 路由到该 dataset 的 DataNode；
- `ReportFactorPeriodComputed` 只接受 `dataset_role=factor_result` 且调用方为 Factor；
- 普通 `UpsertFields(write_source=manual|import)` 写 `factor_result` 返回权限错误；
- `UpsertFields(write_source=factor)` 和内部 clear 允许通过；
- `GetFactorPeriodComputed` 在 Marker 已追加时返回 true。

- [ ] **Step 2: 运行测试确认 RPC 尚不存在**

Run: `cd modules/storage && go test ./internal/service/primarystore/...`

Expected: FAIL，缺少 Primary Marker 方法。

- [ ] **Step 3: 定义公开 RPC**

在 `primary_store.proto` 增加 typed request/response：

```protobuf
rpc ReportDatasetPeriodCollected(ReportDatasetPeriodCollectedReq) returns (ReportDatasetPeriodCollectedRsp);
rpc ReportFactorPeriodComputed(ReportFactorPeriodComputedReq) returns (ReportFactorPeriodComputedRsp);
rpc AppendDatasetSyncPoint(AppendDatasetSyncPointReq) returns (AppendDatasetSyncPointRsp);
rpc GetFactorPeriodComputed(GetFactorPeriodComputedReq) returns (GetFactorPeriodComputedRsp);
rpc WaitViewSyncPoint(WaitViewSyncPointReq) returns (WaitViewSyncPointRsp);
```

`primary_store.proto` 通过 `import "storage_events.proto"` 直接引用公共 payload，三个 report request 分别携带 typed `DatasetPeriodCollected`、`FactorPeriodComputed`、`DatasetSyncPoint`，不复制一套等价字段，也不接收任意 event bytes。`modules/storage/proto/Makefile` 给 `trpc-open` 增加 `--protodir ../../../packages/storagepb`。Primary 负责规范化、校验并构造 envelope。`GetFactorPeriodComputedReq` 固定使用 `source_view_id + trigger_event_id + period_time`，Primary 通过自动 Result View/Dataset 关系路由。

```protobuf
message ReportDatasetPeriodCollectedReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  trpc.moox.storage.event.DatasetPeriodCollected report = 3;
}
message ReportFactorPeriodComputedReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  trpc.moox.storage.event.FactorPeriodComputed report = 3;
}
message AppendDatasetSyncPointReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  trpc.moox.storage.event.DatasetSyncPoint sync_point = 3;
}
message GetFactorPeriodComputedReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string source_view_id = 3;
  string trigger_event_id = 4;
  int64 period_time = 5;
}
message WaitViewSyncPointReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string view_id = 3;
  string request_id = 4;
  repeated string dataset_ids = 5;
}
message ReportDatasetPeriodCollectedRsp { common.RetInfo ret_info = 1; bool existed = 2; }
message ReportFactorPeriodComputedRsp { common.RetInfo ret_info = 1; bool existed = 2; }
message AppendDatasetSyncPointRsp { common.RetInfo ret_info = 1; bool existed = 2; }
message GetFactorPeriodComputedRsp { common.RetInfo ret_info = 1; bool exists = 2; }
message WaitViewSyncPointRsp { common.RetInfo ret_info = 1; bool applied = 2; }
```

- [ ] **Step 4: 实现通用 Primary 追加函数**

```go
func (s *Service) appendDatasetMarker(
    ctx context.Context,
    auth *commonpb.AuthInfo,
    spaceID, datasetID string,
    message *eventpb.EventMessage,
) (bool, error)
```

函数顺序固定为：鉴权 -> 读取 Dataset -> 校验 role/source -> `NodeResolver.Resolve` -> 调用目标 `DataNodeRuntime.AppendDatasetMarker`。不要在 Primary 本地发布 JetStream 消息。

- [ ] **Step 5: 生成代码并运行测试**

Run: `make -C modules/storage/proto`

Run: `cd modules/storage && go test ./internal/service/primarystore/... ./cmd/server`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add modules/storage/proto modules/storage/internal/service/primarystore modules/storage/cmd/server/main.go
git commit -m "feat(storage): route dataset readiness markers"
```

### Task 4: 持久化 View 多 Dataset 聚合和 SyncPoint

**Files:**
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/schema/metadata_schema_version_test.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store_test.go`
- Modify: `modules/storage/internal/service/metadata/store.go`
- Create: `modules/storage/internal/service/metadata/sqlite/crud_view_period_state.go`
- Test: `modules/storage/internal/service/metadata/sqlite/crud_view_period_state_test.go`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/internal/service/catalog/metadata_catalog.go`
- Test: `modules/storage/internal/service/catalog/metadata_catalog_test.go`

- [ ] **Step 1: 写 schema 和 CRUD 失败测试**

测试两张表严格使用当前 `t_`/`c_` 风格，并验证：

- 同 `(space,view,dataset,freq,period)` upsert 覆盖相同 event，拒绝不同 payload 的相同 event ID；
- `ListViewPeriodDatasetStates` 按 dataset ID 排序；
- 同 `(space,view,dataset,request)` 的 SyncPoint 幂等；
- retention 删除只影响早于 cutoff 的记录。

- [ ] **Step 2: 运行测试确认表不存在**

Run: `cd modules/storage && go test ./internal/service/metadata/sqlite/...`

Expected: FAIL，错误包含 `no such table: t_view_period_dataset_states`。

- [ ] **Step 3: 增加最小表并升级 schema version**

```sql
CREATE TABLE IF NOT EXISTS t_view_period_dataset_states (
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time INTEGER NOT NULL,
    c_event_id TEXT NOT NULL,
    c_status TEXT NOT NULL,
    c_subject_ids_json TEXT NOT NULL DEFAULT '[]',
    c_failed_subjects_json TEXT NOT NULL DEFAULT '[]',
    c_updated_at TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_view_id, c_dataset_id, c_frequency, c_period_time)
);

CREATE TABLE IF NOT EXISTS t_view_sync_points (
    c_space_id TEXT NOT NULL,
    c_view_id TEXT NOT NULL,
    c_dataset_id TEXT NOT NULL,
    c_request_id TEXT NOT NULL,
    c_sync_point_id TEXT NOT NULL,
    c_applied_at TEXT NOT NULL,
    PRIMARY KEY (c_space_id, c_view_id, c_dataset_id, c_request_id)
);
```

将 metadata schema version 从当前值加 1；不要增加 `view_period_generations` 或 `c_build_fence_json`。

- [ ] **Step 4: 增加仓储接口和 Metadata RPC**

仓储接口：

```go
UpsertViewPeriodDatasetState(context.Context, *ViewPeriodDatasetState) error
ListViewPeriodDatasetStates(context.Context, spaceID, viewID, frequency string, periodTime int64) ([]*ViewPeriodDatasetState, error)
UpsertViewSyncPoint(context.Context, *ViewSyncPoint) error
HasViewSyncPoint(context.Context, spaceID, viewID, datasetID, requestID string) (bool, error)
DeleteViewPeriodStateBefore(context.Context, time.Time) (int64, error)
```

Metadata service 暴露内部 merge/query RPC，`WaitViewSyncPoint` 留在 Primary 或 Catalog 门面，由它轮询这些状态并尊重 context deadline。

- [ ] **Step 5: 运行 Storage Metadata 测试**

Run: `cd modules/storage && go test ./internal/service/metadata/... ./internal/service/catalog/...`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add modules/storage/schema/metadata.sql modules/storage/proto modules/storage/internal/service/metadata modules/storage/internal/service/catalog
git commit -m "feat(storage): persist view readiness state"
```

### Task 5: 将 View consumer 改为全局串行的多事件消费者

**Files:**
- Modify: `packages/jetstream/consumer.go`
- Modify: `packages/jetstream/consumer_test.go`
- Modify: `packages/events/consumer.go`
- Modify: `packages/events/consumer_test.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/delivery_policy.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/handler.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/subject_dispatcher.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer_test.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/delivery_policy_test.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/subject_dispatcher_test.go`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: `modules/storage/cmd/server/main_test.go`
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml`
- Modify: `modules/admin/cmd/cli/eventbus_credentials.go`
- Modify: `modules/admin/cmd/cli/eventbus_credentials_test.go`

- [ ] **Step 1: 写跨事件顺序失败测试**

用同一 stream 顺序投递 `row-1 -> row-2 -> DatasetPeriodCollected`，让 `row-1` 第一次返回 retry。断言 retry 成功前 `row-2` 和 Marker 均未进入 handler。再断言 durable 使用四个精确 family filter，不订阅 View 自己发布的两个 ready family：

```go
jetstream.ConsumerConfig{
    Stream: "MOOX_STORAGE",
    FilterSubjects: []string{
        "moox.storage.dataset.rows.upserted.v2.>",
        "moox.storage.dataset.period.collected.v1.>",
        "moox.storage.dataset.factor_period.computed.v1.>",
        "moox.storage.dataset.sync_point.v1.>",
    },
    DeliverPolicy: jetstream.DeliverAllPolicy,
    MaxDeliver:    -1,
    MaxAckPending: 1,
}
```

- [ ] **Step 2: 运行测试确认当前 subject dispatcher 会越过**

Run: `cd modules/storage && go test ./internal/service/view/eventconsumer/...`

Expected: FAIL，底层 consumer 尚不支持 `FilterSubjects`，或 Marker 被另一个 subject lane 提前处理。

- [ ] **Step 3: 在公共 consumer 层支持多个精确 filter**

给 `packages/jetstream.ConsumerConfig` 增加 `FilterSubjects []string`；`FilterSubject` 与 `FilterSubjects` 必须二选一。创建/对账 NATS consumer 时分别写 `nats.ConsumerConfig.FilterSubject` 或 `FilterSubjects`，比较配置时先排序去重。给 `packages/events` 增加：

```go
type MultiEventConsumerConfig struct {
    Name          string
    Events        []Event
    AckWait       time.Duration
    MaxDeliver    int
    MaxAckPending int
    FetchMaxWait  time.Duration
    DeliverPolicy nats.DeliverPolicy
}

func NewMultiEventConsumer(ctx context.Context, client *jetstream.Client, registry *Registry, cfg MultiEventConsumerConfig) (*Consumer, error)
```

它通过 registry 为每个 Event 生成 family pattern，拒绝空列表、不同 stream、重复 event，并传给 `FilterSubjects`。不要用 `moox.storage.>`，否则 View 会消费自己发布的 ready 事件。

- [ ] **Step 4: 实现全局串行模式**

把 View consumer 的默认值固定为 `FetchBatch=1`、`MaxWorkers=1`、`MaxAckPending=1`、`Ordering="global"`。`subject_dispatcher` 在 global 模式使用常量 queue key，不按 NATS subject 拆 lane。handler 用 `events.DecodeRaw` 后按 payload 类型调用：

```go
type Handler interface {
    HandleDatasetRows(context.Context, *eventpb.EventMessage, *storagepb.DatasetRowsUpserted) error
    HandleDatasetPeriodCollected(context.Context, *eventpb.EventMessage, *storagepb.DatasetPeriodCollected) error
    HandleFactorPeriodComputed(context.Context, *eventpb.EventMessage, *storagepb.FactorPeriodComputed) error
    HandleDatasetSyncPoint(context.Context, *eventpb.EventMessage, *storagepb.DatasetSyncPoint) error
}
```

暂时错误返回 retry 且不 ACK。无法解析或契约错误记录高优先级告警，并以较长 delay 持续 retry，保留当前消息和全局顺序供人工修复；不 TERM，也不增加 blocked-lane 状态表。

- [ ] **Step 5: 更新 EventBus topology 和凭据**

Task 1 已扩展 stream subjects 和 publish ACL；本步骤只把 Storage consumer API 权限从旧 `storage_view` 切到新 durable `storage_view_period_v1`，并把 Factor 权限从 `factor_calc` 改为 `factor_view_ready_v1`。Collector 保留行事件 consumer 权限。同步更新 credential contract tests。

- [ ] **Step 6: 装配新配置并跑测试**

Run: `cd packages/jetstream && go test ./...`

Run: `cd packages/events && go test ./...`

Run: `cd modules/admin && go test ./cmd/cli/...`

Run: `cd modules/storage && go test ./internal/service/view/eventconsumer/... ./internal/config/... ./cmd/server`

Expected: PASS，测试明确记录处理顺序为 `row-1,row-2,marker`。

- [ ] **Step 7: 提交**

```bash
git add packages/jetstream packages/events modules/storage/internal/service/view/eventconsumer modules/storage/cmd/server/main.go modules/storage/cmd/server/main_test.go modules/storage/internal/config modules/storage/config/storage.yaml modules/storage/config/storage_view/trpc_go.yaml modules/admin/cmd/cli/eventbus_credentials.go modules/admin/cmd/cli/eventbus_credentials_test.go
git commit -m "feat(storage): serialize view row and marker events"
```

### Task 6: View 发布 Source Ready、Result Ready 并应用 SyncPoint

**Files:**
- Create: `modules/storage/internal/service/view/period_ready.go`
- Create: `modules/storage/internal/service/view/sync_point.go`
- Modify: `modules/storage/internal/service/view/event_apply.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Test: `modules/storage/internal/service/view/period_ready_test.go`
- Test: `modules/storage/internal/service/view/sync_point_test.go`
- Modify: `modules/storage/internal/service/primarystore/marker.go`
- Test: `modules/storage/internal/service/primarystore/marker_test.go`

- [ ] **Step 1: 写单 Dataset、多 Dataset 和 Result View 测试**

测试精确断言：

- 单 Dataset Source View 收到 Marker 后立即发布一次 `ViewSourcePeriodReady`；
- 双 Dataset View 只收到一个 Marker 时不发布，两个到齐后发布，status 按任一 degraded 聚合；
- 重投同 Marker 使用固定 ready event ID/payload；
- `FactorPeriodComputed` 到达时直接发布对应 `ViewFactorPeriodReady`，不扫描行、不计算 hash；
- SyncPoint 记录到所有依赖该 Dataset 的 Active/Building View，`WaitViewSyncPoint` 只在请求所含 Dataset 全部完成后返回。

- [ ] **Step 2: 运行测试确认 handler 尚未实现**

Run: `cd modules/storage && go test ./internal/service/view/... ./internal/service/primarystore/...`

Expected: FAIL，缺少 Marker handler 或 ready publisher。

- [ ] **Step 3: 实现 Source View 聚合**

```go
func (s *Service) HandleDatasetPeriodCollected(ctx context.Context, msg *eventpb.EventMessage, p *storagepb.DatasetPeriodCollected) error
```

处理顺序：列出 Active Source Views -> 只保留周期输入包含该 Dataset 的 View -> upsert dataset state -> 读取该 View 的所有 periodic input datasets -> 未齐则返回 nil -> 聚合 sorted datasets/subjects/failures -> 用固定 ID 发布 `ViewSourcePeriodReady`。单 Dataset 仍走同一函数，不创建另一条特殊协议。

- [ ] **Step 4: 实现 Result Ready 和 SyncPoint**

```go
func (s *Service) HandleFactorPeriodComputed(ctx context.Context, msg *eventpb.EventMessage, p *storagepb.FactorPeriodComputed) error
func (s *Service) HandleDatasetSyncPoint(ctx context.Context, msg *eventpb.EventMessage, p *storagepb.DatasetSyncPoint) error
```

Result handler 通过结果 Dataset 查自动 Result View，确认 Active 后直接发布最终事件。SyncPoint handler 幂等保存应用状态；`WaitViewSyncPoint` 每 100ms 查询一次，context 到期返回 deadline exceeded，不建立额外 worker。

- [ ] **Step 5: 运行测试**

Run: `cd modules/storage && go test ./internal/service/view/... ./internal/service/primarystore/...`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add modules/storage/internal/service/view modules/storage/internal/service/primarystore
git commit -m "feat(storage): publish view period readiness"
```

### Task 7: Collector 持久化周期 readiness

**Files:**
- Modify: `modules/collector/schema/collector.sql`
- Create: `modules/collector/internal/domain/period_readiness.go`
- Create: `modules/collector/internal/store/period_readiness.go`
- Test: `modules/collector/internal/store/period_readiness_test.go`
- Create: `modules/collector/internal/marketfetch/period.go`
- Test: `modules/collector/internal/marketfetch/period_test.go`

- [ ] **Step 1: 写周期对齐和状态机失败测试**

固定 UTC 用例：`12:03:45` 在 `1m` 归为 `12:03:00`，在 `5m` 归为 `12:00:00`，在 `1h` 归为 `12:00:00`；再覆盖周/月日历边界。实现必须复用 `report.RecentDatasetTimes` 和现有 `targetDataTime` 语义，不用固定 duration 另写一套周/月对齐。readiness 状态覆盖：

```text
waiting + all success -> complete/report_pending
waiting + deadline + pending -> degraded/report_pending
report_pending + success/late row -> no state change
report_pending + RPC retry -> same event_id, collected_at, payload_json
report_pending + success RPC -> reported
```

- [ ] **Step 2: 运行测试确认 schema/repository 不存在**

Run: `cd modules/collector && go test ./internal/store/... ./internal/marketfetch/...`

Expected: FAIL，缺少 readiness repository。

- [ ] **Step 3: 增加两张表**

增加设计文档第 5.4 节的 `t_period_readiness`，并在 item 中保存预建时的不可变任务来源：

```sql
CREATE TABLE IF NOT EXISTS t_period_readiness_items (
    c_readiness_id INTEGER NOT NULL,
    c_task_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_function_name TEXT NOT NULL,
    c_write_source TEXT NOT NULL,
    c_required_fields_json TEXT NOT NULL DEFAULT '[]',
    c_state TEXT NOT NULL,
    c_updated_at TEXT NOT NULL,
    PRIMARY KEY (c_readiness_id, c_task_id),
    UNIQUE (c_readiness_id, c_subject_id),
    FOREIGN KEY (c_readiness_id) REFERENCES t_period_readiness(c_id)
);
```

预建时若同一 `(space,dataset,frequency,period,subject)` 对应多个未删除 TaskInstance，立即报配置冲突，不静默合并。额外索引只增加：

```sql
CREATE INDEX IF NOT EXISTS idx_period_readiness_report
ON t_period_readiness (c_report_state, c_deadline_at);
```

SQLite 是权威状态；内存只缓存当前周期 ID。父状态的 `payload_json` 在第一次全部终态时一次性写入，后续重试不可重建时间字段。

- [ ] **Step 4: 实现仓储原子转换**

```go
type PeriodReadinessRepository interface {
    EnsurePeriod(context.Context, PeriodSeed) (int64, error)
    MarkSubjectSuccess(context.Context, PeriodKey, string, time.Time) error
    FinalizeDue(context.Context, time.Time) (int64, error)
    ListPendingReports(context.Context, int) ([]PeriodReport, error)
    MarkReported(context.Context, int64, time.Time) error
    DeleteBefore(context.Context, time.Time) (int64, error)
}
```

`MarkSubjectSuccess` 和 `FinalizeDue` 在事务内检查是否全部终态；首次终态时同时固定 status、event ID、collected_at 和规范化 payload JSON。

- [ ] **Step 5: 运行测试**

Run: `cd modules/collector && go test ./internal/store/... ./internal/marketfetch/...`

Expected: PASS，重复 transition 不改变固定 payload。

- [ ] **Step 6: 提交**

```bash
git add modules/collector/schema/collector.sql modules/collector/internal/domain/period_readiness.go modules/collector/internal/store/period_readiness.go modules/collector/internal/store/period_readiness_test.go modules/collector/internal/marketfetch/period.go modules/collector/internal/marketfetch/period_test.go
git commit -m "feat(collector): persist period readiness"
```

### Task 8: Collector 预建周期、监听行事件并上报一次完成

**Files:**
- Create: `modules/collector/internal/marketfetch/period_readiness.go`
- Create: `modules/collector/internal/marketfetch/period_reporter.go`
- Test: `modules/collector/internal/marketfetch/period_readiness_test.go`
- Test: `modules/collector/internal/marketfetch/period_reporter_test.go`
- Modify: `modules/collector/internal/marketfetch/storage_write_consumer.go`
- Modify: `modules/collector/internal/marketfetch/storage_write_consumer_test.go`
- Modify: `modules/collector/internal/bootstrap/bootstrap.go`
- Modify: `modules/collector/internal/bootstrap/config.go`
- Modify: `modules/collector/config/app.yaml`

- [ ] **Step 1: 写三种周期结果和 RPC 重试测试**

覆盖：全部成功、部分超时、整次 Timer 未执行。再模拟 Storage RPC“服务端成功但客户端超时”，第二次调用必须携带完全相同 event ID 和 payload，最终只生成一个 Marker。

- [ ] **Step 2: 运行目标测试确认失败**

Run: `cd modules/collector && go test ./internal/marketfetch/... ./internal/bootstrap/...`

Expected: FAIL，缺少 `PeriodReadinessTracker`/`PeriodReporter`。

- [ ] **Step 3: 实现周期预建**

```go
type PeriodReadinessTracker struct {
    instances TaskInstanceRepository
    periods   PeriodReadinessRepository
    grace     func(frequency string) time.Duration
}

func (t *PeriodReadinessTracker) EnsureCurrentAndNext(ctx context.Context, now time.Time) error
func (t *PeriodReadinessTracker) ApplyRows(ctx context.Context, p *storagepb.DatasetRowsUpserted) error
```

启动恢复时和每次 `Scheduler.Tick -> Reconciler.Reconcile` 后调用 `EnsureCurrentAndNext`。它从全部未删除 TaskInstance 按 `(space,dataset,frequency)` 预建当前和下一周期，不放进“fingerprint 变化才 Upsert TaskInstance”的条件分支；assignment 变化只影响尚未创建的周期。SCF 的 `TimerRequestFromEnv` 保持无状态，不访问 Collector SQLite。`ApplyRows` 使用 RowKey `data_time` 和 `report.RecentDatasetTimes` 对齐，不使用 envelope `occurred_at`；只接受匹配预建 task ID、function/write_source 且包含必需字段的行。

- [ ] **Step 4: 把 readiness 更新放进 ACK 前**

`storage_write_consumer` 现有 TaskInstance freshness 更新成功后调用 `ApplyRows`。任一 SQLite 写失败都返回 retry；两者成功才 ACK。不要依赖 `MarketFetchBatchCompleted`，它继续只服务 fetch 调度/指标。

- [ ] **Step 5: 实现 deadline/reporter 循环**

```go
func (r *PeriodReporter) Run(ctx context.Context) error {
    ticker := time.NewTicker(r.interval)
    defer ticker.Stop()
    for {
        if err := r.flush(ctx); err != nil { r.metrics.ReportError(err) }
        select {
        case <-ctx.Done(): return ctx.Err()
        case <-ticker.C:
        }
    }
}
```

每次 flush 先 `FinalizeDue(now)`，再分页读取 `report_pending`，调用 `ReportDatasetPeriodCollected`，成功后 `MarkReported`。单条失败记录日志并留待下轮，不阻塞其他 dataset。

- [ ] **Step 6: 装配、配置和测试**

配置默认值：`period_ready_grace = 2 x frequency` 且最大 10m、report interval 5s、item retention 60 periods、parent retention 7d。Bootstrap 顺序必须是：恢复/预建 readiness -> 启动 reporter -> 启动 storage-write consumer -> 注册后续 schedule。不要复用 `StartCompletionConsumer` 或 FetchBatch timeout 判断 Storage 是否落盘。

Run: `cd modules/collector && go test ./...`

Expected: PASS。

- [ ] **Step 7: 提交**

```bash
git add modules/collector/internal/marketfetch modules/collector/internal/bootstrap modules/collector/config/app.yaml
git commit -m "feat(collector): report completed dataset periods"
```

### Task 9: Factor binding 改为 Source View 并自动创建 Result View

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Modify: `modules/factor/schema/factor.sql`
- Modify: `modules/factor/internal/domain/binding.go`
- Modify: `modules/factor/internal/store/binding.go`
- Modify: `modules/factor/internal/store/database.go`
- Modify: `modules/factor/internal/store/database_test.go`
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/registry/binding_contract.go`
- Modify: `modules/factor/internal/registry/binding_contract_test.go`
- Modify: `modules/factor/internal/registry/metadata_sync.go`
- Modify: `modules/factor/internal/registry/metadata_sync_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap_test.go`

- [ ] **Step 1: 写 binding 和自动 View 失败测试**

断言：binding 接口只接收 `source_view_id`，自动推导 `result_dataset_id/result_view_id`；Source View `keep_duration` 必须为 0；Source View 不能包含 `factor_result` origin；Result View filter 固定包含 binding frequency；所需字段未激活时 binding 为 `pending_view`，激活后 reconciler 置为 `enabled`。

- [ ] **Step 2: 运行 Factor store/registry 测试确认失败**

Run: `cd modules/factor && go test ./internal/store/... ./internal/registry/... ./internal/rpc/...`

Expected: FAIL，现有模型仍要求 `source_dataset/target_dataset`。

- [ ] **Step 3: 修改公开模型和 SQLite schema**

`FactorBinding` 改为：

```protobuf
message FactorBinding {
  string binding_id = 1;
  string factor_id = 2;
  string space_id = 3;
  string source_view_id = 4;
  string freq = 5;
  string subject_mode = 6;
  string subjects_json = 7;
  string result_dataset_id = 8; // response-only
  string result_view_id = 9;    // response-only
  string status = 10;           // pending_view | enabled | disabled | cleanup_pending
  string created_at = 11;
  string updated_at = 12;
}
```

SQLite 用 `c_source_view_id/c_result_dataset_id/c_result_view_id` 替代旧列，status check 加 `pending_view` 和 `cleanup_pending`。项目仍处于新建阶段，沿用现有 obsolete-schema 拒绝策略：检测旧列时给出清晰错误并要求重建 Factor SQLite，不实现在线迁移。

- [ ] **Step 4: 扩展 metadata sync**

稳定命名函数：

```go
func ResultDataset(sourceViewID string) string { return resultObjectID(sourceViewID, "_factor") }
func ResultView(sourceViewID string) string    { return resultObjectID(sourceViewID, "_factor_view") }
```

`resultObjectID` 从现有 `ResultDataset` 提取：输入先 lower/trim，候选不超过 20 字符时直接返回；超长时保留 SHA-1 前 8 bytes 的稳定 suffix，并按 20 字符上限截断 prefix。Dataset/View suffix 不同，避免同 namespace 冲突。

扩展 `MetadataSync` client interface 和 bootstrap adapter，加入 `CreateView/GetView/UpdateView/ListViewColumns/UpsertViewColumn`。复用现有结果 Dataset 创建逻辑，将 Dataset attributes 写为 `dataset_role=factor_result`、`write_owner=factor`、实际 `source_view_id`、计算得到的 `result_view_id` 和 binding frequency，使 Storage 能从 Source View 稳定解析结果 Dataset；并在同 space 创建 time-series Result View。Result View 只包含该结果 Dataset、`keep_duration=0`、`filter_json` 明确写入 frequency。output fields 加入 desired schema；Active schema 满足后 reconciler 将 binding 从 `pending_view` 置为 `enabled`。

- [ ] **Step 5: 生成 proto 并跑测试**

Run: `make -C modules/factor/proto`

Run: `cd modules/factor && go test ./internal/store/... ./internal/registry/... ./internal/rpc/...`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add modules/factor/proto modules/factor/schema/factor.sql modules/factor/internal/domain modules/factor/internal/store modules/factor/internal/registry modules/factor/internal/rpc modules/factor/internal/bootstrap
git commit -m "feat(factor): bind factors to source views"
```

### Task 10: Factor 只消费 Source Ready 并通过 View 读取

**Files:**
- Delete: `modules/factor/internal/trigger/event_batcher.go`
- Delete: `modules/factor/internal/trigger/event_batcher_test.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/consumer.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/handler.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/jetstream.go`
- Modify: `modules/factor/internal/trigger/eventconsumer/consumer_test.go`
- Create: `modules/factor/internal/trigger/eventconsumer/handler_test.go`
- Create: `modules/factor/internal/trigger/period_executor.go`
- Test: `modules/factor/internal/trigger/period_executor_test.go`
- Modify: `modules/factor/internal/scheduler/task.go`
- Modify: `modules/factor/internal/scheduler/builder.go`
- Modify: `modules/factor/internal/scheduler/service.go`
- Modify: `modules/factor/internal/scheduler/service_test.go`
- Modify: `modules/factor/internal/storageio/client.go`
- Modify: `modules/factor/internal/storageio/client_test.go`
- Modify: `modules/factor/internal/bootstrap/bootstrap.go`
- Modify: `modules/factor/internal/bootstrap/config.go`
- Modify: `modules/factor/config/app.yaml`

- [ ] **Step 1: 写 consumer ACK 和执行组失败测试**

断言 consumer 配置为新 durable 名 `factor_view_ready_v1`、`DeliverAll`、`MaxDeliver=-1`、`MaxAckPending=1`；重复 ready 若 `GetFactorPeriodComputed` 命中则 ACK 且不调用 Python；未命中时只有所有 bindings 完成并成功上报 Factor Marker 才 ACK；`jetstream.RunnerConfig.InProgressInterval=30s` 时长任务会自动续 ACK wait。

- [ ] **Step 2: 运行测试确认当前仍依赖 EventBatcher**

Run: `cd modules/factor && go test ./internal/trigger/... ./internal/storageio/...`

Expected: FAIL，当前 handler 只解码 `DatasetRowsUpserted`。

- [ ] **Step 3: 定义单执行器**

```go
type PeriodTrigger struct {
    TriggerEventID string
    TriggeredAt    time.Time
    SourceViewID   string
    Frequency      string
    PeriodTime     time.Time
    Status         string
    PrimarySubjects []string
}

type PeriodExecutor struct {
    operationMu sync.Mutex
    bindings   BindingRepository
    scheduler  *scheduler.Scheduler
    storage    PeriodStorage
}

func (e *PeriodExecutor) Execute(ctx context.Context, trigger PeriodTrigger) error
```

`Execute` 在 `operationMu` 下：preflight Marker -> 读取当前 enabled bindings -> 取 primary subjects 与 binding include 的交集 -> 逐 binding/subject 调用现有 scheduler/engine -> 汇总状态 -> 上报 `FactorPeriodComputed`。source-ready status 为 degraded 时仍执行有输入的 subject。

现有 `scheduler.Task` 增加 `BindingID/SourceViewID/PeriodTime/TriggerEventID/TriggeredAt`，builder 必须显式填充；保留 `Service.Run/runValidated` 作为单任务原语，移除其中 settle sleep 和 View 完整度轮询。旧的多 shard `Enqueue/runShard` 不再作为 realtime 入口。

- [ ] **Step 4: 将读路径改为 DataView**

`storageio.Client` 注入 `storagepb.DataViewClientProxy`，使用：

```go
QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
    SpaceId: source.SpaceID,
    ViewId:  source.SourceViewID,
    Selectors: []*storagepb.TimeSeriesSelector{{
        SubjectId: source.SubjectID,
        Freq:      source.Frequency,
    }},
    TimeRange: lookbackRange,
    ColumnNames: binding.InputColumns,
})
```

不再通过 dataset ID 让 Primary 自动选择 View，不再使用 `ViewSettleDelay` 或 `ErrViewIncomplete` 读取重试。

- [ ] **Step 5: 替换 consumer 和 bootstrap**

handler 解码 `ViewSourcePeriodReady`，构造 `PeriodTrigger` 后同步调用 `Execute`。Runner 直接使用已有 heartbeat 能力配置 `InProgressInterval=30s`，不要另起 goroutine。删除 EventBatcher、其 loop 和配置项 `event_batch_window_ms/view_settle_delay/event_read_retry*`。保留 consumer 断线重连逻辑。

- [ ] **Step 6: 运行测试**

Run: `cd modules/factor && go test ./internal/trigger/... ./internal/storageio/... ./internal/bootstrap/...`

Expected: PASS，代码搜索不再命中旧 settle 链。

Run: `rg -n "EventBatcher|ViewSettleDelay|EventReadRetry|ErrViewIncomplete" modules/factor`

Expected: 无输出。

- [ ] **Step 7: 提交**

```bash
git add modules/factor/internal/trigger modules/factor/internal/scheduler modules/factor/internal/storageio modules/factor/internal/bootstrap modules/factor/config/app.yaml
git commit -m "feat(factor): trigger calculations from view readiness"
```

### Task 11: 用 manifest 清理动态输出并追加 Factor Marker

**Files:**
- Modify: `modules/factor/schema/factor.sql`
- Create: `modules/factor/internal/domain/output_manifest.go`
- Create: `modules/factor/internal/store/output_manifest.go`
- Test: `modules/factor/internal/store/output_manifest_test.go`
- Modify: `modules/factor/internal/storageio/writeback.go`
- Modify: `modules/factor/internal/storageio/writeback_test.go`
- Modify: `modules/factor/internal/trigger/period_executor.go`
- Modify: `modules/factor/internal/trigger/period_executor_test.go`

- [ ] **Step 1: 写动态 tag 清理失败测试**

覆盖三个序列：第一次输出 tag A，第二次输出 tag B，第三次零行。断言第二次写入 B 并将 A 的本 binding 字段写为 NULL，第三次将 B 写为 NULL；其他 binding 字段和 source 字段不受影响；所有 clear/upsert 成功后才调用 `ReportFactorPeriodComputed`。

- [ ] **Step 2: 运行测试确认零行仍直接返回**

Run: `cd modules/factor && go test ./internal/storageio/... ./internal/store/... ./internal/trigger/...`

Expected: FAIL，现有 `WriteFactorPatch` 在零行时返回且没有 manifest。

- [ ] **Step 3: 增加 manifest 表**

```sql
CREATE TABLE IF NOT EXISTS t_factor_output_manifests (
    c_binding_id TEXT NOT NULL,
    c_subject_id TEXT NOT NULL,
    c_frequency TEXT NOT NULL,
    c_period_time INTEGER NOT NULL,
    c_row_keys_json TEXT NOT NULL DEFAULT '[]',
    c_updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (c_binding_id, c_subject_id, c_frequency, c_period_time),
    FOREIGN KEY (c_binding_id) REFERENCES t_factor_bindings(c_binding_id)
);
```

row keys 采用 Storage RowKey 的规范 JSON，写入前排序去重。

- [ ] **Step 4: 实现确定性写计划**

```go
type FactorWritePlan struct {
    Upserts []*storagepb.RowFieldUpsert
    Clears  []*storagepb.RowFieldUpsert
    Current []storageio.RowKey
}

func BuildFactorWritePlan(previous []RowKey, result *engine.FactorResult, outputFields []string) FactorWritePlan
```

`previous - current` 生成仅包含本 binding output fields 的 NULL patch。零行仍执行 clear。upsert 和 clear 的 `source_event_id` 分别使用 `hash(trigger_event_id,binding_id,subject_id,period,"upsert")` 与 `hash(...,"clear")`；`factor.computed_at` 使用 trigger 的固定时间，不能用每次重试的 `time.Now()`。

- [ ] **Step 5: 规定提交顺序**

每个任务顺序固定为：构造 plan -> 写 clears -> 写 upserts -> 事务更新 manifest。clear 只包含 `previous-current`，不会清掉本次 current RowKey。整个执行组全部终态后再上报 Marker。若 manifest 更新前退出，重投会再次 clear/upsert；若 Marker 已成功但 ACK 丢失，Task 10 preflight 直接 ACK。

- [ ] **Step 6: 运行测试**

Run: `cd modules/factor && go test ./internal/storageio/... ./internal/store/... ./internal/trigger/...`

Expected: PASS，A -> B -> empty 三阶段均无旧值残留。

- [ ] **Step 7: 提交**

```bash
git add modules/factor/schema/factor.sql modules/factor/internal/domain modules/factor/internal/store modules/factor/internal/storageio modules/factor/internal/trigger
git commit -m "feat(factor): clear dynamic outputs before period markers"
```

### Task 12: Recalc 和 import/catchup 接入 SyncPoint

**Files:**
- Modify: `modules/factor/proto/factor.proto`
- Modify: `modules/factor/internal/rpc/recalc.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/trigger/period_executor.go`
- Modify: `modules/cli/internal/command/storage_import.go`
- Modify: `modules/cli/internal/command/storage_import_test.go`
- Modify: `modules/collector/internal/marketfetch/executor.go`
- Modify: `modules/collector/internal/marketfetch/executor_test.go`
- Modify: `modules/collector/internal/sources/binance/storage_rpc.go`
- Create: `modules/collector/internal/sources/binance/storage_rpc_test.go`

- [ ] **Step 1: 写 SyncPoint 等待和 realtime/Recalc 串行测试**

断言 import/catchup 的 Upsert 返回后先追加每个 Dataset 的 SyncPoint；`WaitViewSyncPoint` 未完成时不得调用 Recalc；realtime 正在执行时 Recalc 阻塞在同一个 `operationMu`；相同 `request_id + period` 重试命中 Factor Marker 后跳过 Python。

- [ ] **Step 2: 运行测试确认当前 import 会抢跑 View**

Run: `cd modules/cli && go test ./internal/command/...`

Run: `cd modules/factor && go test ./internal/rpc/... ./internal/trigger/...`

Expected: FAIL，当前 import 只等待 `UpsertFields` 返回，Recalc 直接进入 scheduler。

- [ ] **Step 3: 完善 Recalc 请求**

```protobuf
message RecalcFactorReq {
  string request_id = 1;
  string factor_id = 2;
  string space_id = 3;
  string source_view_id = 4;
  string subject_id = 5;
  string freq = 6;
  string start_time = 7;
  string end_time = 8;
  string sync_request_id = 9;
}
```

`request_id` 必填；若有 `sync_request_id`，Factor 先调用 `WaitViewSyncPoint`。每个周期构造 `trigger_event_id=hash(request_id,period_time)`，再调用同一个 `PeriodExecutor.Execute`。

- [ ] **Step 4: 修改 import/catchup**

CLI 每次操作生成一个稳定 request ID，并对本次涉及的每个 Dataset 调用：

```text
UpsertFields(all batches)
  -> AppendDatasetSyncPoint(request_id,dataset_id,source)
  -> WaitViewSyncPoint(request_id,view_id,all dataset_ids)
  -> optional RecalcFactor(sync_request_id=request_id)
```

命令重试允许用户显式复用 request ID。对于 `BatchKindCatchup`，SCF `Executor` 在聚合 `UpsertFieldsWithSource` 成功后，通过扩展后的 Binance Storage RPC writer 追加 `DatasetSyncPoint(request_id=batch_id, source=catchup)`；SyncPoint 失败时本 batch 返回 Storage error，现有 batch/source ID 重试会幂等补齐。catchup 不发布 `DatasetPeriodCollected`，也不自动触发 Recalc；后续 Recalc 使用该 batch ID 作为 `sync_request_id`。

- [ ] **Step 5: 生成 proto 并运行三个模块测试**

Run: `make -C modules/factor/proto`

Run: `cd modules/collector && go test ./internal/marketfetch/...`

Run: `cd modules/factor && go test ./internal/rpc/... ./internal/trigger/...`

Run: `cd modules/cli && go test ./internal/command/...`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add modules/factor/proto modules/factor/internal/rpc modules/factor/internal/trigger/period_executor.go modules/cli/internal/command/storage_import.go modules/cli/internal/command/storage_import_test.go modules/collector/internal/marketfetch/executor.go modules/collector/internal/marketfetch/executor_test.go modules/collector/internal/sources/binance/storage_rpc.go modules/collector/internal/sources/binance/storage_rpc_test.go
git commit -m "feat(storage): synchronize bulk writes before recalc"
```

### Task 13: 简化 View A/B build，不引入 build fence

**Files:**
- Modify: `modules/storage/internal/service/view/build.go`
- Modify: `modules/storage/internal/service/view/live_gate.go`
- Modify: `modules/storage/internal/service/view/consume.go`
- Modify: `modules/storage/internal/service/view/eventconsumer/consumer.go`
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Modify: `modules/storage/internal/service/view/build_test.go`
- Modify: `modules/storage/internal/service/view/reconcile_test.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager.go`
- Modify: `modules/storage/internal/service/viewindex/duckdb/index_manager_test.go`

- [ ] **Step 1: 写暂停、全量字段合并和崩溃重建测试**

测试场景：Active 旧行含 `close+volume`，Building 先收到只含 `close` 的 live delta；backfill 后 Building 必须保留 `volume`。build 期间暂停产生 row/Marker 积压，恢复双写后按序追平；第二次短暂停顿激活；中途失败时 Building 被删除，下次 reconcile 从空 Building 全量开始。

- [ ] **Step 2: 运行目标测试确认当前整行跳过问题**

Run: `cd modules/storage && go test ./internal/service/view/... ./internal/service/viewindex/duckdb/...`

Expected: FAIL，当前 backfill 对已存在 RowKey 整行跳过，字段可能不完整。

- [ ] **Step 3: 实现受控暂停流程**

给 View consumer 增加 `Pause/Resume/WaitIdle`，使用现有 `liveGate`，阶段固定为：暂停 fetch 并 `WaitIdle` -> 创建/清空 Building -> 全量回填 -> 开启 Active/Building 双写 -> Resume 并消费 backlog -> backlog 为 0 后再次 Pause/WaitIdle -> 激活 Building -> Resume。Marker 在追平阶段仍按当前 Active 发布 ready。

DuckDB backfill 对同 RowKey 按字段 COALESCE/merge，不再因为 RowKey 存在就跳过整行。替换当前 `resumeViewBuild` 续建语义：失败恢复只保留 build error/rows/time，删除 Building 后从头构建。

- [ ] **Step 4: 证明没有新增复杂状态**

Run: `rg -n "view_period_generations|c_build_fence_json|snapshot_hash|sealed_generation|build_fence" modules/storage docs/因子视图驱动计算设计.md`

Expected: 只允许设计文档中“明确不使用这些字段”的说明，不允许 schema/proto/Go 定义命中。

- [ ] **Step 5: 运行 View 测试**

Run: `cd modules/storage && go test ./internal/service/view/... ./internal/service/viewindex/duckdb/...`

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add modules/storage/internal/service/view modules/storage/internal/service/viewindex/duckdb
git commit -m "refactor(storage): simplify view rebuild synchronization"
```

### Task 14: 生命周期、保留策略、指标与文档收口

**Files:**
- Modify: `modules/factor/internal/rpc/service.go`
- Modify: `modules/factor/internal/rpc/service_test.go`
- Modify: `modules/factor/internal/registry/service.go`
- Modify: `modules/factor/internal/registry/service_test.go`
- Modify: `modules/collector/internal/marketfetch/period_reporter.go`
- Modify: `modules/collector/internal/marketfetch/metrics.go`
- Modify: `modules/collector/internal/marketfetch/metrics_test.go`
- Modify: `modules/storage/internal/service/view/period_ready.go`
- Modify: `modules/storage/internal/observability/view_metrics.go`
- Modify: `modules/storage/internal/observability/view_metrics_test.go`
- Create: `modules/factor/internal/observability/period_metrics.go`
- Create: `modules/factor/internal/observability/period_metrics_test.go`
- Modify: `docs/因子计算模块设计.md`
- Modify: `docs/内置市场行情采集架构.md`
- Modify: `docs/存储层架构.md`
- Modify: `docs/运维/MooX指标监控.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`

- [ ] **Step 1: 写 binding 生命周期和 retention 测试**

disable 先把 binding 置为 `cleanup_pending` 阻止新任务，再按 manifest 清理保留窗口内旧字段；成功转为 disabled，失败保留 `cleanup_pending` 和 manifest 供重试。unbind 只有在清理成功且 Result View 新 revision 激活后才删除 binding/manifest。retention worker 只删除已 reported 的 Collector 父状态、过期 View dataset state/SyncPoint 和超出窗口的 manifest。

- [ ] **Step 2: 增加最小运行指标**

固定指标：

```text
moox_collector_period_pending_total{dataset,frequency}
moox_collector_period_report_retry_total{dataset,frequency}
moox_view_period_waiting_datasets{view,frequency}
moox_view_ready_publish_retry_total{view,event}
moox_factor_period_running{source_view,frequency}
moox_factor_period_degraded_total{source_view,frequency}
moox_factor_manifest_clear_total{binding}
moox_factor_source_ready_lag_seconds{source_view,frequency}
```

written/cleared row count 只写 Metrics 和结构化日志，不加回公共事件。

- [ ] **Step 3: 更新配置和架构文档**

文档明确删除 row EventBatcher、settle delay、read retry；补充四事件链、latest-wins、Result View 分离、SyncPoint、保留期和人工恢复操作。不得描述 generation/hash/fence 或自动 restatement 为一期能力。

- [ ] **Step 4: 运行模块测试**

Run: `cd modules/collector && go test ./...`

Run: `cd modules/storage && go test ./...`

Run: `cd modules/factor && go test ./...`

Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add modules/collector/internal/marketfetch modules/storage/internal/service/view/period_ready.go modules/storage/internal/observability modules/factor/internal/rpc modules/factor/internal/registry modules/factor/internal/observability docs
git commit -m "docs(factor): document view-driven period operations"
```

### Task 15: 端到端验收和发布切换

**Files:**
- Create: `modules/factor/test/view_driven_e2e_test.go`
- Create: `scripts/tests/e2e/test-factor-view-ready-e2e.sh`
- Modify: `scripts/verify-event-contracts.sh`
- Modify: `Makefile`

- [ ] **Step 1: 写完整链路 E2E**

`view_driven_e2e_test.go` 启动临时 NATS、Storage、Collector 和 Factor，至少覆盖：

1. 两个 subject 全成功，最终收到 complete；
2. 一个 subject deadline 超时，最终收到 degraded 且失败清单正确；
3. 整次 Timer 未执行仍由预建周期形成 degraded；
4. Marker RPC 超时重试只产生一个确定性 Marker；
5. 双 Dataset Source View 只在两个 Marker 到齐后 ready；
6. Factor 进程在结果写入后、Marker 前退出，重启后重算覆盖；
7. Factor Marker 已交接但 source-ready 未 ACK，重投只做 preflight；
8. 动态 tag A -> B -> empty 时旧输出被 clear；
9. Result 行应用失败时不发布最终 ready；
10. import SyncPoint 完成前 Recalc 不启动；
11. Recalc 与 realtime 不并发；
12. A/B build 追平、激活、失败后全量重建；
13. Source View 查询不返回 factor-result 行；
14. manual/import 写自动结果 Dataset 被拒绝。

- [ ] **Step 2: 运行完整链路 E2E**

Run: `./scripts/tests/e2e/test-factor-view-ready-e2e.sh`

Expected: PASS。若失败，按首个未满足的 ACK/Marker/View 状态断言回到对应任务修复，不在 E2E 中放宽契约。

- [ ] **Step 3: 串行生成所有 proto**

不要与 Go 测试并行，避免测试读取生成中间态：

```bash
make proto
git status --short
```

Expected: 只出现本计划预期的生成文件，不出现意外删除或无关格式化修改。

- [ ] **Step 4: 运行分模块验证**

```bash
(cd packages/storagepb && go test ./...)
(cd packages/events && go test ./...)
(cd modules/storage && go test ./...)
(cd modules/collector && go test ./...)
(cd modules/factor && go test ./...)
(cd modules/cli && go test ./...)
```

Expected: 全部 PASS。

- [ ] **Step 5: 运行 Factor Python 和 workspace 验证**

```bash
cd modules/factor
PYTHONPATH="$PWD/../../packages/pyruntime/python:pyworker" \
  uv run --with-requirements pyworker/requirements.txt python -m pytest pyworker -q
cd ../..
./scripts/verify-event-contracts.sh
./scripts/test-go-workspace.sh
make verify-pr
```

Expected: 全部 PASS；若仓库已有基线失败，记录完整命令、首个错误和与本变更无关的证据，不把局部测试冒充全量通过。

- [ ] **Step 6: 运行竞态和 E2E 验证**

```bash
(cd modules/storage && go test -race -count=1 ./internal/service/view/... ./internal/service/datanode/...)
(cd modules/collector && go test -race -count=1 ./internal/marketfetch/...)
(cd modules/factor && go test -race -count=1 ./internal/trigger/... ./internal/storageio/...)
./scripts/tests/e2e/test-factor-storage-e2e.sh
./scripts/tests/e2e/test-series-tag-e2e.sh
./scripts/tests/e2e/test-factor-view-ready-e2e.sh
```

Expected: 全部 PASS。

- [ ] **Step 7: 做破坏式 schema 切换检查**

发布前停止 Collector、View、Factor；备份 SQLite/Pebble；升级 Storage Metadata schema；清理并重建旧 Factor SQLite binding schema；先启动 Storage/View，再启动 Collector，最后启动 Factor。确认新 durable backlog 为 0、Source/Result View 均 Active 后再恢复交易策略。

回滚边界：代码回滚前停止新 Collector/Factor；旧 Factor 不能读取新 binding schema，因此回滚必须同时恢复发布前 SQLite 备份。DataNode 中新增 Marker 是可忽略事件，不需要回滚 Pebble 行数据。

- [ ] **Step 8: 提交验收测试**

```bash
git add modules/factor/test/view_driven_e2e_test.go scripts/tests/e2e/test-factor-view-ready-e2e.sh scripts/verify-event-contracts.sh Makefile
git commit -m "test(factor): verify view-driven calculation end to end"
```

## 4. 完成定义

以下条件全部满足才算实施完成：

- Collector 能在全成功、部分超时和整次 Timer 未执行三种情况下形成一次固定 Dataset Marker。
- Dataset Marker 与对应 K 线行由同一 DataNode outbox 按先行后 Marker 发布。
- View 的单 durable 不会越过失败行处理后续 Marker。
- 多 Dataset Source View 只在全部输入到齐后发布 source-ready。
- Factor 实时代码不再订阅 `DatasetRowsUpserted`，不再存在 EventBatcher/settle/read-retry 链。
- Factor 从绑定的 Source View 读取，并只向独立 Result Dataset 写入。
- 动态 tag 缩小和零行输出会清理本 binding 的旧值。
- Factor source-ready 只有在 Factor Marker 已交接后才 ACK。
- Result View 只有在结果行已应用后才发布最终 ready。
- import/catchup 通过 SyncPoint 等待 View 后才触发 Recalc。
- realtime、Recalc、disable/unbind 在 Factor 单实例操作队列中串行。
- A/B build 能短暂停顿并从失败中全量重建，没有 `view_period_generations` 或 `c_build_fence_json`。
- 新公共事件、RPC、schema、配置、指标和运维文档一致。
- 分模块测试、race、Python、workspace 和 E2E 验证结果已记录。

## 5. 明确不做

- 不保证每个中间状态都产生一次且仅一次事件。
- 不提供旧 final 事件对应的历史结果快照。
- 不自动处理 degraded 后迟到 K 线的 restatement。
- 不自动扩展历史 Recalc 对未来 lookback 周期的影响范围。
- 不支持多实例 View/Factor、分布式锁或通用工作流引擎。
- 不把 RowKey manifest、written/cleared count、物理 index ID 或 schema hash 放进公共事件。
- 不新增 `view_period_generations`、`c_build_fence_json`、generation、previous report、snapshot hash、read fence、barrier、reservation 或 emission history。
