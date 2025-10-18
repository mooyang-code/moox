package provider

import (
	"encoding/json"
	"fmt"
)

// Provider 云平台类型
type Provider string

const (
	Tencent Provider = "tencent" // 腾讯云
	Aliyun  Provider = "aliyun"  // 阿里云
	AWS     Provider = "aws"     // AWS
)

// Config 云平台配置
type Config struct {
	Provider    Provider               // 云平台类型
	SecretID    string                 // 密钥ID
	SecretKey   string                 // 密钥
	ExtraConfig map[string]interface{} // 额外配置（如region、endpoint等）
}

// NewConfig 创建云平台配置
func NewConfig(provider Provider, secretID, secretKey string, extraConfig string) (*Config, error) {
	config := &Config{
		Provider:  provider,
		SecretID:  secretID,
		SecretKey: secretKey,
	}

	// 解析额外配置
	if extraConfig != "" {
		var extra map[string]interface{}
		if err := json.Unmarshal([]byte(extraConfig), &extra); err != nil {
			return nil, fmt.Errorf("invalid extra config format: %w", err)
		}
		config.ExtraConfig = extra
	} else {
		config.ExtraConfig = make(map[string]interface{})
	}

	return config, nil
}

// GetString 从额外配置中获取字符串值
func (c *Config) GetString(key string) string {
	if val, ok := c.ExtraConfig[key]; ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return ""
}

// GetInt 从额外配置中获取整数值
func (c *Config) GetInt(key string) int {
	if val, ok := c.ExtraConfig[key]; ok {
		switch v := val.(type) {
		case int:
			return v
		case float64:
			return int(v)
		}
	}
	return 0
}

// GetBool 从额外配置中获取布尔值
func (c *Config) GetBool(key string) bool {
	if val, ok := c.ExtraConfig[key]; ok {
		if b, ok := val.(bool); ok {
			return b
		}
	}
	return false
}