package dnsproxy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetGetConfig_RoundTrip_ShouldWork(t *testing.T) {
	cfg := &Config{
		DNSProxy: DNSProxyConfig{
			Domains: []string{"example.com"},
			Cache:   CacheConfig{TTLSeconds: 60},
		},
	}
	SetConfig(cfg)
	got := GetConfig()
	require.NotNil(t, got)
	assert.Equal(t, []string{"example.com"}, got.DNSProxy.Domains)
}

func TestValidateConfig_EmptyDomains_ShouldPass(t *testing.T) {
	require.NoError(t, validateConfig(&Config{}))
}

func TestValidateConfig_InvalidDNSServer_ShouldError(t *testing.T) {
	cfg := &Config{
		DNSProxy: DNSProxyConfig{
			Domains: []string{"a.com"},
			DNSServers: []DNSServerConfig{
				{Name: "bad", Address: "not-an-ip", Enabled: true},
			},
			Cache: CacheConfig{TTLSeconds: 60},
			Timeouts: TimeoutConfig{
				DNSQuerySeconds: 5,
				PingTestSeconds: 3,
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid DNS server address")
}

func TestValidateConfig_NoEnabledServer_ShouldError(t *testing.T) {
	cfg := &Config{
		DNSProxy: DNSProxyConfig{
			Domains: []string{"a.com"},
			DNSServers: []DNSServerConfig{
				{Name: "off", Address: "8.8.8.8", Enabled: false},
			},
			Cache: CacheConfig{TTLSeconds: 60},
			Timeouts: TimeoutConfig{
				DNSQuerySeconds: 5,
				PingTestSeconds: 3,
			},
		},
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one DNS server must be enabled")
}

func TestGetEnabledDNSServers_NilConfig_ShouldReturnDefaults(t *testing.T) {
	SetConfig(nil)
	servers := getEnabledDNSServers()
	assert.Contains(t, servers, "8.8.8.8")
}

func TestGetCacheTTL_NilConfig_ShouldReturnDefault(t *testing.T) {
	SetConfig(nil)
	assert.Equal(t, time.Hour, getCacheTTL())
}

func TestGetScheduledDomains_WithConfig_ShouldReturnConfigured(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{Domains: []string{"moox.test"}}})
	assert.Equal(t, []string{"moox.test"}, getScheduledDomains())
}

func TestGetPingPorts_WithConfig_ShouldReturnConfigured(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{Ping: PingConfig{PingPorts: []string{"443"}}}})
	assert.Equal(t, []string{"443"}, getPingPorts())
}

func TestGetConcurrentLimit_NilOrInvalid_ShouldReturnDefault(t *testing.T) {
	SetConfig(nil)
	assert.Equal(t, 5, getConcurrentLimit())

	SetConfig(&Config{DNSProxy: DNSProxyConfig{Timeouts: TimeoutConfig{ConcurrentLimit: 0}}})
	assert.Equal(t, 5, getConcurrentLimit())

	SetConfig(&Config{DNSProxy: DNSProxyConfig{Timeouts: TimeoutConfig{ConcurrentLimit: -1}}})
	assert.Equal(t, 5, getConcurrentLimit())
}

func TestGetConcurrentLimit_WithConfig_ShouldReturnConfigured(t *testing.T) {
	SetConfig(&Config{DNSProxy: DNSProxyConfig{Timeouts: TimeoutConfig{ConcurrentLimit: 12}}})
	assert.Equal(t, 12, getConcurrentLimit())
}

func TestGetScheduledDomains_NilConfig_ShouldReturnDefaults(t *testing.T) {
	SetConfig(nil)
	assert.Equal(t, []string{"www.google.com", "www.baidu.com"}, getScheduledDomains())
}

func TestGetPingPorts_NilConfig_ShouldReturnDefault(t *testing.T) {
	SetConfig(nil)
	assert.Equal(t, []string{"80"}, getPingPorts())
}
