package viewbuilder_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/services/viewbuilder"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestBuilderBuildsViewFromPebbleFacts(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()

	builder := fixture.builder
	built, err := builder.Build(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.NotEmpty(t, built.GetActiveResult())
	require.Equal(t, "active", built.GetBuildStatus())

	stored, err := fixture.meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Equal(t, built.GetActiveResult(), stored.GetActiveResult())

	columns, rows, _, err := fixture.views.QueryView(ctx, built.GetActiveResult(), &pb.QueryViewReq{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		SubjectIds: []string{"APT-USDT"},
	})
	require.NoError(t, err)
	require.Len(t, columns, 1)
	require.Equal(t, "close", columns[0].GetColumnName())
	require.Len(t, rows, 1)
	require.Equal(t, "APT-USDT", rows[0].GetSubjectId())
	require.Equal(t, 8.1, rows[0].GetValues()[0].GetValue().GetDoubleValue())
}

func TestBuilderRebuildsPendingViews(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()

	views, err := fixture.builder.RebuildPendingViews(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, views, 1)
	require.Equal(t, "kline_view", views[0].GetViewId())
	require.NotEmpty(t, views[0].GetActiveResult())

	stored, err := fixture.meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "active", stored.GetBuildStatus())
}

func TestBuilderBuildsAllPagesOfPebbleFacts(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()

	facts := quantstore.New(fixture.root)
	var rows []*pb.DataRow
	for i := 1; i <= 1000; i++ {
		rows = append(rows, &pb.DataRow{
			Key: &pb.DataKey{
				Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
				DataTime: time.Date(2026, 6, 15, 0, i, 0, 0, time.UTC).Format(time.RFC3339),
				RowId:    "extra-" + strconv.Itoa(i),
			},
			Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", float64(i))},
		})
	}
	require.NoError(t, facts.WriteRows(ctx, rows, pb.WriteMode_WRITE_MODE_UPSERT))

	built, err := fixture.builder.Build(ctx, "crypto", "kline_view")
	require.NoError(t, err)

	_, rowsOut, page, err := fixture.views.QueryView(ctx, built.GetActiveResult(), &pb.QueryViewReq{
		SpaceId: "crypto",
		ViewId:  "kline_view",
		Page:    &pb.Page{Page: 1, Size: 2000},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1001), page.GetTotal())
	require.Len(t, rowsOut, 1001)
}

func TestBuilderRebuildsAllPagesOfPendingViews(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(root, "metadata.db"),
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
	defer meta.Close()
	require.NoError(t, meta.InitSchema(ctx))
	require.NoError(t, seedMinimalViewMetadata(ctx, meta))

	const totalViews = 1005
	for i := 0; i < totalViews; i++ {
		viewID := "paged_view_" + strconv.Itoa(i)
		_, err := meta.UpsertView(ctx, &pb.View{
			SpaceId:          "crypto",
			ViewId:           viewID,
			Name:             viewID,
			PrimaryDatasetId: "kline",
			DatasetIds:       []string{"kline"},
			GrainKeys:        []string{"subject_id", "data_time", "freq"},
			QueryWindow:      "30d",
			Status:           "active",
		})
		require.NoError(t, err)
		_, err = meta.UpsertViewColumn(ctx, &pb.ViewColumn{
			SpaceId:    "crypto",
			ViewId:     viewID,
			ColumnName: "close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "kline.close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		})
		require.NoError(t, err)
	}

	writer := &fakeViewWriter{}
	builder := viewbuilder.NewBuilder(viewbuilder.Options{
		Metadata: meta,
		Facts:    fakeFactReader{},
		Views:    writer,
		Now:      fixedNow,
	})

	built, err := builder.RebuildPendingViews(ctx, "crypto")
	require.NoError(t, err)
	require.Len(t, built, totalViews+1)
	require.Len(t, writer.tables, totalViews+1)
}

func TestBuilderBuildsAllPagesOfDatasetSubjects(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(root, "metadata.db"),
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
	defer meta.Close()
	require.NoError(t, meta.InitSchema(ctx))
	require.NoError(t, seedMinimalViewMetadata(ctx, meta))

	const totalSubjects = 1005
	for i := 0; i < totalSubjects; i++ {
		subjectID := "SUBJECT-" + strconv.Itoa(i)
		_, err := meta.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: subjectID, SubjectType: "crypto_pair", Name: subjectID})
		require.NoError(t, err)
		_, err = meta.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: subjectID})
		require.NoError(t, err)
	}
	writer := &fakeViewWriter{}
	builder := viewbuilder.NewBuilder(viewbuilder.Options{
		Metadata: meta,
		Facts:    fakeFactReader{},
		Views:    writer,
		Now:      fixedNow,
	})

	_, err = builder.Build(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Len(t, writer.rows, totalSubjects+1)
}

func TestBuilderRejectsOverlappingBuildForSameView(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	meta, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(root, "metadata.db"),
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
	defer meta.Close()
	require.NoError(t, meta.InitSchema(ctx))
	require.NoError(t, seedMinimalViewMetadata(ctx, meta))

	writer := &blockingViewWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	builder := viewbuilder.NewBuilder(viewbuilder.Options{
		Metadata: meta,
		Facts:    fakeFactReader{},
		Views:    writer,
		Now:      fixedNow,
	})

	firstErr := make(chan error, 1)
	go func() {
		_, err := builder.Build(ctx, "crypto", "kline_view")
		firstErr <- err
	}()
	<-writer.started

	_, err = builder.Build(ctx, "crypto", "kline_view")
	require.ErrorContains(t, err, "already running")
	close(writer.release)
	require.NoError(t, <-firstErr)
}

func TestBuilderJoinsColumnsFromMultipleDatasets(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()

	_, err := fixture.meta.UpsertDataSet(ctx, &pb.DataSet{
		SpaceId:      "crypto",
		DatasetId:    "factor",
		DataSourceId: "binance",
		Name:         "因子",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	})
	require.NoError(t, err)
	_, err = fixture.meta.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "factor", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertDataSetColumn(ctx, &pb.DataSetColumn{
		SpaceId:    "crypto",
		DatasetId:  "factor",
		ColumnName: "ma20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FACTOR,
		OriginId:   "ma20",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     "active",
	})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_factor_view",
		Name:             "K线因子视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline", "factor"},
		GrainKeys:        []string{"subject_id", "data_time", "freq"},
		QueryWindow:      "30d",
		Status:           "active",
	})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_factor_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_factor_view",
		ColumnName: "ma20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "factor.ma20",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)

	facts := quantstore.New(fixture.root)
	require.NoError(t, facts.WriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "factor", SubjectId: "APT-USDT", Freq: "1m"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{quantstore.DoubleValue("ma20", 7.9)},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	built, err := fixture.builder.Build(ctx, "crypto", "kline_factor_view")
	require.NoError(t, err)

	_, rows, _, err := fixture.views.QueryView(ctx, built.GetActiveResult(), &pb.QueryViewReq{
		SpaceId:    "crypto",
		ViewId:     "kline_factor_view",
		SubjectIds: []string{"APT-USDT"},
	})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	values := map[string]float64{}
	for _, value := range rows[0].GetValues() {
		values[value.GetColumnName()] = value.GetValue().GetDoubleValue()
	}
	require.Equal(t, 8.1, values["close"])
	require.Equal(t, 7.9, values["ma20"])
}

func TestBuilderJoinsRowsWithAmbiguousDimensionTextSeparately(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()

	_, err := fixture.meta.UpsertDataSet(ctx, &pb.DataSet{
		SpaceId:      "crypto",
		DatasetId:    "factor",
		DataSourceId: "binance",
		Name:         "因子",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	})
	require.NoError(t, err)
	_, err = fixture.meta.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "factor", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertDataSetColumn(ctx, &pb.DataSetColumn{
		SpaceId:    "crypto",
		DatasetId:  "factor",
		ColumnName: "ma20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FACTOR,
		OriginId:   "ma20",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     "active",
	})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "dimension_join_view",
		Name:             "维度 Join 视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline", "factor"},
		GrainKeys:        []string{"subject_id", "data_time", "freq", "dimensions"},
		QueryWindow:      "30d",
		Status:           "active",
	})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "dimension_join_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)
	_, err = fixture.meta.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "dimension_join_view",
		ColumnName: "ma20",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "factor.ma20",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)

	facts := quantstore.New(fixture.root)
	writeFact := func(datasetID string, dims map[string]string, column *pb.ColumnValue) *pb.DataRow {
		return &pb.DataRow{
			Key: &pb.DataKey{
				Scope: &pb.DataScope{
					SpaceId:    "crypto",
					DatasetId:  datasetID,
					SubjectId:  "APT-USDT",
					Freq:       "1m",
					Dimensions: dims,
				},
				DataTime: "2026-06-15T00:01:00Z",
			},
			Columns: []*pb.ColumnValue{column},
		}
	}
	require.NoError(t, facts.WriteRows(ctx, []*pb.DataRow{
		writeFact("kline", map[string]string{"a": "b&c=d"}, quantstore.DoubleValue("close", 1)),
		writeFact("kline", map[string]string{"a": "b", "c": "d"}, quantstore.DoubleValue("close", 2)),
		writeFact("factor", map[string]string{"a": "b&c=d"}, quantstore.DoubleValue("ma20", 10)),
		writeFact("factor", map[string]string{"a": "b", "c": "d"}, quantstore.DoubleValue("ma20", 20)),
	}, pb.WriteMode_WRITE_MODE_UPSERT))

	built, err := fixture.builder.Build(ctx, "crypto", "dimension_join_view")
	require.NoError(t, err)
	_, rows, _, err := fixture.views.QueryView(ctx, built.GetActiveResult(), &pb.QueryViewReq{
		SpaceId:    "crypto",
		ViewId:     "dimension_join_view",
		SubjectIds: []string{"APT-USDT"},
	})
	require.NoError(t, err)

	joined := map[float64]float64{}
	for _, row := range rows {
		var closeValue float64
		var ma20Value float64
		for _, value := range row.GetValues() {
			switch value.GetColumnName() {
			case "close":
				closeValue = value.GetValue().GetDoubleValue()
			case "ma20":
				ma20Value = value.GetValue().GetDoubleValue()
			}
		}
		if closeValue != 0 {
			joined[closeValue] = ma20Value
		}
	}
	require.Equal(t, 10.0, joined[1])
	require.Equal(t, 20.0, joined[2])
}

func TestBuilderRebuildsPendingViewsInAllSpaces(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()

	built, err := fixture.builder.RebuildPendingViewsInAllSpaces(ctx)
	require.NoError(t, err)
	require.Len(t, built, 1)
	require.Equal(t, "kline_view", built[0].GetViewId())
}

func TestHandleScheduleRebuildsPendingViews(t *testing.T) {
	ctx := context.Background()
	fixture := newBuilderFixture(t, ctx)
	defer fixture.close()
	viewbuilder.SetDefaultBuilder(fixture.builder)
	t.Cleanup(func() { viewbuilder.SetDefaultBuilder(nil) })

	require.NoError(t, viewbuilder.HandleSchedule(ctx, "space_id=crypto"))

	stored, err := fixture.meta.GetView(ctx, "crypto", "kline_view")
	require.NoError(t, err)
	require.Equal(t, "active", stored.GetBuildStatus())
	require.NotEmpty(t, stored.GetActiveResult())
}

type builderFixture struct {
	meta    *metasqlite.Store
	views   *deviceduckdb.ViewStore
	builder *viewbuilder.Builder
	root    string
}

func (f builderFixture) close() {
	_ = f.views.Close()
	_ = f.meta.Close()
}

func newBuilderFixture(t *testing.T, ctx context.Context) builderFixture {
	t.Helper()
	root := t.TempDir()

	meta, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       filepath.Join(root, "metadata.db"),
		SchemaPath: schemaPath(t),
	})
	require.NoError(t, err)
	require.NoError(t, meta.InitSchema(ctx))

	_, err = meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"})
	require.NoError(t, err)
	_, err = meta.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSet(ctx, &pb.DataSet{
		SpaceId:      "crypto",
		DatasetId:    "kline",
		DataSourceId: "binance",
		Name:         "K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	})
	require.NoError(t, err)
	_, err = meta.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"})
	require.NoError(t, err)
	_, err = meta.UpsertDataSetColumn(ctx, &pb.DataSetColumn{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD,
		OriginId:   "close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     "active",
	})
	require.NoError(t, err)
	_, err = meta.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		GrainKeys:        []string{"subject_id", "data_time", "freq"},
		QueryWindow:      "30d",
		Status:           "active",
	})
	require.NoError(t, err)
	_, err = meta.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	require.NoError(t, err)

	facts := quantstore.New(root)
	require.NoError(t, facts.WriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    &pb.DataScope{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m"},
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
	}}, pb.WriteMode_WRITE_MODE_UPSERT))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "duckdb"), 0o755))
	views, err := deviceduckdb.Open(deviceduckdb.Options{Path: filepath.Join(root, "duckdb", "views.duckdb")})
	require.NoError(t, err)

	builder := viewbuilder.NewBuilder(viewbuilder.Options{
		Metadata: meta,
		Facts:    facts,
		Views:    views,
		Now: func() time.Time {
			return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	return builderFixture{meta: meta, views: views, builder: builder, root: root}
}

func fixedNow() time.Time {
	return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
}

func seedMinimalViewMetadata(ctx context.Context, meta *metasqlite.Store) error {
	if _, err := meta.UpsertSpace(ctx, &pb.Space{SpaceId: "crypto", Name: "crypto"}); err != nil {
		return err
	}
	if _, err := meta.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange"}); err != nil {
		return err
	}
	if _, err := meta.UpsertDataSet(ctx, &pb.DataSet{
		SpaceId:      "crypto",
		DatasetId:    "kline",
		DataSourceId: "binance",
		Name:         "K线",
		DataKind:     pb.DataKind_DATA_KIND_TIME_SERIES,
		Freqs:        []string{"1m"},
		Status:       "active",
	}); err != nil {
		return err
	}
	if _, err := meta.UpsertSubject(ctx, &pb.Subject{SpaceId: "crypto", SubjectId: "APT-USDT", SubjectType: "crypto_pair", Name: "APT-USDT"}); err != nil {
		return err
	}
	if _, err := meta.BindDataSetSubject(ctx, &pb.DataSetSubject{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT"}); err != nil {
		return err
	}
	if _, err := meta.UpsertDataSetColumn(ctx, &pb.DataSetColumn{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_FIELD,
		OriginId:   "close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     "active",
	}); err != nil {
		return err
	}
	if _, err := meta.UpsertView(ctx, &pb.View{
		SpaceId:          "crypto",
		ViewId:           "kline_view",
		Name:             "K线视图",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline"},
		GrainKeys:        []string{"subject_id", "data_time", "freq"},
		QueryWindow:      "30d",
		Status:           "active",
	}); err != nil {
		return err
	}
	_, err := meta.UpsertViewColumn(ctx, &pb.ViewColumn{
		SpaceId:    "crypto",
		ViewId:     "kline_view",
		ColumnName: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
		OriginId:   "kline.close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
	})
	return err
}

type fakeFactReader struct{}

func (fakeFactReader) ReadRows(_ context.Context, scope *pb.DataScope, _ pb.ReadMode, _ *pb.TimeRange, _ string, _ []string, _ []string, _ *pb.Page) ([]*pb.DataRow, *pb.PageResult, error) {
	return []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope:    scope,
			DataTime: "2026-06-15T00:00:00Z",
		},
		Columns: []*pb.ColumnValue{quantstore.DoubleValue("close", 8.1)},
	}}, &pb.PageResult{Page: 1, Size: 1000, Total: 1}, nil
}

type fakeViewWriter struct {
	tables []string
	rows   []*pb.QueryViewRow
}

func (w *fakeViewWriter) CreateResultTable(_ context.Context, tableName string, _ []*pb.ViewColumn) error {
	w.tables = append(w.tables, tableName)
	return nil
}

func (w *fakeViewWriter) InsertRows(_ context.Context, _ string, rows []*pb.QueryViewRow) error {
	w.rows = append(w.rows, rows...)
	return nil
}

type blockingViewWriter struct {
	fakeViewWriter
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (w *blockingViewWriter) CreateResultTable(ctx context.Context, tableName string, columns []*pb.ViewColumn) error {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return w.fakeViewWriter.CreateResultTable(ctx, tableName, columns)
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../schema/storage_metadata.sql"))
}
