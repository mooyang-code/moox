package config

import (
	"time"
)

// Config 管理员服务配置
type Config struct {
	// 数据库配置
	Database DatabaseConfig `yaml:"database"`

	// 缓存配置
	Cache CacheConfig `yaml:"cache"`

	// JWT配置
	JWT JWTConfig `yaml:"jwt"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// CacheConfig 缓存配置
type CacheConfig struct {
	DataDir  string `yaml:"data_dir"` // BadgerDB数据目录
	Password string `yaml:"password"` // 缓存密码（预留）
	DB       int    `yaml:"db"`       // 数据库编号（预留）
}

// JWTConfig JWT配置
type JWTConfig struct {
	SecretKey     string        `yaml:"secret_key"`
	AccessExpired time.Duration `yaml:"access_expired"`
}
