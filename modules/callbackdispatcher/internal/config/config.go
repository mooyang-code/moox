// Package config 加载事件中心（moox-callback-dispatcher）的进程配置。
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Config 是配置根。
type Config struct {
	// GatewayClient 是调用其他服务（系统动作调 Trade）用的网关身份；公开的 KeyID 与密钥文件路径由安装流程写入。
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	Database      DatabaseConfig           `yaml:"database"`
	EventBus      EventBusConfig           `yaml:"eventbus"`
	Health        HealthConfig             `yaml:"health"`
	Delivery      DeliveryConfig           `yaml:"delivery"`
	Retention     RetentionConfig          `yaml:"retention"`
}

// DatabaseConfig 是 SQLite 库的位置。
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// EventBusConfig 是 EventBus 连接；durable 的名字和过滤主题由代码决定，不在这里配置。
type EventBusConfig struct {
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
}

// HealthConfig 是健康检查的监听地址。
type HealthConfig struct {
	Addr string `yaml:"addr"`
}

// DeliveryConfig 控制通知的投递。
type DeliveryConfig struct {
	// WorkerBatch 是每轮最多同时处理的执行记录数。
	WorkerBatch    int           `yaml:"worker_batch"`
	PollInterval   time.Duration `yaml:"poll_interval"`
	ConnectTimeout time.Duration `yaml:"connect_timeout"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	// RetryWindow 是从第一次失败起允许重试的最长时间，超过就置为失败。
	RetryWindow time.Duration `yaml:"retry_window"`
}

// RetentionConfig 控制执行记录与事件回执的保留。
type RetentionConfig struct {
	ExecutionDays int `yaml:"execution_days"`
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.FileConfig{Caller: "callback-dispatcher", KeyFile: "../../secrets/caller-callback-dispatcher.key"},
		Database:      DatabaseConfig{Path: "./data/moox_callback_dispatcher.db"},
		EventBus:      EventBusConfig{URLs: []string{"nats://127.0.0.1:4222"}},
		Health:        HealthConfig{Addr: ":11461"},
		Delivery: DeliveryConfig{
			WorkerBatch: 8, PollInterval: time.Second,
			ConnectTimeout: 3 * time.Second, RequestTimeout: 10 * time.Second, RetryWindow: 24 * time.Hour,
		},
		Retention: RetentionConfig{ExecutionDays: 30},
	}
}

// Load 读取并校验配置文件；未知字段与多文档都视为错误。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s 失败: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析配置 %s 失败: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("事件中心配置只能包含一个 YAML 文档")
	}
	if v := os.Getenv("MOOX_CALLBACK_DISPATCHER_DB_PATH"); v != "" {
		cfg.Database.Path = v
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("校验配置 %s 失败: %w", path, err)
	}
	return cfg, nil
}

// Validate 校验配置。
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("配置不能为空")
	}
	if c.GatewayClient.Caller != "callback-dispatcher" {
		return fmt.Errorf("gateway_client.caller 必须是 callback-dispatcher")
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path 不能为空")
	}
	if len(c.EventBus.URLs) == 0 {
		return fmt.Errorf("eventbus.urls 不能为空")
	}
	if c.Health.Addr == "" {
		return fmt.Errorf("health.addr 不能为空")
	}
	d := c.Delivery
	if d.WorkerBatch < 1 || d.WorkerBatch > 64 {
		return fmt.Errorf("delivery.worker_batch 必须在 1 到 64 之间")
	}
	if d.PollInterval < 100*time.Millisecond || d.PollInterval > time.Minute {
		return fmt.Errorf("delivery.poll_interval 必须在 100ms 到 1m 之间")
	}
	if d.ConnectTimeout <= 0 || d.RequestTimeout < d.ConnectTimeout || d.RequestTimeout > time.Minute {
		return fmt.Errorf("delivery.connect_timeout 必须大于 0，delivery.request_timeout 必须不小于连接超时且不超过 1m")
	}
	if d.RetryWindow < time.Minute {
		return fmt.Errorf("delivery.retry_window 不能小于 1m")
	}
	if c.Retention.ExecutionDays < 1 {
		return fmt.Errorf("retention.execution_days 必须大于 0")
	}
	return nil
}
