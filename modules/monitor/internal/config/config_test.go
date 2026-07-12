package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestMonitorConfigDefaults(t *testing.T) {
	cfg := Default()

	if cfg.Database.Path != "./data/monitor/monitor.db" {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
	if cfg.Health.Addr != ":11409" {
		t.Fatalf("health addr = %q", cfg.Health.Addr)
	}
	if cfg.Instance.InstanceID == "" {
		t.Fatal("instance id must not be empty")
	}
	if cfg.Instance.BaseURL != "http://127.0.0.1:11409" {
		t.Fatalf("base url = %q", cfg.Instance.BaseURL)
	}
	if cfg.Scheduler.ReloadIntervalSeconds != 30 {
		t.Fatalf("reload interval = %d", cfg.Scheduler.ReloadIntervalSeconds)
	}
	if cfg.Scheduler.ResultRetentionDays != 14 {
		t.Fatalf("retention days = %d", cfg.Scheduler.ResultRetentionDays)
	}
	if cfg.Scheduler.MaxConcurrency != 16 {
		t.Fatalf("max concurrency = %d", cfg.Scheduler.MaxConcurrency)
	}
	if !cfg.SysDeploy.Enabled || cfg.SysDeploy.Target != "ip://127.0.0.1:11109" {
		t.Fatalf("sysdeploy = %+v", cfg.SysDeploy)
	}
	if !cfg.Peer.Enabled || cfg.Peer.PullIntervalSeconds != 10 || cfg.Peer.TimeoutSeconds != 5 {
		t.Fatalf("peer = %+v", cfg.Peer)
	}
	if cfg.Alert.SendTimeoutSeconds != 10 {
		t.Fatalf("alert send timeout = %d", cfg.Alert.SendTimeoutSeconds)
	}
	if !cfg.Metrics.HostStorage.Enabled || cfg.Metrics.HostStorage.SpaceID != "moox_system" || cfg.Metrics.HostStorage.Frequency != "1m" || cfg.Metrics.HostStorage.Retention != 72*time.Hour {
		t.Fatalf("host storage defaults = %+v", cfg.Metrics.HostStorage)
	}
}

func TestMonitorConfigTRPCPort(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "trpc_go.yaml"))
	if err != nil {
		t.Fatalf("read trpc config: %v", err)
	}
	var cfg struct {
		Server struct {
			Service []struct {
				Name string `yaml:"name"`
				Port int    `yaml:"port"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse trpc config: %v", err)
	}
	if len(cfg.Server.Service) != 3 {
		t.Fatalf("service count = %d", len(cfg.Server.Service))
	}
	if cfg.Server.Service[0].Name != "trpc.moox.monitor.MonitorMgr" || cfg.Server.Service[0].Port != 11410 {
		t.Fatalf("service = %+v", cfg.Server.Service[0])
	}
	if cfg.Server.Service[1].Name != "trpc.moox.monitor.Health" || cfg.Server.Service[1].Port != 11409 {
		t.Fatalf("health service = %+v", cfg.Server.Service[1])
	}
	if cfg.Server.Service[2].Name != "trpc.moox.monitor.metrics.timer" || cfg.Server.Service[2].Port != 11415 {
		t.Fatalf("metrics timer service = %+v", cfg.Server.Service[2])
	}
}

func TestMonitorConfigEnvOverride(t *testing.T) {
	t.Setenv("MOOX_MONITOR_DB_PATH", "/tmp/moox-monitor.db")

	cfg, err := Load(writeConfig(t, `
instance:
  instance_id: monitor-test
peer:
  enabled: false
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Path != "/tmp/moox-monitor.db" {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
}

func TestMonitorConfigValidatesInstanceID(t *testing.T) {
	cfg := Default()
	cfg.Instance.InstanceID = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want instance_id error")
	}
}

func TestMonitorConfigValidatesPeerEntries(t *testing.T) {
	path := writeConfig(t, `
instance:
  instance_id: monitor-test
peer:
  peers:
    - instance_id: peer-a
      base_url: http://127.0.0.1:11419
`)
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want invalid peer entry")
	}
}

func TestMonitorConfigRequiresPeerTokenWhenEnabled(t *testing.T) {
	cfg := Default()
	cfg.Instance.InstanceID = "monitor-test"
	cfg.Peer.Enabled = true
	cfg.Peer.Token = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want peer token error")
	}
}

func TestMonitorConfigValidatesHostStorageContract(t *testing.T) {
	cfg := Default()
	cfg.Metrics.HostStorage.SpaceID = "crypto"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want reserved host space error")
	}
	cfg = Default()
	cfg.Metrics.HostStorage.Retention = 73 * time.Hour
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want retention limit error")
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
