package dnsresolver

import (
	"context"
	"errors"
	"testing"
	"time"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeEgressResolver struct {
	call func(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error)
}

func (f fakeEgressResolver) ResolveDomains(ctx context.Context, req *egresspb.ResolveDomainsReq, _ ...client.Option) (*egresspb.ResolveDomainsRsp, error) {
	return f.call(ctx, req)
}

func TestEgressClientBatchesAndNormalizesResponse(t *testing.T) {
	var got *egresspb.ResolveDomainsReq
	var deadline time.Time
	resolver := fakeEgressResolver{call: func(ctx context.Context, req *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
		got = req
		deadline, _ = ctx.Deadline()
		return &egresspb.ResolveDomainsRsp{
			RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS},
			Resolutions: []*egresspb.DomainResolution{{
				Domain: "FAPI.BINANCE.COM.",
				Ips: []*egresspb.ResolvedIP{
					{Ip: "8.8.8.8", TcpConnectLatencyMs: 8},
					{Ip: "1.1.1.1", TcpConnectLatencyMs: 12},
					{Ip: "10.0.0.1", TcpConnectLatencyMs: 1},
					{Ip: "2001:db8::1", TcpConnectLatencyMs: 1},
				},
			}, {
				Domain: "evil.example.com",
				Ips:    []*egresspb.ResolvedIP{{Ip: "8.8.4.4", TcpConnectLatencyMs: 3}},
			}},
			UnresolvedDomains: []string{"api.binance.com"},
		}, nil
	}}
	result, err := NewEgressClient(resolver, time.Second).ResolveDomains(context.Background(), []string{"fapi.binance.com", "API.binance.com", "api.binance.com."})
	require.NoError(t, err)
	require.Equal(t, []string{"fapi.binance.com", "api.binance.com"}, got.GetDomains(), "域名规范化并去重后一次请求")
	require.Equal(t, uint32(4), got.GetMaxIpsPerDomain())
	require.WithinDuration(t, time.Now().Add(time.Second), deadline, 200*time.Millisecond)
	require.Equal(t, []string{"8.8.8.8", "1.1.1.1"}, result["fapi.binance.com"].IPs, "只保留公网 IPv4")
	require.Equal(t, uint32(8), result["fapi.binance.com"].LatencyMS["8.8.8.8"])
	_, ok := result["api.binance.com"]
	require.False(t, ok, "没有解析结果的域名不能产生空路由")
	_, ok = result["evil.example.com"]
	require.False(t, ok, "忽略没有请求过的域名")
}

func TestEgressClientReturnsRequestLevelErrors(t *testing.T) {
	for name, call := range map[string]func(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error){
		"调用失败": func(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
			return nil, errors.New("timeout")
		},
		"空响应": func(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
			return &egresspb.ResolveDomainsRsp{}, nil
		},
		"出口代理报错": func(context.Context, *egresspb.ResolveDomainsReq) (*egresspb.ResolveDomainsRsp, error) {
			return &egresspb.ResolveDomainsRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "出口代理没有启用 DNS 解析"}}, nil
		},
	} {
		_, err := NewEgressClient(fakeEgressResolver{call: call}, time.Second).ResolveDomains(context.Background(), []string{"fapi.binance.com"})
		require.Error(t, err, name)
	}
	_, err := NewEgressClient(nil, time.Second).ResolveDomains(context.Background(), []string{"fapi.binance.com"})
	require.ErrorContains(t, err, "未初始化")
}
