# Storage View Index Blue-Green Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Unify TimeSeries DuckDB Views and Record/Bleve Views under one bounded, blue-green View index lifecycle so PrimaryStore remains the only complete fact store and all derived View indexes can rotate, rebuild, and switch safely.

**Architecture:** Introduce a single `ViewIndexEngine` abstraction with `Prepare`, `Write`, `Stat`, and `Remove` methods. View metadata continues to own the read pointer through `active_result` and the warming pointer through `building_result`; the rotation manager decides whether to rebuild because the View definition changed or rotate because the active index exceeded capacity. DuckDB and Bleve implement the same lifecycle while keeping their existing query APIs separate.

**Tech Stack:** Go 1.24, tRPC-Go timers, SQLite metadata, Pebble PrimaryStore, DuckDB CGO, Bleve, Storage ViewBuilder, NATS/memory eventbus, `go test`.

---

## Locked Decisions

| Decision | Detail |
|---|---|
| Derived index semantics | DuckDB and Bleve are bounded derived View indexes. PrimaryStore KV/Pebble is the only complete and authoritative fact store. |
| Unified lifecycle | TimeSeries and Record Views both use `active_result` and `building_result` as View index instance IDs. |
| Interface name | Use `ViewIndexEngine`. |
| Physical index identifier | Use `indexID`, not `resultID`, `generation`, or `versionID`. |
| Engine methods | Use `Prepare`, `Write`, `Stat`, `Remove`. Do not use `CreateOrReplace`, `Inspect`, `Retire`, or `GetStats`. |
| Query path | Query APIs stay engine-specific: `QueryTimeSeriesRows` for DuckDB and `SearchRecordRows` for Bleve. The lifecycle interface does not contain a generic `Query` method. |
| Default schema rebuild | Schema/view definition changes are always preemptive. No config switch is needed. If `view_version > active_view_version`, the next timer run rebuilds a new warming index immediately. |
| Warming validity | A warming index is writable only when `building_result != ""` and `building_view_version == view_version`. Otherwise it is obsolete and must not receive new writes. |
| Active schema | New fields become queryable only after the new warming index switches to active. Until then, reads continue to use `active_result` and `active_view_version`. |
| Obsolete warming | If a ViewColumn or View definition changes while a warming index is building, clear `building_result`, stop writing to the old warming index, and remove it asynchronously. |
| Backfill source | Never copy from the old DuckDB table or old Bleve index. Warming indexes are backfilled from PrimaryStore via Access/FactReader so rows are complete. |
| Backfill window | `overlap_window=30m` is only late-arrival/disorder protection. TimeSeries backfill also considers `query_window` and `freq_backfill_window`; Record backfill uses `version_window` semantics. |
| Cleanup | Remove obsolete/old indexes as complete physical objects. Do not rely on periodic row/document deletes to keep derived indexes healthy. |
| Latest helper | Remove the DuckDB `__latest` helper table path. Bounded active indexes make that special path unnecessary. |

## Target Interface

Create the common lifecycle interface in `modules/storage/internal/services/view/index_engine.go`:

```go
type ViewIndexEngine interface {
	Engine() string
	Prepare(ctx context.Context, indexID string, schema ViewIndexSchema) error
	Write(ctx context.Context, indexID string, batch ViewIndexBatch) error
	Stat(ctx context.Context, indexID string) (ViewIndexStats, error)
	Remove(ctx context.Context, indexID string) error
}

type ViewIndexSchema struct {
	SpaceID     string
	ViewID      string
	ViewVersion uint64
	Engine      string
	Columns     []*pb.ViewColumn
	SchemaHash  string
}

type ViewIndexBatch struct {
	TimeSeriesRows []*pb.TimeSeriesRow
	RecordRows     []*pb.RecordRow
	Columns        []*pb.ViewColumn
}

type ViewIndexStats struct {
	Exists     bool
	EntryCount int64
	MinVersion string
	MaxVersion string
	SchemaHash string
}
```

Mapping:

- DuckDB `indexID` is the physical result table name.
- Bleve `indexID` is the physical index directory name.
- `EntryCount` means row count for DuckDB and document count for Bleve.
- `MinVersion` / `MaxVersion` means `data_time` for DuckDB and record `version` for Bleve.
- `SchemaHash` is computed from View definition and ViewColumn shape, not from user display-only text.

## Target Configuration

Replace the DuckDB-specific rotation config with generic View index rotation config:

```yaml
storage:
  view:
    batch_size: 500
    batch_wait_ms: 200
    max_workers: 2
    rotation:
      enabled: true
      max_entries: 200000
      min_ready_entries: 50000
      overlap_window: 30m
      default_backfill_window: 1d
      allowed_lag: 2m
      remove_grace_ms: 60000
      time_series:
        freq_backfill_window:
          1m: 6h
          1h: 30d
          1d: 730d
      record:
        default_version_window: 30d
        max_backfill_entries: 200000
```

Interpretation:

- `max_entries`: capacity threshold for active indexes.
- `min_ready_entries`: minimum warming index entry count before switch.
- `overlap_window`: late-arrival/disorder safety buffer.
- `default_backfill_window`: fallback backfill window.
- `allowed_lag`: warming `MaxVersion` may lag active/latest by at most this duration when versions are parseable timestamps.
- `remove_grace_ms`: delay before removing old active or obsolete warming indexes.
- `time_series.freq_backfill_window`: frequency-aware minimum windows for K-line Views.
- `record.default_version_window`: default Record View version range when versions are timestamp-like.
- `record.max_backfill_entries`: safety cap for Record View backfill pages.

No `rebuild_on_schema_change` config exists. Schema changes always rebuild preemptively.

## File Structure

### New files

- `modules/storage/internal/services/view/index_engine.go`  
  Defines `ViewIndexEngine`, `ViewIndexSchema`, `ViewIndexBatch`, `ViewIndexStats`, schema hash helpers, and index ID helpers.
- `modules/storage/internal/services/view/index_engine_test.go`  
  Tests schema hash stability, index ID naming, and active/building version guards.
- `modules/storage/internal/services/view/rotation.go`  
  Unified rotation manager for DuckDB and Bleve View indexes.
- `modules/storage/internal/services/view/rotation_test.go`  
  Tests schema-preemptive rebuild, capacity rotation, stale warming cleanup, and switch readiness.

### Modified files

- `modules/storage/internal/config/loader.go`  
  Add generic `StorageViewRotation` config and defaults.
- `modules/storage/internal/config/loader_test.go`  
  Cover generic rotation defaults and YAML overrides.
- `modules/storage/config/storage.yaml`  
  Add `storage.view.rotation`.
- `modules/storage/config/storage.view_builder.yaml`  
  Add the same defaults for independent view-builder deployment.
- `modules/storage/internal/infra/device/duckdb/view_store.go`  
  Implement DuckDB index lifecycle primitives and remove latest-helper behavior.
- `modules/storage/internal/infra/device/duckdb/view_store_nocgo.go`  
  Keep no-cgo API parity.
- `modules/storage/internal/infra/device/duckdb/view_store_test.go`  
  Cover `Prepare`, `Write`, `Stat`, `Remove`, and no `__latest`.
- `modules/storage/internal/services/view/search/service.go`  
  Implement Bleve index lifecycle primitives.
- `modules/storage/internal/infra/device/bleve/index.go`  
  Add document count/version stats and remove-index support if missing.
- `modules/storage/internal/services/view/search/service_test.go`  
  Cover Bleve `Prepare`, `Write`, `Stat`, and `Remove`.
- `modules/storage/internal/infra/metadata/sqlite/crud.go`  
  Preserve `active_result`; clear obsolete `building_result` on View shape changes; keep compare-and-switch semantics.
- `modules/storage/internal/infra/metadata/sqlite/crud_test.go`  
  Cover ViewColumn changes bump version and clear building pointers.
- `modules/storage/internal/services/view/view_builder.go`  
  Use deterministic index IDs for both DuckDB and Bleve builds and stream backfill pages.
- `modules/storage/internal/services/view/schedule.go`  
  Add timer operation `op=rotate`.
- `modules/storage/internal/services/view/builder/time_series.go`  
  Write active plus valid building index only.
- `modules/storage/internal/services/view/builder/record.go`  
  Write active plus valid building index only.
- `modules/storage/internal/services/view/service.go`  
  Validate requested columns against active schema/version.
- `modules/storage/cmd/server/main.go`  
  Wire rotation config and register the rotate timer.
- `docs/存储目标架构与元数据.md`  
  Document unified View index semantics.
- `docs/存储服务架构与部署.md`  
  Document timer and config.
- `modules/storage/README.md`  
  Update operational notes.

---

### Task 1: Add Generic View Rotation Config

**Files:**
- Modify: `modules/storage/internal/config/loader.go`
- Modify: `modules/storage/internal/config/loader_test.go`
- Modify: `modules/storage/config/storage.yaml`
- Modify: `modules/storage/config/storage.view_builder.yaml`

- [ ] **Step 1: Write failing config tests**

Add to `modules/storage/internal/config/loader_test.go`:

```go
func TestStorageViewRotationDefaults(t *testing.T) {
	var cfg StorageConfig
	cfg.ApplyDefaults()

	rotation := cfg.View.Rotation
	if !rotation.Enabled {
		t.Fatal("rotation enabled = false, want true")
	}
	if rotation.MaxEntries != 200000 {
		t.Fatalf("max entries = %d, want 200000", rotation.MaxEntries)
	}
	if rotation.MinReadyEntries != 50000 {
		t.Fatalf("min ready entries = %d, want 50000", rotation.MinReadyEntries)
	}
	if rotation.OverlapWindow != "30m" {
		t.Fatalf("overlap window = %q, want 30m", rotation.OverlapWindow)
	}
	if rotation.TimeSeries.FreqBackfillWindow["1d"] != "730d" {
		t.Fatalf("1d backfill window = %q, want 730d", rotation.TimeSeries.FreqBackfillWindow["1d"])
	}
	if rotation.Record.DefaultVersionWindow != "30d" {
		t.Fatalf("record version window = %q, want 30d", rotation.Record.DefaultVersionWindow)
	}
}

func TestStorageViewRotationYAMLOverrides(t *testing.T) {
	raw := []byte(`
storage:
  view:
    rotation:
      enabled: true
      max_entries: 300000
      min_ready_entries: 80000
      overlap_window: 45m
      default_backfill_window: 2d
      allowed_lag: 5m
      remove_grace_ms: 120000
      time_series:
        freq_backfill_window:
          1h: 60d
          1d: 1095d
      record:
        default_version_window: 45d
        max_backfill_entries: 300000
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()

	rotation := cfg.Storage.View.Rotation
	if rotation.MaxEntries != 300000 {
		t.Fatalf("max entries = %d, want 300000", rotation.MaxEntries)
	}
	if rotation.TimeSeries.FreqBackfillWindow["1h"] != "60d" {
		t.Fatalf("1h window = %q, want 60d", rotation.TimeSeries.FreqBackfillWindow["1h"])
	}
	if rotation.Record.DefaultVersionWindow != "45d" {
		t.Fatalf("record version window = %q, want 45d", rotation.Record.DefaultVersionWindow)
	}
}
```

- [ ] **Step 2: Run focused config tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/config -run 'TestStorageViewRotation'
```

Expected: fails because `StorageView.Rotation` does not exist or still has DuckDB-only field names.

- [ ] **Step 3: Add config structs and defaults**

In `modules/storage/internal/config/loader.go`, define:

```go
type StorageView struct {
	MetadataServiceName string              `yaml:"metadata_service_name"`
	AccessServiceName   string              `yaml:"access_service_name"`
	BatchSize           int                 `yaml:"batch_size"`
	BatchWaitMS         int                 `yaml:"batch_wait_ms"`
	MaxWorkers          int                 `yaml:"max_workers"`
	Rotation            StorageViewRotation `yaml:"rotation"`
}

type StorageViewRotation struct {
	Enabled               bool                          `yaml:"enabled"`
	MaxEntries            int                           `yaml:"max_entries"`
	MinReadyEntries       int                           `yaml:"min_ready_entries"`
	OverlapWindow         string                        `yaml:"overlap_window"`
	DefaultBackfillWindow string                        `yaml:"default_backfill_window"`
	AllowedLag            string                        `yaml:"allowed_lag"`
	RemoveGraceMS         int                           `yaml:"remove_grace_ms"`
	TimeSeries            StorageTimeSeriesViewRotation `yaml:"time_series"`
	Record                StorageRecordViewRotation     `yaml:"record"`
}

type StorageTimeSeriesViewRotation struct {
	FreqBackfillWindow map[string]string `yaml:"freq_backfill_window"`
}

type StorageRecordViewRotation struct {
	DefaultVersionWindow string `yaml:"default_version_window"`
	MaxBackfillEntries   int    `yaml:"max_backfill_entries"`
}
```

Add defaults inside `StorageConfig.ApplyDefaults()`:

```go
c.View.Rotation.Enabled = true
if c.View.Rotation.MaxEntries <= 0 {
	c.View.Rotation.MaxEntries = 200000
}
if c.View.Rotation.MinReadyEntries <= 0 {
	c.View.Rotation.MinReadyEntries = 50000
}
if c.View.Rotation.OverlapWindow == "" {
	c.View.Rotation.OverlapWindow = "30m"
}
if c.View.Rotation.DefaultBackfillWindow == "" {
	c.View.Rotation.DefaultBackfillWindow = "1d"
}
if c.View.Rotation.AllowedLag == "" {
	c.View.Rotation.AllowedLag = "2m"
}
if c.View.Rotation.RemoveGraceMS <= 0 {
	c.View.Rotation.RemoveGraceMS = 60000
}
if c.View.Rotation.TimeSeries.FreqBackfillWindow == nil {
	c.View.Rotation.TimeSeries.FreqBackfillWindow = map[string]string{}
}
if c.View.Rotation.TimeSeries.FreqBackfillWindow["1m"] == "" {
	c.View.Rotation.TimeSeries.FreqBackfillWindow["1m"] = "6h"
}
if c.View.Rotation.TimeSeries.FreqBackfillWindow["1h"] == "" {
	c.View.Rotation.TimeSeries.FreqBackfillWindow["1h"] = "30d"
}
if c.View.Rotation.TimeSeries.FreqBackfillWindow["1d"] == "" {
	c.View.Rotation.TimeSeries.FreqBackfillWindow["1d"] = "730d"
}
if c.View.Rotation.Record.DefaultVersionWindow == "" {
	c.View.Rotation.Record.DefaultVersionWindow = "30d"
}
if c.View.Rotation.Record.MaxBackfillEntries <= 0 {
	c.View.Rotation.Record.MaxBackfillEntries = 200000
}
```

- [ ] **Step 4: Add YAML defaults**

Add this block under `storage.view` in both config files:

```yaml
    rotation:
      enabled: true
      max_entries: 200000
      min_ready_entries: 50000
      overlap_window: 30m
      default_backfill_window: 1d
      allowed_lag: 2m
      remove_grace_ms: 60000
      time_series:
        freq_backfill_window:
          1m: 6h
          1h: 30d
          1d: 730d
      record:
        default_version_window: 30d
        max_backfill_entries: 200000
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/config
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/config/loader.go modules/storage/internal/config/loader_test.go modules/storage/config/storage.yaml modules/storage/config/storage.view_builder.yaml
git commit -m "feat(storage): add view index rotation config"
```

---

### Task 2: Define ViewIndexEngine And Shared Naming

**Files:**
- Create: `modules/storage/internal/services/view/index_engine.go`
- Create: `modules/storage/internal/services/view/index_engine_test.go`

- [ ] **Step 1: Write failing interface helper tests**

Create `index_engine_test.go`:

```go
package view

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestViewIndexIDAlternatesBetweenSlots(t *testing.T) {
	slotA := ViewIndexID("crypto", "spot_kline_1m_view", "a")
	if slotA != "view_crypto_spot_kline_1m_view_a" {
		t.Fatalf("slot a = %q", slotA)
	}
	if got := InactiveViewIndexID("crypto", "spot_kline_1m_view", slotA); got != "view_crypto_spot_kline_1m_view_b" {
		t.Fatalf("inactive slot = %q", got)
	}
}

func TestViewIndexSchemaHashChangesWhenColumnAdded(t *testing.T) {
	base := ViewIndexSchema{
		SpaceID: "crypto", ViewID: "spot", ViewVersion: 1, Engine: "duckdb",
		Columns: []*pb.ViewColumn{{ColumnName: "close", OriginId: "binance_spot_kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	withVolume := base
	withVolume.Columns = append(withVolume.Columns, &pb.ViewColumn{ColumnName: "volume", OriginId: "binance_spot_kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	if HashViewIndexSchema(base) == HashViewIndexSchema(withVolume) {
		t.Fatal("schema hash did not change after column add")
	}
}

func TestBuildingIndexWritableRequiresCurrentViewVersion(t *testing.T) {
	view := &pb.View{ViewVersion: 3, BuildingViewVersion: 2, BuildingResult: "view_crypto_spot_b"}
	if BuildingIndexWritable(view) {
		t.Fatal("building index writable for stale version, want false")
	}
	view.BuildingViewVersion = 3
	if !BuildingIndexWritable(view) {
		t.Fatal("building index not writable for current version, want true")
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view -run 'TestViewIndex|TestBuildingIndexWritable'
```

Expected: fails because helpers and types do not exist.

- [ ] **Step 3: Implement common types and helpers**

Create `index_engine.go` with:

```go
package view

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type ViewIndexEngine interface {
	Engine() string
	Prepare(ctx context.Context, indexID string, schema ViewIndexSchema) error
	Write(ctx context.Context, indexID string, batch ViewIndexBatch) error
	Stat(ctx context.Context, indexID string) (ViewIndexStats, error)
	Remove(ctx context.Context, indexID string) error
}

type ViewIndexSchema struct {
	SpaceID     string
	ViewID      string
	ViewVersion uint64
	Engine      string
	Columns     []*pb.ViewColumn
	SchemaHash  string
}

type ViewIndexBatch struct {
	TimeSeriesRows []*pb.TimeSeriesRow
	RecordRows     []*pb.RecordRow
	Columns        []*pb.ViewColumn
}

type ViewIndexStats struct {
	Exists     bool
	EntryCount int64
	MinVersion string
	MaxVersion string
	SchemaHash string
}

func ViewIndexID(spaceID string, viewID string, slot string) string {
	name := sanitizeResultTableName(fmt.Sprintf("view_%s_%s_%s", spaceID, viewID, slot))
	if name == "" {
		return "view_result_" + slot
	}
	return name
}

func InactiveViewIndexID(spaceID string, viewID string, activeIndexID string) string {
	slotA := ViewIndexID(spaceID, viewID, "a")
	slotB := ViewIndexID(spaceID, viewID, "b")
	if activeIndexID == slotA {
		return slotB
	}
	return slotA
}

func BuildingIndexWritable(view *pb.View) bool {
	return view != nil && view.GetBuildingResult() != "" && view.GetBuildingViewVersion() == view.GetViewVersion()
}

func HashViewIndexSchema(schema ViewIndexSchema) string {
	type columnShape struct {
		Name       string `json:"name"`
		OriginID   string `json:"origin_id"`
		OriginType int32  `json:"origin_type"`
		ValueType  int32  `json:"value_type"`
		SortOrder  uint32 `json:"sort_order"`
	}
	shape := struct {
		SpaceID     string        `json:"space_id"`
		ViewID      string        `json:"view_id"`
		ViewVersion uint64        `json:"view_version"`
		Engine      string        `json:"engine"`
		Columns     []columnShape `json:"columns"`
	}{SpaceID: schema.SpaceID, ViewID: schema.ViewID, ViewVersion: schema.ViewVersion, Engine: schema.Engine}
	for _, column := range schema.Columns {
		shape.Columns = append(shape.Columns, columnShape{
			Name: column.GetColumnName(), OriginID: column.GetOriginId(),
			OriginType: int32(column.GetOriginType()), ValueType: int32(column.GetValueType()),
			SortOrder: column.GetSortOrder(),
		})
	}
	raw, _ := json.Marshal(shape)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view -run 'TestViewIndex|TestBuildingIndexWritable'
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add modules/storage/internal/services/view/index_engine.go modules/storage/internal/services/view/index_engine_test.go
git commit -m "feat(storage): define view index engine abstraction"
```

---

### Task 3: Make Schema Changes Preemptive And Obsolete Warming Safe

**Files:**
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud.go`
- Modify: `modules/storage/internal/infra/metadata/sqlite/crud_test.go`
- Modify: `modules/storage/internal/services/view/builder/time_series.go`
- Modify: `modules/storage/internal/services/view/builder/record.go`
- Modify: `modules/storage/internal/services/view/builder/time_series_test.go`
- Modify: `modules/storage/internal/services/view/builder/record_test.go`

- [ ] **Step 1: Add metadata test for schema change clearing building result**

Add to `crud_test.go`:

```go
func TestUpsertViewColumnBumpsVersionAndClearsBuildingIndex(t *testing.T) {
	store := newTestMetadataStore(t)
	ctx := context.Background()
	view := testView("crypto", "spot_kline_1m_view")
	view.ViewVersion = 1
	view.ActiveViewVersion = 1
	view.ActiveResult = "view_crypto_spot_kline_1m_view_a"
	view.BuildingViewVersion = 1
	view.BuildingResult = "view_crypto_spot_kline_1m_view_b"
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
	_, err := store.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", ColumnName: "volume",
		OriginId: "binance_spot_kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	if err != nil {
		t.Fatalf("UpsertViewColumn: %v", err)
	}
	got, err := store.GetView(ctx, "crypto", "spot_kline_1m_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if got.GetViewVersion() != 2 {
		t.Fatalf("view version = %d, want 2", got.GetViewVersion())
	}
	if got.GetBuildingResult() != "" || got.GetBuildingViewVersion() != 0 {
		t.Fatalf("building pointer = %q/%d, want cleared", got.GetBuildingResult(), got.GetBuildingViewVersion())
	}
	if got.GetActiveResult() != "view_crypto_spot_kline_1m_view_a" {
		t.Fatalf("active result changed to %q", got.GetActiveResult())
	}
}
```

- [ ] **Step 2: Add builder dual-write version guard tests**

Add one test each for TimeSeries and Record processors. Expected behavior:

- active index receives writes,
- stale building index with `building_view_version != view_version` receives no writes,
- current building index receives writes.

The TimeSeries test should set `ViewVersion=2`, `ActiveResult=a`, `BuildingResult=b`, `BuildingViewVersion=1` and assert only `a` was written.

The Record test should use the same shape for a Bleve View.

- [ ] **Step 3: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/infra/metadata/sqlite ./internal/services/view/builder -run 'TestUpsertViewColumnBumpsVersionAndClearsBuildingIndex|Test.*Building.*Version'
```

Expected: metadata may already pass if existing behavior is correct; builder tests fail until write guards are added.

- [ ] **Step 4: Guard building writes**

In both `time_series.go` and `record.go`, write active unconditionally when non-empty, and write building only through `viewsvc.BuildingIndexWritable(item)`:

```go
if item.GetActiveResult() != "" {
	if err := s.views.InsertRows(ctx, item.GetActiveResult(), mapped); err != nil {
		return err
	}
}
if viewsvc.BuildingIndexWritable(item) {
	if err := s.views.InsertRows(ctx, item.GetBuildingResult(), mapped); err != nil {
		return err
	}
}
```

For Record/Bleve use `s.search.IndexRecordViewRows` with the same guard.

- [ ] **Step 5: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/infra/metadata/sqlite ./internal/services/view/builder
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/infra/metadata/sqlite/crud.go modules/storage/internal/infra/metadata/sqlite/crud_test.go modules/storage/internal/services/view/builder/time_series.go modules/storage/internal/services/view/builder/record.go modules/storage/internal/services/view/builder/time_series_test.go modules/storage/internal/services/view/builder/record_test.go
git commit -m "fix(storage): stop writing stale warming view indexes"
```

---

### Task 4: Implement DuckDB ViewIndexEngine

**Files:**
- Modify: `modules/storage/internal/infra/device/duckdb/view_store.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_nocgo.go`
- Modify: `modules/storage/internal/infra/device/duckdb/view_store_test.go`

- [ ] **Step 1: Add failing DuckDB engine tests**

Add tests covering:

- `Prepare` creates an empty table with the requested schema.
- `Write` upserts TimeSeries rows.
- `Stat` returns `EntryCount`, `MinVersion`, and `MaxVersion`.
- `Remove` drops the table and column metadata.
- latest-preview queries do not create `__latest`.

Use existing `duckDBTestRow` and `duckDBTestValue` helpers.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./internal/infra/device/duckdb -run 'TestDuckDBViewIndex|TestLatestPreviewDoesNotCreateLatestHelper'
```

Expected: fail until lifecycle methods exist and latest helper path is removed.

- [ ] **Step 3: Implement lifecycle methods**

Add methods to `ViewStore`:

```go
func (s *ViewStore) Engine() string { return "duckdb" }

func (s *ViewStore) Prepare(ctx context.Context, indexID string, schema viewsvc.ViewIndexSchema) error {
	if err := s.DropResultTable(ctx, indexID); err != nil {
		return err
	}
	return s.CreateResultTable(ctx, indexID, schema.Columns)
}

func (s *ViewStore) Write(ctx context.Context, indexID string, batch viewsvc.ViewIndexBatch) error {
	return s.InsertRows(ctx, indexID, batch.TimeSeriesRows)
}

func (s *ViewStore) Stat(ctx context.Context, indexID string) (viewsvc.ViewIndexStats, error) {
	stats, err := s.resultTableStats(ctx, indexID)
	if err != nil {
		return viewsvc.ViewIndexStats{}, err
	}
	return viewsvc.ViewIndexStats{
		Exists: true, EntryCount: int64(stats.RowCount),
		MinVersion: stats.MinDataTime, MaxVersion: stats.MaxDataTime,
	}, nil
}

func (s *ViewStore) Remove(ctx context.Context, indexID string) error {
	return s.DropResultTable(ctx, indexID)
}
```

Use package names that avoid import cycles. If `duckdb` cannot import `services/view`, move common `ViewIndexEngine` types to `modules/storage/internal/core/viewindex`.

- [ ] **Step 4: Remove latest helper code**

Remove the `__latest` helper path:

- `latestResultTableSuffix`
- `latestHelperMaxRows`
- `queryLatestTimeSeriesRows`
- `ensureLatestResultTable`
- `syncLatestRowsLocked`
- `latestHelperCandidate`
- helper stats/prune functions only used by latest helper

`QueryTimeSeriesRows` should query the active bounded table directly.

- [ ] **Step 5: Update no-cgo stub**

Add no-cgo lifecycle methods returning the existing DuckDB-disabled error.

- [ ] **Step 6: Run DuckDB tests**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./internal/infra/device/duckdb
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add modules/storage/internal/infra/device/duckdb/view_store.go modules/storage/internal/infra/device/duckdb/view_store_nocgo.go modules/storage/internal/infra/device/duckdb/view_store_test.go
git commit -m "refactor(storage): implement duckdb view index engine"
```

---

### Task 5: Implement Bleve ViewIndexEngine

**Files:**
- Modify: `modules/storage/internal/services/view/search/service.go`
- Modify: `modules/storage/internal/services/view/search/service_test.go`
- Modify: `modules/storage/internal/infra/device/bleve/index.go`
- Modify: `modules/storage/internal/infra/device/bleve/index_test.go`

- [ ] **Step 1: Add failing Bleve engine tests**

Add tests covering:

- `Prepare` removes any existing index directory and creates an empty index.
- `Write` indexes Record rows.
- `Stat` returns document count plus min/max record version.
- `Remove` deletes the index directory and closes any open handle.

Use Record rows with versions `2026-07-09T01:00:00Z` and `2026-07-09T01:01:00Z`.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view/search ./internal/infra/device/bleve -run 'TestBleveViewIndex'
```

Expected: fail until lifecycle and stats methods exist.

- [ ] **Step 3: Add Bleve lifecycle methods**

In `search.Service`, add:

```go
func (s *Service) Engine() string { return "bleve" }

func (s *Service) Prepare(ctx context.Context, indexID string, schema viewsvc.ViewIndexSchema) error {
	if err := s.Remove(ctx, indexID); err != nil {
		return err
	}
	_, err := s.searchIndex(indexID, true)
	return err
}

func (s *Service) Write(ctx context.Context, indexID string, batch viewsvc.ViewIndexBatch) error {
	return s.IndexRecordViewRows(ctx, indexID, batch.Columns, batch.RecordRows)
}

func (s *Service) Stat(ctx context.Context, indexID string) (viewsvc.ViewIndexStats, error) {
	index, err := s.searchIndex(indexID, false)
	if err != nil {
		return viewsvc.ViewIndexStats{}, err
	}
	return index.Stat(ctx)
}

func (s *Service) Remove(ctx context.Context, indexID string) error {
	path := s.indexPath(indexID)
	if loaded, ok := s.indexes.LoadAndDelete(path); ok {
		if index, ok := loaded.(*devicebleve.Index); ok {
			if err := index.Close(); err != nil {
				return err
			}
		}
	}
	return os.RemoveAll(path)
}
```

Extract `indexPath(indexID string)` from `searchIndex` so `Remove` and `searchIndex` use the same sanitized path.

- [ ] **Step 4: Add Bleve stats support**

In `infra/device/bleve/index.go`, add a `Stat(ctx)` method. It should return document count and min/max version using Bleve search/facet APIs or an internal metadata document maintained during `IndexRows`. Prefer the simpler internal metadata document if Bleve aggregate APIs are awkward.

- [ ] **Step 5: Run Bleve tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view/search ./internal/infra/device/bleve
```

Expected: pass.

- [ ] **Step 6: Commit**

```bash
git add modules/storage/internal/services/view/search/service.go modules/storage/internal/services/view/search/service_test.go modules/storage/internal/infra/device/bleve/index.go modules/storage/internal/infra/device/bleve/index_test.go
git commit -m "refactor(storage): implement bleve view index engine"
```

---

### Task 6: Add Unified Rotation Manager

**Files:**
- Create: `modules/storage/internal/services/view/rotation.go`
- Create: `modules/storage/internal/services/view/rotation_test.go`
- Modify: `modules/storage/internal/services/view/schedule.go`
- Modify: `modules/storage/cmd/server/main.go`

- [ ] **Step 1: Add failing rotation tests**

Create tests for:

- schema gap triggers rebuild even when active index is below `max_entries`,
- capacity triggers rotation only when `view_version == active_view_version`,
- stale building index is removed and replaced,
- TimeSeries Views use DuckDB engine,
- Record Views use Bleve engine.

Use fake `ViewIndexEngine` implementations that record calls to `Prepare`, `Write`, `Stat`, and `Remove`.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view -run 'TestRotation'
```

Expected: fail until rotation manager exists.

- [ ] **Step 3: Implement rotation manager**

Implement `RotateViewIndexes(ctx, spaceID)` on `Builder` or a small `RotationManager`. The decision order must be:

```text
for each active View:
  if view_version > active_view_version:
      rebuild latest schema now
      continue
  if building_result is stale:
      remove stale building index
      clear building metadata if still present
      continue
  if active Stat.EntryCount > max_entries:
      rotate capacity
      continue
```

The selected inactive index ID is:

```go
indexID := InactiveViewIndexID(view.GetSpaceId(), view.GetViewId(), view.GetActiveResult())
```

- [ ] **Step 4: Add timer operation**

In `HandleSchedule`, add:

```go
case "rotate":
	rotated, err := builder.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		log.ErrorContextf(ctx, "[ViewBuilder] rotate schedule failed: %v", err)
		return err
	}
	log.InfoContextf(ctx, "[ViewBuilder] rotate handled %d view index(es)", rotated)
	return nil
```

- [ ] **Step 5: Wire engines**

In `cmd/server/main.go`, register engine implementations for the View builder:

- DuckDB `ViewStore` for engine `duckdb`
- Bleve search service for engine `bleve`

Do not add engine-specific rotation code to `schedule.go`.

- [ ] **Step 6: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view ./cmd/server
```

Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add modules/storage/internal/services/view/rotation.go modules/storage/internal/services/view/rotation_test.go modules/storage/internal/services/view/schedule.go modules/storage/cmd/server/main.go
git commit -m "feat(storage): add unified view index rotation"
```

---

### Task 7: Backfill Warming Indexes From PrimaryStore

**Files:**
- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/internal/services/view/view_builder_test.go`
- Modify: `modules/storage/internal/services/view/rotation.go`
- Modify: `modules/storage/internal/services/view/rotation_test.go`

- [ ] **Step 1: Add TimeSeries daily-window test**

Add a test proving a TimeSeries View whose Dataset has `Freqs: []string{"1m", "1d"}` backfills the daily window from `freq_backfill_window["1d"]`, not just `overlap_window`.

- [ ] **Step 2: Add Record version-window test**

Add a test proving a Record View backfill uses `VersionRange{StartVersion: now - default_version_window, EndVersion: now}` when record versions are timestamp-like.

- [ ] **Step 3: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view -run 'Test.*Backfill.*Window'
```

Expected: fail until backfill windows are implemented.

- [ ] **Step 4: Implement TimeSeries backfill window**

Effective TimeSeries backfill window:

```text
max(view.query_window, rotation.default_backfill_window, rotation.time_series.freq_backfill_window[freq], rotation.overlap_window)
```

Scan with `ScanTimeSeriesRows`, project rows, and call `engine.Write` per page. Do not accumulate the full window in memory.

- [ ] **Step 5: Implement Record backfill window**

Effective Record backfill uses:

```text
max(view.query_window, rotation.record.default_version_window, rotation.overlap_window)
```

When versions are not parseable as timestamps, cap by `record.max_backfill_entries` and log a warning. Do not try to infer full history from Bleve.

- [ ] **Step 6: Add cancellation guard before each page write**

Before each warming write, reload or re-check metadata:

```text
building_result == current indexID
building_view_version == target view_version
view_version == target view_version
```

If any check fails, stop the backfill without switching.

- [ ] **Step 7: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view
```

Expected: pass.

- [ ] **Step 8: Commit**

```bash
git add modules/storage/internal/services/view/view_builder.go modules/storage/internal/services/view/view_builder_test.go modules/storage/internal/services/view/rotation.go modules/storage/internal/services/view/rotation_test.go
git commit -m "feat(storage): backfill view indexes from primary store"
```

---

### Task 8: Validate Reads Against Active Schema

**Files:**
- Modify: `modules/storage/internal/services/view/service.go`
- Modify: `modules/storage/internal/services/view/service_test.go`

- [ ] **Step 1: Add failing active-schema tests**

Add tests:

- `QueryTimeSeriesRows` requesting a newly added column before switch returns a clear not-ready error.
- `SearchRecordRows` requesting a newly added column before switch returns the same not-ready error.
- Existing active columns remain queryable while a new warming index is building.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view -run 'Test.*ActiveSchema'
```

Expected: fail until read validation uses active schema/version.

- [ ] **Step 3: Implement active schema validation**

When `view.GetViewVersion() > view.GetActiveViewVersion()`, requested `column_names` must be checked against the active schema. If a requested column belongs only to the latest ViewColumn set and not the active index schema, return:

```text
VIEW_NOT_READY: view schema change is building
```

Use existing error code if no dedicated `VIEW_NOT_READY` exists; prefer adding a dedicated enum only if current error model already allows it cleanly.

- [ ] **Step 4: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add modules/storage/internal/services/view/service.go modules/storage/internal/services/view/service_test.go
git commit -m "fix(storage): validate view reads against active schema"
```

---

### Task 9: Update Docs And Protocol Comments

**Files:**
- Modify: `docs/存储目标架构与元数据.md`
- Modify: `docs/存储服务架构与部署.md`
- Modify: `modules/storage/README.md`
- Modify: `modules/storage/proto/view.proto`

- [ ] **Step 1: Update Query comments**

In `view.proto`, update comments to say:

```protobuf
// QueryTimeSeriesRows 查询 DuckDB 中当前 active View 索引。
// View 索引是可重建的近期派生结果，不是完整事实存储。
// 完整历史以 PrimaryStore KV/Pebble 为准。
```

For `SearchRecordRows`, add equivalent wording for Bleve active View indexes.

- [ ] **Step 2: Update architecture docs**

Add:

```markdown
View 派生索引统一使用 active/building 双索引生命周期。`active_result` 是当前可读索引 ID，`building_result` 是正在构建或 warming 的索引 ID。DuckDB 表和 Bleve 索引目录都实现 `ViewIndexEngine`，上层只调用 `Prepare`、`Write`、`Stat`、`Remove`。

字段、列来源、过滤条件、粒度、查询窗口等 View 定义变化会递增 `view_version`。系统默认抢占式重建新 building 索引；旧 active 继续服务旧 schema。新字段只有在 building 索引切换为 active 后才可查询。
```

- [ ] **Step 3: Update deployment docs**

Document:

- `op=rotate` timer,
- `storage.view.rotation` config,
- remote verification through admin gateway `11000`,
- no unbounded production scans.

- [ ] **Step 4: Regenerate protobuf code**

Run:

```bash
cd modules/storage
make proto
```

Expected: generated comments update or no generated diff if comments are not emitted.

- [ ] **Step 5: Commit**

```bash
git add docs/存储目标架构与元数据.md docs/存储服务架构与部署.md modules/storage/README.md modules/storage/proto/view.proto modules/storage/proto/gen/view.pb.go
git commit -m "docs(storage): document unified view index rotation"
```

---

### Task 10: Local Verification

**Files:**
- No new source files.

- [ ] **Step 1: Run focused tests**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./internal/config ./internal/infra/device/duckdb ./internal/infra/device/bleve ./internal/infra/metadata/sqlite ./internal/services/view ./internal/services/view/builder ./internal/services/view/search ./cmd/server
```

Expected: pass.

- [ ] **Step 2: Run full storage tests**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./...
```

Expected: pass. If an unrelated pre-existing failure appears, record the exact package and failure before release.

- [ ] **Step 3: Check formatting and docs**

Run:

```bash
gofmt -w modules/storage/internal/config modules/storage/internal/infra/device/duckdb modules/storage/internal/infra/device/bleve modules/storage/internal/services/view
git diff --check
```

Expected: no formatting or whitespace errors.

---

### Task 11: Remote Release And Safe Live Verification

**Files:**
- No source changes expected.

- [ ] **Step 1: Build Linux storage binary on remote host**

Use the existing remote Linux CGO build flow:

```bash
rsync -az --delete \
  --exclude .git \
  --exclude web/node_modules \
  /Users/mooyang/Documents/go/src/github.com/mooyang-code/moox/ \
  ubuntu@106.53.107.122:/tmp/moox-build/

ssh ubuntu@106.53.107.122 'cd /tmp/moox-build/modules/storage && GOFLAGS=-buildvcs=false CGO_ENABLED=1 TARGET_GOOS=linux TARGET_GOARCH=amd64 ./scripts/build.sh storage'
```

Expected: storage binaries build successfully.

- [ ] **Step 2: Deploy storage binaries only**

Do not overwrite production `storage.yaml` from local dev config. Copy binaries only:

```bash
ssh ubuntu@106.53.107.122 'cd /home/ubuntu/moox/prod && ./stop.sh storage'
scp ubuntu@106.53.107.122:/tmp/moox-build/modules/storage/bin/moox-storage ubuntu@106.53.107.122:/home/ubuntu/moox/prod/bin/moox-storage.new
ssh ubuntu@106.53.107.122 'cd /home/ubuntu/moox/prod && mv -f bin/moox-storage.new bin/moox-storage && STARTUP_WAIT_SECONDS=10 ./start.sh storage'
```

Expected: storage starts.

- [ ] **Step 3: Verify health**

Run:

```bash
ssh ubuntu@106.53.107.122 'curl -fsS --max-time 5 http://127.0.0.1:11000/healthz'
```

Expected: JSON response with `"status":"ok"`.

- [ ] **Step 4: Verify TimeSeries View query through admin gateway**

Run:

```bash
curl -fsS --max-time 30 \
  -H 'content-type: application/json' \
  -d '{"space_id":"crypto","view_id":"spot_kline_1m_view","sorts":[{"field_name":"data_time","desc":true}],"page":{"page":1,"size":3},"limit":1000,"total_mode":"NONE"}' \
  http://106.53.107.122:11000/api/admin/storage_view/QueryTimeSeriesRows
```

Expected:

- `ret_info.code` is `0`.
- rows are sorted by `data_time` descending.
- no full-table total count is required.

- [ ] **Step 5: Verify Record View query when a Record View exists**

Use `SearchRecordRows` with a small page size and a known View. Do not run broad unbounded scans. Expected:

- `ret_info.code` is `0`,
- rows come from `active_result`,
- logs show no stale `building_result` writes.

- [ ] **Step 6: Verify rotation logs and no lock/OOM**

Run:

```bash
ssh ubuntu@106.53.107.122 'cd /home/ubuntu/moox/prod && grep -E "rotate|warming|active_result|building_result|ViewIndexEngine|lock held|OOM" logs/storage/trpc.log | tail -100'
```

Expected:

- rotation logs show schema/capacity decisions,
- no new `lock held by current process`,
- no OOM-kill in system logs.

- [ ] **Step 7: Record verification**

Append a short verification note under `docs/superpowers/verification/` and commit it:

```bash
git add docs/superpowers/verification/
git commit -m "docs(storage): record view index rotation verification"
```

---

## Rollback Plan

1. Disable rotation in production config:

```yaml
storage:
  view:
    rotation:
      enabled: false
```

2. Restart storage.
3. Existing `active_result` indexes remain readable because query paths still use active pointers.
4. If a warming index was being built, clear `building_result` via the existing failed-build path or let the cleanup timer remove it.
5. Do not remove `active_result` during rollback.
6. If a View index is corrupt, run `RebuildTimeSeriesView` or `RebuildRecordView` for the affected View to rebuild from PrimaryStore.

## Success Criteria

- `ViewIndexEngine` exists with methods `Prepare`, `Write`, `Stat`, `Remove`.
- The physical index identifier is consistently called `indexID`.
- TimeSeries DuckDB and Record/Bleve Views use the same active/building lifecycle.
- Schema changes always trigger preemptive rebuilding without a config toggle.
- Stale warming indexes are not written after `view_version` changes.
- New fields are queryable only after the new index becomes active.
- DuckDB latest-preview queries do not create `__latest` tables.
- Each View normally has at most two live index instances.
- TimeSeries daily/hourly windows are protected by `freq_backfill_window`, not only `overlap_window`.
- Record View backfill uses bounded version-window semantics.
- Production verification uses admin gateway port `11000` and avoids unbounded scans.
