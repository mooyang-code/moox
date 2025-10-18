package provider

import (
	"fmt"
)

// Factory 云平台工厂函数类型
type Factory func(config *Config) (Client, error)

// providerFactories 注册的云平台工厂函数
var providerFactories = make(map[Provider]Factory)

// RegisterProvider 注册云平台
func RegisterProvider(providerType Provider, factory Factory) {
	providerFactories[providerType] = factory
}

// New 创建云平台实例
func New(config *Config) (Client, error) {
	if config == nil {
		return nil, fmt.Errorf("cloud config is nil")
	}

	factory, exists := providerFactories[config.Provider]
	if !exists {
		return nil, fmt.Errorf("unsupported cloud platform: %s", config.Provider)
	}

	return factory(config)
}