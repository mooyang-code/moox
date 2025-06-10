package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v2"
)

// Config 认证服务配置
type Config struct {
	Database DatabaseConfig `yaml:"database"` // 数据库配置
	Cache    CacheConfig    `yaml:"cache"`    // 缓存配置
	JWT      JWTConfig      `yaml:"jwt"`      // JWT配置
	Security SecurityConfig `yaml:"security"` // 安全配置
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
}

// CacheConfig 缓存配置
type CacheConfig struct {
	DataDir string `yaml:"data_dir"` // BadgerDB数据目录
	DB      int    `yaml:"db"`       // 数据库编号（预留）
}

// JWTConfig JWT配置
type JWTConfig struct {
	SecretKey      string        `yaml:"secret_key"`
	AccessExpired  time.Duration `yaml:"access_expired"`
	RefreshExpired time.Duration `yaml:"refresh_expired"`
}

// SecurityConfig 安全配置
type SecurityConfig struct {
	SaltExpired     time.Duration `yaml:"salt_expired"`      // 盐值过期时间
	MaxLoginAttempt int           `yaml:"max_login_attempt"` // 最大登录尝试次数
	LockDuration    time.Duration `yaml:"lock_duration"`     // 账户锁定时间
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	// 读取配置文件
	configPath := "../config/auth.yaml"
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
