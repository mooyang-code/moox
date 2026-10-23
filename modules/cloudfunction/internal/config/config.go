// Package config loads moox-cloudfunction process configuration.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/cloudfunction/internal/clsprovision"
	"github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Config is the root moox-cloudfunction configuration.
type Config struct {
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	sourcePath    string                   `yaml:"-"`
	Database      DatabaseConfig           `yaml:"database"`
	FunctionJob   FunctionJobConfig        `yaml:"function_job"`
	TencentSCF    TencentSCFConfig         `yaml:"tencent_scf"`
	// CLS 控制日志服务的自动开通与函数环境下发：默认开启，初始化后创建的 SCF 采集函数自动带上日志上报配置。
	CLS    clsprovision.Config `yaml:"cls"`
	Debug  DebugConfig         `yaml:"debug"`
	Health HealthConfig        `yaml:"health"`
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

// FunctionJobConfig controls asynchronous SCF instance batch execution.
type FunctionJobConfig struct {
	BatchSize    int           `yaml:"batch_size"`
	PollInterval time.Duration `yaml:"poll_interval"`
}

// TencentSCFConfig stores defaults for the Tencent SCF provider.
type TencentSCFConfig struct {
	DefaultRegion    string `yaml:"default_region"`
	DefaultNamespace string `yaml:"default_namespace"`
	DefaultRuntime   string `yaml:"default_runtime"`
	// APIRateLimits 按接口名覆盖进程内令牌桶的速率（次/秒）；未写的接口使用官方限频的 75%。
	// 值必须大于 0 且不超过官方限频，接口名必须是 FunctionInstance 调用的 SCF 接口。
	APIRateLimits map[string]float64 `yaml:"api_rate_limits"`
}

// DebugConfig controls local diagnostics endpoints.
type DebugConfig struct {
	PprofAddr string `yaml:"pprof_addr"`
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
	cfg.sourcePath = path
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("cloudfunction config must contain exactly one YAML document")
	}
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	if c.GatewayClient.Caller != "cloudfunction" {
		return fmt.Errorf("gateway_client.caller must be cloudfunction")
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}
	if c.FunctionJob.BatchSize < 1 || c.FunctionJob.BatchSize > 10 {
		return fmt.Errorf("function_job.batch_size must be between 1 and 10")
	}
	if c.FunctionJob.PollInterval < 100*time.Millisecond || c.FunctionJob.PollInterval > 10*time.Second {
		return fmt.Errorf("function_job.poll_interval must be between 100ms and 10s")
	}
	if err := tencentscf.ValidateRateLimits(c.TencentSCF.APIRateLimits); err != nil {
		return err
	}
	if err := c.CLS.Validate(); err != nil {
		return err
	}
	return nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_CLOUDFUNCTION_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_CLOUDFUNCTION_PPROF_ADDR"); v != "" {
		c.Debug.PprofAddr = v
	}
	if v := os.Getenv("MOOX_CLOUDFUNCTION_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
}

// Default returns safe local defaults.
func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.FileConfig{Caller: "cloudfunction", KeyFile: "../../secrets/caller-cloudfunction.key"},
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_cloudfunction.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		FunctionJob: FunctionJobConfig{
			BatchSize:    3,
			PollInterval: 500 * time.Millisecond,
		},
		TencentSCF: TencentSCFConfig{
			DefaultRegion:    "ap-guangzhou",
			DefaultNamespace: "default",
			DefaultRuntime:   "Go1",
		},
		CLS:   clsprovision.DefaultConfig(),
		Debug: DebugConfig{},
		Health: HealthConfig{
			Addr: ":11411",
		},
	}
}

// OpenGateway creates the process client shared by credential and lease calls.
func (c *Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c == nil || c.sourcePath == "" {
		return nil, fmt.Errorf("cloudfunction gateway client requires a loaded module configuration")
	}
	if c.GatewayClient.Caller != "cloudfunction" {
		return nil, fmt.Errorf("gateway_client.caller must be cloudfunction")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.Database.Path), onRefreshError)
}
