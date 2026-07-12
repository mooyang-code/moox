package access

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpaceAndViewCRUDFlow(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto", Name: "Crypto", Status: "active",
	}})
	mustRetOK(t, spaceRsp, err)

	dataSourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active",
	}})
	mustRetOK(t, dataSourceRsp, err)

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance",
		Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active",
		Freqs: []string{"1m"},
	}})
	mustRetOK(t, datasetRsp, err)

	viewRsp, err := svc.CreateView(ctx, &pb.CreateViewReq{View: &pb.View{
		SpaceId: "crypto", ViewId: "kline_view", Name: "K线视图",
		DatasetIds: []string{"kline"}, Engine: "duckdb", Status: "active",
		FilterJson: `{"freq":"1m"}`,
	}})
	mustRetOK(t, viewRsp, err)

	getViewRsp, err := svc.GetView(ctx, &pb.GetViewReq{SpaceId: "crypto", ViewId: "kline_view"})
	mustRetOK(t, getViewRsp, err)

	viewRsp.GetView().Name = "K线视图更新"
	updateViewRsp, err := svc.UpdateView(ctx, &pb.UpdateViewReq{View: viewRsp.GetView()})
	mustRetOK(t, updateViewRsp, err)

	listViewsRsp, err := svc.ListViews(ctx, &pb.ListViewsReq{SpaceId: "crypto"})
	mustRetOK(t, listViewsRsp, err)
	require.NotEmpty(t, listViewsRsp.GetViews())

	colRsp, err := svc.UpsertViewColumn(ctx, &pb.UpsertViewColumnReq{Column: &pb.ViewColumn{
		SpaceId: "crypto", ViewId: "kline_view", ColumnName: "close",
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Attributes: map[string]string{"display_name": "收盘价"},
	}})
	mustRetOK(t, colRsp, err)

	listColsRsp, err := svc.ListViewColumns(ctx, &pb.ListViewColumnsReq{SpaceId: "crypto", ViewId: "kline_view"})
	mustRetOK(t, listColsRsp, err)

	listSpacesRsp, err := svc.ListSpaces(ctx, &pb.ListSpacesReq{})
	mustRetOK(t, listSpacesRsp, err)

	updateSpaceRsp, err := svc.UpdateSpace(ctx, &pb.UpdateSpaceReq{Space: spaceRsp.GetSpace()})
	mustRetOK(t, updateSpaceRsp, err)
}

func TestCreateViewRejectsMissingFields(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	rsp, err := svc.CreateView(context.Background(), &pb.CreateViewReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}
