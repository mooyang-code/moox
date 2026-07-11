package view

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMaintenanceBuildsAndActivatesMissingTimeSeriesIndex(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	facts := &maintenanceFacts{timeRows: []*pb.TimeSeriesRow{maintenanceTimeRow("1m", now.Add(-time.Minute))}}
	engine := newMaintenanceEngine("duckdb")
	activeStayedEmptyDuringWrite := false
	engine.onWrite = func() {
		item, _ := metadata.GetView(ctx, "crypto", "market_view")
		activeStayedEmptyDuringWrite = item.GetActiveIndexId() == ""
	}
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata,
		Engines:  map[string]ManagedViewIndex{"duckdb": engine},
		Facts:    facts,
		Records:  facts,
		Now:      func() time.Time { return now },
		Config:   maintenanceTestConfig(),
	})

	changed, err := manager.MaintainViewIndexes(ctx, "crypto")
	if err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if changed != 1 || !activeStayedEmptyDuringWrite {
		t.Fatalf("changed=%d activeStayedEmptyDuringWrite=%v", changed, activeStayedEmptyDuringWrite)
	}
	item, err := metadata.GetView(ctx, "crypto", "market_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	wantIndex := viewindex.ViewIndexID("crypto", "market_view", viewindex.SlotA)
	if item.GetActiveIndexId() != wantIndex || item.GetActiveViewVersion() != item.GetViewVersion() || item.GetIndexBuild() != nil {
		t.Fatalf("activated view = %+v", item)
	}
	if len(item.GetActiveColumns()) != 1 || item.GetActiveSchemaHash() == "" {
		t.Fatalf("active schema = %+v/%q", item.GetActiveColumns(), item.GetActiveSchemaHash())
	}
	if len(facts.timeRanges) < 2 {
		t.Fatalf("scan ranges = %d, want backfill and catch-up", len(facts.timeRanges))
	}
	wantStart := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
	if facts.timeRanges[0].GetStartTime() != wantStart {
		t.Fatalf("backfill start = %q, want %q", facts.timeRanges[0].GetStartTime(), wantStart)
	}
}

func TestMaintenanceUsesActualViewFrequencyRetention(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1d"}`, "")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	facts := &maintenanceFacts{}
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata,
		Engines:  map[string]ManagedViewIndex{"duckdb": newMaintenanceEngine("duckdb")},
		Facts:    facts, Records: facts, Now: func() time.Time { return now }, Config: maintenanceTestConfig(),
	})
	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	wantStart := now.Add(-730 * 24 * time.Hour).Format(time.RFC3339Nano)
	if len(facts.timeRanges) == 0 || facts.timeRanges[0].GetStartTime() != wantStart {
		t.Fatalf("first range = %+v, want start %s", facts.timeRanges, wantStart)
	}
}

func TestMaintenanceCatchUpOverlapCoversViewFrequency(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1d"}`, "")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	facts := &maintenanceFacts{timeRows: []*pb.TimeSeriesRow{maintenanceTimeRow("1d", now)}}
	cfg := maintenanceTestConfig()
	cfg.OverlapWindow = 30 * time.Minute
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": newMaintenanceEngine("duckdb")},
		Facts: facts, Records: facts, Now: func() time.Time { return now }, Config: cfg,
	})
	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if len(facts.timeRanges) < 2 {
		t.Fatalf("scan ranges = %d, want backfill and catch-up", len(facts.timeRanges))
	}
	wantStart := now.Add(-24 * time.Hour).Format(time.RFC3339Nano)
	if got := facts.timeRanges[1].GetStartTime(); got != wantStart {
		t.Fatalf("catch-up start = %q, want %q to cover 1d updates", got, wantStart)
	}
}

func TestMaintenanceRejectsInvalidExplicitRetentionWindow(t *testing.T) {
	manager := NewMaintenanceManager(MaintenanceOptions{Config: maintenanceTestConfig()})
	if _, err := manager.retentionWindow(&pb.View{Engine: "duckdb", RetentionWindow: "not-a-duration"}); err == nil {
		t.Fatal("invalid explicit retention window was silently replaced by a default")
	}
}

func TestMaintenanceSkipsOverlappingPassInSameProcess(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	started := make(chan struct{})
	release := make(chan struct{})
	facts := &maintenanceFacts{timeRows: []*pb.TimeSeriesRow{maintenanceTimeRow("1m", now)}}
	facts.onTimeScan = func(call int) {
		if call == 0 {
			close(started)
			<-release
		}
	}
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": newMaintenanceEngine("duckdb")},
		Facts: facts, Records: facts, Now: func() time.Time { return now }, Config: maintenanceTestConfig(),
	})
	first := make(chan error, 1)
	go func() {
		_, err := manager.MaintainViewIndexes(ctx, "crypto")
		first <- err
	}()
	<-started

	secondDone := make(chan struct{})
	var secondChanged int
	var secondErr error
	go func() {
		secondChanged, secondErr = manager.MaintainViewIndexes(ctx, "crypto")
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("overlapping maintenance pass waited instead of being skipped")
	}
	if secondErr != nil || secondChanged != 0 {
		t.Fatalf("overlapping pass changed=%d err=%v", secondChanged, secondErr)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first maintenance pass: %v", err)
	}
}

func TestMaintenanceDoesNotLoopWhenActiveCoverageAlreadyMatchesRetention(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	activeID := viewindex.ViewIndexID("crypto", "market_view", viewindex.SlotA)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, activeID)
	item, err := metadata.GetView(ctx, "crypto", "market_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	engine := newMaintenanceEngine("duckdb")
	engine.stats[activeID] = viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, EntryCount: 5000, PhysicalBytes: 1024, SchemaHash: item.GetActiveSchemaHash()}
	cfg := maintenanceTestConfig()
	cfg.MaxEntries = 1000
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": engine},
		Facts: &maintenanceFacts{}, Records: &maintenanceFacts{}, Now: func() time.Time { return now }, Config: cfg,
	})
	changed, err := manager.MaintainViewIndexes(ctx, "crypto")
	if err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if changed != 0 || len(engine.prepared) != 0 {
		t.Fatalf("changed=%d prepared=%v, want pressure without rebuild loop", changed, engine.prepared)
	}
}

func TestMaintenanceSwitchesCapacityWhenOldIndexCanShrinkToTarget(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	activeID := viewindex.ViewIndexID("crypto", "market_view", viewindex.SlotA)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, activeID)
	item, err := metadata.GetView(ctx, "crypto", "market_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	engine := newMaintenanceEngine("duckdb")
	engine.stats[activeID] = viewindex.ViewIndexStats{
		Exists: true, ViewVersion: 1, EntryCount: 2000, PhysicalBytes: 1024, SchemaHash: item.GetActiveSchemaHash(),
		MinVersion: now.Add(-48 * time.Hour).Format(time.RFC3339Nano),
		MaxVersion: now.Format(time.RFC3339Nano),
	}
	cfg := maintenanceTestConfig()
	cfg.MaxEntries = 1000
	cfg.TargetEntries = 1500
	cfg.MinReadyEntries = 1
	facts := &maintenanceFacts{timeRows: []*pb.TimeSeriesRow{maintenanceTimeRow("1m", now)}}
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": engine},
		Facts: facts, Records: facts, Now: func() time.Time { return now }, Config: cfg,
	})

	changed, err := manager.MaintainViewIndexes(ctx, "crypto")
	if err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if changed != 1 || len(engine.prepared) != 1 {
		t.Fatalf("changed=%d prepared=%v, want one capacity switch", changed, engine.prepared)
	}
	item, err = metadata.GetView(ctx, "crypto", "market_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	if item.GetActiveIndexId() != viewindex.ViewIndexID("crypto", "market_view", viewindex.SlotB) {
		t.Fatalf("active index = %q, want slot b", item.GetActiveIndexId())
	}
}

func TestMaintenanceRejectsBuildWhenOwnerDiskIsBelowFloor(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "")
	engine := newMaintenanceEngine("duckdb")
	engine.freeDiskBytes = 99
	cfg := maintenanceTestConfig()
	cfg.MinFreeDiskBytes = 100
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": engine},
		Facts: &maintenanceFacts{}, Records: &maintenanceFacts{}, Config: cfg,
	})

	_, err := manager.MaintainViewIndexes(ctx, "crypto")
	if err == nil || !strings.Contains(err.Error(), "free disk") {
		t.Fatalf("MaintainViewIndexes error = %v, want free disk admission error", err)
	}
	if len(engine.prepared) != 0 {
		t.Fatalf("prepared indexes = %v, want none", engine.prepared)
	}
}

func TestMaintenanceBoundsViewsProcessedPerRun(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceNamedView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "", "first_view")
	seedMaintenanceNamedView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "", "second_view")
	engine := newMaintenanceEngine("duckdb")
	cfg := maintenanceTestConfig()
	cfg.MaxViewsPerRun = 1
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": engine},
		Facts: &maintenanceFacts{}, Records: &maintenanceFacts{}, Config: cfg,
	})

	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if len(engine.prepared) != 1 {
		t.Fatalf("prepared indexes = %v, want exactly one View", engine.prepared)
	}
}

func TestMaintenanceRoundRobinsViewsAcrossRuns(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	for _, viewID := range []string{"first_view", "second_view", "third_view"} {
		seedMaintenanceNamedView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "", viewID)
	}
	engine := newMaintenanceEngine("duckdb")
	cfg := maintenanceTestConfig()
	cfg.MaxViewsPerRun = 1
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": engine},
		Facts: &maintenanceFacts{}, Records: &maintenanceFacts{}, Config: cfg,
	})

	for run := 0; run < 3; run++ {
		if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
			t.Fatalf("MaintainViewIndexes run %d: %v", run+1, err)
		}
	}
	preparedViews := make(map[string]bool)
	for _, indexID := range engine.prepared {
		ref, err := viewindex.ParseViewIndexID(indexID)
		if err != nil {
			t.Fatalf("ParseViewIndexID(%q): %v", indexID, err)
		}
		preparedViews[ref.ViewID] = true
	}
	if len(preparedViews) != 3 {
		t.Fatalf("prepared Views = %v, want all three Views across three runs", preparedViews)
	}
}

func TestMaintenanceCatchUpKeepsOneDurableSourceEndAcrossPages(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, "")
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	facts := &maintenanceFacts{
		timePages: []*pb.PageResult{
			{Page: 1, Size: 100},
			{Page: 1, Size: 100, HasMore: true, NextCursor: "catch-up-page-2"},
			{Page: 2, Size: 100},
		},
	}
	facts.onTimeScan = func(call int) {
		if call == 1 {
			now = now.Add(10 * time.Minute)
		}
	}
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": newMaintenanceEngine("duckdb")},
		Facts: facts, Records: facts, Now: func() time.Time { return now }, Config: maintenanceTestConfig(),
	})

	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if len(facts.timeRanges) != 3 {
		t.Fatalf("scan ranges = %d, want backfill plus two catch-up pages", len(facts.timeRanges))
	}
	if facts.timeRanges[1].GetEndTime() != facts.timeRanges[2].GetEndTime() {
		t.Fatalf("catch-up source end changed across pages: %q != %q", facts.timeRanges[1].GetEndTime(), facts.timeRanges[2].GetEndTime())
	}
}

func TestMaintenanceRemovesUnreferencedIndexAfterGrace(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	activeID := viewindex.ViewIndexID("crypto", "market_view", viewindex.SlotA)
	orphanID := viewindex.ViewIndexID("crypto", "orphan_view", viewindex.SlotB)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, activeID)
	item, err := metadata.GetView(ctx, "crypto", "market_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	engine := newMaintenanceEngine("duckdb")
	engine.stats[activeID] = viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: item.GetActiveSchemaHash()}
	engine.stats[orphanID] = viewindex.ViewIndexStats{Exists: true, SchemaHash: "orphan"}
	engine.keys[activeID] = map[string]bool{}
	engine.keys[orphanID] = map[string]bool{}
	cfg := maintenanceTestConfig()
	cfg.RemoveGrace = time.Minute
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata, Engines: map[string]ManagedViewIndex{"duckdb": engine},
		Facts: &maintenanceFacts{}, Records: &maintenanceFacts{}, Now: func() time.Time { return now }, Config: cfg,
	})

	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("first MaintainViewIndexes: %v", err)
	}
	if !engine.stats[orphanID].Exists {
		t.Fatal("orphan removed before grace elapsed")
	}
	now = now.Add(2 * time.Minute)
	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("second MaintainViewIndexes: %v", err)
	}
	if _, exists := engine.stats[orphanID]; exists {
		t.Fatal("orphan still exists after grace elapsed")
	}
}

func TestMaintenanceRemovesIndexFromAnOldEngine(t *testing.T) {
	ctx := context.Background()
	metadata := openMaintenanceMetadata(t, ctx)
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	activeID := viewindex.ViewIndexID("crypto", "market_view", viewindex.SlotA)
	seedMaintenanceView(t, ctx, metadata, pb.DataKind_DATA_KIND_TIME_SERIES, "duckdb", `{"freq":"1m"}`, activeID)
	item, err := metadata.GetView(ctx, "crypto", "market_view")
	if err != nil {
		t.Fatalf("GetView: %v", err)
	}
	duck := newMaintenanceEngine("duckdb")
	duck.stats[activeID] = viewindex.ViewIndexStats{Exists: true, ViewVersion: 1, SchemaHash: item.GetActiveSchemaHash()}
	duck.keys[activeID] = map[string]bool{}
	bleve := newMaintenanceEngine("bleve")
	bleve.stats[activeID] = viewindex.ViewIndexStats{Exists: true, SchemaHash: "old-engine"}
	bleve.keys[activeID] = map[string]bool{}
	cfg := maintenanceTestConfig()
	cfg.RemoveGrace = 0
	manager := NewMaintenanceManager(MaintenanceOptions{
		Metadata: metadata,
		Engines:  map[string]ManagedViewIndex{"duckdb": duck, "bleve": bleve},
		Facts:    &maintenanceFacts{}, Records: &maintenanceFacts{}, Now: func() time.Time { return now }, Config: cfg,
	})

	if _, err := manager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("MaintainViewIndexes: %v", err)
	}
	if _, exists := bleve.stats[activeID]; exists {
		t.Fatal("old-engine index was retained because the active index ID matched")
	}
}

func maintenanceTestConfig() MaintenanceConfig {
	return MaintenanceConfig{
		Enabled: true, OwnerID: "builder-1", LeaseTTL: 90 * time.Second, RunBudget: 20 * time.Second,
		PageSize: 100, MaxEntries: 200000, TargetEntries: 150000, MaxPhysicalBytes: 512 << 20,
		MinReadyEntries: 1000, AllowedLag: 2 * time.Minute, OverlapWindow: 30 * time.Minute,
		TimeSeriesDefaultRetention: 7 * 24 * time.Hour,
		TimeSeriesRetentionByFreq:  map[string]time.Duration{"1m": 24 * time.Hour, "1h": 90 * 24 * time.Hour, "1d": 730 * 24 * time.Hour},
		RecordRetention:            30 * 24 * time.Hour,
	}
}

func openMaintenanceMetadata(t *testing.T, ctx context.Context) *metasqlite.Store {
	t.Helper()
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path: filepath.Join(t.TempDir(), "metadata.db"), SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatalf("Open metadata: %v", err)
	}
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedMaintenanceView(t *testing.T, ctx context.Context, store *metasqlite.Store, kind pb.DataKind, engine string, filterJSON string, activeIndexID string) {
	seedMaintenanceNamedView(t, ctx, store, kind, engine, filterJSON, activeIndexID, "market_view")
}

func seedMaintenanceNamedView(t *testing.T, ctx context.Context, store *metasqlite.Store, kind pb.DataKind, engine string, filterJSON string, activeIndexID string, viewID string) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "source", Name: "Source", Kind: "exchange", Status: "active"}); err != nil {
		t.Fatalf("UpsertDataSource: %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{
		SpaceId: "crypto", DatasetId: "dataset", DataSourceId: "source", Name: "Dataset", DataKind: kind,
		Freqs: []string{"1m", "1h", "1d"}, Status: "active",
	}); err != nil {
		t.Fatalf("UpsertDataset: %v", err)
	}
	view := &pb.View{
		SpaceId: "crypto", ViewId: viewID, Name: "Market " + viewID, PrimaryDatasetId: "dataset",
		DatasetIds: []string{"dataset"}, Engine: engine, FilterJson: filterJSON, Status: "active",
		ViewVersion: 1, ActiveIndexId: activeIndexID,
		Columns: []*pb.ViewColumn{{
			SpaceId: "crypto", ViewId: viewID, ColumnName: "close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "dataset.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}},
	}
	if activeIndexID != "" {
		view.ActiveViewVersion = 1
		view.ActiveColumns = view.Columns
		view.ActiveCoverageStart = "2026-07-09T12:00:00Z"
		view.ActiveCoverageEnd = "2026-07-10T12:00:00Z"
		view.ActiveSchemaHash = viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{
			SpaceID: "crypto", ViewID: viewID, Engine: engine, Columns: view.Columns,
		})
	}
	if _, err := store.UpsertView(ctx, view); err != nil {
		t.Fatalf("UpsertView: %v", err)
	}
}

type maintenanceFacts struct {
	timeRows   []*pb.TimeSeriesRow
	recordRows []*pb.RecordRow
	timeRanges []*pb.TimeRange
	timePages  []*pb.PageResult
	onTimeScan func(int)
	timeScans  int
}

func (f *maintenanceFacts) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (f *maintenanceFacts) ScanTimeSeriesRows(_ context.Context, _ string, _ string, timeRange *pb.TimeRange, _ []string, _ *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	f.timeRanges = append(f.timeRanges, timeRange)
	call := f.timeScans
	f.timeScans++
	if f.onTimeScan != nil {
		f.onTimeScan(call)
	}
	if call < len(f.timePages) {
		return f.timeRows, f.timePages[call], nil
	}
	return f.timeRows, &pb.PageResult{Page: 1, Size: 100}, nil
}

func (f *maintenanceFacts) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}}, nil
}

func (f *maintenanceFacts) ScanRecordRows(_ context.Context, _ string, _ string, _ []string, _ *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return f.recordRows, &pb.PageResult{Page: 1, Size: 100}, nil
}

type maintenanceEngine struct {
	name          string
	prepared      []string
	stats         map[string]viewindex.ViewIndexStats
	keys          map[string]map[string]bool
	onWrite       func()
	freeDiskBytes uint64
}

func newMaintenanceEngine(name string) *maintenanceEngine {
	return &maintenanceEngine{name: name, stats: make(map[string]viewindex.ViewIndexStats), keys: make(map[string]map[string]bool)}
}

func (e *maintenanceEngine) Engine() string { return e.name }

func (e *maintenanceEngine) Prepare(_ context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	e.prepared = append(e.prepared, indexID)
	e.stats[indexID] = viewindex.ViewIndexStats{Exists: true, ViewVersion: schema.ViewVersion, SchemaHash: schema.SchemaHash}
	e.keys[indexID] = make(map[string]bool)
	return nil
}

func (e *maintenanceEngine) Write(_ context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if e.onWrite != nil {
		e.onWrite()
	}
	keys := e.keys[indexID]
	stat := e.stats[indexID]
	for _, row := range batch.TimeSeriesRows {
		key := row.GetKey().GetSubjectId() + "|" + row.GetKey().GetFreq() + "|" + row.GetKey().GetDataTime()
		keys[key] = true
		version := row.GetKey().GetDataTime()
		if stat.MinVersion == "" || version < stat.MinVersion {
			stat.MinVersion = version
		}
		if stat.MaxVersion == "" || version > stat.MaxVersion {
			stat.MaxVersion = version
		}
	}
	for _, row := range batch.RecordRows {
		key := row.GetKey().GetRecordId() + "|" + fmt.Sprint(row.GetRevision())
		keys[key] = true
		version := row.GetUpdatedAt()
		if stat.MinVersion == "" || version < stat.MinVersion {
			stat.MinVersion = version
		}
		if stat.MaxVersion == "" || version > stat.MaxVersion {
			stat.MaxVersion = version
		}
	}
	stat.EntryCount = int64(len(keys))
	e.stats[indexID] = stat
	return nil
}

func (e *maintenanceEngine) Stat(_ context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	stats := e.stats[indexID]
	stats.FreeDiskBytes = e.freeDiskBytes
	return stats, nil
}

func (e *maintenanceEngine) Remove(_ context.Context, indexID string) error {
	delete(e.stats, indexID)
	delete(e.keys, indexID)
	return nil
}

func (e *maintenanceEngine) List(context.Context) ([]string, error) {
	out := make([]string, 0, len(e.stats))
	for indexID := range e.stats {
		out = append(out, indexID)
	}
	return out, nil
}

func maintenanceTimeRow(freq string, at time.Time) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "dataset", SubjectId: "BTC-USDT", Freq: freq, DataTime: at.Format(time.RFC3339Nano)},
		Columns: []*pb.ColumnValue{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}},
	}
}
