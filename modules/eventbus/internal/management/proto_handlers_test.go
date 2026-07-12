package management

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/eventbus/proto/eventbusgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
)

func noopTRPCFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

type EventBusMgrServiceStub struct{}
func (s *EventBusMgrServiceStub) GetOverview(context.Context, *pb.GetOverviewReq) (*pb.GetOverviewRsp, error) { return &pb.GetOverviewRsp{}, nil }
func (s *EventBusMgrServiceStub) ListTopics(context.Context, *pb.ListTopicsReq) (*pb.ListTopicsRsp, error) { return &pb.ListTopicsRsp{}, nil }
func (s *EventBusMgrServiceStub) ListStreams(context.Context, *pb.ListStreamsReq) (*pb.ListStreamsRsp, error) { return &pb.ListStreamsRsp{}, nil }
func (s *EventBusMgrServiceStub) ListConsumers(context.Context, *pb.ListConsumersReq) (*pb.ListConsumersRsp, error) { return &pb.ListConsumersRsp{}, nil }
func (s *EventBusMgrServiceStub) GetConsumer(context.Context, *pb.GetConsumerReq) (*pb.GetConsumerRsp, error) { return &pb.GetConsumerRsp{}, nil }

func TestEventBusMgrServiceHandlers_ShouldDispatch(t *testing.T) {
	stub := &EventBusMgrServiceStub{}
	ctx := context.Background()
	rsp, err := pb.EventBusMgrService_GetOverview_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetOverviewRsp{}, rsp)
	rsp, err = pb.EventBusMgrService_ListTopics_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListTopicsRsp{}, rsp)
	rsp, err = pb.EventBusMgrService_ListStreams_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListStreamsRsp{}, rsp)
	rsp, err = pb.EventBusMgrService_ListConsumers_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.ListConsumersRsp{}, rsp)
	rsp, err = pb.EventBusMgrService_GetConsumer_Handler(stub, ctx, noopTRPCFilter)
	require.NoError(t, err)
	assert.IsType(t, &pb.GetConsumerRsp{}, rsp)
}
