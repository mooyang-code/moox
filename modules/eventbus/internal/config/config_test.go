package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultConfigIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Broker.MaxPayloadBytes != 8*1024*1024 || c.Broker.StartupTimeout != 10*time.Second {
		t.Fatalf("unexpected broker defaults: %#v", c.Broker)
	}
}

func TestRepositoryConfigLoads(t *testing.T) {
	if _, err := Load("../../config/app.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestLoadYAMLAndEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte("broker:\n  store_dir: ./data/test\n  port: 4223\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_EVENTBUS_PORT", "4333")
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Broker.Port != 4333 || c.Broker.StoreDir != "./data/test" {
		t.Fatalf("overrides not applied: %#v", c.Broker)
	}
}

func TestRejectUnsafeAndInvalidConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"root store":           func(c *Config) { c.Broker.StoreDir = "/" },
		"duplicate stream":     func(c *Config) { c.Streams = append(c.Streams, c.Streams[0]) },
		"bad TLS":              func(c *Config) { c.Broker.TLS.Enabled = true; c.Broker.TLS.CertFile = "cert" },
		"bad auth":             func(c *Config) { c.Broker.Auth.Enabled = true },
		"bad version":          func(c *Config) { c.Topics[0].PayloadVersion = 0 },
		"version mismatch":     func(c *Config) { c.Topics[0].PayloadVersion = 2 },
		"cluster default name": func(c *Config) { c.Broker.Cluster.Enabled = true },
		"overlap":              func(c *Config) { c.Streams[1].Subjects = []string{"moox.storage.>"} },
	} {
		t.Run(name, func(t *testing.T) {
			c := Default()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate returned nil")
			}
		})
	}
}

func TestRejectReplicaWithoutCluster(t *testing.T) {
	c := Default()
	c.Streams[0].Replicas = 2
	if err := c.Validate(); err == nil {
		t.Fatal("replica count was accepted without a cluster")
	}
}
