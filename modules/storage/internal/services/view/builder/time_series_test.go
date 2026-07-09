package builder

import (
	"context"
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

func TestTimeSeriesBuildingVersionGuardWritesActiveOnlyForStaleBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "spot_kline_1m_view",
		PrimaryDatasetId:    "kline",
		DatasetIds:          []string{"kline"},
		Engine:              "duckdb",
		Status:              "active",
		BuildStatus:         "building",
		ViewVersion:         2,
		ActiveResult:        "a",
		ActiveViewVersion:   1,
		BuildingResult:      "b",
		BuildingViewVersion: 1,
	})

	assertWriteTargets(t, writes, map[string]int{"a": 1})
}

func TestTimeSeriesBuildingStatusGuardWritesActiveOnlyForFailedBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "spot_kline_1m_view",
		PrimaryDatasetId:    "kline",
		DatasetIds:          []string{"kline"},
		Engine:              "duckdb",
		Status:              "active",
		BuildStatus:         "failed",
		ViewVersion:         2,
		ActiveResult:        "a",
		ActiveViewVersion:   1,
		BuildingResult:      "b",
		BuildingViewVersion: 2,
	})

	assertWriteTargets(t, writes, map[string]int{"a": 1})
}

func TestTimeSeriesCurrentBuildingWritesActiveAndBuilding(t *testing.T) {
	writes := runTimeSeriesBuildingWriteTest(t, &pb.View{
		SpaceId:             "crypto",
		ViewId:              "spot_kline_1m_view",
		PrimaryDatasetId:    "kline",
		DatasetIds:          []string{"kline"},
		Engine:              "duckdb",
		Status:              "active",
		BuildStatus:         "building",
		ViewVersion:         2,
		ActiveResult:        "a",
		ActiveViewVersion:   1,
		BuildingResult:      "b",
		BuildingViewVersion: 2,
	})

	assertWriteTargets(t, writes, map[string]int{"a": 1, "b": 1})
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
	writer := &recordingTimeSeriesWriter{writes: map[string]int{}}
	service := &Service{
		reader: &buildingGuardReader{
			timeSeriesRows: []*pb.TimeSeriesRow{testBuilderTimeSeriesRow(key)},
		},
		metadata: newBuildingGuardMetadata(view),
		views:    writer,
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
		out.views = append(out.views, proto.Clone(view).(*pb.View))
	}
	return out
}

func (m *buildingGuardMetadata) GetView(_ context.Context, spaceID string, viewID string) (*pb.View, error) {
	for _, view := range m.views {
		if view.GetSpaceId() == spaceID && view.GetViewId() == viewID {
			return proto.Clone(view).(*pb.View), nil
		}
	}
	return nil, errors.New("view not found")
}

func (m *buildingGuardMetadata) ListViews(_ context.Context, spaceID string, datasetID string, status string, _ *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	var out []*pb.View
	for _, view := range m.views {
		if spaceID != "" && view.GetSpaceId() != spaceID {
			continue
		}
		if datasetID != "" && view.GetPrimaryDatasetId() != datasetID {
			continue
		}
		if status != "" && view.GetStatus() != status {
			continue
		}
		out = append(out, proto.Clone(view).(*pb.View))
	}
	return out, &pb.PageResult{}, nil
}

func (m *buildingGuardMetadata) ListViewsByDataset(_ context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	views, _, err := m.ListViews(context.Background(), spaceID, datasetID, "active", nil)
	return views, err
}

func (m *buildingGuardMetadata) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	out := make([]*pb.ViewColumn, 0, len(m.columns))
	for _, column := range m.columns {
		out = append(out, proto.Clone(column).(*pb.ViewColumn))
	}
	return out, &pb.PageResult{}, nil
}

func (m *buildingGuardMetadata) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (m *buildingGuardMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DataKind: pb.DataKind_DATA_KIND_TIME_SERIES}, nil
}

func (m *buildingGuardMetadata) UpsertView(_ context.Context, item *pb.View) (*pb.View, error) {
	copied := proto.Clone(item).(*pb.View)
	for i, view := range m.views {
		if view.GetSpaceId() == copied.GetSpaceId() && view.GetViewId() == copied.GetViewId() {
			m.views[i] = copied
			return proto.Clone(copied).(*pb.View), nil
		}
	}
	m.views = append(m.views, copied)
	return proto.Clone(copied).(*pb.View), nil
}

func (m *buildingGuardMetadata) BeginViewBuild(context.Context, string, string, uint64, string) (*pb.View, error) {
	return nil, nil
}

func (m *buildingGuardMetadata) CompleteViewBuild(context.Context, string, string, uint64, string) error {
	return nil
}

func (m *buildingGuardMetadata) FailViewBuild(context.Context, string, string, uint64, string, error) error {
	return nil
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

type recordingTimeSeriesWriter struct {
	writes map[string]int
}

func (w *recordingTimeSeriesWriter) InsertRows(_ context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	if w.writes == nil {
		w.writes = map[string]int{}
	}
	w.writes[tableName] += len(rows)
	return nil
}

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
