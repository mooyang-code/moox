package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigurationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Config)
	}{
		{"remote admin", func(c *Config) { c.Upstreams.Admin = "192.0.2.1:11000" }},
		{"DNS loopback", func(c *Config) { c.Upstreams.Admin = "localhost:11000" }},
		{"public diagnostics", func(c *Config) { c.Health.Listen = "0.0.0.0:19528" }},
		{"port zero", func(c *Config) { c.Public.Port = 0 }},
		{"injected hostname", func(c *Config) { c.Public.Host = "localhost { respond hi }" }},
		{"wildcard", func(c *Config) { c.Public.Host = "*.example.com" }},
		{"mode", func(c *Config) { c.TLS.Mode = "auto" }},
		{"unbounded drain", func(c *Config) { c.Lifecycle.DrainTimeout = 0 }},
		{"excessive drain", func(c *Config) { c.Lifecycle.DrainTimeout = time.Hour }},
		{"untrusted ACME", func(c *Config) { c.TLS.ACMECA = "http://192.0.2.1/directory" }},
		{"unspecified self proxy", func(c *Config) { c.Upstreams.Admin = "127.0.0.1:9527" }},
		{"overlapping diagnostics", func(c *Config) { c.Health.Listen = "127.0.0.1:9527" }},
		{"health upstream", func(c *Config) { c.Upstreams.Web = c.Health.Listen }},
		{"IPv4 mapped self proxy", func(c *Config) {
			c.Public.Bind = "127.0.0.1"
			c.Upstreams.Admin = "[::ffff:127.0.0.1]:9527"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Defaults()
			tc.mutate(&c)
			if c.Validate() == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadStrictAndSideEffectFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config", "proxy.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("public:\n  host: console.example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLS.StorageRoot != filepath.Join(dir, "data", "caddy", "caddy") {
		t.Fatal("state paths not relative to config")
	}
	if _, err := os.Stat(cfg.TLS.StorageRoot); !os.IsNotExist(err) {
		t.Fatal("validation touched persistent state")
	}
	if cfg.StopBudget() != 45*time.Second {
		t.Fatalf("unexpected stop budget %s", cfg.StopBudget())
	}
	for _, raw := range []string{"unknown: value", "{}\n---\n{}", "public:\n  typo: true", "{}\n#" + strings.Repeat("x", 64*1024)} {
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted invalid YAML %q", raw)
		}
	}
}
