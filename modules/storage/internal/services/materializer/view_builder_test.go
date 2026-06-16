package materializer_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	deviceduckdb "github.com/mooyang-code/moox/modules/storage/internal/services/device/duckdb"
	"github.com/mooyang-code/moox/modules/storage/internal/services/materializer"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/services/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestBuilderMaterializesViewFromPebbleFacts(t *testing.T) {
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

type builderFixture struct {
	meta    *metasqlite.Store
	views   *deviceduckdb.ViewStore
	builder *materializer.Builder
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

	builder := materializer.NewBuilder(materializer.Options{
		Metadata: meta,
		Facts:    facts,
		Views:    views,
		Now: func() time.Time {
			return time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)
		},
	})
	return builderFixture{meta: meta, views: views, builder: builder}
}

func schemaPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../../../schema/storage_metadata.sql"))
}
