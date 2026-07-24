package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	EventBus    EventBusConfig    `yaml:"eventbus"`
	Aggregation AggregationConfig `yaml:"aggregation"`
	State       StateConfig       `yaml:"state"`
}

type StateConfig struct {
	CheckpointPath string `yaml:"checkpoint_path"`
}

type EventBusConfig struct {
	URLs         []string      `yaml:"urls"`
	Stream       string        `yaml:"stream"`
	Durable      string        `yaml:"durable"`
	FetchBatch   int           `yaml:"fetch_batch"`
	FetchMaxWait time.Duration `yaml:"fetch_max_wait"`
}

type AggregationConfig struct {
	InputFrequency  string        `yaml:"input_frequency"`
	TargetFrequency string        `yaml:"target_frequency"`
	AllowedLateness time.Duration `yaml:"allowed_lateness"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read streamcalc config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse streamcalc config: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.State.CheckpointPath == "" {
		c.State.CheckpointPath = "./data/streamcalc/checkpoint.json"
	}
	if len(c.EventBus.URLs) == 0 {
		c.EventBus.URLs = []string{"nats://127.0.0.1:4222"}
	}
	if c.EventBus.Stream == "" {
		c.EventBus.Stream = "MOOX_MARKET"
	}
	if c.EventBus.Durable == "" {
		c.EventBus.Durable = "streamcalc_kline_v1"
	}
	if c.EventBus.FetchBatch <= 0 {
		c.EventBus.FetchBatch = 32
	}
	if c.EventBus.FetchMaxWait <= 0 {
		c.EventBus.FetchMaxWait = time.Second
	}
	if c.Aggregation.InputFrequency == "" {
		c.Aggregation.InputFrequency = "1m"
	}
	if c.Aggregation.TargetFrequency == "" {
		c.Aggregation.TargetFrequency = "5m"
	}
	if c.Aggregation.AllowedLateness < 0 {
		c.Aggregation.AllowedLateness = 0
	}
}

func (c Config) Validate() error {
	if len(c.EventBus.URLs) == 0 || strings.TrimSpace(c.EventBus.Stream) == "" || strings.TrimSpace(c.EventBus.Durable) == "" {
		return fmt.Errorf("eventbus urls, stream, and durable are required")
	}
	if c.EventBus.FetchBatch <= 0 || c.EventBus.FetchMaxWait <= 0 {
		return fmt.Errorf("eventbus fetch settings must be positive")
	}
	if _, err := ParseFrequency(c.Aggregation.InputFrequency); err != nil {
		return fmt.Errorf("input_frequency: %w", err)
	}
	if _, err := ParseFrequency(c.Aggregation.TargetFrequency); err != nil {
		return fmt.Errorf("target_frequency: %w", err)
	}
	if strings.TrimSpace(c.State.CheckpointPath) == "" {
		return fmt.Errorf("state checkpoint_path is required")
	}
	return nil
}

func ParseFrequency(value string) (time.Duration, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return 0, fmt.Errorf("frequency is empty")
	}
	var multiplier time.Duration
	var number string
	switch value[len(value)-1] {
	case 'm':
		multiplier, number = time.Minute, value[:len(value)-1]
	case 'h':
		multiplier, number = time.Hour, value[:len(value)-1]
	case 'd':
		multiplier, number = 24*time.Hour, value[:len(value)-1]
	default:
		return 0, fmt.Errorf("unsupported frequency %q", value)
	}
	var n int
	if _, err := fmt.Sscanf(number, "%d", &n); err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid frequency %q", value)
	}
	return time.Duration(n) * multiplier, nil
}
