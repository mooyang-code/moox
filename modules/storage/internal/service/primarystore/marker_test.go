package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type periodMarkerRecordingNode struct {
	*recordingMarkerNode
	appended *pb.AppendDatasetPeriodCompletedReq
	read     *pb.GetDatasetPeriodCompletedMarkerReq
}

func (n *periodMarkerRecordingNode) AppendDatasetPeriodCompleted(_ context.Context, req *pb.AppendDatasetPeriodCompletedReq) (*pb.AppendDatasetPeriodCompletedRsp, error) {
	n.appended = req
	return &pb.AppendDatasetPeriodCompletedRsp{RetInfo: successRetInfo(), EventId: "period-marker"}, nil
}

func (n *periodMarkerRecordingNode) GetDatasetPeriodCompletedMarker(_ context.Context, req *pb.GetDatasetPeriodCompletedMarkerReq) (*pb.GetDatasetPeriodCompletedMarkerRsp, error) {
	n.read = req
	return &pb.GetDatasetPeriodCompletedMarkerRsp{RetInfo: successRetInfo(), Found: true, EventId: "period-marker", Marker: n.appended.GetMarker()}, nil
}

// Primary 只转发周期完成的报告，明细字节原样转发与回读，不解析。
func TestPrimaryDatasetPeriodCompletedForwardsOpaqueDetailUnchanged(t *testing.T) {
	ctx := context.Background()
	node := &periodMarkerRecordingNode{recordingMarkerNode: &recordingMarkerNode{recordingNode: &recordingNode{}}}
	service, err := New(Options{Node: node, Snapshot: func() metadata.RequestSnapshot { return factorResultSnapshot{} }})
	require.NoError(t, err)
	auth := &pb.AuthInfo{AppId: "factor"}
	marker := &pb.DatasetPeriodCompletedMarker{
		DatasetId: "result", Frequency: "1m", PeriodTime: 1786032000, CauseEventId: "source-ready",
		Status: "degraded", UniverseSubjectIds: []string{"ETH", "BTC"}, FailedSubjects: []string{"BTC"},
		DetailType: "test.Detail.v1", Detail: []byte{0x01, 0x02, 0xff},
	}
	rsp, err := service.ReportDatasetPeriodCompleted(ctx, &pb.ReportDatasetPeriodCompletedReq{AuthInfo: auth, SpaceId: "quant", Marker: marker})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "period-marker", rsp.GetEventId())
	require.Equal(t, "quant", node.appended.GetSpaceId())
	require.Equal(t, "node-owner", node.appended.GetNodeId())
	require.True(t, proto.Equal(marker, node.appended.GetMarker()), "Primary changed the marker before forwarding")
	got, err := service.GetDatasetPeriodCompleted(ctx, &pb.GetDatasetPeriodCompletedReq{AuthInfo: auth, SpaceId: "quant", DatasetId: "result", CauseEventId: "source-ready", PeriodTime: marker.GetPeriodTime()})
	require.NoError(t, err)
	require.True(t, got.GetFound())
	require.Equal(t, rsp.GetEventId(), got.GetEventId())
	require.True(t, proto.Equal(marker, got.GetMarker()), "Primary changed the marker on readback")
	require.Equal(t, "quant", node.read.GetSpaceId())
	require.Equal(t, "result", node.read.GetDatasetId())
	require.Equal(t, "source-ready", node.read.GetCauseEventId())
	require.Equal(t, marker.GetPeriodTime(), node.read.GetPeriodTime())
	require.Equal(t, "node-owner", node.read.GetNodeId())
}
