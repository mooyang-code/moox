package view

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/catalog"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestSingleDatasetView(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.InitSchema(ctx))

	_, err = store.UpsertSpace(ctx, &pb.Space{SpaceId: "space", Name: "空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "space", DataSourceId: "source", Name: "来源", Kind: "internal"})
	require.NoError(t, err)
	_, err = store.RegisterDataNode(ctx, "node-a", "trpc://node-a", "node-a")
	require.NoError(t, err)
	prices, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_prices", DataSourceId: "source", DataNodeId: "node-a",
		Name: "行情", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, KeepDuration: "0",
	})
	require.NoError(t, err)
	_, err = store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_fundamentals", DataSourceId: "source", DataNodeId: "node-a",
		Name: "基本面", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, KeepDuration: "0",
	})
	require.NoError(t, err)

	svc, err := catalog.NewMetadataService(store, nil, catalog.Options{AuthSecret: "secret"})
	require.NoError(t, err)

	closeCol := &pb.ViewColumn{
		SpaceId: "space", ViewId: "view_close", ColumnName: "dataset_prices.close", OriginId: "dataset_prices.close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "收盘价"},
	}
	volumeCol := &pb.ViewColumn{
		SpaceId: "space", ViewId: "view_volume", ColumnName: "dataset_prices.volume", OriginId: "dataset_prices.volume",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "成交量"},
	}

	t.Run("two projections on one dataset succeed", func(t *testing.T) {
		closeRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_close", Name: "收盘视图", DatasetId: prices.GetDatasetId(),
			FilterJson: `{"freq":"1m"}`, Columns: []*pb.ViewColumn{closeCol},
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, closeRsp.GetRetInfo().GetCode(), closeRsp.GetRetInfo().GetMsg())

		volumeRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_volume", Name: "成交视图", DatasetId: prices.GetDatasetId(),
			FilterJson: `{"freq":"1m"}`, Columns: []*pb.ViewColumn{volumeCol},
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, volumeRsp.GetRetInfo().GetCode(), volumeRsp.GetRetInfo().GetMsg())

		listed, err := svc.ListViews(ctx, &pb.ListViewsReq{SpaceId: "space", DatasetId: prices.GetDatasetId()})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, listed.GetRetInfo().GetCode())
		require.Len(t, listed.GetViews(), 2)
	})

	t.Run("multiple source datasets are rejected", func(t *testing.T) {
		rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_joined", Name: "多源视图",
			DatasetId:  "dataset_prices",
			FilterJson: `{"freq":"1m"}`,
			Columns: []*pb.ViewColumn{
				closeCol,
				{
					SpaceId: "space", ViewId: "view_joined", ColumnName: "dataset_fundamentals.pe",
					OriginId: "dataset_fundamentals.pe", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		}})
		require.NoError(t, err)
		require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "multi-dataset view must be rejected")
	})

	t.Run("rebuild keeps filter and projection", func(t *testing.T) {
		got, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, got.GetRetInfo().GetCode())
		filter := got.GetView().GetFilterJson()
		require.Contains(t, filter, `"1m"`)

		rebuild, err := svc.RequestViewRebuild(ctx, &pb.RequestViewRebuildReq{
			AuthInfo: &pb.AuthInfo{AppId: "admin-gateway", AppKey: datanode.ServiceAuthKey("secret", "admin-gateway")},
			SpaceId:  "space", ViewId: "view_close",
		})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, rebuild.GetRetInfo().GetCode(), rebuild.GetRetInfo().GetMsg())

		after, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, filter, after.GetView().GetFilterJson())
		require.Equal(t, prices.GetDatasetId(), after.GetView().GetDatasetId())
		cols, err := svc.ListViewColumns(ctx, &pb.ListViewColumnsReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, cols.GetRetInfo().GetCode())
		require.Len(t, cols.GetColumns(), 1)
		require.Equal(t, "dataset_prices.close", cols.GetColumns()[0].GetColumnName())
	})

	t.Run("deleting one view keeps dataset and the other view", func(t *testing.T) {
		deleted, err := svc.UpdateView(ctx, &pb.UpdateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_volume", Name: "成交视图", DatasetId: prices.GetDatasetId(),
			FilterJson: `{"freq":"1m"}`, Status: "deleted",
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, deleted.GetRetInfo().GetCode(), deleted.GetRetInfo().GetMsg())

		dataset, err := store.GetDataset(ctx, "space", prices.GetDatasetId())
		require.NoError(t, err)
		require.Equal(t, prices.GetDatasetId(), dataset.GetDatasetId())

		remaining, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, remaining.GetRetInfo().GetCode())
		require.NotEqual(t, "deleted", remaining.GetView().GetStatus())
	})
}
