// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

func Default() *Config {
	return &Config{
		Broker: BrokerConfig{Host: "127.0.0.1", Port: 4222, ServerName: "eventbus-dev-1", StoreDir: "./data/eventbus/jetstream", StartupTimeout: 10 * time.Second, MaxPayloadBytes: 8 * 1024 * 1024, Cluster: ClusterConfig{Name: "MOOX_EVENTBUS", Host: "127.0.0.1", Port: 6222}},
		Health: HealthConfig{Addr: "127.0.0.1:11419"},
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	d := Default()
	if c.Broker.Host == "" {
		c.Broker.Host = d.Broker.Host
	}
	if c.Broker.Port == 0 {
		c.Broker.Port = d.Broker.Port
	}
	if c.Broker.ServerName == "" {
		c.Broker.ServerName = d.Broker.ServerName
	}
	if c.Broker.StoreDir == "" {
		c.Broker.StoreDir = d.Broker.StoreDir
	}
	if c.Broker.StartupTimeout == 0 {
		c.Broker.StartupTimeout = d.Broker.StartupTimeout
	}
	if c.Broker.MaxPayloadBytes == 0 {
		c.Broker.MaxPayloadBytes = d.Broker.MaxPayloadBytes
	}
	if c.Broker.Cluster.Name == "" {
		c.Broker.Cluster.Name = d.Broker.Cluster.Name
	}
	if c.Broker.Cluster.Host == "" {
		c.Broker.Cluster.Host = d.Broker.Cluster.Host
	}
	if c.Broker.Cluster.Port == 0 {
		c.Broker.Cluster.Port = d.Broker.Cluster.Port
	}
	if c.Health.Addr == "" {
		c.Health.Addr = d.Health.Addr
	}
	for i := range c.Streams {
		normalizeStream(&c.Streams[i])
	}
	for i := range c.KV {
		normalizeKV(&c.KV[i])
	}
}

func normalizeStream(s *StreamConfig) {
	if s.Retention == "" {
		s.Retention = "limits"
	}
	if s.Storage == "" {
		s.Storage = "file"
	}
	if s.Discard == "" {
		s.Discard = "old"
	}
	if s.Replicas == 0 {
		s.Replicas = 1
	}
}

func normalizeKV(k *KVConfig) {
	if k.Storage == "" {
		k.Storage = "file"
	}
	if k.History == 0 {
		k.History = 1
	}
	if k.Replicas == 0 {
		k.Replicas = 1
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_EVENTBUS_STORE_DIR"); v != "" {
		c.Broker.StoreDir = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_HOST"); v != "" {
		c.Broker.Host = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_SERVER_NAME"); v != "" {
		c.Broker.ServerName = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := os.Getenv("MOOX_EVENTBUS_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Broker.Port = n
		}
	}
	if v := os.Getenv("MOOX_EVENTBUS_MAX_PAYLOAD_BYTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Broker.MaxPayloadBytes = n
		}
	}
	if v := os.Getenv("MOOX_EVENTBUS_STREAM_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			for i := range c.Streams {
				c.Streams[i].MaxBytes = n
			}
		}
	}
}
