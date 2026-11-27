// Package config 提供统一的配置管理，使用 yaml.v3 + KnownFields(true) 单次加载 app.yaml。
package config

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AppConfig 应用配置（单文件 app.yaml，合并原 app.yaml 与 console.yaml）。
type AppConfig struct {
	Database  DatabaseConfig  `yaml:"database"`
	Session   SessionConfig   `yaml:"session"`
	Security  SecurityConfig  `yaml:"security"`
	Console   ConsoleConfig   `yaml:"console"`
	RateLimit RateLimitConfig `yaml:"rate_limit"`
}

// DatabaseConfig 数据库配置。
type DatabaseConfig struct {
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// SessionConfig 浏览器会话配置。
type SessionConfig struct {
	TTL time.Duration `yaml:"ttl"`
}

// SecurityConfig 登录失败锁定配置：同一用户名加来源 IP 连续失败 max_login_attempt 次后锁定 lock_duration。
type SecurityConfig struct {
	MaxLoginAttempt int           `yaml:"max_login_attempt"`
	LockDuration    time.Duration `yaml:"lock_duration"`
}

// ConsoleConfig 控制台配置。
type ConsoleConfig struct {
	// AllowedOrigins 是本站之外还接受的 Origin（例如 http://localhost:9527），只给本机开发经开发服务器代理时用，生产留空。
	AllowedOrigins []string `yaml:"allowed_origins"`
}

// RateLimitConfig 限流配置：只对列出的接口限流（登录等免会话或敏感接口），没列出的不限。
type RateLimitConfig struct {
	MethodLimits map[string]MethodLimit `yaml:"method_limits"`
}

// MethodLimit 接口级别限流配置。
type MethodLimit struct {
	QPS   int `yaml:"qps"`
	Burst int `yaml:"burst"`
}

// DefaultConfig 返回默认配置。
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Database: DatabaseConfig{
			Path:            "./data/admin.db",
			MaxIdleConns:    4,
			MaxOpenConns:    8,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Session: SessionConfig{
			TTL: 24 * time.Hour,
		},
		Security: SecurityConfig{
			MaxLoginAttempt: 5,
			LockDuration:    15 * time.Minute,
		},
	}
}

// Load 从单个 app.yaml 文件严格加载配置（KnownFields(true)）。
func Load(configPath string) (*AppConfig, error) {
	cfg := DefaultConfig()

	if strings.TrimSpace(configPath) != "" {
		data, err := os.ReadFile(filepath.Clean(configPath))
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		if len(bytes.TrimSpace(data)) > 0 {
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			if err := decoder.Decode(cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file: %w", err)
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				return nil, fmt.Errorf("failed to parse config file: must contain a single YAML document")
			}
		}
	}

	cfg.applyEnv()

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// applyEnv 从环境变量覆盖配置。
func (c *AppConfig) applyEnv() {
	if v := os.Getenv("MOOX_ADMIN_DB_PATH"); v != "" {
		c.Database.Path = v
	}
}

// Validate 验证配置。
func (c *AppConfig) Validate() error {
	if c.Database.Path == "" {
		return fmt.Errorf("database path is required")
	}
	if c.Session.TTL <= 0 {
		return fmt.Errorf("session.ttl 必须大于 0")
	}
	if c.Security.MaxLoginAttempt <= 0 || c.Security.LockDuration <= 0 {
		return fmt.Errorf("security.max_login_attempt 与 security.lock_duration 必须大于 0")
	}
	for _, origin := range c.Console.AllowedOrigins {
		if !validOrigin(origin) {
			return fmt.Errorf("console.allowed_origins 里的 %q 不是合法的来源，要写成 scheme://主机[:端口]，不能用 *", origin)
		}
	}
	dir := filepath.Dir(c.Database.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}
	return nil
}

// validOrigin 判断是不是浏览器 Origin 头的形式：只有协议、主机和可选端口。
func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && !strings.Contains(u.Host, "*") &&
		u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}
