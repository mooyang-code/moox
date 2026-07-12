package view

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"github.com/mooyang-code/moox/modules/storage/internal/core/viewindex"
	"github.com/mooyang-code/moox/modules/storage/internal/service/view/search"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingTimeSeriesIndexQuery struct {
	indexID string
	req     *pb.QueryTimeSeriesRowsReq
	rows    []*pb.TimeSeriesRow
	page    *pb.PageResult
}

func (q *recordingTimeSeriesIndexQuery) QueryTimeSeriesRows(_ context.Context, indexID string, req *pb.QueryTimeSeriesRowsReq) ([]*pb.ResultColumn, []*pb.TimeSeriesRow, *pb.PageResult, error) {
	q.indexID = indexID
	q.req = proto.Clone(req).(*pb.QueryTimeSeriesRowsReq)
	return []*pb.ResultColumn{{ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE}}, q.rows, q.page, nil
}

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

func TestQueryTimeSeriesRowsRoutesActiveIndexToOwner(t *testing.T) {
	ctx := context.Background()
	owner := &recordingTimeSeriesIndexQuery{
		rows: []*pb.TimeSeriesRow{
			activeSchemaTimeSeriesRow(activeSchemaValue("close", 1)),
		},
		page: &pb.PageResult{Page: 1, Size: 5, HasMore: true, TotalState: pb.TotalState_SKIPPED},
	}
	service := NewService(ServiceOptions{
		Metadata:          activeSchemaTestMetadata(pb.DataKind_DATA_KIND_TIME_SERIES, []*pb.ViewColumn{activeSchemaColumn("close")}),
		TimeSeriesIndexes: owner,
	})

	rsp, err := service.QueryTimeSeriesRows(ctx, &pb.QueryTimeSeriesRowsReq{
		SpaceId:     "crypto",
		ViewId:      "spot_view",
		ColumnNames: []string{"close"},
		Keys: []*pb.TimeSeriesKey{{
			SpaceId:   "crypto",
			DatasetId: "ds1",
			SubjectId: "BTC-USDT",
			Freq:      "1m",
		}},
		Sorts:     []*pb.SortSpec{{FieldName: "data_time", Desc: true}},
		Page:      &pb.Page{Page: 1, Size: 5},
		Limit:     1000,
		TotalMode: pb.TotalMode_NONE,
	})
	if err != nil {
		t.Fatalf("QueryTimeSeriesRows rpc error: %v", err)
	}
	if ret := rsp.GetRetInfo(); ret.GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("ret_info = %#v, want success", ret)
	}
	if owner.indexID != activeSchemaActiveIndexID() || owner.req == nil || owner.req.GetPage().GetSize() != 5 {
		t.Fatalf("owner call = index:%q request:%+v", owner.indexID, owner.req)
	}
	if len(rsp.GetRows()) != 1 {
		t.Fatalf("rows = %+v, want owner rows", rsp.GetRows())
	}
	if rsp.GetPageResult().GetTotal() != 0 || rsp.GetPageResult().GetTotalState() != pb.TotalState_SKIPPED {
		t.Fatalf("page_result = %+v, want skipped total", rsp.GetPageResult())
	}
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
	indexID := activeSchemaActiveIndexID()
	if err := searchService.Prepare(ctx, indexID, activeSchemaViewIndexSchema(activeColumns)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := searchService.Write(ctx, indexID, viewindex.ViewIndexBatch{Columns: activeColumns, RecordRows: []*pb.RecordRow{
		activeSchemaRecordRow(activeSchemaStringValue("title", "market update")),
	}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	service := NewService(ServiceOptions{
		Metadata:      activeSchemaTestMetadata(pb.DataKind_DATA_KIND_RECORD, activeColumns, activeSchemaColumn("sentiment")),
		RecordIndexes: searchService,
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
	indexID := activeSchemaActiveIndexID()
	if err := searchService.Prepare(ctx, indexID, activeSchemaViewIndexSchema(activeColumns)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := searchService.Write(ctx, indexID, viewindex.ViewIndexBatch{Columns: activeColumns, RecordRows: []*pb.RecordRow{
		activeSchemaRecordRow(activeSchemaStringValue("title", "market update")),
	}}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	service := NewService(ServiceOptions{
		Metadata:      activeSchemaTestMetadata(pb.DataKind_DATA_KIND_RECORD, activeColumns, activeSchemaColumn("sentiment")),
		RecordIndexes: searchService,
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

func TestSearchRecordRowsRejectsFilterOrSortOnFieldBeforeSwitch(t *testing.T) {
	for _, test := range []struct {
		name    string
		filters []*pb.FilterExpr
		sorts   []*pb.SortSpec
	}{
		{name: "filter", filters: []*pb.FilterExpr{{Expr: "sentiment == $value", Args: map[string]*pb.TypedValue{"value": &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "positive"}}}}}},
		{name: "sort", sorts: []*pb.SortSpec{{FieldName: "sentiment", Desc: true}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewService(ServiceOptions{
				Metadata:      activeSchemaTestMetadata(pb.DataKind_DATA_KIND_RECORD, []*pb.ViewColumn{activeSchemaColumn("title")}, activeSchemaColumn("sentiment")),
				RecordIndexes: &activeSchemaRecordQuery{},
			})
			rsp, err := service.SearchRecordRows(context.Background(), &pb.SearchRecordRowsReq{
				SpaceId: "crypto", ViewId: "spot_view", ColumnNames: []string{"title"}, Filters: test.filters, Sorts: test.sorts,
			})
			if err != nil {
				t.Fatalf("SearchRecordRows: %v", err)
			}
			assertViewNotReady(t, rsp.GetRetInfo())
		})
	}
}

type activeSchemaRecordQuery struct{}

func (*activeSchemaRecordQuery) QueryRecordRows(context.Context, string, string, *pb.SearchRecordRowsReq) ([]*pb.ResultColumn, []*pb.RecordRow, *pb.PageResult, error) {
	return nil, nil, &pb.PageResult{}, nil
}

func activeSchemaTestMetadata(kind pb.DataKind, activeColumns []*pb.ViewColumn, newColumns ...*pb.ViewColumn) *activeSchemaMetadata {
	allColumns := make([]*pb.ViewColumn, 0, len(activeColumns)+len(newColumns))
	allColumns = append(allColumns, activeColumns...)
	allColumns = append(allColumns, newColumns...)
	return &activeSchemaMetadata{
		view: &pb.View{
			SpaceId: "crypto", ViewId: "spot_view", PrimaryDatasetId: "ds1",
			Engine: activeSchemaEngine(kind), ActiveIndexId: activeSchemaActiveIndexID(),
			ViewVersion: 2, ActiveViewVersion: 1, ActiveColumns: activeColumns, Columns: allColumns,
			IndexBuild: &pb.ViewIndexBuild{
				BuildId: "build-2", IndexId: viewindex.ViewIndexID("crypto", "spot_view", viewindex.SlotB),
				TargetViewVersion: 2, State: pb.ViewIndexBuild_BUILDING,
			},
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

func activeSchemaActiveIndexID() string {
	return viewindex.ViewIndexID("crypto", "spot_view", viewindex.SlotA)
}

func activeSchemaColumn(name string) *pb.ViewColumn {
	valueType := pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	if name == "title" || name == "sentiment" {
		valueType = pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	return &pb.ViewColumn{SpaceId: "crypto", ViewId: "spot_view", ColumnName: name, OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "ds1." + name, ValueType: valueType}
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
	schema := viewindex.ViewIndexSchema{SpaceID: "crypto", ViewID: "spot_view", Engine: "bleve", Columns: columns}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	return schema
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

func TestQueryTimeSeriesRowsRejectsMissingViewID(t *testing.T) {
	svc := NewService(ServiceOptions{})
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{SpaceId: "crypto"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_VIEW_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestQueryTimeSeriesRowsRejectsWrongEngine(t *testing.T) {
	svc := NewService(ServiceOptions{
		Metadata: &activeSchemaMetadata{
			view: &pb.View{SpaceId: "crypto", ViewId: "spot_view", Engine: "bleve", PrimaryDatasetId: "ds1", ActiveIndexId: "idx-1"},
			datasetKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		},
	})
	rsp, err := svc.QueryTimeSeriesRows(context.Background(), &pb.QueryTimeSeriesRowsReq{
		SpaceId: "crypto", ViewId: "spot_view", Limit: 1, TotalMode: pb.TotalMode_NONE,
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestSearchRecordRowsRejectsMissingPrimaryDataset(t *testing.T) {
	svc := NewService(ServiceOptions{
		Metadata: &activeSchemaMetadata{
			view: &pb.View{SpaceId: "crypto", ViewId: "spot_view", Engine: "bleve", ActiveIndexId: "idx-1"},
			datasetKind: pb.DataKind_DATA_KIND_RECORD,
		},
		RecordIndexes: &activeSchemaRecordQuery{},
	})
	rsp, err := svc.SearchRecordRows(context.Background(), &pb.SearchRecordRowsReq{
		SpaceId: "crypto", ViewId: "spot_view",
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestValidateRequestedColumnsAgainstActiveSchemaAllowsActiveFields(t *testing.T) {
	view := &pb.View{ViewVersion: 2, ActiveViewVersion: 1}
	err := ValidateRequestedColumnsAgainstActiveSchema(view, []string{"close"}, []string{"close"}, []*pb.ViewColumn{{ColumnName: "volume"}})
	assert.NoError(t, err)
}

func TestNeedsActiveSchemaValidationRequiresVersionGap(t *testing.T) {
	assert.False(t, NeedsActiveSchemaValidation(&pb.View{ViewVersion: 1, ActiveViewVersion: 1}, []string{"close"}))
	assert.True(t, NeedsActiveSchemaValidation(&pb.View{ViewVersion: 2, ActiveViewVersion: 1}, []string{"close"}))
}

func TestIsViewNotReadyErrorDetectsMarker(t *testing.T) {
	assert.True(t, IsViewNotReadyError(errViewNotReady))
	assert.False(t, IsViewNotReadyError(errors.New("other")))
}

func TestViewColumnNamesSkipsBlankNames(t *testing.T) {
	names := ViewColumnNames([]*pb.ViewColumn{{ColumnName: "close"}, {ColumnName: " "}})
	assert.Equal(t, []string{"close"}, names)
}

func TestServiceCloseReturnsNil(t *testing.T) {
	assert.NoError(t, NewService(ServiceOptions{}).Close())
}

func TestRequestedViewQueryFields_DedupesFields(t *testing.T) {
	got := requestedViewQueryFields(
		[]string{"open", "close"},
		[]*pb.FilterExpr{{Expr: "close > 0"}, {Expr: "contains(symbol, BTC)"}},
		[]*pb.SortSpec{{FieldName: "open"}},
	)
	assert.Equal(t, []string{"open", "close", "symbol"}, got)
}

func TestViewFilterField_ParsesExpressions(t *testing.T) {
	assert.Equal(t, "close", viewFilterField("close > 0"))
	assert.Equal(t, "symbol", viewFilterField("contains(symbol, BTC)"))
	assert.Equal(t, "price", viewFilterField("max(price, 1)"))
	assert.Equal(t, "", viewFilterField(""))
}

func TestNormalizeRecordSearchKeys_FillsDefaults(t *testing.T) {
	keys, err := normalizeRecordSearchKeys("crypto", "ds-1", []*pb.RecordKey{{RecordId: "r1"}})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "crypto", keys[0].GetSpaceId())
	assert.Equal(t, "ds-1", keys[0].GetDatasetId())
}

func TestNormalizeRecordSearchKeys_RejectsMismatchedSpace(t *testing.T) {
	_, err := normalizeRecordSearchKeys("crypto", "ds-1", []*pb.RecordKey{
		{SpaceId: "other", DatasetId: "ds-1", RecordId: "r1"},
	})
	require.Error(t, err)
}

func TestProjectRecordRowColumns_FiltersColumns(t *testing.T) {
	row := &pb.RecordRow{
		Columns: []*pb.ColumnValue{
			{ColumnName: "a"},
			{ColumnName: "b"},
		},
	}
	got := projectRecordRowColumns(row, []string{"b"})
	require.Len(t, got.GetColumns(), 1)
	assert.Equal(t, "b", got.GetColumns()[0].GetColumnName())
}

func TestProjectResultColumns_FiltersByIncludes(t *testing.T) {
	columns := []*pb.ViewColumn{
		{ColumnName: "open", OriginId: "kline.open"},
		{ColumnName: "close", OriginId: "kline.close"},
	}
	got := projectResultColumns(columns, []string{"open"})
	require.Len(t, got, 1)
	assert.Equal(t, "open", got[0].GetColumnName())
	assert.Equal(t, "kline", got[0].GetDatasetId())
}

func TestViewColumnDatasetID_ParsesOrigin(t *testing.T) {
	assert.Equal(t, "kline", viewColumnDatasetID(&pb.ViewColumn{OriginId: "kline.open"}))
	assert.Equal(t, "", viewColumnDatasetID(&pb.ViewColumn{OriginId: "kline"}))
}

func TestProjectRecordRowColumns_ReturnsOriginalWhenIncludesEmpty(t *testing.T) {
	row := &pb.RecordRow{Columns: []*pb.ColumnValue{{ColumnName: "a"}}}
	got := projectRecordRowColumns(row, nil)
	assert.True(t, proto.Equal(row, got))
}
