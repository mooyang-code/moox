package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// Config 网关服务配置
type Config struct {
	Gateway   GatewayConfig   `yaml:"gateway"`    // 网关配置
	RateLimit RateLimitConfig `yaml:"rate_limit"` // 限流配置
}

// GatewayConfig 网关配置
type GatewayConfig struct {
	Port          int                      `yaml:"port"`            // 网关服务端口
	Timeout       int                      `yaml:"timeout"`         // 超时时间(毫秒)
	Debug         bool                     `yaml:"debug"`           // 是否开启调试模式
	NoAuthMethods []string                 `yaml:"no_auth_methods"` // 不需要鉴权的接口列表
	Services      map[string]ServiceDetail `yaml:"services"`        // 服务配置映射
}

// ServiceDetail 服务详细配置
type ServiceDetail struct {
	Address string `yaml:"address"` // 服务地址(可废弃。当前使用thttp，从trpc_go.yaml中读配置)
	Path    string `yaml:"path"`    // 服务路径
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

// ServiceConfig 服务配置
type ServiceConfig struct {
	BaseURL     string
	ServicePath string
	Headers     map[string]string
	Timeout     time.Duration
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

// GetStorageServiceConfig 获取存储服务配置
func (c *Config) GetStorageServiceConfig() (ServiceConfig, error) {
	return c.GetServiceConfigByID("storage")
}

// GetAuthServiceConfig 获取认证服务配置
func (c *Config) GetAuthServiceConfig() (ServiceConfig, error) {
	return c.GetServiceConfigByID("auth")
}

// GetMetadataServiceConfig 获取元数据服务配置
func (c *Config) GetMetadataServiceConfig() (ServiceConfig, error) {
	return c.GetServiceConfigByID("metadata")
}

// GetServiceConfigByID 根据服务ID获取服务配置
func (c *Config) GetServiceConfigByID(serviceID string) (ServiceConfig, error) {
	serviceDetail, exists := c.Gateway.Services[serviceID]
	if !exists {
		return ServiceConfig{}, fmt.Errorf("服务 '%s' 未在配置文件中找到", serviceID)
	}

	return ServiceConfig{
		BaseURL:     fmt.Sprintf("http://%s", serviceDetail.Address),
		ServicePath: serviceDetail.Path,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Timeout: time.Duration(c.Gateway.Timeout) * time.Millisecond,
	}, nil
}

// GetServiceConfig 获取指定服务的配置（通用方法，兼容旧接口）
func (c *Config) GetServiceConfig(serviceID string, serviceAddr string) ServiceConfig {
	// 优先使用配置文件中的服务配置
	if serviceDetail, exists := c.Gateway.Services[serviceID]; exists {
		return ServiceConfig{
			BaseURL:     fmt.Sprintf("http://%s", serviceDetail.Address),
			ServicePath: serviceDetail.Path,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Timeout: time.Duration(c.Gateway.Timeout) * time.Millisecond,
		}
	}

	// 如果配置文件中没有，使用传入的地址
	return ServiceConfig{
		BaseURL:     fmt.Sprintf("http://%s", serviceAddr),
		ServicePath: fmt.Sprintf("trpc.%s.%s", serviceID, serviceID),
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
		Timeout: time.Duration(c.Gateway.Timeout) * time.Millisecond,
	}
}

// GetAllServiceIDs 获取所有已配置的服务ID列表
func (c *Config) GetAllServiceIDs() []string {
	serviceIDs := make([]string, 0, len(c.Gateway.Services))
	for serviceID := range c.Gateway.Services {
		serviceIDs = append(serviceIDs, serviceID)
	}
	return serviceIDs
}

// HasService 检查是否配置了指定的服务
func (c *Config) HasService(serviceID string) bool {
	_, exists := c.Gateway.Services[serviceID]
	return exists
}
