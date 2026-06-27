package admin

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/require"
)

func TestCreateWorkspaceAndConfigureDataSet(t *testing.T) {
	svc := NewService()
	ctx := context.Background()

	workspaceRsp, err := svc.CreateWorkspaceWithDefaults(ctx, &pb.CreateWorkspaceWithDefaultsReq{
		Workspace: &pb.WorkspaceSummary{Name: "default", DisplayName: "Default"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, workspaceRsp.GetRetInfo().GetCode())
	require.Equal(t, "default", workspaceRsp.GetWorkspace().GetWorkspaceId())

	datasetRsp, err := svc.ConfigureDataSet(ctx, &pb.ConfigureDataSetReq{
		Dataset: &pb.DataSetConfig{WorkspaceId: "default", Name: "binance_spot_kline_1m", DataKind: "TIME_SERIES", DataDomain: "MARKET_BAR"},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, datasetRsp.GetRetInfo().GetCode())
	require.Equal(t, "binance_spot_kline_1m", datasetRsp.GetDataset().GetDatasetId())
}
