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
		Name: "行情", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	})
	require.NoError(t, err)
	_, err = store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "space", DatasetId: "dataset_fundamentals", DataSourceId: "source", DataNodeId: "node-a",
		Name: "基本面", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	})
	require.NoError(t, err)

	for _, column := range []*pb.DatasetColumn{
		{SpaceId: "space", DatasetId: "dataset_prices", ColumnName: "close", OriginId: "close"},
		{SpaceId: "space", DatasetId: "dataset_prices", ColumnName: "volume", OriginId: "volume"},
		{SpaceId: "space", DatasetId: "dataset_fundamentals", ColumnName: "pe", OriginId: "pe"},
	} {
		column.OriginType = pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD
		column.ValueType = pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
		column.Status = "active"
		_, err = store.UpsertDatasetColumn(ctx, column)
		require.NoError(t, err)
	}

	svc, err := catalog.NewMetadataService(store, nil, catalog.Options{AuthSecret: "secret"})
	require.NoError(t, err)

	closeCol := &pb.ViewColumn{
		SpaceId: "space", ViewId: "view_close", ColumnName: "close", OriginId: "close",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "收盘价"},
	}
	volumeCol := &pb.ViewColumn{
		SpaceId: "space", ViewId: "view_volume", ColumnName: "volume", OriginId: "volume",
		OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN, ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "成交量"},
	}

	t.Run("two projections on one dataset succeed", func(t *testing.T) {
		closeRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_close", Name: "收盘视图", DatasetId: prices.GetDatasetId(),
			Freq: "1m", Columns: []*pb.ViewColumn{closeCol},
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, closeRsp.GetRetInfo().GetCode(), closeRsp.GetRetInfo().GetMsg())

		volumeRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_volume", Name: "成交视图", DatasetId: prices.GetDatasetId(),
			Freq: "1m", Columns: []*pb.ViewColumn{volumeCol},
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, volumeRsp.GetRetInfo().GetCode(), volumeRsp.GetRetInfo().GetMsg())

		listed, err := svc.ListViews(ctx, &pb.ListViewsReq{SpaceId: "space", DatasetId: prices.GetDatasetId()})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, listed.GetRetInfo().GetCode())
		require.Len(t, listed.GetViews(), 2)
	})

	t.Run("a column of another dataset is rejected", func(t *testing.T) {
		rsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_joined", Name: "多源视图",
			DatasetId: "dataset_prices",
			Freq:      "1m",
			Columns: []*pb.ViewColumn{
				closeCol,
				{
					SpaceId: "space", ViewId: "view_joined", ColumnName: "pe",
					OriginId: "pe", OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
					ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				},
			},
		}})
		require.NoError(t, err)
		require.NotEqual(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), "a View may only index columns of its own dataset")
		require.Contains(t, rsp.GetRetInfo().GetMsg(), `view column "pe" is not an active column of dataset dataset_prices`)
	})

	t.Run("rebuild keeps freq and projection", func(t *testing.T) {
		got, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, got.GetRetInfo().GetCode())
		require.Equal(t, "1m", got.GetView().GetFreq())

		rebuild, err := svc.RequestViewRebuild(ctx, &pb.RequestViewRebuildReq{
			AuthInfo: &pb.AuthInfo{AppId: "console", AppKey: datanode.ServiceAuthKey("secret", "console")},
			SpaceId:  "space", ViewId: "view_close",
		})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, rebuild.GetRetInfo().GetCode(), rebuild.GetRetInfo().GetMsg())

		after, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, "1m", after.GetView().GetFreq())
		require.Equal(t, prices.GetDatasetId(), after.GetView().GetDatasetId())
		cols, err := svc.ListViewColumns(ctx, &pb.ListViewColumnsReq{SpaceId: "space", ViewId: "view_close"})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, cols.GetRetInfo().GetCode())
		require.Len(t, cols.GetColumns(), 1)
		require.Equal(t, "close", cols.GetColumns()[0].GetColumnName())
	})

	t.Run("deleting one view keeps dataset and the other view", func(t *testing.T) {
		deleted, err := svc.UpdateView(ctx, &pb.UpdateViewReq{View: &pb.View{
			SpaceId: "space", ViewId: "view_volume", Name: "成交视图", DatasetId: prices.GetDatasetId(),
			Freq: "1m", Status: "deleted",
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
