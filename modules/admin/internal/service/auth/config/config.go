package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// Config 认证服务配置
type Config struct {
	Cache    CacheConfig    `yaml:"cache"`    // 缓存配置
	JWT      JWTConfig      `yaml:"jwt"`      // JWT配置
	Security SecurityConfig `yaml:"security"` // 安全配置
}

// CacheConfig 缓存配置
type CacheConfig struct {
	DataDir string `yaml:"data_dir"` // BadgerDB数据目录
}

// JWTConfig JWT配置
type JWTConfig struct {
	SecretKey     string        `yaml:"secret_key"`
	AccessExpired time.Duration `yaml:"access_expired"`
}

// SecurityConfig 安全配置
type SecurityConfig struct {
	SaltExpired     time.Duration `yaml:"salt_expired"`     // 盐值过期时间
	MaxLoginAttempt int           `yaml:"max_login_attempt"` // 最大登录尝试次数
	LockDuration    time.Duration `yaml:"lock_duration"`     // 账户锁定时间
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 读取配置文件（认证服务配置已合并到gateway.yaml）
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
	if secret := os.Getenv("MOOX_ADMIN_JWT_SECRET_KEY"); secret != "" {
		config.JWT.SecretKey = secret
	}
	return &config, nil
}
