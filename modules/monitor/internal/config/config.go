// Package config loads moox-monitor process configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Database      DatabaseConfig      `yaml:"database"`
	Health        HealthConfig        `yaml:"health"`
	HealthAuth    HealthAuthConfig    `yaml:"health_auth"`
	Instance      InstanceConfig      `yaml:"instance"`
	Scheduler     SchedulerConfig     `yaml:"scheduler"`
	SysDeploy     SysDeployConfig     `yaml:"sysdeploy"`
	Alert         AlertConfig         `yaml:"alert"`
	Observability ObservabilityConfig `yaml:"observability"`
	Metrics       MetricsConfig       `yaml:"metrics"`
	MarketCanary  MarketCanaryConfig  `yaml:"market_canary"`
}

type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

type HealthConfig struct {
	Addr string `yaml:"addr"`
}

type HealthAuthConfig struct {
	Version   string `yaml:"version"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

type InstanceConfig struct {
	InstanceID string `yaml:"instance_id"`
}

type SchedulerConfig struct {
	ResultRetentionDays int `yaml:"result_retention_days"`
	MaxConcurrency      int `yaml:"max_concurrency"`
}

type SysDeployConfig struct {
	Enabled     bool              `yaml:"enabled"`
	Target      string            `yaml:"target"`
	ServiceAuth ServiceAuthConfig `yaml:"service_auth"`
}

type ServiceAuthConfig struct {
	KeyID      string `yaml:"key_id"`
	SecretKey  string `yaml:"secret_key"`
	TargetNode string `yaml:"target_node"`
	CAFile     string `yaml:"ca_file"`
}

type AlertConfig struct {
	SendTimeoutSeconds int `yaml:"send_timeout_seconds"`
}

type MetricsConfig struct {
	Enabled                 bool                 `yaml:"enabled"`
	DatasetHealthPolicyPath string               `yaml:"dataset_health_policy_path"`
	NoDataIntervals         int                  `yaml:"no_data_intervals"`
	Storage                 MetricsStorageConfig `yaml:"storage"`
	HostStorage             HostStorageConfig    `yaml:"host_storage"`
}

type ObservabilityConfig struct {
	Enabled                    bool     `yaml:"enabled"`
	EventBusURLs               []string `yaml:"eventbus_urls"`
	CredentialFile             string   `yaml:"credential_file"`
	BalanceDifferenceThreshold float64  `yaml:"balance_difference_threshold"`
}

type MarketCanaryConfig struct {
	Enabled         bool                  `yaml:"enabled"`
	Freshness       time.Duration         `yaml:"freshness"`
	ReturnThreshold float64               `yaml:"return_threshold"`
	Subjects        []MarketCanarySubject `yaml:"subjects"`
}

type MarketCanarySubject struct {
	SpaceID   string  `yaml:"space_id"`
	DatasetID string  `yaml:"dataset_id"`
	Symbol    string  `yaml:"symbol"`
	Frequency string  `yaml:"frequency"`
	SeriesTag *string `yaml:"series_tag"`
}

type MetricsStorageConfig struct {
	GatewayTarget              string        `yaml:"gateway_target"`
	GatewayNodeID              string        `yaml:"gateway_node_id"`
	KeyID                      string        `yaml:"key_id"`
	HMACKeyFile                string        `yaml:"hmac_key_file"`
	SpaceID                    string        `yaml:"space_id"`
	DatasetID                  string        `yaml:"dataset_id"`
	Frequency                  string        `yaml:"frequency"`
	MetadataValidationInterval time.Duration `yaml:"metadata_validation_interval"`
	WriteBatchSize             int           `yaml:"write_batch_size"`
}

// HostStorageConfig controls the direct Storage path for host snapshots.
type HostStorageConfig struct {
	Enabled                 bool          `yaml:"enabled"`
	GatewayTarget           string        `yaml:"gateway_target"`
	GatewayNodeID           string        `yaml:"gateway_node_id"`
	KeyID                   string        `yaml:"key_id"`
	HMACKeyFile             string        `yaml:"hmac_key_file"`
	SpaceID                 string        `yaml:"space_id"`
	Frequency               string        `yaml:"frequency"`
	WriteTimeout            time.Duration `yaml:"write_timeout"`
	ReadLimit               int           `yaml:"read_limit"`
	MetadataRefreshInterval time.Duration `yaml:"metadata_refresh_interval"`
	RuleRefreshInterval     time.Duration `yaml:"rule_refresh_interval"`
	ResourceDatasetID       string        `yaml:"resource_dataset_id"`
	FilesystemDatasetID     string        `yaml:"filesystem_dataset_id"`
	DiskDatasetID           string        `yaml:"disk_dataset_id"`
	NetworkDatasetID        string        `yaml:"network_dataset_id"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/monitor/monitor.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Health: HealthConfig{
			Addr: ":11409",
		},
		HealthAuth: HealthAuthConfig{Version: "moox-health-v1"},
		Instance: InstanceConfig{
			InstanceID: defaultInstanceID(),
		},
		Scheduler: SchedulerConfig{
			ResultRetentionDays: 14,
			MaxConcurrency:      16,
		},
		SysDeploy: SysDeployConfig{
			Enabled:     true,
			Target:      "ip://127.0.0.1:11109",
			ServiceAuth: ServiceAuthConfig{},
		},
		Alert: AlertConfig{
			SendTimeoutSeconds: 10,
		},
		Observability: ObservabilityConfig{Enabled: true, EventBusURLs: []string{"nats://127.0.0.1:4222"}, BalanceDifferenceThreshold: 0.05},
		MarketCanary:  MarketCanaryConfig{Enabled: true, Freshness: 3 * time.Minute, ReturnThreshold: 0.05, Subjects: []MarketCanarySubject{{SpaceID: "crypto_market", DatasetID: "binance_spot_kline_1m", Symbol: "BTC-USDT", Frequency: "1m", SeriesTag: stringPointer("venue:binance")}}},
		Metrics:       MetricsConfig{Enabled: true, DatasetHealthPolicyPath: "../../examples/setup/default/dataset-health-policy.yaml", NoDataIntervals: 2, Storage: MetricsStorageConfig{GatewayTarget: "ip://127.0.0.1:11003", KeyID: "monitor", SpaceID: "moox_system", DatasetID: "moox_service_metrics", Frequency: "30s", MetadataValidationInterval: 30 * time.Second, WriteBatchSize: 1000}, HostStorage: HostStorageConfig{Enabled: true, GatewayTarget: "ip://127.0.0.1:11003", KeyID: "monitor", SpaceID: "moox_system", Frequency: "1m", WriteTimeout: 5 * time.Second, ReadLimit: 500, MetadataRefreshInterval: time.Minute, RuleRefreshInterval: 30 * time.Second, ResourceDatasetID: "host_resource_v1", FilesystemDatasetID: "host_fs_v1", DiskDatasetID: "host_disk_v1", NetworkDatasetID: "host_net_v1"}},
	}
}

func (c *Config) applyDefaults() {
	defaults := Default()
	if c.Database.Type == "" {
		c.Database.Type = defaults.Database.Type
	}
	if c.Database.Path == "" {
		c.Database.Path = defaults.Database.Path
	}
	if c.Database.MaxIdleConns == 0 {
		c.Database.MaxIdleConns = defaults.Database.MaxIdleConns
	}
	if c.Database.MaxOpenConns == 0 {
		c.Database.MaxOpenConns = defaults.Database.MaxOpenConns
	}
	if c.Database.ConnMaxLifetime == 0 {
		c.Database.ConnMaxLifetime = defaults.Database.ConnMaxLifetime
	}
	if c.Database.ConnMaxIdleTime == 0 {
		c.Database.ConnMaxIdleTime = defaults.Database.ConnMaxIdleTime
	}
	if c.Health.Addr == "" {
		c.Health.Addr = defaults.Health.Addr
	}
	if c.HealthAuth.Version == "" {
		c.HealthAuth.Version = defaults.HealthAuth.Version
	}
	if c.Instance.InstanceID == "" {
		c.Instance.InstanceID = defaults.Instance.InstanceID
	}
	if c.Scheduler.ResultRetentionDays == 0 {
		c.Scheduler.ResultRetentionDays = defaults.Scheduler.ResultRetentionDays
	}
	if c.Scheduler.MaxConcurrency == 0 {
		c.Scheduler.MaxConcurrency = defaults.Scheduler.MaxConcurrency
	}
	if c.SysDeploy.Target == "" {
		c.SysDeploy.Target = defaults.SysDeploy.Target
	}
	if c.Alert.SendTimeoutSeconds == 0 {
		c.Alert.SendTimeoutSeconds = defaults.Alert.SendTimeoutSeconds
	}
	metricsDefaults := Default().Metrics
	observabilityDefaults := Default().Observability
	if len(c.Observability.EventBusURLs) == 0 {
		c.Observability.EventBusURLs = observabilityDefaults.EventBusURLs
	}
	if c.Observability.BalanceDifferenceThreshold == 0 {
		c.Observability.BalanceDifferenceThreshold = observabilityDefaults.BalanceDifferenceThreshold
	}
	canaryDefaults := Default().MarketCanary
	if c.MarketCanary.Freshness == 0 {
		c.MarketCanary.Freshness = canaryDefaults.Freshness
	}
	if c.MarketCanary.ReturnThreshold == 0 {
		c.MarketCanary.ReturnThreshold = canaryDefaults.ReturnThreshold
	}
	if len(c.MarketCanary.Subjects) == 0 {
		c.MarketCanary.Subjects = canaryDefaults.Subjects
	}
	if c.Metrics.DatasetHealthPolicyPath == "" {
		c.Metrics.DatasetHealthPolicyPath = metricsDefaults.DatasetHealthPolicyPath
	}
	if c.Metrics.NoDataIntervals == 0 {
		c.Metrics.NoDataIntervals = metricsDefaults.NoDataIntervals
	}
	if c.Metrics.Storage.GatewayTarget == "" {
		c.Metrics.Storage.GatewayTarget = metricsDefaults.Storage.GatewayTarget
	}
	if c.Metrics.Storage.KeyID == "" {
		c.Metrics.Storage.KeyID = metricsDefaults.Storage.KeyID
	}
	if c.Metrics.Storage.SpaceID == "" {
		c.Metrics.Storage.SpaceID = metricsDefaults.Storage.SpaceID
	}
	if c.Metrics.Storage.DatasetID == "" {
		c.Metrics.Storage.DatasetID = metricsDefaults.Storage.DatasetID
	}
	if c.Metrics.Storage.Frequency == "" {
		c.Metrics.Storage.Frequency = metricsDefaults.Storage.Frequency
	}
	if c.Metrics.Storage.MetadataValidationInterval == 0 {
		c.Metrics.Storage.MetadataValidationInterval = metricsDefaults.Storage.MetadataValidationInterval
	}
	if c.Metrics.Storage.WriteBatchSize == 0 {
		c.Metrics.Storage.WriteBatchSize = metricsDefaults.Storage.WriteBatchSize
	}
	if c.Metrics.HostStorage.GatewayTarget == "" {
		c.Metrics.HostStorage.GatewayTarget = metricsDefaults.HostStorage.GatewayTarget
	}
	if c.Metrics.HostStorage.KeyID == "" {
		c.Metrics.HostStorage.KeyID = metricsDefaults.HostStorage.KeyID
	}
	if c.Metrics.HostStorage.SpaceID == "" {
		c.Metrics.HostStorage.SpaceID = metricsDefaults.HostStorage.SpaceID
	}
	if c.Metrics.HostStorage.Frequency == "" {
		c.Metrics.HostStorage.Frequency = metricsDefaults.HostStorage.Frequency
	}
	if c.Metrics.HostStorage.WriteTimeout == 0 {
		c.Metrics.HostStorage.WriteTimeout = metricsDefaults.HostStorage.WriteTimeout
	}
	if c.Metrics.HostStorage.ReadLimit == 0 {
		c.Metrics.HostStorage.ReadLimit = metricsDefaults.HostStorage.ReadLimit
	}
	if c.Metrics.HostStorage.MetadataRefreshInterval == 0 {
		c.Metrics.HostStorage.MetadataRefreshInterval = metricsDefaults.HostStorage.MetadataRefreshInterval
	}
	if c.Metrics.HostStorage.RuleRefreshInterval == 0 {
		c.Metrics.HostStorage.RuleRefreshInterval = metricsDefaults.HostStorage.RuleRefreshInterval
	}
	if c.Metrics.HostStorage.ResourceDatasetID == "" {
		c.Metrics.HostStorage.ResourceDatasetID = metricsDefaults.HostStorage.ResourceDatasetID
	}
	if c.Metrics.HostStorage.FilesystemDatasetID == "" {
		c.Metrics.HostStorage.FilesystemDatasetID = metricsDefaults.HostStorage.FilesystemDatasetID
	}
	if c.Metrics.HostStorage.DiskDatasetID == "" {
		c.Metrics.HostStorage.DiskDatasetID = metricsDefaults.HostStorage.DiskDatasetID
	}
	if c.Metrics.HostStorage.NetworkDatasetID == "" {
		c.Metrics.HostStorage.NetworkDatasetID = metricsDefaults.HostStorage.NetworkDatasetID
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_MONITOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_MONITOR_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_VERSION")); v != "" {
		c.HealthAuth.Version = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY")); v != "" {
		c.HealthAuth.AccessKey = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")); v != "" {
		c.HealthAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_MONITOR_INSTANCE_ID"); v != "" {
		c.Instance.InstanceID = v
	}
	if v := os.Getenv("MOOX_MONITOR_SYSDEPLOY_TARGET"); v != "" {
		c.SysDeploy.Target = v
	}
	if v := firstEnv("MOOX_OBSERVABILITY_EVENTBUS_URL", "MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_URL"); v != "" {
		c.Observability.EventBusURLs = strings.Split(v, ",")
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_OBSERVABILITY_CREDENTIAL_FILE")); v != "" {
		c.Observability.CredentialFile = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_DATASET_HEALTH_POLICY")); v != "" {
		c.Metrics.DatasetHealthPolicyPath = v
	}
	if v := os.Getenv("MOOX_GATEWAY_NODE_ID"); v != "" {
		c.SysDeploy.ServiceAuth.TargetNode = v
		c.Metrics.Storage.GatewayNodeID = v
		c.Metrics.HostStorage.GatewayNodeID = v
	}
	// SysDeploy authenticates through the local control gateway, while Storage
	// may live behind a different gateway node. Keep these overrides separate.
	if v := strings.TrimSpace(os.Getenv("MOOX_MONITOR_STORAGE_GATEWAY_TARGET")); v != "" {
		c.Metrics.Storage.GatewayTarget = v
		c.Metrics.HostStorage.GatewayTarget = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_MONITOR_STORAGE_GATEWAY_NODE_ID")); v != "" {
		c.Metrics.Storage.GatewayNodeID = v
		c.Metrics.HostStorage.GatewayNodeID = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); v != "" {
		c.SysDeploy.ServiceAuth.KeyID = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_FILE"); v != "" {
		c.SysDeploy.ServiceAuth.CAFile = v
	}
}

func (c *Config) Validate() error {
	if c.Instance.InstanceID == "" {
		return fmt.Errorf("instance.instance_id must not be empty")
	}
	if c.Alert.SendTimeoutSeconds <= 0 || c.Alert.SendTimeoutSeconds > 300 {
		return fmt.Errorf("alert.send_timeout_seconds must be between 1 and 300")
	}
	if c.Observability.Enabled {
		if len(c.Observability.EventBusURLs) == 0 {
			return fmt.Errorf("observability.eventbus_urls must not be empty")
		}
		for _, url := range c.Observability.EventBusURLs {
			if strings.TrimSpace(url) == "" {
				return fmt.Errorf("observability.eventbus_urls must not contain empty values")
			}
		}
		if c.Observability.BalanceDifferenceThreshold <= 0 || c.Observability.BalanceDifferenceThreshold > 1 {
			return fmt.Errorf("observability.balance_difference_threshold must be in (0, 1]")
		}
	}
	if c.MarketCanary.Enabled {
		if c.MarketCanary.Freshness <= 0 || c.MarketCanary.ReturnThreshold <= 0 {
			return fmt.Errorf("market_canary price threshold and freshness must be positive")
		}
		if len(c.MarketCanary.Subjects) == 0 || len(c.MarketCanary.Subjects) > 8 {
			return fmt.Errorf("market_canary subjects must contain between 1 and 8 entries")
		}
		for _, subject := range c.MarketCanary.Subjects {
			if strings.TrimSpace(subject.SpaceID) == "" || strings.TrimSpace(subject.DatasetID) == "" ||
				strings.TrimSpace(subject.Symbol) == "" || strings.TrimSpace(subject.Frequency) == "" {
				return fmt.Errorf("market_canary subject requires space_id, dataset_id, symbol, and frequency")
			}
			if subject.SeriesTag == nil {
				return fmt.Errorf("market_canary subject requires series_tag (use an explicit empty value for the default series)")
			}
		}
	}
	if c.SysDeploy.Enabled && (strings.TrimSpace(c.HealthAuth.Version) == "" || strings.TrimSpace(c.HealthAuth.AccessKey) == "" || strings.TrimSpace(c.HealthAuth.SecretKey) == "") {
		return fmt.Errorf("health_auth version, access_key, and secret_key must not be empty when sysdeploy monitoring is enabled")
	}
	if c.Metrics.HostStorage.Enabled {
		h := c.Metrics.HostStorage
		if h.SpaceID != "moox_system" {
			return fmt.Errorf("metrics.host_storage.space_id must be moox_system")
		}
		if h.Frequency != "1m" {
			return fmt.Errorf("metrics.host_storage.frequency must be 1m")
		}
		if h.ReadLimit <= 0 || h.ReadLimit > 500 {
			return fmt.Errorf("metrics.host_storage.read_limit must be between 1 and 500")
		}
		for name, value := range map[string]string{"resource_dataset_id": h.ResourceDatasetID, "filesystem_dataset_id": h.FilesystemDatasetID, "disk_dataset_id": h.DiskDatasetID, "network_dataset_id": h.NetworkDatasetID} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("metrics.host_storage.%s must not be empty", name)
			}
		}
	}
	for name, values := range map[string][2]string{
		"metrics.storage":      {c.Metrics.Storage.KeyID, c.Metrics.Storage.HMACKeyFile},
		"metrics.host_storage": {c.Metrics.HostStorage.KeyID, c.Metrics.HostStorage.HMACKeyFile},
	} {
		if strings.TrimSpace(values[1]) != "" {
			if _, err := gatewayauth.CredentialsFromKeyFile(values[0], values[1]); err != nil {
				return fmt.Errorf("%s hmac credentials: %w", name, err)
			}
		}
	}
	return nil
}

func stringPointer(value string) *string {
	return &value
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "monitor"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
