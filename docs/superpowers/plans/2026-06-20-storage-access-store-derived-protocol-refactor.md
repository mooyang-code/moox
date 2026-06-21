# Storage Access / Store / View Protocol Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reorganize the storage protobuf contracts and implementation around Access, PrimaryStore, View, Dataset, TimeSeries, and Record concepts, with no legacy compatibility surface.

**Execution status (2026-06-20):** Completed in the current workspace. Verified with:

```bash
cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test ./...
cd modules/cli && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test ./...
cd modules/storage && env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -tags e2e -timeout 600s -v ./tests/e2e/...
```

**Completion audit:** The implementation items below have been executed in this worktree: proto split, generated code refresh, Access/PrimaryStore/View service migration, TimeSeries/Record storage paths, event split, CLI/config/docs/tests migration, e2e direct-storage verification, and independent agent review. The checkbox list is retained as the original execution plan for traceability. Commit/push is treated as a separate handoff action and is not part of this goal unless explicitly requested.

**Architecture:** User-facing fact writes and source-of-truth reads live in `access.proto` through `AccessService`. Internal online primary storage execution lives in `store.proto` through `PrimaryStoreService` and `PrimaryStore*` row/key/target types. View read models live in `view.proto` through `ViewService`, where TimeSeries is queried via DuckDB-backed views and Record is searched via Bleve-backed views.

**Tech Stack:** Go, tRPC-Go, protobuf/trpc-open generated code, Pebble primary store, DuckDB view store, Bleve search index, SQLite metadata, NATS/event bus abstraction, Go unit/e2e tests.

---

## Final Protocol Shape

The final proto files are:

```text
modules/storage/proto/common.proto
modules/storage/proto/access.proto
modules/storage/proto/metadata.proto
modules/storage/proto/store.proto
modules/storage/proto/view.proto
modules/storage/proto/message.proto
```

The old proto files must be removed:

```text
modules/storage/proto/data.proto
modules/storage/proto/primary.proto
modules/storage/proto/query.proto
```

The final service names are:

```text
AccessService
MetadataService
PrimaryStoreService
ViewService
```

The final externally visible row models are:

```text
TimeSeriesKey / TimeSeriesRow
RecordKey / RecordRow
```

The final internal primary store row models are:

```text
PrimaryStoreKey
PrimaryStoreRow
PrimaryStoreTarget
```

The final primary metadata topology names are:

```text
PrimaryStoreNode
PrimaryStoreRoute
```

## Non-Negotiable Requirements

- No compatibility layer: remove old user-facing `WriteRows`, `ReadRows`, `DataKey`, `DataRow`, `DataScope`, and `ReadMode`.
- Replace `Object*` naming with `Record*`: `RecordKey`, `RecordRow`, `WriteRecordRows`, `ReadRecordRows`, and `record_id`.
- Keep `keys` as the read selector field name in Access APIs because the logical model is `key + version`.
- Rename all `DataSet*` symbols to `Dataset*`; keep field name `dataset_id`.
- Rename all `StorageNode` / `StorageRoute` metadata concepts to `PrimaryStoreNode` / `PrimaryStoreRoute`.
- Use `PrimaryStore*` consistently for internal primary store protocol types; do not mix bare `StoreKey` with `PrimaryStoreNode`.
- Delete `QueryView` and `QueryTime`.
- Delete `snapshot_time`; point/as-of queries are expressed with `TimeRange`.
- Delete `WriteMode`; write semantics are fixed: same key+version merges only provided columns and preserves absent old columns.
- Delete `FieldValue` and old `ErrorCode` aliases.
- Split view APIs:
  - `QueryTimeSeriesRows` is the TimeSeries + DuckDB-facing view query.
  - `SearchRecordRows` is the Record + Bleve-facing view query.
  - `RebuildTimeSeriesView` and `RebuildRecordView` rebuild view storage asynchronously.
- Record data can be exposed through View metadata just like TimeSeries data. View is the user-facing view query entry, not a DuckDB-only concept.
- Event messages must carry user-facing `TimeSeriesKey` / `RecordKey`, not `PrimaryStoreKey`; consumers must回读 Access, not primary target shards.

## File Responsibility Map

### Proto Contracts

- Create `modules/storage/proto/access.proto`: user-facing AccessService, TimeSeries/Record keys and rows, Access read/write requests and responses.
- Create `modules/storage/proto/store.proto`: internal PrimaryStoreService, PrimaryStoreKey/Row/Target and primary read/write requests.
- Create `modules/storage/proto/view.proto`: ViewService, TimeSeries DuckDB-view query, Record Bleve-backed search, rebuild requests and responses.
- Modify `modules/storage/proto/common.proto`: shared types, ColumnValue, TimeRange, VersionRange, SortOrder, DataKind, ErrorCode; remove WriteMode and FieldValue.
- Modify `modules/storage/proto/metadata.proto`: Dataset naming, PrimaryStore topology naming, View metadata.
- Modify `modules/storage/proto/message.proto`: TimeSeriesRowsChangedEvent and RecordRowsChangedEvent.
- Modify `modules/storage/proto/Makefile`: generate `common metadata access store view message`.

### Generated Code

- Remove old generated files under `modules/storage/proto/gen` for `data`, `primary`, and `query`.
- Generate new `access`, `store`, and `view` pb/trpc files.
- Keep generated package alias as `storagepb`.

### Storage Implementation

- Modify `modules/storage/internal/services/access`: implement generated `AccessService`, convert Access rows to PrimaryStore rows, publish split events, remove old WriteRows/ReadRows compatibility.
- Modify `modules/storage/internal/services/primary`: implement generated `PrimaryStoreService` over `PrimaryStoreRow` and `PrimaryStoreKey`.
- Modify `modules/storage/internal/services/search`: consume split events and support ViewService record search and rebuild.
- Modify `modules/storage/internal/services/view`: support ViewService time-series query using View metadata and DuckDB.
- Modify `modules/storage/internal/infra/device/pebble`: read/write `PrimaryStoreRow`.
- Modify `modules/storage/internal/infra/device/duckdb`: accept `TimeSeriesRow` or converted primary rows for TimeSeries view materialization.
- Modify `modules/storage/internal/infra/device/bleve`: index/search Record rows and keep optional TimeSeries search only if required by tests; final public API must be `SearchRecordRows`.
- Modify `modules/storage/internal/core/router`: use `PrimaryStoreRoute` and `PrimaryStoreNode`.
- Modify `modules/storage/internal/core/schema`: validate `TimeSeriesRow` and `RecordRow` against `DatasetColumn`.
- Modify `modules/storage/internal/core/metadata` and `modules/storage/internal/infra/metadata`: rename Dataset and PrimaryStore APIs.

### CLI / Config / Docs / Tests

- Modify `modules/cli`: clients and commands use AccessService, ViewService, Dataset, Record.
- Modify `modules/storage/cmd/moox-storage`: register AccessService, PrimaryStoreService, ViewService, MetadataService.
- Modify `modules/storage/cmd/moox-storage-bench`: record benchmark naming and ViewService time-series query.
- Modify `modules/storage/config/*.yaml` and `modules/storage/tests/testdata/*.yaml`: service names and metadata seed names.
- Modify docs under `docs/` and `modules/storage/docs/`: Access / PrimaryStore / View terminology, Dataset spelling, Record model.
- Modify tests under `modules/storage/internal/**` and `modules/storage/tests/e2e/**`.

## Task 1: Baseline and Guard Rails

**Files:**
- Read: `modules/storage/proto/*.proto`
- Read: `modules/storage/proto/Makefile`
- Read: `modules/storage/internal/services/access/*.go`
- Read: `modules/storage/internal/services/primary/*.go`
- Read: `modules/storage/internal/services/search/*.go`
- Read: `modules/storage/internal/services/view/*.go`

- [ ] Step 1: Confirm the worktree is clean.

Run:

```bash
git status --short --branch
```

Expected: only `## main...origin/main` before implementation starts.

- [ ] Step 2: Run baseline storage tests.

Run from `modules/storage`:

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./...
```

Expected before refactor: PASS.

- [ ] Step 3: Run baseline CLI tests.

Run from `modules/cli`:

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./...
```

Expected before refactor: PASS.

- [ ] Step 4: Capture old naming footprint for later residual checks.

Run from repo root:

```bash
rg -n "DataSet|DataRow|DataKey|DataScope|ReadMode|WriteRows|ReadRows|ObjectKey|ObjectRow|object_id|StorageNode|StorageRoute|PrimaryTarget|QueryView|QueryService|DataService|WriteMode|FieldValue|snapshot_time" modules/storage modules/cli docs -g '!modules/storage/proto/gen/*.pb.go' -g '!modules/storage/proto/gen/*.trpc.go'
```

Expected before refactor: many matches. Save no file; use this as a mental before-state only.

## Task 2: Rewrite Proto Contracts

**Files:**
- Create: `modules/storage/proto/access.proto`
- Create: `modules/storage/proto/store.proto`
- Create: `modules/storage/proto/view.proto`
- Modify: `modules/storage/proto/common.proto`
- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/message.proto`
- Modify: `modules/storage/proto/Makefile`
- Delete: `modules/storage/proto/data.proto`
- Delete: `modules/storage/proto/primary.proto`
- Delete: `modules/storage/proto/query.proto`

- [ ] Step 1: Create `access.proto` with only user-facing TimeSeries and Record APIs.

Required shape:

```proto
syntax = "proto3";

package trpc.storage.access;

option go_package = "github.com/mooyang-code/moox/modules/storage/proto/gen;storagepb";

import "common.proto";

message TimeSeriesKey {
  string space_id = 1;
  string dataset_id = 2;
  string subject_id = 3;
  string freq = 4;
  map<string, string> dimensions = 5;
  string data_time = 6;
}

message TimeSeriesRow {
  TimeSeriesKey key = 1;
  repeated common.ColumnValue columns = 2;
  map<string, string> attributes = 3;
}

message RecordKey {
  string space_id = 1;
  string dataset_id = 2;
  string record_id = 3;
  string version = 4;
}

message RecordRow {
  RecordKey key = 1;
  repeated common.ColumnValue columns = 2;
  map<string, string> attributes = 3;
}

message WriteTimeSeriesRowsReq {
  common.AuthInfo auth_info = 1;
  repeated TimeSeriesRow rows = 2;
}

message WriteTimeSeriesRowsRsp {
  common.RetInfo ret_info = 1;
}

message ReadTimeSeriesRowsReq {
  common.AuthInfo auth_info = 1;
  repeated TimeSeriesKey keys = 2;
  common.TimeRange time_range = 3;
  common.SortOrder order = 4;
  repeated string column_names = 5;
  common.Page page = 6;
}

message ReadTimeSeriesRowsRsp {
  common.RetInfo ret_info = 1;
  repeated TimeSeriesRow rows = 2;
  common.PageResult page_result = 3;
}

message WriteRecordRowsReq {
  common.AuthInfo auth_info = 1;
  repeated RecordRow rows = 2;
}

message WriteRecordRowsRsp {
  common.RetInfo ret_info = 1;
}

message ReadRecordRowsReq {
  common.AuthInfo auth_info = 1;
  repeated RecordKey keys = 2;
  common.VersionRange version_range = 3;
  common.SortOrder order = 4;
  repeated string column_names = 5;
  common.Page page = 6;
}

message ReadRecordRowsRsp {
  common.RetInfo ret_info = 1;
  repeated RecordRow rows = 2;
  common.PageResult page_result = 3;
}

service AccessService {
  rpc WriteTimeSeriesRows(WriteTimeSeriesRowsReq) returns (WriteTimeSeriesRowsRsp);
  rpc ReadTimeSeriesRows(ReadTimeSeriesRowsReq) returns (ReadTimeSeriesRowsRsp);
  rpc WriteRecordRows(WriteRecordRowsReq) returns (WriteRecordRowsRsp);
  rpc ReadRecordRows(ReadRecordRowsReq) returns (ReadRecordRowsRsp);
}
```

The actual file must include comments explaining:
- TimeSeries requires fixed `subject_id + freq`.
- Record covers non-fixed subject/freq data, even if it has versions.
- Read `keys` keep the key+version model; range reads may leave `data_time` / `version` empty and use range fields.
- Writes are column-level merge/patch; no `write_mode`.

- [ ] Step 2: Create `store.proto` for internal primary store protocol.

Required shape:

```proto
syntax = "proto3";

package trpc.storage.store;

option go_package = "github.com/mooyang-code/moox/modules/storage/proto/gen;storagepb";

import "common.proto";

message PrimaryStoreKey {
  string space_id = 1;
  string dataset_id = 2;
  string key = 3;
  string version = 4;
}

message PrimaryStoreRow {
  PrimaryStoreKey key = 1;
  repeated common.ColumnValue columns = 2;
  map<string, string> attributes = 3;
}

message PrimaryStoreTarget {
  string space_id = 1;
  string node_id = 2;
  string device_id = 3;
  string engine = 4;
  string dataset_id = 5;
  string device_table = 6;
  string endpoint = 7;
}

message WritePrimaryRowsReq {
  common.AuthInfo auth_info = 1;
  PrimaryStoreTarget target = 2;
  repeated PrimaryStoreRow rows = 3;
}

message WritePrimaryRowsRsp {
  common.RetInfo ret_info = 1;
}

message ReadPrimaryRowsReq {
  common.AuthInfo auth_info = 1;
  PrimaryStoreTarget target = 2;
  repeated PrimaryStoreKey keys = 3;
  common.VersionRange version_range = 4;
  common.SortOrder order = 5;
  repeated string column_names = 6;
  common.Page page = 7;
}

message ReadPrimaryRowsRsp {
  common.RetInfo ret_info = 1;
  repeated PrimaryStoreRow rows = 2;
  common.PageResult page_result = 3;
}

service PrimaryStoreService {
  rpc WritePrimaryRows(WritePrimaryRowsReq) returns (WritePrimaryRowsRsp);
  rpc ReadPrimaryRows(ReadPrimaryRowsReq) returns (ReadPrimaryRowsRsp);
}
```

- [ ] Step 3: Create `view.proto` for view read models.

Required public RPCs:

```proto
service ViewService {
  rpc QueryTimeSeriesRows(QueryTimeSeriesRowsReq) returns (QueryTimeSeriesRowsRsp);
  rpc SearchRecordRows(SearchRecordRowsReq) returns (SearchRecordRowsRsp);
  rpc RebuildTimeSeriesView(RebuildTimeSeriesViewReq) returns (RebuildTimeSeriesViewRsp);
  rpc RebuildRecordView(RebuildRecordViewReq) returns (RebuildRecordViewRsp);
}
```

Required behavior in comments:
- `QueryTimeSeriesRows` queries TimeSeries view data through View metadata and DuckDB by default.
- `SearchRecordRows` searches Record view data through View metadata and Bleve by default.
- `Rebuild*View` runs asynchronously and must not block normal writes.

- [ ] Step 4: Modify `common.proto`.

Required changes:
- Move `ColumnValue` into `common.proto`.
- Remove `FieldValue`.
- Remove `WriteMode`.
- Remove old `ErrorCode` aliases: `WORKSPACE_NOT_FOUND`, `INSTRUMENT_NOT_FOUND`, `FACTOR_INSTANCE_NOT_FOUND`, `DATA_VIEW_NOT_READY`, `DATA_VIEW_COLUMN_NOT_FOUND`.
- Rename `DATA_KIND_OBJECT` to `DATA_KIND_RECORD`.
- Keep `dataset_id` field names unchanged wherever applicable.

- [ ] Step 5: Modify `metadata.proto`.

Required renames:

```text
DataSet -> Dataset
DataSetColumn -> DatasetColumn
DataSetSubject -> DatasetSubject
CreateDataSet -> CreateDataset
UpdateDataSet -> UpdateDataset
GetDataSet -> GetDataset
ListDataSets -> ListDatasets
BindDataSetSubject -> BindDatasetSubject
ListDataSetSubjects -> ListDatasetSubjects
UpsertDataSetColumn -> UpsertDatasetColumn
ListDataSetColumns -> ListDatasetColumns
StorageNode -> PrimaryStoreNode
StorageRoute -> PrimaryStoreRoute
CreateStorageNode -> CreatePrimaryStoreNode
UpdateStorageNode -> UpdatePrimaryStoreNode
GetStorageNode -> GetPrimaryStoreNode
ListStorageNodes -> ListPrimaryStoreNodes
CreateStorageRoute -> CreatePrimaryStoreRoute
UpdateStorageRoute -> UpdatePrimaryStoreRoute
GetStorageRoute -> GetPrimaryStoreRoute
ListStorageRoutes -> ListPrimaryStoreRoutes
```

- [ ] Step 6: Modify `message.proto`.

Required shape:

```proto
syntax = "proto3";

package trpc.storage.message;

option go_package = "github.com/mooyang-code/moox/modules/storage/proto/gen;storagepb";

import "access.proto";

message TimeSeriesRowsChangedEvent {
  string event_id = 1;
  string event_time = 2;
  repeated access.TimeSeriesKey keys = 3;
  map<string, string> attributes = 4;
}

message RecordRowsChangedEvent {
  string event_id = 1;
  string event_time = 2;
  repeated access.RecordKey keys = 3;
  map<string, string> attributes = 4;
}
```

- [ ] Step 7: Modify `proto/Makefile`.

Required `PROTO_FILES`:

```makefile
PROTO_FILES := common.proto metadata.proto access.proto store.proto view.proto message.proto
```

- [ ] Step 8: Delete the old proto files after replacements are complete.

Run:

```bash
git rm modules/storage/proto/data.proto modules/storage/proto/primary.proto modules/storage/proto/query.proto
```

Expected: the old files are staged for deletion.

## Task 3: Regenerate Protobuf Code

**Files:**
- Modify generated files under `modules/storage/proto/gen`

- [ ] Step 1: Remove old generated files.

Run:

```bash
git rm modules/storage/proto/gen/data.pb.go modules/storage/proto/gen/data.trpc.go modules/storage/proto/gen/primary.pb.go modules/storage/proto/gen/primary.trpc.go modules/storage/proto/gen/query.pb.go modules/storage/proto/gen/query.trpc.go
```

- [ ] Step 2: Generate new files.

Run from `modules/storage`:

```bash
make proto
```

Expected generated files:

```text
modules/storage/proto/gen/access.pb.go
modules/storage/proto/gen/access.trpc.go
modules/storage/proto/gen/store.pb.go
modules/storage/proto/gen/store.trpc.go
modules/storage/proto/gen/view.pb.go
modules/storage/proto/gen/view.trpc.go
```

- [ ] Step 3: Verify generated service names.

Run:

```bash
rg -n "AccessService|PrimaryStoreService|ViewService|DataService|QueryService" modules/storage/proto/gen
```

Expected:
- `AccessService`, `PrimaryStoreService`, and `ViewService` exist.
- `DataService` and `QueryService` do not exist.

## Task 4: Update Core Interfaces and Converters

**Files:**
- Modify: `modules/storage/internal/core/metadata/store.go`
- Modify: `modules/storage/internal/core/router/resolver.go`
- Modify: `modules/storage/internal/core/schema/validator.go`
- Modify: `modules/storage/internal/services/access/key_adapter.go`
- Modify: `modules/storage/internal/infra/device/factkey/key.go`

- [ ] Step 1: Rename metadata interfaces to Dataset and PrimaryStore.

Required interface method names:

```go
GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
ListDatasets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error)
ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string) ([]*pb.DatasetSubject, error)
ListDatasetSubjectsPage(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error)
ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
GetPrimaryStoreNode(ctx context.Context, nodeID string) (*pb.PrimaryStoreNode, error)
ListPrimaryStoreRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error)
```

- [ ] Step 2: Replace old DataKey conversion helpers with PrimaryStore conversions.

Required helper behavior:
- TimeSeries:
  - `PrimaryStoreKey.key = BuildTimeSeriesDataKey(subject_id, freq, dimensions)`
  - `PrimaryStoreKey.version = data_time`
- Record:
  - `PrimaryStoreKey.key = BuildRecordDataKey(record_id)`
  - `PrimaryStoreKey.version = version`, with current default version behavior preserved.

- [ ] Step 3: Rename factkey object helper.

Required rename:

```text
BuildObjectDataKey -> BuildRecordDataKey
```

Required error text:

```text
record_id is required
```

- [ ] Step 4: Update schema validator.

Required public validation methods:

```go
ValidateWriteTimeSeriesRows(ctx context.Context, rows []*pb.TimeSeriesRow) error
ValidateWriteRecordRows(ctx context.Context, rows []*pb.RecordRow) error
```

Required behavior:
- No write path binds `DatasetSubject`.
- No write path requires subject already bound to dataset.
- Both methods validate dataset exists and each provided column exists in `DatasetColumn`.
- Type validation remains as today.

## Task 5: Update Primary Store Device Layer

**Files:**
- Modify: `modules/storage/internal/infra/device/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/key.go`
- Modify tests under `modules/storage/internal/infra/device/pebble`

- [ ] Step 1: Change primary device interface.

Required shape:

```go
WriteRows(ctx context.Context, rows []*pb.PrimaryStoreRow) error
ReadRows(ctx context.Context, keys []*pb.PrimaryStoreKey, versionRange *pb.VersionRange, order pb.SortOrder, columnNames []string, page *pb.Page) ([]*pb.PrimaryStoreRow, *pb.PageResult, error)
```

- [ ] Step 2: Update Pebble key encoding to use `PrimaryStoreKey`.

Required logical shape:

```text
t|space|dataset|key|version
r|space|dataset|key|version
```

The actual prefix may remain implementation-private, but TimeSeries and Record key spaces must remain distinguishable.

- [ ] Step 3: Preserve merge semantics.

Required behavior:
- Writing the same key+version merges provided columns.
- Old columns not included in the new write are retained.
- There is no overwrite/delete range mode.

- [ ] Step 4: Update Pebble tests.

Replace old tests that call `pb.DataRow`, `pb.DataKey`, `pb.DataScope`, `pb.WriteMode`, or `pb.ReadMode` with `pb.PrimaryStoreRow` and `pb.PrimaryStoreKey`.

## Task 6: Update Access Service

**Files:**
- Modify: `modules/storage/internal/services/access/service.go`
- Modify: `modules/storage/internal/services/access/data.go`
- Modify: `modules/storage/internal/services/access/query.go`
- Modify: `modules/storage/internal/services/access/archive.go`
- Modify: `modules/storage/internal/services/access/factreader.go`
- Modify: `modules/storage/internal/services/access/options.go`
- Modify tests under `modules/storage/internal/services/access`

- [ ] Step 1: Implement generated `pb.AccessService` instead of `pb.DataService`.

Required RPCs:

```go
WriteTimeSeriesRows(ctx context.Context, req *pb.WriteTimeSeriesRowsReq) (*pb.WriteTimeSeriesRowsRsp, error)
ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error)
WriteRecordRows(ctx context.Context, req *pb.WriteRecordRowsReq) (*pb.WriteRecordRowsRsp, error)
ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error)
```

- [ ] Step 2: Delete old `WriteRows` and `ReadRows` handlers.

Expected after deletion:

```bash
rg -n "func .*WriteRows|func .*ReadRows|WriteRowsReq|ReadRowsReq" modules/storage/internal/services/access
```

returns no Access-service compatibility handlers.

- [ ] Step 3: Convert writes to `PrimaryStoreRow`.

Required flow:

```text
Access row -> validate DatasetColumn -> resolve PrimaryStoreRoute -> PrimaryStoreRow -> PrimaryStoreService.WritePrimaryRows -> publish TimeSeries/Record event
```

- [ ] Step 4: Convert reads from `PrimaryStoreRow` back to user rows.

Required behavior:
- TimeSeries rows return `TimeSeriesRow` with `TimeSeriesKey`.
- Record rows return `RecordRow` with `RecordKey`.
- Column projection behavior remains unchanged.

- [ ] Step 5: Split event publishing.

Required event APIs:

```go
PublishTimeSeriesRowsChanged(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
PublishRecordRowsChanged(ctx context.Context, event *pb.RecordRowsChangedEvent) error
```

## Task 7: Update Primary Store Service

**Files:**
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/internal/services/primary/local.go`
- Modify: `modules/storage/internal/services/primary/remote.go`
- Modify tests under `modules/storage/internal/services/primary`

- [ ] Step 1: Implement generated `pb.PrimaryStoreService` from `store.proto`.

Required request/response types:

```go
*pb.WritePrimaryRowsReq
*pb.WritePrimaryRowsRsp
*pb.ReadPrimaryRowsReq
*pb.ReadPrimaryRowsRsp
```

- [ ] Step 2: Replace `PrimaryTarget` with `PrimaryStoreTarget`.

Required rename:

```text
PrimaryTarget -> PrimaryStoreTarget
```

- [ ] Step 3: Remove old read mode handling.

Expected after refactor:

```bash
rg -n "ReadMode|snapshot_time|object_id" modules/storage/internal/services/primary modules/storage/internal/infra/device/pebble
```

returns no old read-mode implementation.

## Task 8: Update Event Bus

**Files:**
- Modify: `modules/storage/internal/core/eventbus/bus.go`
- Modify: `modules/storage/internal/core/eventbus/bus_test.go`
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`
- Modify: `modules/storage/internal/infra/transport/message.go`
- Modify: `modules/storage/internal/infra/transport/nats/producer.go`
- Modify tests under `modules/storage/internal/infra/eventbus` and `modules/storage/internal/infra/transport/nats`

- [ ] Step 1: Split event interfaces into TimeSeries and Record variants.

Required interface shape:

```go
type TimeSeriesRowsChangedHandler func(ctx context.Context, event *pb.TimeSeriesRowsChangedEvent) error
type RecordRowsChangedHandler func(ctx context.Context, event *pb.RecordRowsChangedEvent) error
```

- [ ] Step 2: Use event subject names with a unified prefix.

Required subject pattern:

```text
moox.storage.time_series.rows_changed.v1
moox.storage.record.rows_changed.v1
```

- [ ] Step 3: Ensure consumers回读 AccessService.

Search consumers must not call PrimaryStoreService directly and must not inspect `PrimaryStoreTarget`.

## Task 9: Update View Service and View Stores

**Files:**
- Modify: `modules/storage/internal/services/search/service.go`
- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/internal/services/view/schedule.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Modify: `modules/storage/internal/infra/device/bleve/index.go`
- Modify tests under `modules/storage/internal/services/view`, `modules/storage/internal/infra/device/duckdb`, and `modules/storage/internal/infra/device/bleve`

- [ ] Step 1: Implement generated `pb.ViewService`.

Required RPCs:

```go
QueryTimeSeriesRows(ctx context.Context, req *pb.QueryTimeSeriesRowsReq) (*pb.QueryTimeSeriesRowsRsp, error)
SearchRecordRows(ctx context.Context, req *pb.SearchRecordRowsReq) (*pb.SearchRecordRowsRsp, error)
RebuildTimeSeriesView(ctx context.Context, req *pb.RebuildTimeSeriesViewReq) (*pb.RebuildTimeSeriesViewRsp, error)
RebuildRecordView(ctx context.Context, req *pb.RebuildRecordViewReq) (*pb.RebuildRecordViewRsp, error)
```

- [ ] Step 2: Delete `QueryView` RPC implementation.

Expected after deletion:

```bash
rg -n "QueryView|QueryTime|snapshot_time" modules/storage/internal/services modules/storage/proto
```

returns no live API references. Historical docs may be updated in Task 14.

- [ ] Step 3: Make TimeSeries view query View-based.

Required request behavior:
- Request carries `space_id`, `view_id`, `keys`, `time_range`, `column_names`, `filters`, `sorts`, and `page`.
- Implementation loads View metadata.
- Default engine is DuckDB.
- Returned rows are `TimeSeriesRow`.

- [ ] Step 4: Make Record View search View-based.

Required request behavior:
- Request carries `space_id`, `view_id`, optional `keys`, optional `text_query`, `version_range`, filters, sorts, column projection, and page.
- Implementation loads View metadata.
- Default engine is Bleve.
- Returned rows are `RecordRow`.

- [ ] Step 5: Rebuild methods are asynchronous.

Required behavior:
- `RebuildTimeSeriesView` returns a rebuild ID.
- `RebuildRecordView` returns a rebuild ID.
- Neither RPC blocks normal Access writes.

## Task 10: Update Metadata Storage and Schema

**Files:**
- Modify: `modules/storage/schema/storage_metadata.sql`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud.go`
- Modify: `modules/storage/internal/infra/metadata/cache/store.go`
- Modify tests under `modules/storage/internal/infra/metadata`
- Modify: `modules/storage/config/metadata.seed.yaml`

- [ ] Step 1: Rename API methods in sqlite/cache stores.

Required method names:

```text
CreateDataset / UpdateDataset / GetDataset / ListDatasets
BindDatasetSubject / ListDatasetSubjects
UpsertDatasetColumn / ListDatasetColumns
CreatePrimaryStoreNode / ListPrimaryStoreNodes
CreatePrimaryStoreRoute / ListPrimaryStoreRoutes
```

- [ ] Step 2: Decide SQL physical table names.

Because the project is not online and no compatibility is required, prefer physical table rename in schema:

```text
t_datasets
t_dataset_subjects
t_dataset_columns
t_primary_store_nodes
t_primary_store_routes
```

Execution note: the final implementation uses `t_primary_store_nodes` and `t_primary_store_routes` for SQLite physical tables, matching the PrimaryStore protocol and metadata names.

- [ ] Step 3: Update seed data.

Required changes:
- legacy non-time-series data-kind naming becomes `data_kind: record`.
- `DataSet` textual comments become `Dataset`.
- `StorageRoute` seed entries become `PrimaryStoreRoute`.

## Task 11: Update Service Bootstrap and Config

**Files:**
- Modify: `modules/storage/cmd/moox-storage/main.go`
- Modify: `modules/storage/cmd/moox-storage/main_test.go`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/tests/testdata/trpc_go.e2e.yaml`
- Modify: `modules/storage/tests/testdata/storage.e2e.yaml`

- [ ] Step 1: Register new services.

Required service names:

```text
trpc.storage.access.AccessService
trpc.storage.store.PrimaryStoreService
trpc.storage.view.ViewService
trpc.storage.metadata.MetadataService
```

- [ ] Step 2: Remove old service names.

Expected:

```bash
rg -n "trpc.storage.data|trpc.storage.query|trpc.storage.primary" modules/storage/config modules/storage/tests/testdata modules/storage/cmd/moox-storage
```

returns no old service registrations.

## Task 12: Update CLI and Bench Tools

**Files:**
- Modify: `modules/cli/cmd/*.go`
- Modify: `modules/cli/README.md`
- Modify: `modules/storage/cmd/moox-storage-bench/main.go`
- Modify: `modules/storage/cmd/moox-storage-bench/main_test.go`
- Modify: `modules/storage/internal/bench/*.go`

- [ ] Step 1: Update CLI service clients.

Required changes:
- DataService client -> AccessService client.
- QueryService client -> ViewService client.
- Dataset command names and output use `Dataset`, not `DataSet`.
- Import command writes TimeSeries or Record rows.
- CSV import option for non-time-series uses `--record-id-column` instead of object naming.

- [ ] Step 2: Update benchmark naming.

Required output phrases:

```text
非时序记录写入性能
WriteRecordRows
Record
View TimeSeries 查询性能
```

Forbidden output phrases:

```text
Object
WriteObjectRows
QueryView
```

## Task 13: Update Tests

**Files:**
- Modify all tests under:
  - `modules/storage/internal/**`
  - `modules/storage/tests/e2e/**`
  - `modules/cli/cmd/**`

- [ ] Step 1: Update tests to compile against new generated API.

Required replacements:

```text
pb.DataSet -> pb.Dataset
pb.DataSetColumn -> pb.DatasetColumn
pb.DataSetSubject -> pb.DatasetSubject
pb.ObjectKey -> pb.RecordKey
pb.ObjectRow -> pb.RecordRow
pb.DataRow -> pb.PrimaryStoreRow or pb.TimeSeriesRow/pb.RecordRow depending on layer
pb.DataKey -> pb.PrimaryStoreKey
pb.DataService -> pb.AccessService
pb.QueryService -> pb.ViewService
```

- [ ] Step 2: Add or update behavior tests.

Required test coverage:
- TimeSeries write does not require DatasetSubject binding.
- Record write does not bind DatasetSubject.
- Rewriting same key+version merges provided columns.
- TimeSeries view query uses View and returns TimeSeries rows.
- Record View search uses View and returns Record rows.
- Search consumer回读 AccessService, not PrimaryStoreService.

- [ ] Step 3: Update e2e tests.

Required e2e coverage:
- Metadata creates Dataset, DatasetColumn, View, PrimaryStoreNode, PrimaryStoreRoute.
- Access writes TimeSeries rows to PrimaryStore.
- Access writes Record rows to PrimaryStore.
- TimeSeries data asynchronously materializes to DuckDB-view storage.
- Record data asynchronously indexes to Bleve-view storage.
- Access reads TimeSeries and Record source-of-truth rows.
- ViewService queries TimeSeries rows.
- ViewService searches Record rows.

## Task 14: Update Documentation

**Files:**
- Modify: `docs/pb-protocol-redesign.md`
- Modify: `docs/storage-concepts-and-design-intent.md`
- Modify: `docs/storage-target-architecture-and-metadata.md`
- Modify: `docs/quant-financial-data-concepts.md`
- Modify: `modules/storage/README.md`
- Modify: `modules/storage/docs/architecture.md`
- Modify: `modules/storage/tests/README.md`
- Modify: `modules/storage/BUILD.md`
- Modify: `modules/storage/DEPLOY.md`

- [ ] Step 1: Rewrite protocol terminology.

Required terminology:

```text
AccessService
PrimaryStoreService
ViewService
Dataset
DatasetColumn
DatasetSubject
PrimaryStoreNode
PrimaryStoreRoute
TimeSeries
Record
PrimaryStoreKey
PrimaryStoreRow
PrimaryStoreTarget
```

- [ ] Step 2: Remove obsolete concepts from docs.

Forbidden live-doc terminology:

```text
DataService
QueryService
DataSet
DataRow
DataKey
ObjectKey
ObjectRow
object_id
StorageNode
StorageRoute
QueryView
WriteMode
snapshot_time
FieldValue
```

Historical implementation plan docs under `docs/superpowers/plans/` may retain old terms as archived history except this plan.

## Task 15: Residual Scans

**Files:**
- Inspect whole repo.

- [ ] Step 1: Scan live code and current docs for forbidden names.

Run:

```bash
rg -n "DataSet|DataRow|DataKey|DataScope|ReadMode|WriteRows|ReadRows|ObjectKey|ObjectRow|object_id|StorageNode|StorageRoute|PrimaryTarget|QueryView|QueryTime|QueryService|DataService|WriteMode|FieldValue|snapshot_time" modules/storage modules/cli docs -g '!docs/superpowers/plans/2026-06-13-*' -g '!docs/superpowers/plans/2026-06-15-*' -g '!docs/superpowers/plans/2026-06-17-*' -g '!modules/storage/proto/gen/*.pb.go' -g '!modules/storage/proto/gen/*.trpc.go'
```

Expected:
- No matches in live source, proto, config, tests, or current docs.
- Matches inside this plan are acceptable until the plan is archived or updated; after implementation, this plan can retain old names as requirements context.

- [ ] Step 2: Scan generated code for old services.

Run:

```bash
rg -n "DataService|QueryService|DataRow|DataKey|ObjectRow|ObjectKey|WriteMode|FieldValue|snapshot_time" modules/storage/proto/gen
```

Expected: no matches.

- [ ] Step 3: Scan for final service names.

Run:

```bash
rg -n "AccessService|PrimaryStoreService|ViewService|MetadataService" modules/storage modules/cli
```

Expected: all final services appear in generated code, registration, clients, and tests.

## Task 16: Final Verification

**Files:**
- No edits unless verification reveals failures.

- [ ] Step 1: Run storage unit/integration tests.

Run from `modules/storage`:

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./...
```

Expected: PASS.

- [ ] Step 2: Run CLI tests.

Run from `modules/cli`:

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 ./...
```

Expected: PASS.

- [ ] Step 3: Run storage e2e tests.

Run from `modules/storage`:

```bash
env GOCACHE=/tmp/moox-gocache CGO_ENABLED=1 go test -count=1 -tags e2e -timeout 600s ./tests/e2e/...
```

Expected: PASS.

- [ ] Step 4: Run proto generation idempotency check.

Run from `modules/storage`:

```bash
make proto
git diff -- modules/storage/proto modules/storage/proto/gen
```

Expected: no diff after generation.

## Task 17: Independent Agent Review

**Files:**
- Review all modified files and command outputs.

- [x] Step 1: Start an independent review agent after implementation and verification.

Review prompt:

```text
请独立 review 当前 worktree 是否完整完成 storage proto 重构目标：
1. proto 文件已重组为 common/access/metadata/store/view/message。
2. 用户侧只保留 TimeSeries 和 Record 两套 Access 入口，无 WriteRows/ReadRows/DataKey/DataRow/ReadMode。
3. Object 已改 Record，DataSet 已改 Dataset，StorageNode/Route 已改 PrimaryStoreNode/Route。
4. Store 协议统一使用 PrimaryStoreKey/PrimaryStoreRow/PrimaryStoreTarget。
5. View 协议无 QueryView/QueryTime/snapshot_time，提供 QueryTimeSeriesRows、SearchRecordRows、RebuildTimeSeriesView、RebuildRecordView。
6. WriteMode、FieldValue 和旧 ErrorCode alias 已删除。
7. 事件拆为 TimeSeriesRowsChangedEvent 和 RecordRowsChangedEvent，消费者通过 AccessService 回读。
8. CLI、配置、文档、测试已同步。
9. `go test -count=1 ./...`、CLI 测试、e2e 测试和残留扫描结果是否足以证明完成。
请按 P0/P1/P2 输出缺陷和证据，不要修改代码。
```

- [x] Step 2: Address all P0/P1 review findings.

Required behavior:
- P0/P1 must be fixed before final completion.
- P2 can be documented if not necessary for this goal.

- [x] Step 3: Re-run final verification after fixes.

Run the same commands from Task 16.

Execution note (2026-06-20): Independent review agent `019ee4d6-1b11-7240-aaa9-c4fb4388f854` found stale docs/config naming. The P1 docs issue and P2 seed/comment/checklist issues were addressed by updating root docs, seed YAML keys, CLI import resource names, SQL schema comments, and this checklist. Final verification was rerun after those fixes.

Final review note (2026-06-20): Later independent review found remaining legacy non-time-series data-kind text, historical benchmark labels/wording, and an outdated event subject example in this plan. These were updated to `record`, `record_write`, visible record wording, and `moox.storage.time_series.rows_changed.v1` / `moox.storage.record.rows_changed.v1`.

## Task 18: Optional Commit and Push

Only when the user explicitly asks to package and publish this work after all verification passes and review feedback is resolved.

**Files:**
- All modified files.

- [ ] Step 1: Inspect final diff.

Run:

```bash
git status --short --branch
git diff --stat
```

- [ ] Step 2: Commit.

Recommended commit message:

```bash
git commit -m "refactor(storage): reorganize access store view protocols" -m "Replace legacy data/query/primary protobuf contracts with AccessService, PrimaryStoreService, and ViewService. Rename Object to Record, DataSet to Dataset, and Storage routes to PrimaryStore routes while removing legacy WriteRows/ReadRows, WriteMode, FieldValue, QueryView, and snapshot_time concepts." -m "Tests: storage go test -count=1 ./...; cli go test -count=1 ./...; storage go test -count=1 -tags e2e -timeout 600s ./tests/e2e/..."
```

- [ ] Step 3: Push.

Run:

```bash
git push origin main
```

Expected: push succeeds and `git status --short --branch` shows `## main...origin/main`.
