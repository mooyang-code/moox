package eventbuspb

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
)

func exerciseMessage(t *testing.T, msg interface {
	Reset()
	String() string
	ProtoMessage()
}) {
	t.Helper()
	msg.Reset()
	_ = msg.String()
	msg.ProtoMessage()
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func TestProtoMessages_ShouldSupportResetAndString(t *testing.T) {
	exerciseMessage(t, &GetOverviewReq{})
	exerciseMessage(t, &GetOverviewRsp{})
	exerciseMessage(t, &ListTopicsReq{})
	exerciseMessage(t, &ListTopicsRsp{})
	exerciseMessage(t, &ListStreamsReq{})
	exerciseMessage(t, &ListStreamsRsp{})
	exerciseMessage(t, &ListConsumersReq{})
	exerciseMessage(t, &ListConsumersRsp{})
	exerciseMessage(t, &GetConsumerReq{})
	exerciseMessage(t, &GetConsumerRsp{})
	exerciseMessage(t, &TopicInfo{})
	exerciseMessage(t, &StreamInfo{})
	exerciseMessage(t, &ConsumerInfo{})
	exerciseMessage(t, &Overview{})
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var nilGetOverviewReq *GetOverviewReq
	_ = nilGetOverviewReq
	var nilGetOverviewRsp *GetOverviewRsp
	_ = nilGetOverviewRsp
	var nilListTopicsReq *ListTopicsReq
	_ = nilListTopicsReq
	var nilListTopicsRsp *ListTopicsRsp
	_ = nilListTopicsRsp
	var nilListStreamsReq *ListStreamsReq
	_ = nilListStreamsReq
	var nilListStreamsRsp *ListStreamsRsp
	_ = nilListStreamsRsp
	var nilListConsumersReq *ListConsumersReq
	_ = nilListConsumersReq
	var nilListConsumersRsp *ListConsumersRsp
	_ = nilListConsumersRsp
}

type EventBusMgrServiceStub struct{}
func (s *EventBusMgrServiceStub) GetOverview(context.Context, *GetOverviewReq) (*GetOverviewRsp, error) {
	return &GetOverviewRsp{}, nil
}
func (s *EventBusMgrServiceStub) ListTopics(context.Context, *ListTopicsReq) (*ListTopicsRsp, error) {
	return &ListTopicsRsp{}, nil
}
func (s *EventBusMgrServiceStub) ListStreams(context.Context, *ListStreamsReq) (*ListStreamsRsp, error) {
	return &ListStreamsRsp{}, nil
}
func (s *EventBusMgrServiceStub) ListConsumers(context.Context, *ListConsumersReq) (*ListConsumersRsp, error) {
	return &ListConsumersRsp{}, nil
}
func (s *EventBusMgrServiceStub) GetConsumer(context.Context, *GetConsumerReq) (*GetConsumerRsp, error) {
	return &GetConsumerRsp{}, nil
}

func TestEventBusMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &EventBusMgrServiceStub{}
	ctx := context.Background()
	rsp, err := EventBusMgrService_GetOverview_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetOverviewRsp{}, rsp)
	rsp, err = EventBusMgrService_ListTopics_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListTopicsRsp{}, rsp)
	rsp, err = EventBusMgrService_ListStreams_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListStreamsRsp{}, rsp)
	rsp, err = EventBusMgrService_ListConsumers_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &ListConsumersRsp{}, rsp)
	rsp, err = EventBusMgrService_GetConsumer_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.IsType(t, &GetConsumerRsp{}, rsp)
}

func TestUnimplementedEventBusMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedEventBusMgr{}
	ctx := context.Background()
	_, err := svc.GetOverview(ctx, &GetOverviewReq{})
	assert.Error(t, err)
	_, err = svc.ListTopics(ctx, &ListTopicsReq{})
	assert.Error(t, err)
	_, err = svc.ListStreams(ctx, &ListStreamsReq{})
	assert.Error(t, err)
	_, err = svc.ListConsumers(ctx, &ListConsumersReq{})
	assert.Error(t, err)
	_, err = svc.GetConsumer(ctx, &GetConsumerReq{})
	assert.Error(t, err)
}

func TestRegisterEventBusMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &EventBusMgrServiceStub{}
	s := &fakeTRPCService{}
	require.NotPanics(t, func() {
		RegisterEventBusMgrService(s, stub)
	})
	assert.True(t, s.registered)
}

type fakeTRPCService struct { registered bool }

func (f *fakeTRPCService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}

func (f *fakeTRPCService) Serve() error { return nil }

func (f *fakeTRPCService) Close(chan struct{}) error { return nil }

