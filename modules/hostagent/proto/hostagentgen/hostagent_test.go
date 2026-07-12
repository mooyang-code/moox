package hostagentpb

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
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
	assert.NotEmpty(t, msg.String())
	msg.ProtoMessage()
}

func TestProtoMessages_ShouldSupportReflectionAndGetters(t *testing.T) {
	exerciseMessage(t, &GetStatusReq{})

	status := &GetStatusRsp{
		AgentId: "agent-1", Version: "v1", Hostname: "host", BootId: "boot",
		LastCollectAt: "2020-01-01T00:00:00Z", LastPublishAt: "2020-01-01T00:00:01Z",
		LastError: "err", Collected: 1, Published: 2, Dropped: 3, Skipped: 4,
		EventbusConnected: true,
		Latest:            &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 1}},
	}
	exerciseMessage(t, status)
	assert.Equal(t, "agent-1", status.GetAgentId())
	assert.Equal(t, "v1", status.GetVersion())
	assert.Equal(t, "host", status.GetHostname())
	assert.Equal(t, "boot", status.GetBootId())
	assert.Equal(t, "err", status.GetLastError())
	assert.Equal(t, uint64(1), status.GetCollected())
	assert.Equal(t, uint64(2), status.GetPublished())
	assert.Equal(t, uint64(3), status.GetDropped())
	assert.Equal(t, uint64(4), status.GetSkipped())
	assert.True(t, status.GetEventbusConnected())
	assert.NotNil(t, status.GetLatest())
	desc, idx := status.Descriptor()
	assert.NotEmpty(t, desc)
	assert.NotEmpty(t, idx)
	_ = status.ProtoReflect()

	exerciseMessage(t, &GetSnapshotReq{})
	snapshotRsp := &GetSnapshotRsp{
		Snapshot:    &hostmetricpb.HostSnapshot{Memory: &hostmetricpb.MemoryMetric{TotalBytes: 1}},
		CollectedAt: "2020-01-01T00:00:00Z",
	}
	exerciseMessage(t, snapshotRsp)
	assert.NotNil(t, snapshotRsp.GetSnapshot())
	assert.Equal(t, "2020-01-01T00:00:00Z", snapshotRsp.GetCollectedAt())

	exerciseMessage(t, &RunOnceReq{})
	runOnceRsp := &RunOnceRsp{
		MessageId: "msg-1", Published: true, PublishError: "none",
		Snapshot: &hostmetricpb.HostSnapshot{},
	}
	exerciseMessage(t, runOnceRsp)
	assert.Equal(t, "msg-1", runOnceRsp.GetMessageId())
	assert.True(t, runOnceRsp.GetPublished())
	assert.Equal(t, "none", runOnceRsp.GetPublishError())
	assert.NotNil(t, runOnceRsp.GetSnapshot())
}

func TestNilGetters_ShouldReturnZeroValues(t *testing.T) {
	var status *GetStatusRsp
	assert.Empty(t, status.GetAgentId())
	assert.False(t, status.GetEventbusConnected())
	var runOnce *RunOnceRsp
	assert.False(t, runOnce.GetPublished())
}

func noopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

type hostAgentHandlerStub struct {
	status *GetStatusRsp
	snap   *GetSnapshotRsp
	run    *RunOnceRsp
}

func (s *hostAgentHandlerStub) GetStatus(context.Context, *GetStatusReq) (*GetStatusRsp, error) {
	return s.status, nil
}
func (s *hostAgentHandlerStub) GetSnapshot(context.Context, *GetSnapshotReq) (*GetSnapshotRsp, error) {
	return s.snap, nil
}
func (s *hostAgentHandlerStub) RunOnce(context.Context, *RunOnceReq) (*RunOnceRsp, error) {
	return s.run, nil
}

func TestHostAgentMgrServiceHandlers_ShouldDispatchRPCs(t *testing.T) {
	stub := &hostAgentHandlerStub{
		status: &GetStatusRsp{AgentId: "agent-1"},
		snap:   &GetSnapshotRsp{CollectedAt: "now"},
		run:    &RunOnceRsp{Published: true},
	}
	ctx := context.Background()

	statusRsp, err := HostAgentMgrService_GetStatus_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "agent-1", statusRsp.(*GetStatusRsp).GetAgentId())

	snapRsp, err := HostAgentMgrService_GetSnapshot_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.Equal(t, "now", snapRsp.(*GetSnapshotRsp).GetCollectedAt())

	runRsp, err := HostAgentMgrService_RunOnce_Handler(stub, ctx, noopFilter)
	require.NoError(t, err)
	assert.True(t, runRsp.(*RunOnceRsp).GetPublished())
}

func TestUnimplementedHostAgentMgr_ShouldReturnErrors(t *testing.T) {
	svc := &UnimplementedHostAgentMgr{}
	ctx := context.Background()
	_, err := svc.GetStatus(ctx, &GetStatusReq{})
	assert.Error(t, err)
	_, err = svc.GetSnapshot(ctx, &GetSnapshotReq{})
	assert.Error(t, err)
	_, err = svc.RunOnce(ctx, &RunOnceReq{})
	assert.Error(t, err)
}

func TestRegisterHostAgentMgrService_ShouldRegisterWithoutPanic(t *testing.T) {
	stub := &hostAgentHandlerStub{status: &GetStatusRsp{}}
	s := &fakeService{}
	require.NotPanics(t, func() {
		RegisterHostAgentMgrService(s, stub)
	})
	assert.True(t, s.registered)
}

type fakeService struct {
	registered bool
}

func (f *fakeService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}

func (f *fakeService) Serve() error { return nil }

func (f *fakeService) Close(chan struct{}) error { return nil }
