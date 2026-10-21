// Package streamapp 装配 collector-stream：WS 推送接收运行时、计划同步代理与本地执行端。
package streamapp

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Config 是 collector-stream 的应用配置（严格解析，未知字段拒绝）。
type Config struct {
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	// SpaceID 是本实例服务的空间（同一空间只部署一个实例）；为空时读 MOOX_SPACE_ID。
	SpaceID string `yaml:"space_id"`
	// InstanceID 为空时取主机名。
	InstanceID string `yaml:"instance_id"`
	// DataDir 保存网关目录缓存等本地状态。
	DataDir string `yaml:"data_dir"`
	// MarketConfig 是数据源配置（binance.yaml：WS 地址与限额、REST 端点、Storage 绑定）。
	MarketConfig string `yaml:"market_config"`
	// StorageTimeoutMS 是本地执行端单次 Storage 写入的超时。
	StorageTimeoutMS int `yaml:"storage_timeout_ms"`
	// LocalExecutor 是本地执行端（补采与本地定时拉取）的并发上限。
	LocalExecutor struct {
		Concurrency int `yaml:"concurrency"`
	} `yaml:"local_executor"`
	sourcePath string
}

// Load 读取并校验配置；相对路径按配置文件所在目录解析。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read collector-stream config %s: %w", path, err)
	}
	cfg := &Config{DataDir: "../data", MarketConfig: "sources/market/binance.yaml", StorageTimeoutMS: 4000}
	cfg.LocalExecutor.Concurrency = 4
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse collector-stream config %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("collector-stream config must contain exactly one YAML document")
	}
	if cfg.sourcePath, err = filepath.Abs(path); err != nil {
		return nil, err
	}
	if cfg.GatewayClient.Caller != "collector-stream" {
		return nil, fmt.Errorf("gateway_client.caller must be collector-stream")
	}
	if err := cfg.GatewayClient.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.SpaceID) == "" {
		cfg.SpaceID = os.Getenv("MOOX_SPACE_ID")
	}
	cfg.SpaceID = strings.ToLower(strings.TrimSpace(cfg.SpaceID))
	if cfg.SpaceID == "" {
		return nil, fmt.Errorf("space_id (or MOOX_SPACE_ID) is required")
	}
	if strings.TrimSpace(cfg.InstanceID) == "" {
		host, _ := os.Hostname()
		cfg.InstanceID = "collector-stream@" + host
	}
	if cfg.StorageTimeoutMS < 500 || cfg.StorageTimeoutMS > 30000 || cfg.LocalExecutor.Concurrency < 1 || cfg.LocalExecutor.Concurrency > 32 {
		return nil, fmt.Errorf("storage_timeout_ms 必须在 500～30000 之间，local_executor.concurrency 在 1～32 之间")
	}
	cfg.DataDir, cfg.MarketConfig = cfg.resolve(cfg.DataDir), cfg.resolve(cfg.MarketConfig)
	if _, err := os.Stat(cfg.MarketConfig); err != nil {
		return nil, fmt.Errorf("market_config: %w", err)
	}
	return cfg, nil
}

func (c *Config) resolve(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(filepath.Dir(c.sourcePath), path)
}

// SourcePath 是配置文件的绝对路径。
func (c *Config) SourcePath() string { return c.sourcePath }
