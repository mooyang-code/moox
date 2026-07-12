package dnsproxy

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-database/localcache"
)

func TestDNSProxy_ConvertMapToSlice_MultipleIPs_ShouldReturnAll(t *testing.T) {
	d := &DNSProxy{}
	ipSet := &sync.Map{}
	ipSet.Store("1.1.1.1", struct{}{})
	ipSet.Store("8.8.8.8", struct{}{})

	ips := d.convertMapToSlice(ipSet)
	assert.ElementsMatch(t, []string{"1.1.1.1", "8.8.8.8"}, ips)
}

func TestDNSProxy_CreateResolver_Localhost_ShouldUseSystemResolver(t *testing.T) {
	d := &DNSProxy{dnsTimeout: time.Second}
	resolver := d.createResolver("localhost")
	require.NotNil(t, resolver)
	assert.True(t, resolver.PreferGo)
}

func TestNewDNSProxy_DefaultConfig_ShouldInitialize(t *testing.T) {
	SetConfig(nil)
	d := NewDNSProxy()
	require.NotNil(t, d)
	assert.NotEmpty(t, d.dnsServers)
	assert.Equal(t, 80, d.pingPort)
}

func TestInitDNSProxyInstance_ShouldSetGlobal(t *testing.T) {
	InitDNSProxyInstance()
	require.NotNil(t, globalDNSInstance)
	require.NotNil(t, GlobalDNSInstance)
}

func TestGetLocalDNSResult_CacheMiss_ShouldReturnEmpty(t *testing.T) {
	assert.Empty(t, GetLocalDNSResult("missing.example"))
}

func waitLocalCache(t *testing.T, key string) interface{} {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if v, ok := localcache.Get(key); ok {
			return v
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("localcache key %q not ready", key)
	return nil
}

func TestGetLocalDNSResult_CacheHit_ShouldReturnIPs(t *testing.T) {
	domain := "cached.example"
	result := &DNSProxyResult{
		Domain:  domain,
		Success: true,
		IPList:  []*IPInfo{{IP: "1.2.3.4", Available: true}},
	}
	require.True(t, localcache.Set(domain, result, 60))

	cached := waitLocalCache(t, domain)
	got, ok := cached.(*DNSProxyResult)
	require.True(t, ok)
	ips := GetLocalDNSResult(domain)
	require.Len(t, ips, 1)
	assert.Equal(t, "1.2.3.4", ips[0].IP)
	_ = got
}

func TestHandleSchedule_DisabledLocalResolve_ShouldSkip(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{EnableLocalDNSResolve: false}})
	err := HandleSchedule(context.Background(), "")
	require.NoError(t, err)
}

func TestHandleSchedule_UninitializedInstance_ShouldError(t *testing.T) {
	globalDNSInstance = nil
	SetConfig(&Config{DNSProxy: DNSProxyConfig{EnableLocalDNSResolve: true}})
	err := HandleSchedule(context.Background(), "")
	require.Error(t, err)
}

func TestResolveDomainsBatch_EmptyDomains_ShouldPass(t *testing.T) {
	require.NoError(t, resolveDomainsBatch(context.Background(), nil, 10))
}

func TestDNSProxy_PingIP_Unreachable_ShouldMarkUnavailable(t *testing.T) {
	d := &DNSProxy{pingTimeout: 100 * time.Millisecond, pingPort: 1}
	latency, available := d.pingIP(context.Background(), "127.0.0.1")
	assert.False(t, available)
	assert.Equal(t, int64(0), latency)
}

func TestDNSProxy_PingAndSort_SortsAvailableFirst(t *testing.T) {
	d := &DNSProxy{pingTimeout: 50 * time.Millisecond, pingPort: 1}
	ips := d.PingAndSort(context.Background(), []string{"127.0.0.1", "127.0.0.2"})
	require.Len(t, ips, 2)
	for _, ip := range ips {
		assert.False(t, ip.Available)
	}
}
