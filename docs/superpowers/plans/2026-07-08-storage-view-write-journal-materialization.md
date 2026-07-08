# Storage View Write-Journal Materialization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild TimeSeries（及对齐的 Record）视图物化链路：主存写入后发布携带完整行的**写入流水**；稳态增量只 patch 本 dataset 贡献列、不回读 PrimaryStore；仅在新建/形状变更时用 `query_window`（默认 `backfill_window`）回扫。

**Architecture:** PrimaryStore 仍是事实源，DuckDB/Bleve 为可重建读模型。将 `*RowsChangedEvent`（仅 keys）改为 `*RowsUpdated` 写入流水（`batch_id` + `written_at` + `rows`）。NATS 入队后立即 Ack，内部按 view 合并批处理。稳态路径去掉重型 `ProjectRows`/回读；join 视图依赖各 dataset 各自流水 patch 列。Backfill 仅由 CreateView / view_version bump / 手动 Rebuild 触发，与热路径分离。

**Tech Stack:** Go, tRPC-Go, NATS JetStream, Pebble PrimaryStore, DuckDB CGO, SQLite metadata, existing `go test`。

**Supersedes:** `docs/superpowers/plans/2026-07-08-storage-view-materialization-lag.md`（旧计划以「批量回读 + staging + 周期 catch-up」优化旧热路径；本计划从架构上替换该路径。旧文档保留作诊断档案，**不要再按旧计划实现**）。

---

## Design Decisions (locked)

| # | Decision |
|---|----------|
| D1 | 流水类型名：`TimeSeriesRowsUpdated` / `RecordRowsUpdated`；字段 `batch_id`、`written_at`、`rows`（禁止再用 EventId/EventTime 话术） |
| D2 | 一次 `Write*Rows` RPC → 一条流水；允许同批多标的（如 SCF 一次写多币种 K 线） |
| D3 | 系统默认回拉窗口配置名：`storage.view.backfill_window`（必填）；视图实例优先 `query_window` |
| D4 | 稳态增量：只改本 dataset 贡献列，不读其他 dataset；允许「半行先插入」 |
| D5 | 去掉稳态 `ProjectRows`；热路径只做事实列 → 视图列的薄映射（如 `MapDatasetColumnsToView`） |
| D6 | NATS：解析入队后立即 Ack；物化吞吐由内部 queue + per-view writer 决定 |
| D7 | Backfill：新建视图、列/join/filter/grain/`query_window` 变更导致 `view_version++`、手动 Rebuild；不做 60s 周期 catch-up |
| D8 | 批内去重键时序：`(space, dataset, subject, freq, dimensions, data_time)`；记录：`(space, dataset, record_id, version)`；同键 **后到覆盖先到** |
| D9 | Access 发布的 `rows` 应是写主存后可用于物化的行内容（至少含本次写入列；推荐写后合并结果，避免物化端再做 partial-column 回读） |
| D10 | Blue-Green 保留：BUILDING 期间流水双写 `building_result` + `active_result` |

## Diagnosis Reminder (why this redesign)

- Raw `binance_spot_kline` 继续写入，但 `spot_kline_1m_view` 可落后数十分钟。
- NATS consumer `Outstanding Acks: 1000/1000`：同步等投影再 Ack。
- 旧链路三放大器：同步 Ack、按 key 回读、同视图表级长临界区。
- 旧优化任务（批量回读 / staging 未去锁 / catch-up）不能根治；未上线项目按合理性直接改协议与物化模型。

## Scope

- In: `modules/storage`（proto、eventbus、access 发布、view builder、DuckDB upsert、config、设计文档）。
- Out（本计划默认不改，除非联调必需）：collector/SCF、cloudnode、factor、frontend。
- 不删除、不改写 PrimaryStore 事实数据。

## Target Hot Path

```text
WriteTimeSeriesRows (多标的一批)
  → PrimaryStore write
  → publish TimeSeriesRowsUpdated{batch_id, written_at, rows}
  → NATS: enqueue → Ack immediately
  → coalesce by merge key
  → for each view bound to dataset:
       MapDatasetColumnsToView (本侧列 only)
       DuckDB upsert (partial columns; allow half-row insert)
```

```text
CreateView / column|join shape change / Rebuild
  → view_version++ → BUILDING
  → scan PrimaryStore in query_window (default backfill_window)
  → assemble full rows (backfill-only join)
  → write building_result → CompleteViewBuild → STEADY
  → during BUILDING: incremental patches also hit building_result
```

## File Structure

### New / heavily reworked

- `modules/storage/proto/message.proto` — 流水消息定义。
- `modules/storage/internal/services/view/builder/write_batch.go` — 入队、批合并、去重。
- `modules/storage/internal/services/view/builder/column_map.go` — 稳态本侧列映射（替代 ProjectRows 热路径）。
- `modules/storage/internal/services/view/builder/view_writer.go` — per-view single-writer channel。
- `modules/storage/internal/services/view/builder/backfill.go` — 窗口扫描回拉（从现有 `view_builder.go` 迁出/重构）。

### Modified

- `modules/storage/internal/infra/eventbus/producer_bus.go` — subject / publish / subscribe 命名与 payload。
- `modules/storage/internal/core/eventbus/bus.go` — Bus API 从 Changed → Updated。
- `modules/storage/internal/services/access/data.go` — 发布 `rows` 而非 `keys`。
- `modules/storage/internal/services/view/builder/service.go` — 异步入队、启动 writer。
- `modules/storage/internal/services/view/builder/time_series.go` — 改为 patch 路径。
- `modules/storage/internal/infra/device/duckdb/view_store.go` — partial-column upsert；允许半行插入。
- `modules/storage/internal/config/loader.go` + `modules/storage/config/storage.yaml` — `backfill_window`。
- `modules/storage/cmd/server/main.go` — 接线配置。
- `docs/存储引擎架构.md`、`modules/storage/README.md` — 物化模型说明。

### Explicitly not implementing (from old lag plan)

- 周期 catch-up goroutine。
- 为热路径服务的「批量回读 keys」。
- 声称去掉写串行化但实际加长锁内路径的 staging（staging 仅可留给 backfill 大页，可选、非本计划必做）。

---

### Task 1: Proto — Write Journal Messages

**Files:**
- Modify: `modules/storage/proto/message.proto`
- Regenerate: `modules/storage/proto/gen/`（按仓库既有 `trpc`/`protoc` 脚本）
- Test: compile + proto 字段存在性（Go 编译即可）

- [ ] **Step 1: Replace message definitions**

将 `message.proto` 改为（注释用中文，与仓库风格一致）：

```protobuf
// TimeSeriesRowsUpdated 表示一次时序主存写入成功后的更新流水。
// 派生消费者应使用 rows 直接物化，不应再按 key 回读 PrimaryStore。
message TimeSeriesRowsUpdated {
  // batch_id 是本批写入流水 ID。
  string batch_id = 1;
  // written_at 是主存写入成功时间（RFC3339Nano）。
  string written_at = 2;
  // space_id / dataset_id 便于路由到关联 View（可与 rows[].key 一致，避免解析每行）。
  string space_id = 3;
  string dataset_id = 4;
  // rows 是本批写入涉及的时序行（可跨多 subject）。
  repeated TimeSeriesRow rows = 5;
  // attributes 是附加属性。
  map<string, string> attributes = 6;
}

// RecordRowsUpdated 表示一次记录主存写入成功后的更新流水。
message RecordRowsUpdated {
  string batch_id = 1;
  string written_at = 2;
  string space_id = 3;
  string dataset_id = 4;
  repeated RecordRow rows = 5;
  map<string, string> attributes = 6;
}
```

删除（或 deprecated 后立即移除）旧的 `TimeSeriesRowsChangedEvent` / `RecordRowsChangedEvent`（新项目无兼容负担）。

- [ ] **Step 2: Regenerate stubs and fix compile breaks**

在仓库惯用命令下重生 `proto/gen`，然后 `go build ./modules/storage/...`，把所有引用旧类型的调用点标为后续 Task 修改（本 Task 至少让 `message.pb.go` 正确生成）。

- [ ] **Step 3: Commit**

```bash
git add modules/storage/proto/message.proto modules/storage/proto/gen
git commit -m "$(cat <<'EOF'
feat(storage): replace row-change events with write journal messages

EOF
)"
```

---

### Task 2: Event Bus Subjects And APIs

**Files:**
- Modify: `modules/storage/internal/infra/eventbus/producer_bus.go`
- Modify: `modules/storage/internal/core/eventbus/bus.go`
- Modify: archive/view subscriptions that still say `Changed`

- [ ] **Step 1: Rename subjects**

```go
DefaultTimeSeriesRowsUpdatedSubject = "moox.storage.time_series.rows_updated.v1"
DefaultRecordRowsUpdatedSubject     = "moox.storage.record.rows_updated.v1"
```

Bus 方法：`PublishTimeSeriesRowsUpdated` / `SubscribeTimeSeriesRowsUpdated`（Record 同理）。

- [ ] **Step 2: Update durable consumer name derivation**

确保 durable 名随 subject kind 变化（避免与旧 `rows_changed` consumer 撞状态）；新部署可干净订阅 `rows_updated`。

- [ ] **Step 3: Unit test round-trip on MemoryBus**

断言 Publish 后 handler 收到含 `rows` 的 `TimeSeriesRowsUpdated`。

- [ ] **Step 4: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): rename eventbus to time series/record rows updated

EOF
)"
```

---

### Task 3: Access Publishes Full Rows Per Write RPC

**Files:**
- Modify: `modules/storage/internal/services/access/data.go`
- Test: access 包内 publish 行为测试（新建或扩展现有 test）

- [ ] **Step 1: Write failing test**

`WriteTimeSeriesRows` 成功后，MemoryBus 上出现一条 `TimeSeriesRowsUpdated`：

- `batch_id` 非空
- `written_at` 非空
- `space_id`/`dataset_id` 与写入一致（同批同 dataset；跨 dataset 则按写路径分组发多条流水，每条只含该 dataset 的 rows）
- `len(rows)` 等于该组写入行数（含多 subject）

- [ ] **Step 2: Implement publish with rows**

替换 `publishTimeSeriesRowsChanged(keys)`：

```go
func (s *Service) publishTimeSeriesRowsUpdated(ctx context.Context, spaceID, datasetID string, rows []*pb.TimeSeriesRow) error {
	if len(rows) == 0 || s.events == nil {
		return nil
	}
	return s.events.PublishTimeSeriesRowsUpdated(ctx, &pb.TimeSeriesRowsUpdated{
		BatchId:   xid.New().String(),
		WrittenAt: time.Now().UTC().Format(time.RFC3339Nano),
		SpaceId:   spaceID,
		DatasetId: datasetID,
		Rows:      cloneTimeSeriesRows(rows),
	})
}
```

Record 路径对称实现。  
跨 PrimaryStore target 分组写入时：**每个成功写入的 group 发一条流水**（保持「一次组写 → 一条流水」；整 RPC 多 group 则多条流水）。

- [ ] **Step 3: Pass tests + commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): publish write journals with full rows

EOF
)"
```

---

### Task 4: NATS Ack-After-Enqueue (Not After Materialize)

**Files:**
- Modify: `modules/storage/internal/services/view/builder/service.go`
- Modify: NATS subscribe 调用链（确认 handler 返回时机）
- Test: builder 单元测试 — enqueue 返回不依赖 DuckDB 完成

- [ ] **Step 1: Change subscribe handler contract**

`enqueueTimeSeries`：

1. 校验/克隆流水
2. 推入内部 batcher/channel
3. **立即返回 nil**（NATS Ack）
4. **删除** `waitDeriveResults` 阻塞

失败策略：入队失败才返回 error → Nak；物化失败走内部日志/metrics + 依赖 backfill/Rebuild 修复（新项目不做 JetStream 同步重试背压）。

- [ ] **Step 2: Test**

Fake slow writer：enqueue 在 writer 完成前必须返回成功。

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
perf(storage): ack write journals after enqueue

EOF
)"
```

---

### Task 5: Coalesce Write Batches And Deduplicate Rows

**Files:**
- Create: `modules/storage/internal/services/view/builder/write_batch.go`
- Create: `modules/storage/internal/services/view/builder/write_batch_test.go`
- Modify: `modules/storage/internal/services/view/builder/options.go`（保留/沿用 `batch_size`、`batch_wait_ms`）

- [ ] **Step 1: Failing tests**

1. 两批流水合并后同键后者覆盖前者列。
2. 同批多 subject（BTC/ETH）不去丢。
3. Flush 触发：`batch_size` 满 **或** `batch_wait` 到期。

- [ ] **Step 2: Implement merge**

```go
func mergeTimeSeriesRowsLatestWins(rows []*pb.TimeSeriesRow) []*pb.TimeSeriesRow
func timeSeriesMergeKey(row *pb.TimeSeriesRow) string
```

Record 对称。合并后按 `dataset_id` 路由到关联 views。

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
perf(storage): coalesce write-journal batches for view materialization

EOF
)"
```

---

### Task 6: Steady-State Column Patch (No ProjectRows, No Cross-Dataset Read)

**Files:**
- Create: `modules/storage/internal/services/view/builder/column_map.go`
- Create: `modules/storage/internal/services/view/builder/column_map_test.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`
- Note: `modules/storage/internal/services/view/projection.go` 的 `TimeSeriesRowsForView` **不得**再用于稳态热路径（可留给 backfill 或逐步收缩）

- [ ] **Step 1: Failing tests**

给定 view 列来自 `kline.close` 与 `funding.rate`：

- 仅含 kline 的流水 → 输出 patch 只有 `close` 映射列，**不**调用任何 reader。
- 仅含 funding 的流水 → 只有 `rate` 列。

- [ ] **Step 2: Implement thin mapper**

```go
// MapDatasetColumnsToView maps columns from one dataset write batch into view column names.
// It never reads other datasets.
func MapDatasetColumnsToView(
	view *pb.View,
	columns []*pb.ViewColumn,
	datasetID string,
	rows []*pb.TimeSeriesRow,
) []*pb.TimeSeriesRow
```

逻辑：过滤 `ViewColumnOriginDataset == datasetID` 的列；复制 grain 键（以 primary grain 约定：用行 key 的 subject/freq/data_time/dimensions）；只填本侧列。

若本流水 dataset **不是** primary，且尚未存在目标 grain 行：仍产出「半行」patch（决策 D4），由 DuckDB upsert 插入。

- [ ] **Step 3: Wire process path to mapper + InsertRows; remove currentTimeSeriesRows reads from steady path**

- [ ] **Step 4: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): patch view columns from write journals without reread

EOF
)"
```

---

### Task 7: DuckDB Partial-Column Upsert (Allow Half-Row Insert)

**Files:**
- Modify: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`

- [ ] **Step 1: Failing tests**

1. 插入仅 `close` 的新 grain → 行存在，`volume` 为空/NULL。
2. 再 upsert 仅 `volume` → 同一 grain，`close` 保留，`volume` 更新。
3. 再 upsert 仅 `close` → `volume` 不被抹掉。

- [ ] **Step 2: Implement upsert semantics**

优先 `INSERT ... ON CONFLICT (row_key, data_time) DO UPDATE SET col = COALESCE(excluded.col, target.col)` 对本次 patch 出现的列；未出现的列不更新。

若当前 DuckDB 版本/表结构不便 ON CONFLICT：锁外算好最终列后短锁写入，但**禁止**为补列去读 PrimaryStore；允许半行。

表级 mutex 可保留（DuckDB 写串行），但临界区只做 SQL upsert，不做 Go 侧 load-merge 长事务。

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): upsert duckdb view patches with partial columns

EOF
)"
```

---

### Task 8: Per-View Writer Pipeline

**Files:**
- Create: `modules/storage/internal/services/view/builder/view_writer.go`
- Modify: `modules/storage/internal/services/view/builder/service.go`

- [ ] **Step 1: One writer goroutine per active DuckDB view_id（懒创建）**

Channel 有界；满则背压在进程内（可丢弃最旧的同键由合并覆盖，或阻塞入队——默认阻塞入队，避免丢更新）。

BUILDING 期间：同一 patch 写入 `building_result` 与（若存在）`active_result`。

- [ ] **Step 2: Test**

两路并发入队同一 view，写入串行且最终行正确。

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): serialize duckdb view patches per view writer

EOF
)"
```

---

### Task 9: Config `backfill_window` + Create/Rebuild Window Resolution

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/cmd/server/main.go`
- Modify: CreateView / Rebuild / `buildTimeRange` 调用处

- [ ] **Step 1: Config**

```yaml
storage:
  view:
    backfill_window: 90d   # 必填；CreateView 未指定 query_window 时的默认回拉窗口
```

`ApplyDefaults`：**不要**静默给空串；启动校验——`backfill_window` 非法或空则 fail-fast（新项目允许强校验）。

解析复用 `parseWindow`（`view_builder.go`）。

窗口解析：

```text
effective_window = view.query_window if non-empty else config.backfill_window
```

- [ ] **Step 2: Tests for resolver + config required**

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): require backfill_window for view history scans

EOF
)"
```

---

### Task 10: Backfill Engine (Create / Version Bump / Rebuild Only)

**Files:**
- Create or refactor: `modules/storage/internal/services/view/builder/backfill.go`（可继续由 `services/view/view_builder.go` 承担构建，但必须与稳态 builder 路径清晰分离）
- Modify: rebuild/pending timer 路径

- [ ] **Step 1: Documented triggers only**

| Trigger | Action |
|---------|--------|
| CreateView（active，无 active_result） | pending → build in effective_window |
| UpsertViewColumn / join dataset / filter / grain / query_window 变更 → `view_version++` | build new result table |
| `RebuildTimeSeriesView` | 同上 |

**禁止**定时 catch-up 循环。

- [ ] **Step 2: Scan with cursor pages**（沿用 `readAllRows` cursor，禁止 page-number 全窗重复扫）

多 dataset：在 **backfill 内**按 grain 组装完整行（这是唯一允许跨 dataset 组装之处）；稳态仍只 patch。

- [ ] **Step 3: Tests**

1. `query_window=30d` 时扫描 start_time ≈ now-30d。
2. `query_window` 空则用 `backfill_window`。
3. BUILDING 中入站流水补丁写到 building 表。

- [ ] **Step 4: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): backfill views from query_window without hot-path catchup

EOF
)"
```

---

### Task 11: Record (Non-Time-Series) Path Parity

**Files:**
- Record publish / subscribe / coalesce / Bleve index patch 对齐 Task 3–6、8 的语义（消息已是 `RecordRowsUpdated`）

- [ ] **Step 1: Same journal + ack-after-enqueue + latest-wins**

Bleve 更新策略：本 dataset 贡献字段 patch；不允许为补列回读。具体 API 保持 `IndexRecordViewRows` 或增加 partial update——实现时选对 Bleve 最简单且正确的路径，测「半文档后补另一侧字段」。

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
feat(storage): align record view materialization with write journals

EOF
)"
```

---

### Task 12: Observability And Docs

**Files:**
- Modify: builder 日志
- Modify: `docs/存储引擎架构.md`
- Modify: `modules/storage/README.md`
- Modify: 本计划 Verification Notes（部署后填写）

- [ ] **Step 1: Logs (INFO, once per flush)**

```text
[ViewBuilder] journal applied dataset=... views=... input_rows=... merged_rows=...
[ViewBuilder] backfill view=.../... window=... rows=...
```

可选指标：入队深度、单 view writer 排队时长、DuckDB upsert 耗时。

- [ ] **Step 2: Docs section**

写清：

- 写入流水 ≠ 事件；字段 `batch_id`/`written_at`/`rows`
- 稳态不回读；join 靠各 dataset 流水 patch
- backfill 仅版本构建；默认窗口 `backfill_window`
- subject：`rows_updated.v1`

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
docs(storage): describe write-journal view materialization

EOF
)"
```

---

### Task 13: Full Test And Manual Verification

- [ ] **Step 1: Unit tests**

```bash
go test ./modules/storage/internal/config ./modules/storage/internal/services/view/builder ./modules/storage/internal/services/view ./modules/storage/internal/services/access ./modules/storage/internal/infra/eventbus -count=1
go test ./modules/storage/internal/infra/device/duckdb -count=1
```

- [ ] **Step 2: Scenario checks (local or remote)**

1. 同一次 Write 多 subject → 一条 `TimeSeriesRowsUpdated`，视图两侧都更新。
2. join 视图先写 A dataset 半行，再写 B dataset → 同行两侧列齐全。
3. CreateView 用默认 `backfill_window` 扫历史；显式 `query_window` 覆盖默认。
4. NATS consumer **不再**因物化慢而长期 `Outstanding Acks` 顶满（允许短暂升高）。
5. `spot_kline_1m_view` 与 raw 最新差距可接受（秒～分钟级，取决于 batch_wait）。

- [ ] **Step 3: Record verification note in this file and commit**

---

## Rollback Plan

- 回滚部署到改流水协议前的版本；新 `rows_updated` subject 可残留无消费者，无事实数据损坏风险。
- DuckDB 结果异常：`RebuildTimeSeriesView` + 有效 `query_window` / `backfill_window` 重建读模型。
- 勿用 `--reset-data` 除非明确要清库。

## Success Criteria

- [ ] 协议与代码中无 `EventId`/`EventTime`/`RowsChangedEvent` 作为物化主路径。
- [ ] 稳态物化路径零 PrimaryStore 回读（join 亦不回读）。
- [ ] 半行插入 + 后补另一侧列行为有测试覆盖。
- [ ] `storage.yaml` 含必填 `backfill_window`；空配置无法安静默认成功启动。
- [ ] NATS Ack 与物化完成解耦；热点视图表单写队列可观测。
- [ ] 无周期 catch-up；仅 Create/版本变更/Rebuild 回扫。

## Out Of Scope Follow-Ups (optional later)

- 热点 1:1 视图 Write-through（跳过 NATS）。
- 事件内携带「写后已在 Access 完成的主存整行 merge」若当前 publish 的是请求 partial columns，需在 Access 发布前合并旧列（若实测需要再单开任务）。
- Bleve 细粒度 partial 文档更新深度优化。

---

## Relation To Old Lag Plan

| Old task | Disposition |
|----------|-------------|
| Task 1 knobs for read batch / catch-up | Replace with `backfill_window` only |
| Task 2 batch read keys | **Dropped** |
| Task 3 metadata TTL cache | Optional later；非本计划 blocker |
| Task 4 staging upsert for root-cause #3 | **Dropped** as hot-path fix；改 partial SQL upsert |
| Task 5 catch-up | **Dropped**；改 versioned backfill |
| Task 6–8 observe/docs/verify | Replaced by Task 12–13 here |
