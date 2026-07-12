package access

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMetadataInfraCRUDFlow(t *testing.T) {
	ctx := context.Background()
	svc := NewServiceWithOptions(Options{
		Root:           t.TempDir(),
		InitSchemaPath: storageSchemaPath(t),
	})
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	spaceRsp, err := svc.CreateSpace(ctx, &pb.CreateSpaceReq{Space: &pb.Space{
		SpaceId: "crypto", Name: "Crypto", Status: "active",
	}})
	mustRetOK(t, spaceRsp, err)

	nodeRsp, err := svc.CreatePrimaryStoreNode(ctx, &pb.CreatePrimaryStoreNodeReq{
		Node: &pb.PrimaryStoreNode{NodeId: "node-1", Name: "Node 1", Status: "active"},
	})
	mustRetOK(t, nodeRsp, err)

	getNodeRsp, err := svc.GetPrimaryStoreNode(ctx, &pb.GetPrimaryStoreNodeReq{NodeId: "node-1"})
	mustRetOK(t, getNodeRsp, err)

	nodeRsp.GetNode().Name = "Node One"
	updateNodeRsp, err := svc.UpdatePrimaryStoreNode(ctx, &pb.UpdatePrimaryStoreNodeReq{Node: nodeRsp.GetNode()})
	mustRetOK(t, updateNodeRsp, err)

	listNodesRsp, err := svc.ListPrimaryStoreNodes(ctx, &pb.ListPrimaryStoreNodesReq{})
	mustRetOK(t, listNodesRsp, err)

	deviceRsp, err := svc.CreateDevice(ctx, &pb.CreateDeviceReq{Device: &pb.Device{
		DeviceId: "dev-1", NodeId: "node-1", Engine: "pebble", Status: "active",
	}})
	mustRetOK(t, deviceRsp, err)

	getDeviceRsp, err := svc.GetDevice(ctx, &pb.GetDeviceReq{DeviceId: "dev-1"})
	mustRetOK(t, getDeviceRsp, err)

	deviceRsp.GetDevice().Engine = "pebble"
	updateDeviceRsp, err := svc.UpdateDevice(ctx, &pb.UpdateDeviceReq{Device: deviceRsp.GetDevice()})
	mustRetOK(t, updateDeviceRsp, err)

	listDevicesRsp, err := svc.ListDevices(ctx, &pb.ListDevicesReq{NodeId: "node-1"})
	mustRetOK(t, listDevicesRsp, err)

	dataSourceRsp, err := svc.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{
		SpaceId: "crypto", DataSourceId: "binance", Name: "Binance", Kind: "exchange", Status: "active",
	}})
	mustRetOK(t, dataSourceRsp, err)

	datasetRsp, err := svc.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId: "crypto", DatasetId: "kline", DataSourceId: "binance",
		Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active",
	}})
	mustRetOK(t, datasetRsp, err)

	routeRsp, err := svc.CreatePrimaryStoreRoute(ctx, &pb.CreatePrimaryStoreRouteReq{PrimaryStoreRoute: &pb.PrimaryStoreRoute{
		SpaceId: "crypto", DatasetId: "kline", NodeId: "node-1", Status: "active",
	}})
	mustRetOK(t, routeRsp, err)

	getRouteRsp, err := svc.GetPrimaryStoreRoute(ctx, &pb.GetPrimaryStoreRouteReq{
		SpaceId: "crypto", RouteId: routeRsp.GetPrimaryStoreRoute().GetRouteId(),
	})
	mustRetOK(t, getRouteRsp, err)

	updateRouteRsp, err := svc.UpdatePrimaryStoreRoute(ctx, &pb.UpdatePrimaryStoreRouteReq{
		PrimaryStoreRoute: routeRsp.GetPrimaryStoreRoute(),
	})
	mustRetOK(t, updateRouteRsp, err)

	listRoutesRsp, err := svc.ListPrimaryStoreRoutes(ctx, &pb.ListPrimaryStoreRoutesReq{SpaceId: "crypto"})
	mustRetOK(t, listRoutesRsp, err)

	archiveRsp, err := svc.RegisterArchiveFile(ctx, &pb.RegisterArchiveFileReq{ArchiveFile: &pb.ArchiveFile{
		SpaceId: "crypto", DatasetId: "kline", DeviceId: "dev-1",
		FileUri: "file:///tmp/kline.parquet", MinTime: "2026-07-01T00:00:00Z", MaxTime: "2026-07-02T00:00:00Z",
	}})
	mustRetOK(t, archiveRsp, err)

	listArchiveRsp, err := svc.ListArchiveFiles(ctx, &pb.ListArchiveFilesReq{
		SpaceId: "crypto", DatasetId: "kline", DeviceId: "dev-1",
		TimeRange: &pb.TimeRange{StartTime: "2026-07-01T00:00:00Z", EndTime: "2026-07-03T00:00:00Z"},
	})
	mustRetOK(t, listArchiveRsp, err)
	require.NotEmpty(t, listArchiveRsp.GetArchiveFiles())
}

func TestPageSliceReturnsPagedResult(t *testing.T) {
	items := []string{"a", "b", "c", "d"}
	paged, page := pageSlice(items, &pb.Page{Page: 2, Size: 2})
	assert.Equal(t, []string{"c", "d"}, paged)
	assert.Equal(t, uint32(4), page.GetTotal())
	assert.False(t, page.GetHasMore())
}

func TestArchiveFileOverlapsFiltersByTimeRange(t *testing.T) {
	item := &pb.ArchiveFile{MinTime: "2026-07-10T00:00:00Z", MaxTime: "2026-07-11T00:00:00Z"}
	assert.True(t, archiveFileOverlaps(item, nil))
	assert.True(t, archiveFileOverlaps(item, &pb.TimeRange{
		StartTime: "2026-07-10T12:00:00Z", EndTime: "2026-07-10T13:00:00Z",
	}))
	assert.False(t, archiveFileOverlaps(item, &pb.TimeRange{
		StartTime: "2026-07-12T00:00:00Z", EndTime: "2026-07-13T00:00:00Z",
	}))
}

func TestDefaultIDNormalizesName(t *testing.T) {
	assert.Equal(t, "hello_world", defaultID("Hello World", "prefix"))
	assert.Contains(t, defaultID("", "node"), "node_")
}
