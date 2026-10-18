package dnsresolver

import (
	"context"
	"errors"
	"testing"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestEgressClientBatchesAndNormalizesResponse(t *testing.T) {
	var got *egresspb.ResolveDomainsReq
	gateway := fakeEgressDNSClient{call: func(_ context.Context, req *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
		got = req
		return &egresspb.ResolveDomainsRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Resolutions: []*egresspb.DomainResolution{{
				Domain: "FAPI.BINANCE.COM.",
				Ips: []*egresspb.ResolvedIP{
					{Ip: "8.8.8.8", TcpConnectLatencyMs: 8},
					{Ip: "1.1.1.1", TcpConnectLatencyMs: 12},
					{Ip: "2001:db8::1", TcpConnectLatencyMs: 1},
				},
			}, {
				Domain: "evil.example.com",
				Ips:    []*egresspb.ResolvedIP{{Ip: "8.8.4.4", TcpConnectLatencyMs: 3}},
			}},
			UnresolvedDomains: []string{"api.binance.com"},
		}, nil
	}}
	client := NewEgressClient(gateway, time.Second)
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

func TestEgressClientReturnsRequestLevelErrors(t *testing.T) {
	client := NewEgressClient(fakeEgressDNSClient{call: func(ctx context.Context, _ *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), time.Second)
		return nil, errors.New("timeout")
	}}, time.Second)
	_, err := client.ResolveDomains(context.Background(), []string{"api.binance.com"})
	require.ErrorContains(t, err, "timeout")
}

type fakeEgressDNSClient struct {
	call func(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error)
}

func (f fakeEgressDNSClient) Invoke(ctx context.Context, service, method string, req, response any) error {
	if service != "trpc.moox.egress.Proxy" || method != "ResolveDomains" {
		return errors.New("unexpected service or method")
	}
	result, err := f.call(ctx, req.(*egresspb.ResolveDomainsReq))
	if err == nil {
		proto.Merge(response.(*egresspb.ResolveDomainsRsp), result)
	}
	return err
}
