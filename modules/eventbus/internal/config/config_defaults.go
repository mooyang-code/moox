// Package config owns the explicit, validated EventBus configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"gopkg.in/yaml.v3"
)

func Default() *Config {
	return &Config{
		Broker:  BrokerConfig{Host: "127.0.0.1", Port: 4222, ServerName: "eventbus-dev-1", StoreDir: "./data/eventbus/jetstream", StartupTimeout: 10 * time.Second, MaxPayloadBytes: 8 * 1024 * 1024},
		Streams: events.Streams(),
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("parse config %s: expected a single YAML document", path)
	}
	cfg.applyDefaults()
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
}
