package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rpcPingAdapter struct {
	exchange.ExchangeAdapter
	latency int64
	err     error
}

func (a rpcPingAdapter) Name() string { return "fake" }
func (a rpcPingAdapter) Ping(context.Context, exchange.Credential) (int64, error) {
	return a.latency, a.err
}

func TestServer_ListTrades_Empty_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, _ := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListTrades(ctx, &mooxpb.ListTradesReq{AccountId: accountID, Page: &mooxpb.Page{Page: 1, Size: 10}})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_UpdateChannel_ShouldPersist(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.UpdateChannel(ctx, &mooxpb.UpdateChannelReq{
		ChannelId: channelID, ChannelName: "updated",
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_DeleteChannel_ShouldSucceed(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.DeleteChannel(ctx, &mooxpb.DeleteChannelReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_SyncOrders_EmptyAccount_ShouldReject(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.SyncOrders(ctx, &mooxpb.SyncOrdersReq{AccountId: ""})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INVALID_PARAM, rsp.RetInfo.Code)
}

func TestServer_AdvanceRebalance_NoKernel_ShouldReturnInnerError(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.AdvanceRebalance(ctx, &mooxpb.AdvanceRebalanceReq{RunId: "run-1"})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func TestServer_CreateRebalance_InvalidQuantity_ShouldReject(t *testing.T) {
	ks, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ks.Close() })
	h := New(nil, &command.Engine{Store: ks})
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.CreateRebalance(ctx, &mooxpb.CreateRebalanceReq{
		RunId: "run-1", AccountId: "acct-1", ChannelId: "ch-1",
		Targets: []*mooxpb.TargetPosition{{Symbol: "BTCUSDT", Quantity: "bad"}},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
}

func TestServer_ListInstruments_WithoutAPIKey_ShouldFail(t *testing.T) {
	svc, _ := newRPCTestService(t)
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)
	rsp, err := h.ListInstruments(ctx, &mooxpb.ListInstrumentsReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.NotEqual(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_TestChannel_WithInjectedAdapter_ShouldReturnReachability(t *testing.T) {
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) {
		return rpcPingAdapter{latency: 42}, nil
	}))
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)

	rsp, err := h.TestChannel(ctx, &mooxpb.TestChannelReq{ChannelId: channelID})

	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.True(t, rsp.Reachable)
	assert.Equal(t, int32(42), rsp.LatencyMs)
}

func TestServer_TestChannel_AdapterError_ShouldReturnRetInfo(t *testing.T) {
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(string) (exchange.ExchangeAdapter, error) {
		return rpcPingAdapter{err: errors.New("dial failed")}, nil
	}))
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedAccountChannel(t, h, ctx)

	rsp, err := h.TestChannel(ctx, &mooxpb.TestChannelReq{ChannelId: channelID})

	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_INNER_ERR, rsp.RetInfo.Code)
	assert.False(t, rsp.Reachable)
}
