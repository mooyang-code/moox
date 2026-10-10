package hostgatewayconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigRoundTripAndReleaseRelativePaths(t *testing.T) {
	for _, host := range []string{"control", "compute-1"} {
		config := Default(host, "control", "2001:db8::1", "fixture-key-id")
		raw, err := Encode(config)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(t.TempDir(), "host-gateway", "config")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "app.yaml")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Dir(filepath.Dir(dir))
		if loaded.Host != config.Host || loaded.Control.KeyID != config.Control.KeyID || loaded.Control.KeyFile != filepath.Join(base, "secrets", "caller-host-gateway.key") || loaded.TLS.CAFile != filepath.Join(base, "certs", "moox-ca.crt") || loaded.Store.Path != filepath.Join(base, "data", "host-gateway") {
			t.Fatalf("incorrect release path resolution: %+v", loaded)
		}
		want := "[2001:db8::1]:11003"
		if host == "control" {
			want = "127.0.0.1:11112"
		}
		if loaded.Control.Target != want {
			t.Fatalf("control target %q, want %q", loaded.Control.Target, want)
		}
	}
}

func TestConfigRejectsInvalidIdentityListenersAndControlEndpoint(t *testing.T) {
	for _, change := range []func(*Config){
		func(c *Config) { c.Host.ID = "Control" },
		func(c *Config) { c.Control.HostID = "../control" },
		func(c *Config) { c.Control.Caller = "host-gateway@storage" },
		func(c *Config) { c.Control.KeyID = "" },
		func(c *Config) { c.Control.KeyID = "key\x01" },
		func(c *Config) { c.Server.LocalAddr = "0.0.0.0:11002" },
		func(c *Config) { c.Server.RemoteAddr = "localhost:11003" },
		func(c *Config) { c.Server.HealthAddr = c.Server.RemoteAddr },
		func(c *Config) { c.Control.Target = "https://control:11003" },
		func(c *Config) { c.Control.Target = "127.0.0.1:11003" },
		func(c *Config) { c.Control.Target = "192.0.2.1:11112" },
		func(c *Config) { c.TLS.CAFile = "" },
		func(c *Config) { c.Control.KeyFile = "bad\npath" },
	} {
		config := Default("control", "control", "192.0.2.1", "fixture-key")
		change(&config)
		if err := config.Validate(); err == nil {
			t.Fatalf("accepted invalid configuration: %+v", config)
		}
	}
}

func TestConfigStrictBoundedYAMLNeverEchoesUnknownValues(t *testing.T) {
	raw, err := Encode(Default("control", "control", "192.0.2.1", "fixture-key"))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		string(raw) + "obsolete: fixture-do-not-echo\n",
		string(raw) + "---\n{}\n",
		string(raw) + "#" + strings.Repeat("x", 1<<20),
		"[invalid yaml",
		"",
	} {
		_, err := Decode(strings.NewReader(value))
		if err == nil || strings.Contains(err.Error(), "fixture-do-not-echo") {
			t.Fatalf("expected redacted validation error, got %v", err)
		}
	}
}
