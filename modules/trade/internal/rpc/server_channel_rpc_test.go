package rpc

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pingStubAdapter struct {
	rpcStubAdapter
	latency int64
	pingErr error
}

func (a *pingStubAdapter) Ping(context.Context, exchange.Credential) (int64, error) {
	if a.pingErr != nil {
		return 0, a.pingErr
	}
	return a.latency, nil
}

func newPingRPCService(t *testing.T, latency int64, pingErr error) (*Server, string) {
	t.Helper()
	_, daoStore := newRPCTestService(t)
	svc := service.New("trade", service.WithStore(daoStore), service.WithExchangeFactory(func(name string) (exchange.ExchangeAdapter, error) {
		return &pingStubAdapter{latency: latency, pingErr: pingErr}, nil
	}))
	h := New(svc)
	ctx := rpcCtx(t, "crypto", "user-1")
	_, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	return h, channelID
}

func TestServer_TestChannel_Reachable_ShouldReturnLatency(t *testing.T) {
	h, channelID := newPingRPCService(t, 42, nil)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.TestChannel(ctx, &mooxpb.TestChannelReq{ChannelId: channelID})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	assert.True(t, rsp.Reachable)
	assert.Equal(t, int32(42), rsp.LatencyMs)
}

func TestServer_TestChannel_MissingChannel_ShouldReturnError(t *testing.T) {
	h, _ := newPingRPCService(t, 0, nil)
	ctx := rpcCtx(t, "crypto", "user-1")
	rsp, err := h.TestChannel(ctx, &mooxpb.TestChannelReq{ChannelId: "missing"})
	require.NoError(t, err)
	assert.NotEqual(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
}

func TestServer_ListChannels_WithFilters_ShouldReturnChannel(t *testing.T) {
	svc, h := newRPCStubService(t)
	ctx := rpcCtx(t, "crypto", "user-1")
	accountID, channelID := seedLinkedAccountChannel(t, svc, h, ctx)
	rsp, err := h.ListChannels(ctx, &mooxpb.ListChannelsReq{
		AccountId: accountID, Exchange: "binance", Page: &mooxpb.Page{Page: 1, Size: 10},
	})
	require.NoError(t, err)
	assert.Equal(t, mooxpb.ErrorCode_SUCCESS, rsp.RetInfo.Code)
	require.NotEmpty(t, rsp.Channels)
	assert.Equal(t, channelID, rsp.Channels[0].ChannelId)
}
