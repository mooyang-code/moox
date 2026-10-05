// Package bootstrap loads configuration and wires the moox-factor-mgr process.
package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/enginehub"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Database DatabaseConfig `yaml:"database"`
	Storage  StorageConfig  `yaml:"storage"`
	Python   PythonConfig   `yaml:"python"`
	Engine   EngineConfig   `yaml:"engine"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type StorageConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

// PythonConfig names the interpreter that test-loads factor sources when a
// definition is created or edited; it needs pandas and numpy.
type PythonConfig struct {
	Bin string `yaml:"bin"`
}

// EngineConfig bounds the compute engine's leases.
type EngineConfig struct {
	LeaseTTL    time.Duration `yaml:"lease_ttl"`
	JobLeaseTTL time.Duration `yaml:"job_lease_ttl"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read factor config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse factor config %s: %w", path, err)
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		Database: DatabaseConfig{Path: "./data/factor/factor.db"},
		Storage:  StorageConfig{GatewayTarget: "ip://127.0.0.1:11003", KeyID: "factor"},
		Python:   PythonConfig{Bin: "python3"},
		Engine:   EngineConfig{LeaseTTL: enginehub.DefaultEngineLeaseTTL, JobLeaseTTL: enginehub.DefaultJobLeaseTTL},
	}
}

func (c *Config) applyDefaults() {
	defaults := Default()
	if c.Database.Path == "" {
		c.Database.Path = defaults.Database.Path
	}
	if c.Storage.GatewayTarget == "" {
		c.Storage.GatewayTarget = defaults.Storage.GatewayTarget
	}
	if c.Storage.KeyID == "" {
		c.Storage.KeyID = defaults.Storage.KeyID
	}
	if c.Python.Bin == "" {
		c.Python.Bin = defaults.Python.Bin
	}
	if c.Engine.LeaseTTL == 0 {
		c.Engine.LeaseTTL = defaults.Engine.LeaseTTL
	}
	if c.Engine.JobLeaseTTL == 0 {
		c.Engine.JobLeaseTTL = defaults.Engine.JobLeaseTTL
	}
}

func (c *Config) applyEnv() {
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_DB_PATH")); value != "" {
		c.Database.Path = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_GATEWAY_TARGET")); value != "" {
		c.Storage.GatewayTarget = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_GATEWAY_NODE_ID")); value != "" {
		c.Storage.GatewayNodeID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_KEY_ID")); value != "" {
		c.Storage.KeyID = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_HMAC_KEY_FILE")); value != "" {
		c.Storage.HMACKeyFile = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_FACTOR_PYTHON_BIN")); value != "" {
		c.Python.Bin = value
	}
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("factor config is required")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		return fmt.Errorf("database.path is required")
	}
	if !validTRPCTarget(c.Storage.GatewayTarget) {
		return fmt.Errorf("storage.gateway_target must be a tRPC target")
	}
	if strings.TrimSpace(c.Storage.HMACKeyFile) != "" {
		if _, err := gatewayauth.CredentialsFromKeyFile(c.Storage.KeyID, c.Storage.HMACKeyFile); err != nil {
			return fmt.Errorf("storage hmac credentials: %w", err)
		}
	}
	if strings.TrimSpace(c.Python.Bin) == "" {
		return fmt.Errorf("python.bin is required")
	}
	if c.Engine.LeaseTTL < 10*time.Second {
		return fmt.Errorf("engine.lease_ttl must be at least 10s")
	}
	if c.Engine.JobLeaseTTL < time.Minute {
		return fmt.Errorf("engine.job_lease_ttl must be at least 1m")
	}
	return nil
}

func validTRPCTarget(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value != "" && !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://")
}
