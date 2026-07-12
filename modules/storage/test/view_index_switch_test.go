//go:build integration && cgo

package tests

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/duckdb"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	viewsvc "github.com/mooyang-code/moox/modules/storage/internal/service/view"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestViewIndexDualDatabaseSwitch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	metadata := openIntegrationMetadata(t, ctx, root)
	facts := seedIntegrationMetadata(t, ctx, metadata, now)
	duck, err := duckdb.OpenIndexManager(duckdb.IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("OpenIndexManager: %v", err)
	}
	records := search.NewService(search.Options{Root: root})
	t.Cleanup(func() {
		_ = duck.Close()
		_ = records.Close()
	})

	cfg := integrationMaintenanceConfig()
	manager := viewsvc.NewMaintenanceManager(viewsvc.MaintenanceOptions{
		Metadata: metadata,
		Engines:  map[string]viewsvc.ManagedViewIndex{"duckdb": duck, "bleve": records},
		Facts:    facts,
		Records:  facts,
		Config:   cfg,
		Now:      func() time.Time { return now },
	})
	changed, err := manager.MaintainViewIndexes(ctx, "crypto")
	if err != nil || changed != 2 {
		t.Fatalf("initial MaintainViewIndexes changed=%d err=%v", changed, err)
	}

	query := viewsvc.NewService(viewsvc.ServiceOptions{
		Metadata: metadata, TimeSeriesIndexes: duck, RecordIndexes: records,
	})
	assertTimeSeriesQuery(t, ctx, query, 2)
	assertRecordQuery(t, ctx, query, "title", pb.ErrorCode_SUCCESS)

	// A new Record field preempts the old schema. The old active slot remains
	// queryable until the new slot is atomically activated.
	if _, err := metadata.UpsertViewColumn(ctx, integrationViewColumn("record_view", "score", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)); err != nil {
		t.Fatalf("UpsertViewColumn(score): %v", err)
	}
	assertRecordQuery(t, ctx, query, "title", pb.ErrorCode_SUCCESS)
	assertRecordQuery(t, ctx, query, "score", pb.ErrorCode_VIEW_NOT_READY)
	changed, err = manager.MaintainViewIndexes(ctx, "crypto")
	if err != nil || changed != 1 {
		t.Fatalf("schema MaintainViewIndexes changed=%d err=%v", changed, err)
	}
	assertRecordQuery(t, ctx, query, "score", pb.ErrorCode_SUCCESS)

	tsView, err := metadata.GetView(ctx, "crypto", "ts_view")
	if err != nil {
		t.Fatalf("GetView(ts_view): %v", err)
	}
	oldTimeIndex := tsView.GetActiveIndexId()
	if err := duck.Write(ctx, oldTimeIndex, viewindex.ViewIndexBatch{TimeSeriesRows: []*pb.TimeSeriesRow{
		integrationTimeRow(now.Add(-48*time.Hour), 90),
	}}); err != nil {
		t.Fatalf("write stale row into active slot: %v", err)
	}

	readableDuringSwitch := false
	capacityDuck := &callbackManagedIndex{ManagedViewIndex: duck}
	capacityDuck.onWrite = func() {
		current, getErr := metadata.GetView(ctx, "crypto", "ts_view")
		if getErr != nil || current.GetActiveIndexId() != oldTimeIndex {
			t.Fatalf("active pointer changed before backfill completed: view=%+v err=%v", current, getErr)
		}
		rsp, rpcErr := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
			SpaceId: "crypto", ViewId: "ts_view", Page: &pb.Page{Page: 1, Size: 25}, TotalMode: pb.TotalMode_NONE,
		})
		readableDuringSwitch = rpcErr == nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS && len(rsp.GetRows()) > 0
	}
	capacityCfg := cfg
	capacityCfg.MaxEntries = 2
	capacityCfg.TargetEntries = 2
	capacityManager := viewsvc.NewMaintenanceManager(viewsvc.MaintenanceOptions{
		Metadata: metadata,
		Engines:  map[string]viewsvc.ManagedViewIndex{"duckdb": capacityDuck, "bleve": records},
		Facts:    facts, Records: facts, Config: capacityCfg, Now: func() time.Time { return now },
	})
	changed, err = capacityManager.MaintainViewIndexes(ctx, "crypto")
	if err != nil || changed != 1 || !readableDuringSwitch {
		t.Fatalf("capacity MaintainViewIndexes changed=%d readable=%v err=%v", changed, readableDuringSwitch, err)
	}
	newTimeView, err := metadata.GetView(ctx, "crypto", "ts_view")
	if err != nil {
		t.Fatalf("GetView(ts_view after switch): %v", err)
	}
	if newTimeView.GetActiveIndexId() == oldTimeIndex {
		t.Fatalf("time-series active index did not switch: %q", oldTimeIndex)
	}
	assertTimeSeriesQuery(t, ctx, query, 2)

	// The old files are removed only after grace, then active indexes survive
	// owner restart and remain queryable.
	now = now.Add(2 * time.Second)
	if _, err := capacityManager.MaintainViewIndexes(ctx, "crypto"); err != nil {
		t.Fatalf("orphan cleanup MaintainViewIndexes: %v", err)
	}
	assertIndexRemoved(t, root, oldTimeIndex, "duckdb")
	recordView, err := metadata.GetView(ctx, "crypto", "record_view")
	if err != nil {
		t.Fatalf("GetView(record_view): %v", err)
	}
	oldRecordIndex := viewindex.InactiveViewIndexID("crypto", "record_view", recordView.GetActiveIndexId())
	assertIndexRemoved(t, root, oldRecordIndex, "bleve")

	if err := duck.Close(); err != nil {
		t.Fatalf("close DuckDB owner: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("close Bleve owner: %v", err)
	}
	duck, err = duckdb.OpenIndexManager(duckdb.IndexManagerOptions{Root: root})
	if err != nil {
		t.Fatalf("restart DuckDB owner: %v", err)
	}
	records = search.NewService(search.Options{Root: root})
	query = viewsvc.NewService(viewsvc.ServiceOptions{
		Metadata: metadata, TimeSeriesIndexes: duck, RecordIndexes: records,
	})
	assertTimeSeriesQuery(t, ctx, query, 2)
	assertRecordQuery(t, ctx, query, "score", pb.ErrorCode_SUCCESS)
}

type callbackManagedIndex struct {
	viewsvc.ManagedViewIndex
	onWrite func()
}

func (e *callbackManagedIndex) Write(ctx context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if e.onWrite != nil {
		e.onWrite()
	}
	return e.ManagedViewIndex.Write(ctx, indexID, batch)
}

type integrationFacts struct {
	timeRows   []*pb.TimeSeriesRow
	recordRows []*pb.RecordRow
}

func (f *integrationFacts) ReadTimeSeriesRows(_ context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: integrationSuccess(), Rows: selectIntegrationTimeRows(f.timeRows, req.GetKeys(), nil)}, nil
}

func (f *integrationFacts) ScanTimeSeriesRows(_ context.Context, _ string, _ string, timeRange *pb.TimeRange, _ []string, _ *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return selectIntegrationTimeRows(f.timeRows, nil, timeRange), &pb.PageResult{Page: 1, Size: 100}, nil
}

func (f *integrationFacts) ReadRecordRows(_ context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	return &pb.ReadRecordRowsRsp{RetInfo: integrationSuccess(), Rows: selectIntegrationRecordRows(f.recordRows, req.GetKeys(), nil)}, nil
}

func (f *integrationFacts) ScanRecordRows(_ context.Context, _ string, _ string, versionRange *pb.VersionRange, _ []string, _ *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return selectIntegrationRecordRows(f.recordRows, nil, versionRange), &pb.PageResult{Page: 1, Size: 100}, nil
}

func selectIntegrationTimeRows(rows []*pb.TimeSeriesRow, keys []*pb.TimeSeriesKey, timeRange *pb.TimeRange) []*pb.TimeSeriesRow {
	var out []*pb.TimeSeriesRow
	for _, row := range rows {
		if len(keys) > 0 && !containsIntegrationTimeKey(keys, row.GetKey()) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
		if err != nil || !integrationTimeInRange(at, timeRange.GetStartTime(), timeRange.GetEndTime()) {
			continue
		}
		out = append(out, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return out
}

func selectIntegrationRecordRows(rows []*pb.RecordRow, keys []*pb.RecordKey, versionRange *pb.VersionRange) []*pb.RecordRow {
	var out []*pb.RecordRow
	for _, row := range rows {
		if len(keys) > 0 && !containsIntegrationRecordKey(keys, row.GetKey()) {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, row.GetKey().GetVersion())
		if err != nil || !integrationTimeInRange(at, versionRange.GetStartVersion(), versionRange.GetEndVersion()) {
			continue
		}
		out = append(out, proto.Clone(row).(*pb.RecordRow))
	}
	return out
}

func integrationTimeInRange(at time.Time, start string, end string) bool {
	if start != "" {
		value, err := time.Parse(time.RFC3339Nano, start)
		if err != nil || at.Before(value) {
			return false
		}
	}
	if end != "" {
		value, err := time.Parse(time.RFC3339Nano, end)
		if err != nil || at.After(value) {
			return false
		}
	}
	return true
}

func containsIntegrationTimeKey(keys []*pb.TimeSeriesKey, want *pb.TimeSeriesKey) bool {
	for _, key := range keys {
		if key.GetDatasetId() == want.GetDatasetId() && key.GetSubjectId() == want.GetSubjectId() && key.GetFreq() == want.GetFreq() && key.GetDataTime() == want.GetDataTime() {
			return true
		}
	}
	return false
}

func containsIntegrationRecordKey(keys []*pb.RecordKey, want *pb.RecordKey) bool {
	for _, key := range keys {
		if key.GetDatasetId() == want.GetDatasetId() && key.GetRecordId() == want.GetRecordId() && key.GetVersion() == want.GetVersion() {
			return true
		}
	}
	return false
}

func openIntegrationMetadata(t *testing.T, ctx context.Context, root string) *metasqlite.Store {
	t.Helper()
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path: filepath.Join(root, "metadata.db"), SchemaPath: filepath.Join("..", "schema", "metadata.sql"),
	})
	if err != nil {
		t.Fatalf("open metadata: %v", err)
	}
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("init metadata: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedIntegrationMetadata(t *testing.T, ctx context.Context, store *metasqlite.Store, now time.Time) *integrationFacts {
	t.Helper()
	mustUpsertSpace(t, ctx, store)
	if _, err := store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "source", Name: "Source", Kind: "internal", Status: "active"}); err != nil {
		t.Fatalf("UpsertDataSource: %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "ts", DataSourceId: "source", Name: "TS", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"}); err != nil {
		t.Fatalf("UpsertDataset(ts): %v", err)
	}
	if _, err := store.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "records", DataSourceId: "source", Name: "Records", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}); err != nil {
		t.Fatalf("UpsertDataset(records): %v", err)
	}
	tsColumn := integrationViewColumn("ts_view", "close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE)
	tsColumn.OriginId = "ts.close"
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "crypto", ViewId: "ts_view", Name: "TS View", PrimaryDatasetId: "ts", DatasetIds: []string{"ts"},
		FilterJson: `{"freq":"1m"}`, Engine: "duckdb", RetentionWindow: "24h", Status: "active", Columns: []*pb.ViewColumn{tsColumn},
	}); err != nil {
		t.Fatalf("UpsertView(ts): %v", err)
	}
	recordColumn := integrationViewColumn("record_view", "title", pb.FieldValueType_FIELD_VALUE_TYPE_STRING)
	recordColumn.OriginId = "records.title"
	if _, err := store.UpsertView(ctx, &pb.View{
		SpaceId: "crypto", ViewId: "record_view", Name: "Record View", PrimaryDatasetId: "records", DatasetIds: []string{"records"},
		FilterJson: `{}`, Engine: "bleve", RetentionWindow: "24h", Status: "active", Columns: []*pb.ViewColumn{recordColumn},
	}); err != nil {
		t.Fatalf("UpsertView(record): %v", err)
	}
	return &integrationFacts{
		timeRows: []*pb.TimeSeriesRow{
			integrationTimeRow(now.Add(-time.Minute), 100), integrationTimeRow(now, 101),
		},
		recordRows: []*pb.RecordRow{{
			Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "records", RecordId: "news-1", Version: now.Format(time.RFC3339Nano)},
			Columns: []*pb.ColumnValue{
				integrationStringValue("title", "market update"), integrationDoubleValue("score", 0.9),
			},
		}},
	}
}

func mustUpsertSpace(t *testing.T, ctx context.Context, store *metasqlite.Store) {
	t.Helper()
	if _, err := store.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "Crypto", Status: "active"}); err != nil {
		t.Fatalf("UpsertSpace: %v", err)
	}
}

func integrationMaintenanceConfig() viewsvc.MaintenanceConfig {
	return viewsvc.MaintenanceConfig{
		Enabled: true, OwnerID: "integration-builder", LeaseTTL: time.Minute, RunBudget: 30 * time.Second,
		PageSize: 100, MaxViewsPerRun: 10, MaxPagesPerViewPerRun: 10,
		MaxEntries: 1000, TargetEntries: 750, MaxPhysicalBytes: 1 << 30,
		MinReadyEntries: 1, AllowedLag: 2 * time.Minute, OverlapWindow: 30 * time.Minute, RemoveGrace: time.Second,
		TimeSeriesDefaultRetention: 24 * time.Hour, TimeSeriesRetentionByFreq: map[string]time.Duration{"1m": 24 * time.Hour},
		RecordRetention: 24 * time.Hour,
	}
}

func integrationViewColumn(viewID string, name string, valueType pb.FieldValueType) *pb.ViewColumn {
	datasetID := "records"
	if viewID == "ts_view" {
		datasetID = "ts"
	}
	return &pb.ViewColumn{
		SpaceId: "crypto", ViewId: viewID, ColumnName: name,
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   datasetID + "." + name, ValueType: valueType,
	}
}

func integrationTimeRow(at time.Time, closeValue float64) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "ts", SubjectId: "BTC-USDT", Freq: "1m", DataTime: at.Format(time.RFC3339Nano)},
		Columns: []*pb.ColumnValue{integrationDoubleValue("close", closeValue)},
	}
}

func integrationDoubleValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Value: &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}}}
}

func integrationStringValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{ColumnName: name, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}}}
}

func integrationSuccess() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

func assertTimeSeriesQuery(t *testing.T, ctx context.Context, query *viewsvc.Service, wantRows int) {
	t.Helper()
	rsp, err := query.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId: "crypto", ViewId: "ts_view", Page: &pb.Page{Page: 1, Size: 25}, TotalMode: pb.TotalMode_NONE,
	})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || len(rsp.GetRows()) != wantRows {
		t.Fatalf("QueryTimeSeriesRows rows=%d ret=%+v err=%v", len(rsp.GetRows()), rsp.GetRetInfo(), err)
	}
}

func assertRecordQuery(t *testing.T, ctx context.Context, query *viewsvc.Service, column string, wantCode pb.ErrorCode) {
	t.Helper()
	rsp, err := query.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId: "crypto", ViewId: "record_view", ColumnNames: []string{column}, Page: &pb.Page{Page: 1, Size: 25},
	})
	if err != nil || rsp.GetRetInfo().GetCode() != wantCode {
		t.Fatalf("SearchRecordRows(%s) ret=%+v err=%v", column, rsp.GetRetInfo(), err)
	}
	if wantCode == pb.ErrorCode_SUCCESS && len(rsp.GetRows()) != 1 {
		t.Fatalf("SearchRecordRows(%s) rows=%d, want 1", column, len(rsp.GetRows()))
	}
}

func assertIndexRemoved(t *testing.T, root string, indexID string, engine string) {
	t.Helper()
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		t.Fatalf("ParseViewIndexID(%s): %v", indexID, err)
	}
	path := viewindex.DuckDBPath(root, ref)
	if engine == "bleve" {
		path = viewindex.BlevePath(root, ref)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old %s index still exists at %s: %v", engine, path, err)
	}
}
