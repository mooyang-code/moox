# Storage View-Versioned Read Models Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework storage read models so TimeSeries views are versioned DuckDB tables, Record views are versioned Bleve indexes, and all fact data follows explicit `key + version` semantics.

**Architecture:** Access remains the only user-facing fact write/read entry and PrimaryStore remains the source of truth. View metadata owns `view_version` and active/building result pointers; background builders rebuild a new version from PrimaryStore and atomically cut reads over after completion. Event consumers, not Access writes, double-write active and building view results during rebuild.

**Tech Stack:** Go, tRPC-Go, protobuf generated code, SQLite metadata, Pebble PrimaryStore, DuckDB TimeSeries view store, Bleve Record view index, storage event bus/NATS abstraction, Go unit/e2e tests.

---

## Confirmed Decisions

- Use `view_version`, not `schema_version`, for the version of a user-defined View.
- Increment `view_version` when a View is created, when a View field is added, or when the View shape changes, including primary dataset, field type, field origin, filter, engine, query window, or other build-affecting definition changes.
- Use option 2 from the discussion: full rebuild from PrimaryStore plus event-consumer double write to active and building results.
- Do not rebuild a new View result from the old View result. DuckDB and Bleve rebuilds both scan PrimaryStore.
- Access writes only PrimaryStore and publishes events. Double write happens only inside view/search consumers.
- All fact data has a version:
  - TimeSeries uses `TimeSeriesKey.data_time` as the row version.
  - Record uses `RecordKey.version` as the row version.
  - Record writes with an empty version receive a server-generated UTC timestamp version.
- Writes are column patch/upsert semantics:
  - absent column: keep old value;
  - non-NULL column: overwrite old value;
  - NULL column: do not overwrite an existing non-NULL value.
- Remove metadata fields such as `text_indexed` or `indexed`; View columns decide what is materialized or indexed.
- DuckDB is the versioned store for TimeSeries views.
- Bleve is the versioned store for Record views.
- `RebuildTimeSeriesView` and `RebuildRecordView` rebuild the full current View version asynchronously and do not require callers to provide historical keys.

## Data Model Invariants

Fact storage is normalized internally as:

```text
space_id + dataset_id + data_kind + data_key + version
```

TimeSeries mapping:

```text
data_kind = TIME_SERIES
data_key  = subject_id | freq | dimhash
version   = data_time
```

Record mapping:

```text
data_kind = RECORD
data_key  = record_id
version   = user version or server UTC timestamp
```

View runtime state:

```text
view_version
active_view_version
active_result
building_view_version
building_result
build_status
build_error
build_started_at
build_finished_at
```

View rebuild state transitions:

```text
pending -> building -> active
pending -> building -> failed
failed  -> building -> active
```

Reads always use `active_result`. A failed or in-progress build must not change `active_result`.

## File Responsibility Map

Proto contracts:

- `modules/storage/proto/metadata.proto`: View version fields, ViewColumn semantics, DatasetColumn cleanup.
- `modules/storage/proto/access.proto`: Record default version contract and write response keys.
- `modules/storage/proto/store.proto`: internal PrimaryStore scan API.
- `modules/storage/proto/view.proto`: full-view rebuild requests for TimeSeries and Record views.
- `modules/storage/proto/message.proto`: event comments and key semantics.
- `modules/storage/proto/gen/*.go`: generated protobuf/tRPC files.

Metadata:

- `modules/storage/schema/metadata.sql`: SQLite table columns and indexes.
- `modules/storage/internal/infra/metadata/sqlite/crud.go`: version bump, build state updates, view lookup.
- `modules/storage/internal/core/metadata/store.go`: metadata interfaces.
- `modules/storage/internal/infra/metadata/cache/store.go`: cache snapshot structs and forwarding methods.
- `modules/storage/internal/bootstrap/metadata/seed.go`: seed loader fields.
- `modules/storage/config/metadata.seed.yaml`: example metadata.

PrimaryStore and Access:

- `modules/storage/internal/services/access/data.go`: default Record version, response keys, event keys.
- `modules/storage/internal/services/access/key_adapter.go`: RecordKey normalization and version conversion.
- `modules/storage/internal/infra/device/factkey/key.go`: version normalization helpers.
- `modules/storage/internal/infra/device/pebble/store.go`: merge semantics and scan support.
- `modules/storage/internal/infra/device/store.go`: device interface additions.
- `modules/storage/internal/services/primary/*.go`: PrimaryStore scan service/client path.

View stores and consumers:

- `modules/storage/internal/services/view/view_builder.go`: versioned rebuild state machine.
- `modules/storage/internal/services/view/schedule.go`: timer selection.
- `modules/storage/internal/infra/device/duckdb/view_store.go`: versioned TimeSeries tables and upsert.
- `modules/storage/internal/infra/device/duckdb/view_store_fallback.go`: fallback behavior.
- `modules/storage/internal/infra/device/bleve/index.go`: versioned Record view indexes.
- `modules/storage/internal/services/search/service.go`: Record view indexing service.
- `modules/storage/internal/services/access/query.go`: View query/rebuild APIs.
- `modules/storage/internal/services/access/service.go`: event consumer registration.
- `modules/storage/internal/core/eventbus/bus.go`: event subscription interfaces when needed.

CLI, tests, and docs:

- `modules/cli/cmd/metadata.go`: remove index flags from metadata import.
- `modules/cli/cmd/storage_import.go`: display server-generated Record versions.
- `modules/storage/tests/e2e/*.go`: end-to-end coverage.
- `modules/storage/tests/schema/storage_metadata_schema_test.go`: schema assertions.
- `modules/storage/README.md`: user-facing behavior.
- `modules/storage/docs/architecture.md`: architecture and data flow.
- `modules/storage/tests/README.md`: test matrix.

## Task 1: Update Proto Contracts

**Files:**

- Modify: `modules/storage/proto/metadata.proto`
- Modify: `modules/storage/proto/access.proto`
- Modify: `modules/storage/proto/store.proto`
- Modify: `modules/storage/proto/view.proto`
- Modify: `modules/storage/proto/message.proto`
- Regenerate: `modules/storage/proto/gen/*.go`

- [ ] Add View version/build fields to `metadata.View`:

```proto
uint64 view_version = 18;
uint64 active_view_version = 19;
uint64 building_view_version = 20;
string building_result = 21;
string build_error = 22;
string build_started_at = 23;
string build_finished_at = 24;
```

- [ ] Remove `DatasetColumn.text_indexed` from `metadata.DatasetColumn`.
- [ ] Remove `text_indexed_only` from `ListDatasetColumnsReq`.
- [ ] Add final keys to `WriteRecordRowsRsp`:

```proto
repeated RecordKey keys = 2;
```

- [ ] Update `RecordKey.version` comments to state that an empty version is assigned by Access using a UTC timestamp.
- [ ] Add PrimaryStore scan messages and RPC in `store.proto`, using `PrimaryStoreTarget`, `common.DataKind`, `common.VersionRange`, `common.Page`, and `common.PageResult`.
- [ ] Change `RebuildRecordViewReq` so it contains only `auth_info`, `space_id`, and `view_id`.
- [ ] Update rebuild comments to state both rebuild APIs run a full current-View rebuild from PrimaryStore.
- [ ] Run proto generation:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/modules/storage/proto
make
```

- [ ] Verify generated code compiles far enough for symbol checks:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -run '^$' ./modules/storage/proto/gen
```

## Task 2: Upgrade Metadata Schema and Store APIs

**Files:**

- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud.go`
- Modify: `modules/storage/internal/core/metadata/store.go`
- Modify: `modules/storage/internal/infra/metadata/cache/store.go`
- Modify: `modules/storage/internal/bootstrap/metadata/seed.go`
- Modify: `modules/storage/config/metadata.seed.yaml`

- [ ] Add `t_views` columns:

```sql
c_view_version INTEGER NOT NULL DEFAULT 1,
c_active_view_version INTEGER NOT NULL DEFAULT 0,
c_building_view_version INTEGER NOT NULL DEFAULT 0,
c_building_result TEXT NOT NULL DEFAULT '',
c_build_error TEXT NOT NULL DEFAULT '',
c_build_started_at TEXT NOT NULL DEFAULT '',
c_build_finished_at TEXT NOT NULL DEFAULT ''
```

- [ ] Add metadata indexes for scheduling:

```sql
CREATE INDEX IF NOT EXISTS idx_t_views_version_pending
ON t_views (c_space_id, c_status, c_view_version, c_active_view_version);
```

- [ ] Remove `c_text_indexed` and `idx_t_dataset_columns_text_indexed` from `t_dataset_columns`.
- [ ] Update SQLite scans and row mapping for View version/build fields.
- [ ] Update `CreateView` and `UpsertView`:
  - new View starts at `view_version=1`, `active_view_version=0`, `build_status=pending`;
  - View shape change increments `view_version` and sets `build_status=pending`;
  - no-op update does not increment.
- [ ] Update `UpsertViewColumn`:
  - add or shape-changing update increments parent `view_version`;
  - no-op upsert does not increment.
- [ ] Add metadata API methods:

```go
ListViewsNeedingBuild(ctx context.Context, spaceID string) ([]*pb.View, error)
BeginViewBuild(ctx context.Context, spaceID, viewID string, targetVersion uint64, resultName string) (*pb.View, error)
CompleteViewBuild(ctx context.Context, spaceID, viewID string, targetVersion uint64, resultName string) error
FailViewBuild(ctx context.Context, spaceID, viewID string, targetVersion uint64, resultName string, buildErr error) error
ListViewsByDataset(ctx context.Context, spaceID, datasetID string) ([]*pb.View, error)
```

- [ ] Add unit tests for View version bump and no-op behavior.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/infra/metadata/...
go test -count=1 ./modules/storage/internal/infra/metadata/cache ./modules/storage/internal/bootstrap/metadata
```

## Task 3: Enforce Fact Version and NULL Merge Semantics

**Files:**

- Modify: `modules/storage/internal/services/access/data.go`
- Modify: `modules/storage/internal/services/access/key_adapter.go`
- Modify: `modules/storage/internal/infra/device/factkey/key.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store_test.go`
- Modify: `modules/storage/internal/services/access/service_test.go`

- [ ] Keep `TimeSeriesKey.data_time` required for TimeSeries writes.
- [ ] Generate a default Record version in Access when `RecordKey.version` is empty.
- [ ] Use UTC fixed-width timestamp format for default Record versions.
- [ ] Ensure multiple empty-version rows in one request get distinct, ordered versions.
- [ ] Return final Record keys from `WriteRecordRowsRsp.keys`.
- [ ] Publish Record change events with final keys, not caller-supplied empty versions.
- [ ] Implement merge-upsert so NULL does not overwrite an existing non-NULL value.
- [ ] Add tests:
  - empty Record version gets a response version;
  - returned version can be read back;
  - repeated writes to the same `record_id + version` merge columns;
  - NULL update does not erase a non-NULL value;
  - TimeSeries still rejects empty `data_time`.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/services/access ./modules/storage/internal/infra/device/pebble ./modules/storage/internal/infra/device/factkey
```

## Task 4: Add PrimaryStore Full Scan

**Files:**

- Modify: `modules/storage/proto/store.proto`
- Modify: `modules/storage/internal/infra/device/store.go`
- Modify: `modules/storage/internal/infra/device/pebble/store.go`
- Modify: `modules/storage/internal/services/primary/service.go`
- Modify: `modules/storage/internal/services/primary/local.go`
- Modify: `modules/storage/internal/services/primary/client.go`
- Modify: `modules/storage/internal/services/primary/remote.go`
- Modify: `modules/storage/internal/services/primary/local_test.go`

- [ ] Add internal `ScanPrimaryRows` request/response and service method.
- [ ] Implement Pebble prefix scans for:

```text
t|space|dataset|
r|space|dataset|
```

- [ ] Support `VersionRange`, `SortOrder`, `Page`, and cursor pagination.
- [ ] Keep scan internal to PrimaryStore/View rebuild; do not expose it on user-facing Access APIs.
- [ ] Add tests for TimeSeries prefix scan and Record prefix scan.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/services/primary ./modules/storage/internal/infra/device/pebble
```

## Task 5: Implement View Build State Machine

**Files:**

- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/internal/services/view/schedule.go`
- Modify: `modules/storage/internal/services/view/view_builder_test.go`
- Modify: `modules/storage/internal/services/access/query.go`

- [ ] Change timer selection to `view_version > active_view_version`.
- [ ] On build start, call `BeginViewBuild` with target `view_version` and generated result name.
- [ ] On build failure, preserve `active_result` and `active_view_version`, then write `failed/build_error`.
- [ ] On build success, atomically switch `active_result`, `active_view_version`, and clear building fields.
- [ ] Rebuild stale `building` rows by starting a fresh full rebuild from PrimaryStore.
- [ ] Ensure `QueryTimeSeriesRows` and `SearchRecordRows` reject Views with empty `active_result`.
- [ ] Add tests:
  - build failure leaves old active result;
  - success cuts over to new version;
  - stale building can be rebuilt;
  - no active result returns `VIEW_NOT_FOUND` or the existing project error code.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/services/view ./modules/storage/internal/services/access
```

## Task 6: Version DuckDB TimeSeries View Tables

**Files:**

- Modify: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_fallback.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`
- Modify: `modules/storage/internal/services/view/naming_internal_test.go`

- [ ] Generate TimeSeries result names with View version:

```text
ts_view_{space_id}_{view_id}_v{view_version}_{build_id}
```

- [ ] Create DuckDB result tables from ViewColumn definitions.
- [ ] Add an upsert path that can merge a full TimeSeries row into an existing result row.
- [ ] Enforce NULL does not overwrite an existing non-NULL result value.
- [ ] Keep `QueryTimeSeriesRows` reading only the table named by `View.active_result`.
- [ ] Do not copy rows from an old DuckDB View table during rebuild.
- [ ] Add tests for versioned table naming, upsert merge, NULL preservation, and active table query.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/infra/device/duckdb ./modules/storage/internal/services/view
```

## Task 7: Version Bleve Record View Indexes

**Files:**

- Modify: `modules/storage/internal/infra/device/bleve/index.go`
- Modify: `modules/storage/internal/infra/device/bleve/index_test.go`
- Modify: `modules/storage/internal/services/search/service.go`
- Modify: `modules/storage/internal/services/access/query.go`
- Modify: `modules/storage/internal/services/access/service_test.go`

- [ ] Generate Record index names with View version:

```text
record_view_{space_id}_{view_id}_v{view_version}_{build_id}
```

- [ ] Replace Dataset-level indexing with View-level indexing.
- [ ] Remove all use of `DatasetColumn.text_indexed`.
- [ ] Index fields defined by ViewColumn:
  - string/text values support full text search and keyword filtering;
  - numeric values support numeric filtering;
  - bool values support bool filtering;
  - time/version values use normalized sortable forms.
- [ ] Store full RecordRow payload for result reconstruction.
- [ ] Search only the index named by `View.active_result`.
- [ ] Add tests for full text search, structured filters, version range, and active index switching.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/infra/device/bleve ./modules/storage/internal/services/search ./modules/storage/internal/services/access
```

## Task 8: Add Event-Consumer Double Write

**Files:**

- Modify: `modules/storage/internal/services/access/data.go`
- Modify: `modules/storage/internal/services/access/service.go`
- Modify: `modules/storage/internal/services/search/service.go`
- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/internal/core/eventbus/bus.go`
- Modify: `modules/storage/internal/core/eventbus/bus_test.go`

- [ ] For TimeSeries events, find active Views whose primary or included dataset matches the event dataset.
- [ ] For Record events, find active Views whose primary or included dataset matches the event dataset.
- [ ] Re-read complete rows through Access before writing view results.
- [ ] Write each row to `active_result`.
- [ ] If the View has a `building_result`, also write the same complete row to `building_result`.
- [ ] Maintain an in-memory dirty key set per `space_id/view_id/building_view_version` while building.
- [ ] After full scan completes, drain dirty keys by re-reading Access and writing to `building_result`.
- [ ] Cut over only after dirty drain finishes.
- [ ] Treat process crash during building as safe because the stale building state is rebuilt from PrimaryStore.
- [ ] Add tests for active-only write, active+building double write, dirty drain, and crash-retry semantics at service level.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/services/access ./modules/storage/internal/services/view ./modules/storage/internal/services/search ./modules/storage/internal/core/eventbus
```

## Task 9: Simplify Rebuild APIs and Timer Behavior

**Files:**

- Modify: `modules/storage/proto/view.proto`
- Modify: `modules/storage/internal/services/access/query.go`
- Modify: `modules/storage/internal/services/view/schedule.go`
- Modify: `modules/storage/tests/e2e/e2e_test.go`

- [ ] Make `RebuildTimeSeriesView` enqueue a full rebuild for the current `view_version`.
- [ ] Make `RebuildRecordView` enqueue a full rebuild for the current `view_version`.
- [ ] Remove `keys` and `version_range` validation from `RebuildRecordView`.
- [ ] Return a stable `rebuild_id` when a matching build is already running, or return a clear explicit error; choose one behavior and document it in API comments.
- [ ] Ensure timers only rebuild Views that need a new version or are retryable failed/stale-building.
- [ ] Add e2e coverage for manual TimeSeries rebuild and manual Record rebuild.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/internal/services/access ./modules/storage/internal/services/view
```

## Task 10: Update CLI, Seed Data, and Config

**Files:**

- Modify: `modules/storage/config/metadata.seed.yaml`
- Modify: `modules/cli/cmd/metadata.go`
- Modify: `modules/cli/cmd/metadata_test.go`
- Modify: `modules/cli/cmd/storage_import.go`
- Modify: `modules/cli/cmd/storage_import_test.go`
- Modify: `modules/cli/README.md`

- [ ] Remove `text_indexed` from seed metadata and CLI metadata structs.
- [ ] Ensure CLI metadata import accepts View definitions without index flags.
- [ ] Update CLI data import so Record rows may omit version.
- [ ] Print or return the server-generated Record versions after import.
- [ ] Keep CSV field validation based on DatasetColumn/ViewColumn, not index flags.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/cli/...
```

## Task 11: Update Tests and E2E Coverage

**Files:**

- Modify: `modules/storage/tests/e2e/e2e_test.go`
- Modify: `modules/storage/tests/e2e/direct_storage_test.go`
- Modify: `modules/storage/tests/e2e/kline.go`
- Modify: `modules/storage/tests/schema/storage_metadata_schema_test.go`
- Modify: `modules/storage/tests/README.md`
- Modify: `modules/storage/tests/testdata/storage.e2e.yaml`

- [ ] Assert SQLite metadata contains View version/build columns.
- [ ] Assert DuckDB contains the active versioned TimeSeries table after rebuild.
- [ ] Assert Bleve contains/searches the active versioned Record index after rebuild.
- [ ] Add an e2e case:
  1. create View v1;
  2. write rows;
  3. rebuild;
  4. query active v1;
  5. add View column;
  6. observe `view_version=v2`;
  7. rebuild;
  8. query active v2.
- [ ] Add an e2e case for Record default version:
  1. write Record without version;
  2. read returned version from `WriteRecordRowsRsp.keys`;
  3. read by that exact version;
  4. search via Record View.
- [ ] Add an e2e case for rebuild-time writes:
  1. start rebuild;
  2. write a row while building;
  3. wait for cutover;
  4. verify the row is visible in the new active result.
- [ ] Run:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 -tags e2e -timeout 600s ./modules/storage/tests/e2e
go test -count=1 ./modules/storage/tests/schema
```

## Task 12: Update User-Facing Documentation

**Files:**

- Modify: `modules/storage/README.md`
- Modify: `modules/storage/docs/architecture.md`
- Modify: `modules/storage/tests/README.md`
- Modify: `docs/storage-concepts-and-design-intent.md`
- Modify: `docs/storage-target-architecture-and-metadata.md`

- [ ] Document the universal `key + version` fact model.
- [ ] Document TimeSeries version as `data_time`.
- [ ] Document Record default version behavior.
- [ ] Document NULL merge behavior.
- [ ] Document View version fields and rebuild state transitions.
- [ ] Document DuckDB as TimeSeries View storage.
- [ ] Document Bleve as Record View storage.
- [ ] Remove all `text_indexed` documentation.
- [ ] Document that rebuild is full PrimaryStore scan plus event-consumer double write.

## Task 13: Final Verification and Cleanup

**Files:**

- Review all modified storage/CLI files.

- [ ] Search for removed concepts:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "text_indexed|TextIndexed|indexed_only|schema_version|Dataset-level|Dataset 级索引" modules/storage modules/cli docs
```

- [ ] Search for stale Record rebuild key requirements:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
rg -n "RebuildRecordView.*keys|keys are required|version_range.*rebuild" modules/storage modules/cli docs
```

- [ ] Run full unit tests:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 ./modules/storage/... ./modules/cli/...
```

- [ ] Run e2e tests:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
go test -count=1 -tags e2e -timeout 600s ./modules/storage/tests/e2e
```

- [ ] Confirm the final git diff contains no unrelated reversions:

```bash
cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
git diff --stat
git status --short
```

## Suggested Implementation Order

1. Proto contracts and generated code.
2. Metadata schema, SQLite store, cache store, and seed loader.
3. Access/PrimaryStore version and NULL merge semantics.
4. PrimaryStore full scan.
5. View build state machine.
6. DuckDB TimeSeries versioned result tables.
7. Bleve Record versioned indexes.
8. Event-consumer double write.
9. Rebuild API simplification.
10. CLI, seed, docs, and e2e updates.
11. Full test sweep and stale-concept cleanup.

## Non-Goals

- No compatibility layer for old index flags or old rebuild request shapes.
- No old View table copy path during rebuild.
- No Access-layer double write to DuckDB or Bleve.
- No multi-instance distributed build lock in this iteration; single View runner is assumed for now, while metadata state remains compatible with future CAS/lease hardening.
- No physical data migration for production data because the project is not online yet.
