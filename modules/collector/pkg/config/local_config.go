package config

import (
	"os"

	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

// AppConfig 启动器配置（基于 config.yaml）
type AppConfig struct {
	System   *SystemConfig    `json:"system" yaml:"system"`       // 系统配置
	EventBus *EventBusConfig  `json:"event_bus" yaml:"event_bus"` // 事件总线配置
	Sources  *SourcesConfig   `json:"sources" yaml:"sources"`     // 数据源配置
	DNSProxy *DNSProxyConfig  `json:"dnsproxy" yaml:"dnsproxy"`   // DNS 代理配置
}

// SystemConfig 系统配置
type SystemConfig struct {
	Name        string `json:"name" yaml:"name"`
	Version     string `json:"version" yaml:"version"`
	Environment string `json:"environment" yaml:"environment"`
	Timezone    string `json:"timezone" yaml:"timezone"`
	MooxServerURL string `json:"moox_server_url" yaml:"moox_server_url"` // Moox Server API 地址
	StorageURL    string `json:"storage_url" yaml:"storage_url"`         // 存储服务地址 (如 http://127.0.0.1:19104)
}

// EventBusConfig 事件总线配置
type EventBusConfig struct {
	Type       string                 `json:"type" yaml:"type"`
	BufferSize int                    `json:"buffer_size" yaml:"buffer_size"`
	Workers    int                    `json:"workers" yaml:"workers"`
	Config     map[string]interface{} `json:"config" yaml:"config"`
}

// SourcesConfig 数据源配置
type SourcesConfig struct {
	Market     []SourceConfig `json:"market" yaml:"market"`
	Social     []SourceConfig `json:"social" yaml:"social"`
	News       []SourceConfig `json:"news" yaml:"news"`
	Blockchain []SourceConfig `json:"blockchain" yaml:"blockchain"`
}

// SourceConfig 数据源配置项
type SourceConfig struct {
	Name    string `json:"name" yaml:"name"`
	Enabled bool   `json:"enabled" yaml:"enabled"`
	Config  string `json:"config" yaml:"config"`
}

// DefaultConfig 默认配置
func DefaultConfig() *AppConfig {
	return &AppConfig{
		System: &SystemConfig{
			Name:        "multi-source-data-collector",
			Version:     "2.0.0",
			Environment: "development",
			Timezone:    "UTC",
		},
		EventBus: &EventBusConfig{
			Type:       "memory",
			BufferSize: 10000,
			Workers:    10,
			Config:     make(map[string]interface{}),
		},
		Sources: &SourcesConfig{
			Market: []SourceConfig{
				{Name: "binance", Enabled: true, Config: "./sources/market/binance.yaml"},
				{Name: "okx", Enabled: false, Config: "./sources/market/okx.yaml"},
			},
		},
	}
}

// LoadConfigs 加载系统中各个模块配置
func LoadConfigs(cfg *AppConfig) (*AppConfig, error) {
	log.Info("正在加载应用配置...")

	// 1. 尝试加载配置文件
	if err := loadConfigFile(cfg); err != nil {
		log.Warnf("加载配置文件失败，使用默认配置: %v", err)
	}

	log.Info("应用配置加载完成")
	return cfg, nil
}

// loadConfigFile 加载配置文件
func loadConfigFile(cfg *AppConfig) error {
	data, err := os.ReadFile("./config.yaml")
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, cfg)
}

// DNSProxyConfig DNS 代理配置
type DNSProxyConfig struct {
	ProbeConfigs    []ProbeConfig `json:"probe_configs" yaml:"probe_configs"`       // 探测配置列表
	DNSServers      []string      `json:"dns_servers" yaml:"dns_servers"`           // DNS 服务器列表，如 ["8.8.8.8", "1.1.1.1", "localhost"]
	DNSTimeout      int           `json:"dns_timeout" yaml:"dns_timeout"`           // DNS 解析超时时间（秒），默认 5
	ConcurrentLimit int           `json:"concurrent_limit" yaml:"concurrent_limit"` // 并发解析域名数，默认 10
	ScheduledDomains []string     `json:"scheduled_domains" yaml:"scheduled_domains"` // 需要定时解析的域名列表
}

// ProbeConfig 探测配置
type ProbeConfig struct {
	Domain    string          `json:"domain" yaml:"domain"`           // 域名
	ProbeType string          `json:"probe_type" yaml:"probe_type"`   // 探测类型: https | tcp
	ProbeAPI  *ProbeAPIConfig `json:"probe_api" yaml:"probe_api"`     // HTTPS 探测配置
	TCPPort   int             `json:"tcp_port" yaml:"tcp_port"`       // TCP 探测端口，默认 443
	Timeout   int             `json:"timeout" yaml:"timeout"`         // 超时时间（秒），默认 2
}

// ProbeAPIConfig HTTPS 探测 API 配置
type ProbeAPIConfig struct {
	Path           string `json:"path" yaml:"path"`                       // API 路径
	Method         string `json:"method" yaml:"method"`                   // HTTP 方法
	Timeout        int    `json:"timeout" yaml:"timeout"`                 // 超时时间（秒）
	ExpectedStatus int    `json:"expected_status" yaml:"expected_status"` // 期望的 HTTP 状态码
}
