// Package bootstrap loads configuration and wires the moox-collector service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
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
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

// SysDeployConfig describes optional dependency discovery through admin SysDeploy.
type SysDeployConfig struct {
	AdminGatewayURL string            `yaml:"admin_gateway_url"`
	ServiceAuth     ServiceAuthConfig `yaml:"service_auth"`
}

// ServiceAuthConfig describes backend HMAC auth for /api/service calls.
type ServiceAuthConfig struct {
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	TargetNode    string `yaml:"target_node"`
	CAFile        string `yaml:"ca_file"`
	CAPEMBase64   string `yaml:"ca_pem_base64"`
	ExpireSeconds int64  `yaml:"expire_seconds"`
}

// HealthConfig controls the lightweight HTTP health endpoint.
type HealthConfig struct {
	Addr string `yaml:"addr"`
}

// Load reads YAML config from path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
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
	if v := os.Getenv("MOOX_GATEWAY_NODE_ID"); v != "" {
		c.SysDeploy.ServiceAuth.TargetNode = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); v != "" {
		c.SysDeploy.ServiceAuth.AccessKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_FILE"); v != "" {
		c.SysDeploy.ServiceAuth.CAFile = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_PEM_B64"); v != "" {
		c.SysDeploy.ServiceAuth.CAPEMBase64 = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET"); v != "" {
		c.Storage.GatewayTarget = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID"); v != "" {
		c.Storage.GatewayNodeID = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_KEY_ID"); v != "" {
		c.Storage.KeyID = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_HMAC_KEY_FILE"); v != "" {
		c.Storage.HMACKeyFile = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
}

func (c *Config) validateStorageTargets() error {
	if !isStorageTRPCTarget(c.Storage.GatewayTarget) {
		return fmt.Errorf("storage.gateway_target must be a tRPC target, got %q", c.Storage.GatewayTarget)
	}
	if strings.TrimSpace(c.Storage.HMACKeyFile) != "" {
		if _, err := gatewayauth.CredentialsFromKeyFile(c.Storage.KeyID, c.Storage.HMACKeyFile); err != nil {
			return fmt.Errorf("storage hmac credentials: %w", err)
		}
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
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		CloudNode: CloudNodeConfig{
			Address:     "127.0.0.1:11401",
			ServicePath: "trpc.moox.cloudnode.CloudNodeMgr",
		},
		Storage: StorageConfig{GatewayTarget: "ip://127.0.0.1:11003"},
		SysDeploy: SysDeployConfig{
			ServiceAuth: ServiceAuthConfig{ExpireSeconds: 60},
		},
		Health: HealthConfig{
			Addr: ":11412",
		},
	}
}
