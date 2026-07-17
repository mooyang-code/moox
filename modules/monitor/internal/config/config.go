// Package config loads moox-monitor process configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Database   DatabaseConfig   `yaml:"database"`
	Health     HealthConfig     `yaml:"health"`
	HealthAuth HealthAuthConfig `yaml:"health_auth"`
	Instance   InstanceConfig   `yaml:"instance"`
	Scheduler  SchedulerConfig  `yaml:"scheduler"`
	SysDeploy  SysDeployConfig  `yaml:"sysdeploy"`
	Peer       PeerConfig       `yaml:"peer"`
	Alert      AlertConfig      `yaml:"alert"`
	Metrics    MetricsConfig    `yaml:"metrics"`
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
	BaseURL    string `yaml:"base_url"`
}

type SchedulerConfig struct {
	ReloadIntervalSeconds int `yaml:"reload_interval_seconds"`
	ResultRetentionDays   int `yaml:"result_retention_days"`
	MaxConcurrency        int `yaml:"max_concurrency"`
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

type PeerConfig struct {
	Enabled             bool              `yaml:"enabled"`
	PullIntervalSeconds int               `yaml:"pull_interval_seconds"`
	TimeoutSeconds      int               `yaml:"timeout_seconds"`
	ServiceAuth         ServiceAuthConfig `yaml:"service_auth"`
	Peers               []PeerEntry       `yaml:"peers"`
}

type PeerEntry struct {
	InstanceID string `yaml:"instance_id"`
	GatewayURL string `yaml:"gateway_url"`
	NodeID     string `yaml:"node_id"`
}

type AlertConfig struct {
	SendTimeoutSeconds int `yaml:"send_timeout_seconds"`
}

type MetricsConfig struct {
	Enabled                bool                 `yaml:"enabled"`
	EventBusURL            string               `yaml:"eventbus_url"`
	EventBusCredentialFile string               `yaml:"eventbus_credential_file"`
	Stream                 string               `yaml:"stream"`
	Topic                  string               `yaml:"topic"`
	Consumer               string               `yaml:"consumer"`
	FetchBatchSize         int                  `yaml:"fetch_batch_size"`
	FetchMaxWait           time.Duration        `yaml:"fetch_max_wait"`
	AckWait                time.Duration        `yaml:"ack_wait"`
	MaxAckPending          int                  `yaml:"max_ack_pending"`
	NoDataIntervals        int                  `yaml:"no_data_intervals"`
	Storage                MetricsStorageConfig `yaml:"storage"`
	HostStorage            HostStorageConfig    `yaml:"host_storage"`
}
type MetricsStorageConfig struct {
	AccessTarget               string        `yaml:"access_target"`
	MetadataTarget             string        `yaml:"metadata_target"`
	SpaceID                    string        `yaml:"space_id"`
	DatasetID                  string        `yaml:"dataset_id"`
	Frequency                  string        `yaml:"frequency"`
	MetadataValidationInterval time.Duration `yaml:"metadata_validation_interval"`
	WriteBatchSize             int           `yaml:"write_batch_size"`
}

// HostStorageConfig controls the direct Storage path for host snapshots.
// Host samples are intentionally best-effort and have a short retention.
type HostStorageConfig struct {
	Enabled                 bool          `yaml:"enabled"`
	AccessTarget            string        `yaml:"access_target"`
	MetadataTarget          string        `yaml:"metadata_target"`
	SpaceID                 string        `yaml:"space_id"`
	Frequency               string        `yaml:"frequency"`
	Retention               time.Duration `yaml:"retention"`
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
			BaseURL:    "http://127.0.0.1:11409",
		},
		Scheduler: SchedulerConfig{
			ReloadIntervalSeconds: 30,
			ResultRetentionDays:   14,
			MaxConcurrency:        16,
		},
		SysDeploy: SysDeployConfig{
			Enabled:     true,
			Target:      "ip://127.0.0.1:11109",
			ServiceAuth: ServiceAuthConfig{},
		},
		Peer: PeerConfig{
			Enabled:             true,
			PullIntervalSeconds: 10,
			TimeoutSeconds:      5,
		},
		Alert: AlertConfig{
			SendTimeoutSeconds: 10,
		},
		Metrics: MetricsConfig{Enabled: true, EventBusURL: "nats://127.0.0.1:4222", Stream: "MOOX_METRICS", Topic: "moox.metrics.snapshot.reported.v1", Consumer: "monitor_metrics_ingest_v1", FetchBatchSize: 64, FetchMaxWait: time.Second, AckWait: time.Minute, MaxAckPending: 256, NoDataIntervals: 2, Storage: MetricsStorageConfig{AccessTarget: "ip://127.0.0.1:20102", MetadataTarget: "ip://127.0.0.1:20100", SpaceID: "moox_system", DatasetID: "moox_service_metrics", Frequency: "30s", MetadataValidationInterval: 30 * time.Second, WriteBatchSize: 1000}, HostStorage: HostStorageConfig{Enabled: true, AccessTarget: "ip://127.0.0.1:20102", MetadataTarget: "ip://127.0.0.1:20100", SpaceID: "moox_system", Frequency: "1m", Retention: 72 * time.Hour, WriteTimeout: 5 * time.Second, ReadLimit: 500, MetadataRefreshInterval: time.Minute, RuleRefreshInterval: 30 * time.Second, ResourceDatasetID: "host_resource_v1", FilesystemDatasetID: "host_fs_v1", DiskDatasetID: "host_disk_v1", NetworkDatasetID: "host_net_v1"}},
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
	if c.Instance.BaseURL == "" {
		c.Instance.BaseURL = defaults.Instance.BaseURL
	}
	if c.Scheduler.ReloadIntervalSeconds == 0 {
		c.Scheduler.ReloadIntervalSeconds = defaults.Scheduler.ReloadIntervalSeconds
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
	if c.Peer.PullIntervalSeconds == 0 {
		c.Peer.PullIntervalSeconds = defaults.Peer.PullIntervalSeconds
	}
	if c.Peer.TimeoutSeconds == 0 {
		c.Peer.TimeoutSeconds = defaults.Peer.TimeoutSeconds
	}
	if c.Alert.SendTimeoutSeconds == 0 {
		c.Alert.SendTimeoutSeconds = defaults.Alert.SendTimeoutSeconds
	}
	metricsDefaults := Default().Metrics
	if c.Metrics.EventBusURL == "" {
		c.Metrics.EventBusURL = metricsDefaults.EventBusURL
	}
	if c.Metrics.Stream == "" {
		c.Metrics.Stream = metricsDefaults.Stream
	}
	if c.Metrics.Topic == "" {
		c.Metrics.Topic = metricsDefaults.Topic
	}
	if c.Metrics.Consumer == "" {
		c.Metrics.Consumer = metricsDefaults.Consumer
	}
	if c.Metrics.FetchBatchSize == 0 {
		c.Metrics.FetchBatchSize = metricsDefaults.FetchBatchSize
	}
	if c.Metrics.FetchMaxWait == 0 {
		c.Metrics.FetchMaxWait = metricsDefaults.FetchMaxWait
	}
	if c.Metrics.AckWait == 0 {
		c.Metrics.AckWait = metricsDefaults.AckWait
	}
	if c.Metrics.MaxAckPending == 0 {
		c.Metrics.MaxAckPending = metricsDefaults.MaxAckPending
	}
	if c.Metrics.NoDataIntervals == 0 {
		c.Metrics.NoDataIntervals = metricsDefaults.NoDataIntervals
	}
	if c.Metrics.Storage.AccessTarget == "" {
		c.Metrics.Storage.AccessTarget = metricsDefaults.Storage.AccessTarget
	}
	if c.Metrics.Storage.MetadataTarget == "" {
		c.Metrics.Storage.MetadataTarget = metricsDefaults.Storage.MetadataTarget
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
	if c.Metrics.HostStorage.AccessTarget == "" {
		c.Metrics.HostStorage.AccessTarget = metricsDefaults.HostStorage.AccessTarget
	}
	if c.Metrics.HostStorage.MetadataTarget == "" {
		c.Metrics.HostStorage.MetadataTarget = metricsDefaults.HostStorage.MetadataTarget
	}
	if c.Metrics.HostStorage.SpaceID == "" {
		c.Metrics.HostStorage.SpaceID = metricsDefaults.HostStorage.SpaceID
	}
	if c.Metrics.HostStorage.Frequency == "" {
		c.Metrics.HostStorage.Frequency = metricsDefaults.HostStorage.Frequency
	}
	if c.Metrics.HostStorage.Retention == 0 {
		c.Metrics.HostStorage.Retention = metricsDefaults.HostStorage.Retention
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
	if v := os.Getenv("MOOX_MONITOR_BASE_URL"); v != "" {
		c.Instance.BaseURL = v
	}
	if v := os.Getenv("MOOX_MONITOR_SYSDEPLOY_TARGET"); v != "" {
		c.SysDeploy.Target = v
	}
	if v := firstEnv("MOOX_METRICS_EVENTBUS_URL", "MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_URL"); v != "" {
		c.Metrics.EventBusURL = v
	}
	if v := os.Getenv("MOOX_GATEWAY_NODE_ID"); v != "" {
		c.SysDeploy.ServiceAuth.TargetNode = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); v != "" {
		c.SysDeploy.ServiceAuth.KeyID = v
		c.Peer.ServiceAuth.KeyID = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.SecretKey = v
		c.Peer.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_FILE"); v != "" {
		c.SysDeploy.ServiceAuth.CAFile = v
		c.Peer.ServiceAuth.CAFile = v
	}
}

func (c *Config) Validate() error {
	if c.Instance.InstanceID == "" {
		return fmt.Errorf("instance.instance_id must not be empty")
	}
	if c.Instance.BaseURL == "" {
		return fmt.Errorf("instance.base_url must not be empty")
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
		if h.Retention < time.Hour || h.Retention > 72*time.Hour {
			return fmt.Errorf("metrics.host_storage.retention must be between 1h and 72h")
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
	for i, peer := range c.Peer.Peers {
		if peer.InstanceID == "" || peer.GatewayURL == "" || peer.NodeID == "" {
			return fmt.Errorf("peer.peers[%d] requires instance_id, gateway_url, and node_id", i)
		}
	}
	if c.Peer.Enabled && len(c.Peer.Peers) > 0 && (strings.TrimSpace(c.Peer.ServiceAuth.KeyID) == "" || strings.TrimSpace(c.Peer.ServiceAuth.SecretKey) == "") {
		return fmt.Errorf("peer.service_auth key_id and secret_key must not be empty when peers are configured")
	}
	if c.Peer.Enabled && len(c.Peer.Peers) > 0 && (c.Peer.PullIntervalSeconds <= 0 || c.Peer.TimeoutSeconds <= 0) {
		return fmt.Errorf("peer pull_interval_seconds and timeout_seconds must be positive when peers are configured")
	}
	return nil
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
