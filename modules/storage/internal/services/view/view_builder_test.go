package view_test

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/infra/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestBuilderBuildsViewFromTimeSeriesRows(t *testing.T) {
	ctx := context.Background()
	meta := openViewMetadata(t, ctx)
	writer := &fakeViewWriter{}
	builder := view.NewBuilder(view.Options{
		Metadata: meta,
		Facts: fakeFactReader{rows: []*pb.TimeSeriesRow{
			timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
		}},
		Views: writer,
		Now:   func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})

	built, err := builder.BuildView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "active", built.GetBuildStatus())
	require.NotEmpty(t, built.GetActiveResult())
	require.Len(t, writer.rows[built.GetActiveResult()], 1)
	require.Equal(t, "close", writer.rows[built.GetActiveResult()][0].GetColumns()[0].GetColumnName())
}

func TestBuilderAppliesViewFilterJSONToTimeSeriesRows(t *testing.T) {
	ctx := context.Background()
	meta := openViewMetadata(t, ctx)
	viewMeta, err := meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	viewMeta.FilterJson = `{"freq":"1m"}`
	_, err = meta.UpsertView(ctx, viewMeta)
	require.NoError(t, err)

	writer := &fakeViewWriter{}
	builder := view.NewBuilder(view.Options{
		Metadata: meta,
		Facts: fakeFactReader{rows: []*pb.TimeSeriesRow{
			timeSeriesRowWithFreq("APT-USDT", "1m", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
			timeSeriesRowWithFreq("APT-USDT", "1h", "2026-06-15T01:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}),
		}},
		Views: writer,
		Now:   func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})

	built, err := builder.BuildView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	rows := writer.rows[built.GetActiveResult()]
	require.Len(t, rows, 1)
	require.Equal(t, "1m", rows[0].GetKey().GetFreq())
	require.Equal(t, 8.1, rows[0].GetColumns()[0].GetValue().GetDoubleValue())
}

func TestBuilderBuildsViewByJoiningDatasetsOnGrain(t *testing.T) {
	ctx := context.Background()
	meta := openViewMetadata(t, ctx)
	_, err := meta.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "factor", DataSourceId: "binance", Name: "因子", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "joined_kline_view",
		Name:             "K线因子视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline", "factor"},
		GrainKeys:        []string{"subject_id", "data_time", "freq"},
		Engine:           "duckdb",
		BuildStatus:      "pending",
		Status:           "active",
	})
	require.NoError(t, err)
	_, err = meta.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "crypto", ViewId: "joined_kline_view", ColumnName: "kline.close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 1})
	require.NoError(t, err)
	_, err = meta.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "crypto", ViewId: "joined_kline_view", ColumnName: "factor.alpha", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "factor.alpha", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: 2})
	require.NoError(t, err)
	writer := &fakeViewWriter{}
	builder := view.NewBuilder(view.Options{
		Metadata: meta,
		Facts: fakeFactReader{rowsByDataset: map[string][]*pb.TimeSeriesRow{
			"kline": {
				timeSeriesRowForDataset("kline", "APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)}),
				timeSeriesRowForDataset("kline", "APT-USDT", "2026-06-15T01:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.2)}),
			},
			"factor": {
				timeSeriesRowForDataset("factor", "APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("alpha", 0.42)}),
			},
		}},
		Views: writer,
		Now:   func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})

	built, err := builder.BuildView(ctx, "crypto", "joined_kline_view")
	require.NoError(t, err)
	rows := writer.rows[built.GetActiveResult()]
	require.Len(t, rows, 2)
	require.Equal(t, "kline", rows[0].GetKey().GetDatasetId())
	require.Equal(t, "APT-USDT", rows[0].GetKey().GetSubjectId())
	require.Equal(t, "kline.close", rows[0].GetColumns()[0].GetColumnName())
	require.Equal(t, 8.1, rows[0].GetColumns()[0].GetValue().GetDoubleValue())
	require.Equal(t, "factor.alpha", rows[0].GetColumns()[1].GetColumnName())
	require.Equal(t, 0.42, rows[0].GetColumns()[1].GetValue().GetDoubleValue())
	require.Equal(t, "2026-06-15T01:00:00Z", rows[1].GetKey().GetDataTime())
	require.Equal(t, "factor.alpha", rows[1].GetColumns()[1].GetColumnName())
	require.Nil(t, rows[1].GetColumns()[1].GetValue())
}

func TestBuilderRebuildPendingTreatsBuildingAsRecoverable(t *testing.T) {
	ctx := context.Background()
	meta := openViewMetadata(t, ctx)
	_, err := meta.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "symbols", DataSourceId: "binance", Name: "Symbols", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertView(ctx, &pb.View{SpaceId: "crypto", ViewId: "symbols_view", Name: "Symbols View", PrimaryDatasetId: "symbols", DatasetIds: []string{"symbols"}, Engine: "bleve", ActiveResult: "record_index", BuildStatus: "active", ActiveViewVersion: 1, ViewVersion: 1, Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertView(ctx, &pb.View{SpaceId: "crypto", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"}, GrainKeys: []string{"subject_id", "data_time", "freq"}, BuildStatus: "building", Status: "active"})
	require.NoError(t, err)
	writer := &fakeViewWriter{}
	builder := view.NewBuilder(view.Options{
		Metadata: meta,
		Facts:    fakeFactReader{rows: []*pb.TimeSeriesRow{timeSeriesRow("APT-USDT", "2026-06-15T00:00:00Z", []*pb.ColumnValue{testutil.DoubleValue("close", 8.1)})}},
		Views:    writer,
	})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, built, 1)
	require.Equal(t, "kline_view", built[0].GetViewId())
	require.Equal(t, "active", built[0].GetBuildStatus())
}

func TestBuilderRebuildPendingBuildsRecordViewsIntoSearchIndex(t *testing.T) {
	ctx := context.Background()
	meta := openViewMetadata(t, ctx)
	_, err := meta.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "symbols", DataSourceId: "binance", Name: "Symbols", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertView(ctx, &pb.View{SpaceId: "crypto", ViewId: "symbols_view", Name: "Symbols View", PrimaryDatasetId: "symbols", DatasetIds: []string{"symbols"}, Engine: "bleve", BuildStatus: "pending", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "crypto", ViewId: "symbols_view", ColumnName: "symbols.name", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "symbols.name", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING})
	require.NoError(t, err)
	indexer := &fakeRecordIndexer{}
	builder := view.NewBuilder(view.Options{
		Metadata: meta,
		Facts: fakeFactReader{recordRowsByDataset: map[string][]*pb.RecordRow{
			"symbols": {
				recordRowForDataset("symbols", "BTC-USDT", "v1", []*pb.ColumnValue{testutil.StringValue("name", "Bitcoin")}),
			},
		}},
		Views:  &fakeViewWriter{},
		Search: indexer,
		Now:    func() time.Time { return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC) },
	})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, built, 2)
	recordView := builtViewByID(built, "symbols_view")
	require.NotNil(t, recordView)
	require.Equal(t, "active", recordView.GetBuildStatus())
	require.NotEmpty(t, recordView.GetActiveResult())
	require.Len(t, indexer.rows[recordView.GetActiveResult()], 1)
	require.Equal(t, "BTC-USDT", indexer.rows[recordView.GetActiveResult()][0].GetKey().GetRecordId())
	require.Equal(t, "symbols.name", indexer.rows[recordView.GetActiveResult()][0].GetColumns()[0].GetColumnName())
}

func TestBuilderCleanupInactiveResultsDropsOnlyInactiveTables(t *testing.T) {
	ctx := context.Background()
	meta := openViewMetadata(t, ctx)
	viewMeta, err := meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	_, err = meta.BeginViewBuild(ctx, "crypto", "kline_view", viewMeta.GetViewVersion(), "view_result_crypto_active")
	require.NoError(t, err)
	require.NoError(t, meta.CompleteViewBuild(ctx, "crypto", "kline_view", viewMeta.GetViewVersion(), "view_result_crypto_active"))
	viewMeta, err = meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	_, err = meta.BeginViewBuild(ctx, "crypto", "kline_view", viewMeta.GetViewVersion(), "view_result_crypto_building")
	require.NoError(t, err)
	viewMeta, err = meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "view_result_crypto_active", viewMeta.GetActiveResult())
	require.Equal(t, "view_result_crypto_building", viewMeta.GetBuildingResult())
	writer := &fakeViewWriter{tables: map[string]bool{"view_result_crypto_active": true, "view_result_crypto_building": true, "view_result_crypto_old": true}}
	builder := view.NewBuilder(view.Options{Metadata: meta, Facts: fakeFactReader{}, Views: writer})

	dropped, err := builder.CleanupInactiveResults(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, 1, dropped)
	require.True(t, writer.tables["view_result_crypto_active"])
	require.True(t, writer.tables["view_result_crypto_building"])
	require.False(t, writer.tables["view_result_crypto_old"])
}

// fakeFactReader 是 View 构建测试使用的主存读取桩。
type fakeFactReader struct {
	rows                []*pb.TimeSeriesRow
	rowsByDataset       map[string][]*pb.TimeSeriesRow
	recordRowsByDataset map[string][]*pb.RecordRow
}

func (f fakeFactReader) ReadTimeSeriesRows(ctx context.Context, req *pb.ReadTimeSeriesRowsReq) (*pb.ReadTimeSeriesRowsRsp, error) {
	_ = ctx
	return &pb.ReadTimeSeriesRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Rows: f.rows, PageResult: &pb.PageResult{Total: uint32(len(f.rows))}}, nil
}

func (f fakeFactReader) ScanTimeSeriesRows(ctx context.Context, spaceID string, datasetID string, timeRange *pb.TimeRange, columnNames []string, page *pb.Page) ([]*pb.TimeSeriesRow, *pb.PageResult, error) {
	_ = ctx
	if f.rowsByDataset != nil {
		rows := f.rowsByDataset[datasetID]
		return rows, &pb.PageResult{Total: uint32(len(rows))}, nil
	}
	return f.rows, &pb.PageResult{Total: uint32(len(f.rows))}, nil
}

func (f fakeFactReader) ReadRecordRows(ctx context.Context, req *pb.ReadRecordRowsReq) (*pb.ReadRecordRowsRsp, error) {
	_ = ctx
	var out []*pb.RecordRow
	for _, key := range req.GetKeys() {
		for _, row := range f.recordRowsByDataset[key.GetDatasetId()] {
			if row.GetKey().GetRecordId() == key.GetRecordId() && row.GetKey().GetVersion() == key.GetVersion() {
				out = append(out, row)
			}
		}
	}
	return &pb.ReadRecordRowsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Rows: out, PageResult: &pb.PageResult{Total: uint32(len(out))}}, nil
}

func (f fakeFactReader) ScanRecordRows(ctx context.Context, spaceID string, datasetID string, versionRange *pb.VersionRange, columnNames []string, page *pb.Page) ([]*pb.RecordRow, *pb.PageResult, error) {
	_ = ctx
	rows := f.recordRowsByDataset[datasetID]
	return rows, &pb.PageResult{Total: uint32(len(rows))}, nil
}

// fakeViewWriter 是 View 构建测试使用的结果写入桩。
type fakeViewWriter struct {
	rows   map[string][]*pb.TimeSeriesRow
	tables map[string]bool
}

func (w *fakeViewWriter) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	_ = ctx
	if w.rows == nil {
		w.rows = make(map[string][]*pb.TimeSeriesRow)
	}
	if w.tables == nil {
		w.tables = make(map[string]bool)
	}
	w.tables[tableName] = true
	return nil
}

func (w *fakeViewWriter) InsertRows(ctx context.Context, tableName string, rows []*pb.TimeSeriesRow) error {
	_ = ctx
	if w.rows == nil {
		w.rows = make(map[string][]*pb.TimeSeriesRow)
	}
	w.rows[tableName] = append(w.rows[tableName], rows...)
	return nil
}

func (w *fakeViewWriter) ListResultTables(ctx context.Context) ([]string, error) {
	_ = ctx
	out := make([]string, 0, len(w.tables))
	for table := range w.tables {
		out = append(out, table)
	}
	return out, nil
}

func (w *fakeViewWriter) DropResultTable(ctx context.Context, tableName string) error {
	_ = ctx
	delete(w.tables, tableName)
	return nil
}

type fakeRecordIndexer struct {
	rows map[string][]*pb.RecordRow
}

func (i *fakeRecordIndexer) IndexRecordViewRows(ctx context.Context, resultName string, columns []*pb.ViewColumn, rows []*pb.RecordRow) error {
	_ = ctx
	_ = columns
	if i.rows == nil {
		i.rows = make(map[string][]*pb.RecordRow)
	}
	i.rows[resultName] = append(i.rows[resultName], rows...)
	return nil
}

func openViewMetadata(t *testing.T, ctx context.Context) *metasqlite.Store {
	t.Helper()
	root := t.TempDir()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{Path: filepath.Join(root, "metadata.db"), SchemaPath: schemaPath(t)})
	require.NoError(t, err)
	require.NoError(t, meta.InitSchema(ctx))
	t.Cleanup(func() { require.NoError(t, meta.Close()) })
	_, err = meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = meta.UpsertDataset(ctx, &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance", Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT", Status: "active"})
	require.NoError(t, err)
	_, err = meta.BindDatasetSubject(ctx, &pb.DatasetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertView(ctx, &pb.View{SpaceId: "crypto", ViewId: "kline_view", Name: "K线视图", PrimaryDatasetId: "kline", DatasetIds: []string{"kline"}, GrainKeys: []string{"subject_id", "data_time", "freq"}, BuildStatus: "pending", Status: "active"})
	require.NoError(t, err)
	_, err = meta.UpsertViewColumn(ctx, &pb.ViewColumn{SpaceId: "crypto", ViewId: "kline_view", ColumnName: "close", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: "kline.close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE})
	require.NoError(t, err)
	return meta
}

func timeSeriesRow(subjectID string, dataTime string, columns []*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: subjectID, Freq: "1m", DataTime: dataTime}, Columns: columns}
}

func timeSeriesRowWithFreq(subjectID string, freq string, dataTime string, columns []*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: subjectID, Freq: freq, DataTime: dataTime}, Columns: columns}
}

func timeSeriesRowForDataset(datasetID string, subjectID string, dataTime string, columns []*pb.ColumnValue) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{Key: &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: datasetID, SubjectId: subjectID, Freq: "1m", DataTime: dataTime}, Columns: columns}
}

func recordRowForDataset(datasetID string, recordID string, version string, columns []*pb.ColumnValue) *pb.RecordRow {
	return &pb.RecordRow{Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: datasetID, RecordId: recordID, Version: version}, Columns: columns}
}

func builtViewByID(views []*pb.View, viewID string) *pb.View {
	for _, item := range views {
		if item.GetViewId() == viewID {
			return item
		}
	}
	return nil
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "schema", "metadata.sql")
}
