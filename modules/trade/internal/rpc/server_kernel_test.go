package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openKernelStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestServer_SetPause_InvalidTargetType_ShouldReject(t *testing.T) {
	h := New(nil, &command.Engine{Store: openKernelStore(t)})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.SetPause(ctx, &mooxpb.SetTradePauseReq{TargetType: "invalid", TargetId: "x"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_SetPause_ValidAccount_ShouldPersist(t *testing.T) {
	ks := openKernelStore(t)
	h := New(nil, &command.Engine{Store: ks})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.SetPause(ctx, &mooxpb.SetTradePauseReq{TargetType: "account", TargetId: "acc-1", Paused: true, Reason: "test"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	paused, err := ks.IsPaused(ctx, "space-1", "acc-1", "")
	require.NoError(t, err)
	assert.True(t, paused)
}

func TestServer_ReconcileNow_WithKernel_ShouldEnqueueOutbox(t *testing.T) {
	ks := openKernelStore(t)
	h := New(nil, &command.Engine{Store: ks})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.ReconcileNow(ctx, &mooxpb.ReconcileNowReq{AccountId: "acc-1", ChannelId: "ch-1"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.NotEmpty(t, rsp.MessageId)
}

func TestServer_InspectSaga_MissingSaga_ShouldReturnNotFound(t *testing.T) {
	h := New(nil, &command.Engine{Store: openKernelStore(t)})
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.InspectSaga(ctx, &mooxpb.InspectSagaReq{SagaId: "missing"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func TestServer_KernelNil_ShouldReturnInnerError(t *testing.T) {
	h := New(nil)
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := h.SetPause(ctx, &mooxpb.SetTradePauseReq{TargetType: "account", TargetId: "a"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}
