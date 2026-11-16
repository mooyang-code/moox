package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// Config 认证服务配置
type Config struct {
	Session  SessionConfig  `yaml:"session"`  // 浏览器会话
	Security SecurityConfig `yaml:"security"` // 登录失败锁定
}

// SessionConfig 浏览器会话配置
type SessionConfig struct {
	TTL time.Duration `yaml:"ttl"` // 会话有效期，从登录时算起；Cookie 的过期时间与它一致
}

// SecurityConfig 登录失败锁定配置：同一用户名加来源 IP 连续失败 max_login_attempt 次后锁定 lock_duration。
type SecurityConfig struct {
	MaxLoginAttempt int           `yaml:"max_login_attempt"`
	LockDuration    time.Duration `yaml:"lock_duration"`
}

// LoadConfig 加载配置文件（认证配置在 console.yaml 里）
func LoadConfig() (*Config, error) {
	configPath := "./config/console.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %+v", err)
	}

	config := Config{
		Session:  SessionConfig{TTL: 24 * time.Hour},
		Security: SecurityConfig{MaxLoginAttempt: 5, LockDuration: 15 * time.Minute},
	}
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析YAML失败: %+v", err)
	}
	if config.Session.TTL <= 0 {
		return nil, fmt.Errorf("session.ttl 必须大于 0")
	}
	if config.Security.MaxLoginAttempt <= 0 || config.Security.LockDuration <= 0 {
		return nil, fmt.Errorf("security.max_login_attempt 与 security.lock_duration 必须大于 0")
	}
	return &config, nil
}
