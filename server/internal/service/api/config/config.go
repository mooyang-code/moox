// Package config 提供DNS代理服务的配置管理功能
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// Config DNS代理服务配置
type Config struct {
	DNSProxy DNSProxyConfig `yaml:"dnsproxy"` // DNS代理配置
}

// DNSProxyConfig DNS代理配置
type DNSProxyConfig struct {
	Domains    []string          `yaml:"domains"`     // 需要定时解析的域名列表
	Cache      CacheConfig       `yaml:"cache"`       // 缓存配置
	DNSServers []DNSServerConfig `yaml:"dns_servers"` // DNS服务器配置
	Timeouts   TimeoutConfig     `yaml:"timeouts"`    // 超时配置
	Latency    LatencyConfig     `yaml:"latency"`     // 延迟检测配置
}

// CacheConfig 缓存配置
type CacheConfig struct {
	TTLMinutes      int `yaml:"ttl_minutes"`      // 缓存过期时间（分钟）
	CleanupInterval int `yaml:"cleanup_interval"` // 清理间隔（分钟）
}

// DNSServerConfig DNS服务器配置
type DNSServerConfig struct {
	Name    string `yaml:"name"`    // 服务器名称
	Address string `yaml:"address"` // 服务器地址
	Enabled bool   `yaml:"enabled"` // 是否启用
}

// TimeoutConfig 超时配置
type TimeoutConfig struct {
	DNSQuerySeconds    int `yaml:"dns_query_seconds"`    // DNS查询超时时间（秒）
	LatencyTestSeconds int `yaml:"latency_test_seconds"` // 延迟测试超时时间（秒）
	ConcurrentLimit    int `yaml:"concurrent_limit"`     // 并发连接限制
}

// LatencyConfig 延迟检测配置
type LatencyConfig struct {
	TestPort   string   `yaml:"test_port"`   // 测试端口
	TestPorts  []string `yaml:"test_ports"`  // 多个测试端口
	RetryCount int      `yaml:"retry_count"` // 重试次数
	RetryDelay int      `yaml:"retry_delay"` // 重试延迟（毫秒）
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 读取配置文件
	configPath := "../config/dnsproxy.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取DNS代理配置文件失败: %+v", err)
	}

	// 解析YAML到Config结构
	var config Config
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析DNS代理YAML配置失败: %+v", err)
	}

	// 验证配置
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("DNS代理配置验证失败: %+v", err)
	}
	return &config, nil
}

// validateConfig 验证配置的有效性
func validateConfig(config *Config) error {
	// 验证域名列表
	if len(config.DNSProxy.Domains) == 0 {
		return fmt.Errorf("域名列表不能为空")
	}

	// 验证DNS服务器
	enabledServers := 0
	for _, server := range config.DNSProxy.DNSServers {
		if server.Enabled {
			enabledServers++
			if server.Address == "" {
				return fmt.Errorf("DNS服务器地址不能为空: %s", server.Name)
			}
		}
	}
	if enabledServers == 0 {
		return fmt.Errorf("至少需要启用一个DNS服务器")
	}

	// 验证缓存配置
	if config.DNSProxy.Cache.TTLMinutes <= 0 {
		return fmt.Errorf("缓存TTL必须大于0")
	}

	// 验证超时配置
	if config.DNSProxy.Timeouts.DNSQuerySeconds <= 0 {
		return fmt.Errorf("DNS查询超时时间必须大于0")
	}
	if config.DNSProxy.Timeouts.LatencyTestSeconds <= 0 {
		return fmt.Errorf("延迟测试超时时间必须大于0")
	}
	return nil
}

// GetEnabledDNSServers 获取启用的DNS服务器地址列表
func (c *Config) GetEnabledDNSServers() []string {
	var servers []string
	for _, server := range c.DNSProxy.DNSServers {
		if server.Enabled {
			servers = append(servers, server.Address)
		}
	}
	return servers
}

// GetCacheTTL 获取缓存TTL时间
func (c *Config) GetCacheTTL() time.Duration {
	return time.Duration(c.DNSProxy.Cache.TTLMinutes) * time.Minute
}

// GetCleanupInterval 获取缓存清理间隔
func (c *Config) GetCleanupInterval() time.Duration {
	if c.DNSProxy.Cache.CleanupInterval > 0 {
		return time.Duration(c.DNSProxy.Cache.CleanupInterval) * time.Minute
	}
	// 默认为TTL的一半
	return c.GetCacheTTL() / 2
}

// GetDNSTimeout 获取DNS查询超时时间
func (c *Config) GetDNSTimeout() time.Duration {
	return time.Duration(c.DNSProxy.Timeouts.DNSQuerySeconds) * time.Second
}

// GetLatencyTimeout 获取延迟测试超时时间
func (c *Config) GetLatencyTimeout() time.Duration {
	return time.Duration(c.DNSProxy.Timeouts.LatencyTestSeconds) * time.Second
}

// GetTestPorts 获取延迟测试端口列表
func (c *Config) GetTestPorts() []string {
	if len(c.DNSProxy.Latency.TestPorts) > 0 {
		return c.DNSProxy.Latency.TestPorts
	}
	// 默认返回单个测试端口
	if c.DNSProxy.Latency.TestPort != "" {
		return []string{c.DNSProxy.Latency.TestPort}
	}
	// 默认端口
	return []string{"80"}
}

// GetRetryDelay 获取重试延迟时间
func (c *Config) GetRetryDelay() time.Duration {
	if c.DNSProxy.Latency.RetryDelay > 0 {
		return time.Duration(c.DNSProxy.Latency.RetryDelay) * time.Millisecond
	}
	return 100 * time.Millisecond // 默认100毫秒
}

// GetConcurrentLimit 获取并发连接限制
func (c *Config) GetConcurrentLimit() int {
	if c.DNSProxy.Timeouts.ConcurrentLimit > 0 {
		return c.DNSProxy.Timeouts.ConcurrentLimit
	}
	return 5 // 默认5个并发连接
}

// GetRetryCount 获取重试次数
func (c *Config) GetRetryCount() int {
	if c.DNSProxy.Latency.RetryCount > 0 {
		return c.DNSProxy.Latency.RetryCount
	}
	return 1 // 默认重试1次
}
