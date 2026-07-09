# K-line View Query Performance and Browse Default Sort Plan

> **For mooyang:** REQUIRED SUB-SKILL: Use executing-plans to implement this plan.

**Goal:** Fix the collector view browse page so time-series views no longer add `data_time` sorting by default, then complete the short-, mid-, and long-term performance work needed for `binance_spot_kline` and other time-series views. Keep default browse `size=25`, keep default backend/query `limit=1000`, keep `total` controlled by caller through `total_mode`, and reduce query pressure enough that the remote page opens reliably without triggering storage OOM or CPU saturation.

**Scope:**

- Frontend route: `/#/collector/views?tab=browse`
- Main frontend file: `web/src/views/data/view-browse/index.vue`
- Storage module:
  - `modules/storage/internal/infra/device/duckdb/view_store.go`
  - `modules/storage/internal/services/view`
  - `modules/storage/internal/services/access`
  - `modules/storage/internal/infra/transport/nats/producer.go`
  - `modules/storage/config/trpc_go.yaml`
- Remote verification:
  - Web page: `http://106.53.107.122:9527/#/collector/views?tab=browse`
  - Admin gateway for storage APIs: `http://106.53.107.122:11000`
  - Storage pprof admin port configured by tRPC `server.admin`

**Current evidence to preserve while implementing:**

- Default view browse currently sends a 7-day `time_range`, `limit: 1000`, `total_mode: 'NONE'`, and `sorts: buildViewSorts(sortState)`.
- The time-series browse table currently initializes a default `data_time desc` sort. The user explicitly wants the browse page not to add time sorting by default.
- Remote timings observed before this plan:
  - Global view query with `data_time desc`, 7-day range, `limit=1000`: around `5.02s`.
  - BTC-USDT subject query with `data_time desc`, 7-day range, `limit=1000`: around `2.53s`.
  - Global view query with no sort, 7-day range, `limit=1000`: around `2.45s`.
  - Single-column subject raw access was much faster than all-column reads, so column projection matters.
- Storage pprof CPU showed most samples under `runtime.cgocall`, with DuckDB/sqlite/proto JSON on the hot path.
- Storage RSS was around 1GB while Go heap in-use was much smaller, so the OOM pressure is likely native/DuckDB/NATS/buffer/logging related, not a simple Go heap leak.
- Storage logs were dominated by per-message `消息已发布` debug logs from `modules/storage/internal/infra/transport/nats/producer.go` while storage config log level was `debug`.

## Phase 0: Baseline and Safety

- [ ] Confirm the worktree state before editing.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  git status --short
  ```

  Expected output:

  - May show existing dirty files from previous work.
  - Do not revert unrelated user changes.
  - If files touched by this plan already have local changes, read them first and edit around those changes.

- [ ] Capture the exact frontend and storage code points before edits.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "resetSortState|buildViewSorts|VIEW_BROWSE_PREVIEW_LIMIT|loadTimeSeriesViewRows|fetchKlineTableRows|buildKlineQuerySorts" web/src/views/data/view-browse/index.vue
  rg -n "buildTimeSeriesQuery|resultSelectColumns|projectColumns|buildOrderBy|createResultIndexStatements" modules/storage/internal/infra/device/duckdb/view_store.go
  rg -n "消息已发布|Debugf" modules/storage/internal/infra/transport/nats/producer.go
  rg -n "level:" modules/storage/config/trpc_go.yaml
  ```

  Expected output:

  - `VIEW_BROWSE_PREVIEW_LIMIT` remains `1000`.
  - `resetSortState` is the function to remove default time sort from the browse table.
  - `buildKlineQuerySorts` remains separate from browse default sort and can continue to order K-line chart data explicitly.

## Phase 1: Frontend Browse Page Stops Adding Default Time Sort

- [ ] Change `resetSortState` so the browse table starts with no sort for every view mode.

  File: `web/src/views/data/view-browse/index.vue`

  Replace the time-series default sort branch with a neutral reset:

  ```ts
  const resetSortState = () => {
    sortState.fieldName = ''
    sortState.direction = ''
  }
  ```

  Required behavior:

  - Opening `/#/collector/views?tab=browse` must not send `sorts` for the default time-series table load.
  - Clicking a column header still sets `sortState` and sends the explicit user-selected sort.
  - The K-line chart/table modal may still use its own explicit `buildKlineQuerySorts()` because chart rendering needs deterministic time order.

- [ ] Keep `limit=1000` and table `size=25`.

  File: `web/src/views/data/view-browse/index.vue`

  Verify these constants remain:

  ```ts
  const VIEW_BROWSE_PAGE_SIZE = 25
  const VIEW_BROWSE_PREVIEW_LIMIT = 1000
  ```

  Required behavior:

  - Do not set `size=1000`.
  - Browse preview requests use `limit: VIEW_BROWSE_PREVIEW_LIMIT`.
  - Client-side visible page size stays at 25 rows.

- [ ] Keep `total` caller-controlled.

  File: `web/src/views/data/view-browse/index.vue`

  Required behavior:

  - Default browse requests send `total_mode: 'NONE'`.
  - UI must tolerate `total_state: 'SKIPPED'`.
  - Any explicit future caller that needs exact total can send `total_mode: 'FORCE_EXACT'`.

- [ ] Add a frontend unit-level guard if a test harness already exists for this component.

  Check:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "view-browse|buildViewSorts|resetSortState" web/src -g '*.{test,spec}.{ts,tsx,vue}'
  ```

  If a colocated or nearby Vitest test exists, add a test that mounts or invokes the query-building path and asserts the default time-series request has no `sorts`.

  Required assertion shape:

  ```ts
  expect(request.sorts).toEqual([])
  expect(request.limit).toBe(1000)
  expect(request.total_mode).toBe('NONE')
  ```

  If no frontend test harness exists for this view, do not create a large new harness in this phase. Use typecheck, build, and browser/network verification instead.

## Phase 2: Frontend Sends Only Needed Columns

- [ ] Pass `column_names` from the browse page to storage view queries.

  File: `web/src/views/data/view-browse/index.vue`

  In `loadTimeSeriesViewRows`, add `column_names` using the visible/preferred columns already calculated by the component.

  Required behavior:

  - Default table browse requests only the columns rendered in the table, plus system columns needed by storage.
  - K-line modal requests exactly the OHLCV fields it needs for chart and table rendering:

    ```ts
    const KLINE_COLUMN_NAMES = ['open_time', 'open', 'high', 'low', 'close', 'volume']
    ```

  - Do not request all materialized columns unless the user explicitly opens a mode that needs all of them.

- [ ] Keep query contracts compatible with older storage services.

  Required implementation:

  - Add `column_names` only when the list is non-empty.
  - If the backend ignores `column_names`, the frontend still renders from the returned row objects.
  - If a requested column is absent from one row, the table renders an empty cell rather than throwing.

## Phase 3: Storage Production Logging Noise Reduction

- [ ] Stop per-message publish logs from dominating remote storage.

  Files:

  - `modules/storage/config/trpc_go.yaml`
  - `modules/storage/internal/infra/transport/nats/producer.go`

  Required implementation:

  - Change the default storage log level from `debug` to `info` in `modules/storage/config/trpc_go.yaml`.
  - Keep the per-message publish log at debug level in code, but make sure production default config does not emit it.
  - If the code currently logs large payload details, reduce it to topic/status/duration only.

  Target code shape for publish success:

  ```go
  log.Debugf("消息已发布 topic=%s duration=%s", topic, time.Since(start))
  ```

  Required behavior:

  - Normal remote storage logs should no longer print one publish line per data message.
  - Debug can still be enabled manually for short diagnostic windows.

- [ ] Add a regression check for storage config log level.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "level: info" modules/storage/config/trpc_go.yaml
  rg -n "消息已发布" modules/storage/internal/infra/transport/nats/producer.go
  ```

  Expected output:

  - Config contains `level: info`.
  - Producer still has at most one debug publish success log line.

## Phase 4: DataView SQL Column Projection

- [ ] Push `column_names` down into DuckDB SQL selection for time-series view queries.

  File: `modules/storage/internal/infra/device/duckdb/view_store.go`

  Current issue:

  - `buildTimeSeriesQuery` builds SQL using `resultSelectColumns(columns)` and only applies projection after scanning rows.
  - That still asks DuckDB to decode/return all materialized columns.

  Required implementation:

  - Add a helper that returns selected result columns from the schema and the request projection:

    ```go
    func resultSelectColumnsForRequest(columns []ViewResultColumn, requested []string) []string
    ```

  - Always include required system columns:

    ```go
    row_key
    subject_id
    freq
    data_time
    source_time
    value_json
    metadata_json
    ```

  - Include only requested view columns when `requested` is non-empty.
  - If `requested` is empty, preserve existing all-column behavior.
  - Reject or ignore unknown requested columns consistently with existing `projectColumns` behavior. Prefer the current behavior already used by response projection, so API compatibility does not change unexpectedly.

- [ ] Update `buildTimeSeriesQuery` to use the new helper.

  Required code path:

  ```go
  selectColumns := resultSelectColumnsForRequest(columns, req.GetColumnNames())
  queryText := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectColumns, ", "), quoteIdent(resultTable))
  ```

  Required behavior:

  - A request with `column_names: ['close']` does not select `open`, `high`, `low`, or `volume` from DuckDB.
  - Response shape remains compatible with existing `PageResult`.

- [ ] Add a DuckDB query-builder regression test.

  File: `modules/storage/internal/infra/device/duckdb/view_store_test.go`

  Add a focused test near existing query-builder tests:

  ```go
  func TestBuildTimeSeriesQueryProjectsRequestedColumns(t *testing.T) {
      table := "result_test"
      columns := []ViewResultColumn{
          {Name: "open", Type: "decimal"},
          {Name: "high", Type: "decimal"},
          {Name: "low", Type: "decimal"},
          {Name: "close", Type: "decimal"},
          {Name: "volume", Type: "decimal"},
      }
      req := &storagepb.QueryViewRowsRequest{
          ColumnNames: []string{"close"},
          Limit:       1000,
      }

      plan, err := buildTimeSeriesQuery(table, columns, req)
      if err != nil {
          t.Fatalf("buildTimeSeriesQuery() error = %v", err)
      }
      sql := plan.sqlText

      if !strings.Contains(sql, quoteIdent("close")) {
          t.Fatalf("query does not select requested column close: %s", sql)
      }
      for _, name := range []string{"open", "high", "low", "volume"} {
          if strings.Contains(sql, quoteIdent(name)) {
              t.Fatalf("query unexpectedly selects %s: %s", name, sql)
          }
      }
      for _, name := range []string{"row_key", "subject_id", "freq", "data_time"} {
          if !strings.Contains(sql, quoteIdent(name)) {
              t.Fatalf("query does not select required system column %s: %s", name, sql)
          }
      }
  }
  ```

  Adjust field names only if the local `ViewResultColumn` or `queryPlan` types use different names. Keep the test's assertion intent exactly the same.

## Phase 5: Query Ordering and Indexes for Time-Series Views

- [ ] Add a standalone `data_time` index for result tables.

  File: `modules/storage/internal/infra/device/duckdb/view_store.go`

  Current indexes include row-key and subject/freq/time patterns, but global latest browsing benefits from a direct time index or a dedicated latest model.

  Required implementation in `createResultIndexStatements`:

  ```go
  fmt.Sprintf(
      "CREATE INDEX IF NOT EXISTS %s ON %s (%s)",
      quoteIdent(fmt.Sprintf("idx_%s_data_time", tableName)),
      quoteIdent(tableName),
      quoteIdent("data_time"),
  )
  ```

  Keep the existing `(subject_id, freq, data_time)` index.

- [ ] Ensure indexes are created for existing result tables, not only newly built tables.

  File: `modules/storage/internal/infra/device/duckdb/view_store.go`

  Required implementation:

  - Add an `ensureResultIndexes(ctx, resultTable string) error` method that calls the same index creation statements used during table creation.
  - Call it before executing time-series view queries after resolving the result table.
  - Guard repeated calls with an in-memory `sync.Map` keyed by result table name, so every query does not issue DDL.

  Required behavior:

  - Existing `binance_spot_kline` result tables receive the new index after storage restart and first query.
  - A failed index creation returns a visible error instead of silently serving a slow query forever.

- [ ] Keep no-sort browse truly no-sort at the frontend, but keep backend default deterministic ordering.

  Required behavior:

  - If frontend sends no `sorts`, backend may retain its deterministic default order for stable paging.
  - Do not reintroduce frontend default `data_time desc`.
  - For remote freshness checks, use explicit `data_time desc` in the verification request instead of relying on UI defaults.

## Phase 6: Dedicated Latest Read Model for Expensive Global Latest Queries

- [ ] Add a latest-row read path for time-series views.

  File: `modules/storage/internal/infra/device/duckdb/view_store.go`

  Required behavior:

  - Queries matching all of the following use the latest-row path:
    - time-series view result table
    - `limit > 0`
    - sort is exactly `data_time desc` or `data_time desc` followed by stable tie-breakers
    - no offset or `offset=0`
    - no non-time filters that cannot be served by the latest model
  - Queries not matching those conditions continue through the general DuckDB SQL path.

- [ ] Implement latest-row helper table per result table.

  Required schema:

  ```sql
  CREATE TABLE IF NOT EXISTS <result_table>__latest (
      row_key TEXT PRIMARY KEY,
      subject_id TEXT NOT NULL,
      freq TEXT NOT NULL,
      data_time TIMESTAMP NOT NULL,
      source_time TIMESTAMP,
      value_json TEXT,
      metadata_json TEXT
  )
  ```

  Required index:

  ```sql
  CREATE INDEX IF NOT EXISTS <result_table>__latest_data_time ON <result_table>__latest(data_time);
  ```

  Required population:

  - During materialization/rebuild, upsert the newest row per `row_key` into the latest helper table.
  - For full rebuilds, rebuild the helper table transactionally after the result table is swapped or refreshed.
  - For incremental updates, update only changed row keys.

- [ ] Add query routing tests.

  File: `modules/storage/internal/infra/device/duckdb/view_store_test.go`

  Required assertions:

  - A global `data_time desc limit 1000` query routes to `<result_table>__latest`.
  - A no-sort browse query does not route to latest helper unless a caller explicitly requested `data_time desc`.
  - A subject-filtered K-line query with `data_time desc` can use either the subject/freq/time index or latest helper only if it preserves exact row semantics.

## Phase 7: Split Storage Runtime Roles for Long-Term Stability

- [ ] Split current storage `view` role into query and builder responsibilities.

  Files to inspect and then update:

  ```bash
  rg -n "roles|view|access|Start|Register|QueryViewRows|ViewBuilder" modules/storage/cmd modules/storage/internal
  ```

  Required role model:

  - `access`: write/access API and ingestion-facing responsibilities.
  - `view_builder`: materialization, scheduled rebuild, and incremental view updates.
  - `view_query`: read-only DataView query API.
  - `all`: local development profile that starts all roles in one process.

- [ ] Add independent deployable configs.

  Required files:

  - `modules/storage/config/trpc_go.access.yaml`
  - `modules/storage/config/trpc_go.view_builder.yaml`
  - `modules/storage/config/trpc_go.view_query.yaml`
  - Matching storage module configs if the module currently separates app config from tRPC config.

  Required behavior:

  - Each role has its own service name and admin pprof port.
  - The monitor module can probe each role through `/health`.
  - `view_query` can be deployed on more than one machine once the backing read model is replicated or snapshotted.

- [ ] Keep the first deployment conservative.

  Required first deployment topology:

  - One `access+view_builder` instance on the writer host.
  - One `view_query` instance on the same host reading the same local view store only after concurrency is verified.
  - Then add a second `view_query` instance on another host using snapshot replication or object storage sync.

- [ ] Add read-only store opening support only after a concurrency test proves it is safe.

  Required local test:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  go test ./modules/storage/internal/infra/device/duckdb -run 'TestConcurrentWriterAndReadOnlyQuery|TestViewQueryRoleReadOnly' -count=1
  ```

  Expected output:

  - Tests pass.
  - If DuckDB cannot safely support concurrent read-only access to the live writer database, implement snapshot-based query replicas instead of sharing the live DB file.

## Phase 8: Health Checks for Monitor Readiness

- [ ] Verify every independently deployable moox submodule exposes health.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  rg -n "Health|health|/health|Check" cmd modules web-host modules/*/cmd modules/*/internal
  ```

  Required behavior:

  - Every independently deployable module has a lightweight health endpoint.
  - Storage split roles each expose health, including the future `view_query`.
  - Health must not perform heavy data scans.

- [ ] Add monitor-facing health metadata where missing.

  Required response fields:

  ```json
  {
    "status": "ok",
    "service": "storage-view-query",
    "version": "<build version>",
    "role": "view_query",
    "time": "<server time>"
  }
  ```

  Required behavior:

  - Existing health consumers continue working if they only check status code.
  - Monitor module can distinguish multiple storage roles and multiple monitor instances.

## Phase 9: Local Verification

- [ ] Run frontend typecheck and build.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  pnpm --dir web exec vue-tsc --noEmit
  pnpm --dir web build:dev
  ```

  Expected output:

  - Both commands exit `0`.
  - No TypeScript errors from `column_names`, `total_mode`, or sort-state changes.

- [ ] Run focused storage tests.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  CGO_ENABLED=1 go test ./modules/storage/internal/infra/device/duckdb -count=1
  go test ./modules/storage/internal/services/view ./modules/storage/internal/services/access ./modules/storage/internal/config -count=1
  ```

  Expected output:

  - All packages pass.
  - New projection, index, latest-helper, and role tests pass.

- [ ] Run boundary checks if the repo provides the target.

  Run:

  ```bash
  cd /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox
  make check-boundaries
  ```

  Expected output:

  - Command exits `0`.
  - If the target is missing, record that it is unavailable and continue with the package tests above.

## Phase 10: Remote Deploy and Verification

- [ ] Deploy storage and web-host using the repo's existing deployment path.

  Required behavior:

  - Do not deploy unrelated modules.
  - Restart storage so `trpc_go.yaml` admin and log-level changes take effect.
  - Restart web-host so embedded frontend assets update.

- [ ] Verify the browse page opens.

  Open:

  ```text
  http://106.53.107.122:9527/#/collector/views?tab=browse
  ```

  Required browser/network checks:

  - Initial `binance_spot_kline` browse request has `limit: 1000`.
  - Initial request has `total_mode: 'NONE'`.
  - Initial request does not contain `data_time desc` sort.
  - Table page size remains 25.
  - Page renders without timeout or blank state.

- [ ] Verify explicit K-line modal ordering still works.

  Required checks:

  - Open the `binance_spot_kline` K-line view/modal for `BTC-USDT`.
  - Chart data is ordered by time.
  - Red-up/green-down rendering behavior is unchanged.
  - K-line query includes subject filtering and only needed OHLCV columns.

- [ ] Measure remote API performance through the admin gateway.

  Use the storage API path already used in previous verification under port `11000`.

  Required cases:

  - Default browse, no sort, 7-day range, `limit=1000`, `total_mode=NONE`.
  - BTC-USDT subject, `data_time desc`, 7-day range, `limit=1000`, projected OHLCV columns.
  - Global explicit latest, `data_time desc`, 7-day range, `limit=1000`, projected close-only column.

  Required acceptance:

  - Default no-sort browse remains close to or faster than the previous no-sort baseline around `2.45s`.
  - Subject K-line query improves from the previous all-column path and stays under `2.0s` when projected columns are used.
  - Explicit global latest either improves by at least 30% from the previous `5.02s` baseline or routes through the latest helper table with a measured reason if the first query includes helper-table build cost.

- [ ] Capture pprof after deployment.

  Run from the remote host or through an SSH tunnel to the storage admin port:

  ```bash
  curl -o /tmp/storage-cpu.pprof 'http://127.0.0.1:<storage_admin_port>/debug/pprof/profile?seconds=8'
  curl -o /tmp/storage-heap.pprof 'http://127.0.0.1:<storage_admin_port>/debug/pprof/heap'
  go tool pprof -top /tmp/storage-cpu.pprof
  go tool pprof -top /tmp/storage-heap.pprof
  ```

  Expected output:

  - CPU may still include `runtime.cgocall`, but request latency should drop from reduced sorting/projection/latest path.
  - Heap should not show unbounded growth during repeated browse requests.

- [ ] Verify log volume drops.

  Required check:

  - Tail the latest storage log after the page is loaded several times.
  - The log must not be dominated by per-message `消息已发布` lines.
  - Health and error logs remain visible at info/error levels.

## Acceptance Criteria

- [ ] `/#/collector/views?tab=browse` opens and the default time-series browse request does not add `data_time` sorting.
- [ ] Default browse keeps `limit=1000`, `size=25`, and `total_mode=NONE`.
- [ ] Explicit user sorting still works from the table header.
- [ ] K-line chart/modal still uses deterministic time ordering and subject filtering.
- [ ] Frontend sends a narrow `column_names` list for table and K-line reads.
- [ ] DuckDB SQL selection respects requested columns instead of selecting every view column and projecting after scan.
- [ ] Storage default log level no longer emits per-message publish logs in production.
- [ ] Existing result tables receive the new `data_time` index or latest-helper optimization without manual table rebuild.
- [ ] Remote API timings improve against the measured baselines, especially global latest and subject K-line reads.
- [ ] pprof and logs show no evidence of repeated browse requests causing OOM pressure.
- [ ] Split-role groundwork exists for `access`, `view_builder`, and `view_query`, each with health endpoints for the future monitor module.

## Rollback Plan

- [ ] Frontend rollback:

  - Revert only the `view-browse/index.vue` changes from this plan.
  - Keep backend `limit` and `total_mode` support if already deployed, because those are backward-compatible.

- [ ] Storage projection/index rollback:

  - Disable latest-helper routing behind a config flag if query semantics are wrong.
  - Keep index creation statements; `CREATE INDEX IF NOT EXISTS` is safe to leave in place.
  - Revert SQL projection helper only if response compatibility breaks.

- [ ] Logging rollback:

  - Temporarily set storage log level to `debug` only during a narrow diagnostic window.
  - Do not re-enable long-running debug logging on the remote service.

## Notes for Implementation

- Use `apply_patch` for manual edits.
- Do not revert unrelated dirty files.
- Do not run broad, unbounded production scans while verifying. Use explicit `limit`, explicit subject filters for freshness checks, and port `11000` for storage admin API verification.
- If a command output shows a failing test unrelated to this plan, record the package and failure, then continue with focused verification unless the failure blocks the changed code.
