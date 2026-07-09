package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

type activeSchemaMetadata struct {
	mu          sync.Mutex
	view        *pb.View
	datasetKind pb.DataKind
	columns     []*pb.ViewColumn
}

func (m *activeSchemaMetadata) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.view == nil || m.view.GetSpaceId() != spaceID || m.view.GetViewId() != viewID {
		return nil, fmt.Errorf("view %s/%s not found", spaceID, viewID)
	}
	return proto.Clone(m.view).(*pb.View), nil
}

func (m *activeSchemaMetadata) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (m *activeSchemaMetadata) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	return nil, errors.New("not implemented")
}

func (m *activeSchemaMetadata) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*pb.ViewColumn, 0, len(m.columns))
	for _, column := range m.columns {
		out = append(out, proto.Clone(column).(*pb.ViewColumn))
	}
	return out, &pb.PageResult{Page: 1, Size: uint32(len(out)), Total: uint32(len(out)), HasMore: false}, nil
}

func (m *activeSchemaMetadata) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return nil, nil, errors.New("not implemented")
}

func (m *activeSchemaMetadata) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	return &pb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataKind: m.datasetKind}, nil
}

func (m *activeSchemaMetadata) UpsertView(ctx context.Context, item *pb.View) (*pb.View, error) {
	return nil, errors.New("not implemented")
}

func (m *activeSchemaMetadata) BeginViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) (*pb.View, error) {
	return nil, errors.New("not implemented")
}

func (m *activeSchemaMetadata) CompleteViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string) error {
	return errors.New("not implemented")
}

func (m *activeSchemaMetadata) FailViewBuild(ctx context.Context, spaceID string, viewID string, targetVersion uint64, resultName string, buildErr error) error {
	return errors.New("not implemented")
}

func TestQueryTimeSeriesRowsActiveSchemaRejectsNewColumnBeforeSwitch(t *testing.T) {
	ctx := context.Background()
	activeColumns := []*pb.ViewColumn{activeSchemaColumn("close")}

	service := NewService(ServiceOptions{
		Metadata: activeSchemaTestMetadata(pb.DataKind_DATA_KIND_TIME_SERIES, activeColumns, activeSchemaColumn("volume")),
	})
	rsp, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId:     "crypto",
		ViewId:      "spot_view",
		ColumnNames: []string{"volume"},
		Limit:       1,
		TotalMode:   pb.TotalMode_NONE,
	})
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows rpc error: %v", err)
	}
	assertViewNotReady(t, rsp.GetRetInfo())
}

func TestSearchRecordRowsActiveSchemaRejectsNewColumnBeforeSwitch(t *testing.T) {
	ctx := context.Background()
	searchService := search.NewService(search.Options{Root: t.TempDir()})
	defer searchService.Close()

	activeColumns := []*pb.ViewColumn{activeSchemaColumn("title")}
	if err := searchService.Prepare(ctx, "view_crypto_news_a", activeSchemaViewIndexSchema(activeColumns)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := searchService.IndexRecordViewRows(ctx, "view_crypto_news_a", activeColumns, []*pb.RecordRow{
		activeSchemaRecordRow(activeSchemaStringValue("title", "market update")),
	}); err != nil {
		t.Fatalf("IndexRecordViewRows: %v", err)
	}

	service := NewService(ServiceOptions{
		Metadata: activeSchemaTestMetadata(pb.DataKind_DATA_KIND_RECORD, activeColumns, activeSchemaColumn("sentiment")),
		Search:   searchService,
	})
	rsp, err := service.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:     "crypto",
		ViewId:      "spot_view",
		ColumnNames: []string{"sentiment"},
		Keys:        []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "ds1", RecordId: "record-1", Version: "2026-07-09T01:00:00Z"}},
	})
	if err != nil {
		t.Fatalf("SearchRecordRows rpc error: %v", err)
	}
	assertViewNotReady(t, rsp.GetRetInfo())
}

func TestSearchRecordRowsActiveSchemaAllowsExistingColumnDuringBuild(t *testing.T) {
	ctx := context.Background()
	searchService := search.NewService(search.Options{Root: t.TempDir()})
	defer searchService.Close()

	activeColumns := []*pb.ViewColumn{activeSchemaColumn("title")}
	if err := searchService.Prepare(ctx, "view_crypto_news_a", activeSchemaViewIndexSchema(activeColumns)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := searchService.IndexRecordViewRows(ctx, "view_crypto_news_a", activeColumns, []*pb.RecordRow{
		activeSchemaRecordRow(activeSchemaStringValue("title", "market update")),
	}); err != nil {
		t.Fatalf("IndexRecordViewRows: %v", err)
	}

	service := NewService(ServiceOptions{
		Metadata: activeSchemaTestMetadata(pb.DataKind_DATA_KIND_RECORD, activeColumns, activeSchemaColumn("sentiment")),
		Search:   searchService,
	})
	rsp, err := service.SearchRecordRows(ctx, &pb.SearchRecordRowsReq{
		SpaceId:     "crypto",
		ViewId:      "spot_view",
		ColumnNames: []string{"title"},
		Keys:        []*pb.RecordKey{{SpaceId: "crypto", DatasetId: "ds1", RecordId: "record-1", Version: "2026-07-09T01:00:00Z"}},
	})
	if err != nil {
		t.Fatalf("SearchRecordRows rpc error: %v", err)
	}
	if ret := rsp.GetRetInfo(); ret.GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %#v, want success", ret)
	}
	if got := resultColumnNames(rsp.GetColumns()); len(got) != 1 || got[0] != "title" {
		t.Fatalf("columns = %v, want [title]", got)
	}
	if len(rsp.GetRows()) != 1 {
		t.Fatalf("rows = %d, want 1", len(rsp.GetRows()))
	}
}

func activeSchemaTestMetadata(kind pb.DataKind, activeColumns []*pb.ViewColumn, newColumns ...*pb.ViewColumn) *activeSchemaMetadata {
	allColumns := make([]*pb.ViewColumn, 0, len(activeColumns)+len(newColumns))
	allColumns = append(allColumns, activeColumns...)
	allColumns = append(allColumns, newColumns...)
	return &activeSchemaMetadata{
		view: &pb.View{
			SpaceId:             "crypto",
			ViewId:              "spot_view",
			PrimaryDatasetId:    "ds1",
			Engine:              activeSchemaEngine(kind),
			ActiveResult:        activeSchemaActiveResult(kind),
			BuildingResult:      "view_crypto_spot_b",
			BuildStatus:         "building",
			ViewVersion:         2,
			ActiveViewVersion:   1,
			BuildingViewVersion: 2,
			Columns:             activeColumns,
		},
		datasetKind: kind,
		columns:     allColumns,
	}
}

func activeSchemaEngine(kind pb.DataKind) string {
	if kind == pb.DataKind_DATA_KIND_RECORD {
		return "bleve"
	}
	return "duckdb"
}

func activeSchemaActiveResult(kind pb.DataKind) string {
	if kind == pb.DataKind_DATA_KIND_RECORD {
		return "view_crypto_news_a"
	}
	return "view_crypto_spot_a"
}

func activeSchemaColumn(name string) *pb.ViewColumn {
	valueType := pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	if name == "title" || name == "sentiment" {
		valueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	return &pb.ViewColumn{SpaceId: "crypto", ViewId: "spot_view", ColumnName: name, OriginId: "ds1." + name, ValueType: valueType}
}

func activeSchemaValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func activeSchemaStringValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func activeSchemaTimeSeriesRow(columns ...*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:   "crypto",
			DatasetId: "ds1",
			SubjectId: "BTC-USDT",
			Freq:      "1m",
			DataTime:  "2026-07-09T01:00:00Z",
		},
		Columns: columns,
	}
}

func activeSchemaRecordRow(columns ...*pb.ColumnValue) *pb.RecordRow {
	return &pb.RecordRow{
		Key: &pb.RecordKey{
			SpaceId:   "crypto",
			DatasetId: "ds1",
			RecordId:  "record-1",
			Version:   "2026-07-09T01:00:00Z",
		},
		Columns: columns,
	}
}

func activeSchemaViewIndexSchema(columns []*pb.ViewColumn) viewindex.ViewIndexSchema {
	return viewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "spot_view", Engine: "bleve", Columns: columns}
}

func assertViewNotReady(t *testing.T, ret *pb.RetInfo) {
	t.Helper()
	if ret.GetCode() != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("ret code = %v, want VIEW_NOT_READY; ret=%#v", ret.GetCode(), ret)
	}
	if ret.GetMsg() != viewNotReadyMessage {
		t.Fatalf("ret msg = %q, want %q", ret.GetMsg(), viewNotReadyMessage)
	}
}

func resultColumnNames(columns []*pb.ResultColumn) []string {
	out := make([]string, 0, len(columns))
	for _, column := range columns {
		out = append(out, column.GetColumnName())
	}
	sort.Strings(out)
	return out
}
