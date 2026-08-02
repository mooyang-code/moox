package runtime

import (
	"os"

	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go/log"
)

// AppConfig 启动器配置（基于 yaml）
type AppConfig struct {
	System   *SystemConfig   `json:"system" yaml:"system"`       // 系统配置
	EventBus *EventBusConfig `json:"event_bus" yaml:"event_bus"` // 事件总线配置
	Sources  *SourcesConfig  `json:"sources" yaml:"sources"`     // 数据源配置
}

// SystemConfig 系统配置
type SystemConfig struct {
	Name        string            `json:"name" yaml:"name"`
	Version     string            `json:"version" yaml:"version"`
	Environment string            `json:"environment" yaml:"environment"`
	Timezone    string            `json:"timezone" yaml:"timezone"`
	StorageRPC  StorageRPCConfig  `json:"storage_rpc" yaml:"storage_rpc"`
	ServiceAuth ServiceAuthConfig `json:"service_auth" yaml:"service_auth"` // 后台服务请求签名鉴权配置
}

type StorageRPCConfig struct {
	GatewayTarget string `json:"gateway_target" yaml:"gateway_target"`
	GatewayNodeID string `json:"gateway_node_id" yaml:"gateway_node_id"`
	KeyID         string `json:"key_id" yaml:"key_id"`
	HMACKeyFile   string `json:"hmac_key_file" yaml:"hmac_key_file"`
}

// ServiceAuthConfig 后台服务请求签名鉴权配置。
type ServiceAuthConfig struct {
	AccessKey   string `json:"access_key" yaml:"access_key"`
	SecretKey   string `json:"secret_key" yaml:"secret_key"`
	TargetNode  string `json:"target_node" yaml:"target_node"`
	CAFile      string `json:"ca_file" yaml:"ca_file"`
	CAPEMBase64 string `json:"ca_pem_base64" yaml:"ca_pem_base64"`
	ExpireSec   int64  `json:"expire_seconds" yaml:"expire_seconds"`
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
	Market []SourceConfig `json:"market" yaml:"market"`
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
			Name:        "moox-collector",
			Version:     "2.0.0",
			Environment: "development",
			Timezone:    "UTC",
			StorageRPC:  StorageRPCConfig{GatewayTarget: "ip://127.0.0.1:11003", KeyID: "collector"},
			ServiceAuth: ServiceAuthConfig{
				ExpireSec: 60,
			},
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
			},
		},
	}
}

// GetServiceAuthConfig 获取后台服务请求签名配置。
func GetServiceAuthConfig() ServiceAuthConfig {
	if LocalAppConfig == nil {
		InitLocalAppConfig()
	}

	localAppConfigMu.RLock()
	cfg := ServiceAuthConfig{}
	if LocalAppConfig != nil && LocalAppConfig.System != nil {
		cfg = LocalAppConfig.System.ServiceAuth
	}
	localAppConfigMu.RUnlock()

	if cfg.ExpireSec <= 0 {
		cfg.ExpireSec = 60
	}
	if value := os.Getenv("MOOX_GATEWAY_NODE_ID"); value != "" {
		cfg.TargetNode = value
	}
	if value := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); value != "" {
		cfg.AccessKey = value
	}
	if value := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); value != "" {
		cfg.SecretKey = value
	}
	if value := os.Getenv("MOOX_GATEWAY_CA_FILE"); value != "" {
		cfg.CAFile = value
	}
	if value := os.Getenv("MOOX_GATEWAY_CA_PEM_B64"); value != "" {
		cfg.CAPEMBase64 = value
	}
	// Service Gateway is exposed through Caddy and may use a different trust
	// root from the native Gateway peer bundle.
	if value := os.Getenv("MOOX_SERVICE_GATEWAY_CA_FILE"); value != "" {
		cfg.CAFile = value
		cfg.CAPEMBase64 = ""
	}
	if value := os.Getenv("MOOX_SERVICE_GATEWAY_CA_PEM_B64"); value != "" {
		cfg.CAFile = ""
		cfg.CAPEMBase64 = value
	}
	return cfg
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
