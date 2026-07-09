# Storage View Index Blue-Green Rotation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **Status (2026-07-09):** Plan locked and implementation approved. Implement task-by-task from this document; do not revive deleted rebuild/cleanup/Rebuild* paths or `__latest` removal in v1.

**Goal:** Unify TimeSeries DuckDB Views and Record/Bleve Views under one bounded, blue-green View index lifecycle so PrimaryStore remains the only complete fact store and all derived View indexes can rotate, rebuild, and switch safely.

**Architecture:** Introduce a single `ViewIndexEngine` abstraction with `Prepare`, `Write`, `Stat`, and `Remove` methods in `internal/core/viewindex`. View metadata owns the read pointer through `active_result` and the warming pointer through `building_result`. One scheduler entry `op=rotate` owns schema rebuild, capacity rotation, stale cleanup, ready-switch, and orphan remove. DuckDB and Bleve implement the same lifecycle while keeping their existing query APIs separate.

**Tech Stack:** Go 1.24, tRPC-Go timers, SQLite metadata, Pebble PrimaryStore, DuckDB CGO, Bleve, Storage ViewBuilder, NATS/memory eventbus, `go test`.

---

## Locked Decisions

| Decision | Detail |
|---|---|
| Derived index semantics | DuckDB and Bleve are bounded derived View indexes. PrimaryStore KV/Pebble is the only complete and authoritative fact store. |
| Unified lifecycle | TimeSeries and Record Views both use `active_result` and `building_result` as View index instance IDs. |
| Single scheduler entry | All View index lifecycle work goes through `RotateViewIndexes` via timer `op=rotate`. Delete `RebuildPendingViews`, `RebuildFailedViews`, `CleanupInactiveResults`, and their schedule ops (`cleanup`, `retry_failed`, default pending rebuild). |
| Manual rebuild RPCs | Delete `RebuildTimeSeriesView` / `RebuildRecordView` RPC handlers and proto methods. Operators rely on schema version bumps + `op=rotate`, or capacity rotation. No separate rebuild API. |
| Zero read gap | Old `active_result` keeps serving reads until warming passes ready checks and `CompleteViewBuild` switches the pointer. Never clear `active_result` before the new index is ready. |
| Interface name | Use `ViewIndexEngine`. |
| Interface package | Put shared types in `modules/storage/internal/core/viewindex` so DuckDB/Bleve infra never imports `services/view`. |
| Physical index identifier | Use `indexID`, not `resultID`, `generation`, or `versionID`. |
| Index naming | Use deterministic dual slots `a` / `b` only. Timestamp-suffixed names are obsolete derived junk and may be discarded. |
| Legacy derived indexes | No compatibility migration and no copy from old indexes. On first rotate after this change, rebuild from PrimaryStore into an `a`/`b` slot; remove any non-slot or unreferenced physical indexes via `Remove`. |
| Engine methods | Use `Prepare`, `Write`, `Stat`, `Remove`. Do not use `CreateOrReplace`, `Inspect`, `Retire`, or `GetStats`. |
| Query path | Query APIs stay engine-specific: `QueryTimeSeriesRows` for DuckDB and `SearchRecordRows` for Bleve. The lifecycle interface does not contain a generic `Query` method. |
| Default schema rebuild | Schema/view definition changes are always preemptive. No config switch is needed. If `view_version > active_view_version`, the next `op=rotate` run starts a new warming index immediately. |
| Warming validity | A warming index is writable only when `building_result != ""`, `building_view_version == view_version`, and `build_status == "building"`. Failed/pending leftovers must not receive writes. |
| Build / backfill order | Hard sequence: `Prepare(indexID)` → conditional `BeginViewBuild` (set building pointer) → incremental dual-write becomes active → page backfill from PrimaryStore → ready check → `CompleteViewBuild` → wait `remove_grace` → `Remove(old active)`. Never backfill before the building pointer is published. |
| Switch readiness | Prefer “backfill scan finished”. `min_ready_entries` is only a large-View guard: require `EntryCount >= min(min_ready_entries, expected_or_active_count)`, so small Views can switch as soon as backfill completes. Timestamp-like versions also require `allowed_lag`; non-timestamp Record versions rely on scan-complete + `max_backfill_entries` only. |
| Active schema | New fields become queryable only after the new warming index switches to active. Until then, reads continue to use `active_result` and `active_view_version`. |
| Obsolete warming | If a ViewColumn or View definition changes while a warming index is building, clear `building_result`, stop writing to the old warming index, and remove it asynchronously after grace. |
| Backfill source | Never copy from the old DuckDB table or old Bleve index. Warming indexes are backfilled from PrimaryStore via Access/FactReader so rows are complete. |
| Backfill window | `overlap_window=30m` is only late-arrival/disorder protection. TimeSeries backfill also considers `query_window` and `freq_backfill_window`; Record backfill uses `version_window` semantics. |
| Cleanup | Orphan/old index removal is part of `op=rotate` (`Remove` after grace, plus sweep of unreferenced physical indexes). Do not keep a separate cleanup timer path. Do not rely on periodic row/document deletes. |
| List/Remove correctness | Fix DuckDB `ListResultTables` to discover real `view_*` tables (current `ts_view_%` / `view_result_%` filters are wrong). Bleve must implement directory `Remove`. |
| Concurrency | Register `op=rotate` only on the view_builder role. Keep in-process `activeBuilds`. Make `BeginViewBuild` a conditional claim so multi-replica view_builder does not start two warmings for the same View. |
| Schema hash | `HashViewIndexSchema` hashes engine + column shape only. Do **not** include `ViewVersion`; compare version separately. |
| Latest helper | Keep DuckDB `__latest` in this plan. Delete it only as a follow-up after rotation is live, active indexes are bounded, and K-line query performance is re-verified. |
| `backfill_done` placement | Do **not** add a durable metadata column for `backfill_done`. Prefer finishing warming in one successful rotate claim: local/in-memory `backfill_done` in that run, then ready check + `CompleteViewBuild` before releasing the claim. If the process crashes mid-warming, treat the building index as stale after timeout and restart from PrimaryStore (derived indexes are disposable). Optional resume cursor may later live in `View.attributes`, but is out of scope for v1. |
| `rotation.enabled` semantics | `enabled=false` is a **full kill switch** for `RotateViewIndexes`: no schema warming, no capacity warming, no ready-switch, no orphan sweep. Incremental dual-write to an already-published `building_result` still follows `BuildingIndexWritable` rules, but no new lifecycle transitions run. Schema bumps while disabled leave `view_version` ahead of `active_view_version`; old active keeps serving until rotation is re-enabled and warming completes. Do not add a separate `capacity_enabled` flag in v1. |

## Review Revisions (2026-07-09)

This plan was revised after review. Keep these outcomes when implementing; do not reintroduce the rejected paths.

| Topic | Outcome |
|---|---|
| Compatibility / migration | New project: no historical index migration. Discard non-`a`/`b` derived indexes and rebuild from PrimaryStore. |
| Scheduler | Merge pending rebuild into `op=rotate`. Delete `RebuildPendingViews`, `RebuildFailedViews`, `CleanupInactiveResults`, and cleanup/retry_failed timers. |
| Manual Rebuild RPCs | Delete `RebuildTimeSeriesView` / `RebuildRecordView`. |
| Read gap | Accept zero-gap: old `active_result` serves until warming Complete. |
| Switch ready | Backfill scan-complete first; `min_ready_entries` is a large-View guard only. |
| Dual-write gate | Require `build_status == "building"` plus matching `building_view_version`. |
| Package | `ViewIndexEngine` lives in `internal/core/viewindex`, not `services/view`. |
| Schema hash | Exclude `ViewVersion`. |
| `__latest` | Keep in this plan; remove only as a verified follow-up. |
| `backfill_done` | Local to one rotate claim in v1; no durable metadata column. Crash → stale restart. |
| `rotation.enabled` | Full kill switch for the entire rotate pass. |

## Out Of Scope / Follow-ups

- Delete DuckDB `__latest` helper after rotation is live and K-line query performance is re-checked.
- Durable warming resume cursor in `View.attributes` (optional later).
- Separate `capacity_enabled` vs schema-enabled flags.
- Copying rows/documents from an old derived index into a new slot.

## Target Interface

Create the common lifecycle interface in `modules/storage/internal/core/viewindex/engine.go`:

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
- `SchemaHash` is computed from engine + ViewColumn shape only (not `ViewVersion`, not display-only text).

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

- `enabled`: full kill switch for `RotateViewIndexes`. When false, skip the entire rotate pass.
- `max_entries`: capacity threshold for active indexes.
- `min_ready_entries`: large-View guard only; small Views switch when backfill completes even if below this number.
- `overlap_window`: late-arrival/disorder safety buffer.
- `default_backfill_window`: fallback backfill window.
- `allowed_lag`: warming `MaxVersion` may lag active/latest by at most this duration when versions are parseable timestamps.
- `remove_grace_ms`: delay before removing old active or obsolete warming indexes after switch/cancel.
- `time_series.freq_backfill_window`: frequency-aware minimum windows for K-line Views.
- `record.default_version_window`: default Record View version range when versions are timestamp-like.
- `record.max_backfill_entries`: safety cap for Record View backfill pages.

No `rebuild_on_schema_change` config exists. Schema changes always rebuild preemptively when rotation is enabled.

## Rotate Decision Order

```text
if !rotation.enabled:
  return  // full kill switch; leave active/building pointers untouched

for each View:
  1. if view_version > active_view_version:
       start schema warming on inactive a/b slot (or continue if already building current version)
  2. else if building pointer is stale/failed/obsolete:
       clear building metadata and Remove physical warming index after grace
  3. else if BuildingIndexWritable(view):
       // v1: warming work should finish inside the same claim when possible.
       // local backfill_done → ready check → CompleteViewBuild
       // old active keeps serving until Complete
       // schedule Remove(old active) after remove_grace
       // do NOT start another capacity rotate while warming is in progress
  4. else if active Stat.EntryCount > max_entries:
       start capacity warming on inactive a/b slot
  5. sweep unreferenced physical indexes (non a/b leftovers, dropped building, expired grace queue)
```

## File Structure

### New files

- `modules/storage/internal/core/viewindex/engine.go`  
  Defines `ViewIndexEngine`, `ViewIndexSchema`, `ViewIndexBatch`, `ViewIndexStats`, schema hash helpers, and index ID helpers.
- `modules/storage/internal/core/viewindex/engine_test.go`  
  Tests schema hash stability, index ID naming, and active/building version guards.
- `modules/storage/internal/services/view/rotation.go`  
  Unified rotation manager for DuckDB and Bleve View indexes.
- `modules/storage/internal/services/view/rotation_test.go`  
  Tests schema-preemptive rebuild, capacity rotation, stale warming cleanup, switch readiness, and zero-gap active retention.

### Modified files

- `modules/storage/internal/config/loader.go`  
  Add generic `StorageViewRotation` config and defaults.
- `modules/storage/internal/config/loader_test.go`  
  Cover generic rotation defaults and YAML overrides.
- `modules/storage/config/storage.yaml`  
  Add `storage.view.rotation`.
- `modules/storage/config/storage.view_builder.yaml`  
  Add the same defaults for independent view-builder deployment.
- `modules/storage/config/trpc_go.yaml` / `trpc_go.view_builder.yaml`  
  Replace pending/cleanup/retry_failed timers with a single `op=rotate` timer on view_builder.
- `modules/storage/internal/infra/device/duckdb/view_store.go`  
  Implement DuckDB index lifecycle primitives; fix `ListResultTables` naming; keep `__latest` for now.
- `modules/storage/internal/infra/device/duckdb/view_store_nocgo.go`  
  Keep no-cgo API parity.
- `modules/storage/internal/infra/device/duckdb/view_store_test.go`  
  Cover `Prepare`, `Write`, `Stat`, `Remove`, and list/remove of real `view_*` tables.
- `modules/storage/internal/services/view/search/service.go`  
  Implement Bleve index lifecycle primitives including `Remove`.
- `modules/storage/internal/infra/device/bleve/index.go`  
  Add document count/version stats and remove-index support.
- `modules/storage/internal/services/view/search/service_test.go`  
  Cover Bleve `Prepare`, `Write`, `Stat`, and `Remove`.
- `modules/storage/internal/infra/metadata/sqlite/crud.go`  
  Preserve `active_result` until Complete; clear obsolete `building_result` on View shape changes; make `BeginViewBuild` a conditional claim; keep compare-and-switch semantics.
- `modules/storage/internal/infra/metadata/sqlite/crud_test.go`  
  Cover ViewColumn changes bump version and clear building pointers; Begin claim races; Complete keeps previous active until switch.
- `modules/storage/internal/services/view/view_builder.go`  
  Delete `RebuildPendingViews` / `RebuildFailedViews` / `CleanupInactiveResults` paths; reuse helpers only from rotation/backfill.
- `modules/storage/internal/services/view/schedule.go`  
  Keep only `op=rotate` (delete cleanup/retry_failed/pending branches).
- `modules/storage/internal/services/view/builder/time_series.go`  
  Write active plus valid building index only (`build_status == building`).
- `modules/storage/internal/services/view/builder/record.go`  
  Write active plus valid building index only.
- `modules/storage/internal/services/view/service.go`  
  Validate requested columns against active schema/version; delete Rebuild* RPC handlers.
- `modules/storage/internal/services/access/query.go`  
  Delete Rebuild* access wrappers if present.
- `modules/storage/proto/view.proto`  
  Remove Rebuild* RPCs/messages; update query comments.
- `modules/storage/cmd/server/main.go`  
  Wire rotation config and register the rotate timer only on view_builder.
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
	if !rotation.IsEnabled() {
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

func TestStorageViewRotationYAMLDisableKillSwitch(t *testing.T) {
	raw := []byte(`
storage:
  view:
    rotation:
      enabled: false
`)
	var cfg RuntimeConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	cfg.ApplyDefaults()
	if cfg.Storage.View.Rotation.IsEnabled() {
		t.Fatal("rotation enabled = true after explicit false, want false")
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
	Enabled               *bool                         `yaml:"enabled"`
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

Use `Enabled *bool` (or an equivalent omit-detection pattern already used in this loader) so YAML `enabled: false` is preserved while omitted config defaults to true.

```go
if c.View.Rotation.Enabled == nil {
	enabled := true
	c.View.Rotation.Enabled = &enabled
}
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

Helper used by rotate:

```go
func (r StorageViewRotation) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
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
- Create: `modules/storage/internal/core/viewindex/engine.go`
- Create: `modules/storage/internal/core/viewindex/engine_test.go`

- [ ] **Step 1: Write failing interface helper tests**

Create `engine_test.go`:

```go
package viewindex

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

func TestViewIndexSchemaHashIgnoresViewVersion(t *testing.T) {
	base := ViewIndexSchema{
		SpaceID: "crypto", ViewID: "spot", ViewVersion: 1, Engine: "duckdb",
		Columns: []*pb.ViewColumn{{ColumnName: "close", OriginId: "binance_spot_kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
	bumped := base
	bumped.ViewVersion = 2
	if HashViewIndexSchema(base) != HashViewIndexSchema(bumped) {
		t.Fatal("schema hash changed after version-only bump")
	}
	withVolume := base
	withVolume.Columns = append(withVolume.Columns, &pb.ViewColumn{ColumnName: "volume", OriginId: "binance_spot_kline.volume", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	if HashViewIndexSchema(base) == HashViewIndexSchema(withVolume) {
		t.Fatal("schema hash did not change after column add")
	}
}

func TestBuildingIndexWritableRequiresBuildingStatusAndCurrentVersion(t *testing.T) {
	view := &pb.View{
		ViewVersion: 3, BuildingViewVersion: 3, BuildingResult: "view_crypto_spot_b",
		BuildStatus: "failed",
	}
	if BuildingIndexWritable(view) {
		t.Fatal("building index writable for failed status, want false")
	}
	view.BuildStatus = "building"
	if !BuildingIndexWritable(view) {
		t.Fatal("building index not writable for current building version, want true")
	}
	view.BuildingViewVersion = 2
	if BuildingIndexWritable(view) {
		t.Fatal("building index writable for stale version, want false")
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/core/viewindex -run 'TestViewIndex|TestBuildingIndexWritable'
```

Expected: fails because helpers and types do not exist.

- [ ] **Step 3: Implement common types and helpers**

Create `engine.go` with:

```go
package viewindex

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
	return view != nil &&
		view.GetBuildingResult() != "" &&
		view.GetBuildingViewVersion() == view.GetViewVersion() &&
		view.GetBuildStatus() == "building"
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
		SpaceID string        `json:"space_id"`
		ViewID  string        `json:"view_id"`
		Engine  string        `json:"engine"`
		Columns []columnShape `json:"columns"`
	}{SpaceID: schema.SpaceID, ViewID: schema.ViewID, Engine: schema.Engine}
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

Move or duplicate `sanitizeResultTableName` into this package so naming stays shared.

- [ ] **Step 4: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/core/viewindex -run 'TestViewIndex|TestBuildingIndexWritable'
```

Expected: pass.

- [ ] **Step 5: Commit**

```bash
git add modules/storage/internal/core/viewindex/engine.go modules/storage/internal/core/viewindex/engine_test.go
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
	view.BuildStatus = "building"
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

func TestBeginViewBuildConditionalClaim(t *testing.T) {
	// First claim succeeds; second concurrent claim for another indexID fails while building is fresh.
}

func TestCompleteViewBuildKeepsPreviousActiveUntilSwitch(t *testing.T) {
	// Before Complete, queries still see old active_result; after Complete, active flips atomically.
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

In both `time_series.go` and `record.go`, write active unconditionally when non-empty, and write building only through `viewindex.BuildingIndexWritable(item)` (requires `build_status == "building"` and matching version):

```go
if item.GetActiveResult() != "" {
	if err := s.views.InsertRows(ctx, item.GetActiveResult(), mapped); err != nil {
		return err
	}
}
if viewindex.BuildingIndexWritable(item) {
	if err := s.views.InsertRows(ctx, item.GetBuildingResult(), mapped); err != nil {
		return err
	}
}
```

For Record/Bleve use `s.search.IndexRecordViewRows` with the same guard. Failed building leftovers must receive no writes.

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

- `Prepare` creates an empty table with the requested schema and refuses `indexID == current active` callers from rotation tests.
- `Write` upserts TimeSeries rows; rejects Record batches.
- `Stat` returns `EntryCount`, `MinVersion`, and `MaxVersion`.
- `Remove` drops the table and column metadata.
- `ListResultTables` returns real `view_*` tables (not only `ts_view_%` / `view_result_%`).

Use existing `duckDBTestRow` and `duckDBTestValue` helpers. Keep `__latest` behavior unchanged in this task.

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
CGO_ENABLED=1 go test -count=1 ./internal/infra/device/duckdb -run 'TestDuckDBViewIndex|TestListResultTablesIncludesViewPrefix'
```

Expected: fail until lifecycle methods exist and list filter is fixed.

- [ ] **Step 3: Implement lifecycle methods**

Add methods to `ViewStore` using `modules/storage/internal/core/viewindex`:

```go
func (s *ViewStore) Engine() string { return "duckdb" }

func (s *ViewStore) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	if err := s.DropResultTable(ctx, indexID); err != nil {
		return err
	}
	return s.CreateResultTable(ctx, indexID, schema.Columns)
}

func (s *ViewStore) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if len(batch.RecordRows) > 0 {
		return fmt.Errorf("duckdb view index rejects record rows")
	}
	return s.InsertRows(ctx, indexID, batch.TimeSeriesRows)
}

func (s *ViewStore) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	stats, err := s.resultTableStats(ctx, indexID)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	return viewindex.ViewIndexStats{
		Exists: true, EntryCount: int64(stats.RowCount),
		MinVersion: stats.MinDataTime, MaxVersion: stats.MaxDataTime,
	}, nil
}

func (s *ViewStore) Remove(ctx context.Context, indexID string) error {
	return s.DropResultTable(ctx, indexID)
}
```

Fix `ListResultTables` so orphan `view_*` tables are discoverable for rotate sweeps.

- [ ] **Step 4: Keep `__latest` for now**

Do not delete the `__latest` helper in this plan. Follow-up after rotation is verified and K-line query performance remains acceptable.

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

In `search.Service`, add methods using `viewindex`:

```go
func (s *Service) Engine() string { return "bleve" }

func (s *Service) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	if err := s.Remove(ctx, indexID); err != nil {
		return err
	}
	_, err := s.searchIndex(indexID, true)
	return err
}

func (s *Service) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if len(batch.TimeSeriesRows) > 0 {
		return fmt.Errorf("bleve view index rejects time series rows")
	}
	return s.IndexRecordViewRows(ctx, indexID, batch.Columns, batch.RecordRows)
}

func (s *Service) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	index, err := s.searchIndex(indexID, false)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
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

### Task 6: Add Unified Rotation Manager And Delete Old Rebuild Paths

**Files:**
- Create: `modules/storage/internal/services/view/rotation.go`
- Create: `modules/storage/internal/services/view/rotation_test.go`
- Modify: `modules/storage/internal/services/view/schedule.go`
- Modify: `modules/storage/internal/services/view/view_builder.go`
- Modify: `modules/storage/internal/services/view/service.go`
- Modify: `modules/storage/internal/services/access/query.go`
- Modify: `modules/storage/proto/view.proto`
- Modify: `modules/storage/config/trpc_go.yaml`
- Modify: `modules/storage/config/trpc_go.view_builder.yaml`
- Modify: `modules/storage/cmd/server/main.go`

- [x] **Step 1: Add failing rotation tests**

Create tests for:

- schema gap triggers warming even when active index is below `max_entries`,
- capacity triggers rotation only when `view_version == active_view_version` and no valid warming exists,
- stale/failed building index is removed and cleared,
- ready switch keeps old `active_result` readable until `CompleteViewBuild`,
- small View with `EntryCount < min_ready_entries` still switches after backfill_done,
- TimeSeries Views use DuckDB engine,
- Record Views use Bleve engine,
- `BeginViewBuild` conditional claim prevents a second replica from starting another warming.

Use fake `ViewIndexEngine` implementations that record calls to `Prepare`, `Write`, `Stat`, and `Remove`.

- [x] **Step 2: Run tests and verify failure**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view -run 'TestRotation'
```

Expected: fail until rotation manager exists.

- [x] **Step 3: Implement rotation manager**

Implement `RotateViewIndexes(ctx, spaceID)` as the only lifecycle entry. Decision order must match the Locked Decisions / Rotate Decision Order section:

```text
if !rotation.IsEnabled():
  return

for each View:
  1. schema gap → start/continue warming
  2. stale/failed building → clear + Remove
  3. valid warming → (Task 7 backfill +) ready check → Complete (zero gap) → grace Remove old active
  4. else capacity overflow → start warming
  5. sweep unreferenced physical indexes
```

Task 6 may land the decision skeleton and delete old rebuild paths first. Do **not** call `CompleteViewBuild` in production until Task 7 provides real PrimaryStore backfill and local `backfill_done`. Tests may stub backfill completion.

Warming start sequence is mandatory and should complete inside one successful claim when possible:

```text
Prepare(indexID)
→ BeginViewBuild(conditional claim)
→ dual-write enabled
→ page backfill from PrimaryStore
→ local backfill_done = true
→ ready check
→ CompleteViewBuild   // old active serves until here
→ grace Remove(old active)
```

Do not persist `backfill_done` as a metadata column in v1. If the claim crashes mid-warming, stale timeout clears/restarts the building index from PrimaryStore.

If `rotation.enabled == false`, `RotateViewIndexes` returns immediately without starting, switching, or sweeping.

Selected inactive index ID:

```go
indexID := viewindex.InactiveViewIndexID(view.GetSpaceId(), view.GetViewId(), view.GetActiveResult())
```

If `active_result` is empty or not an a/b slot, treat slot `a` as the first warming target and discard any non-slot leftovers during sweep.

- [x] **Step 4: Replace schedule ops with rotate only**

In `HandleSchedule`, keep only:

```go
case "rotate":
	rotated, err := builder.RotateViewIndexes(ctx, spaceID)
	...
```

Delete branches for default pending rebuild, `cleanup`, and `retry_failed`.

Update `trpc_go.yaml` and `trpc_go.view_builder.yaml`:

- replace `view.timer` params with `op=rotate`
- remove or disable `view.cleanup.timer` and `view.retry_failed.timer`
- register the timer only for view_builder role wiring in `main.go`

- [x] **Step 5: Delete old rebuild/cleanup code and RPCs**

Delete or stop exporting:

- `Builder.RebuildPendingViews` / `RebuildPendingViewsInAllSpaces`
- `Builder.RebuildFailedViews`
- `Builder.CleanupInactiveResults`
- `DataView.RebuildTimeSeriesView` / `RebuildRecordView` handlers
- matching access-layer wrappers
- proto RPCs/messages for Rebuild*

Regenerate protobuf after proto edits.

- [x] **Step 6: Wire engines**

In `cmd/server/main.go`, register engine implementations for the View builder:

- DuckDB `ViewStore` for engine `duckdb`
- Bleve search service for engine `bleve`

Do not add engine-specific rotation code to `schedule.go`.

- [x] **Step 7: Run tests**

Run:

```bash
cd modules/storage
go test -count=1 ./internal/services/view ./internal/services/access ./cmd/server
```

Expected: pass; no remaining references to deleted rebuild/cleanup entry points except historical docs if any.

- [x] **Step 8: Commit**

```bash
git add modules/storage/internal/services/view/rotation.go modules/storage/internal/services/view/rotation_test.go modules/storage/internal/services/view/schedule.go modules/storage/internal/services/view/view_builder.go modules/storage/internal/services/view/service.go modules/storage/internal/services/access/query.go modules/storage/proto/view.proto modules/storage/proto/gen modules/storage/config/trpc_go.yaml modules/storage/config/trpc_go.view_builder.yaml modules/storage/cmd/server/main.go
git commit -m "feat(storage): unify view index lifecycle under rotate"
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

- [ ] **Step 6: Add cancellation guard and ready marking**

Before each warming write, reload or re-check metadata:

```text
building_result == current indexID
building_view_version == target view_version
view_version == target view_version
build_status == building
```

If any check fails, stop the backfill without switching.

When the final page completes (or Record hits `max_backfill_entries` with a logged warning), set local `backfill_done=true` for this claim and evaluate ready in-process:

```text
local backfill_done
AND BuildingIndexWritable
AND EntryCount >= min(min_ready_entries, expected_or_active_count)  // small Views: scan-complete wins
AND (timestamp versions ⇒ lag <= allowed_lag; else skip lag)
→ CompleteViewBuild
→ keep serving until pointer flips
→ grace Remove(old active)
```

Do not write `backfill_done` into SQLite/metadata in v1. Crash recovery is stale-building restart, not resume.

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
View 派生索引统一使用 active/building 双索引生命周期。`active_result` 是当前可读索引 ID，`building_result` 是正在构建或 warming 的索引 ID。DuckDB 表和 Bleve 索引目录都实现 `ViewIndexEngine`（`internal/core/viewindex`），上层只调用 `Prepare`、`Write`、`Stat`、`Remove`。

唯一调度入口是 `op=rotate`：负责 schema 抢占重建、容量轮换、stale/failed warming 清理、ready switch，以及未引用物理索引删除。已删除独立的 pending rebuild / cleanup / retry_failed 路径，也删除手动 Rebuild* RPC。

字段、列来源、过滤条件、粒度、查询窗口等 View 定义变化会递增 `view_version`。系统默认抢占式重建新 building 索引；旧 active 继续服务直到 warming Complete 切换指针。新字段只有在 building 索引切换为 active 后才可查询。旧 timestamp 命名的派生索引可直接丢弃并从 PrimaryStore 重建到 a/b slot。
```

- [ ] **Step 3: Update deployment docs**

Document:

- single `op=rotate` timer on view_builder only,
- `storage.view.rotation` config,
- remote verification through admin gateway `11000`,
- no unbounded production scans,
- `__latest` still present until a follow-up removal after performance verification.

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
CGO_ENABLED=1 go test -count=1 ./internal/config ./internal/core/viewindex ./internal/infra/device/duckdb ./internal/infra/device/bleve ./internal/infra/metadata/sqlite ./internal/services/view ./internal/services/view/builder ./internal/services/view/search ./cmd/server
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
gofmt -w modules/storage/internal/config modules/storage/internal/core/viewindex modules/storage/internal/infra/device/duckdb modules/storage/internal/infra/device/bleve modules/storage/internal/services/view
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

Optional stronger check when safe: temporarily lower `max_entries` for one View, confirm warming → Complete while queries still hit old `active_result`, then grace Remove of the previous slot.

- [ ] **Step 7: Record verification**

Append a short verification note under `docs/superpowers/verification/` and commit it:

```bash
git add docs/superpowers/verification/
git commit -m "docs(storage): record view index rotation verification"
```

---

## Rollback Plan

1. Disable the full rotate kill switch in production config:

```yaml
storage:
  view:
    rotation:
      enabled: false
```

2. Restart storage / view_builder.
3. `RotateViewIndexes` becomes a no-op: no schema warming, no capacity warming, no switch, no orphan sweep.
4. Existing `active_result` indexes remain readable; never clear active during rollback.
5. If a warming index was mid-build, clear `building_result` / `build_status` via metadata repair after disable; physical orphan removal waits until rotation is re-enabled (or manual engine `Remove`).
6. After re-enabling rotation, corrupt or empty Views recover by schema/capacity-driven warming from PrimaryStore (no Rebuild* RPC). Schema gaps accumulated while disabled are drained on later rotate passes.

## Success Criteria

- `ViewIndexEngine` lives in `internal/core/viewindex` with methods `Prepare`, `Write`, `Stat`, `Remove`.
- The physical index identifier is consistently called `indexID` and uses deterministic `a`/`b` slots.
- TimeSeries DuckDB and Record/Bleve Views use the same active/building lifecycle.
- `op=rotate` is the only scheduler entry; `RebuildPendingViews` / `RebuildFailedViews` / `CleanupInactiveResults` and Rebuild* RPCs are gone.
- Schema changes always trigger preemptive warming when rotation is enabled.
- `rotation.enabled=false` stops the entire rotate pass, not only capacity rotation.
- Old `active_result` keeps serving until warming Complete (zero read gap).
- Warming prefers one-claim backfill + local `backfill_done`; no durable backfill metadata column in v1.
- Stale/failed warming indexes are not written after version/status changes.
- New fields are queryable only after the new index becomes active.
- Small Views can switch after backfill completes even when below `min_ready_entries`.
- Non-slot / unreferenced derived indexes are removable (DuckDB list filter fixed; Bleve Remove exists).
- Each View normally has at most two live index instances.
- TimeSeries daily/hourly windows are protected by `freq_backfill_window`, not only `overlap_window`.
- Record View backfill uses bounded version-window semantics.
- `__latest` remains until a verified follow-up removal.
- Production verification uses admin gateway port `11000` and avoids unbounded scans.
