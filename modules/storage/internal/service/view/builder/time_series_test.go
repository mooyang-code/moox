package builder

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func TestTimeSeriesBuildingVersionGuardWritesActiveOnlyForStaleBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "spot_kline_1m_view",
		PrimaryDatasetId:  "kline",
		DatasetIds:        []string{"kline"},
		Engine:            "duckdb",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("spot_kline_1m_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("spot_kline_1m_view", viewindex.SlotB), TargetViewVersion: 1, State: pb.ViewIndexBuild_BUILDING,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("spot_kline_1m_view", viewindex.SlotA): 1})
}

func TestTimeSeriesBuildingStatusGuardWritesActiveOnlyForFailedBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "spot_kline_1m_view",
		PrimaryDatasetId:  "kline",
		DatasetIds:        []string{"kline"},
		Engine:            "duckdb",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("spot_kline_1m_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("spot_kline_1m_view", viewindex.SlotB), TargetViewVersion: 2, State: pb.ViewIndexBuild_FAILED,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("spot_kline_1m_view", viewindex.SlotA): 1})
}

func TestTimeSeriesCurrentBuildingWritesActiveAndBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "spot_kline_1m_view",
		PrimaryDatasetId:  "kline",
		DatasetIds:        []string{"kline"},
		Engine:            "duckdb",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("spot_kline_1m_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("spot_kline_1m_view", viewindex.SlotB), TargetViewVersion: 2, State: pb.ViewIndexBuild_BUILDING,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("spot_kline_1m_view", viewindex.SlotA): 1, builderIndexID("spot_kline_1m_view", viewindex.SlotB): 1})
}

func TestTimeSeriesCurrentCatchingUpWritesActiveAndBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:           "crypto",
		ViewId:            "spot_kline_1m_view",
		PrimaryDatasetId:  "kline",
		DatasetIds:        []string{"kline"},
		Engine:            "duckdb",
		Status:            "active",
		ViewVersion:       2,
		ActiveIndexId:     builderIndexID("spot_kline_1m_view", viewindex.SlotA),
		ActiveViewVersion: 1,
		IndexBuild: &pb.ViewIndexBuild{
			IndexId: builderIndexID("spot_kline_1m_view", viewindex.SlotB), TargetViewVersion: 2, State: pb.ViewIndexBuild_CATCHING_UP,
		},
	})

	assertWriteTargets(t, writes, map[string]int{builderIndexID("spot_kline_1m_view", viewindex.SlotA): 1, builderIndexID("spot_kline_1m_view", viewindex.SlotB): 1})
}

func TestTimeSeriesOwnerWriteFailurePropagates(t *testing.T) {
	wantErr := errors.New("view index owner unavailable")
	view := &pb.View{
		SpaceId: "crypto", ViewId: "spot_kline_1m_view", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"},
		Engine: "duckdb", Status: "active", ViewVersion: 1,
		ActiveIndexId: builderIndexID("spot_kline_1m_view", viewindex.SlotA),
	}
	key := &pb.TimeSeriesKey{
		SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-07-09T01:00:00Z",
	}
	writer := newRecordingViewIndexEngine("duckdb")
	writer.writeErr = wantErr
	service := &Service{
		reader:   &buildingGuardReader{timeSeriesRows: []*pb.TimeSeriesRow{testBuilderTimeSeriesRow(key)}},
		metadata: newBuildingGuardMetadata(view),
		engines:  map[string]viewindex.ViewIndexEngine{"duckdb": writer},
	}

	if err := service.processTimeSeriesBatch(context.Background(), []*pb.TimeSeriesKey{key}); !errors.Is(err, wantErr) {
		t.Fatalf("processTimeSeriesBatch error = %v, want %v", err, wantErr)
	}
}

func TestTimeSeriesViewFilterSkipsMismatchedFreq(t *testing.T) {
	ctx := context.Background()
	view := &pb.View{
		SpaceId:          "crypto",
		ViewId:           "spot_kline_1m_view",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		Engine:           "duckdb",
		Status:           "active",
		ViewVersion:      1,
		ActiveIndexId:    builderIndexID("spot_kline_1m_view", viewindex.SlotA),
		FilterJson:       `{"freq":"1m"}`,
	}
	key := &pb.TimeSeriesKey{
		SpaceId:   "crypto",
		DatasetId: "kline",
		SubjectId: "BTC-USDT",
		Freq:      "1h",
		DataTime:  "2026-07-09T01:00:00Z",
	}
	writer := newRecordingViewIndexEngine("duckdb")
	service := &Service{
		reader: &buildingGuardReader{
			timeSeriesRows: []*pb.TimeSeriesRow{testBuilderTimeSeriesRow(key)},
		},
		metadata: newBuildingGuardMetadata(view),
		engines:  map[string]viewindex.ViewIndexEngine{"duckdb": writer},
	}
	if err := service.processTimeSeriesBatch(ctx, []*pb.TimeSeriesKey{key}); err != nil {
		t.Fatalf("processTimeSeriesBatch: %v", err)
	}
	assertWriteTargets(t, writer.writes, map[string]int{})
}

func runTimeSeriesBuildingWriteTest(t *testing.T, view *pb.View) map[string]int {
	t.Helper()
	ctx := context.Background()
	key := &pb.TimeSeriesKey{
		SpaceId:   view.GetSpaceId(),
		DatasetId: view.GetPrimaryDatasetId(),
		SubjectId: "BTC-USDT",
		Freq:      "1m",
		DataTime:  "2026-07-09T01:00:00Z",
	}
	writer := newRecordingViewIndexEngine("duckdb")
	service := &Service{
		reader: &buildingGuardReader{
			timeSeriesRows: []*pb.TimeSeriesRow{testBuilderTimeSeriesRow(key)},
		},
		metadata: newBuildingGuardMetadata(view),
		engines:  map[string]viewindex.ViewIndexEngine{"duckdb": writer},
	}
	if err := service.processTimeSeriesBatch(ctx, []*pb.TimeSeriesKey{key}); err != nil {
		t.Fatalf("processTimeSeriesBatch: %v", err)
	}
	return writer.writes
}

type buildingGuardMetadata struct {
	views   []*pb.View
	columns []*pb.ViewColumn
}

func newBuildingGuardMetadata(views ...*pb.View) *buildingGuardMetadata {
	out := &buildingGuardMetadata{
		columns: []*pb.ViewColumn{{
			ColumnName: "close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}},
	}
	for _, view := range views {
		copied := proto.Clone(view).(*pb.View)
		copied.Columns = cloneViewColumns(out.columns)
		if copied.GetActiveIndexId() != "" && len(copied.GetActiveColumns()) == 0 {
			copied.ActiveColumns = cloneViewColumns(out.columns)
		}
		if copied.GetIndexBuild() != nil {
			copied.IndexBuild.Engine = copied.GetEngine()
			copied.IndexBuild.Columns = cloneViewColumns(out.columns)
			copied.IndexBuild.SchemaHash = viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{
				SpaceID: copied.GetSpaceId(), ViewID: copied.GetViewId(), Engine: copied.GetEngine(), Columns: out.columns,
			})
		}
		out.views = append(out.views, copied)
	}
	return out
}

func builderIndexID(viewID string, slot viewindex.Slot) string {
	return viewindex.ViewIndexID("crypto", viewID, slot)
}

func cloneViewColumns(columns []*pb.ViewColumn) []*pb.ViewColumn {
	out := make([]*pb.ViewColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, proto.Clone(column).(*pb.ViewColumn))
	}
	return out
}

func (m *buildingGuardMetadata) ListViewsByDataset(_ context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	var out []*pb.View
	for _, view := range m.views {
		if spaceID != "" && view.GetSpaceId() != spaceID {
			continue
		}
		if datasetID != "" && view.GetPrimaryDatasetId() != datasetID {
			continue
		}
		if view.GetStatus() != "active" {
			continue
		}
		out = append(out, proto.Clone(view).(*pb.View))
	}
	return out, nil
}

func (m *buildingGuardMetadata) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return cloneViewColumns(m.columns), &pb.PageResult{}, nil
}

type buildingGuardReader struct {
	timeSeriesRows []*pb.TimeSeriesRow
	recordRows     []*pb.RecordRow
}

func (r *buildingGuardReader) ReadTimeSeriesRows(context.Context, *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	out := make([]*pb.TimeSeriesRow, 0, len(r.timeSeriesRows))
	for _, row := range r.timeSeriesRows {
		out = append(out, proto.Clone(row).(*pb.TimeSeriesRow))
	}
	return &pb.ReadTimeSeriesRowsRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows:    out,
	}, nil
}

func (r *buildingGuardReader) ReadRecordRows(context.Context, *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	out := make([]*pb.RecordRow, 0, len(r.recordRows))
	for _, row := range r.recordRows {
		out = append(out, proto.Clone(row).(*pb.RecordRow))
	}
	return &pb.ReadRecordRowsRsp{
		RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
		Rows:    out,
	}, nil
}

func (r *buildingGuardReader) ScanTimeSeriesRows(context.Context, string, string, *pb.TimeRange, []string, *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	return r.timeSeriesRows, &pb.PageResult{}, nil
}

func (r *buildingGuardReader) ScanRecordRows(context.Context, string, string, *pb.VersionRange, []string, *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	return r.recordRows, &pb.PageResult{}, nil
}

type recordingViewIndexEngine struct {
	engine   string
	writes   map[string]int
	writeErr error
}

func newRecordingViewIndexEngine(engine string) *recordingViewIndexEngine {
	return &recordingViewIndexEngine{engine: engine, writes: map[string]int{}}
}

func (w *recordingViewIndexEngine) Engine() string { return w.engine }

func (w *recordingViewIndexEngine) Prepare(context.Context, string, viewindex.ViewIndexSchema) error {
	return nil
}

func (w *recordingViewIndexEngine) Write(_ context.Context, indexID string, batch viewindex.ViewIndexBatch) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	if w.writes == nil {
		w.writes = map[string]int{}
	}
	w.writes[indexID] += len(batch.TimeSeriesRows) + len(batch.RecordRows)
	return nil
}

func (w *recordingViewIndexEngine) Apply(_ context.Context, indexID string, batch viewindex.ViewIndexApplyBatch) error {
	if w.writeErr != nil {
		return w.writeErr
	}
	if w.writes == nil {
		w.writes = map[string]int{}
	}
	w.writes[indexID] += len(batch.RowWrites)
	return nil
}

func (w *recordingViewIndexEngine) Stat(context.Context, string) (viewindex.ViewIndexStats, error) {
	return viewindex.ViewIndexStats{}, nil
}

func (w *recordingViewIndexEngine) Remove(context.Context, string) error { return nil }

func testBuilderTimeSeriesRow(key *pb.TimeSeriesKey) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: proto.Clone(key).(*pb.TimeSeriesKey),
		Columns: []*pb.ColumnValue{{
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: 1.23}},
		}},
	}
}

func assertWriteTargets(t *testing.T, got map[string]int, want map[string]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("writes = %v, want %v", got, want)
	}
	for target, wantCount := range want {
		if got[target] != wantCount {
			t.Fatalf("writes[%s] = %d, want %d (all writes: %v)", target, got[target], wantCount, got)
		}
	}
}
