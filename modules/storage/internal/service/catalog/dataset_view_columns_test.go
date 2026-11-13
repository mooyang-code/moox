package catalog

import (
	"context"
	"path/filepath"
	"testing"

	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// newDatasetFixture 建一个带 close、bias_20 两列的时序数据集和对应的元数据服务。
func newDatasetFixture(t *testing.T) (*sqlite.Store, *Service, *pb.Dataset) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.InitSchema(ctx))
	_, err = store.UpsertSpace(ctx, &pb.Space{SpaceId: "factor-space", Name: "因子空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "factor-space", DataSourceId: "factor", Name: "因子计算"})
	require.NoError(t, err)
	dataset, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "factor-space", DatasetId: "dataset_factor_btc_1m", DataSourceId: "factor",
		Name: "BTC 因子", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	})
	require.NoError(t, err)
	for _, column := range []struct{ name, display string }{{"close", "收盘"}, {"bias_20", "偏离"}} {
		_, err := store.UpsertDatasetColumn(ctx, &pb.DatasetColumn{
			SpaceId: "factor-space", DatasetId: dataset.GetDatasetId(), ColumnName: column.name,
			OriginId:   column.name,
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Attributes: map[string]string{"display_name": column.display},
		})
		require.NoError(t, err)
	}
	service, err := NewMetadataService(store, nil, Options{Rows: noopRows{}})
	require.NoError(t, err)
	return store, service, dataset
}

// noopRows 是不存行的行存储：只关心元数据的测试用它。
type noopRows struct{}

func (noopRows) DeleteDatasetRows(context.Context, string, string) (uint64, error) { return 0, nil }

// createView 在数据集上建一个 View。View 的列由数据集列推出，这里传入的列会被忽略。
func createView(t *testing.T, store *sqlite.Store, dataset *pb.Dataset, viewID string) *pb.View {
	t.Helper()
	view, err := store.CreateView(context.Background(), &pb.View{
		SpaceId: dataset.GetSpaceId(), ViewId: viewID, Name: dataset.GetName(), DatasetId: dataset.GetDatasetId(),
		GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Freq: "1m", Engine: "duckdb", Status: "active",
		Columns: []*pb.ViewColumn{{ColumnName: "ignored"}},
	})
	require.NoError(t, err)
	return view
}

func viewColumnNames(columns []*pb.ViewColumn) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		if column != nil {
			names = append(names, column.GetColumnName())
		}
	}
	return names
}

func newDatasetColumn(dataset *pb.Dataset, name string, attrs map[string]string) *pb.DatasetColumn {
	return &pb.DatasetColumn{
		SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), ColumnName: name,
		OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_CUSTOM,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Attributes: attrs,
	}
}

// View 的列就是数据集里启用的列：数据集加列后 View 自动有这一列，不需要任何 View 侧的调用；
// 列按数据集列的创建顺序排列，属性取自数据集列；停用的列进隐藏列。
func TestViewColumnsFollowDatasetColumns(t *testing.T) {
	ctx := context.Background()
	store, service, dataset := newDatasetFixture(t)
	view := createView(t, store, dataset, "view_factor_btc_1m")
	require.Equal(t, []string{"close", "bias_20"}, viewColumnNames(view.GetColumns()), "写入时传的列被忽略")

	rsp, err := service.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: newDatasetColumn(dataset, "momentum_5", map[string]string{"display_name": "momentum_5", "factor_id": "Momentum"})})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	listed, err := service.ListViewColumns(ctx, &pb.ListViewColumnsReq{SpaceId: dataset.GetSpaceId(), ViewId: view.GetViewId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, listed.GetRetInfo().GetCode(), listed.GetRetInfo().GetMsg())
	require.Equal(t, []string{"close", "bias_20", "momentum_5"}, viewColumnNames(listed.GetColumns()))
	added := listed.GetColumns()[2]
	require.Equal(t, uint32(2), added.GetSortOrder())
	require.Equal(t, pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, added.GetOriginType())
	require.Equal(t, "momentum_5", added.GetOriginId())
	require.Equal(t, "Momentum", added.GetAttributes()["factor_id"], "属性取自数据集列")

	disabled := newDatasetColumn(dataset, "bias_20", map[string]string{"display_name": "偏离"})
	disabled.OriginType, disabled.OriginId, disabled.Status = pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_UNSPECIFIED, "bias_20", "disabled"
	_, err = store.UpsertDatasetColumn(ctx, disabled)
	require.NoError(t, err)
	got, err := service.GetView(ctx, &pb.GetViewReq{SpaceId: dataset.GetSpaceId(), ViewId: view.GetViewId()})
	require.NoError(t, err)
	require.Equal(t, []string{"close", "momentum_5"}, viewColumnNames(got.GetView().GetColumns()))
	require.Equal(t, []string{"bias_20"}, got.GetView().GetHiddenColumns())
}

// 元数据缓存里的 View 与底层推出的列一致。
func TestCachedViewCarriesDerivedColumns(t *testing.T) {
	ctx := context.Background()
	store, _, dataset := newDatasetFixture(t)
	createView(t, store, dataset, "view_factor_btc_1m")
	cached, err := metacache.New(ctx, store, metacache.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cached.Close() })
	_, err = store.UpsertDatasetColumn(ctx, newDatasetColumn(dataset, "momentum_5", map[string]string{"display_name": "momentum_5"}))
	require.NoError(t, err)
	require.NoError(t, cached.Refresh(ctx))
	view, err := cached.GetView(ctx, dataset.GetSpaceId(), "view_factor_btc_1m")
	require.NoError(t, err)
	require.Equal(t, []string{"close", "bias_20", "momentum_5"}, viewColumnNames(view.GetColumns()))
	columns, _, err := cached.ListViewColumns(ctx, dataset.GetSpaceId(), "view_factor_btc_1m", nil)
	require.NoError(t, err)
	require.Equal(t, []string{"close", "bias_20", "momentum_5"}, viewColumnNames(columns))
}

// 数据集的 View 由创建它的模块自己管理：Storage 不再替它删除，有 View 引用的数据集不能删。
func TestDatasetWithViewCannotBeDeletedUntilViewIsRemoved(t *testing.T) {
	ctx := context.Background()
	store, service, dataset := newDatasetFixture(t)
	createView(t, store, dataset, "view_factor_btc_1m")

	rsp, err := service.DeleteDataset(ctx, &pb.DeleteDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "仍有 View 引用数据集")

	require.NoError(t, store.DeleteView(ctx, dataset.GetSpaceId(), "view_factor_btc_1m"))
	rsp, err = service.DeleteDataset(ctx, &pb.DeleteDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
}

func TestCustomColumnHasNoOriginID(t *testing.T) {
	_, service, dataset := newDatasetFixture(t)
	rsp, err := service.UpsertDatasetColumn(context.Background(), &pb.UpsertDatasetColumnReq{
		Column: newDatasetColumn(dataset, "bias__10", map[string]string{"display_name": "bias__10"}),
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.Empty(t, rsp.GetColumn().GetOriginId(), "自定义列的身份就是列名，不带来源 ID")
}

func TestMetadataReturnsDatasetRetentionFromPolicy(t *testing.T) {
	_, service, dataset := newDatasetFixture(t)
	ctx := context.Background()
	got, err := service.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, got.GetRetInfo().GetCode(), got.GetRetInfo().GetMsg())
	require.Equal(t, "840h", got.GetDataset().GetRetention(), "a 1m Dataset in a space without overrides keeps 35 days")
	require.Equal(t, "default", got.GetDataset().GetRetentionSource())

	listed, err := service.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: dataset.GetSpaceId()})
	require.NoError(t, err)
	require.Len(t, listed.GetDatasets(), 1)
	require.Equal(t, "840h", listed.GetDatasets()[0].GetRetention())

	retention, source := service.datasetRetention(&pb.Dataset{DataKind: pb.DataKind_DATA_KIND_RECORD})
	require.Equal(t, "forever", retention)
	require.Equal(t, "record", source)
	service.retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "60d"}}
	retention, source = service.datasetRetention(&pb.Dataset{SpaceId: "stockcn", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m"})
	require.Equal(t, "1440h", retention)
	require.Equal(t, "space", source)
}

func TestGetDatasetReturnsLiveSubjectsRevision(t *testing.T) {
	store, service, dataset := newDatasetFixture(t)
	ctx := context.Background()
	first, err := service.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode(), first.GetRetInfo().GetMsg())

	_, err = store.UpsertSubject(ctx, &pb.Subject{SpaceId: dataset.GetSpaceId(), SubjectId: "REV-USDT", Name: "REV"})
	require.NoError(t, err)
	second, err := service.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Greater(t, second.GetSubjectsRevision(), first.GetSubjectsRevision(), "新增标的后 GetDataset 返回的修订号必须递增")
}
