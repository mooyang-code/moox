package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if cfg.Scheduler.ResultRetentionDays != 14 {
		t.Fatalf("retention days = %d", cfg.Scheduler.ResultRetentionDays)
	}
	if cfg.Scheduler.MaxConcurrency != 16 {
		t.Fatalf("max concurrency = %d", cfg.Scheduler.MaxConcurrency)
	}
	if !cfg.SysDeploy.Enabled || cfg.SysDeploy.Target != "ip://127.0.0.1:11109" {
		t.Fatalf("sysdeploy = %+v", cfg.SysDeploy)
	}
	if cfg.Alert.SendTimeoutSeconds != 10 {
		t.Fatalf("alert send timeout = %d", cfg.Alert.SendTimeoutSeconds)
	}
	if !cfg.Metrics.HostStorage.Enabled || cfg.Metrics.HostStorage.SpaceID != "moox_system" || cfg.Metrics.HostStorage.Frequency != "1m" {
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
	if len(cfg.Server.Service) != 7 {
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
	if cfg.Server.Service[3].Name != "trpc.moox.monitor.sysdeploy.timer" || cfg.Server.Service[3].Port != 11416 {
		t.Fatalf("sysdeploy timer service = %+v", cfg.Server.Service[3])
	}
}

func TestMonitorConfigEnvOverride(t *testing.T) {
	t.Setenv("MOOX_MONITOR_DB_PATH", "/tmp/moox-monitor.db")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")

	cfg, err := Load(writeConfig(t, `
instance:
  instance_id: monitor-test
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Path != "/tmp/moox-monitor.db" {
		t.Fatalf("database path = %q", cfg.Database.Path)
	}
}

func TestMonitorConfigKeepsExplicitEmptyEventBusCredentials(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	cfg, err := Load(writeConfig(t, "metrics:\n  eventbus_credential_file: \"\"\n  host_eventbus_credential_file: \"\"\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Metrics.EventBusCredentialFile != "" || cfg.Metrics.HostEventBusCredentialFile != "" {
		t.Fatalf("explicit empty eventbus credentials were replaced: %q, %q", cfg.Metrics.EventBusCredentialFile, cfg.Metrics.HostEventBusCredentialFile)
	}
}

func TestMonitorGatewayAuthEnvironment(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-hk-177")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "monitor-key")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "monitor-secret")
	t.Setenv("MOOX_GATEWAY_CA_FILE", "/tmp/peers.pem")
	cfg := Default()
	cfg.applyEnv()
	if cfg.SysDeploy.ServiceAuth.TargetNode != "gateway-hk-177" || cfg.SysDeploy.ServiceAuth.KeyID != "monitor-key" || cfg.SysDeploy.ServiceAuth.SecretKey != "monitor-secret" || cfg.SysDeploy.ServiceAuth.CAFile != "/tmp/peers.pem" {
		t.Fatalf("gateway auth = %#v", cfg.SysDeploy.ServiceAuth)
	}
}

func TestMonitorConfigLoadsHealthAuthOnlyFromEnvironment(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	cfg, err := Load(writeConfig(t, "{}\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HealthAuth.Version != "moox-health-v1" || cfg.HealthAuth.AccessKey != "monitor" || cfg.HealthAuth.SecretKey != "secret" {
		t.Fatalf("health auth = %+v", cfg.HealthAuth)
	}
}

func TestMonitorConfigRequiresHealthCredentialsWhenSysDeployEnabled(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want health credential error")
	}
	cfg.SysDeploy.Enabled = false
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with SysDeploy disabled = %v", err)
	}
}

func TestMonitorConfigValidatesInstanceID(t *testing.T) {
	cfg := Default()
	cfg.Instance.InstanceID = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want instance_id error")
	}
}

func TestMonitorAppConfigHasNoProcessOwnedScheduleIntervals(t *testing.T) {
	raw, err := os.ReadFile("../../config/app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "eventbus_credential_file: ~/.config/moox/eventbus/monitor-metrics-consumer.yaml") || !strings.Contains(text, "host_eventbus_credential_file: ~/.config/moox/eventbus/monitor-eventbus.yaml") {
		t.Fatal("monitor app config must keep separate hostmetrics and metrics credentials")
	}
	if strings.Contains(text, "~/.config/moox/monitor/eventbus.yaml") {
		t.Fatal("monitor app config contains the retired shared credential path")
	}
	for _, oldKey := range []string{"reload_interval_seconds", "pull_interval_seconds"} {
		if strings.Contains(text, oldKey) {
			t.Fatalf("app config still contains %q", oldKey)
		}
	}
}

func TestMonitorConfigValidatesHostStorageContract(t *testing.T) {
	cfg := Default()
	cfg.Metrics.HostStorage.SpaceID = "crypto"
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want reserved host space error")
	}
}

func TestMonitorConfigRejectsLegacyHostRetention(t *testing.T) {
	_, err := Load(writeConfig(t, "metrics:\n  host_storage:\n    retention: 72h\n"))
	if err == nil {
		t.Fatal("Load() error = nil, want unknown host retention field")
	}
}

func TestMonitorConfigRejectsRemovedPeerConfiguration(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	_, err := Load(writeConfig(t, "peer:\n  enabled: false\n"))
	if err == nil || !strings.Contains(err.Error(), "field peer not found") {
		t.Fatalf("Load() error = %v, want removed peer field rejection", err)
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
