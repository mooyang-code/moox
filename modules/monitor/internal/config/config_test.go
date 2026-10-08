package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if !cfg.Metrics.HostStorage.Enabled || cfg.Metrics.HostStorage.SpaceID != "mooxsys" || cfg.Metrics.HostStorage.Frequency != "1m" {
		t.Fatalf("host storage defaults = %+v", cfg.Metrics.HostStorage)
	}
	if !cfg.Observability.Enabled || len(cfg.Observability.EventBusURLs) == 0 {
		t.Fatalf("observability defaults = %+v", cfg.Observability)
	}
	if cfg.Observability.BalanceDifferenceThreshold != 0.05 {
		t.Fatalf("balance difference threshold = %v", cfg.Observability.BalanceDifferenceThreshold)
	}
	if cfg.MarketCanary.ClosedBarMinCoverage != 0.99 {
		t.Fatalf("closed bar minimum coverage = %v", cfg.MarketCanary.ClosedBarMinCoverage)
	}
	if cfg.MarketCanary.Enabled || len(cfg.MarketCanary.Subjects) != 0 {
		t.Fatalf("market canary must come from deployment config, defaults = %+v", cfg.MarketCanary)
	}
	if cfg.MarketHealth.TimerCoordinationStaleAfter != 15*time.Minute ||
		cfg.MarketHealth.TimerCoordinationPendingGrace != 5*time.Minute ||
		cfg.MarketHealth.LowCapacityHeadroom != 2 {
		t.Fatalf("market health defaults = %+v", cfg.MarketHealth)
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

func TestMonitorConfigKeepsExplicitEmptyObservabilityCredential(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	cfg, err := Load(writeConfig(t, "observability:\n  credential_file: \"\"\n"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Observability.CredentialFile != "" {
		t.Fatalf("explicit empty observability credential was replaced: %q", cfg.Observability.CredentialFile)
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

func TestMonitorDefaultGatewayClientFollowsDeploymentLayout(t *testing.T) {
	cfg := Default()
	if err := cfg.GatewayClient.Validate(); err != nil {
		t.Fatalf("默认的 gateway_client 应当合法: %v", err)
	}
	if cfg.GatewayClient.Caller != "monitor" || cfg.Metrics.Storage.AppID != "monitor" || cfg.Metrics.HostStorage.AppID != "monitor" {
		t.Fatalf("gateway client = %+v, storage app = %q/%q", cfg.GatewayClient, cfg.Metrics.Storage.AppID, cfg.Metrics.HostStorage.AppID)
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
	if !strings.Contains(text, "credential_file: ~/.config/moox/eventbus/monitor-observability.yaml") {
		t.Fatal("monitor app config must declare the observability credential")
	}
	for _, retired := range []string{
		"eventbus_credential_file", "host_eventbus_credential_file",
		"monitor_metrics_ingest_v1", "monitor_hostmetrics_ingest_v1",
		"stream:", "consumer:", "filter_subject:",
	} {
		if strings.Contains(text, retired) {
			t.Fatalf("monitor app config contains retired field %q", retired)
		}
	}
	for _, oldKey := range []string{"reload_interval_seconds", "pull_interval_seconds"} {
		if strings.Contains(text, oldKey) {
			t.Fatalf("app config still contains %q", oldKey)
		}
	}
}

func TestMonitorAppConfigLoadsDynamicKlineFreshnessInventory(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var app map[string]any
	if err := yaml.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	metrics, ok := app["metrics"].(map[string]any)
	if !ok {
		t.Fatal("metrics config is missing")
	}
	for _, name := range []string{"storage", "host_storage"} {
		storage, ok := metrics[name].(map[string]any)
		if !ok || storage["app_id"] != "monitor" {
			t.Fatalf("metrics.%s 应当配置 Storage app_id: %v", name, metrics[name])
		}
	}
	fixture, err := yaml.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "app.yaml")
	if err := os.WriteFile(configPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load(app.yaml) error = %v", err)
	}
	if !cfg.KlineFreshness.Enabled {
		t.Fatalf("kline freshness = %+v", cfg.KlineFreshness)
	}
	freshness, ok := app["kline_freshness"].(map[string]any)
	if !ok {
		t.Fatal("kline_freshness config is missing")
	}
	if _, legacy := freshness["collector_gateway_url"]; legacy {
		t.Fatal("Collector 清单经 gatewayclient 访问，不再配置网关地址")
	}
	if cfg.KlineFreshness.InventoryRefreshInterval != time.Minute || cfg.KlineFreshness.InventoryPageSize != 100 ||
		cfg.KlineFreshness.InventoryMaxEntries != 1000 || cfg.KlineFreshness.StaleAfter != 5*time.Minute {
		t.Fatalf("dynamic kline freshness inventory = %+v", cfg.KlineFreshness)
	}
	if !reflect.DeepEqual(cfg.KlineFreshness.SpaceIDs, []string{"crypto", "stockcn"}) {
		t.Fatalf("loaded kline freshness Space IDs = %v", cfg.KlineFreshness.SpaceIDs)
	}
	if strings.Contains(string(raw), "view_binance_kline_1m") || strings.Contains(string(raw), "  rules:") {
		t.Fatal("Monitor app config still pins a static K-line View")
	}
	if len(cfg.MarketCanary.Subjects) != 1 || cfg.MarketCanary.Subjects[0].Symbol != "BTC-USDT" {
		t.Fatalf("market canary subjects = %+v", cfg.MarketCanary.Subjects)
	}
	if cfg.MarketCanary.Subjects[0].SeriesTag == nil || *cfg.MarketCanary.Subjects[0].SeriesTag != "venue:binance|market:spot|source:spot_http" {
		t.Fatalf("loaded market canary series tag = %+v", cfg.MarketCanary.Subjects[0].SeriesTag)
	}
}

func TestMonitorDefaultDatasetHealthPolicyPathExistsFromModuleWorkingDirectory(t *testing.T) {
	moduleRoot := filepath.Join("..", "..")
	cfg := Default()
	if _, err := os.Stat(filepath.Join(moduleRoot, cfg.Metrics.DatasetHealthPolicyPath)); err != nil {
		t.Fatalf("dataset_health_policy_path %q is not usable from modules/monitor: %v", cfg.Metrics.DatasetHealthPolicyPath, err)
	}
}

func TestMonitorConfigRejectsLegacyEventBusFields(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	for _, content := range []string{
		"metrics:\n  eventbus_credential_file: old.yaml\n",
		"metrics:\n  host_eventbus_credential_file: old.yaml\n",
		"metrics:\n  eventbus_url: nats://old:4222\n",
		"observability:\n  stream: MOOX_OBSERVABILITY\n",
		"observability:\n  consumer: another-consumer\n",
		"observability:\n  filter_subject: moox.event.observability.>\n",
	} {
		if _, err := Load(writeConfig(t, content)); err == nil {
			t.Fatalf("Load() accepted retired config: %s", content)
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

func TestMonitorConfigRequiresPresenceAwareMarketCanarySeriesTag(t *testing.T) {
	cfg := Default()
	cfg.SysDeploy.Enabled = false
	cfg.MarketCanary.Enabled = true
	cfg.MarketCanary.Subjects = []MarketCanarySubject{{SpaceID: "crypto", DatasetID: "dataset_task", Symbol: "BTC-USDT", Frequency: "1m"}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires series_tag") {
		t.Fatalf("Validate() error = %v, want missing series_tag error", err)
	}

	empty := ""
	cfg.MarketCanary.Subjects[0].SeriesTag = &empty
	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit default series tag must be valid: %v", err)
	}
}

func TestMonitorConfigKlineFreshnessDefaultsAndValidation(t *testing.T) {
	cfg := Default()
	if cfg.KlineFreshness.EvaluationInterval != 30*time.Second || cfg.KlineFreshness.MaxSubjectsPerAlert != 20 ||
		cfg.KlineFreshness.InventoryRefreshInterval != time.Minute || cfg.KlineFreshness.InventoryPageSize != 100 ||
		cfg.KlineFreshness.InventoryMaxEntries != 1000 || cfg.KlineFreshness.StaleAfter != 5*time.Minute ||
		!reflect.DeepEqual(cfg.KlineFreshness.SpaceIDs, []string{"crypto", "stockcn"}) {
		t.Fatalf("kline freshness defaults = %+v", cfg.KlineFreshness)
	}
	cfg.SysDeploy.Enabled = false
	cfg.KlineFreshness.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid kline freshness config rejected: %v", err)
	}

	cases := []struct {
		name string
		edit func(*Config)
	}{
		{"refresh interval too short", func(c *Config) { c.KlineFreshness.InventoryRefreshInterval = 0 }},
		{"empty Space ID", func(c *Config) { c.KlineFreshness.SpaceIDs = []string{""} }},
		{"duplicate Space ID", func(c *Config) { c.KlineFreshness.SpaceIDs = []string{"crypto", " crypto "} }},
		{"refresh interval unbounded", func(c *Config) { c.KlineFreshness.InventoryRefreshInterval = 11 * time.Minute }},
		{"page size out of range", func(c *Config) { c.KlineFreshness.InventoryPageSize = 101 }},
		{"inventory cap out of range", func(c *Config) { c.KlineFreshness.InventoryMaxEntries = 1001 }},
		{"stale after too short", func(c *Config) { c.KlineFreshness.StaleAfter = 30 * time.Second }},
		{"subjects out of range", func(c *Config) { c.KlineFreshness.MaxSubjectsPerAlert = 101 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			copy := *cfg
			test.edit(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestMonitorConfigRejectsUnknownKlineFields(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "secret")
	_, err := Load(writeConfig(t, "kline_freshness:\n  enabled: true\n  typo_interval: 30s\n"))
	if err == nil {
		t.Fatal("Load() accepted unknown kline freshness field")
	}
}

func TestMonitorConfigValidatesMarketHealthThresholds(t *testing.T) {
	cfg := Default()
	cfg.SysDeploy.Enabled = false
	cfg.MarketCanary.Enabled = true
	cfg.MarketCanary.ClosedBarMinCoverage = 1.01
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "closed_bar_min_coverage") {
		t.Fatalf("Validate() error = %v, want closed_bar_min_coverage error", err)
	}

	cfg = Default()
	cfg.SysDeploy.Enabled = false
	cfg.MarketHealth.TimerCoordinationStaleAfter = -time.Second
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "timer_coordination_stale_after") {
		t.Fatalf("Validate() error = %v, want timer coordination threshold error", err)
	}

	cfg = Default()
	cfg.SysDeploy.Enabled = false
	cfg.MarketHealth.LowCapacityHeadroom = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "low_capacity_headroom") {
		t.Fatalf("Validate() error = %v, want low capacity headroom error", err)
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
