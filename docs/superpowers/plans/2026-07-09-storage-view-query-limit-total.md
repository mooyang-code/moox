# Storage View Query Limit And Total Control Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a safe preview limit and caller-controlled total-count behavior for time-series view queries, so view browse loads up to 1000 rows by default without forcing expensive `COUNT(*)`, while precise pagination can still request exact totals.

**Architecture:** Keep `page.size` as display/server-pagination size with a default of 25. Add `limit` to `QueryTimeSeriesRowsReq` as a preview/TopN mode that returns at most N rows and probes one extra row for `has_more`. Add `total_mode` on the time-series view query and `total_state` on `PageResult` so callers can distinguish exact totals from skipped totals.

**Tech Stack:** Go, tRPC-Go protobuf generation, DuckDB view store, existing storage services, Vue 3 + Arco Design frontend, `pnpm`/Vite build, remote admin gateway verification on port `11000`.

---

## Scope And Decisions

- This plan only changes the time-series view query path: `QueryTimeSeriesRows`.
- Record/Bleve view search keeps its current server-pagination behavior.
- `page.size` defaults to `25` for time-series view pagination.
- `limit` defaults to disabled at protocol level because proto3 scalar fields cannot distinguish omitted `0` from explicit `0`. The frontend view-browse page will explicitly send `limit: 1000` for the default preview load.
- `limit > 0` means preview mode: no page offset, `LIMIT limit + 1`, return at most `limit` rows, set `has_more` from the extra row.
- `page` and `limit` cannot both specify paging behavior. `limit` may be combined with `page: {}` only if page fields are empty.
- `total_mode=AUTO` in preview mode skips `COUNT(*)`.
- `total_mode=AUTO` in page mode computes total only when the query is bounded by `keys`, `time_range`, or `filters`.
- `total_mode=NONE` always skips `COUNT(*)`.
- `total_mode=FORCE_EXACT` computes exact total even in preview mode, but the frontend default must not use it.
- `PageResult.total_state` tells the caller whether `total` is exact or skipped.
- The default frontend table page size becomes `25`, but the initial time-series view browse fetch loads up to `1000` rows into the client for local paging.
- This plan does not fix the existing NATS view-materialization backlog. It only prevents browse queries from creating extra load and removes the "only 50 rows" misleading behavior.

## File Structure

### Modified Files

- `packages/commonpb/moox_common.proto`
  - Add `TotalMode`, `TotalState`, and `PageResult.total_state`.
- `packages/commonpb/moox_common.pb.go`
  - Regenerated from `moox_common.proto`.
- `modules/storage/proto/view.proto`
  - Add `limit` and `total_mode` to `QueryTimeSeriesRowsReq`.
- `modules/storage/proto/gen/view.pb.go`
  - Regenerated storage view protobuf code.
- `modules/storage/proto/gen/common_alias.go`
  - Expose `TotalMode` and `TotalState` aliases if the generator does not maintain this file.
- `modules/storage/internal/services/view/query_options.go`
  - New small helper for validating `QueryTimeSeriesRowsReq` option combinations.
- `modules/storage/internal/services/view/query_options_test.go`
  - Unit tests for `limit`, `page`, and `total_mode` validation.
- `modules/storage/internal/services/view/service.go`
  - Validate query options before calling DuckDB.
- `modules/storage/internal/services/access/query.go`
  - Validate query options for the access-layer view-query path as well.
- `modules/storage/internal/infra/device/duckdb/view_store.go`
  - Build either preview-limit SQL or page SQL; set `PageResult.total_state`.
- `modules/storage/internal/infra/device/duckdb/view_store_test.go`
  - Unit tests for default size 25, preview limit 1000, `AUTO` count rules, and exact-total override.
- `web/src/api/storage/types.ts`
  - Add `TotalMode`, `TotalState`, `PageResult.total_state`.
- `web/src/api/storage/view.ts`
  - Add `limit` and `total_mode` to `QueryTimeSeriesRowsReq`.
- `web/src/views/data/view-browse/index.vue`
  - Use `limit=1000` for time-series preview loads, local page size 25, and no reload on local page changes.
- `docs/存储服务架构与部署.md`
  - Document `limit`, `total_mode`, `total_state`, and safe browse defaults.

---

### Task 1: Extend Protocol For Total State And Time-Series Preview Limit

**Files:**
- Modify: `packages/commonpb/moox_common.proto`
- Modify: `modules/storage/proto/view.proto`
- Generated: `packages/commonpb/moox_common.pb.go`
- Generated: `modules/storage/proto/gen/view.pb.go`
- Modify: `modules/storage/proto/gen/common_alias.go`

- [ ] **Step 1: Add common total enums and result state**

Add these enums before `message Page` in `packages/commonpb/moox_common.proto`:

Use short enum value names on purpose. This keeps generated Go names like `TotalMode_AUTO` and `TotalState_SKIPPED` instead of repetitive names such as `TotalMode_TOTAL_MODE_AUTO`.

```proto
// TotalMode controls whether an API should compute an exact total count.
enum TotalMode {
  // AUTO lets the service decide based on query shape.
  AUTO = 0;
  // NONE skips exact total counting.
  NONE = 1;
  // FORCE_EXACT requires an exact total count.
  FORCE_EXACT = 2;
}

// TotalState describes whether PageResult.total is exact.
enum TotalState {
  // UNKNOWN is used by older callers or APIs not yet setting total state.
  UNKNOWN = 0;
  // EXACT means total is an exact count for the query.
  EXACT = 1;
  // SKIPPED means total was intentionally not computed.
  SKIPPED = 2;
}
```

Extend `PageResult`:

```proto
message PageResult {
  uint32 page = 1;
  uint32 size = 2;
  uint32 total = 3;
  bool has_more = 4;
  string next_cursor = 5;
  // total_state tells callers whether total is exact or skipped.
  TotalState total_state = 6;
}
```

- [ ] **Step 2: Add limit and total_mode to time-series view query**

Extend `QueryTimeSeriesRowsReq` in `modules/storage/proto/view.proto`:

```proto
message QueryTimeSeriesRowsReq {
  common.AuthInfo auth_info = 1;
  string space_id = 2;
  string view_id = 3;
  repeated TimeSeriesKey keys = 4;
  TimeRange time_range = 5;
  repeated string column_names = 6;
  repeated FilterExpr filters = 7;
  repeated SortSpec sorts = 8;
  common.Page page = 9;
  // limit enables preview/TopN mode. When greater than 0, page offset is ignored and at most limit rows are returned.
  uint32 limit = 10;
  // total_mode controls whether the service computes exact total for this query.
  common.TotalMode total_mode = 11;
}
```

- [ ] **Step 3: Regenerate protobuf code**

Run:

```bash
make proto
```

Expected: generated common and storage protobuf Go files update without errors.

- [ ] **Step 4: Expose enum aliases**

Ensure `modules/storage/proto/gen/common_alias.go` exposes the new common enums:

```go
type TotalMode = commonpb.TotalMode
type TotalState = commonpb.TotalState

const (
	TotalMode_AUTO        = commonpb.TotalMode_AUTO
	TotalMode_NONE        = commonpb.TotalMode_NONE
	TotalMode_FORCE_EXACT = commonpb.TotalMode_FORCE_EXACT

	TotalState_UNKNOWN = commonpb.TotalState_UNKNOWN
	TotalState_EXACT   = commonpb.TotalState_EXACT
	TotalState_SKIPPED = commonpb.TotalState_SKIPPED
)
```

- [ ] **Step 5: Compile generated packages**

Run:

```bash
go test ./packages/commonpb ./modules/storage/proto/gen -count=1
```

Expected: both packages pass.

- [ ] **Step 6: Commit protocol changes**

```bash
git add packages/commonpb modules/storage/proto
git commit -m "feat(storage): add view query limit and total state protocol"
```

---

### Task 2: Validate Query Option Combinations

**Files:**
- Create: `modules/storage/internal/services/view/query_options.go`
- Create: `modules/storage/internal/services/view/query_options_test.go`
- Modify: `modules/storage/internal/services/view/service.go`
- Modify: `modules/storage/internal/services/access/query.go`

- [ ] **Step 1: Add failing validation tests**

Create `modules/storage/internal/services/view/query_options_test.go`:

```go
package view

import (
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestValidateTimeSeriesQueryOptionsAllowsPreviewLimit(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{Limit: 1000})
	if err != nil {
		t.Fatalf("ValidateTimeSeriesQueryOptions returned %v, want nil", err)
	}
}

func TestValidateTimeSeriesQueryOptionsRejectsLimitWithPageSize(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{
		Limit: 1000,
		Page:  &pb.Page{Page: 1, Size: 25},
	})
	if err == nil || !strings.Contains(err.Error(), "limit cannot be combined with page") {
		t.Fatalf("error = %v, want limit/page conflict", err)
	}
}

func TestValidateTimeSeriesQueryOptionsAllowsLimitWithEmptyPage(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{
		Limit: 1000,
		Page:  &pb.Page{},
	})
	if err != nil {
		t.Fatalf("ValidateTimeSeriesQueryOptions returned %v, want nil", err)
	}
}

func TestValidateTimeSeriesQueryOptionsRejectsOversizedLimit(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{Limit: MaxTimeSeriesViewQueryLimit + 1})
	if err == nil || !strings.Contains(err.Error(), "limit must be <= 5000") {
		t.Fatalf("error = %v, want max limit error", err)
	}
}
```

- [ ] **Step 2: Run validation tests and verify failure**

Run:

```bash
go test ./modules/storage/internal/services/view -run TestValidateTimeSeriesQueryOptions -count=1
```

Expected: compile failure because `ValidateTimeSeriesQueryOptions` and `MaxTimeSeriesViewQueryLimit` do not exist.

- [ ] **Step 3: Implement validation helper**

Create `modules/storage/internal/services/view/query_options.go`:

```go
package view

import (
	"errors"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

const (
	DefaultTimeSeriesViewPageSize     uint32 = 25
	DefaultTimeSeriesViewPreviewLimit uint32 = 1000
	MaxTimeSeriesViewQueryLimit       uint32 = 5000
)

func ValidateTimeSeriesQueryOptions(req *pb.QueryTimeSeriesRowsReq) error {
	if req == nil {
		return errors.New("query request is required")
	}
	limit := req.GetLimit()
	if limit == 0 {
		return nil
	}
	if limit > MaxTimeSeriesViewQueryLimit {
		return fmt.Errorf("limit must be <= %d", MaxTimeSeriesViewQueryLimit)
	}
	if pageHasPaging(req.GetPage()) {
		return errors.New("limit cannot be combined with page")
	}
	return nil
}

func pageHasPaging(page *pb.Page) bool {
	if page == nil {
		return false
	}
	return page.GetPage() > 0 || page.GetSize() > 0 || page.GetCursor() != ""
}
```

- [ ] **Step 4: Call validation from both query entrypoints**

In `modules/storage/internal/services/view/service.go`, add after the `view_id` check:

```go
if err := ValidateTimeSeriesQueryOptions(req); err != nil {
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
}
```

In `modules/storage/internal/services/access/query.go`, add after the `view_id` check:

```go
if err := view.ValidateTimeSeriesQueryOptions(req); err != nil {
	return &pb.QueryTimeSeriesRowsRsp{RetInfo: response.Error(pb.ErrorCode_INVALID_PARAM, err)}, nil
}
```

- [ ] **Step 5: Run validation package tests**

Run:

```bash
go test ./modules/storage/internal/services/view -run TestValidateTimeSeriesQueryOptions -count=1
```

Expected: all validation tests pass.

- [ ] **Step 6: Commit validation changes**

```bash
git add modules/storage/internal/services/view/query_options.go modules/storage/internal/services/view/query_options_test.go modules/storage/internal/services/view/service.go modules/storage/internal/services/access/query.go
git commit -m "feat(storage): validate time series view query options"
```

---

### Task 3: Implement DuckDB Query Planning For Limit And Total Modes

**Files:**
- Modify: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`

- [ ] **Step 1: Add failing DuckDB query-planning tests**

Append these tests near the existing `TestBuildTimeSeriesQuerySkipsCountForUnboundedBrowse` in `modules/storage/internal/infra/device/duckdb/view_store_test.go`:

```go
func TestBuildTimeSeriesQueryPreviewLimitSkipsCount(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Limit:     1000,
		TotalMode: pb.TotalMode_AUTO,
		Sorts: []*pb.SortSpec{{
			FieldName: "data_time",
			Desc:      true,
		}},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL != "" {
		t.Fatalf("countSQL = %q, want empty", plan.countSQL)
	}
	if !strings.Contains(plan.sqlText, "LIMIT 1001 OFFSET 0") {
		t.Fatalf("sqlText = %q, want limit probe", plan.sqlText)
	}
	if plan.pageNo != 1 || plan.size != 1000 || !plan.preview {
		t.Fatalf("plan page=%d size=%d preview=%v, want 1/1000/true", plan.pageNo, plan.size, plan.preview)
	}
	if plan.totalState != pb.TotalState_SKIPPED {
		t.Fatalf("totalState = %v, want skipped", plan.totalState)
	}
}

func TestBuildTimeSeriesQueryDefaultsPageSizeTo25(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.pageNo != 1 || plan.size != 25 {
		t.Fatalf("page=%d size=%d, want 1/25", plan.pageNo, plan.size)
	}
	if !strings.Contains(plan.sqlText, "LIMIT 26 OFFSET 0") {
		t.Fatalf("sqlText = %q, want size+1 probe", plan.sqlText)
	}
}

func TestBuildTimeSeriesQueryAutoCountsBoundedQuery(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SubjectId: "BTC-USDT", Freq: "1m"}},
		Page: &pb.Page{Page: 1, Size: 25},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL == "" {
		t.Fatalf("countSQL is empty, want exact count for bounded AUTO query")
	}
	if plan.totalState != pb.TotalState_EXACT {
		t.Fatalf("totalState = %v, want exact", plan.totalState)
	}
}

func TestBuildTimeSeriesQueryExactCountsUnboundedQuery(t *testing.T) {
	plan, err := buildTimeSeriesQuery("view_test", nil, &pb.QueryTimeSeriesRowsReq{
		TotalMode: pb.TotalMode_FORCE_EXACT,
		Page:      &pb.Page{Page: 1, Size: 25},
	})
	if err != nil {
		t.Fatalf("buildTimeSeriesQuery: %v", err)
	}
	if plan.countSQL == "" {
		t.Fatalf("countSQL is empty, want exact count for FORCE_EXACT")
	}
	if !strings.Contains(plan.sqlText, "LIMIT 25 OFFSET 0") {
		t.Fatalf("sqlText = %q, want normal page limit without +1 probe", plan.sqlText)
	}
}
```

- [ ] **Step 2: Run focused DuckDB tests and verify failure**

Run:

```bash
go test ./modules/storage/internal/infra/device/duckdb -run 'TestBuildTimeSeriesQuery' -count=1
```

Expected: compile failure because `buildTimeSeriesQuery` still returns multiple values instead of a query plan.

- [ ] **Step 3: Introduce query plan struct**

In `modules/storage/internal/infra/device/duckdb/view_store.go`, add near `QueryTimeSeriesRows`:

```go
type timeSeriesQueryPlan struct {
	sqlText    string
	countSQL   string
	args       []any
	pageNo     uint32
	size       uint32
	preview    bool
	totalState pb.TotalState
}
```

- [ ] **Step 4: Rework `QueryTimeSeriesRows` to consume the plan**

Replace the `buildTimeSeriesQuery` call and `PageResult` construction with this shape:

```go
plan, err := buildTimeSeriesQuery(quoted, columns, req)
if err != nil {
	return nil, nil, nil, err
}
var total uint64
if plan.countSQL != "" {
	if err := s.db.QueryRowContext(ctx, plan.countSQL, plan.args...).Scan(&total); err != nil {
		return nil, nil, nil, err
	}
}
rows, err := s.db.QueryContext(ctx, plan.sqlText, plan.args...)
if err != nil {
	return nil, nil, nil, err
}
defer rows.Close()
out, err := scanResultRows(rows, columns)
if err != nil {
	return nil, nil, nil, err
}
hasMore := uint64(plan.pageNo*plan.size) < total
if plan.countSQL == "" {
	total = 0
	if uint32(len(out)) > plan.size {
		hasMore = true
		out = out[:plan.size]
	} else {
		hasMore = false
	}
}
projectedColumns := projectColumns(columns, req.GetColumnNames())
projectedRows := projectRows(out, req.GetColumnNames())
return projectedColumns, projectedRows, &pb.PageResult{
	Page:       plan.pageNo,
	Size:       plan.size,
	Total:      uint32(total),
	HasMore:    hasMore,
	TotalState: plan.totalState,
}, nil
```

- [ ] **Step 5: Rework `buildTimeSeriesQuery`**

Change `buildTimeSeriesQuery` to return `(*timeSeriesQueryPlan, error)` and implement these rules:

```go
func buildTimeSeriesQuery(quotedTableName string, columns []*pb.ResultColumn, req *pb.QueryTimeSeriesRowsReq) (*timeSeriesQueryPlan, error) {
	where, args, err := buildSQLPredicates(req, columns)
	if err != nil {
		return nil, err
	}
	selectColumns, err := resultSelectColumns(columns)
	if err != nil {
		return nil, err
	}
	orderBy, err := buildOrderBy(req.GetSorts(), columns)
	if err != nil {
		return nil, err
	}

	pageNo, size, preview := queryWindow(req)
	sqlText := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selectColumns, ","), quotedTableName)
	countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s`, quotedTableName)
	if where != "" {
		sqlText += " WHERE " + where
		countSQL += " WHERE " + where
	}
	sqlText += " ORDER BY " + orderBy

	totalState := pb.TotalState_EXACT
	if !shouldCountTimeSeries(req, preview) {
		countSQL = ""
		totalState = pb.TotalState_SKIPPED
	}

	limit := size
	if countSQL == "" {
		limit = size + 1
	}
	offset := uint32(0)
	if !preview {
		offset = (pageNo - 1) * size
	}
	sqlText += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	return &timeSeriesQueryPlan{
		sqlText:    sqlText,
		countSQL:   countSQL,
		args:       args,
		pageNo:     pageNo,
		size:       size,
		preview:    preview,
		totalState: totalState,
	}, nil
}
```

- [ ] **Step 6: Add query-window and count-mode helpers**

Replace the old `shouldSkipTimeSeriesCount` helper with:

```go
const defaultTimeSeriesViewPageSize uint32 = 25

func queryWindow(req *pb.QueryTimeSeriesRowsReq) (uint32, uint32, bool) {
	if req.GetLimit() > 0 {
		return 1, req.GetLimit(), true
	}
	pageNo, size := normalizePage(req.GetPage())
	return pageNo, size, false
}

func shouldCountTimeSeries(req *pb.QueryTimeSeriesRowsReq, preview bool) bool {
	switch req.GetTotalMode() {
	case pb.TotalMode_NONE:
		return false
	case pb.TotalMode_FORCE_EXACT:
		return true
	default:
		if preview {
			return false
		}
		return !unboundedTimeSeriesQuery(req)
	}
}

func unboundedTimeSeriesQuery(req *pb.QueryTimeSeriesRowsReq) bool {
	return len(req.GetKeys()) == 0 && req.GetTimeRange() == nil && len(req.GetFilters()) == 0
}
```

Change `normalizePage` default size:

```go
func normalizePage(page *pb.Page) (uint32, uint32) {
	pageNo := uint32(1)
	size := defaultTimeSeriesViewPageSize
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	return pageNo, size
}
```

- [ ] **Step 7: Run DuckDB tests**

Run:

```bash
go test ./modules/storage/internal/infra/device/duckdb -run 'TestBuildTimeSeriesQuery|TestViewStore' -count=1
```

Expected: focused DuckDB tests pass.

- [ ] **Step 8: Commit DuckDB query changes**

```bash
git add modules/storage/internal/infra/device/duckdb/view_store.go modules/storage/internal/infra/device/duckdb/view_store_test.go
git commit -m "feat(storage): support preview limit for time series view queries"
```

---

### Task 4: Update Frontend API Types

**Files:**
- Modify: `web/src/api/storage/types.ts`
- Modify: `web/src/api/storage/view.ts`

- [ ] **Step 1: Add frontend protocol types**

In `web/src/api/storage/types.ts`, add:

```ts
export type TotalMode =
  | 'AUTO'
  | 'NONE'
  | 'FORCE_EXACT'
  | number;

export type TotalState =
  | 'UNKNOWN'
  | 'EXACT'
  | 'SKIPPED'
  | number;
```

Extend `PageResult`:

```ts
export interface PageResult {
  page: number;
  size: number;
  total: number;
  has_more: boolean;
  next_cursor: string;
  total_state?: TotalState;
}
```

- [ ] **Step 2: Add request fields**

In `web/src/api/storage/view.ts`, import `TotalMode` and extend `QueryTimeSeriesRowsReq`:

```ts
export interface QueryTimeSeriesRowsReq {
  space_id: string;
  view_id: string;
  keys?: TimeSeriesKey[];
  time_range?: TimeRange;
  column_names?: string[];
  filters?: FilterExpr[];
  sorts?: SortSpec[];
  page?: Page;
  limit?: number;
  total_mode?: TotalMode;
}
```

- [ ] **Step 3: Type-check frontend API changes**

Run:

```bash
pnpm --dir web exec vue-tsc --noEmit
```

Expected: Vue type-check passes or fails only on existing unrelated type errors. If it fails on `TotalMode` imports, fix the import list in `web/src/api/storage/view.ts`.

- [ ] **Step 4: Commit frontend type changes**

```bash
git add web/src/api/storage/types.ts web/src/api/storage/view.ts
git commit -m "feat(web): type storage view query limit options"
```

---

### Task 5: Make View Browse Use 1000-Row Preview With 25-Row Local Pages

**Files:**
- Modify: `web/src/views/data/view-browse/index.vue`
- Modify: `web/src/views/data/view-browse/index.vue`

- [ ] **Step 1: Add constants for browse preview**

Near the existing K-line constants in `web/src/views/data/view-browse/index.vue`, add:

```ts
const VIEW_BROWSE_PREVIEW_LIMIT = 1000;
const DEFAULT_VIEW_PAGE_SIZE = 25;
```

- [ ] **Step 2: Change table paging defaults**

Change the `pagination` state:

```ts
const pagination = reactive({
  current: 1,
  pageSize: DEFAULT_VIEW_PAGE_SIZE,
  total: 0,
});
```

Change `tablePagination` options:

```ts
pageSizeOptions: [25, 50, 100, 200],
```

- [ ] **Step 3: Query time-series rows with preview limit**

Change `loadTimeSeriesViewRows` request from page mode to preview mode:

```ts
const rsp = await queryTimeSeriesRows({
  space_id,
  view_id: view.view_id,
  filters: activeFilterExprs(),
  sorts: buildViewSorts(sortState),
  limit: VIEW_BROWSE_PREVIEW_LIMIT,
  total_mode: 'NONE',
});
```

Set total from loaded rows, not from skipped total:

```ts
const pageResult = rsp.page_result;
pagination.total = rows.length;
previewHasMore.value = !!pageResult?.has_more;
```

Add state near the other refs:

```ts
const previewHasMore = ref(false);
```

Reset it in `clearViewState`:

```ts
previewHasMore.value = false;
```

- [ ] **Step 4: Keep time-series page changes local**

Change `onPageChange`:

```ts
async function onPageChange(page: number) {
  pagination.current = page;
  if (mode.value !== 'time_series') {
    await reloadRows();
  }
}
```

Change `onPageSizeChange`:

```ts
async function onPageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize;
  pagination.current = 1;
  if (mode.value !== 'time_series') {
    await reloadRows();
  }
}
```

- [ ] **Step 5: Keep sort and filter reload behavior**

Leave `setSort`, `applyQueryControls`, and `resetQueryControls` reloading rows. These controls change the preview result set and must issue a new `limit=1000` query.

- [ ] **Step 6: Change K-line modal to use limit directly**

Change `fetchKlineTableRows`:

```ts
const rsp = await queryTimeSeriesRows({
  space_id,
  view_id: viewId,
  filters: activeFilterExprs(),
  sorts: buildKlineQuerySorts(),
  limit: normalizedKlineLimit.value,
  total_mode: 'NONE',
});
```

- [ ] **Step 7: Add a compact loaded-count hint**

In the status line near `活跃版本`, add:

```vue
<span v-if="mode === 'time_series' && hasQueried">
  已加载 {{ tableRows.length }} 条<span v-if="previewHasMore">+</span>
</span>
```

This avoids saying "总数 1000" when the backend only returned a preview limit.

- [ ] **Step 8: Build frontend**

Run:

```bash
pnpm --dir web build:dev
```

Expected: Vue type-check and Vite build pass.

- [ ] **Step 9: Commit view browse changes**

```bash
git add web/src/views/data/view-browse/index.vue
git commit -m "feat(web): browse time series views with preview limit"
```

---

### Task 6: Document API Semantics

**Files:**
- Modify: `docs/存储服务架构与部署.md`

- [ ] **Step 1: Add time-series view query section**

Add this section under the storage view/query documentation:

```markdown
### 时序视图查询的 limit 与 total_mode

`QueryTimeSeriesRows` 支持两种查询形态：

- 精确分页：传 `page`，服务端按 `page.page` / `page.size` 返回结果。`page.size` 省略时默认 `25`。
- 预览查询：传 `limit`，服务端最多返回 `limit` 条，并额外探测 1 条设置 `has_more`。默认浏览页传 `limit=1000`。

`total_mode` 控制是否计算精确总数：

- `AUTO`：有 `keys`、`time_range` 或 `filters` 时计算总数；无界浏览时跳过总数。
- `NONE`：跳过总数。
- `FORCE_EXACT`：计算精确总数。

`page_result.total_state` 表示 `total` 是否可信：

- `EXACT`：`total` 是精确总数。
- `SKIPPED`：服务端为了避免大范围 `COUNT(*)` 没有计算总数，调用方应展示“已加载 N 条”而不是“总数 N 条”。

`limit` 不能与有效 `page.page`、`page.size` 或 `page.cursor` 同时使用。
```

- [ ] **Step 2: Commit docs**

```bash
git add docs/存储服务架构与部署.md
git commit -m "docs(storage): document view query limit semantics"
```

---

### Task 7: Run Full Local Verification

**Files:**
- No source edits expected.

- [ ] **Step 1: Run storage tests**

Run:

```bash
go test ./modules/storage/internal/services/view ./modules/storage/internal/services/access ./modules/storage/internal/infra/device/duckdb -count=1
```

Expected: all selected storage tests pass.

- [ ] **Step 2: Run frontend build**

Run:

```bash
pnpm --dir web build:dev
```

Expected: build succeeds.

- [ ] **Step 3: Run module boundary check**

Run:

```bash
make check-boundaries
```

Expected: boundary check passes.

---

### Task 8: Remote Verification After Deploy

**Files:**
- No source edits expected unless verification finds a defect.

- [ ] **Step 1: Deploy storage and web through the existing release path**

Use the repo's existing deploy flow. Preserve production data directories.

```bash
./scripts/deploy-moox.sh --target ubuntu@106.53.107.122
```

Expected: storage, admin gateway, and web-host restart successfully.

- [ ] **Step 2: Verify preview limit over admin gateway**

Run on the remote host:

```bash
curl -fsS -H 'Content-Type: application/json' \
  -d '{
    "spaceId":"crypto",
    "viewId":"spot_kline_1m_view",
    "limit":1000,
    "totalMode":"NONE",
    "sorts":[{"fieldName":"data_time","desc":true}]
  }' \
  http://127.0.0.1:11000/api/admin/storage_view/QueryTimeSeriesRows \
  | jq '{count:(.rows|length), page_result:.page_result, first:.rows[0].key.data_time, last:.rows[-1].key.data_time}'
```

Expected:

- `count` is at most `1000`.
- `page_result.size` is `1000`.
- `page_result.total_state` is `SKIPPED`.
- `page_result.has_more` is `true` when more rows exist.
- The latest row is sorted by `data_time` descending.

- [ ] **Step 3: Verify exact total is opt-in**

Run on the remote host:

```bash
curl -fsS -H 'Content-Type: application/json' \
  -d '{
    "spaceId":"crypto",
    "viewId":"spot_kline_1m_view",
    "keys":[{"spaceId":"crypto","datasetId":"binance_spot_kline","subjectId":"BTC-USDT","freq":"1m"}],
    "totalMode":"FORCE_EXACT",
    "page":{"page":1,"size":25},
    "sorts":[{"fieldName":"data_time","desc":true}]
  }' \
  http://127.0.0.1:11000/api/admin/storage_view/QueryTimeSeriesRows \
  | jq '{count:(.rows|length), page_result:.page_result, first:.rows[0].key.data_time}'
```

Expected:

- `count` is at most `25`.
- `page_result.total` is greater than `25` for active symbols such as `BTC-USDT`.
- `page_result.total_state` is `EXACT`.

- [ ] **Step 4: Verify frontend behavior**

Open:

```text
http://106.53.107.122:9527/#/collector/views?tab=browse
```

Expected:

- Time-series view browse initially loads up to `1000` rows.
- Table local page size defaults to `25`.
- Changing table page does not send another storage query in browser Network panel.
- Sorting or filtering sends a new `QueryTimeSeriesRows` request with `limit=1000`.
- The status line shows loaded-row count, not a misleading exact total.

- [ ] **Step 5: Watch storage memory and pprof during verification**

Run on the remote host while testing:

```bash
ps -o pid,rss,vsz,comm -p "$(pgrep -f moox-storage | head -1)"
curl -fsS http://127.0.0.1:20000/debug/pprof/heap?debug=1 | head -80
```

Expected: storage memory does not grow unbounded during repeated preview queries.

---

## Self-Review

- Spec coverage: The plan covers `limit=1000` preview loading, `page.size=25`, caller-controlled total counting, skipped/exact total states, frontend browse behavior, docs, tests, and remote verification.
- Placeholder scan: No unresolved placeholders remain in commands, files, or expected behavior.
- Type consistency: `TotalMode`, `TotalState`, `limit`, `total_mode`, and `total_state` names are used consistently across proto, Go, and TypeScript. JSON verification uses lower-camel proto field names for direct gateway calls.
