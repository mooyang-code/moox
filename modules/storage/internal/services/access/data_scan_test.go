package access

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/router"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodePrimaryScanCursorParsesTargetAndInnerCursor(t *testing.T) {
	idx, inner, err := decodePrimaryScanCursor("2|page-3")
	require.NoError(t, err)
	assert.Equal(t, 2, idx)
	assert.Equal(t, "page-3", inner)

	idx, inner, err = decodePrimaryScanCursor("legacy")
	require.NoError(t, err)
	assert.Equal(t, 0, idx)
	assert.Equal(t, "legacy", inner)
}

func TestScanTimeSeriesDatasetReturnsSortedRows(t *testing.T) {
	svc := &Service{
		primary: fakePrimaryScanner{rowsPerPage: 2, pages: 1},
		router:  router.NewResolver(fakeRouteReader{}),
	}
	rsp, err := svc.ReadTimeSeriesRows(context.Background(), &pb.ReadTimeSeriesRowsReq{
		Keys: []*pb.TimeSeriesKey{{SpaceId: "crypto", DatasetId: "kline"}},
		Page: &pb.Page{Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Len(t, rsp.GetRows(), 2)
}

func TestScanRecordRowsRPCRejectsMissingIDs(t *testing.T) {
	svc := &Service{}
	rsp, err := svc.ScanRecordRows(context.Background(), &pb.ScanRecordRowsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestNextRecordVersionIncreasesMonotonically(t *testing.T) {
	svc := &Service{}
	first := svc.nextRecordVersion()
	second := svc.nextRecordVersion()
	assert.True(t, second.After(first))
}

func TestCreateDatasetRejectsMissingFields(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	rsp, err := svc.CreateDataset(context.Background(), &pb.CreateDatasetReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCreatePrimaryStoreNodePersistsGeneratedID(t *testing.T) {
	store := &stubMetadataStore{}
	svc := &Service{metadata: store}
	rsp, err := svc.CreatePrimaryStoreNode(context.Background(), &pb.CreatePrimaryStoreNodeReq{
		Node: &pb.PrimaryStoreNode{Name: "node-a"},
	})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetNode().GetNodeId())
	assert.Equal(t, rsp.GetNode().GetNodeId(), store.lastNode.GetNodeId())
}

func TestListDevicesReturnsStoredDevices(t *testing.T) {
	store := &stubMetadataStore{
		devices: []*pb.Device{{DeviceId: "dev-1", NodeId: "node-1", Engine: "pebble"}},
	}
	svc := &Service{metadata: store}
	rsp, err := svc.ListDevices(context.Background(), &pb.ListDevicesReq{NodeId: "node-1"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetDevices(), 1)
	assert.Equal(t, "dev-1", rsp.GetDevices()[0].GetDeviceId())
}

func TestNormalizeWriteRecordRowsFillsVersion(t *testing.T) {
	svc := &Service{}
	rows := svc.normalizeWriteRecordRows([]*pb.RecordRow{{
		Key: &pb.RecordKey{SpaceId: "crypto", DatasetId: "news", RecordId: "news-1"},
	}})
	require.Len(t, rows, 1)
	_, err := time.Parse(time.RFC3339Nano, rows[0].GetKey().GetVersion())
	assert.NoError(t, err)
}
