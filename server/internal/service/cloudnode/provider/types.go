package provider

// 将共享的类型定义移到这里，避免循环导入

// ConfigInterface 配置接口，供provider实现使用
type ConfigInterface interface {
	GetProvider() ProviderType
	GetSecretID() string
	GetSecretKey() string
	GetExtraConfig() map[string]interface{}
	GetString(key string) string
	GetInt(key string) int
	GetBool(key string) bool
}

// 确保CloudConfig实现ConfigInterface
var _ ConfigInterface = (*CloudConfig)(nil)

// GetProvider 获取云厂商类型
func (c *CloudConfig) GetProvider() ProviderType {
	return c.Provider
}

// GetSecretID 获取密钥ID
func (c *CloudConfig) GetSecretID() string {
	return c.SecretID
}

// GetSecretKey 获取密钥
func (c *CloudConfig) GetSecretKey() string {
	return c.SecretKey
}

// GetExtraConfig 获取额外配置
func (c *CloudConfig) GetExtraConfig() map[string]interface{} {
	return c.ExtraConfig
}