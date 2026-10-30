package catalog

import (
	"context"
	"path/filepath"
	"testing"

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
	_, err = store.RegisterDataNode(ctx, "factor-node", "ip://127.0.0.1:19090", "因子节点")
	require.NoError(t, err)
	dataset, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "factor-space", DatasetId: "dataset_factor_btc_1m", DataSourceId: "factor", DataNodeId: "factor-node",
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
	service, err := NewMetadataService(store, nil, Options{
		AuthSecret:       "secret",
		NodeStateChecker: &fakeNodeStateChecker{rsp: readyNodeState("factor-node")},
	})
	require.NoError(t, err)
	return store, service, dataset
}

// createView 在数据集上建一个 View；follow 为真时声明 follow_dataset_columns。
func createView(t *testing.T, store *sqlite.Store, dataset *pb.Dataset, viewID string, follow bool) *pb.View {
	t.Helper()
	attrs := map[string]string{}
	if follow {
		attrs["follow_dataset_columns"] = "true"
	}
	columns := []*pb.ViewColumn{}
	for index, name := range []string{"close", "bias_20"} {
		columns = append(columns, &pb.ViewColumn{
			SpaceId: dataset.GetSpaceId(), ViewId: viewID, ColumnName: name,
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, OriginId: name,
			ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, SortOrder: uint32(index),
			Attributes: map[string]string{"display_name": name},
		})
	}
	view, err := store.CreateView(context.Background(), &pb.View{
		SpaceId: dataset.GetSpaceId(), ViewId: viewID, Name: dataset.GetName(), DatasetId: dataset.GetDatasetId(),
		GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Freq: "1m", Engine: "duckdb", Status: "active",
		Columns: columns, Attributes: attrs,
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

func TestFollowingViewExtendsWhenDatasetGainsColumn(t *testing.T) {
	ctx := context.Background()
	store, _, dataset := newDatasetFixture(t)
	view := createView(t, store, dataset, "view_factor_btc_1m", true)

	column := newDatasetColumn(dataset, "momentum_5", map[string]string{"display_name": "momentum_5"})
	_, err := store.UpsertDatasetColumn(ctx, column)
	require.NoError(t, err)
	view, err = store.GetView(ctx, dataset.GetSpaceId(), view.GetViewId())
	require.NoError(t, err)
	require.Contains(t, viewColumnNames(view.GetColumns()), "momentum_5")
	columns := len(view.GetColumns())

	_, err = store.UpsertDatasetColumn(ctx, column)
	require.NoError(t, err)
	view, err = store.GetView(ctx, dataset.GetSpaceId(), view.GetViewId())
	require.NoError(t, err)
	require.Len(t, view.GetColumns(), columns, "重复 upsert 不重复加列")
}

func TestViewWithoutFollowAttributeIsNotExtended(t *testing.T) {
	ctx := context.Background()
	store, _, dataset := newDatasetFixture(t)
	view := createView(t, store, dataset, "view_plain", false)

	_, err := store.UpsertDatasetColumn(ctx, newDatasetColumn(dataset, "momentum_5", map[string]string{"display_name": "momentum_5"}))
	require.NoError(t, err)
	view, err = store.GetView(ctx, dataset.GetSpaceId(), view.GetViewId())
	require.NoError(t, err)
	require.NotContains(t, viewColumnNames(view.GetColumns()), "momentum_5")
}

func TestFollowingViewColumnAttributeChangeRefreshes(t *testing.T) {
	ctx := context.Background()
	store, _, dataset := newDatasetFixture(t)
	createView(t, store, dataset, "view_factor_btc_1m", true)
	column := func(factorID string) *pb.DatasetColumn {
		return newDatasetColumn(dataset, "fast_bias", map[string]string{"display_name": "fast_bias", "factor_id": factorID})
	}
	_, err := store.UpsertDatasetColumn(ctx, column("BiasA"))
	require.NoError(t, err)
	view, err := store.GetView(ctx, dataset.GetSpaceId(), "view_factor_btc_1m")
	require.NoError(t, err)
	find := func(view *pb.View) *pb.ViewColumn {
		for _, item := range view.GetColumns() {
			if item.GetColumnName() == "fast_bias" {
				return item
			}
		}
		return nil
	}
	before := find(view)
	require.NotNil(t, before)
	require.Equal(t, "BiasA", before.GetAttributes()["factor_id"])

	_, err = store.UpsertDatasetColumn(ctx, column("BiasB"))
	require.NoError(t, err)
	view, err = store.GetView(ctx, dataset.GetSpaceId(), "view_factor_btc_1m")
	require.NoError(t, err)
	after := find(view)
	require.NotNil(t, after)
	require.Equal(t, "BiasB", after.GetAttributes()["factor_id"])
	require.Equal(t, before.GetSortOrder(), after.GetSortOrder(), "只刷新属性，排序不动")
}

// 数据集的 View 由创建它的模块自己管理：Storage 不再替它删除，有 View 引用的数据集不能删。
func TestDatasetWithViewCannotBeDeletedUntilViewIsRemoved(t *testing.T) {
	ctx := context.Background()
	store, service, dataset := newDatasetFixture(t)
	createView(t, store, dataset, "view_factor_btc_1m", true)

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
