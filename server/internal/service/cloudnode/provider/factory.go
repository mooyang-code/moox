package provider

import (
	"fmt"
)

// ProviderFactory 云厂商工厂函数类型
type ProviderFactory func(config *CloudConfig) (CloudProvider, error)

// providerFactories 注册的云厂商工厂函数
var providerFactories = make(map[ProviderType]ProviderFactory)

// RegisterProvider 注册云厂商
func RegisterProvider(providerType ProviderType, factory ProviderFactory) {
	providerFactories[providerType] = factory
}

// NewCloudProvider 创建云厂商实例
func NewCloudProvider(config *CloudConfig) (CloudProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("cloud config is nil")
	}

	factory, exists := providerFactories[config.Provider]
	if !exists {
		return nil, fmt.Errorf("unsupported cloud provider: %s", config.Provider)
	}

	return factory(config)
}