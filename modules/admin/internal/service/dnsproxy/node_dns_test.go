package dnsproxy

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-database/localcache"
)

func TestGetNodeDNSRecords_CacheMiss_ShouldReturnEmpty(t *testing.T) {
	got, err := GetNodeDNSRecords(context.Background(), "missing-node")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGetAllNodesDNSForDomain_NoActiveNodes_ShouldReturnEmpty(t *testing.T) {
	prev := GetActiveNodeIDsFunc
	t.Cleanup(func() { GetActiveNodeIDsFunc = prev })
	GetActiveNodeIDsFunc = func(ctx context.Context) ([]string, error) {
		return nil, nil
	}
	got, err := GetAllNodesDNSForDomain(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGetAllNodesDNSForDomain_UninitializedProvider_ShouldReturnEmpty(t *testing.T) {
	prev := GetActiveNodeIDsFunc
	t.Cleanup(func() { GetActiveNodeIDsFunc = prev })
	GetActiveNodeIDsFunc = nil

	got, err := GetAllNodesDNSForDomain(context.Background(), "example.com")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestGetAllNodesDNSForDomain_WithNodeRecords_ShouldReturnIPs(t *testing.T) {
	ctx := context.Background()
	prev := GetActiveNodeIDsFunc
	t.Cleanup(func() { GetActiveNodeIDsFunc = prev })
	records, err := json.Marshal([]*NodeDNSRecord{{
		Domain: "svc.local", IPList: []string{"10.0.0.1"},
	}})
	require.NoError(t, err)
	require.True(t, localcache.Set(nodeDNSCacheKeyPrefix+"node-a", records, nodeDNSCacheTTL))
	waitLocalCache(t, nodeDNSCacheKeyPrefix+"node-a")
	GetActiveNodeIDsFunc = func(ctx context.Context) ([]string, error) {
		return []string{"node-a"}, nil
	}

	got, err := GetAllNodesDNSForDomain(ctx, "svc.local")
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.1"}, got["node-a"])
}

func TestGetMergedDNSResult_CacheMiss_ShouldError(t *testing.T) {
	_, err := GetMergedDNSResult(context.Background(), "missing.example")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cache miss")
}

func TestGetMergedDNSResult_CacheHit_ShouldReturnResult(t *testing.T) {
	domain := "merged.example"
	result := &MergedDNSResult{
		Domain:  domain,
		Success: true,
		IPList:  []*IPInfo{{IP: "9.9.9.9", Available: true}},
		ProbeAt: time.Now(),
	}
	cacheKey := mergedResultCacheKeyPrefix + domain
	require.True(t, localcache.Set(cacheKey, result, mergedResultCacheTTL))
	waitLocalCache(t, cacheKey)

	got, err := GetMergedDNSResult(context.Background(), domain)
	require.NoError(t, err)
	assert.True(t, got.Success)
	assert.Equal(t, "9.9.9.9", got.IPList[0].IP)
}

func TestMergeAndDNSProbeAllDomains_NoDomains_ShouldPass(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{Domains: nil}})
	require.NoError(t, MergeAndDNSProbeAllDomains(context.Background()))
}

func TestMergeAndDNSProbeDomain_NoIPs_ShouldSkipProbe(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{
		Domains:               []string{"empty.example"},
		EnableLocalDNSResolve: false,
	}})
	prev := GetActiveNodeIDsFunc
	t.Cleanup(func() { GetActiveNodeIDsFunc = prev })
	GetActiveNodeIDsFunc = func(ctx context.Context) ([]string, error) {
		return nil, nil
	}
	require.NoError(t, mergeAndDNSProbeDomain(context.Background(), "empty.example"))
}
