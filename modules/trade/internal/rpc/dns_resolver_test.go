package rpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/resolver"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/require"
)

func TestDNSResolverServerMapsResultsAndUnresolvedDomains(t *testing.T) {
	service := &DNSResolverServer{Resolver: resolver.New(resolver.Config{
		Domains: []string{"good.example.com", "bad.example.com"},
		LookupHost: func(_ context.Context, domain string) ([]string, error) {
			if domain == "bad.example.com" {
				return nil, errors.New("lookup failed")
			}
			return []string{"8.8.8.8"}, nil
		},
		DialContext: func(context.Context, string, string) (net.Conn, error) { return &rpcFakeConn{}, nil },
	})}
	rsp, err := service.ResolveDomains(context.Background(), &tradepb.ResolveDomainsReq{
		Domains:         []string{"good.example.com", "bad.example.com"},
		MaxIpsPerDomain: 1,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetResolutions(), 1)
	require.Equal(t, "good.example.com", rsp.GetResolutions()[0].GetDomain())
	require.Equal(t, "8.8.8.8", rsp.GetResolutions()[0].GetIps()[0].GetIp())
	require.GreaterOrEqual(t, rsp.GetResolutions()[0].GetIps()[0].GetTcpConnectLatencyMs(), uint32(1))
	require.Equal(t, []string{"bad.example.com"}, rsp.GetUnresolvedDomains())
}

func TestDNSResolverServerMapsInvalidAndDisabled(t *testing.T) {
	service := &DNSResolverServer{Resolver: resolver.New(resolver.Config{Domains: []string{"good.example.com"}})}
	rsp, err := service.ResolveDomains(context.Background(), &tradepb.ResolveDomainsReq{Domains: []string{"https://good.example.com"}})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), rsp.GetRetInfo().GetCode())

	disabled, err := (&DNSResolverServer{}).ResolveDomains(context.Background(), &tradepb.ResolveDomainsReq{Domains: []string{"good.example.com"}})
	require.NoError(t, err)
	require.NotEqual(t, int32(0), disabled.GetRetInfo().GetCode())

	nilRequest, err := service.ResolveDomains(context.Background(), nil)
	require.NoError(t, err)
	require.NotEqual(t, int32(0), nilRequest.GetRetInfo().GetCode())
}

func TestDNSResolverServerRejectsMaxIPLimitAboveProtocolCap(t *testing.T) {
	service := &DNSResolverServer{Resolver: resolver.New(resolver.Config{Domains: []string{"good.example.com"}})}
	rsp, err := service.ResolveDomains(context.Background(), &tradepb.ResolveDomainsReq{
		Domains:         []string{"good.example.com"},
		MaxIpsPerDomain: 5,
	})
	require.NoError(t, err)
	require.Equal(t, tradepb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

type rpcFakeConn struct{}

func (*rpcFakeConn) Read([]byte) (int, error)         { return 0, errors.New("not implemented") }
func (*rpcFakeConn) Write([]byte) (int, error)        { return 0, errors.New("not implemented") }
func (*rpcFakeConn) Close() error                     { return nil }
func (*rpcFakeConn) LocalAddr() net.Addr              { return rpcFakeAddr("local") }
func (*rpcFakeConn) RemoteAddr() net.Addr             { return rpcFakeAddr("remote") }
func (*rpcFakeConn) SetDeadline(time.Time) error      { return nil }
func (*rpcFakeConn) SetReadDeadline(time.Time) error  { return nil }
func (*rpcFakeConn) SetWriteDeadline(time.Time) error { return nil }

type rpcFakeAddr string

func (a rpcFakeAddr) Network() string { return "tcp" }
func (a rpcFakeAddr) String() string  { return string(a) }
