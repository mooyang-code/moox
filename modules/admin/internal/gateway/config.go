package gateway

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v2"
)

// 全局配置变量(网关层 由于有权限插件 限流插件，无法依赖注入，故需要有全局配置)
var (
	gatewayConfig *Config
	configMutex   sync.RWMutex
)

// CORSConfig 跨域配置
type CORSConfig struct {
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// Config 网关服务配置
type Config struct {
	JWT       JWTConfig       `yaml:"jwt"`        // JWT配置
	Gateway   GatewayConfig   `yaml:"gateway"`    // 网关配置
	RateLimit RateLimitConfig `yaml:"rate_limit"` // 限流配置
	CORS      CORSConfig      `yaml:"cors"`       // 跨域配置
}

// JWTConfig JWT配置
type JWTConfig struct {
	SecretKey string `yaml:"secret_key"` // JWT密钥
}

// GatewayConfig 网关配置
type GatewayConfig struct {
	Debug         bool                     `yaml:"debug"`           // 是否开启调试模式
	NoAuthMethods []string                 `yaml:"no_auth_methods"` // 不需要鉴权的接口列表
	ServiceAuth   ServiceAuthConfig        `yaml:"service_auth"`    // 后台服务请求签名鉴权配置
}

// ServiceAuthConfig 后台服务请求签名鉴权配置。
type ServiceAuthConfig struct {
	Enabled       bool   `yaml:"enabled"`
	Version       string `yaml:"version"`
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	MaxExpireSecs int64  `yaml:"max_expire_seconds"`
	ClockSkewSecs int64  `yaml:"clock_skew_seconds"`
}

// ServiceDetail 服务详细配置
type ServiceDetail struct {
	Address string
	Path    string
}

// RateLimitConfig 限流配置
type RateLimitConfig struct {
	// 全局默认限流配置
	DefaultQPS   int `yaml:"default_qps"`   // 默认QPS限制
	DefaultBurst int `yaml:"default_burst"` // 默认突发流量

	// 按接口配置限流
	MethodLimits map[string]MethodLimit `yaml:"method_limits"`
}

// MethodLimit 接口级别限流配置
type MethodLimit struct {
	QPS   int `yaml:"qps"`
	Burst int `yaml:"burst"`
}

// SetConfig 设置网关配置（依赖注入）
func SetConfig(cfg *Config) {
	configMutex.Lock()
	defer configMutex.Unlock()
	gatewayConfig = cfg
}

// GetConfig 获取网关配置
func GetConfig() *Config {
	configMutex.RLock()
	defer configMutex.RUnlock()
	return gatewayConfig
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 读取配置文件
	configPath := "./config/gateway.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %+v", err)
	}

	// 解析YAML到Config结构
	var config Config
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %+v", err)
	}
	return &config, nil
}
