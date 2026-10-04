package dnsresolver

import (
	"context"
	"errors"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

func TestTradeClientBatchesAndNormalizesResponse(t *testing.T) {
	original := tradepb.NewTradeDNSResolverServiceClientProxy
	t.Cleanup(func() { tradepb.NewTradeDNSResolverServiceClientProxy = original })
	var got *tradepb.ResolveDomainsReq
	tradepb.NewTradeDNSResolverServiceClientProxy = func(opts ...client.Option) tradepb.TradeDNSResolverServiceClientProxy {
		return fakeTradeDNSClient{call: func(_ context.Context, req *tradepb.ResolveDomainsReq) (*tradepb.ResolveDomainsRsp, error) {
			got = req
			return &tradepb.ResolveDomainsRsp{
				RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_SUCCESS},
				Resolutions: []*tradepb.DomainResolution{{
					Domain: "FAPI.BINANCE.COM.",
					Ips: []*tradepb.ResolvedIP{
						{Ip: "8.8.8.8", TcpConnectLatencyMs: 8},
						{Ip: "1.1.1.1", TcpConnectLatencyMs: 12},
						{Ip: "2001:db8::1", TcpConnectLatencyMs: 1},
					},
				}, {
					Domain: "evil.example.com",
					Ips:    []*tradepb.ResolvedIP{{Ip: "8.8.4.4", TcpConnectLatencyMs: 3}},
				}},
				UnresolvedDomains: []string{"api.binance.com"},
			}, nil
		}}
	}
	client := NewTradeClient("43.132.204.177:11003", "compute-1", gatewayauth.Credentials{KeyID: "collector", Secret: "secret"}, time.Second)
	result, err := client.ResolveDomains(context.Background(), []string{"fapi.binance.com", "api.binance.com"})
	require.NoError(t, err)
	require.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, got.GetDomains())
	require.Equal(t, uint32(4), got.GetMaxIpsPerDomain())
	require.Equal(t, []string{"8.8.8.8", "1.1.1.1"}, result["fapi.binance.com"].IPs)
	require.Equal(t, uint32(8), result["fapi.binance.com"].LatencyMS["8.8.8.8"])
	_, ok := result["api.binance.com"]
	require.False(t, ok, "unresolved domains must not create an empty route")
	_, ok = result["evil.example.com"]
	require.False(t, ok, "the resolver must ignore domains that were not requested")
}

func TestTradeClientReturnsRequestLevelErrors(t *testing.T) {
	original := tradepb.NewTradeDNSResolverServiceClientProxy
	t.Cleanup(func() { tradepb.NewTradeDNSResolverServiceClientProxy = original })
	tradepb.NewTradeDNSResolverServiceClientProxy = func(...client.Option) tradepb.TradeDNSResolverServiceClientProxy {
		return fakeTradeDNSClient{call: func(context.Context, *tradepb.ResolveDomainsReq) (*tradepb.ResolveDomainsRsp, error) {
			return nil, errors.New("timeout")
		}}
	}
	client := NewTradeClient("ip://43.132.204.177:11003", "compute-1", gatewayauth.Credentials{}, time.Second)
	_, err := client.ResolveDomains(context.Background(), []string{"fapi.binance.com"})
	require.Error(t, err)
}

type fakeTradeDNSClient struct {
	call func(context.Context, *tradepb.ResolveDomainsReq) (*tradepb.ResolveDomainsRsp, error)
}

func (f fakeTradeDNSClient) ResolveDomains(ctx context.Context, req *tradepb.ResolveDomainsReq, _ ...client.Option) (*tradepb.ResolveDomainsRsp, error) {
	return f.call(ctx, req)
}
