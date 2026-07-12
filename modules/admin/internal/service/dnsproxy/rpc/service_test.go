package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-database/localcache"
)

func waitLocalCache(t *testing.T, key string) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := localcache.Get(key); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("localcache key %q not ready", key)
}

func TestService_ListDNSRecords_NilConfig_ShouldReturnEmpty(t *testing.T) {
	dnsproxy.SetConfig(nil)
	svc := NewService()

	rsp, err := svc.ListDNSRecords(context.Background(), &pb.ListDNSRecordsReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Empty(t, rsp.GetRecords())
}

func TestService_ListDNSRecords_EmptyDomains_ShouldReturnEmpty(t *testing.T) {
	dnsproxy.SetConfig(&dnsproxy.Config{DNSProxy: dnsproxy.DNSProxyConfig{Domains: nil}})
	svc := NewService()

	rsp, err := svc.ListDNSRecords(context.Background(), &pb.ListDNSRecordsReq{})
	require.NoError(t, err)
	assert.Empty(t, rsp.GetRecords())
	assert.Equal(t, uint32(0), rsp.GetPageResult().GetTotal())
}

func TestService_GetDNSRecord_EmptyDomain_ShouldReturnInvalidParam(t *testing.T) {
	svc := NewService()
	rsp, err := svc.GetDNSRecord(context.Background(), &pb.GetDNSRecordReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestService_GetDNSRecord_CacheMiss_ShouldReturnNotFound(t *testing.T) {
	svc := NewService()
	rsp, err := svc.GetDNSRecord(context.Background(), &pb.GetDNSRecordReq{Domain: "missing.example"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_NOT_FOUND, rsp.GetRetInfo().GetCode())
}

func TestService_GetDNSRecord_CacheHit_ShouldReturnRecord(t *testing.T) {
	domain := "hit.example"
	result := &dnsproxy.MergedDNSResult{
		Domain:  domain,
		Success: true,
		IPList:  []*dnsproxy.IPInfo{{IP: "1.1.1.1", Latency: 10, Available: true}},
		ProbeAt: time.Now(),
	}
	cacheKey := "dnsproxy:merged_result:" + domain
	require.True(t, localcache.Set(cacheKey, result, 300))
	waitLocalCache(t, cacheKey)

	svc := NewService()
	rsp, err := svc.GetDNSRecord(context.Background(), &pb.GetDNSRecordReq{Domain: domain})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, domain, rsp.GetRecord().GetDomain())
	assert.Equal(t, "1.1.1.1", rsp.GetRecord().GetBestIps())
}

func TestBuildBestIPs_AvailableSorted_ShouldJoinWithPlus(t *testing.T) {
	got := buildBestIPs([]*dnsproxy.IPInfo{
		{IP: "2.2.2.2", Latency: 20, Available: true},
		{IP: "1.1.1.1", Latency: 10, Available: true},
		{IP: "3.3.3.3", Available: false},
	})
	assert.Equal(t, "1.1.1.1+2.2.2.2", got)
}

func TestBuildBestIPs_NoAvailable_ShouldReturnEmpty(t *testing.T) {
	assert.Empty(t, buildBestIPs([]*dnsproxy.IPInfo{{IP: "1.1.1.1", Available: false}}))
}
