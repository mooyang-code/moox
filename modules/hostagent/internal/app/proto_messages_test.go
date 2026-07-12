package app

import (
	"context"
	"testing"

	hostagentpb "github.com/mooyang-code/moox/modules/hostagent/proto/hostagentgen"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
)

func exerciseProtoMessage(t *testing.T, msg interface {
	Reset()
	String() string
	ProtoMessage()
}) {
	t.Helper()
	_ = msg.String()
	msg.ProtoMessage()
	msg.Reset()
}

func TestHostagentProtoMessages_ShouldExposeGetters(t *testing.T) {
	exerciseProtoMessage(t, &hostagentpb.GetStatusReq{})

	status := &hostagentpb.GetStatusRsp{
		AgentId: "agent-1", Version: "v1", Hostname: "host", BootId: "boot",
		LastCollectAt: "t1", LastPublishAt: "t2", LastError: "err",
		Collected: 1, Published: 2, Dropped: 3, Skipped: 4, EventbusConnected: true,
		Latest: &hostmetricpb.HostSnapshot{Cpu: &hostmetricpb.CpuMetric{LogicalCores: 4}},
	}
	assert.Equal(t, "agent-1", status.GetAgentId())
	assert.Equal(t, "v1", status.GetVersion())
	assert.Equal(t, "host", status.GetHostname())
	assert.Equal(t, "boot", status.GetBootId())
	assert.Equal(t, "t1", status.GetLastCollectAt())
	assert.Equal(t, "t2", status.GetLastPublishAt())
	assert.Equal(t, "err", status.GetLastError())
	assert.Equal(t, uint64(1), status.GetCollected())
	assert.Equal(t, uint64(2), status.GetPublished())
	assert.Equal(t, uint64(3), status.GetDropped())
	assert.Equal(t, uint64(4), status.GetSkipped())
	assert.True(t, status.GetEventbusConnected())
	assert.NotNil(t, status.GetLatest())
	_ = status.ProtoReflect()
	exerciseProtoMessage(t, status)

	exerciseProtoMessage(t, &hostagentpb.GetSnapshotReq{})
	snapRsp := &hostagentpb.GetSnapshotRsp{
		Snapshot:    &hostmetricpb.HostSnapshot{Memory: &hostmetricpb.MemoryMetric{TotalBytes: 9}},
		CollectedAt: "now",
	}
	assert.NotNil(t, snapRsp.GetSnapshot())
	assert.Equal(t, "now", snapRsp.GetCollectedAt())
	exerciseProtoMessage(t, snapRsp)

	exerciseProtoMessage(t, &hostagentpb.RunOnceReq{})
	runRsp := &hostagentpb.RunOnceRsp{
		MessageId: "mid", Published: true, PublishError: "none",
		Snapshot: &hostmetricpb.HostSnapshot{},
	}
	assert.Equal(t, "mid", runRsp.GetMessageId())
	assert.True(t, runRsp.GetPublished())
	assert.Equal(t, "none", runRsp.GetPublishError())
	assert.NotNil(t, runRsp.GetSnapshot())
	exerciseProtoMessage(t, runRsp)
}

func TestHostagentProtoHandlers_ShouldDispatch(t *testing.T) {
	stub := &hostAgentProtoStub{
		status: &hostagentpb.GetStatusRsp{AgentId: "a1"},
		snap:   &hostagentpb.GetSnapshotRsp{CollectedAt: "now"},
		run:    &hostagentpb.RunOnceRsp{Published: true},
	}
	ctx := context.Background()
	chain := func(req interface{}) (filter.ServerChain, error) {
		return filter.ServerChain{filter.NoopServerFilter}, nil
	}

	statusRsp, err := hostagentpb.HostAgentMgrService_GetStatus_Handler(stub, ctx, chain)
	require.NoError(t, err)
	assert.Equal(t, "a1", statusRsp.(*hostagentpb.GetStatusRsp).GetAgentId())

	snapRsp, err := hostagentpb.HostAgentMgrService_GetSnapshot_Handler(stub, ctx, chain)
	require.NoError(t, err)
	assert.Equal(t, "now", snapRsp.(*hostagentpb.GetSnapshotRsp).GetCollectedAt())

	runRsp, err := hostagentpb.HostAgentMgrService_RunOnce_Handler(stub, ctx, chain)
	require.NoError(t, err)
	assert.True(t, runRsp.(*hostagentpb.RunOnceRsp).GetPublished())
}

func TestUnimplementedHostAgentMgrService_ShouldError(t *testing.T) {
	svc := &hostagentpb.UnimplementedHostAgentMgr{}
	ctx := context.Background()
	_, err := svc.GetStatus(ctx, &hostagentpb.GetStatusReq{})
	assert.Error(t, err)
	_, err = svc.GetSnapshot(ctx, &hostagentpb.GetSnapshotReq{})
	assert.Error(t, err)
	_, err = svc.RunOnce(ctx, &hostagentpb.RunOnceReq{})
	assert.Error(t, err)
}

type hostAgentProtoStub struct {
	status *hostagentpb.GetStatusRsp
	snap   *hostagentpb.GetSnapshotRsp
	run    *hostagentpb.RunOnceRsp
}

func (s *hostAgentProtoStub) GetStatus(context.Context, *hostagentpb.GetStatusReq) (*hostagentpb.GetStatusRsp, error) {
	return s.status, nil
}
func (s *hostAgentProtoStub) GetSnapshot(context.Context, *hostagentpb.GetSnapshotReq) (*hostagentpb.GetSnapshotRsp, error) {
	return s.snap, nil
}
func (s *hostAgentProtoStub) RunOnce(context.Context, *hostagentpb.RunOnceReq) (*hostagentpb.RunOnceRsp, error) {
	return s.run, nil
}

func TestRegisterHostAgentMgrService_WithFakeService_ShouldSucceed(t *testing.T) {
	stub := &hostAgentProtoStub{status: &hostagentpb.GetStatusRsp{}}
	svc := &fakeTRPCService{}
	require.NotPanics(t, func() {
		hostagentpb.RegisterHostAgentMgrService(svc, stub)
	})
	assert.True(t, svc.registered)
}

type fakeTRPCService struct {
	registered bool
}

func (f *fakeTRPCService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}
func (f *fakeTRPCService) Serve() error                { return nil }
func (f *fakeTRPCService) Close(chan struct{}) error { return nil }
