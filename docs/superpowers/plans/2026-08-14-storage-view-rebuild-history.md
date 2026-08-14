# Storage View 重建门禁与构建日志 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在不改变 Collector、Factor 和 Dataset 独立处理边界的前提下，限制高成本 A/B 容量重建的启动时机，持久化重建历史，并在 View 浏览页提供日志弹窗。

**Architecture:** 保留 `t_view_index_builds` 作为当前构建 CAS 状态，新增 `t_view_rebuild_logs` 作为 append-only 历史表。必要修复类重建绕过消费积压门禁；容量整理类重建要求 consumer 已恢复、积压低于阈值、连续空闲检查通过，并由进程级许可保证同一时刻只有一个重建。Metadata 新增只读分页 RPC，Web 在已有构建时间旁按需读取日志。

**Tech Stack:** Go、SQLite、protobuf/tRPC、Vue 3、Arco Design、现有 Storage Metadata/View Reconciler 测试体系。

---

### Task 1: 生成 Metadata 日志协议

**Files:**
- Modify: `modules/storage/proto/metadata.proto`
- Generate: `modules/storage/proto/storagegen/metadata.pb.go`
- Generate: `modules/storage/proto/storagegen/metadata.trpc.go`
- Test: `modules/storage/proto/metadata_proto_test.go`

- [ ] **Step 1: 添加重建日志消息和枚举**

在 `metadata.proto` 中新增：

```proto
enum ViewRebuildTriggerReason {
  VIEW_REBUILD_TRIGGER_UNSPECIFIED = 0;
  VIEW_REBUILD_TRIGGER_INITIAL_BUILD = 1;
  VIEW_REBUILD_TRIGGER_DEFINITION_CHANGE = 2;
  VIEW_REBUILD_TRIGGER_ACTIVE_MISSING = 3;
  VIEW_REBUILD_TRIGGER_ACTIVE_INVALID = 4;
  VIEW_REBUILD_TRIGGER_COVERAGE_REPAIR = 5;
  VIEW_REBUILD_TRIGGER_SIZE_LIMIT = 6;
  VIEW_REBUILD_TRIGGER_MANUAL_REPAIR = 7;
  VIEW_REBUILD_TRIGGER_INTERRUPTED_RETRY = 8;
}

enum ViewRebuildResult {
  VIEW_REBUILD_RESULT_UNSPECIFIED = 0;
  VIEW_REBUILD_RESULT_RUNNING = 1;
  VIEW_REBUILD_RESULT_SUCCEEDED = 2;
  VIEW_REBUILD_RESULT_FAILED = 3;
  VIEW_REBUILD_RESULT_SKIPPED = 4;
}

message ViewRebuildLog {
  int64 log_id = 1;
  string space_id = 2;
  string view_id = 3;
  string build_id = 4;
  string index_id = 5;
  ViewRebuildTriggerReason trigger_reason = 6;
  ViewRebuildResult result = 7;
  string block_reason = 8;
  uint64 target_view_revision = 9;
  uint64 active_view_revision = 10;
  uint64 physical_bytes = 11;
  uint64 num_pending = 12;
  uint64 num_ack_pending = 13;
  uint64 entries_written = 14;
  string started_at = 15;
  string finished_at = 16;
  string first_checked_at = 17;
  string last_checked_at = 18;
  uint64 skip_count = 19;
  string error_summary = 20;
  string details_json = 21;
  string created_at = 22;
  string updated_at = 23;
}

message ListViewRebuildLogsReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string view_id = 3;
  ViewRebuildResult result = 4;
  common.Page page = 5;
}

message ListViewRebuildLogsRsp {
  common.RetInfo ret_info = 1;
  repeated ViewRebuildLog logs = 2;
  common.PageResult page_result = 3;
}
```

Add `ListViewRebuildLogs` to `Metadata`.

- [ ] **Step 2: Add descriptor and RPC contract tests**

Extend `metadata_proto_test.go` to assert the new messages, enums, and `Metadata/ListViewRebuildLogs` descriptor exist.

- [ ] **Step 3: Regenerate generated code and verify drift**

Run:

```bash
cd modules/storage/proto && make proto
git diff --check
```

Expected: generated metadata files update without unrelated proto changes.

---

### Task 2: Persist and query rebuild history

**Files:**
- Modify: `modules/storage/schema/metadata.sql`
- Modify: `modules/storage/internal/service/metadata/store.go`
- Create: `modules/storage/internal/service/metadata/sqlite/crud_view_rebuild_log.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/store.go`
- Modify: `modules/storage/internal/service/metadata/sqlite/crud_view_rebuild_log_test.go`
- Modify: `modules/storage/internal/service/metadata/service.go` or current Metadata RPC implementation

- [ ] **Step 1: Add SQLite table and indexes**

Create `t_view_rebuild_logs` with the fields in the design document. Add indexes for `(c_space_id, c_view_id, c_created_at DESC)` and `(c_space_id, c_view_id, c_trigger_reason, c_block_reason, c_result, c_updated_at DESC)`.

Use `ON DELETE CASCADE` from `(space_id, view_id)` to `t_views`. Keep `t_view_index_builds` unchanged.

- [ ] **Step 2: Extend metadata Reader/Writer interfaces**

Add methods equivalent to:

```go
CreateViewRebuildLog(context.Context, *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error)
UpdateViewRebuildLog(context.Context, *pb.ViewRebuildLog) (*pb.ViewRebuildLog, error)
FindOpenSkippedViewRebuildLog(context.Context, spaceID, viewID string, reason, blockReason string) (*pb.ViewRebuildLog, error)
ListViewRebuildLogs(context.Context, spaceID, viewID string, result pb.ViewRebuildResult, page *pb.Page) ([]*pb.ViewRebuildLog, *pb.PageResult, error)
```

`FindOpenSkippedViewRebuildLog` must only match the latest `skipped` row for the exact trigger/block reason. It must increment `skip_count`, update `last_checked_at`, metrics, and `updated_at` in one transaction.

- [ ] **Step 3: Implement deterministic scanning and pagination**

Order by `c_created_at DESC, c_log_id DESC`. Clamp page size using the existing metadata pagination helper. Return `NOT_FOUND` only for invalid View scope; an empty valid View returns an empty page.

- [ ] **Step 4: Add persistence tests**

Cover schema creation, running-to-success, running-to-failed, skipped deduplication, new row after block reason changes, pagination order, result filtering, and View cascade deletion.

- [ ] **Step 5: Add Metadata RPC handler and tests**

Validate auth, `space_id`, `view_id`, and page. Map storage errors through existing `retinfo` helpers. Test RPC success, empty result, invalid request, and pagination.

---

### Task 3: Add configuration and capacity rebuild gate

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage_view/trpc_go.yaml` or the active Storage View config template
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Test: `modules/storage/internal/config/loader_test.go`
- Test: `modules/storage/internal/service/view/reconcile_test.go`

- [ ] **Step 1: Add validated configuration**

Add:

```go
RebuildMaxPending uint64 `yaml:"rebuild_max_pending"`
RebuildIdleChecks uint32 `yaml:"rebuild_idle_checks"`
```

Default to `32` and `3`. Reject negative/zero `rebuild_idle_checks` and invalid negative pending values at load time; do not silently clamp invalid values.

- [ ] **Step 2: Track process-level rebuild ownership**

Add a process-level mutex/token in the View Service. `tryAcquireRebuildPermit` must return false immediately if another build owns the permit. Release it on every success, failure, panic-safe defer, and activation retry path.

- [ ] **Step 3: Track consecutive idle checks per View**

Store an in-memory counter keyed by `space_id/view_id`. Reset to zero when consumer state is unavailable, consumer is not bound, restore is not ready, or total backlog exceeds the configured threshold. Increment only when all capacity gate conditions pass. Permit a size-limit build only when the counter reaches `RebuildIdleChecks`.

- [ ] **Step 4: Separate necessary and optional rebuild paths**

Classify `needsRebuild` causes before applying the gate. Necessary causes (`initial_build`, revision/contract repair, active missing/invalid, coverage repair, manual repair) bypass backlog and idle checks. `size_limit` alone uses the gate and process-level permit.

- [ ] **Step 5: Record gate skips**

When a size-limit build is skipped, call the metadata history writer with `result=SKIPPED` and the exact blocker. Deduplicate by View, trigger reason, and blocker. Do not write a new row on every reconcile.

- [ ] **Step 6: Add gate tests**

Cover backlog threshold, three consecutive idle checks, counter reset, unavailable consumer fail-closed, another View already building, necessary rebuild bypass, cooldown, and retiring slot behavior.

---

### Task 4: Record build lifecycle results

**Files:**
- Modify: `modules/storage/internal/service/view/reconcile.go`
- Modify: `modules/storage/internal/service/view/build.go`
- Modify: `modules/storage/internal/service/view/service.go`
- Modify: `modules/storage/internal/service/view/reconcile_test.go`
- Modify: `modules/storage/internal/service/view/service_test.go`

- [ ] **Step 1: Add safe reason classification**

Create one helper mapping each reconcile path to the protocol enum. Do not infer reasons from user-visible error strings. Keep `error_summary` bounded and strip credentials, request bodies, and raw SQL.

- [ ] **Step 2: Insert running history at claim success**

After `ClaimViewIndexBuild` succeeds and the process owns the build, create a `running` history row with target revision, active revision, file size, consumer snapshot, `build_id`, and `index_id`.

- [ ] **Step 3: Mark success only after switch**

Update the history row to `succeeded` only after Metadata activation succeeds, runtime Active points to the new index, and the old index cleanup has been scheduled. Fill `finished_at`, entries written, and duration inputs.

- [ ] **Step 4: Mark failure and interrupted builds**

On a confirmed build failure, update the matching row to `failed`. During startup/reconcile cleanup, update an existing `running` row to `failed` with `interrupted_build`; create a fallback row if no matching history exists. Activation response-loss readback must decide the final result rather than guessing from the transport error.

- [ ] **Step 5: Keep history failures non-fatal**

If history persistence fails, preserve the existing A/B state transition and expose a structured warning/metric. Never turn a successful active switch into a failed View solely because the audit row failed.

- [ ] **Step 6: Add lifecycle tests**

Cover successful switch, failed backfill, failed activation, lost activation response readback, restart-interrupted build, and history-write failure. Assert Active A remains usable where the existing A/B contract requires it.

---

### Task 5: Expose ListViewRebuildLogs to Web

**Files:**
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/metadata.ts`
- Modify: `web/src/views/data/view-browse/index.vue`
- Modify: `web/src/views/data/view-browse/view-build-time.ts` or adjacent helper
- Create: `web/src/views/data/view-browse/view-rebuild-log.ts`
- Test: `web/src/views/data/view-browse/view-rebuild-log.test.ts`
- Test: existing view-browse component/API tests

- [ ] **Step 1: Add TypeScript protocol types and API wrapper**

Mirror the generated response shape and add:

```ts
listViewRebuildLogs(params: {
  space_id: string;
  view_id: string;
  result?: string;
  page?: Page;
}): Promise<{ logs: ViewRebuildLog[]; page_result: PageResult }>;
```

- [ ] **Step 2: Add display helpers**

Map trigger reasons and result enums to concise Chinese labels. Format UTC server timestamps through the existing `formatTime` helper. Display skip count and error summary without exposing internal credentials.

- [ ] **Step 3: Add the adjacent `日志` button**

Place a small text button immediately after `buildTimeText` in the shared View browse status line. Keep the existing build status and timestamp unchanged.

- [ ] **Step 4: Add the log modal**

Load only when opened. Show a compact table with time, reason, result, duration, entries, and description. Support server pagination, loading, empty, and error states. Do not poll while closed.

- [ ] **Step 5: Add Web tests**

Test helper mappings, skip aggregation text, pagination request scope, empty state, and API failure display. Confirm the embedded Factor results page receives the same button through the shared component.

---

### Task 6: Documentation and release wiring

**Files:**
- Modify: `modules/storage/README.md`
- Modify: `docs/运维/数据保留与磁盘空间.md`
- Modify: `skills/moox/references/cli-operations.md` if operational rebuild history is documented there
- Modify: active Storage config examples

- [ ] **Step 1: Document gate semantics**

Describe necessary versus capacity rebuilds, defaults `32/3`, skip aggregation, and why increasing worker counts is not a substitute for reducing queue pressure.

- [ ] **Step 2: Document UI log fields**

Explain that the View page log is the authoritative rebuild history, while `t_view_index_builds` is only current state.

- [ ] **Step 3: Add deployment contract checks**

Assert generated Storage configuration includes the new defaults and the Storage build package contains the Metadata RPC generated descriptors.

---

### Task 7: Verification, codeCR, release, and live acceptance

**Files:**
- No source ownership; inspect all changes from Tasks 1-6.

- [ ] **Step 1: Run focused tests**

```bash
cd modules/storage && go test ./proto ./internal/config ./internal/service/metadata/... ./internal/service/view/... -count=1
cd ../../web && npm run test -- --run view-browse
```

- [ ] **Step 2: Run race, vet, generated-code, and contract checks**

```bash
cd modules/storage && go test -race ./internal/service/metadata/... ./internal/service/view/... ./internal/service/viewindex/duckdb/... -count=1
go vet ./...
cd ../.. && git diff --check
bash scripts/tests/contract/test-deploy-moox-storage-view.sh
bash scripts/tests/contract/test-storage-view-watchdog.sh
```

- [ ] **Step 3: Request a fresh codeCR review**

The reviewer must inspect gate bypass classification, history CAS/update semantics, log deduplication, migration safety, RPC authorization, and Web scope filtering. Record every finding with file/symbol/line evidence and fix all P0-P2 findings before release.

- [ ] **Step 4: Build release artifacts**

Build Linux/amd64 Storage primary, Storage View, Storage Node, Storage CLI, Admin/Gateway metadata proxies as required by the existing release script, plus the Web production bundle. Record SHA-256 values.

- [ ] **Step 5: Deploy with rollback evidence**

Back up the Storage Metadata SQLite database and current binaries. Deploy using the canonical production path, verify the Storage View watchdog timer is active and waiting, then check `/healthz` and `/readyz`.

- [ ] **Step 6: Run live functional verification**

On the production host:

1. Query the View page API and verify `ListViewRebuildLogs` returns an empty page or current history without error.
2. Confirm the existing K-line and Factor View latest timestamps continue advancing.
3. Create a controlled capacity gate condition or use a test View with an artificially low threshold; verify a `SKIPPED/consumer_backlog` row is created and deduplicated.
4. Clear the backlog, wait for three idle checks, and verify exactly one `RUNNING` build starts.
5. Verify the final row is `SUCCEEDED` or a safely recorded `FAILED`, Active queries remain available, and the Web log button displays the same record.
6. Restore production thresholds and remove only the temporary test View/metadata.

- [ ] **Step 7: Record release status**

Report local tests, codeCR result, binary hashes, deployment host/time, live API evidence, and any acceptance gaps separately. Do not claim completion until each requirement has direct evidence.
