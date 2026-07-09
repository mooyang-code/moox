package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// fakeEngine is a ViewIndexEngine test double that records Prepare/Write/
// Stat/Remove calls so tests can assert the rotate decision order without a
// real DuckDB or Bleve backend.
type fakeEngine struct {
	name string

	mu         sync.Mutex
	stats      map[string]*viewindex.ViewIndexStats
	prepared   []string
	removed    []string
	writeCalls int
	prepareErr error
	statErr    map[string]error
}

func newFakeEngine(name string) *fakeEngine {
	return &fakeEngine{name: name, stats: map[string]*viewindex.ViewIndexStats{}, statErr: map[string]error{}}
}

func (f *fakeEngine) Engine() string { return f.name }

func (f *fakeEngine) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prepareErr != nil {
		return f.prepareErr
	}
	f.prepared = append(f.prepared, indexID)
	if _, ok := f.stats[indexID]; !ok {
		f.stats[indexID] = &viewindex.ViewIndexStats{Exists: true}
	}
	return nil
}

func (f *fakeEngine) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCalls++
	stat, ok := f.stats[indexID]
	if !ok {
		stat = &viewindex.ViewIndexStats{}
		f.stats[indexID] = stat
	}
	stat.Exists = true
	stat.EntryCount += int64(len(batch.TimeSeriesRows) + len(batch.RecordRows))
	for _, row := range batch.TimeSeriesRows {
		if v := row.GetKey().GetDataTime(); v > stat.MaxVersion {
			stat.MaxVersion = v
		}
	}
	for _, row := range batch.RecordRows {
		if v := row.GetKey().GetVersion(); v > stat.MaxVersion {
			stat.MaxVersion = v
		}
	}
	return nil
}

func (f *fakeEngine) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.statErr[indexID]; ok {
		return viewindex.ViewIndexStats{}, err
	}
	if stat, ok := f.stats[indexID]; ok {
		return *stat, nil
	}
	return viewindex.ViewIndexStats{}, nil
}

func (f *fakeEngine) Remove(ctx context.Context, indexID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, indexID)
	delete(f.stats, indexID)
	return nil
}

func (f *fakeEngine) setStat(indexID string, stat viewindex.ViewIndexStats) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats[indexID] = &stat
}

func (f *fakeEngine) wasPrepared(indexID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return containsString(f.prepared, indexID)
}

func (f *fakeEngine) wasRemoved(indexID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return containsString(f.removed, indexID)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// fakeMetadata is an in-memory Metadata test double whose BeginViewBuild /
// CompleteViewBuild / FailViewBuild mirror the conditional-claim semantics of
// the sqlite-backed Store (see internal/infra/metadata/sqlite/crud.go) so
// rotation tests exercise the same claim contract RotateViewIndexes relies
// on in production.
type fakeMetadata struct {
	mu       sync.Mutex
	views    map[string]*pb.View
	columns  map[string][]*pb.ViewColumn
	datasets map[string]*pb.Dataset
}

func newFakeMetadata() *fakeMetadata {
	return &fakeMetadata{views: map[string]*pb.View{}, columns: map[string][]*pb.ViewColumn{}, datasets: map[string]*pb.Dataset{}}
}

// putDataset registers a Dataset (e.g. with Freqs) used by
// primaryStoreBackfill.timeSeriesBackfillWindow. Views without a
// registered Dataset fall back to the default TimeSeries dataset used by
// the rest of this test file.
func (m *fakeMetadata) putDataset(ds *pb.Dataset) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.datasets[fakeViewKey(ds.GetSpaceId(), ds.GetDatasetId())] = proto.Clone(ds).(*pb.Dataset)
}

func fakeViewKey(spaceID, viewID string) string { return spaceID + "|" + viewID }

func (m *fakeMetadata) putView(item *pb.View) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.views[fakeViewKey(item.GetSpaceId(), item.GetViewId())] = proto.Clone(item).(*pb.View)
}

func (m *fakeMetadata) putColumns(spaceID, viewID string, columns []*pb.ViewColumn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.columns[fakeViewKey(spaceID, viewID)] = columns
}

func (m *fakeMetadata) getViewLocked(spaceID, viewID string) (*pb.View, error) {
	item, ok := m.views[fakeViewKey(spaceID, viewID)]
	if !ok {
		return nil, fmt.Errorf("view %s/%s not found", spaceID, viewID)
	}
	return item, nil
}

func (m *fakeMetadata) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.getViewLocked(spaceID, viewID)
	if err != nil {
		return nil, err
	}
	return proto.Clone(item).(*pb.View), nil
}

func (m *fakeMetadata) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.views))
	for key := range m.views {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out []*pb.View
	for _, key := range keys {
		item := m.views[key]
		if spaceID != "" && item.GetSpaceId() != spaceID {
			continue
		}
		if datasetID != "" && item.GetPrimaryDatasetId() != datasetID {
			continue
		}
		if status != "" && item.GetStatus() != status {
			continue
		}
		out = append(out, proto.Clone(item).(*pb.View))
	}
	return out, &pb.PageResult{Page: 1, Size: uint32(len(out)), Total: uint32(len(out)), HasMore: false}, nil
}

func (m *fakeMetadata) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	views, _, err := m.ListViews(ctx, spaceID, datasetID, "", nil)
	return views, err
}

func (m *fakeMetadata) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	columns := m.columns[fakeViewKey(spaceID, viewID)]
	return columns, &pb.PageResult{Page: 1, Size: uint32(len(columns)), Total: uint32(len(columns)), HasMore: false}, nil
}

func (m *fakeMetadata) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var spaces []*pb.Space
	keys := make([]string, 0, len(m.views))
	for key := range m.views {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		spaceID := m.views[key].GetSpaceId()
		if seen[spaceID] {
			continue
		}
		seen[spaceID] = true
		spaces = append(spaces, &pb.Space{SpaceId: spaceID})
	}
	return spaces, &pb.PageResult{Page: 1, Size: uint32(len(spaces)), Total: uint32(len(spaces)), HasMore: false}, nil
}

func (m *fakeMetadata) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ds, ok := m.datasets[fakeViewKey(spaceID, datasetID)]; ok {
		return proto.Clone(ds).(*pb.Dataset), nil
	}
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}, nil
}

func (m *fakeMetadata) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := proto.Clone(item).(*pb.View)
	m.views[fakeViewKey(item.GetSpaceId(), item.GetViewId())] = clone
	return proto.Clone(clone).(*pb.View), nil
}

// BeginViewBuild mirrors sqlite Store.BeginViewBuild: it claims the building
// slot unless the view is already building the same target_version with a
// different result_name (the conditional-claim guard that stops a second
// view_builder replica from starting a competing warming attempt).
func (m *fakeMetadata) BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error) {
	if spaceID == "" || viewID == "" || targetVersion == 0 || resultName == "" {
		return nil, errors.New("space_id, view_id, target_version and result_name are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.getViewLocked(spaceID, viewID)
	if err != nil {
		return nil, err
	}
	if item.GetViewVersion() < targetVersion {
		return nil, fmt.Errorf("view %s/%s version %d is older than target %d", spaceID, viewID, item.GetViewVersion(), targetVersion)
	}
	if item.GetBuildStatus() == "building" && item.GetBuildingResult() != "" &&
		item.GetBuildingViewVersion() == targetVersion && item.GetBuildingResult() != resultName {
		return nil, fmt.Errorf("view %s/%s building target already claimed", spaceID, viewID)
	}
	clone := proto.Clone(item).(*pb.View)
	clone.BuildStatus = "building"
	clone.BuildingViewVersion = targetVersion
	clone.BuildingResult = resultName
	clone.BuildError = ""
	clone.BuildStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	clone.BuildFinishedAt = ""
	m.views[fakeViewKey(spaceID, viewID)] = clone
	return proto.Clone(clone).(*pb.View), nil
}

func (m *fakeMetadata) CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.getViewLocked(spaceID, viewID)
	if err != nil {
		return err
	}
	if item.GetBuildingViewVersion() != targetVersion || item.GetBuildingResult() != resultName {
		return fmt.Errorf("view %s/%s building target changed", spaceID, viewID)
	}
	clone := proto.Clone(item).(*pb.View)
	clone.ActiveResult = resultName
	clone.ActiveViewVersion = targetVersion
	clone.BuildingViewVersion = 0
	clone.BuildingResult = ""
	clone.BuildStatus = "active"
	clone.BuildError = ""
	clone.BuildFinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.views[fakeViewKey(spaceID, viewID)] = clone
	return nil
}

func (m *fakeMetadata) FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, err := m.getViewLocked(spaceID, viewID)
	if err != nil {
		return err
	}
	clone := proto.Clone(item).(*pb.View)
	clone.BuildStatus = "failed"
	if buildErr != nil {
		clone.BuildError = buildErr.Error()
	}
	clone.BuildFinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	m.views[fakeViewKey(spaceID, viewID)] = clone
	return nil
}

func sampleViewColumns() []*pb.ViewColumn {
	return []*pb.ViewColumn{
		{ColumnName: "value", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "ds1.value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
	}
}

// alwaysDoneBackfill is a Task 6 test stub standing in for the Task 7
// PrimaryStore backfill hook: it reports the warming index as fully
// backfilled on the first call so tests can exercise the ready-switch path
// end to end without a real PrimaryStore.
func alwaysDoneBackfill(context.Context, viewindex.ViewIndexEngine, *pb.View, string) (bool, error) {
	return true, nil
}

// neverDoneBackfill simulates a warming pass whose backfill scan has not
// completed yet, so CompleteViewBuild must not be called.
func neverDoneBackfill(context.Context, viewindex.ViewIndexEngine, *pb.View, string) (bool, error) {
	return false, nil
}

func newTestRotationManager(metadata Metadata, engines map[string]viewindex.ViewIndexEngine, cfg RotationConfig, backfill BackfillFunc) *RotationManager {
	return NewRotationManager(RotationOptions{
		Metadata: metadata,
		Engines:  engines,
		Config:   cfg,
		Backfill: backfill,
	})
}

func TestRotation_SchemaGapTriggersWarmingBelowCapacity(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_gap"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	warmingSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	// Active index is far below max_entries; only the schema gap should
	// trigger warming, not capacity.
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 10})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1_000_000, MinReadyEntries: 0}, alwaysDoneBackfill)

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected 1 rotated view, got %d", rotated)
	}
	if !engine.wasPrepared(warmingSlot) {
		t.Fatalf("expected warming slot %s to be prepared", warmingSlot)
	}

	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetActiveResult() != warmingSlot {
		t.Fatalf("expected active_result to switch to %s, got %s", warmingSlot, updated.GetActiveResult())
	}
	if updated.GetActiveViewVersion() != 2 {
		t.Fatalf("expected active_view_version 2, got %d", updated.GetActiveViewVersion())
	}
	if updated.GetBuildingResult() != "" {
		t.Fatalf("expected building_result to be cleared after switch, got %q", updated.GetBuildingResult())
	}
}

func TestRotation_CapacityTriggersRotationOnlyAtSameVersionWithoutValidWarming(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_capacity"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	warmingSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 1, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 5000})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1000, MinReadyEntries: 0}, alwaysDoneBackfill)

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected capacity overflow to trigger 1 rotation, got %d", rotated)
	}
	if !engine.wasPrepared(warmingSlot) {
		t.Fatalf("expected capacity warming to prepare %s", warmingSlot)
	}

	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetActiveResult() != warmingSlot {
		t.Fatalf("expected capacity rotation to switch active_result to %s, got %s", warmingSlot, updated.GetActiveResult())
	}

	// A second rotate pass with the index now below capacity and no schema
	// gap must not start another warming cycle.
	engine.setStat(warmingSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 10})
	preparedBefore := len(engine.prepared)
	rotated, err = mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("second RotateViewIndexes returned error: %v", err)
	}
	if rotated != 0 {
		t.Fatalf("expected no further rotation once below capacity, got %d", rotated)
	}
	if len(engine.prepared) != preparedBefore {
		t.Fatalf("expected no additional Prepare calls, prepared=%v", engine.prepared)
	}
}

func TestRotation_StaleBuildingClearedAndRemoved(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_stale"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	staleSlot := viewindex.ViewIndexID(spaceID, viewID, "b")
	staleStartedAt := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339Nano)

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 1, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "building",
		BuildingResult: staleSlot, BuildingViewVersion: 1, BuildStartedAt: staleStartedAt,
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 5})
	engine.setStat(staleSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 1})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1000, StaleBuildingAfter: 10 * time.Minute}, neverDoneBackfill)

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected stale building cleanup to count as 1 rotation, got %d", rotated)
	}
	if !engine.wasRemoved(staleSlot) {
		t.Fatalf("expected stale building index %s to be removed", staleSlot)
	}

	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetBuildingResult() != "" {
		t.Fatalf("expected building_result cleared, got %q", updated.GetBuildingResult())
	}
	if updated.GetActiveResult() != activeSlot {
		t.Fatalf("expected active_result to be untouched by stale cleanup, got %q", updated.GetActiveResult())
	}
}

func TestRotation_FailedBuildingClearedAndRemoved(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_failed"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	failedSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 1, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "failed",
		BuildingResult: failedSlot, BuildingViewVersion: 1, BuildError: "boom",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 5})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1000}, neverDoneBackfill)

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected failed building cleanup to count as 1 rotation, got %d", rotated)
	}
	if !engine.wasRemoved(failedSlot) {
		t.Fatalf("expected failed building index %s to be removed", failedSlot)
	}
	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetBuildingResult() != "" {
		t.Fatalf("expected building_result cleared after failure cleanup, got %q", updated.GetBuildingResult())
	}
	if updated.GetBuildStatus() != "active" {
		t.Fatalf("expected build_status active once active_result exists, got %q", updated.GetBuildStatus())
	}
}

func TestRotation_ReadySwitchKeepsOldActiveUntilComplete(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_ready"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	warmingSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 10})

	// First pass: backfill has not completed, so the switch must not happen
	// yet and the old active_result must keep serving reads.
	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1_000_000, MinReadyEntries: 0, RemoveGrace: 0}, neverDoneBackfill)
	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 0 {
		t.Fatalf("expected no rotation while backfill is pending, got %d", rotated)
	}
	mid, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if mid.GetActiveResult() != activeSlot {
		t.Fatalf("expected old active_result %s to keep serving before backfill completes, got %s", activeSlot, mid.GetActiveResult())
	}
	if mid.GetBuildingResult() != warmingSlot {
		t.Fatalf("expected warming slot %s to be claimed, got %q", warmingSlot, mid.GetBuildingResult())
	}
	if engine.wasRemoved(activeSlot) {
		t.Fatalf("old active_result must not be removed before switch")
	}

	// Second pass: backfill hook now reports done, so the switch must happen
	// and only then should the old active be queued/removed.
	mgr2 := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1_000_000, MinReadyEntries: 0, RemoveGrace: 0}, alwaysDoneBackfill)
	rotated, err = mgr2.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected switch to count as 1 rotation, got %d", rotated)
	}
	final, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if final.GetActiveResult() != warmingSlot {
		t.Fatalf("expected active_result to switch to %s after backfill completes, got %s", warmingSlot, final.GetActiveResult())
	}
	if !engine.wasRemoved(activeSlot) {
		t.Fatalf("expected old active_result %s to be grace-removed after switch", activeSlot)
	}
}

func TestRotation_SmallViewSwitchesBelowMinReadyEntries(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_small"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	warmingSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	// Both the active and warming indexes are tiny (3 and 5 entries), far
	// below min_ready_entries (1000); the small-view guard should still let
	// the switch happen once backfill is done.
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 3})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1_000_000, MinReadyEntries: 1000, RemoveGrace: 0}, func(ctx context.Context, engine viewindex.ViewIndexEngine, item *pb.View, indexID string) (bool, error) {
			engine.(*fakeEngine).setStat(indexID, viewindex.ViewIndexStats{Exists: true, EntryCount: 5})
			return true, nil
		})

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected small view to switch despite being below min_ready_entries, got %d", rotated)
	}
	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetActiveResult() != warmingSlot {
		t.Fatalf("expected small view active_result to switch to %s, got %s", warmingSlot, updated.GetActiveResult())
	}
}

func TestRotation_TimeSeriesViewUsesDuckDBEngine(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_ts"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	duckdb := newFakeEngine("duckdb")
	duckdb.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 5})
	bleve := newFakeEngine("bleve")

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": duckdb, "bleve": bleve},
		RotationConfig{Enabled: true, MaxEntries: 1_000_000}, alwaysDoneBackfill)

	if _, err := mgr.RotateViewIndexes(ctx, spaceID); err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if len(duckdb.prepared) == 0 {
		t.Fatalf("expected TimeSeries view to warm through the duckdb engine")
	}
	if len(bleve.prepared) != 0 {
		t.Fatalf("expected bleve engine to be untouched for a TimeSeries view, prepared=%v", bleve.prepared)
	}
}

func TestRotation_RecordViewUsesBleveEngine(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_record"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "bleve",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	duckdb := newFakeEngine("duckdb")
	bleve := newFakeEngine("bleve")
	bleve.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 5})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": duckdb, "bleve": bleve},
		RotationConfig{Enabled: true, MaxEntries: 1_000_000}, alwaysDoneBackfill)

	if _, err := mgr.RotateViewIndexes(ctx, spaceID); err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if len(bleve.prepared) == 0 {
		t.Fatalf("expected Record view to warm through the bleve engine")
	}
	if len(duckdb.prepared) != 0 {
		t.Fatalf("expected duckdb engine to be untouched for a Record view, prepared=%v", duckdb.prepared)
	}
}

func TestRotation_BeginViewBuildConditionalClaimBlocksSecondReplica(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_claim"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	warmingSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 1, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "building",
		// A different replica already owns the warming claim for this
		// target version under a different result name.
		BuildingResult: "replica-a-result", BuildingViewVersion: 1,
		BuildStartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	// Force this replica to attempt claiming a *different* result for the
	// same target version, mimicking a second view_builder replica racing
	// to start warming independently.
	if _, err := metadata.BeginViewBuild(ctx, spaceID, viewID, 1, warmingSlot); err == nil {
		t.Fatalf("expected BeginViewBuild to reject a competing claim for the same target version")
	}

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 5})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: true, MaxEntries: 1000, StaleBuildingAfter: time.Hour}, alwaysDoneBackfill)

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 0 {
		t.Fatalf("expected no rotation while another replica's claim is still fresh, got %d", rotated)
	}
	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetBuildingResult() != "replica-a-result" {
		t.Fatalf("expected the existing replica's claim to remain untouched, got %q", updated.GetBuildingResult())
	}
	if updated.GetActiveResult() != activeSlot {
		t.Fatalf("expected active_result unchanged while claim is contested, got %q", updated.GetActiveResult())
	}
}

func TestRotation_DisabledIsFullKillSwitch(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_disabled"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 10_000_000})

	mgr := newTestRotationManager(metadata, map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		RotationConfig{Enabled: false, MaxEntries: 1}, alwaysDoneBackfill)

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 0 {
		t.Fatalf("expected disabled rotation to be a full no-op, got %d rotated", rotated)
	}
	if len(engine.prepared) != 0 {
		t.Fatalf("expected no Prepare calls while rotation is disabled, prepared=%v", engine.prepared)
	}
	if len(engine.removed) != 0 {
		t.Fatalf("expected no Remove calls while rotation is disabled, removed=%v", engine.removed)
	}
}

// cancelAfterNGetView wraps fakeMetadata to simulate a concurrent
// cancellation of an in-progress warming claim (e.g. another rotate pass
// clearing a stale/obsolete building pointer) exactly after `remaining`
// GetView calls for the target View. primaryStoreBackfill.buildingStillValid
// calls GetView once per page before writing, so this lets Task 7
// cancellation tests deterministically cancel mid-backfill.
type cancelAfterNGetView struct {
	*fakeMetadata
	spaceID string
	viewID  string

	mu        sync.Mutex
	remaining int
}

func (c *cancelAfterNGetView) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	item, err := c.fakeMetadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		return nil, err
	}
	if spaceID != c.spaceID || viewID != c.viewID {
		return item, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.remaining <= 0 {
		return item, nil
	}
	c.remaining--
	if c.remaining > 0 {
		return item, nil
	}
	// Simulate a concurrent rotate pass invalidating this claim mid-scan
	// (e.g. a schema bump or stale-claim cleanup on another replica).
	cleared := proto.Clone(item).(*pb.View)
	cleared.BuildStatus = "failed"
	c.fakeMetadata.putView(cleared)
	return cleared, nil
}

func TestRotation_CancellationMidBackfillDoesNotSwitch(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_cancel"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")

	base := newFakeMetadata()
	base.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb", PrimaryDatasetId: "ds1",
		ViewVersion: 2, ActiveViewVersion: 1, ActiveResult: activeSlot,
		Status: "active", BuildStatus: "active",
	})
	base.putColumns(spaceID, viewID, sampleViewColumns())
	// Cancel exactly on the buildingStillValid check before the second
	// page, after the first page has already been written.
	metadata := &cancelAfterNGetView{fakeMetadata: base, spaceID: spaceID, viewID: viewID, remaining: 2}

	facts := newFakeFactReader()
	facts.pageSize = 1
	facts.setRows("ds1", []*pb.TimeSeriesRow{
		tsRow("ds1", "sub1", "2026-07-09T00:00:00Z"),
		tsRow("ds1", "sub1", "2026-07-09T00:01:00Z"),
		tsRow("ds1", "sub1", "2026-07-09T00:02:00Z"),
	})

	engine := newFakeEngine("duckdb")
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 3})

	mgr := NewRotationManager(RotationOptions{
		Metadata: metadata,
		Engines:  map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		Config:   RotationConfig{Enabled: true, MaxEntries: 1_000_000, MinReadyEntries: 0, DefaultBackfillWindow: 24 * time.Hour},
		Facts:    facts,
	})

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 0 {
		t.Fatalf("expected mid-backfill cancellation to prevent the switch, got rotated=%d", rotated)
	}
	updated, err := metadata.fakeMetadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetActiveResult() != activeSlot {
		t.Fatalf("expected active_result to remain %s after cancellation, got %q", activeSlot, updated.GetActiveResult())
	}
	if engine.writeCalls == 0 {
		t.Fatalf("expected at least one page to be written before cancellation was detected")
	}
	if engine.writeCalls >= 3 {
		t.Fatalf("expected cancellation to stop before all 3 rows were written, got %d write calls", engine.writeCalls)
	}
}

func TestRotation_SmallViewSwitchesAfterScanCompleteBelowMinReadyEntries(t *testing.T) {
	ctx := context.Background()
	spaceID, viewID := "space1", "view_small_real_backfill"
	activeSlot := viewindex.ViewIndexID(spaceID, viewID, "a")
	warmingSlot := viewindex.ViewIndexID(spaceID, viewID, "b")

	metadata := newFakeMetadata()
	metadata.putView(&pb.View{
		SpaceId: spaceID, ViewId: viewID, Engine: "duckdb",
		PrimaryDatasetId: "ds1", ViewVersion: 2, ActiveViewVersion: 1,
		ActiveResult: activeSlot, Status: "active", BuildStatus: "active",
	})
	metadata.putColumns(spaceID, viewID, sampleViewColumns())

	engine := newFakeEngine("duckdb")
	// Both the active index and the real PrimaryStore backfill scan are
	// tiny (3 and 5 rows), far below min_ready_entries (1000); the
	// small-view guard should still let the switch happen once the real
	// backfill scan completes.
	engine.setStat(activeSlot, viewindex.ViewIndexStats{Exists: true, EntryCount: 3})

	facts := newFakeFactReader()
	facts.setRows("ds1", []*pb.TimeSeriesRow{
		tsRow("ds1", "sub1", "2026-07-09T00:00:00Z"),
		tsRow("ds1", "sub1", "2026-07-09T00:01:00Z"),
		tsRow("ds1", "sub1", "2026-07-09T00:02:00Z"),
		tsRow("ds1", "sub1", "2026-07-09T00:03:00Z"),
		tsRow("ds1", "sub1", "2026-07-09T00:04:00Z"),
	})

	mgr := NewRotationManager(RotationOptions{
		Metadata: metadata,
		Engines:  map[string]viewindex.ViewIndexEngine{"duckdb": engine},
		Config:   RotationConfig{Enabled: true, MaxEntries: 1_000_000, MinReadyEntries: 1000, RemoveGrace: 0, DefaultBackfillWindow: 24 * time.Hour},
		Facts:    facts,
	})

	rotated, err := mgr.RotateViewIndexes(ctx, spaceID)
	if err != nil {
		t.Fatalf("RotateViewIndexes returned error: %v", err)
	}
	if rotated != 1 {
		t.Fatalf("expected small view to switch after the real backfill scan completes, got %d", rotated)
	}
	updated, err := metadata.GetView(ctx, spaceID, viewID)
	if err != nil {
		t.Fatalf("GetView failed: %v", err)
	}
	if updated.GetActiveResult() != warmingSlot {
		t.Fatalf("expected active_result to switch to %s, got %s", warmingSlot, updated.GetActiveResult())
	}
}
