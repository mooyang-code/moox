package dnsproxy

import (
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v2"
)

// 全局配置变量
var (
	dnsConfig   *Config // 全局配置
	configMutex sync.RWMutex
)

// Config 服务配置
type Config struct {
	DNSProxy DNSProxyConfig `yaml:"dnsproxy"` // DNS代理配置
}

// DNSProxyConfig DNS代理配置
type DNSProxyConfig struct {
	Domains               []string          `yaml:"domains"`                  // 需要定时解析的域名列表
	EnableLocalDNSResolve bool              `yaml:"enable_local_dns_resolve"` // 是否启用本地DNS解析（默认false）
	Cache                 CacheConfig       `yaml:"cache"`                    // 缓存配置
	DNSServers            []DNSServerConfig `yaml:"dns_servers"`              // DNS服务器配置
	Timeouts              TimeoutConfig     `yaml:"timeouts"`                 // 超时配置
	Ping                  PingConfig        `yaml:"ping"`                     // Ping检测配置
}

// CacheConfig 缓存配置
type CacheConfig struct {
	TTLSeconds int `yaml:"ttl_seconds"` // 缓存过期时间（秒）
}

// DNSServerConfig DNS服务器配置
type DNSServerConfig struct {
	Name    string `yaml:"name"`    // 服务器名称
	Address string `yaml:"address"` // 服务器地址（支持localhost表示使用系统默认DNS）
	Enabled bool   `yaml:"enabled"` // 是否启用
}

// TimeoutConfig 超时配置
type TimeoutConfig struct {
	DNSQuerySeconds int `yaml:"dns_query_seconds"` // DNS查询超时时间（秒）
	PingTestSeconds int `yaml:"ping_test_seconds"` // Ping测试超时时间（秒）
	ConcurrentLimit int `yaml:"concurrent_limit"`  // 并发连接限制
}

// PingConfig Ping检测配置
type PingConfig struct {
	PingPort    string   `yaml:"ping_port"`    // Ping端口
	PingPorts   []string `yaml:"ping_ports"`   // 多个Ping端口
	PingRetries int      `yaml:"ping_retries"` // Ping重试次数
	PingDelay   int      `yaml:"ping_delay"`   // Ping重试延迟（毫秒）
}

// SetConfig 设置DNSProxy配置（依赖注入）
func SetConfig(cfg *Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	dnsConfig = cfg
}

// GetConfig 获取DNSProxy配置
func GetConfig() *Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return dnsConfig
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	var config Config

	// 加载DNS代理配置
	dnsConfigPath := "./config/dnsproxy.yaml"
	if yamlFile, err := os.ReadFile(dnsConfigPath); err == nil {
		var dnsConfig struct {
			DNSProxy DNSProxyConfig `yaml:"dnsproxy"`
		}
		if err := yaml.Unmarshal(yamlFile, &dnsConfig); err == nil {
			config.DNSProxy = dnsConfig.DNSProxy
		}
	}

	// 验证配置
	if err := validateConfig(&config); err != nil {
		return nil, fmt.Errorf("config validation failed: %+v", err)
	}
	return &config, nil
}

// validateConfig 验证配置的有效性
func validateConfig(config *Config) error {
	// 验证DNS代理配置（如果有配置的话）
	if len(config.DNSProxy.Domains) > 0 {
		// 验证DNS服务器
		enabledServers := 0
		for _, server := range config.DNSProxy.DNSServers {
			if server.Enabled {
				enabledServers++
				if server.Address == "" {
					return fmt.Errorf("DNS server address cannot be empty: %s", server.Name)
				}
				// 验证DNS服务器地址格式（除了localhost）
				if server.Address != "localhost" && server.Address != "127.0.0.1" {
					// 简单的IP格式验证（非localhost情况下）
					if net.ParseIP(server.Address) == nil {
						return fmt.Errorf("invalid DNS server address format: %s (server: %s)",
							server.Address, server.Name)
					}
				}
			}
		}
		if enabledServers == 0 {
			return fmt.Errorf("at least one DNS server must be enabled")
		}

		// 验证缓存配置
		if config.DNSProxy.Cache.TTLSeconds <= 0 {
			return fmt.Errorf("cache TTL must be greater than 0")
		}

		// 验证超时配置
		if config.DNSProxy.Timeouts.DNSQuerySeconds <= 0 {
			return fmt.Errorf("DNS query timeout must be greater than 0")
		}
		if config.DNSProxy.Timeouts.PingTestSeconds <= 0 {
			return fmt.Errorf("ping test timeout must be greater than 0")
		}
	}
	return nil
}

// 获取配置的方法
func getEnabledDNSServers() []string {
	cfg := GetConfig()
	if cfg == nil {
		return []string{"8.8.8.8", "1.1.1.1"} // 默认DNS服务器
	}

	var servers []string
	for _, server := range cfg.DNSProxy.DNSServers {
		if server.Enabled {
			servers = append(servers, server.Address)
		}
	}
	return servers
}

func getCacheTTL() time.Duration {
	cfg := GetConfig()
	if cfg == nil || cfg.DNSProxy.Cache.TTLSeconds <= 0 {
		return 3600 * time.Second // 默认1小时
	}
	return time.Duration(cfg.DNSProxy.Cache.TTLSeconds) * time.Second
}

func getDNSTimeout() time.Duration {
	cfg := GetConfig()
	if cfg == nil || cfg.DNSProxy.Timeouts.DNSQuerySeconds <= 0 {
		return 5 * time.Second // 默认5秒
	}
	return time.Duration(cfg.DNSProxy.Timeouts.DNSQuerySeconds) * time.Second
}

func getPingTimeout() time.Duration {
	cfg := GetConfig()
	if cfg == nil || cfg.DNSProxy.Timeouts.PingTestSeconds <= 0 {
		return 3 * time.Second // 默认3秒
	}
	return time.Duration(cfg.DNSProxy.Timeouts.PingTestSeconds) * time.Second
}

func getPingPorts() []string {
	cfg := GetConfig()
	if cfg == nil {
		return []string{"80"} // 默认端口
	}

	if len(cfg.DNSProxy.Ping.PingPorts) > 0 {
		return cfg.DNSProxy.Ping.PingPorts
	}

	// 默认返回单个Ping端口
	if cfg.DNSProxy.Ping.PingPort != "" {
		return []string{cfg.DNSProxy.Ping.PingPort}
	}

	// 默认端口
	return []string{"80"}
}

func getConcurrentLimit() int {
	cfg := GetConfig()
	if cfg == nil || cfg.DNSProxy.Timeouts.ConcurrentLimit <= 0 {
		return 5 // 默认5个并发连接
	}
	return cfg.DNSProxy.Timeouts.ConcurrentLimit
}

func getScheduledDomains() []string {
	cfg := GetConfig()
	if cfg == nil {
		return []string{"www.google.com", "www.baidu.com"} // 默认域名
	}
	return cfg.DNSProxy.Domains
}
