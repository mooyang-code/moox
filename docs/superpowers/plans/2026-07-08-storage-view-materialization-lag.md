# Storage View Materialization Lag Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reduce `binance_spot_kline -> spot_kline_1m_view` materialization lag from tens of minutes to near-real-time, and add a periodic catch-up path that can repair missed or delayed incremental events.

**Architecture:** Keep PrimaryStore as the source of truth and DuckDB view tables as rebuildable read models. Optimize the incremental view builder by deduplicating event keys, batch-reading fact rows, caching view metadata briefly, and shortening DuckDB write transactions with staging-table upsert. Add a lightweight catch-up loop that periodically replays recent source rows into active time-series views based on each view table's current max `data_time`.

**Tech Stack:** Go, tRPC-Go, NATS JetStream, Pebble PrimaryStore, DuckDB CGO driver, SQLite metadata store, existing `go test` suites, remote deploy via `scripts/deploy-moox.sh`.

---

## Diagnosis Summary

- The raw dataset `binance_spot_kline` is still receiving rows. A direct `ReadTimeSeriesRows` query returned `BTC-USDT` rows at `2026-07-08T07:07:00.000000000Z`.
- The frontend view `spot_kline_1m_view` is reading the DuckDB materialized result and was only at `2026-07-08T06:13:00.000000000Z`.
- The remote NATS consumer `storage_view_time_series_rows_changed_v1` was backlogged with `Outstanding Acks: 1000 / 1000` and more than 20k unprocessed messages.
- The current hot path does too much work per event: it waits for full view projection before Ack, reads time-series keys one by one, reloads view metadata for every batch, and serializes every write to the same DuckDB result table.

## Scope And Decisions

- This change is limited to `modules/storage`. It does not change collector, cloudnode, factor, or frontend behavior.
- No raw K-line data is deleted or rewritten. DuckDB view result tables remain rebuildable derived data.
- The fix must preserve partial-row merge semantics: writing only `close` for an existing row must not erase existing `volume`.
- `max_workers` and NATS `MaxAckPending` are not the primary fix. They can hide symptoms, but the main work is reducing per-message cost and adding catch-up.
- Record/Bleve view materialization should keep its current behavior unless a shared helper is explicitly safe for both time-series and record paths.
- Catch-up should use the view table's own latest `data_time`, not wall-clock time, so it can repair a view that is already behind by more than the lookback window.
- The first deploy should preserve production data directories. Use `scripts/deploy-moox.sh` without `--reset-data`.

## File Structure

### New Files

- `modules/storage/internal/services/view/builder/time_series_keys.go`
  - Owns time-series key identity, deduplication, deterministic sorting, and chunking helpers used by incremental materialization.
- `modules/storage/internal/services/view/builder/time_series_keys_test.go`
  - Verifies duplicate event keys collapse to one read key and output order is stable.
- `modules/storage/internal/services/view/builder/metadata_cache.go`
  - Owns a short-lived in-memory metadata cache for `ListViewsByDataset` and `ListViewColumns`.
- `modules/storage/internal/services/view/builder/metadata_cache_test.go`
  - Verifies cache hits, TTL expiry, and clone isolation.
- `modules/storage/internal/services/view/builder/catchup.go`
  - Owns periodic time-series view catch-up: list active DuckDB views, read each active result max time, scan source rows from `max_time - lookback`, project, and upsert.
- `modules/storage/internal/services/view/builder/catchup_test.go`
  - Verifies catch-up scans from the view's current max time, applies lookback, pages source reads, and writes projected rows.

### Modified Files

- `modules/storage/internal/services/view/builder/options.go`
  - Add `ReadBatchSize`, `MetadataCacheTTL`, `CatchupEnabled`, `CatchupInterval`, `CatchupLookback`, and `CatchupPageSize`.
- `modules/storage/internal/services/view/builder/service.go`
  - Initialize metadata cache, normalize new options, start/stop catch-up goroutine, and expose an internal `catchUpOnce` method for tests.
- `modules/storage/internal/services/view/builder/time_series.go`
  - Deduplicate keys before reading, batch `ReadTimeSeriesRows` calls, use cached metadata, and write fewer larger batches to DuckDB.
- `modules/storage/internal/infra/device/duckdb/view_store.go`
  - Add `MaxDataTime` and replace chunked delete/insert merge with staging-table delete plus insert.
- `modules/storage/internal/infra/device/duckdb/view_store_test.go`
  - Extend behavior tests for staging upsert and max data time.
- `modules/storage/internal/config/loader.go`
  - Add config fields and defaults for metadata cache, read batch size, and catch-up.
- `modules/storage/config/storage.yaml`
  - Add production defaults for the new view builder knobs.
- `modules/storage/cmd/server/main.go`
  - Pass the new config values into `viewbuilder.Options`.
- `docs/存储模块设计.md`
  - Document that time-series views are maintained by incremental events plus periodic catch-up.
- `docs/superpowers/plans/2026-07-08-storage-view-materialization-lag.md`
  - Track this implementation.

---

### Task 1: Add View Builder Runtime Knobs

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/internal/services/view/builder/options.go`
- Modify: `modules/storage/cmd/server/main.go`
- Test: use focused `go test` package commands listed below

- [ ] **Step 1: Add failing config assertions**

Add this test file at `modules/storage/internal/config/loader_test.go` if it does not already exist:

```go
package config

import (
	"testing"
	"time"
)

func TestStorageViewMaterializationDefaults(t *testing.T) {
	var cfg RuntimeConfig
	cfg.ApplyDefaults()

	if cfg.Storage.View.ReadBatchSize != 1000 {
		t.Fatalf("ReadBatchSize = %d, want 1000", cfg.Storage.View.ReadBatchSize)
	}
	if cfg.Storage.View.MetadataCacheTTLMS != int((60 * time.Second) / time.Millisecond) {
		t.Fatalf("MetadataCacheTTLMS = %d, want 60000", cfg.Storage.View.MetadataCacheTTLMS)
	}
	if !cfg.Storage.View.CatchupEnabledValue() {
		t.Fatalf("CatchupEnabled = false, want true")
	}
	if cfg.Storage.View.CatchupIntervalMS != int((60*time.Second)/time.Millisecond) {
		t.Fatalf("CatchupIntervalMS = %d, want 60000", cfg.Storage.View.CatchupIntervalMS)
	}
	if cfg.Storage.View.CatchupLookbackMinutes != 30 {
		t.Fatalf("CatchupLookbackMinutes = %d, want 30", cfg.Storage.View.CatchupLookbackMinutes)
	}
	if cfg.Storage.View.CatchupPageSize != 5000 {
		t.Fatalf("CatchupPageSize = %d, want 5000", cfg.Storage.View.CatchupPageSize)
	}
}
```

- [ ] **Step 2: Run config test and verify it fails**

Run:

```bash
go test ./modules/storage/internal/config -run TestStorageViewMaterializationDefaults -count=1
```

Expected: compile failure because the new `StorageView` fields do not exist.

- [ ] **Step 3: Add config fields and defaults**

Extend `StorageView` in `modules/storage/internal/config/loader.go`:

```go
type StorageView struct {
	MetadataServiceName    string `yaml:"metadata_service_name"`
	AccessServiceName      string `yaml:"access_service_name"`
	BatchSize              int    `yaml:"batch_size"`
	BatchWaitMS            int    `yaml:"batch_wait_ms"`
	MaxWorkers             int    `yaml:"max_workers"`
	ReadBatchSize          int    `yaml:"read_batch_size"`
	MetadataCacheTTLMS     int    `yaml:"metadata_cache_ttl_ms"`
	CatchupEnabled         *bool  `yaml:"catchup_enabled"`
	CatchupIntervalMS      int    `yaml:"catchup_interval_ms"`
	CatchupLookbackMinutes int    `yaml:"catchup_lookback_minutes"`
	CatchupPageSize        int    `yaml:"catchup_page_size"`
}
```

Add defaults in `StorageConfig.ApplyDefaults()` after the existing `MaxWorkers` default:

```go
if c.View.ReadBatchSize <= 0 {
	c.View.ReadBatchSize = 1000
}
if c.View.MetadataCacheTTLMS <= 0 {
	c.View.MetadataCacheTTLMS = 60000
}
if c.View.CatchupIntervalMS <= 0 {
	c.View.CatchupIntervalMS = 60000
}
if c.View.CatchupLookbackMinutes <= 0 {
	c.View.CatchupLookbackMinutes = 30
}
if c.View.CatchupPageSize <= 0 {
	c.View.CatchupPageSize = 5000
}
```

Add a helper so omitted `catchup_enabled` defaults to true while explicit `false` still disables catch-up:

```go
func (v StorageView) CatchupEnabledValue() bool {
	if v.CatchupEnabled == nil {
		return true
	}
	return *v.CatchupEnabled
}
```

Update `modules/storage/config/storage.yaml`:

```yaml
  view:
    metadata_service_name: trpc.moox.storage.Metadata
    access_service_name: trpc.moox.storage.Access
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 4
    read_batch_size: 1000
    metadata_cache_ttl_ms: 60000
    catchup_enabled: true
    catchup_interval_ms: 60000
    catchup_lookback_minutes: 30
    catchup_page_size: 5000
```

Extend `builder.Options` in `modules/storage/internal/services/view/builder/options.go`:

```go
type Options struct {
	Events           eventbus.Bus
	Reader           FactReader
	Metadata         viewsvc.Metadata
	Views            TimeSeriesViewWriter
	Search           RecordViewIndexer
	BatchSize        int
	BatchWait        time.Duration
	MaxWorkers       int
	ReadBatchSize    int
	MetadataCacheTTL time.Duration
	CatchupEnabled   bool
	CatchupInterval  time.Duration
	CatchupLookback  time.Duration
	CatchupPageSize  uint32
}
```

Pass config values in `modules/storage/cmd/server/main.go`:

```go
service := viewbuilder.NewService(viewbuilder.Options{
	Events:           opts.Events,
	Reader:           accessReader,
	Metadata:         metadata,
	Views:            views,
	Search:           search,
	BatchSize:        storage.View.BatchSize,
	BatchWait:        time.Duration(storage.View.BatchWaitMS) * time.Millisecond,
	MaxWorkers:       storage.View.MaxWorkers,
	ReadBatchSize:    storage.View.ReadBatchSize,
	MetadataCacheTTL: time.Duration(storage.View.MetadataCacheTTLMS) * time.Millisecond,
	CatchupEnabled:   storage.View.CatchupEnabledValue(),
	CatchupInterval:  time.Duration(storage.View.CatchupIntervalMS) * time.Millisecond,
	CatchupLookback:  time.Duration(storage.View.CatchupLookbackMinutes) * time.Minute,
	CatchupPageSize:  uint32(storage.View.CatchupPageSize),
})
```

- [ ] **Step 4: Run config test and verify it passes**

Run:

```bash
go test ./modules/storage/internal/config -run TestStorageViewMaterializationDefaults -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add modules/storage/internal/config/loader.go modules/storage/internal/config/loader_test.go modules/storage/config/storage.yaml modules/storage/internal/services/view/builder/options.go modules/storage/cmd/server/main.go
git commit -m "feat(storage): add view materialization tuning config"
```

---

### Task 2: Deduplicate Event Keys And Batch Fact Reads

**Files:**
- Create: `modules/storage/internal/services/view/builder/time_series_keys.go`
- Create: `modules/storage/internal/services/view/builder/time_series_keys_test.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`

- [ ] **Step 1: Write failing key helper tests**

Create `modules/storage/internal/services/view/builder/time_series_keys_test.go`:

```go
package builder

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestDedupeTimeSeriesKeysNormalizesAndSorts(t *testing.T) {
	keys := []*pb.TimeSeriesKey{
		{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "ETH-USDT", Freq: "1m", DataTime: "2026-07-08T06:12:00Z"},
		{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-08T06:12:00Z"},
		{SpaceId: "crypto", DatasetId: "binance_spot_kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-08T06:12:00.000000000Z"},
		nil,
	}

	got := dedupeTimeSeriesKeys(keys)

	if len(got) != 2 {
		t.Fatalf("dedupe len = %d, want 2", len(got))
	}
	if got[0].GetSubjectId() != "BTC-USDT" {
		t.Fatalf("first subject = %q, want BTC-USDT", got[0].GetSubjectId())
	}
	if got[0].GetDataTime() != "2026-07-08T06:12:00.000000000Z" {
		t.Fatalf("first data_time = %q, want normalized timestamp", got[0].GetDataTime())
	}
	if got[1].GetSubjectId() != "ETH-USDT" {
		t.Fatalf("second subject = %q, want ETH-USDT", got[1].GetSubjectId())
	}
}

func TestChunkTimeSeriesKeysHonorsBatchSize(t *testing.T) {
	keys := []*pb.TimeSeriesKey{
		{SubjectId: "A"},
		{SubjectId: "B"},
		{SubjectId: "C"},
	}

	got := chunkTimeSeriesKeys(keys, 2)

	if len(got) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(got))
	}
	if len(got[0]) != 2 || len(got[1]) != 1 {
		t.Fatalf("chunk lengths = %d,%d, want 2,1", len(got[0]), len(got[1]))
	}
}
```

- [ ] **Step 2: Run key helper tests and verify they fail**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -run 'TestDedupeTimeSeriesKeys|TestChunkTimeSeriesKeys' -count=1
```

Expected: compile failure because the helper functions do not exist.

- [ ] **Step 3: Add key helper implementation**

Create `modules/storage/internal/services/view/builder/time_series_keys.go`:

```go
package builder

import (
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func dedupeTimeSeriesKeys(keys []*pb.TimeSeriesKey) []*pb.TimeSeriesKey {
	seen := make(map[string]*pb.TimeSeriesKey, len(keys))
	for _, key := range keys {
		if key == nil {
			continue
		}
		copied := proto.Clone(key).(*pb.TimeSeriesKey)
		if normalized, err := factkey.NormalizeTimeVersion(copied.GetDataTime()); err == nil {
			copied.DataTime = normalized
		}
		id := timeSeriesMaterializationKey(copied)
		if id == "" {
			continue
		}
		seen[id] = copied
	}
	out := make([]*pb.TimeSeriesKey, 0, len(seen))
	for _, key := range seen {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		return timeSeriesMaterializationKey(out[i]) < timeSeriesMaterializationKey(out[j])
	})
	return out
}

func timeSeriesMaterializationKey(key *pb.TimeSeriesKey) string {
	if key == nil {
		return ""
	}
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetSubjectId(),
		key.GetFreq(),
		factkey.DimensionsHash(key.GetDimensions()),
		key.GetDataTime(),
	}, "\x00")
}

func chunkTimeSeriesKeys(keys []*pb.TimeSeriesKey, batchSize int) [][]*pb.TimeSeriesKey {
	if batchSize <= 0 {
		batchSize = 1000
	}
	var chunks [][]*pb.TimeSeriesKey
	for start := 0; start < len(keys); start += batchSize {
		end := start + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		chunks = append(chunks, keys[start:end])
	}
	return chunks
}
```

- [ ] **Step 4: Update `currentTimeSeriesRows` to batch reads**

In `modules/storage/internal/services/view/builder/time_series.go`, replace the loop in `currentTimeSeriesRows` with this structure:

```go
func (s *Service) currentTimeSeriesRows(ctx context.Context, keys []*pb.TimeSeriesKey) ([]*pb.TimeSeriesRow, error) {
	queryKeys := dedupeTimeSeriesKeys(keys)
	if len(queryKeys) == 0 {
		return nil, nil
	}
	batchSize := s.readBatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	var out []*pb.TimeSeriesRow
	for _, chunk := range chunkTimeSeriesKeys(queryKeys, batchSize) {
		cloned := make([]*pb.TimeSeriesKey, 0, len(chunk))
		for _, key := range chunk {
			cloned = append(cloned, proto.Clone(key).(*pb.TimeSeriesKey))
		}
		rsp, err := s.reader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{Keys: cloned})
		if err != nil {
			return nil, err
		}
		if rsp == nil {
			return nil, errors.New("read time-series rows returned nil response")
		}
		if err := retInfoError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		out = append(out, rsp.GetRows()...)
	}
	return out, nil
}
```

Add `readBatchSize int` to `Service` and set it in `NewService`:

```go
readBatchSize := opts.ReadBatchSize
if readBatchSize <= 0 {
	readBatchSize = 1000
}
```

- [ ] **Step 5: Add batch-read behavior test**

Create this test in `modules/storage/internal/services/view/builder/time_series_test.go`:

```go
package builder

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestCurrentTimeSeriesRowsDedupesAndBatchesReads(t *testing.T) {
	reader := &countingFactReader{}
	service := NewService(Options{Reader: reader, ReadBatchSize: 2})

	keys := []*pb.TimeSeriesKey{
		testBuilderTimeSeriesKey("BTC-USDT", "2026-07-08T06:12:00Z"),
		testBuilderTimeSeriesKey("BTC-USDT", "2026-07-08T06:12:00.000000000Z"),
		testBuilderTimeSeriesKey("ETH-USDT", "2026-07-08T06:12:00Z"),
		testBuilderTimeSeriesKey("SOL-USDT", "2026-07-08T06:12:00Z"),
	}

	rows, err := service.currentTimeSeriesRows(context.Background(), keys)
	if err != nil {
		t.Fatalf("currentTimeSeriesRows: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if len(reader.readKeyCounts) != 2 {
		t.Fatalf("read calls = %d, want 2", len(reader.readKeyCounts))
	}
	if reader.readKeyCounts[0] != 2 || reader.readKeyCounts[1] != 1 {
		t.Fatalf("read key counts = %v, want [2 1]", reader.readKeyCounts)
	}
}

type countingFactReader struct {
	readKeyCounts []int
}

func (r *countingFactReader) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	r.readKeyCounts = append(r.readKeyCounts, len(req.GetKeys()))
	rows := make([]*pb.TimeSeriesRow, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		rows = append(rows, &pb.TimeSeriesRow{Key: key})
	}
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"},
		Rows:    rows,
	}, nil
}

func (r *countingFactReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}}, nil
}

func testBuilderTimeSeriesKey(subjectID string, dataTime string) *pb.TimeSeriesKey {
	return &pb.TimeSeriesKey{
		SpaceId:   "crypto",
		DatasetId: "binance_spot_kline",
		SubjectId: subjectID,
		Freq:      "1m",
		DataTime:  dataTime,
	}
}
```

- [ ] **Step 6: Run builder tests and verify they pass**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -run 'TestDedupeTimeSeriesKeys|TestChunkTimeSeriesKeys|TestCurrentTimeSeriesRows' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add modules/storage/internal/services/view/builder/time_series_keys.go modules/storage/internal/services/view/builder/time_series_keys_test.go modules/storage/internal/services/view/builder/time_series.go modules/storage/internal/services/view/builder/time_series_test.go modules/storage/internal/services/view/builder/service.go modules/storage/internal/services/view/builder/options.go
git commit -m "perf(storage): batch time series view reads"
```

---

### Task 3: Cache View Metadata During Incremental Projection

**Files:**
- Create: `modules/storage/internal/services/view/builder/metadata_cache.go`
- Create: `modules/storage/internal/services/view/builder/metadata_cache_test.go`
- Modify: `modules/storage/internal/services/view/builder/service.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`

- [ ] **Step 1: Write failing metadata cache tests**

Create `modules/storage/internal/services/view/builder/metadata_cache_test.go`:

```go
package builder

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestMetadataCacheReusesViewsAndColumnsWithinTTL(t *testing.T) {
	ctx := context.Background()
	store := &metadataCacheTestStore{
		views: []*pb.View{{
			SpaceId:          "crypto",
			ViewId:           "spot_kline_1m_view",
			PrimaryDatasetId: "binance_spot_kline",
			DatasetIds:       []string{"binance_spot_kline"},
			Engine:           "duckdb",
			ActiveResult:     "view_result",
		}},
		columns: []*pb.ViewColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	now := time.Date(2026, 7, 8, 7, 0, 0, 0, time.UTC)
	cache := newViewMetadataCache(store, time.Minute, func() time.Time { return now })

	if _, err := cache.listViewsByDataset(ctx, "crypto", "binance_spot_kline"); err != nil {
		t.Fatalf("first listViewsByDataset: %v", err)
	}
	if _, err := cache.listViewsByDataset(ctx, "crypto", "binance_spot_kline"); err != nil {
		t.Fatalf("second listViewsByDataset: %v", err)
	}
	if _, _, err := cache.listViewColumns(ctx, "crypto", "spot_kline_1m_view"); err != nil {
		t.Fatalf("first listViewColumns: %v", err)
	}
	if _, _, err := cache.listViewColumns(ctx, "crypto", "spot_kline_1m_view"); err != nil {
		t.Fatalf("second listViewColumns: %v", err)
	}

	if store.viewCalls != 1 {
		t.Fatalf("viewCalls = %d, want 1", store.viewCalls)
	}
	if store.columnCalls != 1 {
		t.Fatalf("columnCalls = %d, want 1", store.columnCalls)
	}
}

func TestMetadataCacheExpiresAfterTTL(t *testing.T) {
	ctx := context.Background()
	store := &metadataCacheTestStore{
		views:   []*pb.View{{SpaceId: "crypto", ViewId: "v1", Engine: "duckdb"}},
		columns: []*pb.ViewColumn{{ColumnName: "close"}},
	}
	now := time.Date(2026, 7, 8, 7, 0, 0, 0, time.UTC)
	cache := newViewMetadataCache(store, time.Minute, func() time.Time { return now })

	_, _ = cache.listViewsByDataset(ctx, "crypto", "binance_spot_kline")
	now = now.Add(time.Minute + time.Nanosecond)
	_, _ = cache.listViewsByDataset(ctx, "crypto", "binance_spot_kline")

	if store.viewCalls != 2 {
		t.Fatalf("viewCalls = %d, want 2", store.viewCalls)
	}
}

type metadataCacheTestStore struct {
	viewCalls   int
	columnCalls int
	views       []*pb.View
	columns     []*pb.ViewColumn
}

func (s *metadataCacheTestStore) ListViewsByDataset(context.Context, string, string) ([]*pb.View, error) {
	s.viewCalls++
	out := make([]*pb.View, 0, len(s.views))
	for _, item := range s.views {
		out = append(out, proto.Clone(item).(*pb.View))
	}
	return out, nil
}

func (s *metadataCacheTestStore) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	s.columnCalls++
	out := make([]*pb.ViewColumn, 0, len(s.columns))
	for _, item := range s.columns {
		out = append(out, proto.Clone(item).(*pb.ViewColumn))
	}
	return out, &pb.PageResult{}, nil
}
```

Use a small wrapper interface in the test if the compiler asks for methods not needed by `newViewMetadataCache`.

- [ ] **Step 2: Run metadata cache tests and verify they fail**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -run TestMetadataCache -count=1
```

Expected: compile failure because the cache does not exist.

- [ ] **Step 3: Add metadata cache implementation**

Create `modules/storage/internal/services/view/builder/metadata_cache.go`:

```go
package builder

import (
	"context"
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type metadataCacheStore interface {
	ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error)
	ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error)
}

type viewMetadataCache struct {
	store metadataCacheStore
	ttl   time.Duration
	now   func() time.Time

	mu      sync.Mutex
	views   map[string]cachedViews
	columns map[string]cachedColumns
}

type cachedViews struct {
	expiresAt time.Time
	items     []*pb.View
}

type cachedColumns struct {
	expiresAt time.Time
	items     []*pb.ViewColumn
	page      *pb.PageResult
}

func newViewMetadataCache(store metadataCacheStore, ttl time.Duration, now func() time.Time) *viewMetadataCache {
	if now == nil {
		now = time.Now
	}
	return &viewMetadataCache{
		store:   store,
		ttl:     ttl,
		now:     now,
		views:   make(map[string]cachedViews),
		columns: make(map[string]cachedColumns),
	}
}

func (c *viewMetadataCache) listViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	key := spaceID + "\x00" + datasetID
	if c.ttl > 0 {
		c.mu.Lock()
		item, ok := c.views[key]
		if ok && c.now().Before(item.expiresAt) {
			out := cloneViews(item.items)
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
	}
	items, err := c.store.ListViewsByDataset(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.views[key] = cachedViews{expiresAt: c.now().Add(c.ttl), items: cloneViews(items)}
	c.mu.Unlock()
	return cloneViews(items), nil
}

func (c *viewMetadataCache) listViewColumns(ctx context.Context, spaceID string, viewID string) ([]*pb.ViewColumn, *pb.PageResult, error) {
	key := spaceID + "\x00" + viewID
	if c.ttl > 0 {
		c.mu.Lock()
		item, ok := c.columns[key]
		if ok && c.now().Before(item.expiresAt) {
			cols, page := cloneViewColumns(item.items), clonePageResult(item.page)
			c.mu.Unlock()
			return cols, page, nil
		}
		c.mu.Unlock()
	}
	cols, page, err := c.store.ListViewColumns(ctx, spaceID, viewID, &pb.Page{Size: 10000})
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	c.columns[key] = cachedColumns{expiresAt: c.now().Add(c.ttl), items: cloneViewColumns(cols), page: clonePageResult(page)}
	c.mu.Unlock()
	return cloneViewColumns(cols), clonePageResult(page), nil
}

func cloneViews(items []*pb.View) []*pb.View {
	out := make([]*pb.View, 0, len(items))
	for _, item := range items {
		out = append(out, proto.Clone(item).(*pb.View))
	}
	return out
}

func cloneViewColumns(items []*pb.ViewColumn) []*pb.ViewColumn {
	out := make([]*pb.ViewColumn, 0, len(items))
	for _, item := range items {
		out = append(out, proto.Clone(item).(*pb.ViewColumn))
	}
	return out
}

func clonePageResult(page *pb.PageResult) *pb.PageResult {
	if page == nil {
		return nil
	}
	return proto.Clone(page).(*pb.PageResult)
}
```

- [ ] **Step 4: Wire cache into Service and time-series processing**

Add fields to `Service`:

```go
metadataCache *viewMetadataCache
now           func() time.Time
```

In `NewService`, set:

```go
now := time.Now
metadataCache := newViewMetadataCache(opts.Metadata, opts.MetadataCacheTTL, now)
```

In `processTimeSeriesBatch`, replace direct metadata calls:

```go
views, err := s.metadataCache.listViewsByDataset(ctx, key.spaceID, key.datasetID)
```

and:

```go
columns, _, err := s.metadataCache.listViewColumns(ctx, item.GetSpaceId(), item.GetViewId())
```

Keep direct metadata calls in full rebuild code unchanged.

- [ ] **Step 5: Run metadata cache tests and builder tests**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -run 'TestMetadataCache|TestCurrentTimeSeriesRows' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/services/view/builder/metadata_cache.go modules/storage/internal/services/view/builder/metadata_cache_test.go modules/storage/internal/services/view/builder/service.go modules/storage/internal/services/view/builder/time_series.go
git commit -m "perf(storage): cache view metadata for incremental builds"
```

---

### Task 4: Shorten DuckDB Upsert Transactions

**Files:**
- Modify: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`

- [ ] **Step 1: Add failing DuckDB behavior tests**

Append to `modules/storage/internal/infra/device/duckdb/view_store_test.go`:

```go
func TestMaxDataTimeReturnsLatestMaterializedTime(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	if err := store.CreateResultTable(ctx, "test_view", []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-08T06:12:00Z", duckDBTestValue("close", 1)),
		duckDBTestRow("ETH-USDT", "2026-07-08T06:13:00Z", duckDBTestValue("close", 2)),
	}); err != nil {
		t.Fatalf("InsertRows: %v", err)
	}

	got, err := store.MaxDataTime(ctx, "test_view")
	if err != nil {
		t.Fatalf("MaxDataTime: %v", err)
	}
	if got != "2026-07-08T06:13:00.000000000Z" {
		t.Fatalf("MaxDataTime = %q, want 2026-07-08T06:13:00.000000000Z", got)
	}
}

func TestInsertRowsLargeBatchPreservesPartialMergeSemantics(t *testing.T) {
	ctx := context.Background()
	store, err := Open(Options{Path: filepath.Join(t.TempDir(), "views.duckdb")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	columns := []*pb.ViewColumn{
		{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{ColumnName: "volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
	if err := store.CreateResultTable(ctx, "test_view_large", columns); err != nil {
		t.Fatalf("CreateResultTable: %v", err)
	}
	if err := store.InsertRows(ctx, "test_view_large", []*pb.TimeSeriesRow{
		duckDBTestRow("BTC-USDT", "2026-07-08T06:12:00Z", duckDBTestValue("close", 1), duckDBTestValue("volume", 10)),
	}); err != nil {
		t.Fatalf("InsertRows base: %v", err)
	}

	var rows []*pb.TimeSeriesRow
	rows = append(rows, duckDBTestRow("BTC-USDT", "2026-07-08T06:12:00Z", duckDBTestValue("close", 2)))
	for i := 0; i < 1000; i++ {
		rows = append(rows, duckDBTestRow("ALT-USDT", time.Date(2026, 7, 8, 6, 13+i, 0, 0, time.UTC).Format(time.RFC3339), duckDBTestValue("close", float64(i))))
	}
	if err := store.InsertRows(ctx, "test_view_large", rows); err != nil {
		t.Fatalf("InsertRows large patch: %v", err)
	}

	var count int
	var closeValue, volumeValue float64
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(close), MAX(volume)
		FROM test_view_large
		WHERE subject_id = 'BTC-USDT' AND data_time = '2026-07-08T06:12:00.000000000Z'
	`).Scan(&count, &closeValue, &volumeValue); err != nil {
		t.Fatalf("query merged BTC row: %v", err)
	}
	if count != 1 || closeValue != 2 || volumeValue != 10 {
		t.Fatalf("merged BTC row = count:%d close:%v volume:%v, want count:1 close:2 volume:10", count, closeValue, volumeValue)
	}
}
```

Add `time` to the test imports.

- [ ] **Step 2: Run DuckDB tests and verify they fail**

Run:

```bash
go test ./modules/storage/internal/infra/device/duckdb -run 'TestMaxDataTime|TestInsertRowsLargeBatch' -count=1
```

Expected: compile failure because `MaxDataTime` does not exist.

- [ ] **Step 3: Add `MaxDataTime`**

Add this method to `view_store.go`:

```go
func (s *ViewStore) MaxDataTime(ctx context.Context, tableName string) (string, error) {
	quoted, err := quoteTableName(tableName)
	if err != nil {
		return "", err
	}
	var value sql.NullString
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT MAX(data_time) FROM %s`, quoted)).Scan(&value); err != nil {
		return "", err
	}
	if !value.Valid {
		return "", nil
	}
	return value.String, nil
}
```

Ensure `database/sql` is already imported in `view_store.go`; if it is already present, do not add a duplicate import.

- [ ] **Step 4: Replace chunked delete with staging-table delete plus insert**

Keep `mergeRowsByPrimaryKey`, `loadExistingRows`, and `mergeTimeSeriesRow` so partial updates still work. Replace the body of `mergeRowsIntoTable` after final `mergedRows` are computed with a staging table transaction:

```go
func (s *ViewStore) mergeRowsIntoTable(ctx context.Context, quotedTableName string, columns []*pb.ResultColumn, rows []*pb.TimeSeriesRow) error {
	mergedRows := mergeRowsByPrimaryKey(rows)
	if len(mergedRows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	existing, err := loadExistingRows(ctx, tx, quotedTableName, mergedRows)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	tableName, err := unquoteTableName(quotedTableName)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	stagingName := tableName + "_staging_" + strings.ReplaceAll(xid.New().String(), "-", "_")
	quotedStaging, err := quoteTableName(stagingName)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE TEMP TABLE %s AS SELECT * FROM %s LIMIT 0`, quotedStaging, quotedTableName)); err != nil {
		_ = tx.Rollback()
		return err
	}

	insertSQL, err := buildInsertSQL(quotedStaging, columns)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	insertStmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer insertStmt.Close()
	for _, row := range mergedRows {
		merged := row
		if existingRaw := existing[rowPrimaryKey(row)]; existingRaw != "" {
			base := &pb.TimeSeriesRow{}
			if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(existingRaw), base); err != nil {
				_ = tx.Rollback()
				return err
			}
			merged = mergeTimeSeriesRow(base, row)
		}
		args, err := resultRowArgs(merged, columns)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := insertStmt.ExecContext(ctx, args...); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		DELETE FROM %s
		USING %s
		WHERE %s.row_key = %s.row_key AND %s.data_time = %s.data_time
	`, quotedTableName, quotedStaging, quotedTableName, quotedStaging, quotedTableName, quotedStaging)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s`, quotedTableName, quotedStaging)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, quotedStaging)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
```

Add `github.com/rs/xid` to `view_store.go` imports for the staging table suffix.

If DuckDB rejects qualified quoted table names in `DELETE ... USING`, use tuple predicate SQL instead:

```sql
DELETE FROM target
WHERE (row_key, data_time) IN (SELECT row_key, data_time FROM staging)
```

Keep this fallback inside the implementation, not as a manual deployment step.

- [ ] **Step 5: Run DuckDB tests**

Run:

```bash
go test ./modules/storage/internal/infra/device/duckdb -run 'TestInsertRows|TestMaxDataTime' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/infra/device/duckdb/view_store.go modules/storage/internal/infra/device/duckdb/view_store_test.go
git commit -m "perf(storage): upsert duckdb view rows through staging"
```

---

### Task 5: Add Periodic Time-Series View Catch-Up

**Files:**
- Create: `modules/storage/internal/services/view/builder/catchup.go`
- Create: `modules/storage/internal/services/view/builder/catchup_test.go`
- Modify: `modules/storage/internal/services/view/builder/options.go`
- Modify: `modules/storage/internal/services/view/builder/service.go`

- [ ] **Step 1: Add catch-up interfaces and failing tests**

Create `modules/storage/internal/services/view/builder/catchup_test.go`:

```go
package builder

import (
	"context"
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestCatchUpTimeSeriesViewsScansFromViewMaxMinusLookback(t *testing.T) {
	ctx := context.Background()
	meta := &catchupMetadata{
		spaces: []*pb.Space{{SpaceId: "crypto"}},
		views: []*pb.View{{
			SpaceId:          "crypto",
			ViewId:           "spot_kline_1m_view",
			PrimaryDatasetId: "binance_spot_kline",
			DatasetIds:       []string{"binance_spot_kline"},
			Engine:           "duckdb",
			Status:           "active",
			BuildStatus:      "active",
			ActiveResult:     "view_result",
		}},
		columns: []*pb.ViewColumn{{ColumnName: "close", OriginId: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	reader := &catchupReader{
		rows: []*pb.TimeSeriesRow{
			{Key: testBuilderTimeSeriesKey("BTC-USDT", "2026-07-08T06:14:00Z"), Columns: []*pb.ColumnValue{{
				ColumnName: "close",
				ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1}},
			}}},
		},
	}
	views := &catchupViewStore{maxDataTime: "2026-07-08T06:13:00.000000000Z"}
	service := NewService(Options{
		Reader:          reader,
		Metadata:        meta,
		Views:           views,
		CatchupLookback: 30 * time.Minute,
		CatchupPageSize: 5000,
	})

	if err := service.catchUpOnce(ctx); err != nil {
		t.Fatalf("catchUpOnce: %v", err)
	}
	if reader.lastRange.GetStartTime() != "2026-07-08T05:43:00.000000000Z" {
		t.Fatalf("start_time = %q, want 2026-07-08T05:43:00.000000000Z", reader.lastRange.GetStartTime())
	}
	if len(views.inserted) != 1 {
		t.Fatalf("inserted rows = %d, want 1", len(views.inserted))
	}
}
```

Add fake structs in the same test file:

```go
type catchupMetadata struct {
	spaces  []*pb.Space
	views   []*pb.View
	columns []*pb.ViewColumn
}

func (m *catchupMetadata) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return cloneSpaces(m.spaces), &pb.PageResult{}, nil
}

func (m *catchupMetadata) ListViews(context.Context, string, string, string, *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	return cloneViews(m.views), &pb.PageResult{}, nil
}

func (m *catchupMetadata) ListViewsByDataset(context.Context, string, string) ([]*pb.View, error) {
	return cloneViews(m.views), nil
}

func (m *catchupMetadata) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return cloneViewColumns(m.columns), &pb.PageResult{}, nil
}

func (m *catchupMetadata) GetView(context.Context, string, string) (*pb.View, error) { return nil, nil }
func (m *catchupMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}, nil
}
func (m *catchupMetadata) UpsertView(context.Context, *pb.View) (*pb.View, error) { return nil, nil }
func (m *catchupMetadata) BeginViewBuild(context.Context, string, string, uint64, string) (*pb.View, error) {
	return nil, nil
}
func (m *catchupMetadata) CompleteViewBuild(context.Context, string, string, uint64, string) error {
	return nil
}
func (m *catchupMetadata) FailViewBuild(context.Context, string, string, uint64, string, error) error {
	return nil
}

type catchupReader struct {
	rows      []*pb.TimeSeriesRow
	lastRange *pb.TimeRange
}

func (r *catchupReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (r *catchupReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}
func (r *catchupReader) ScanTimeSeriesRows(_ context.Context, _ string, _ string, tr *pb.TimeRange, _ []string, _ *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	r.lastRange = proto.Clone(tr).(*pb.TimeRange)
	return r.rows, &pb.PageResult{HasMore: false}, nil
}
func (r *catchupReader) ScanRecordRows(context.Context, string, string, *pb.VersionRange, []string, *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

type catchupViewStore struct {
	maxDataTime string
	inserted    []*pb.TimeSeriesRow
}

func (s *catchupViewStore) InsertRows(_ context.Context, _ string, rows []*pb.TimeSeriesRow) error {
	for _, row := range rows {
		s.inserted = append(s.inserted, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return nil
}

func (s *catchupViewStore) MaxDataTime(context.Context, string) (string, error) {
	return s.maxDataTime, nil
}

func cloneSpaces(items []*pb.Space) []*pb.Space {
	out := make([]*pb.Space, 0, len(items))
	for _, item := range items {
		out = append(out, proto.Clone(item).(*pb.Space))
	}
	return out
}
```

- [ ] **Step 2: Run catch-up test and verify it fails**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -run TestCatchUpTimeSeriesViewsScansFromViewMaxMinusLookback -count=1
```

Expected: compile failure because `catchUpOnce` and `MaxDataTime` interface support do not exist.

- [ ] **Step 3: Add catch-up interfaces and implementation**

Create `modules/storage/internal/services/view/builder/catchup.go`:

```go
package builder

import (
	"context"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type timeSeriesViewMaxReader interface {
	MaxDataTime(ctx context.Context, tableName string) (string, error)
}

func (s *Service) catchUpOnce(ctx context.Context) error {
	if s == nil || s.metadata == nil || s.reader == nil || s.views == nil {
		return nil
	}
	maxReader, ok := s.views.(timeSeriesViewMaxReader)
	if !ok {
		return nil
	}
	if _, ok := s.reader.(AccessReader); !ok {
		return nil
	}
	spaces, _, err := s.metadata.ListSpaces(ctx, "", &pb.Page{Size: 1000})
	if err != nil {
		return err
	}
	for _, space := range spaces {
		if err := s.catchUpSpace(ctx, maxReader, space.GetSpaceId()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) catchUpSpace(ctx context.Context, maxReader timeSeriesViewMaxReader, spaceID string) error {
	views, _, err := s.metadata.ListViews(ctx, spaceID, "", "active", &pb.Page{Size: 10000})
	if err != nil {
		return err
	}
	for _, item := range views {
		if !strings.EqualFold(item.GetEngine(), "duckdb") || item.GetActiveResult() == "" {
			continue
		}
		if err := s.catchUpView(ctx, maxReader, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) catchUpView(ctx context.Context, maxReader timeSeriesViewMaxReader, item *pb.View) error {
	maxTime, err := maxReader.MaxDataTime(ctx, item.GetActiveResult())
	if err != nil || maxTime == "" {
		return err
	}
	normalized, err := factkey.NormalizeTimeVersion(maxTime)
	if err != nil {
		return err
	}
	start, err := time.Parse(time.RFC3339Nano, normalized)
	if err != nil {
		return err
	}
	lookback := s.catchupLookback
	if lookback <= 0 {
		lookback = 30 * time.Minute
	}
	accessReader, ok := s.reader.(AccessReader)
	if !ok {
		return nil
	}
	timeRange := &pb.TimeRange{StartTime: start.Add(-lookback).UTC().Format(factkey.TimeVersionLayout)}
	columns, _, err := s.metadataCache.listViewColumns(ctx, item.GetSpaceId(), item.GetViewId())
	if err != nil {
		return err
	}
	pageSize := s.catchupPageSize
	if pageSize == 0 {
		pageSize = 5000
	}
	for pageNo := uint32(1); ; pageNo++ {
		rows, page, err := accessReader.ScanTimeSeriesRows(ctx, item.GetSpaceId(), item.GetPrimaryDatasetId(), timeRange, nil, &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return err
		}
		mapped, ok, err := viewsvc.TimeSeriesRowsForView(ctx, item, columns, rows, s.readTimeSeriesProjectionRow)
		if err != nil {
			return err
		}
		if ok && len(mapped) > 0 {
			if err := s.views.InsertRows(ctx, item.GetActiveResult(), mapped); err != nil {
				return err
			}
		}
		if page == nil || !page.GetHasMore() {
			return nil
		}
	}
}
```

- [ ] **Step 4: Start and stop catch-up loop**

In `Service`, add fields:

```go
catchupEnabled  bool
catchupInterval time.Duration
catchupLookback time.Duration
catchupPageSize uint32
```

In `Start`, after subscriptions are created, start a goroutine only when `catchupEnabled` is true:

```go
if s.catchupEnabled {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runCatchupLoop(runCtx)
	}()
}
```

Add:

```go
func (s *Service) runCatchupLoop(ctx context.Context) {
	interval := s.catchupInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.catchUpOnce(ctx); err != nil {
				// Use tRPC log package already used in this module.
				log.ErrorContextf(ctx, "[ViewBuilder] catch-up failed: %v", err)
			}
		}
	}
}
```

Import `trpc.group/trpc-go/trpc-go/log` in `service.go` if not already imported.

- [ ] **Step 5: Run catch-up and builder tests**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -run 'TestCatchUp|TestMetadataCache|TestCurrentTimeSeriesRows' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/services/view/builder/catchup.go modules/storage/internal/services/view/builder/catchup_test.go modules/storage/internal/services/view/builder/service.go modules/storage/internal/services/view/builder/options.go
git commit -m "feat(storage): add time series view catch-up"
```

---

### Task 6: Add Observability For Lag And Backlog

**Files:**
- Modify: `modules/storage/internal/services/view/builder/service.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`
- Modify: `modules/storage/internal/services/view/builder/catchup.go`

- [ ] **Step 1: Add concise info logs**

Add a single info log after each processed time-series batch:

```go
log.InfoContextf(ctx,
	"[ViewBuilder] time-series batch projected input_keys=%d deduped_keys=%d rows=%d datasets=%d",
	len(keys), len(queryKeys), len(rows), len(grouped),
)
```

Add a catch-up completion log:

```go
log.InfoContextf(ctx,
	"[ViewBuilder] catch-up view=%s/%s start_time=%s rows=%d",
	item.GetSpaceId(), item.GetViewId(), timeRange.GetStartTime(), totalRows,
)
```

Keep these logs at `INFO` only once per batch or catch-up view; do not log per row.

- [ ] **Step 2: Run package tests**

Run:

```bash
go test ./modules/storage/internal/services/view/builder -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add modules/storage/internal/services/view/builder/service.go modules/storage/internal/services/view/builder/time_series.go modules/storage/internal/services/view/builder/catchup.go
git commit -m "chore(storage): log view materialization progress"
```

---

### Task 7: Document The Materialization Model

**Files:**
- Modify: `docs/存储模块设计.md`

- [ ] **Step 1: Add storage documentation section**

Add this section to `docs/存储模块设计.md` near the view materialization section:

```markdown
## TimeSeries View 物化延迟控制

TimeSeries View 是 DuckDB 读模型，PrimaryStore 仍然是事实数据源。写入事实数据后，Storage 通过 NATS `moox.storage.time_series.rows_changed.v1` 事件触发增量物化；增量链路会先对事件 key 去重，再批量回读事实行，按 View 定义投影后批量 upsert 到 DuckDB 结果表。

为避免服务重启、NATS 积压或单次事件处理失败导致 View 长时间落后，View Builder 还会周期性执行 catch-up：读取每个 active DuckDB View 当前最大 `data_time`，从 `max(data_time) - catchup_lookback` 开始扫描 PrimaryStore，并重新 upsert 到 active result。Catch-up 不修改事实数据，只修复可重建的读模型。

核心配置位于 `modules/storage/config/storage.yaml` 的 `storage.view`：

- `read_batch_size`: 单次批量回读事实 key 数量，默认 `1000`
- `metadata_cache_ttl_ms`: View 元数据短缓存 TTL，默认 `60000`
- `catchup_enabled`: 是否启用周期追平，默认 `true`
- `catchup_interval_ms`: 追平执行间隔，默认 `60000`
- `catchup_lookback_minutes`: 从 View 最大时间向前回看的窗口，默认 `30`
- `catchup_page_size`: 追平扫描 PrimaryStore 的分页大小，默认 `5000`
```

- [ ] **Step 2: Commit docs**

```bash
git add docs/存储模块设计.md
git commit -m "docs(storage): describe view materialization catch-up"
```

---

### Task 8: Full Test, Deploy, And Remote Verification

**Files:**
- No source files are modified in this task.

- [ ] **Step 1: Run storage unit tests**

Run:

```bash
go test ./modules/storage/internal/config ./modules/storage/internal/services/view/builder ./modules/storage/internal/services/view ./modules/storage/internal/services/access -count=1
```

Expected: PASS.

- [ ] **Step 2: Run DuckDB CGO tests**

Run:

```bash
go test ./modules/storage/internal/infra/device/duckdb -count=1
```

Expected: PASS. If this fails because local DuckDB CGO dependencies are missing, record the exact linker error and run the same package test on the remote Linux host after deploy.

- [ ] **Step 3: Build and deploy storage to remote**

Run:

```bash
scripts/deploy-moox.sh \
  --target ubuntu@106.53.107.122 \
  --dir /home/ubuntu/moox/prod \
  --goos linux \
  --goarch amd64 \
  --no-web-host \
  --no-cloudnode \
  --no-collector \
  --no-factor \
  --reuse-web-assets
```

Expected:

- `moox-storage` is rebuilt for Linux amd64.
- Existing `/home/ubuntu/moox/prod/data` is preserved.
- Storage service restarts successfully.

- [ ] **Step 4: Check service status**

Run:

```bash
ssh ubuntu@106.53.107.122 'MOOX_WITH_STORAGE=1 /home/ubuntu/moox/prod/status.sh'
```

Expected: `storage` is running and listening on the existing storage ports.

- [ ] **Step 5: Watch NATS consumer backlog**

Open a local tunnel:

```bash
ssh -N -L 14222:127.0.0.1:4222 ubuntu@106.53.107.122
```

In another terminal, run:

```bash
go run github.com/nats-io/natscli/nats@v0.3.0 \
  --server nats://127.0.0.1:14222 \
  consumer info MOOX_STORAGE storage_view_time_series_rows_changed_v1
```

Expected after several minutes:

- `Acknowledgment Floor` advances steadily.
- `Unprocessed Messages` trends downward.
- `Outstanding Acks` does not remain pinned at `1000 / 1000` for long periods.

- [ ] **Step 6: Verify raw latest and view latest converge**

Use the existing admin login flow and compare:

- Raw API: `/api/admin/storage_access/ReadTimeSeriesRows`
- View API: `/api/admin/storage_view/QueryTimeSeriesRows`

Query `BTC-USDT` with:

```json
{
  "space_id": "crypto",
  "dataset_id": "binance_spot_kline",
  "subject_id": "BTC-USDT",
  "freq": "1m"
}
```

Expected:

- Raw latest remains current.
- `spot_kline_1m_view` latest catches up to within 2-3 minutes of raw latest after catch-up has run.
- The gap stays bounded during at least 10 minutes of observation.

- [ ] **Step 7: Verify frontend symptom**

Open:

```text
http://106.53.107.122:9527/#/collector/views?tab=browse
```

Expected:

- `现货1分钟K线视图` shows rows newer than the previous stuck time `2026-07-08T06:13:00.000000000Z`.
- Sorting by `data_time` descending shows continuously advancing K-line rows.

- [ ] **Step 8: Commit final verification note**

If verification requires a doc note, append a short dated verification section to this plan:

```markdown
## Verification Notes

- Remote host: `106.53.107.122`
- Raw latest before deploy: `<observed raw latest>`
- View latest after deploy: `<observed view latest>`
- NATS backlog after 10 minutes: `<observed consumer state>`
```

Then commit:

```bash
git add docs/superpowers/plans/2026-07-08-storage-view-materialization-lag.md
git commit -m "docs(storage): record view lag verification"
```

---

## Rollback Plan

If the deployed storage process fails to start, redeploy the previous commit with the same deploy command and without `--reset-data`.

If storage starts but DuckDB write errors appear, disable catch-up first by setting:

```yaml
storage:
  view:
    catchup_enabled: false
```

Then restart storage. This keeps the event-driven incremental path running while isolating catch-up.

If NATS backlog grows after the optimized code is deployed, stop catch-up, capture `storage/trpc.log`, and inspect whether time is spent in `ScanTimeSeriesRows`, `TimeSeriesRowsForView`, or `InsertRows`. Do not increase `MaxAckPending` until the slow stage is identified.

## Success Criteria

- `storage_view_time_series_rows_changed_v1` no longer remains pinned at `Outstanding Acks: 1000 / 1000`.
- For `BTC-USDT`, raw latest and `spot_kline_1m_view` latest differ by no more than 2-3 minutes under normal load.
- `QueryTimeSeriesRows` on `spot_kline_1m_view` returns rows newer than `2026-07-08T06:13:00.000000000Z`.
- Existing DuckDB partial merge behavior remains intact.
- All focused storage tests pass locally or on the remote Linux host.
