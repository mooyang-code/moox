package view

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestHandleScheduleSkipsUnsupportedOp(t *testing.T) {
	require.NoError(t, HandleSchedule(context.Background(), "op=unknown"))
}

func TestHandleScheduleSkipsWhenManagerMissing(t *testing.T) {
	SetDefaultMaintenance(nil)
	require.NoError(t, HandleSchedule(context.Background(), "op=maintain&space_id=crypto"))
}

func TestHandleScheduleToleratesInvalidParams(t *testing.T) {
	require.NoError(t, HandleSchedule(context.Background(), "%"))
}

func TestSetDefaultMaintenanceStoresManager(t *testing.T) {
	manager := &MaintenanceManager{}
	SetDefaultMaintenance(manager)
	assert.Equal(t, manager, currentDefaultMaintenance())
	SetDefaultMaintenance(nil)
}

func TestRemoteMetadataGetViewReturnsView(t *testing.T) {
	proxy := &metadataProxyStub{
		getViewRsp: &pb.GetViewRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, View: &pb.View{SpaceId: "crypto", ViewId: "kline_view"}},
	}
	meta := &RemoteMetadata{proxy: proxy}
	view, err := meta.GetView(context.Background(), "crypto", "kline_view")
	require.NoError(t, err)
	assert.Equal(t, "kline_view", view.GetViewId())
}

func TestRemoteMetadataGetViewIndexBuildRequiresBuildState(t *testing.T) {
	proxy := &metadataProxyStub{
		getViewRsp: &pb.GetViewRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, View: &pb.View{SpaceId: "crypto", ViewId: "v1"}},
	}
	meta := &RemoteMetadata{proxy: proxy}
	_, err := meta.GetViewIndexBuild(context.Background(), "crypto", "v1")
	require.Error(t, err)
}

func TestRemoteMetadataListViewColumnsReturnsColumns(t *testing.T) {
	proxy := &metadataProxyStub{
		listViewColumnsRsp: &pb.ListViewColumnsRsp{
			RetInfo:    &pb.RetInfo{Code: pb.ErrorCode_SUCCESS},
			Columns:    []*pb.ViewColumn{{SpaceId: "crypto", ViewId: "v1", ColumnName: "close"}},
			PageResult: &pb.PageResult{},
		},
	}
	meta := &RemoteMetadata{proxy: proxy}
	cols, page, err := meta.ListViewColumns(context.Background(), "crypto", "v1", nil)
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.NotNil(t, page)
}

type metadataProxyStub struct {
	pb.MetadataClientProxy
	getViewRsp         *pb.GetViewRsp
	listViewColumnsRsp *pb.ListViewColumnsRsp
}

func (s *metadataProxyStub) GetView(context.Context, *pb.GetViewReq, ...client.Option) (*pb.GetViewRsp, error) {
	return s.getViewRsp, nil
}

func (s *metadataProxyStub) ListViewColumns(context.Context, *pb.ListViewColumnsReq, ...client.Option) (*pb.ListViewColumnsRsp, error) {
	return s.listViewColumnsRsp, nil
}
