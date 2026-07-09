// Package config loads moox-collector control-plane configuration.
package control

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root collector control-plane configuration.
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	CloudNode CloudNodeConfig `yaml:"cloudnode"`
	Storage   StorageConfig   `yaml:"storage"`
	SysDeploy SysDeployConfig `yaml:"sysdeploy"`
	Health    HealthConfig    `yaml:"health"`
}

// DatabaseConfig describes SQLite settings.
type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// CloudNodeConfig describes cloudnode RPC routing.
type CloudNodeConfig struct {
	Address     string `yaml:"address"`
	ServicePath string `yaml:"service_path"`
}

// StorageConfig describes storage service addresses.
type StorageConfig struct {
	MetadataTarget string `yaml:"metadata_target"`
	AccessTarget   string `yaml:"access_target"`
	MetadataURL    string `yaml:"metadata_url"` // Deprecated: use metadata_target.
	AccessURL      string `yaml:"access_url"`   // Deprecated: use access_target.
}

// SysDeployConfig describes optional dependency discovery through admin SysDeploy.
type SysDeployConfig struct {
	AdminGatewayURL string            `yaml:"admin_gateway_url"`
	ServiceAuth     ServiceAuthConfig `yaml:"service_auth"`
}

// ServiceAuthConfig describes backend HMAC auth for /api/service calls.
type ServiceAuthConfig struct {
	Version       string `yaml:"version"`
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	ExpireSeconds int64  `yaml:"expire_seconds"`
}

// HealthConfig controls the lightweight HTTP health endpoint.
type HealthConfig struct {
	Addr string `yaml:"addr"`
}

var globalConfig *Config

// Load reads YAML config from path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyEnv()
	if err := cfg.validateStorageTargets(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_COLLECTOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_ADMIN_GATEWAY_URL"); v != "" {
		c.SysDeploy.AdminGatewayURL = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_METADATA_TARGET"); v != "" {
		c.Storage.MetadataTarget = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_ACCESS_TARGET"); v != "" {
		c.Storage.AccessTarget = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_VERSION"); v != "" {
		c.SysDeploy.ServiceAuth.Version = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.AccessKey = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_SERVICE_AUTH_EXPIRE_SECONDS"); v != "" {
		if seconds, err := strconv.ParseInt(v, 10, 64); err == nil {
			c.SysDeploy.ServiceAuth.ExpireSeconds = seconds
		}
	}
}

func (c *Config) validateStorageTargets() error {
	if !isStorageTRPCTarget(c.Storage.MetadataTarget) {
		return fmt.Errorf("storage.metadata_target must be a tRPC target, got %q", c.Storage.MetadataTarget)
	}
	if !isStorageTRPCTarget(c.Storage.AccessTarget) {
		return fmt.Errorf("storage.access_target must be a tRPC target, got %q", c.Storage.AccessTarget)
	}
	return nil
}

func isStorageTRPCTarget(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return raw != "" && !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://")
}

// Default returns safe local defaults.
func Default() *Config {
	return &Config{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_collector.db",
			MaxIdleConns:    10,
			MaxOpenConns:    50,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		CloudNode: CloudNodeConfig{
			Address:     "127.0.0.1:11401",
			ServicePath: "trpc.moox.cloudnode.CloudNodeMgr",
		},
		Storage: StorageConfig{
			MetadataTarget: "127.0.0.1:20100",
			AccessTarget:   "127.0.0.1:20102",
		},
		SysDeploy: SysDeployConfig{
			ServiceAuth: ServiceAuthConfig{
				Version:       "moox-auth-v1",
				ExpireSeconds: 1800,
			},
		},
		Health: HealthConfig{
			Addr: ":11412",
		},
	}
}

// SetGlobalConfig stores the process-wide runtimeapp.
func SetGlobalConfig(cfg *Config) {
	globalConfig = cfg
}

// Global returns the process-wide runtimeapp.
func Global() *Config {
	if globalConfig == nil {
		globalConfig = Default()
	}
	return globalConfig
}
