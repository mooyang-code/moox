package primarystore

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type factorMarkerRecordingNode struct {
	*recordingMarkerNode
	appended *pb.AppendFactorPeriodComputedReq
	read     *pb.GetFactorPeriodComputedMarkerReq
}

func (n *factorMarkerRecordingNode) AppendFactorPeriodComputed(_ context.Context, req *pb.AppendFactorPeriodComputedReq) (*pb.AppendFactorPeriodComputedRsp, error) {
	n.appended = req
	return &pb.AppendFactorPeriodComputedRsp{RetInfo: successRetInfo(), EventId: "factor-marker"}, nil
}

func (n *factorMarkerRecordingNode) GetFactorPeriodComputedMarker(_ context.Context, req *pb.GetFactorPeriodComputedMarkerReq) (*pb.GetFactorPeriodComputedMarkerRsp, error) {
	n.read = req
	return &pb.GetFactorPeriodComputedMarkerRsp{RetInfo: successRetInfo(), Found: true, EventId: "factor-marker", Marker: n.appended.GetMarker()}, nil
}

func TestPrimaryFactorPeriodComputedForwardsFactorStatesUnchanged(t *testing.T) {
	ctx := context.Background()
	node := &factorMarkerRecordingNode{recordingMarkerNode: &recordingMarkerNode{recordingNode: &recordingNode{}}}
	service, err := New(Options{Node: node})
	require.NoError(t, err)
	auth := &pb.AuthInfo{AppId: "factor"}
	marker := &pb.FactorPeriodComputedMarker{
		DatasetId: "result", SourceDatasetId: "prices", Frequency: "1m", PeriodTime: 1786032000, TriggerEventId: "source-ready",
		Status: "degraded", UniverseSubjectIds: []string{"ETH", "BTC"}, FailedSubjects: []string{"BTC"},
		Factors: []*pb.FactorPeriodState{
			{FactorId: "z-factor", Status: "degraded", FailedSubjects: []string{"ETH", "BTC"}, SourceHash: "hash-z"},
			{FactorId: "a-factor", Status: "complete", SourceHash: "hash-a"},
		},
	}
	rsp, err := service.ReportFactorPeriodComputed(ctx, &pb.ReportFactorPeriodComputedReq{AuthInfo: auth, SpaceId: "quant", Marker: marker})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "factor-marker", rsp.GetEventId())
	require.Equal(t, "quant", node.appended.GetSpaceId())
	require.True(t, proto.Equal(marker, node.appended.GetMarker()), "Primary changed ordered factors before forwarding")
	got, err := service.GetFactorPeriodComputed(ctx, &pb.GetFactorPeriodComputedReq{AuthInfo: auth, SpaceId: "quant", DatasetId: "result", TriggerEventId: "source-ready", PeriodTime: marker.GetPeriodTime()})
	require.NoError(t, err)
	require.True(t, got.GetFound())
	require.Equal(t, rsp.GetEventId(), got.GetEventId())
	require.True(t, proto.Equal(marker, got.GetMarker()), "Primary changed factor states on marker readback")
	require.Equal(t, "quant", node.read.GetSpaceId())
	require.Equal(t, "result", node.read.GetDatasetId())
	require.Equal(t, "source-ready", node.read.GetTriggerEventId())
	require.Equal(t, marker.GetPeriodTime(), node.read.GetPeriodTime())
}
