package primarystore

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
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
	service, err := New(Options{Node: node, Snapshot: func() metadata.RequestSnapshot { return factorResultSnapshot{} }})
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
	require.Equal(t, "node-owner", node.appended.GetNodeId())
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
	require.Equal(t, "node-owner", node.read.GetNodeId())
}

func TestPeriodMarkersRequireMatchingDatasetOwnership(t *testing.T) {
	tests := []struct {
		name   string
		owner  string
		attrs  map[string]string
		invoke func(*Service) *pb.RetInfo
	}{
		{
			name: "factor marker rejects raw dataset", owner: "factor",
			attrs: map[string]string{"owner_module": "collector", "dataset_role": "raw_collection"},
			invoke: func(service *Service) *pb.RetInfo {
				rsp, _ := service.ReportFactorPeriodComputed(context.Background(), &pb.ReportFactorPeriodComputedReq{
					AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", Marker: &pb.FactorPeriodComputedMarker{DatasetId: "dataset"},
				})
				return rsp.GetRetInfo()
			},
		},
		{
			name: "factor marker rejects wrong write owner", owner: "factor",
			attrs: map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "collector"},
			invoke: func(service *Service) *pb.RetInfo {
				rsp, _ := service.ReportFactorPeriodComputed(context.Background(), &pb.ReportFactorPeriodComputedReq{
					AuthInfo: &pb.AuthInfo{AppId: "factor"}, SpaceId: "space", Marker: &pb.FactorPeriodComputedMarker{DatasetId: "dataset"},
				})
				return rsp.GetRetInfo()
			},
		},
		{
			name: "collector marker rejects factor dataset", owner: "collector",
			attrs: map[string]string{"owner_module": "factor", "dataset_role": "factor_result", "write_owner": "factor"},
			invoke: func(service *Service) *pb.RetInfo {
				rsp, _ := service.ReportCollectorPeriodCompleted(context.Background(), &pb.ReportCollectorPeriodCompletedReq{
					AuthInfo: &pb.AuthInfo{AppId: "collector"}, SpaceId: "space", Marker: &pb.CollectorPeriodCompletedMarker{DatasetId: "dataset"},
				})
				return rsp.GetRetInfo()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := false
			service, err := New(Options{
				Resolver: func(context.Context, string, string) (DataNodeClient, error) {
					resolved = true
					return &recordingNode{}, nil
				},
				Snapshot: func() metadata.RequestSnapshot {
					return factorRowsSnapshot{dataset: &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataNodeId: "node-owner", Attributes: test.attrs}}
				},
			})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_NO_PERMISSION, test.invoke(service).GetCode())
			require.False(t, resolved, "%s marker reached DataNode resolver", test.owner)
		})
	}
}
