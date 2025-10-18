package provider

import (
	"fmt"
)

// Factory 云平台工厂函数类型
type Factory func(config *Config) (Client, error)

// COSFactory 支持COS的云平台工厂函数类型
type COSFactory func(config *Config) (ClientWithCOS, error)

// providerFactories 注册的云平台工厂函数
var providerFactories = make(map[CloudPlatform]Factory)

// cosProviderFactories 注册的支持COS的云平台工厂函数
var cosProviderFactories = make(map[CloudPlatform]COSFactory)

// RegisterProvider 注册云平台
func RegisterProvider(providerType CloudPlatform, factory Factory) {
	providerFactories[providerType] = factory
}

// RegisterCOSProvider 注册支持COS的云平台
func RegisterCOSProvider(providerType CloudPlatform, factory COSFactory) {
	cosProviderFactories[providerType] = factory
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

// NewWithCOS 创建支持COS的云平台实例
func NewWithCOS(config *Config) (ClientWithCOS, error) {
	if config == nil {
		return nil, fmt.Errorf("cloud config is nil")
	}

	factory, exists := cosProviderFactories[config.Provider]
	if !exists {
		return nil, fmt.Errorf("unsupported cloud platform with COS: %s", config.Provider)
	}

	return factory(config)
}

// ParseCloudPlatform 将字符串转换为CloudPlatform类型
func ParseCloudPlatform(providerStr string) (CloudPlatform, error) {
	switch providerStr {
	case "tencent":
		return Tencent, nil
	case "aliyun":
		return Aliyun, nil
	case "aws":
		return AWS, nil
	default:
		return "", fmt.Errorf("unsupported provider: %s", providerStr)
	}
}